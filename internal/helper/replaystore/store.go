package replaystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

const (
	replayRecordVersion      uint16 = 1
	replayRecordMaximumBytes        = 4096
	replayRecordDomain              = "goforj.harbor/helper-replay:v1\x00"
	replayRecordSuffix              = ".json"
)

// replayRecord is the canonical tombstone retained for at least the ticket's complete admission window.
type replayRecord struct {
	Version             uint16 `json:"version"`
	InstallationID      string `json:"installation_id"`
	OwnershipGeneration uint64 `json:"ownership_generation"`
	Nonce               string `json:"nonce"`
	ExpiresAt           string `json:"expires_at"`
}

// Store owns one protected directory of durable helper replay tombstones.
type Store struct {
	mutex  sync.Mutex
	path   string
	root   *os.Root
	clock  helper.Clock
	closed bool
}

// OpenDefault opens the installer-provisioned replay directory without creating or repairing it.
func OpenDefault() (*Store, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve helper replay store: %w", err)
	}
	return Open(paths.ReplayDirectory)
}

// Open retains one existing absolute replay directory using the system clock.
func Open(directory string) (*Store, error) {
	return open(directory, helper.SystemClock{})
}

// open keeps time deterministic in tests without making the production clock optional.
func open(directory string, clock helper.Clock) (*Store, error) {
	if clock == nil {
		return nil, fmt.Errorf("open helper replay store: clock is required")
	}
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, fmt.Errorf("open helper replay store: directory %q must be an absolute canonical path", directory)
	}
	validated, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("open helper replay store %q: %w", directory, err)
	}
	if validated.Mode()&os.ModeSymlink != 0 || !validated.IsDir() {
		return nil, fmt.Errorf("open helper replay store %q: path is not a direct directory", directory)
	}
	if err := validatePlatformDirectory(directory, validated); err != nil {
		return nil, fmt.Errorf("open helper replay store %q: %w", directory, err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open helper replay store %q: %w", directory, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(validated, opened) {
		return nil, errors.Join(
			fmt.Errorf("open helper replay store %q: directory changed while opening", directory),
			err,
			root.Close(),
		)
	}
	if err := validatePlatformRoot(root); err != nil {
		return nil, errors.Join(
			fmt.Errorf("open helper replay store %q: validate retained directory: %w", directory, err),
			root.Close(),
		)
	}
	return &Store{path: directory, root: root, clock: clock}, nil
}

// Consume durably reserves one validated nonce before returning mutation authority to the dispatcher.
func (store *Store) Consume(ctx context.Context, claim helper.ReplayClaim) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := store.clock.Now().UTC()
	if err := claim.Validate(now); err != nil {
		return fmt.Errorf("consume helper replay claim: %w", err)
	}
	record := replayRecordFromClaim(claim)
	content, err := encodeReplayRecord(record)
	if err != nil {
		return replayStoreUnavailable(err)
	}
	name := replayRecordName(claim.Key)

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.closed {
		return replayStoreUnavailable(fmt.Errorf("store %q is closed", store.path))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := createPlatformFile(store.root, store.path, name)
	if errors.Is(err, fs.ErrExist) {
		return store.classifyExisting(name, record)
	}
	if err != nil {
		return replayStoreUnavailable(fmt.Errorf("create replay tombstone %q: %w", name, err))
	}
	if err := securePlatformFile(file); err != nil {
		return errors.Join(replayStoreUnavailable(fmt.Errorf("secure replay tombstone %q: %w", name, err)), file.Close())
	}
	if err := store.validateOpened(name, file); err != nil {
		return errors.Join(replayStoreUnavailable(err), file.Close())
	}
	if err := writeAll(file, content); err != nil {
		return errors.Join(replayStoreUnavailable(fmt.Errorf("write replay tombstone %q: %w", name, err)), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(replayStoreUnavailable(fmt.Errorf("sync replay tombstone %q: %w", name, err)), file.Close())
	}
	if err := file.Close(); err != nil {
		return replayStoreUnavailable(fmt.Errorf("close replay tombstone %q: %w", name, err))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.syncDirectory(); err != nil {
		return replayStoreUnavailable(fmt.Errorf("sync helper replay directory: %w", err))
	}
	return nil
}

// Close releases the retained directory handle without deleting replay evidence.
func (store *Store) Close() error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.root.Close()
}

// classifyExisting proves an existing tombstone represents the same claim before reporting a replay.
func (store *Store) classifyExisting(name string, expected replayRecord) error {
	file, err := store.root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return replayStoreUnavailable(fmt.Errorf("open existing replay tombstone %q: %w", name, err))
	}
	if err := store.validateOpened(name, file); err != nil {
		return errors.Join(replayStoreUnavailable(err), file.Close())
	}
	content, readErr := readBounded(file, replayRecordMaximumBytes)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return replayStoreUnavailable(errors.Join(readErr, closeErr))
	}
	record, err := decodeReplayRecord(content)
	if err != nil {
		return replayStoreUnavailable(fmt.Errorf("decode existing replay tombstone %q: %w", name, err))
	}
	if record != expected {
		return replayStoreUnavailable(fmt.Errorf("replay tombstone %q does not match its key", name))
	}
	return helper.ErrReplay
}

