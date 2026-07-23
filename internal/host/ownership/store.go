package ownership

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	// MaximumRecordBytes bounds protected-state reads before any JSON decoding occurs.
	MaximumRecordBytes int64 = 16 * 1024

	privateFileMode   = 0o600
	temporaryAttempts = 128
)

var (
	// ErrConflict identifies an existing machine claim that differs from a requested claim.
	ErrConflict = errors.New("machine ownership conflict")
	// ErrCorruptRecord identifies persisted content that cannot be trusted as a canonical ownership record.
	ErrCorruptRecord = errors.New("machine ownership record is corrupt")
	// ErrDurabilityUncertain identifies a completed name transition whose storage barrier did not confirm persistence.
	ErrDurabilityUncertain = errors.New("machine ownership durability is uncertain")
	// ErrNotClaimed identifies a mutation attempted while no machine ownership record exists.
	ErrNotClaimed = errors.New("machine ownership is not claimed")
	// ErrStaleFingerprint identifies a mutation whose compare-and-swap observation no longer matches storage.
	ErrStaleFingerprint = errors.New("machine ownership fingerprint is stale")
	// ErrUnsafePath identifies a symbolic link, special file, or replaced path at the protected storage boundary.
	ErrUnsafePath = errors.New("unsafe machine ownership path")

	pathLocks sync.Map
)

// processPathLock is a context-aware binary semaphore shared by every Store for one canonical path.
type processPathLock struct {
	token chan struct{}
}

// newProcessPathLock creates an available path semaphore.
func newProcessPathLock() *processPathLock {
	lock := &processPathLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

// acquire waits for local ownership without touching storage after cancellation.
func (lock *processPathLock) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lock.token:
		return nil
	}
}

// release returns local ownership to the next waiter.
func (lock *processPathLock) release() {
	lock.token <- struct{}{}
}

// Observation is an immutable snapshot of the current machine-global claim.
type Observation struct {
	Exists      bool
	Record      Record
	Fingerprint string
}

// ConflictError reports both sides of a claim that cannot safely be adopted.
type ConflictError struct {
	Requested Record
	Existing  Observation
}

// Error describes the immutable ownership dimensions without exposing incidental filesystem details.
func (conflict *ConflictError) Error() string {
	return fmt.Sprintf(
		"%s: installation %q owned by %q at generation %d has fingerprint %s; requested installation %q owned by %q at generation %d",
		ErrConflict,
		conflict.Existing.Record.InstallationID,
		conflict.Existing.Record.OwnerIdentity,
		conflict.Existing.Record.Generation,
		conflict.Existing.Fingerprint,
		conflict.Requested.InstallationID,
		conflict.Requested.OwnerIdentity,
		conflict.Requested.Generation,
	)
}

// Unwrap makes ownership conflicts classifiable without discarding their observed evidence.
func (conflict *ConflictError) Unwrap() error {
	return ErrConflict
}

// FingerprintMismatchError preserves the current observation when an ownership mutation loses its compare-and-swap race.
type FingerprintMismatchError struct {
	Expected string
	Actual   Observation
}

// Error describes the exact fingerprint mismatch that prevented destructive cleanup.
func (mismatch *FingerprintMismatchError) Error() string {
	return fmt.Sprintf("%s: expected %s, found %s", ErrStaleFingerprint, mismatch.Expected, mismatch.Actual.Fingerprint)
}

// Unwrap makes stale mutations classifiable while retaining the current ownership observation.
func (mismatch *FingerprintMismatchError) Unwrap() error {
	return ErrStaleFingerprint
}

// Store serializes ownership compare-and-swap operations against one absolute protected path.
type Store struct {
	path       string
	name       string
	lockName   string
	root       *os.Root
	pathLock   *processPathLock
	operations storeOperations
	closed     bool
}

