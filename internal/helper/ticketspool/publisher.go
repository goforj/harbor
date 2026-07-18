package ticketspool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

const (
	referenceEntropyBytes  = 32
	stagingEntropyBytes    = 32
	maximumPublishAttempts = 128
	privateFileMode        = 0o600
	stagingPrefix          = ".harbor-ticket-"
	stagingSuffix          = ".pending"
)

var (
	// ErrCollisionExhausted means every bounded publication attempt found an existing immutable reference.
	ErrCollisionExhausted = errors.New("helper ticket reference collisions exhausted")
	// ErrDurabilityUncertain means the final reference was published but its persistence barrier failed.
	ErrDurabilityUncertain = errors.New("helper ticket publication durability is uncertain")
	// ErrUnsafePath means the pending-ticket directory or a staging file crossed its private security boundary.
	ErrUnsafePath = errors.New("unsafe helper ticket spool path")
)

// fileOperations keeps destructive storage boundaries replaceable in focused fault tests.
type fileOperations struct {
	create        func(*os.Root, *os.File, string, string) (*os.File, error)
	reopen        func(*os.Root, *os.File, string, string) (*os.File, error)
	write         func(io.Writer, []byte) error
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	rename        func(*os.Root, *os.File, *os.File, string, string) (bool, error)
	syncDirectory func(*os.File) error
	remove        func(*os.Root, string) error
}

// publisherDependencies contains authority inputs that production fixes and tests replace explicitly.
type publisherDependencies struct {
	clock           helper.Clock
	referenceRandom func([]byte) error
	stagingRandom   func([]byte) error
	files           fileOperations
}

// Publisher owns a retained handle to the fixed pending-ticket directory.
type Publisher struct {
	path         string
	root         *os.Root
	directory    *os.File
	dependencies publisherDependencies
	stateMu      sync.RWMutex
	randomMu     sync.Mutex
	closed       bool
}

// OpenDefault opens the installer-provisioned pending-ticket directory for daemon publication.
func OpenDefault() (*Publisher, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve helper ticket spool: %w", err)
	}
	return open(paths.PendingDirectory, defaultDependencies())
}

// open retains and revalidates one pre-provisioned test or production directory without repairing it.
func open(path string, dependencies publisherDependencies) (*Publisher, error) {
	if err := validateDependencies(dependencies); err != nil {
		return nil, err
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("open helper ticket spool: path %q is not absolute and canonical", path)
	}

	initial, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("open helper ticket spool %q: %w", path, err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.IsDir() {
		return nil, fmt.Errorf("%w: helper ticket spool %q is not a direct directory", ErrUnsafePath, path)
	}
	if err := validatePlatformDirectory(path, initial); err != nil {
		return nil, fmt.Errorf("%w: validate helper ticket spool %q: %v", ErrUnsafePath, path, err)
	}

	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open retained helper ticket spool %q: %w", path, err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open retained helper ticket spool directory %q: %w", path, err),
			root.Close(),
		)
	}
	opened, statErr := directory.Stat()
	var boundaryErr error
	sameDirectory := false
	if statErr == nil {
		boundaryErr = validatePlatformObject(directory, opened, true)
		sameDirectory = os.SameFile(initial, opened)
	}
	if statErr != nil || boundaryErr != nil || !sameDirectory {
		return nil, errors.Join(
			fmt.Errorf("%w: helper ticket spool %q changed while opening", ErrUnsafePath, path),
			statErr,
			boundaryErr,
			directory.Close(),
			root.Close(),
		)
	}

	return &Publisher{
		path:         path,
		root:         root,
		directory:    directory,
		dependencies: dependencies,
	}, nil
}

// Close releases retained filesystem handles without removing pending tickets.
func (publisher *Publisher) Close() error {
	publisher.stateMu.Lock()
	defer publisher.stateMu.Unlock()
	if publisher.closed {
		return nil
	}
	publisher.closed = true
	return errors.Join(publisher.directory.Close(), publisher.root.Close())
}

