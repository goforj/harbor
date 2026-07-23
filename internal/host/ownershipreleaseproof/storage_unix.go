//go:build darwin || linux

package ownershipreleaseproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	proofFileMode            = 0o644
	proofLockMode            = 0o600
	proofDirectoryMode       = 0o711
	maximumProofBytes  int64 = 16 * 1024
)

// RootWriter writes a proof only while holding the fixed root-owned cross-process lock.
type RootWriter struct {
	path     string
	lockPath string
	owner    uint32
}

// Observer reads the root-authored proof without write authority.
type Observer struct {
	path  string
	owner uint32
}

// newRootWriter rejects arbitrary authority and requires root before durable mutation is possible.
func newRootWriter(path, lockPath string) (*RootWriter, error) {
	if os.Geteuid() != 0 {
		return nil, ErrNotRoot
	}
	return newWriterForOwner(path, lockPath, 0)
}

// newWriterForOwner creates test-only writer fixtures with an explicit expected owner.
func newWriterForOwner(path, lockPath string, owner uint32) (*RootWriter, error) {
	if err := validateFixedPath(path, "ownership-release-proof.json"); err != nil {
		return nil, err
	}
	if err := validateFixedPath(lockPath, "ownership-release-proof.lock"); err != nil {
		return nil, err
	}
	if filepath.Dir(path) != filepath.Dir(lockPath) {
		return nil, fmt.Errorf("%w: proof and lock do not share the fixed machine root", ErrUnsafePath)
	}
	if err := validateProofDirectory(filepath.Dir(path), owner); err != nil {
		return nil, err
	}
	if err := ensureLock(lockPath, owner); err != nil {
		return nil, err
	}
	return &RootWriter{path: path, lockPath: lockPath, owner: owner}, nil
}

// newObserver defers filesystem validation until read time so optional helper-owned storage does not block daemon assembly.
func newObserver(path string) (*Observer, error) {
	return newObserverForOwner(path, 0)
}

// newObserverForOwner creates test-only observer fixtures with an explicit expected owner.
func newObserverForOwner(path string, owner uint32) (*Observer, error) {
	if err := validateFixedPath(path, "ownership-release-proof.json"); err != nil {
		return nil, err
	}
	return &Observer{path: path, owner: owner}, nil
}

// complete serializes the whole mutation proof, allowing only same-authority recovery to retain initial ticket evidence.
func (writer *RootWriter) complete(ctx context.Context, request Request, transaction Transaction, verifiedAt time.Time) (Proof, error) {
	if err := contextErr(ctx); err != nil {
		return Proof{}, err
	}
	candidate := requestProof(request, verifiedAt)
	if err := validateProof(candidate); err != nil {
		return Proof{}, fmt.Errorf("complete ownership release proof: %w", err)
	}
	if transaction.CompareAndSwap == nil || transaction.ObserveOwnership == nil {
		return Proof{}, errors.New("complete ownership release proof: ownership callbacks are required")
	}
	lock, err := openLock(writer.lockPath, writer.owner)
	if err != nil {
		return Proof{}, err
	}
	defer lock.Close()
	if err := flockContext(ctx, lock); err != nil {
		return Proof{}, err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	current, exists, err := readProof(writer.path, writer.owner)
	if err != nil {
		return Proof{}, err
	}
	if exists {
		if !sameAuthority(current, request.Authority()) {
			present, err := transaction.ObserveOwnership(ctx)
			if err != nil {
				return Proof{}, fmt.Errorf("observe ownership for proof rollover: %w", err)
			}
			if !present {
				return Proof{}, ErrAbsentProof
			}
			if err := writeProof(writer.path, writer.owner, candidate); err != nil {
				return Proof{}, err
			}
			current = candidate
		}
		present, err := transaction.ObserveOwnership(ctx)
		if err != nil {
			return Proof{}, fmt.Errorf("observe ownership for proof recovery: %w", err)
		}
		if current.State == StateReleased {
			if present {
				return Proof{}, errors.New("released ownership proof found ownership present")
			}
			return current, nil
		}
		if !present {
			return writer.releaseLocked(current, verifiedAt)
		}
		if err := transaction.CompareAndSwap(ctx); err != nil {
			return Proof{}, fmt.Errorf("compare and swap ownership for proof recovery: %w", err)
		}
		present, err = transaction.ObserveOwnership(ctx)
		if err != nil {
			return Proof{}, fmt.Errorf("observe released ownership for proof recovery: %w", err)
		}
		if present {
			return Proof{}, errors.New("ownership remains present after compare and swap")
		}
		return writer.releaseLocked(current, verifiedAt)
	}
	if err := writeProof(writer.path, writer.owner, candidate); err != nil {
		return Proof{}, err
	}
	if err := transaction.CompareAndSwap(ctx); err != nil {
		return Proof{}, fmt.Errorf("compare and swap ownership: %w", err)
	}
	present, err := transaction.ObserveOwnership(ctx)
	if err != nil {
		return Proof{}, fmt.Errorf("observe released ownership: %w", err)
	}
	if present {
		return Proof{}, errors.New("ownership remains present after compare and swap")
	}
	return writer.releaseLocked(candidate, verifiedAt)
}

// releaseLocked promotes exactly the pending proof observed under the held cross-process lock.
func (writer *RootWriter) releaseLocked(proof Proof, verifiedAt time.Time) (Proof, error) {
	proof.State, proof.VerifiedAt = StateReleased, verifiedAt.UTC()
	if err := validateProof(proof); err != nil {
		return Proof{}, err
	}
	if err := writeProof(writer.path, writer.owner, proof); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

// observe permits missing proof distinction for diagnostics.
func (observer *Observer) observe(ctx context.Context) (Proof, bool, error) {
	if err := contextErr(ctx); err != nil {
		return Proof{}, false, err
	}
	return readProof(observer.path, observer.owner)
}

// ensureLock creates only the root-owned lock file required to serialize writers.
func ensureLock(path string, owner uint32) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, proofLockMode)
		if createErr != nil {
			return createErr
		}
		if syncErr := file.Sync(); syncErr != nil {
			file.Close()
			return syncErr
		}
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
		parent, openErr := os.Open(filepath.Dir(path))
		if openErr != nil {
			return openErr
		}
		defer parent.Close()
		return parent.Sync()
	}
	if err != nil {
		return err
	}
	return validateLockFile(info, owner)
}