// storeOperations keeps mutation fault injection scoped to one Store so parallel security tests cannot interfere.
type storeOperations struct {
	acquireLock     func(context.Context, *os.File) error
	releaseLock     func(*os.File) error
	closeLock       func(*os.File) error
	createFile      func(*os.Root, string, string) (*os.File, error)
	renameNoReplace func(*os.Root, string, string, string) (bool, error)
	renameReplace   func(*os.Root, string, string, string) (bool, error)
	confirmEntry    func(*os.Root, string, string) error
	confirmCleanup  func(*os.Root) error
	removeEntry     func(*os.Root, string) error
	randomRead      func([]byte) (int, error)
	writeTemporary  func(io.Writer, []byte) error
	syncTemporary   func(*os.File) error
	closeTemporary  func(*os.File) error
}

// defaultStoreOperations binds production stores directly to reviewed operating-system primitives.
func defaultStoreOperations() storeOperations {
	return storeOperations{
		acquireLock:     acquirePlatformLock,
		releaseLock:     releasePlatformLock,
		closeLock:       func(file *os.File) error { return file.Close() },
		createFile:      createPlatformFile,
		renameNoReplace: platformRenameNoReplace,
		renameReplace:   platformRenameReplace,
		confirmEntry:    platformConfirmEntry,
		confirmCleanup:  platformConfirmCleanup,
		removeEntry:     func(root *os.Root, name string) error { return root.Remove(name) },
		randomRead:      rand.Read,
		writeTemporary:  writeAll,
		syncTemporary:   func(file *os.File) error { return file.Sync() },
		closeTemporary:  func(file *os.File) error { return file.Close() },
	}
}

// NewStore opens the existing parent directory that contains one fixed machine ownership path.
func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("open machine ownership store: path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("open machine ownership store: path %q is not absolute", path)
	}
	if filepath.Clean(path) != path {
		return nil, fmt.Errorf("open machine ownership store: path %q is not canonical", path)
	}
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, fmt.Errorf("open machine ownership store: path %q does not name a file", path)
	}

	directory := filepath.Dir(path)
	validated, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("open machine ownership store directory %q: %w", directory, err)
	}
	if validated.Mode()&os.ModeSymlink != 0 || !validated.IsDir() {
		return nil, fmt.Errorf("%w: machine ownership parent %q is not a direct directory", ErrUnsafePath, directory)
	}
	if err := validatePlatformDirectory(directory, validated); err != nil {
		return nil, fmt.Errorf("%w: validate machine ownership parent %q: %v", ErrUnsafePath, directory, err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open machine ownership store directory %q: %w", directory, err)
	}
	openedDirectory, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("open retained machine ownership parent %q: %w", directory, err),
			root.Close(),
		)
	}
	opened, statErr := openedDirectory.Stat()
	var securityErr error
	sameDirectory := false
	if statErr == nil {
		securityErr = validatePlatformFile(openedDirectory, opened, true)
		sameDirectory = os.SameFile(validated, opened)
	}
	closeErr := openedDirectory.Close()
	if statErr != nil || securityErr != nil || closeErr != nil || !sameDirectory {
		return nil, errors.Join(
			fmt.Errorf("%w: machine ownership parent %q changed while opening", ErrUnsafePath, directory),
			statErr,
			securityErr,
			closeErr,
			root.Close(),
		)
	}

	lockName := name + ".lock"
	if err := validateExistingEntry(root, name); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if err := validateExistingEntry(root, lockName); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	lock, _ := pathLocks.LoadOrStore(path, newProcessPathLock())
	return &Store{
		path:       path,
		name:       name,
		lockName:   lockName,
		root:       root,
		pathLock:   lock.(*processPathLock),
		operations: defaultStoreOperations(),
	}, nil
}

// Path returns the absolute fixed path owned by this store.
func (store *Store) Path() string {
	return store.path
}

// Close releases the retained directory handle without changing the machine ownership claim.
func (store *Store) Close() error {
	if err := store.pathLock.acquire(context.Background()); err != nil {
		return err
	}
	defer store.pathLock.release()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.root.Close()
}