// Publish signs one ticket and commits it under a fresh immutable reference.
// A non-empty reference returned with ErrDurabilityUncertain identifies the possibly durable final file.
func (publisher *Publisher) Publish(ctx context.Context, ticket helper.Ticket, privateKey ed25519.PrivateKey) (helper.TicketReference, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	publisher.stateMu.RLock()
	defer publisher.stateMu.RUnlock()
	if publisher.closed {
		return "", errors.New("publish helper ticket: publisher is closed")
	}
	if err := publisher.validateRoot(); err != nil {
		return "", err
	}

	now := publisher.dependencies.clock.Now()
	envelope, err := ticketauth.Sign(ticket, privateKey, now)
	if err != nil {
		return "", err
	}
	encoded, err := ticketauth.Encode(envelope)
	if err != nil {
		return "", err
	}

	for range maximumPublishAttempts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		reference, err := publisher.randomReference()
		if err != nil {
			return "", fmt.Errorf("generate helper ticket reference: %w", err)
		}
		committed, collision, err := publisher.publishAttempt(ctx, reference, encoded)
		if committed {
			return reference, err
		}
		if err != nil {
			return "", err
		}
		if !collision {
			return "", errors.New("publish helper ticket: publication ended without a result")
		}
	}
	return "", ErrCollisionExhausted
}

// validateRoot catches permission or object-type drift before each publication begins.
func (publisher *Publisher) validateRoot() error {
	info, err := publisher.directory.Stat()
	if err != nil {
		return fmt.Errorf("validate retained helper ticket spool %q: %w", publisher.path, err)
	}
	if err := validatePlatformObject(publisher.directory, info, true); err != nil {
		return fmt.Errorf("%w: validate retained helper ticket spool %q: %v", ErrUnsafePath, publisher.path, err)
	}
	return nil
}

// randomReference encodes exactly 32 independently generated bytes as lowercase hexadecimal.
func (publisher *Publisher) randomReference() (helper.TicketReference, error) {
	random := make([]byte, referenceEntropyBytes)
	publisher.randomMu.Lock()
	err := publisher.dependencies.referenceRandom(random)
	publisher.randomMu.Unlock()
	if err != nil {
		return "", err
	}
	return helper.TicketReference(hex.EncodeToString(random)), nil
}

// randomStagingName keeps incomplete content outside the valid final-reference namespace.
func (publisher *Publisher) randomStagingName() (string, error) {
	random := make([]byte, stagingEntropyBytes)
	publisher.randomMu.Lock()
	err := publisher.dependencies.stagingRandom(random)
	publisher.randomMu.Unlock()
	if err != nil {
		return "", err
	}
	return stagingPrefix + hex.EncodeToString(random) + stagingSuffix, nil
}

// publishAttempt stages, reopens, validates, and no-replace commits one candidate reference.
func (publisher *Publisher) publishAttempt(ctx context.Context, reference helper.TicketReference, encoded []byte) (committed bool, collision bool, operationErr error) {
	stagingName, staging, err := publisher.createStaging(ctx)
	if err != nil {
		return false, false, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			operationErr = errors.Join(operationErr, ignoreMissing(publisher.dependencies.files.remove(publisher.root, stagingName)))
		}
	}()

	createdInfo, err := staging.Stat()
	if err != nil {
		return false, false, errors.Join(fmt.Errorf("stat staged helper ticket: %w", err), publisher.dependencies.files.closeFile(staging))
	}
	if err := publisher.dependencies.files.write(staging, encoded); err != nil {
		return false, false, errors.Join(fmt.Errorf("write staged helper ticket: %w", err), publisher.dependencies.files.closeFile(staging))
	}
	if err := publisher.dependencies.files.syncFile(staging); err != nil {
		return false, false, errors.Join(fmt.Errorf("sync staged helper ticket: %w", err), publisher.dependencies.files.closeFile(staging))
	}
	if err := publisher.dependencies.files.closeFile(staging); err != nil {
		return false, false, fmt.Errorf("close staged helper ticket: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, false, err
	}

	validated, err := publisher.dependencies.files.reopen(publisher.root, publisher.directory, publisher.path, stagingName)
	if err != nil {
		return false, false, fmt.Errorf("reopen staged helper ticket: %w", err)
	}
	defer func() {
		closeErr := publisher.dependencies.files.closeFile(validated)
		if closeErr != nil && committed {
			closeErr = durabilityUncertain(reference, closeErr)
		}
		operationErr = errors.Join(operationErr, closeErr)
	}()
	if err := validateStagedFile(validated, createdInfo, encoded); err != nil {
		return false, false, fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	if err := publisher.validateRoot(); err != nil {
		return false, false, err
	}
	if err := ctx.Err(); err != nil {
		return false, false, err
	}

	applied, err := publisher.dependencies.files.rename(publisher.root, publisher.directory, validated, stagingName, string(reference))
	if !applied {
		if errors.Is(err, fs.ErrExist) {
			return false, true, nil
		}
		if err == nil {
			err = errors.New("rename reported no applied transition")
		}
		return false, false, fmt.Errorf("commit helper ticket reference %q: %w", reference, err)
	}
	cleanup = false
	if err != nil {
		return true, false, durabilityUncertain(reference, err)
	}
	if err := publisher.dependencies.files.syncDirectory(publisher.directory); err != nil {
		return true, false, durabilityUncertain(reference, err)
	}
	return true, false, nil
}

// createStaging finds one exclusive unpredictable staging name without entering the final namespace.
func (publisher *Publisher) createStaging(ctx context.Context) (string, *os.File, error) {
	for range maximumPublishAttempts {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		name, err := publisher.randomStagingName()
		if err != nil {
			return "", nil, fmt.Errorf("generate helper ticket staging name: %w", err)
		}
		file, err := publisher.dependencies.files.create(publisher.root, publisher.directory, publisher.path, name)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("create staged helper ticket: %w", err)
		}
	}
	return "", nil, errors.New("create staged helper ticket: staging name collisions exhausted")
}