// openLock retains a no-follow descriptor after validating its stable file identity.
func openLock(path string, owner uint32) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := validateLockFile(info, owner); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		return nil, unix.Close(fd)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("%w: ownership release proof lock changed while opening", ErrUnsafePath)
	}
	if err := validateLockFile(opened, owner); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// flockContext waits in short non-blocking intervals so callers can cancel before mutation begins.
func flockContext(ctx context.Context, file *os.File) error {
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return nil
		} else if err != unix.EWOULDBLOCK {
			return err
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

// readProof opens with O_NOFOLLOW and validates canonical proof JSON.
func readProof(path string, owner uint32) (Proof, bool, error) {
	_, err := os.Lstat(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return Proof{}, false, nil
	}
	if err != nil {
		return Proof{}, false, fmt.Errorf("%w: inspect proof directory: %v", ErrUnsafePath, err)
	}
	if err := validateProofDirectory(filepath.Dir(path), owner); err != nil {
		return Proof{}, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Proof{}, false, nil
	}
	if err != nil {
		return Proof{}, false, err
	}
	if err := validateProofFile(info, owner); err != nil {
		return Proof{}, false, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Proof{}, false, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		return Proof{}, false, unix.Close(fd)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Proof{}, false, fmt.Errorf("%w: ownership release proof changed while opening", ErrUnsafePath)
	}
	if err := validateProofFile(opened, owner); err != nil {
		return Proof{}, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumProofBytes+1))
	if err != nil {
		return Proof{}, false, err
	}
	if int64(len(data)) > maximumProofBytes {
		return Proof{}, false, errors.New("ownership release proof exceeds maximum size")
	}
	if err := validateUniqueJSONObject(data); err != nil {
		return Proof{}, false, fmt.Errorf("validate ownership release proof JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proof Proof
	if err := decoder.Decode(&proof); err != nil {
		return Proof{}, false, fmt.Errorf("decode ownership release proof: %w", err)
	}
	if decoder.More() {
		return Proof{}, false, errors.New("ownership release proof has trailing JSON")
	}
	if err := validateProof(proof); err != nil {
		return Proof{}, false, err
	}
	canonical, err := encodeProof(proof)
	if err != nil || !bytes.Equal(canonical, data) {
		return Proof{}, false, errors.New("ownership release proof is not canonical JSON")
	}
	return proof, true, nil
}

// encodeProof keeps the write and canonical-read representation identical.
func encodeProof(proof Proof) ([]byte, error) {
	return json.Marshal(proof)
}

// validateUniqueJSONObject rejects duplicate keys before decoding would otherwise discard evidence.
func validateUniqueJSONObject(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("proof is not an object")
	}
	names := map[string]struct{}{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("proof key is not a string")
		}
		if _, exists := names[name]; exists {
			return errors.New("proof has a duplicate key")
		}
		names[name] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("proof has trailing JSON")
	}
	return nil
}

// writeProof durably publishes canonical evidence after syncing both file and directory.
func writeProof(path string, owner uint32, proof Proof) error {
	if err := validateProofDirectory(filepath.Dir(path), owner); err != nil {
		return err
	}
	encoded, err := encodeProof(proof)
	if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ownership-release-proof-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(proofFileMode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return directory.Sync()
}

// validateFixedPath requires canonical absolute fixed-name storage.
func validateFixedPath(path, name string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != name {
		return fmt.Errorf("%w: ownership release proof path is not fixed", ErrUnsafePath)
	}
	return nil
}

// validateProofDirectory requires the root machine gateway policy used by ticket redemption.
func validateProofDirectory(path string, owner uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect proof directory: %v", ErrUnsafePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrUnsafePath
	}
	const bits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if info.Mode()&bits != proofDirectoryMode {
		return ErrUnsafePath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner {
		return ErrUnsafePath
	}
	return nil
}

// validateProofFile requires one root-owned daemon-readable direct name.
func validateProofFile(info os.FileInfo, owner uint32) error {
	return validateProtectedFile(info, owner, proofFileMode)
}

// validateLockFile requires one root-owned helper-only direct name.
func validateLockFile(info os.FileInfo, owner uint32) error {
	return validateProtectedFile(info, owner, proofLockMode)
}

// validateProtectedFile enforces strict type, mode, owner, and link-count checks.
func validateProtectedFile(info os.FileInfo, owner uint32, mode os.FileMode) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	const bits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode()&bits != mode || !ok || stat.Uid != owner || stat.Nlink != 1 {
		return ErrUnsafePath
	}
	return nil
}

// contextErr normalizes nil contexts while preserving cancellation before storage access.
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
