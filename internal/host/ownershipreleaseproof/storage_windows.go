//go:build windows

package ownershipreleaseproof

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
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsAdministratorsSID       = "S-1-5-32-544"
	windowsSystemSID               = "S-1-5-18"
	windowsFileAllAccess           = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	windowsFileExecuteAccess       = windows.STANDARD_RIGHTS_EXECUTE | windows.SYNCHRONIZE | windows.FILE_READ_ATTRIBUTES | windows.FILE_EXECUTE
	maximumProofBytes        int64 = 16 * 1024
)

// windowsACEPolicy defines one exact grant in a protected Windows DACL.
type windowsACEPolicy struct {
	mask       windows.ACCESS_MASK
	mappedMask windows.ACCESS_MASK
	flags      uint8
}

// RootWriter writes proof through the elevated Administrators boundary while holding the fixed cross-process lock.
type RootWriter struct {
	path      string
	lockPath  string
	requester string
}

// Observer reads root-authored proof through the admitted interactive user's read-only grant.
type Observer struct {
	path      string
	requester string
}

// newRootWriter rejects non-elevated callers before durable machine-state mutation is possible.
func newRootWriter(path, lockPath string) (*RootWriter, error) {
	if err := validateWindowsWriterAdmission(); err != nil {
		return nil, err
	}
	requester, err := currentWindowsUserSID()
	if err != nil {
		return nil, err
	}
	if err := validateFixedPath(path, "ownership-release-proof.json"); err != nil {
		return nil, err
	}
	if err := validateFixedPath(lockPath, "ownership-release-proof.lock"); err != nil {
		return nil, err
	}
	if filepath.Dir(path) != filepath.Dir(lockPath) {
		return nil, fmt.Errorf("%w: proof and lock do not share the fixed machine root", ErrUnsafePath)
	}
	if err := validateProofDirectory(filepath.Dir(path), requester); err != nil {
		return nil, err
	}
	if err := ensureLock(lockPath, requester); err != nil {
		return nil, err
	}
	return &RootWriter{path: path, lockPath: lockPath, requester: requester}, nil
}

// newObserver defers filesystem validation until read time so optional helper-owned storage does not block daemon assembly.
func newObserver(path string) (*Observer, error) {
	if err := validateFixedPath(path, "ownership-release-proof.json"); err != nil {
		return nil, err
	}
	requester, err := currentWindowsUserSID()
	if err != nil {
		return nil, err
	}
	return &Observer{path: path, requester: requester}, nil
}

// complete serializes the entire ownership mutation and preserves the first ticket evidence for same-authority recovery.
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
	lock, err := openLock(writer.lockPath, writer.requester)
	if err != nil {
		return Proof{}, err
	}
	defer lock.Close()
	if err := lockContext(ctx, lock); err != nil {
		return Proof{}, err
	}
	defer unlock(lock)

	current, exists, err := readProof(writer.path, writer.requester)
	if err != nil {
		return Proof{}, err
	}
	if exists {
		if !sameAuthority(current, request.Authority()) {
			present, observeErr := transaction.ObserveOwnership(ctx)
			if observeErr != nil {
				return Proof{}, fmt.Errorf("observe ownership for proof rollover: %w", observeErr)
			}
			if !present {
				return Proof{}, ErrAbsentProof
			}
			if err := writeProof(writer.path, writer.requester, candidate); err != nil {
				return Proof{}, err
			}
			current = candidate
		}
		present, observeErr := transaction.ObserveOwnership(ctx)
		if observeErr != nil {
			return Proof{}, fmt.Errorf("observe ownership for proof recovery: %w", observeErr)
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
		present, observeErr = transaction.ObserveOwnership(ctx)
		if observeErr != nil {
			return Proof{}, fmt.Errorf("observe released ownership for proof recovery: %w", observeErr)
		}
		if present {
			return Proof{}, errors.New("ownership remains present after compare and swap")
		}
		return writer.releaseLocked(current, verifiedAt)
	}
	if err := writeProof(writer.path, writer.requester, candidate); err != nil {
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
	if err := writeProof(writer.path, writer.requester, proof); err != nil {
		return Proof{}, err
	}
	return proof, nil
}

// observe permits missing proof distinction for diagnostics.
func (observer *Observer) observe(ctx context.Context) (Proof, bool, error) {
	if err := contextErr(ctx); err != nil {
		return Proof{}, false, err
	}
	return readProof(observer.path, observer.requester)
}

// validateWindowsWriterAdmission requires the same high-token Administrators boundary as ticket redemption.
func validateWindowsWriterAdmission() error {
	token := windows.GetCurrentProcessToken()
	if !token.IsElevated() {
		return ErrNotRoot
	}
	administrators, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		return fmt.Errorf("resolve Windows Administrators SID: %w", err)
	}
	member, err := token.IsMember(administrators)
	if err != nil {
		return fmt.Errorf("inspect Windows Administrators membership: %w", err)
	}
	if !member {
		return ErrNotRoot
	}
	return nil
}