// validateStagedFile proves the reopened object is the exact private file whose canonical bytes were synced.
func validateStagedFile(file *os.File, createdInfo os.FileInfo, expected []byte) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat reopened staged helper ticket: %w", err)
	}
	if !os.SameFile(createdInfo, info) {
		return errors.New("staged helper ticket changed before publication")
	}
	if err := validatePlatformObject(file, info, false); err != nil {
		return err
	}
	if info.Size() != int64(len(expected)) || info.Size() > ticketauth.MaxEnvelopeBytes {
		return fmt.Errorf("staged helper ticket size is %d, want %d", info.Size(), len(expected))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek staged helper ticket: %w", err)
	}
	content, err := io.ReadAll(io.LimitReader(file, ticketauth.MaxEnvelopeBytes+1))
	if err != nil {
		return fmt.Errorf("read staged helper ticket: %w", err)
	}
	if !bytes.Equal(content, expected) {
		return errors.New("staged helper ticket content changed before publication")
	}
	if _, err := ticketauth.Decode(content); err != nil {
		return fmt.Errorf("decode staged helper ticket: %w", err)
	}
	return nil
}

// defaultDependencies fixes production publication to cryptographic entropy and native durable operations.
func defaultDependencies() publisherDependencies {
	return publisherDependencies{
		clock:           helper.SystemClock{},
		referenceRandom: readRandom,
		stagingRandom:   readRandom,
		files: fileOperations{
			create:        createPlatformFile,
			reopen:        reopenPlatformFile,
			write:         writeAll,
			syncFile:      func(file *os.File) error { return file.Sync() },
			closeFile:     func(file *os.File) error { return file.Close() },
			rename:        renamePlatformNoReplace,
			syncDirectory: syncPlatformDirectory,
			remove:        func(root *os.Root, name string) error { return root.Remove(name) },
		},
	}
}

// readRandom fills the requested identity material from the operating system's cryptographic source.
func readRandom(destination []byte) error {
	_, err := io.ReadFull(rand.Reader, destination)
	return err
}

// validateDependencies keeps test seams complete so missing authority checks fail at open time.
func validateDependencies(dependencies publisherDependencies) error {
	if dependencies.clock == nil || dependencies.referenceRandom == nil || dependencies.stagingRandom == nil ||
		dependencies.files.create == nil || dependencies.files.reopen == nil || dependencies.files.write == nil ||
		dependencies.files.syncFile == nil || dependencies.files.closeFile == nil || dependencies.files.rename == nil || dependencies.files.syncDirectory == nil ||
		dependencies.files.remove == nil {
		return errors.New("open helper ticket spool: dependencies are incomplete")
	}
	return nil
}

// writeAll rejects zero-progress writes before incomplete authority can be published.
func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

// durabilityUncertain preserves both the committed reference and the storage-barrier cause.
func durabilityUncertain(reference helper.TicketReference, err error) error {
	return errors.Join(fmt.Errorf("%w for helper ticket reference %q", ErrDurabilityUncertain, reference), err)
}

// ignoreMissing treats successful prior cleanup as equivalent to removing the staging name now.
func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
