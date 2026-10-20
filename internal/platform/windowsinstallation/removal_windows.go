//go:build windows

package windowsinstallation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// deletionObject binds one fixed path to its already admitted bytes and native creation identity.
type deletionObject struct {
	path      string
	identity  fileIdentity
	size      int64
	digest    string
	directory bool
}

// Uninstall removes only the selected signed installation after the owned daemon task and machine integration settle.
func Uninstall(ctx context.Context) (result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireInstallerAuthority(); err != nil {
		return err
	}
	installation, err := Admit()
	if err != nil {
		return fmt.Errorf("admit installation for removal: %w", err)
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if installation.selection.UserSID != user.User.Sid.String() {
		return errors.New("uninstall belongs to a different interactive user SID")
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	self, err := openRetained(current, false)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, self.Close()) }()
	if err := installation.VerifyUninstallerCopy(self); err != nil {
		return err
	}
	agent, err := installation.Path("installer")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, agent, "--remove-task")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = taskEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("settle installed daemon task: %w: %s", err, strings.TrimSpace(string(output)))
	}
	guard, err := installation.HoldUninstallGuard(ctx, self)
	if err != nil {
		return fmt.Errorf("acquire stopped-runtime removal lease: %w", err)
	}
	defer func() { result = errors.Join(result, guard.Close()) }()
	ctx = guard.Context()
	return WithMachineTransaction(ctx, func() error {
		return removeInstallation(ctx, installation, guard)
	})
}

// removeInstallation runs only after helper writers are excluded and the low worker owns the daemon byte lock.
func removeInstallation(ctx context.Context, installation *Installation, guard *UninstallGuard) (result error) {
	if err := guard.Check(); err != nil {
		return err
	}
	if err := installation.Task(ctx, "absent"); err != nil {
		return fmt.Errorf("observe owned task absence: %w", err)
	}
	if err := validateUninstallNetworking(ctx, installation); err != nil {
		return fmt.Errorf("observe released networking: %w", err)
	}
	if err := installation.ValidateMachineIntegration(ctx); err != nil {
		return fmt.Errorf("admit machine projections before removal: %w", err)
	}
	objects, err := snapshotDeletion(installation)
	if err != nil {
		return fmt.Errorf("snapshot admitted payload before deletion: %w", err)
	}
	if err := guard.Check(); err != nil {
		return err
	}
	if err := installation.RemoveMachineIntegration(ctx, guard.Check); err != nil {
		return fmt.Errorf("remove admitted machine projections: %w", err)
	}
	if err := installation.Close(); err != nil {
		return fmt.Errorf("release payload admission before deletion: %w", err)
	}
	if err := deletePayload(ctx, objects, guard.Check); err != nil {
		return fmt.Errorf("remove admitted payload: %w", err)
	}
	return nil
}