// currentWindowsUserSID returns the interactive identity retained by the elevated helper token.
func currentWindowsUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

// ensureLock creates the machine-only lock with its final DACL in the creation syscall.
func ensureLock(path, requester string) error {
	file, err := openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err == nil {
		defer file.Close()
		return validateWindowsObject(file, false, windowsAdministratorsSID, machineFilePolicy())
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	descriptor, err := windowsProofDescriptor("", false)
	if err != nil {
		return err
	}
	file, err = openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.CREATE_NEW, descriptor)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ensureLock(path, requester)
		}
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	file, err = openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		return err
	}
	defer file.Close()
	return validateWindowsObject(file, false, windowsAdministratorsSID, machineFilePolicy())
}

// openLock retains and validates the exact no-follow lock handle used by LockFileEx.
func openLock(path, requester string) (*os.File, error) {
	if err := validateProofDirectory(filepath.Dir(path), requester); err != nil {
		return nil, err
	}
	file, err := openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		return nil, err
	}
	if err := validateWindowsObject(file, false, windowsAdministratorsSID, machineFilePolicy()); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// lockContext retries a nonblocking byte-range lock so cancellation remains responsive.
func lockContext(ctx context.Context, file *os.File) error {
	for {
		overlapped := new(windows.Overlapped)
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

// unlock releases the byte-range transaction lock before the handle closes.
func unlock(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// readProof validates the protected root and proof handle before decoding canonical JSON.
func readProof(path, requester string) (Proof, bool, error) {
	if _, err := os.Stat(filepath.Dir(path)); errors.Is(err, fs.ErrNotExist) {
		return Proof{}, false, nil
	} else if err != nil {
		return Proof{}, false, fmt.Errorf("%w: inspect proof directory: %v", ErrUnsafePath, err)
	}
	if err := validateProofDirectory(filepath.Dir(path), requester); err != nil {
		return Proof{}, false, err
	}
	file, err := openWindowsFile(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return Proof{}, false, nil
	}
	if err != nil {
		return Proof{}, false, err
	}
	defer file.Close()
	if err := validateWindowsObject(file, false, windowsAdministratorsSID, proofFilePolicy(requester)); err != nil {
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

// writeProof atomically replaces the proof with creation-time protected canonical evidence.
func writeProof(path, requester string, proof Proof) error {
	if err := validateProofDirectory(filepath.Dir(path), requester); err != nil {
		return err
	}
	if existing, err := openWindowsFile(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, nil); err == nil {
		validateErr := validateWindowsObject(existing, false, windowsAdministratorsSID, proofFilePolicy(requester))
		closeErr := existing.Close()
		if err := errors.Join(validateErr, closeErr); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	encoded, err := encodeProof(proof)
	if err != nil {
		return err
	}
	descriptor, err := windowsProofDescriptor(requester, false)
	if err != nil {
		return err
	}
	temporaryPath, temporary, err := createTemporaryProof(filepath.Dir(path), descriptor)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
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
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return nil
}

// createTemporaryProof creates a collision-resistant direct child with the final proof descriptor.
func createTemporaryProof(directory string, descriptor *windows.SECURITY_DESCRIPTOR) (string, *os.File, error) {
	for attempt := 0; attempt < 10; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", nil, err
		}
		path := filepath.Join(directory, ".ownership-release-proof-"+hex.EncodeToString(random))
		file, err := openWindowsFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.CREATE_NEW, descriptor)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return path, file, err
	}
	return "", nil, errors.New("create ownership release proof temporary file: collision limit exceeded")
}

// openWindowsFile opens the final directory entry itself and optionally supplies a creation-time descriptor.
func openWindowsFile(path string, access uint32, disposition uint32, descriptor *windows.SECURITY_DESCRIPTOR) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var attributes *windows.SecurityAttributes
	if descriptor != nil {
		attributes = &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: descriptor,
		}
	}
	handle, err := windows.CreateFile(
		pathPointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		attributes,
		disposition,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(errors.New("retain ownership release proof handle"), windows.CloseHandle(handle))
	}
	return file, nil
}

// validateFixedPath requires canonical absolute fixed-name storage.
func validateFixedPath(path, name string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != name {
		return fmt.Errorf("%w: ownership release proof path is not fixed", ErrUnsafePath)
	}
	return nil
}

// validateProofDirectory requires the installer-authored gateway policy used by ticket redemption.
func validateProofDirectory(path, requester string) error {
	file, err := openWindowsDirectory(path)
	if err != nil {
		return fmt.Errorf("%w: inspect proof directory: %v", ErrUnsafePath, err)
	}
	defer file.Close()
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	return validateWindowsObject(file, true, windowsAdministratorsSID, map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL, flags: inherit},
		windowsSystemSID:         {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL, flags: inherit},
		requester:                {mask: windowsFileExecuteAccess, mappedMask: windows.GENERIC_EXECUTE, flags: inherit},
	})
}