// validateOpened proves the retained handle is still the direct regular file named inside the rooted directory.
func (store *Store) validateOpened(name string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened replay tombstone %q: %w", name, err)
	}
	current, err := store.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect replay tombstone %q: %w", name, err)
	}
	if !opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("replay tombstone %q is not one direct regular file", name)
	}
	if err := validatePlatformFile(file, opened); err != nil {
		return fmt.Errorf("validate replay tombstone %q: %w", name, err)
	}
	return nil
}

// syncDirectory makes the newly linked replay tombstone durable before mutation begins.
func (store *Store) syncDirectory() error {
	directory, err := store.root.Open(".")
	if err != nil {
		return err
	}
	syncErr := platformSyncDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// replayRecordFromClaim freezes time without monotonic process metadata.
func replayRecordFromClaim(claim helper.ReplayClaim) replayRecord {
	return replayRecord{
		Version:             replayRecordVersion,
		InstallationID:      claim.Key.InstallationID,
		OwnershipGeneration: claim.Key.OwnershipGeneration,
		Nonce:               claim.Key.Nonce,
		ExpiresAt:           claim.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

// replayRecordName hides ticket identifiers while retaining deterministic exclusive-create semantics.
func replayRecordName(key helper.ReplayKey) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(replayRecordDomain))
	_, _ = fmt.Fprintf(hash, "%d:%s\x00%d:%s\x00", len(key.InstallationID), key.InstallationID, key.OwnershipGeneration, key.Nonce)
	return hex.EncodeToString(hash.Sum(nil)) + replayRecordSuffix
}

// encodeReplayRecord produces the only durable byte representation accepted on reload.
func encodeReplayRecord(record replayRecord) ([]byte, error) {
	content, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode replay tombstone: %w", err)
	}
	return append(content, '\n'), nil
}

// decodeReplayRecord rejects ignored fields, concatenated JSON, and noncanonical time or identity data.
func decodeReplayRecord(content []byte) (replayRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var record replayRecord
	if err := decoder.Decode(&record); err != nil {
		return replayRecord{}, err
	}
	if record.Version != replayRecordVersion {
		return replayRecord{}, fmt.Errorf("replay tombstone version is unsupported")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	if err != nil || expiresAt.Location() != time.UTC || expiresAt.Format(time.RFC3339Nano) != record.ExpiresAt {
		return replayRecord{}, fmt.Errorf("replay tombstone expiry is not canonical UTC")
	}
	claim := helper.ReplayClaim{
		Key: helper.ReplayKey{
			InstallationID:      record.InstallationID,
			OwnershipGeneration: record.OwnershipGeneration,
			Nonce:               record.Nonce,
		},
		ExpiresAt: expiresAt,
	}
	if err := claim.Key.Validate(); err != nil {
		return replayRecord{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return replayRecord{}, fmt.Errorf("replay tombstone contains trailing JSON")
	}
	canonical, err := encodeReplayRecord(record)
	if err != nil {
		return replayRecord{}, err
	}
	if !bytes.Equal(content, canonical) {
		return replayRecord{}, fmt.Errorf("replay tombstone encoding is not canonical")
	}
	return record, nil
}

// readBounded caps allocation before parsing untrusted durable bytes.
func readBounded(file *os.File, maximum int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("replay tombstone exceeds %d bytes", maximum)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("replay tombstone exceeds %d bytes", maximum)
	}
	return content, nil
}

// writeAll rejects zero-progress writes before a partial tombstone can be treated as durable admission.
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

// replayStoreUnavailable preserves the stable fail-closed classification across filesystem details.
func replayStoreUnavailable(err error) error {
	return errors.Join(helper.ErrReplayProtectionUnavailable, err)
}

var _ helper.ReplayGuard = (*Store)(nil)
