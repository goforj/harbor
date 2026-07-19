package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/userpaths"
)

const (
	projectRemovalIntentRecordVersion = 1
	projectRemovalIntentRecordLimit   = 4096
	projectRemovalIntentDirectory     = "project-removal-intents"
	projectRemovalIntentLockFilename  = ".lock"
	projectRemovalIntentFileMode      = 0o600
	projectRemovalIntentDirectoryMode = 0o700
)

// projectRemovalIntentJournal preserves one client-owned intent across independent CLI processes.
type projectRemovalIntentJournal interface {
	// LoadOrCreate returns the durable attempt identity, creating it before any daemon call can commit work.
	LoadOrCreate(context.Context, domain.ProjectID, domain.IntentID, removeIntentFactory) (domain.IntentID, error)
	// Clear removes only the exact attempt whose authoritative terminal output reached the caller.
	Clear(context.Context, domain.ProjectID, domain.IntentID) error
}

// projectRemovalDataDirectory resolves Harbor's owner-local data directory without touching it during wiring.
type projectRemovalDataDirectory func() (string, error)

// filesystemProjectRemovalIntentJournal serializes intent records across goroutines and operating-system processes.
type filesystemProjectRemovalIntentJournal struct {
	dataDirectory             projectRemovalDataDirectory
	afterDirectoryObservation func()
	beforeDirectOpen          func(string)
}

// projectRemovalIntentFilesystem confines every mutable name beneath one retained private directory handle.
type projectRemovalIntentFilesystem struct {
	root             *os.Root
	directory        *os.File
	beforeDirectOpen func(string)
}

// projectRemovalIntentRecord is the bounded, non-secret state required to replay one removal attempt.
type projectRemovalIntentRecord struct {
	// Version prevents a future format from being silently interpreted with older semantics.
	Version int `json:"version"`
	// ProjectID detects path hashing or filesystem corruption before an intent is replayed.
	ProjectID domain.ProjectID `json:"project_id"`
	// IntentID is the client-owned idempotency identity sent to the daemon.
	IntentID domain.IntentID `json:"intent_id"`
}

// projectRemovalIntentTransaction runs while both in-process and interprocess exclusion are held.
type projectRemovalIntentTransaction func(*projectRemovalIntentFilesystem) error

var projectRemovalIntentProcessGate = newProjectRemovalIntentProcessGate()

// newFilesystemProjectRemovalIntentJournal creates the production journal without resolving user paths eagerly.
func newFilesystemProjectRemovalIntentJournal() projectRemovalIntentJournal {
	return &filesystemProjectRemovalIntentJournal{dataDirectory: userpaths.DataDirectory}
}

// newFilesystemProjectRemovalIntentJournalWithDirectory keeps owner-local path failures and persistence testable.
func newFilesystemProjectRemovalIntentJournalWithDirectory(dataDirectory projectRemovalDataDirectory) projectRemovalIntentJournal {
	return &filesystemProjectRemovalIntentJournal{dataDirectory: dataDirectory}
}

// LoadOrCreate reuses a retained intent or durably creates one before the daemon may commit work.
func (journal *filesystemProjectRemovalIntentJournal) LoadOrCreate(
	ctx context.Context,
	projectID domain.ProjectID,
	override domain.IntentID,
	create removeIntentFactory,
) (domain.IntentID, error) {
	var intentID domain.IntentID
	err := journal.withLock(ctx, func(filesystem *projectRemovalIntentFilesystem) error {
		path := projectRemovalIntentPath(projectID)
		record, found, err := filesystem.readRecord(path)
		if err != nil {
			return err
		}
		if found {
			if record.ProjectID != projectID {
				return fmt.Errorf("retained project removal intent belongs to %s instead of %s", record.ProjectID, projectID)
			}
			if override != "" && override != record.IntentID {
				return fmt.Errorf(
					"project %s already retains removal intent %s; retry without --intent or with --intent %s",
					projectID,
					record.IntentID,
					record.IntentID,
				)
			}
			intentID = record.IntentID
			return nil
		}

		intentID = override
		if intentID == "" {
			if create == nil {
				return errors.New("project removal intent factory is required")
			}
			intentID, err = create()
			if err != nil {
				return err
			}
		}
		if err := intentID.Validate(); err != nil {
			return fmt.Errorf("validate generated intent: %w", err)
		}

		record = projectRemovalIntentRecord{
			Version:   projectRemovalIntentRecordVersion,
			ProjectID: projectID,
			IntentID:  intentID,
		}
		return filesystem.writeRecord(path, record)
	})
	if err != nil {
		return "", err
	}
	return intentID, nil
}

