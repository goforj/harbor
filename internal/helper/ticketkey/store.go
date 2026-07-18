package ticketkey

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/goforj/harbor/internal/platform/userpaths"
)

const (
	activeDirectory    = "active"
	keyFilename        = "key.json"
	stagingRandomBytes = 16
)

// storeDependencies keep entropy and path-swap failures deterministic in package tests.
type storeDependencies struct {
	random          io.Reader
	afterValidation func()
	beforeOpen      func(string)
}

// Store owns one confined per-user helper-ticket signing-key directory.
type Store struct {
	mutex      sync.Mutex
	filesystem *rootedFilesystem
	random     io.Reader
	closed     bool
}

// OpenDefault opens Harbor's platform-standard helper-ticket signing-key directory.
func OpenDefault() (*Store, error) {
	directory, err := userpaths.HelperTicketKeyDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve helper ticket key directory: %w", err)
	}
	return Open(directory)
}

// Open creates or verifies one owner-private helper-ticket signing-key store.
func Open(directory string) (*Store, error) {
	return openStore(directory, storeDependencies{random: rand.Reader})
}

// openStore retains deterministic dependencies without exposing persistence fault injection to product callers.
func openStore(directory string, dependencies storeDependencies) (*Store, error) {
	if dependencies.random == nil {
		return nil, errors.New("open helper ticket key store: random source is required")
	}
	filesystem, err := openRootedFilesystem(directory, dependencies.afterValidation, dependencies.beforeOpen)
	if err != nil {
		return nil, err
	}
	return &Store{filesystem: filesystem, random: dependencies.random}, nil
}

// Close releases the rooted filesystem handle and safely tolerates repeated daemon shutdown paths.
func (store *Store) Close() error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.filesystem.Close()
}

// Load returns the established signing identity without creating replacement authority when it is absent.
func (store *Store) Load(ctx context.Context) (ed25519.PrivateKey, error) {
	ctx = normalizeContext(ctx)
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return nil, err
	}

	privateKey, err := store.load()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrKeyNotEstablished
	}
	if err != nil {
		return nil, err
	}
	return clonePrivateKey(privateKey), nil
}

// LoadOrCreate reloads the exact signing identity or atomically publishes one first-run identity.
func (store *Store) LoadOrCreate(ctx context.Context) (ed25519.PrivateKey, error) {
	ctx = normalizeContext(ctx)
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return nil, err
	}

	privateKey, err := store.load()
	if err == nil {
		return clonePrivateKey(privateKey), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return store.create(ctx)
}

// load distinguishes an absent first-run directory from malformed established material.
func (store *Store) load() (privateKey ed25519.PrivateKey, loadErr error) {
	directory, err := store.filesystem.openDirect(activeDirectory, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, corrupt("active directory", err)
	}
	defer func() {
		if err := directory.Close(); err != nil {
			loadErr = errors.Join(loadErr, corrupt("active directory", fmt.Errorf("close active directory: %w", err)))
		}
	}()
	if err := validateDirectoryHandle(directory, activeDirectory, keyFilename); err != nil {
		return nil, corrupt("active directory", err)
	}
	encoded, err := store.filesystem.readBoundedFile(filepath.Join(activeDirectory, keyFilename), maximumKeyDocumentBytes)
	if err != nil {
		return nil, corrupt("key document", err)
	}
	privateKey, err = decodePrivateKey(encoded)
	if err != nil {
		return nil, corrupt("key document", err)
	}
	if err := store.filesystem.validateOpened(activeDirectory, directory, true); err != nil {
		return nil, corrupt("active directory", err)
	}
	return privateKey, nil
}

// create commits a complete immutable directory so concurrent daemons can only observe one winner.
func (store *Store) create(ctx context.Context) (privateKey ed25519.PrivateKey, createErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	staging, err := store.stagingDirectory()
	if err != nil {
		return nil, err
	}
	if err := store.filesystem.ensureDirectory(staging); err != nil {
		return nil, fmt.Errorf("create helper ticket key staging directory: %w", err)
	}
	defer func() {
		if err := store.filesystem.removeStaging(staging); err != nil {
			createErr = errors.Join(createErr, fmt.Errorf("remove helper ticket key staging directory: %w", err))
		}
	}()

	_, privateKey, err = ed25519.GenerateKey(store.random)
	if err != nil {
		return nil, fmt.Errorf("generate helper ticket signing key: %w", err)
	}
	encoded, err := encodePrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(staging, keyFilename), encoded); err != nil {
		return nil, fmt.Errorf("persist helper ticket signing key: %w", err)
	}
	if err := store.filesystem.validateDirectory(staging, keyFilename); err != nil {
		return nil, fmt.Errorf("validate helper ticket signing key staging directory: %w", err)
	}
	stagedDocument, err := store.filesystem.readBoundedFile(filepath.Join(staging, keyFilename), maximumKeyDocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("reload staged helper ticket signing key: %w", err)
	}
	stagedKey, err := decodePrivateKey(stagedDocument)
	if err != nil {
		return nil, fmt.Errorf("validate staged helper ticket signing key: %w", err)
	}
	if !privateKey.Equal(stagedKey) {
		return nil, errors.New("validate staged helper ticket signing key: persisted identity changed before publication")
	}
	if err := store.filesystem.syncDirectory(staging); err != nil {
		return nil, fmt.Errorf("sync helper ticket signing key staging directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.filesystem.renameDirectoryNoReplace(staging, activeDirectory); err != nil {
		if errors.Is(err, fs.ErrExist) {
			winner, loadErr := store.load()
			if loadErr != nil {
				return nil, fmt.Errorf("load concurrently published helper ticket signing key: %w", loadErr)
			}
			return clonePrivateKey(winner), nil
		}
		return nil, fmt.Errorf("publish helper ticket signing key: %w", err)
	}
	published, err := store.load()
	if err != nil {
		return nil, fmt.Errorf("reload published helper ticket signing key: %w", err)
	}
	if !privateKey.Equal(published) {
		return nil, corrupt("key document", errors.New("published identity changed after publication"))
	}
	return clonePrivateKey(published), nil
}

// stagingDirectory returns a collision-resistant path whose prefix is reserved for incomplete writes.
func (store *Store) stagingDirectory() (string, error) {
	random := make([]byte, stagingRandomBytes)
	if _, err := io.ReadFull(store.random, random); err != nil {
		return "", fmt.Errorf("generate helper ticket key staging name: %w", err)
	}
	return ".staging-" + hex.EncodeToString(random), nil
}

// ready rejects cancelled work and use after shutdown before any store mutation.
func (store *Store) ready(ctx context.Context) error {
	if store.closed {
		return ErrStoreClosed
	}
	return ctx.Err()
}

// normalizeContext keeps the store usable from lifecycle paths that omit an optional cancellation scope.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// clonePrivateKey prevents callers from mutating bytes retained by another result.
func clonePrivateKey(privateKey ed25519.PrivateKey) ed25519.PrivateKey {
	return append(ed25519.PrivateKey(nil), privateKey...)
}