// deletePayload checks every exact object before effects and checks the owned worker before each deletion.
func deletePayload(ctx context.Context, objects []deletionObject, check func() error) (result error) {
	files, err := awaitDeletion(ctx, objects)
	if err != nil {
		return err
	}
	defer func() {
		for _, file := range files {
			if file != nil {
				result = errors.Join(result, file.Close())
			}
		}
	}()
	for index, file := range files {
		if err := check(); err != nil {
			return err
		}
		remove := uint32(1)
		err := windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4)
		if err != nil && !(objects[index].directory && errors.Is(err, windows.ERROR_DIR_NOT_EMPTY)) {
			return fmt.Errorf("remove exact admitted component %q: %w", file.Name(), err)
		}
		// Closing each successfully processed exact handle lets a later parent directory become empty.
		closeErr := file.Close()
		files[index] = nil
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// snapshotDeletion records immutable native identity and bytes while admission still excludes writes and replacement.
func snapshotDeletion(installation *Installation) ([]deletionObject, error) {
	var objects []deletionObject
	seen := make(map[string]bool)
	for _, file := range installation.handles {
		information, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if seen[strings.ToLower(file.Name())] {
			continue
		}
		seen[strings.ToLower(file.Name())] = true
		if information.IsDir() && !strings.EqualFold(file.Name(), installation.root) && !strings.HasPrefix(strings.ToLower(file.Name()), strings.ToLower(installation.root+string(filepath.Separator))) {
			continue
		}
		identity, err := identifyFile(windows.Handle(file.Fd()))
		if err != nil {
			return nil, err
		}
		if information.IsDir() {
			objects = append(objects, deletionObject{path: file.Name(), identity: identity, directory: true})
			continue
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return nil, err
		}
		objects = append(objects, deletionObject{path: file.Name(), identity: identity, size: information.Size(), digest: hex.EncodeToString(hash.Sum(nil))})
	}
	sort.Slice(objects, func(left, right int) bool {
		if objects[left].directory != objects[right].directory {
			return !objects[left].directory
		}
		return len(objects[left].path) > len(objects[right].path)
	})
	return objects, nil
}

// retainDeletion validates every reopened delete-capable object before the first deletion is requested.
func retainDeletion(objects []deletionObject) (_ []*os.File, result error) {
	var files []*os.File
	defer func() {
		if result != nil {
			for _, file := range files {
				result = errors.Join(result, file.Close())
			}
		}
	}()
	for _, object := range objects {
		flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
		access := uint32(windows.GENERIC_READ | windows.READ_CONTROL | windows.DELETE)
		if object.directory {
			flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
			access = windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.DELETE
		}
		handle, err := windows.CreateFile(windows.StringToUTF16Ptr(object.path), access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
		if err != nil {
			return nil, fmt.Errorf("reopen exact payload for deletion %q: %w", object.path, err)
		}
		file := os.NewFile(uintptr(handle), object.path)
		files = append(files, file)
		var information windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
			return nil, err
		}
		if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != object.directory || (!object.directory && information.NumberOfLinks != 1) {
			return nil, errors.New("preserve aliased or nonregular component during uninstall")
		}
		identity, err := identifyFile(handle)
		if err != nil {
			return nil, err
		}
		if identity != object.identity {
			return nil, errors.New("preserve replaced native component during uninstall")
		}
		final, err := windows.GetFinalPathNameByHandle(handle, nil, 0, 0)
		if err != nil {
			return nil, err
		}
		buffer := make([]uint16, final+1)
		if _, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0); err != nil {
			return nil, err
		}
		if !strings.EqualFold(windows.UTF16ToString(buffer), `\\?\`+object.path) {
			return nil, errors.New("preserve aliased cleanup path")
		}
		if err := admitSecurity(handle, object.directory); err != nil {
			return nil, err
		}
		if object.directory {
			continue
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		if err != nil {
			return nil, err
		}
		if size != object.size || hex.EncodeToString(hash.Sum(nil)) != object.digest {
			return nil, errors.New("preserve modified component during uninstall")
		}
	}
	return files, nil
}

// validateUninstallNetworking observes host trust and NRPT through the fixed signed read-only policy before any cleanup effect.
func validateUninstallNetworking(ctx context.Context, installation *Installation) error {
	policy, err := installation.Path("uninstall-policy")
	if err != nil {
		return err
	}
	powershell, err := taskPowerShell()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "AllSigned", "-File", policy, "-ValidateOnly")
	command.Env = taskEnvironment(os.Environ())
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("validate released Harbor networking: %w: %.4096s", err, output)
	}
	return nil
}

// awaitDeletion joins ordinary owned bootstrap image unloading without signaling any process or accepting a changed object.
func awaitDeletion(ctx context.Context, objects []deletionObject) ([]*os.File, error) {
	settlement, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := settlement.Err(); err != nil {
			return nil, err
		}
		files, err := retainDeletion(objects)
		if err == nil {
			return files, nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, err
		}
		select {
		case <-settlement.Done():
			return nil, errors.Join(err, settlement.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