// Observe reads one stable ownership snapshot under the same process and cross-process lock used for mutation.
func (store *Store) Observe(ctx context.Context) (Observation, error) {
	ctx = normalizeContext(ctx)
	var observation Observation
	err := store.withLock(ctx, func() error {
		var err error
		observation, err = store.observeLocked()
		return err
	})
	if err != nil {
		return Observation{}, err
	}
	return observation, nil
}

// Claim creates a missing claim, replays an identical claim, and preserves every differing existing claim.
// ErrDurabilityUncertain means the active name changed but must be reconciled before authority is granted.
func (store *Store) Claim(ctx context.Context, record Record) (Observation, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := record.Validate(); err != nil {
		return Observation{}, fmt.Errorf("claim machine ownership: %w", err)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		return Observation{}, err
	}
	requested := Observation{Exists: true, Record: record, Fingerprint: fingerprint}

	var claimed Observation
	mutationApplied := false
	err = store.withLock(ctx, func() error {
		existing, err := store.observeLocked()
		if err != nil {
			return err
		}
		if existing.Exists {
			if existing.Record == record {
				if err := store.operations.confirmEntry(store.root, filepath.Dir(store.path), store.name); err != nil {
					return durabilityUncertain("confirm machine ownership claim", store.path, err)
				}
				claimed = existing
				return nil
			}
			return &ConflictError{Requested: record, Existing: existing}
		}

		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode machine ownership claim: %w", err)
		}
		if err := store.writeExclusiveLocked(encoded); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			existing, observeErr := store.observeLocked()
			if observeErr != nil {
				return errors.Join(err, observeErr)
			}
			if existing.Exists && existing.Record == record {
				if confirmErr := store.operations.confirmEntry(store.root, filepath.Dir(store.path), store.name); confirmErr != nil {
					return durabilityUncertain("confirm concurrent machine ownership claim", store.path, confirmErr)
				}
				claimed = existing
				return nil
			}
			return &ConflictError{Requested: record, Existing: existing}
		}
		mutationApplied = true
		claimed = requested
		return nil
	})
	if err != nil {
		if mutationApplied && !errors.Is(err, ErrDurabilityUncertain) {
			return Observation{}, durabilityUncertain("finish machine ownership claim", store.path, err)
		}
		return Observation{}, err
	}
	return claimed, nil
}