// openWindowsDirectory retains the final directory entry without following a reparse point.
func openWindowsDirectory(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES|windows.FILE_TRAVERSE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// validateWindowsObject proves the retained object's type, direct identity, owner, and exact protected DACL.
func validateWindowsObject(file *os.File, directory bool, ownerSID string, policy map[string]windowsACEPolicy) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return err
	}
	if native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !directory && native.NumberOfLinks != 1 {
		return ErrUnsafePath
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnsafePath
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != ownerSID {
		return ErrUnsafePath
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || int(dacl.AceCount) != len(policy) {
		return ErrUnsafePath
	}
	seen := make(map[string]bool, len(policy))
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnsafePath
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		expected, exists := policy[principal]
		if !exists || seen[principal] ||
			(ace.Mask != expected.mask && ace.Mask != expected.mappedMask) ||
			ace.Header.AceFlags != expected.flags {
			return ErrUnsafePath
		}
		seen[principal] = true
	}
	for principal := range policy {
		if !seen[principal] {
			return ErrUnsafePath
		}
	}
	return nil
}

// machineFilePolicy grants proof-lock access only to Administrators and LocalSystem.
func machineFilePolicy() map[string]windowsACEPolicy {
	return map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL},
		windowsSystemSID:         {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL},
	}
}

// proofFilePolicy adds only read access for the daemon's admitted interactive identity.
func proofFilePolicy(requester string) map[string]windowsACEPolicy {
	return map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL},
		windowsSystemSID:         {mask: windowsFileAllAccess, mappedMask: windows.GENERIC_ALL},
		requester:                {mask: windows.FILE_GENERIC_READ, mappedMask: windows.GENERIC_READ},
	}
}

// windowsProofDescriptor constructs either the machine-only lock policy or daemon-readable proof policy.
func windowsProofDescriptor(requester string, directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%sD:P(A;%s;FA;;;%s)(A;%s;FA;;;%s)",
		windowsAdministratorsSID,
		inheritance,
		windowsAdministratorsSID,
		inheritance,
		windowsSystemSID,
	)
	if requester != "" {
		sddl += fmt.Sprintf("(A;%s;GR;;;%s)", inheritance, requester)
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build ownership release proof Windows descriptor: %w", err)
	}
	return descriptor, nil
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

// contextErr normalizes nil contexts while preserving cancellation before storage access.
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