// Clear removes only the exact terminal attempt so an older caller cannot erase a newer intent.
func (journal *filesystemProjectRemovalIntentJournal) Clear(
	ctx context.Context,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) error {
	return journal.withLock(ctx, func(filesystem *projectRemovalIntentFilesystem) error {
		path := projectRemovalIntentPath(projectID)
		record, found, err := filesystem.readRecord(path)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if record.ProjectID != projectID || record.IntentID != intentID {
			return fmt.Errorf(
				"retained project removal attempt changed while clearing %s for %s",
				intentID,
				projectID,
			)
		}
		if err := filesystem.root.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove retained project removal intent: %w", err)
		}
		if err := syncProjectRemovalIntentDirectory(filesystem.directory); err != nil {
			return fmt.Errorf("sync project removal intent directory: %w", err)
		}
		return nil
	})
}

// withLock protects one rooted filesystem transaction and makes lock contention cancellation observable.
func (journal *filesystemProjectRemovalIntentJournal) withLock(
	ctx context.Context,
	transaction projectRemovalIntentTransaction,
) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if journal == nil || journal.dataDirectory == nil {
		return errors.New("project removal intent journal is not configured")
	}
	if transaction == nil {
		return errors.New("project removal intent transaction is required")
	}
	if err := acquireProjectRemovalIntentProcessGate(ctx); err != nil {
		return err
	}
	defer releaseProjectRemovalIntentProcessGate()

	filesystem, err := journal.openFilesystem()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, filesystem.Close())
	}()

	lock, err := filesystem.openOrCreateDirectFile(projectRemovalIntentLockFilename, os.O_RDWR)
	if err != nil {
		return fmt.Errorf("open project removal intent lock: %w", err)
	}
	if err := acquireProjectRemovalIntentLock(ctx, lock); err != nil {
		return errors.Join(fmt.Errorf("acquire project removal intent lock: %w", err), lock.Close())
	}
	defer func() {
		err = errors.Join(err, releaseProjectRemovalIntentLock(lock), lock.Close())
	}()
	if err := filesystem.validateCurrent(projectRemovalIntentLockFilename, lock, false); err != nil {
		return fmt.Errorf("validate project removal intent lock: %w", err)
	}

	return transaction(filesystem)
}

// openFilesystem creates one owner-private journal leaf and retains the exact directory it validated.
func (journal *filesystemProjectRemovalIntentJournal) openFilesystem() (*projectRemovalIntentFilesystem, error) {
	base, err := journal.dataDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve Harbor data directory: %w", err)
	}
	if base == "" || !filepath.IsAbs(base) {
		return nil, fmt.Errorf("resolve Harbor data directory: path %q is not absolute", base)
	}
	base = filepath.Clean(base)
	if err := os.MkdirAll(base, projectRemovalIntentDirectoryMode); err != nil {
		return nil, fmt.Errorf("create Harbor data directory: %w", err)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("open Harbor data directory: %w", err)
	}
	defer baseRoot.Close()

	created := false
	if err := baseRoot.Mkdir(projectRemovalIntentDirectory, projectRemovalIntentDirectoryMode); err == nil {
		created = true
	} else if !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create project removal intent directory: %w", err)
	}
	observed, err := baseRoot.Lstat(projectRemovalIntentDirectory)
	if err != nil {
		return nil, fmt.Errorf("inspect project removal intent directory: %w", err)
	}
	if observed.Mode()&os.ModeSymlink != 0 || !observed.IsDir() {
		return nil, errors.New("project removal intent path is not a direct directory")
	}
	if journal.afterDirectoryObservation != nil {
		journal.afterDirectoryObservation()
	}

	root, err := baseRoot.OpenRoot(projectRemovalIntentDirectory)
	if err != nil {
		return nil, fmt.Errorf("open project removal intent directory: %w", err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("retain project removal intent directory: %w", err), root.Close())
	}
	filesystem := &projectRemovalIntentFilesystem{
		root:             root,
		directory:        directory,
		beforeDirectOpen: journal.beforeDirectOpen,
	}
	if created {
		if err := directory.Chmod(projectRemovalIntentDirectoryMode); err != nil {
			return nil, errors.Join(fmt.Errorf("secure project removal intent directory: %w", err), filesystem.Close())
		}
	}
	if err := validateProjectRemovalIntentObject(directory, true); err != nil {
		return nil, errors.Join(fmt.Errorf("validate project removal intent directory: %w", err), filesystem.Close())
	}
	opened, err := directory.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect retained project removal intent directory: %w", err), filesystem.Close())
	}
	reobserved, err := baseRoot.Lstat(projectRemovalIntentDirectory)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("reinspect project removal intent directory: %w", err),
			filesystem.Close(),
		)
	}
	if reobserved.Mode()&os.ModeSymlink != 0 || !os.SameFile(observed, opened) || !os.SameFile(opened, reobserved) {
		return nil, errors.Join(
			errors.New("project removal intent directory changed while its handle opened"),
			filesystem.Close(),
		)
	}
	return filesystem, nil
}