// Upgrade atomically replaces an exact schema-1 claim with its schema-2 network-policy-bound form.
// ErrDurabilityUncertain means the active name changed but must be reconciled before authority is granted.
func (store *Store) Upgrade(ctx context.Context, expectedSchema1Fingerprint string, targetSchema2 Record) (Observation, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := validateFingerprint(expectedSchema1Fingerprint); err != nil {
		return Observation{}, fmt.Errorf("upgrade machine ownership: %w", err)
	}
	if err := targetSchema2.Validate(); err != nil {
		return Observation{}, fmt.Errorf("upgrade machine ownership: %w", err)
	}
	if targetSchema2.SchemaVersion != NetworkPolicySchemaVersion {
		return Observation{}, fmt.Errorf(
			"upgrade machine ownership: target schema version is %d, want %d",
			targetSchema2.SchemaVersion,
			NetworkPolicySchemaVersion,
		)
	}

	sourceSchema1 := targetSchema2
	sourceSchema1.SchemaVersion = IdentitySchemaVersion
	sourceSchema1.NetworkPolicyFingerprint = ""
	sourceFingerprint, err := sourceSchema1.Fingerprint()
	if err != nil {
		return Observation{}, fmt.Errorf("upgrade machine ownership: derive schema-%d source: %w", IdentitySchemaVersion, err)
	}
	if expectedSchema1Fingerprint != sourceFingerprint {
		return Observation{}, fmt.Errorf(
			"upgrade machine ownership: expected schema-%d fingerprint %s does not match target-derived source fingerprint %s",
			IdentitySchemaVersion,
			expectedSchema1Fingerprint,
			sourceFingerprint,
		)
	}
	targetFingerprint, err := targetSchema2.Fingerprint()
	if err != nil {
		return Observation{}, fmt.Errorf("upgrade machine ownership: fingerprint target: %w", err)
	}
	target := Observation{Exists: true, Record: targetSchema2, Fingerprint: targetFingerprint}

	var upgraded Observation
	mutationApplied := false
	err = store.withLock(ctx, func() error {
		existing, err := store.observeLocked()
		if err != nil {
			return err
		}
		if existing.Exists && existing.Record == targetSchema2 {
			if err := store.operations.confirmEntry(store.root, filepath.Dir(store.path), store.name); err != nil {
				return durabilityUncertain("confirm upgraded machine ownership claim", store.path, err)
			}
			upgraded = existing
			return nil
		}
		if err := compareUpgradeSource(existing, expectedSchema1Fingerprint, sourceSchema1); err != nil {
			return err
		}

		encoded, err := json.Marshal(targetSchema2)
		if err != nil {
			return fmt.Errorf("encode upgraded machine ownership claim: %w", err)
		}
		mutationApplied, err = store.writeReplaceLocked(encoded)
		if err != nil {
			return err
		}
		observed, err := store.observeLocked()
		if err != nil {
			return fmt.Errorf("verify upgraded machine ownership claim: %w", err)
		}
		if observed != target {
			return fmt.Errorf(
				"verify upgraded machine ownership claim: found fingerprint %s, want %s",
				observed.Fingerprint,
				target.Fingerprint,
			)
		}
		upgraded = observed
		return nil
	})
	if err != nil {
		if mutationApplied && !errors.Is(err, ErrDurabilityUncertain) {
			return Observation{}, durabilityUncertain("finish machine ownership upgrade", store.path, err)
		}
		return Observation{}, err
	}
	return upgraded, nil
}

// Downgrade atomically replaces an exact schema-2 claim with its schema-1 identity-only form.
// ErrDurabilityUncertain means the active name changed but must be reconciled before authority is granted.
func (store *Store) Downgrade(ctx context.Context, expectedSchema2Fingerprint string, sourceSchema2 Record) (Observation, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := validateFingerprint(expectedSchema2Fingerprint); err != nil {
		return Observation{}, fmt.Errorf("downgrade machine ownership: %w", err)
	}
	if err := sourceSchema2.Validate(); err != nil {
		return Observation{}, fmt.Errorf("downgrade machine ownership: %w", err)
	}
	if sourceSchema2.SchemaVersion != NetworkPolicySchemaVersion {
		return Observation{}, fmt.Errorf(
			"downgrade machine ownership: source schema version is %d, want %d",
			sourceSchema2.SchemaVersion,
			NetworkPolicySchemaVersion,
		)
	}
	sourceFingerprint, err := sourceSchema2.Fingerprint()
	if err != nil {
		return Observation{}, fmt.Errorf("downgrade machine ownership: fingerprint source: %w", err)
	}
	if expectedSchema2Fingerprint != sourceFingerprint {
		return Observation{}, fmt.Errorf(
			"downgrade machine ownership: expected schema-%d fingerprint %s does not match source fingerprint %s",
			NetworkPolicySchemaVersion,
			expectedSchema2Fingerprint,
			sourceFingerprint,
		)
	}

	targetSchema1 := sourceSchema2
	targetSchema1.SchemaVersion = IdentitySchemaVersion
	targetSchema1.NetworkPolicyFingerprint = ""
	targetFingerprint, err := targetSchema1.Fingerprint()
	if err != nil {
		return Observation{}, fmt.Errorf("downgrade machine ownership: derive schema-%d target: %w", IdentitySchemaVersion, err)
	}
	target := Observation{Exists: true, Record: targetSchema1, Fingerprint: targetFingerprint}

	var downgraded Observation
	mutationApplied := false
	err = store.withLock(ctx, func() error {
		existing, err := store.observeLocked()
		if err != nil {
			return err
		}
		if existing.Exists && existing.Record == targetSchema1 {
			if err := store.operations.confirmEntry(store.root, filepath.Dir(store.path), store.name); err != nil {
				return durabilityUncertain("confirm downgraded machine ownership claim", store.path, err)
			}
			downgraded = existing
			return nil
		}
		if err := compareDowngradeSource(existing, expectedSchema2Fingerprint, sourceSchema2); err != nil {
			return err
		}

		encoded, err := json.Marshal(targetSchema1)
		if err != nil {
			return fmt.Errorf("encode downgraded machine ownership claim: %w", err)
		}
		mutationApplied, err = store.writeReplaceLocked(encoded)
		if err != nil {
			return err
		}
		observed, err := store.observeLocked()
		if err != nil {
			return fmt.Errorf("verify downgraded machine ownership claim: %w", err)
		}
		if observed != target {
			return fmt.Errorf(
				"verify downgraded machine ownership claim: found fingerprint %s, want %s",
				observed.Fingerprint,
				target.Fingerprint,
			)
		}
		downgraded = observed
		return nil
	})
	if err != nil {
		if mutationApplied && !errors.Is(err, ErrDurabilityUncertain) {
			return Observation{}, durabilityUncertain("finish machine ownership downgrade", store.path, err)
		}
		return Observation{}, err
	}
	return downgraded, nil
}

// compareUpgradeSource requires both the expected digest and its exact source record before replacement.
func compareUpgradeSource(existing Observation, expectedFingerprint string, source Record) error {
	if !existing.Exists {
		return ErrNotClaimed
	}
	if existing.Fingerprint != expectedFingerprint {
		return &FingerprintMismatchError{Expected: expectedFingerprint, Actual: existing}
	}
	if existing.Record != source {
		return &ConflictError{Requested: source, Existing: existing}
	}
	return nil
}

// compareDowngradeSource requires both the expected digest and its exact source record before replacement.
func compareDowngradeSource(existing Observation, expectedFingerprint string, source Record) error {
	return compareUpgradeSource(existing, expectedFingerprint, source)
}

// Release removes a claim only when the caller presents its exact current observation fingerprint.
// ErrDurabilityUncertain means the active name changed and startup must reconcile whether that release persisted.
func (store *Store) Release(ctx context.Context, fingerprint string) error {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return fmt.Errorf("release machine ownership: %w", err)
	}
	mutationApplied := false
	err := store.withLock(ctx, func() error {
		observation, err := store.observeLocked()
		if err != nil {
			return err
		}
		if !observation.Exists {
			return ErrNotClaimed
		}
		if observation.Fingerprint != fingerprint {
			return &FingerprintMismatchError{Expected: fingerprint, Actual: observation}
		}
		retiredName, err := store.retireLocked()
		mutationApplied = retiredName != ""
		if err != nil {
			return err
		}
		// The durable rename is the release boundary; retirement cleanup must not turn a completed release into a retry hazard.
		store.removeRetiredLocked(retiredName)
		return nil
	})
	if err != nil && mutationApplied && !errors.Is(err, ErrDurabilityUncertain) {
		return durabilityUncertain("finish machine ownership release", store.path, err)
	}
	return err
}