// Close releases both retained handles without removing durable intent state.
func (filesystem *projectRemovalIntentFilesystem) Close() error {
	if filesystem == nil {
		return nil
	}
	return errors.Join(filesystem.directory.Close(), filesystem.root.Close())
}

// openOrCreateDirectFile opens only a regular direct child and never chmods an existing object by name.
func (filesystem *projectRemovalIntentFilesystem) openOrCreateDirectFile(name string, flags int) (*os.File, error) {
	file, err := filesystem.root.OpenFile(
		name,
		flags|os.O_CREATE|os.O_EXCL,
		projectRemovalIntentFileMode,
	)
	if err == nil {
		if err := file.Chmod(projectRemovalIntentFileMode); err != nil {
			return nil, errors.Join(err, file.Close(), filesystem.root.Remove(name))
		}
		if err := filesystem.validateCurrent(name, file, false); err != nil {
			return nil, errors.Join(err, file.Close(), filesystem.root.Remove(name))
		}
		return file, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	return filesystem.openDirectFile(name, flags)
}

// openDirectFile binds an observed regular child name to the exact handle that will be read or locked.
func (filesystem *projectRemovalIntentFilesystem) openDirectFile(name string, flags int) (*os.File, error) {
	observed, err := filesystem.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if observed.Mode()&os.ModeSymlink != 0 || !observed.Mode().IsRegular() {
		return nil, fmt.Errorf("project removal intent child %q is not a direct regular file", name)
	}
	if filesystem.beforeDirectOpen != nil {
		filesystem.beforeDirectOpen(name)
	}
	file, err := filesystem.root.OpenFile(name, flags, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !os.SameFile(observed, opened) {
		return nil, errors.Join(fmt.Errorf("project removal intent child %q changed while its handle opened", name), file.Close())
	}
	if err := filesystem.validateCurrent(name, file, false); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// validateCurrent proves a retained handle still names the same direct, private child within the journal root.
func (filesystem *projectRemovalIntentFilesystem) validateCurrent(name string, file *os.File, directory bool) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	observed, err := filesystem.root.Lstat(name)
	if err != nil {
		return err
	}
	if observed.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, observed) {
		return fmt.Errorf("project removal intent child %q changed after its handle opened", name)
	}
	return validateProjectRemovalIntentObject(file, directory)
}

// readRecord rejects malformed or unexpectedly large state through the exact handle it validated.
func (filesystem *projectRemovalIntentFilesystem) readRecord(path string) (record projectRemovalIntentRecord, found bool, readErr error) {
	file, err := filesystem.openDirectFile(path, os.O_RDONLY)
	if errors.Is(err, fs.ErrNotExist) {
		return projectRemovalIntentRecord{}, false, nil
	}
	if err != nil {
		return projectRemovalIntentRecord{}, false, fmt.Errorf("open retained project removal intent: %w", err)
	}
	defer func() {
		readErr = errors.Join(readErr, file.Close())
	}()
	info, err := file.Stat()
	if err != nil {
		return projectRemovalIntentRecord{}, false, fmt.Errorf("inspect retained project removal intent: %w", err)
	}
	if info.Size() < 0 || info.Size() > projectRemovalIntentRecordLimit {
		return projectRemovalIntentRecord{}, false, fmt.Errorf(
			"retained project removal intent exceeds %d bytes",
			projectRemovalIntentRecordLimit,
		)
	}
	contents, err := io.ReadAll(io.LimitReader(file, projectRemovalIntentRecordLimit+1))
	if err != nil {
		return projectRemovalIntentRecord{}, false, fmt.Errorf("read retained project removal intent: %w", err)
	}
	if len(contents) > projectRemovalIntentRecordLimit {
		return projectRemovalIntentRecord{}, false, fmt.Errorf(
			"retained project removal intent exceeds %d bytes",
			projectRemovalIntentRecordLimit,
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return projectRemovalIntentRecord{}, false, fmt.Errorf("decode retained project removal intent: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return projectRemovalIntentRecord{}, false, errors.New("decode retained project removal intent: trailing data")
	}
	if err := record.Validate(); err != nil {
		return projectRemovalIntentRecord{}, false, fmt.Errorf("validate retained project removal intent: %w", err)
	}
	return record, true, nil
}

// Validate rejects records that cannot safely identify exactly one supported removal attempt.
func (record projectRemovalIntentRecord) Validate() error {
	if record.Version != projectRemovalIntentRecordVersion {
		return fmt.Errorf("unsupported record version %d", record.Version)
	}
	if err := record.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if err := record.IntentID.Validate(); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	return nil
}

// writeRecord syncs content before atomically publishing its confined child name and directory entry.
func (filesystem *projectRemovalIntentFilesystem) writeRecord(path string, record projectRemovalIntentRecord) error {
	temporary, temporaryName, err := filesystem.createTemporaryFile()
	if err != nil {
		return err
	}
	removeTemporary := true
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if removeTemporary {
			_ = filesystem.root.Remove(temporaryName)
		}
	}()

	if err := json.NewEncoder(temporary).Encode(record); err != nil {
		return fmt.Errorf("encode project removal intent: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync project removal intent: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close project removal intent: %w", err)
	}
	closed = true
	if err := filesystem.root.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish project removal intent: %w", err)
	}
	removeTemporary = false
	if err := syncProjectRemovalIntentDirectory(filesystem.directory); err != nil {
		return fmt.Errorf("sync project removal intent directory: %w", err)
	}
	return nil
}

// createTemporaryFile makes an unpredictable exclusive child beneath the retained journal root.
func (filesystem *projectRemovalIntentFilesystem) createTemporaryFile() (*os.File, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return nil, "", fmt.Errorf("create temporary project removal intent name: %w", err)
		}
		name := ".project-removal-intent-" + hex.EncodeToString(random)
		file, err := filesystem.root.OpenFile(
			name,
			os.O_RDWR|os.O_CREATE|os.O_EXCL,
			projectRemovalIntentFileMode,
		)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("create temporary project removal intent: %w", err)
		}
		if err := file.Chmod(projectRemovalIntentFileMode); err != nil {
			return nil, "", errors.Join(err, file.Close(), filesystem.root.Remove(name))
		}
		if err := filesystem.validateCurrent(name, file, false); err != nil {
			return nil, "", errors.Join(err, file.Close(), filesystem.root.Remove(name))
		}
		return file, name, nil
	}
	return nil, "", errors.New("create temporary project removal intent: name collision limit reached")
}

// newProjectRemovalIntentProcessGate creates a context-aware mutex for file-lock semantics that vary by platform.
func newProjectRemovalIntentProcessGate() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}

// acquireProjectRemovalIntentProcessGate waits for in-process ownership without ignoring caller cancellation.
func acquireProjectRemovalIntentProcessGate(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-projectRemovalIntentProcessGate:
		return nil
	}
}

// releaseProjectRemovalIntentProcessGate returns in-process ownership after the filesystem transaction ends.
func releaseProjectRemovalIntentProcessGate() {
	projectRemovalIntentProcessGate <- struct{}{}
}

// projectRemovalIntentPath hashes the validated project identity into one bounded, path-safe record name.
func projectRemovalIntentPath(projectID domain.ProjectID) string {
	digest := sha256.Sum256([]byte(projectID))
	return hex.EncodeToString(digest[:]) + ".json"
}