// withLock serializes all path aliases in this process before acquiring the operating-system lock file.
func (store *Store) withLock(ctx context.Context, operation func() error) (operationErr error) {
	if err := store.pathLock.acquire(ctx); err != nil {
		return err
	}
	defer store.pathLock.release()
	if store.closed {
		return fmt.Errorf("machine ownership store %q is closed", store.path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	lock, err := store.openLockFile()
	if err != nil {
		return err
	}
	if err := store.operations.acquireLock(ctx, lock); err != nil {
		return errors.Join(
			fmt.Errorf("acquire machine ownership lock %q: %w", store.path, err),
			store.operations.closeLock(lock),
		)
	}
	defer func() {
		operationErr = errors.Join(
			operationErr,
			wrapStoreError("release machine ownership lock", store.path, store.operations.releaseLock(lock)),
			wrapStoreError("close machine ownership lock", store.path, store.operations.closeLock(lock)),
		)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

// normalizeContext preserves convenience for internal callers while keeping every blocking boundary cancellable.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// openLockFile obtains a direct regular file and proves its path still names the opened inode or handle.
func (store *Store) openLockFile() (*os.File, error) {
	for attempt := 0; attempt < 2; attempt++ {
		info, err := store.root.Lstat(store.lockName)
		if errors.Is(err, fs.ErrNotExist) {
			lock, createErr := store.operations.createFile(store.root, filepath.Dir(store.path), store.lockName)
			if errors.Is(createErr, fs.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, fmt.Errorf("create machine ownership lock %q: %w", store.path, createErr)
			}
			if err := validateOpenedEntry(store.root, store.lockName, lock); err != nil {
				return nil, errors.Join(err, lock.Close(), store.root.Remove(store.lockName))
			}
			return lock, nil
		}
		if err != nil {
			return nil, fmt.Errorf("inspect machine ownership lock %q: %w", store.path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: machine ownership lock for %q is not a direct regular file", ErrUnsafePath, store.path)
		}
		lock, err := store.root.OpenFile(store.lockName, os.O_RDWR, privateFileMode)
		if err != nil {
			return nil, fmt.Errorf("open machine ownership lock %q: %w", store.path, err)
		}
		if err := validateOpenedEntry(store.root, store.lockName, lock); err != nil {
			return nil, errors.Join(err, lock.Close())
		}
		return lock, nil
	}
	return nil, fmt.Errorf("open machine ownership lock %q: concurrent creation did not settle", store.path)
}

// observeLocked reads the protected record only after callers hold both ownership locks.
func (store *Store) observeLocked() (Observation, error) {
	file, err := store.openRecordLocked()
	if errors.Is(err, fs.ErrNotExist) {
		return Observation{}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	content, readErr := readBounded(file, MaximumRecordBytes)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return Observation{}, errors.Join(readErr, wrapStoreError("close machine ownership record", store.path, closeErr))
	}
	record, err := decodeRecord(content)
	if err != nil {
		return Observation{}, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		return Observation{}, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	return Observation{Exists: true, Record: record, Fingerprint: fingerprint}, nil
}

// openRecordLocked rejects links and special files before and after opening the exact record path.
func (store *Store) openRecordLocked() (*os.File, error) {
	info, err := store.root.Lstat(store.name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: machine ownership record %q is not a direct regular file", ErrUnsafePath, store.path)
	}
	file, err := store.root.OpenFile(store.name, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open machine ownership record %q: %w", store.path, err)
	}
	if err := validateOpenedEntry(store.root, store.name, file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// writeExclusiveLocked durably publishes a new record without replacing any path that appeared after observation.
func (store *Store) writeExclusiveLocked(content []byte) (writeErr error) {
	temporaryName, err := store.stageTemporaryLocked(content)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			writeErr = errors.Join(writeErr, store.removeTemporaryLocked(temporaryName))
		}
	}()

	applied, err := store.operations.renameNoReplace(store.root, filepath.Dir(store.path), temporaryName, store.name)
	if applied {
		removeTemporary = false
	}
	if err != nil {
		if applied {
			return durabilityUncertain("publish machine ownership record", store.path, err)
		}
		return fmt.Errorf("publish machine ownership record %q: %w", store.path, err)
	}
	published, err := store.openRecordLocked()
	if err != nil {
		return durabilityUncertain("validate published machine ownership record", store.path, err)
	}
	if err := published.Close(); err != nil {
		return durabilityUncertain("close validated machine ownership record", store.path, err)
	}
	return nil
}

// writeReplaceLocked atomically overwrites the active name with a fully staged canonical record.
func (store *Store) writeReplaceLocked(content []byte) (applied bool, replaceErr error) {
	temporaryName, err := store.stageTemporaryLocked(content)
	if err != nil {
		return false, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			replaceErr = errors.Join(replaceErr, store.removeTemporaryLocked(temporaryName))
		}
	}()

	applied, err = store.operations.renameReplace(store.root, filepath.Dir(store.path), temporaryName, store.name)
	if applied {
		removeTemporary = false
	}
	if err != nil {
		if applied {
			return true, durabilityUncertain("replace machine ownership record", store.path, err)
		}
		return false, fmt.Errorf("replace machine ownership record %q: %w", store.path, err)
	}
	if !applied {
		return false, fmt.Errorf("replace machine ownership record %q: platform rename reported no transition", store.path)
	}
	return true, nil
}

// stageTemporaryLocked writes, synchronizes, and closes an owner-private same-directory record before publication.
func (store *Store) stageTemporaryLocked(content []byte) (temporaryName string, stageErr error) {
	name, temporary, err := store.createTemporaryLocked()
	if err != nil {
		return "", err
	}
	closed := false
	defer func() {
		stageErr = errors.Join(stageErr, wrapStoreError(
			"close temporary machine ownership record",
			store.path,
			closeOnce(&closed, func() error { return store.operations.closeTemporary(temporary) }),
		))
		if stageErr != nil {
			stageErr = errors.Join(stageErr, store.removeTemporaryLocked(name))
		}
	}()

	if err := store.operations.writeTemporary(temporary, content); err != nil {
		return "", fmt.Errorf("write temporary machine ownership record %q: %w", store.path, err)
	}
	if err := store.operations.syncTemporary(temporary); err != nil {
		return "", fmt.Errorf("sync temporary machine ownership record %q: %w", store.path, err)
	}
	if err := closeOnce(&closed, func() error { return store.operations.closeTemporary(temporary) }); err != nil {
		return "", fmt.Errorf("close temporary machine ownership record %q: %w", store.path, err)
	}
	return name, nil
}

// removeTemporaryLocked removes an unpublished staged record while preserving cleanup evidence.
func (store *Store) removeTemporaryLocked(name string) error {
	err := store.operations.removeEntry(store.root, name)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove temporary machine ownership record %q: %w", store.path, err)
}

// createTemporaryLocked creates an unpredictable same-directory file that cannot replace an existing entry.
func (store *Store) createTemporaryLocked() (string, *os.File, error) {
	for attempt := 0; attempt < temporaryAttempts; attempt++ {
		random := make([]byte, 16)
		if _, err := store.operations.randomRead(random); err != nil {
			return "", nil, fmt.Errorf("create machine ownership temporary name: %w", err)
		}
		name := "." + store.name + ".tmp-" + hex.EncodeToString(random)
		file, err := store.operations.createFile(store.root, filepath.Dir(store.path), name)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create temporary machine ownership record %q: %w", store.path, err)
		}
		if err := validateOpenedEntry(store.root, name, file); err != nil {
			return "", nil, errors.Join(err, file.Close(), store.root.Remove(name))
		}
		return name, file, nil
	}
	return "", nil, fmt.Errorf("create temporary machine ownership record %q: exhausted unique names", store.path)
}

// retireLocked durably removes the active name without ever overwriting another protected entry.
func (store *Store) retireLocked() (string, error) {
	for attempt := 0; attempt < temporaryAttempts; attempt++ {
		retiredName, err := store.randomSiblingName("." + store.name + ".retired-")
		if err != nil {
			return "", fmt.Errorf("create retired machine ownership name: %w", err)
		}
		applied, renameErr := store.operations.renameNoReplace(store.root, filepath.Dir(store.path), store.name, retiredName)
		err = renameErr
		if applied {
			if err != nil {
				return retiredName, durabilityUncertain("retire machine ownership record", store.path, err)
			}
			return retiredName, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("retire machine ownership record %q: %w", store.path, err)
		}
	}
	return "", fmt.Errorf("retire machine ownership record %q: exhausted unique names", store.path)
}

// removeRetiredLocked reclaims a retired inode after the durable active-name transition has completed.
func (store *Store) removeRetiredLocked(name string) {
	if err := store.operations.removeEntry(store.root, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	_ = store.operations.confirmCleanup(store.root)
}

// randomSiblingName creates an unpredictable protected-directory entry name without touching storage.
func (store *Store) randomSiblingName(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := store.operations.randomRead(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

// closeOnce marks a handle consumed before closing so an error cannot trigger a second close in deferred cleanup.
func closeOnce(closed *bool, closeFile func() error) error {
	if *closed {
		return nil
	}
	*closed = true
	return closeFile()
}

// durabilityUncertain distinguishes an applied transition from an ordinary mutation failure so callers can reconcile safely.
func durabilityUncertain(operation string, path string, err error) error {
	return errors.Join(fmt.Errorf("%w: %s %q", ErrDurabilityUncertain, operation, path), err)
}

// validateExistingEntry rejects an existing symbolic link or special file without requiring the record to exist.
func validateExistingEntry(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect machine ownership path %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: machine ownership path %q is not a direct regular file", ErrUnsafePath, name)
	}
	file, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open existing machine ownership path %q: %w", name, err)
	}
	validateErr := validateOpenedEntry(root, name, file)
	closeErr := file.Close()
	return errors.Join(validateErr, wrapStoreError("close existing machine ownership path", name, closeErr))
}

// validateOpenedEntry proves a path still names the direct regular file that was actually opened.
func validateOpenedEntry(root *os.Root, name string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened machine ownership path %q: %w", name, err)
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%w: opened machine ownership path %q is not a regular file", ErrUnsafePath, name)
	}
	current, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("%w: inspect opened machine ownership path %q: %v", ErrUnsafePath, name, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("%w: machine ownership path %q changed while opening", ErrUnsafePath, name)
	}
	if err := validatePlatformFile(file, opened, false); err != nil {
		return fmt.Errorf("%w: machine ownership path %q is not protected: %v", ErrUnsafePath, name, err)
	}
	return nil
}

// readBounded rejects oversized storage based on both metadata and the exact bytes read.
func readBounded(file *os.File, maximum int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect machine ownership record %q: %w", file.Name(), err)
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("machine ownership record %q exceeds %d bytes", file.Name(), maximum)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read machine ownership record %q: %w", file.Name(), err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("machine ownership record %q exceeds %d bytes", file.Name(), maximum)
	}
	return content, nil
}

// decodeRecord rejects duplicate, unknown, trailing, and semantically noncanonical persisted fields.
func decodeRecord(content []byte) (Record, error) {
	if err := validateJSONShape(content); err != nil {
		return Record{}, fmt.Errorf("decode machine ownership record: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode machine ownership record: %w", err)
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("re-encode machine ownership record: %w", err)
	}
	if !bytes.Equal(content, canonical) {
		return Record{}, fmt.Errorf("machine ownership record is not canonically encoded")
	}
	return record, nil
}

// validateJSONShape applies exact field naming and duplicate detection before typed decoding can normalize input.
func validateJSONShape(content []byte) error {
	allowed := map[string]struct{}{
		"schema_version":             {},
		"installation_id":            {},
		"owner_identity":             {},
		"generation":                 {},
		"loopback_pool_prefix":       {},
		"network_policy_fingerprint": {},
		"ticket_verifier_key":        {},
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := start.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("top-level value must be an object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("object field name is not a string")
		}
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("top-level object is incomplete")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

// validateFingerprint requires the exact lowercase SHA-256 spelling returned by Observation.
func validateFingerprint(value string) error {
	if len(value) != sha256DigestHexLength {
		return fmt.Errorf("machine ownership fingerprint must contain %d lowercase hexadecimal characters", sha256DigestHexLength)
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return fmt.Errorf("machine ownership fingerprint must contain %d lowercase hexadecimal characters", sha256DigestHexLength)
	}
	return nil
}

const sha256DigestHexLength = 64

// writeAll treats a short successful write as corruption because ownership publication must be exact.
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

// wrapStoreError adds operation context only when cleanup produced an error.
func wrapStoreError(operation string, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %q: %w", operation, path, err)
}
