//go:build windows

package windowsinstallation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

const uninstallGuardStageSDDL = "O:BAG:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)S:P(ML;OICI;NW;;;HI)"

// UninstallGuard retains the restricted worker that holds the user's daemon byte lock during machine removal.
type UninstallGuard struct {
	child   *Child
	ready   windows.Handle
	release windows.Handle
	files   []*os.File
	context context.Context
	cancel  context.CancelCauseFunc
	settled chan struct{}
	outcome uninstallGuardOutcome
	closing atomic.Bool
	mutex   sync.Mutex
	closed  bool
}

// uninstallGuardOutcome records one retained child's settlement without replacing its native process authority with a PID.
type uninstallGuardOutcome struct {
	code uint32
	err  error
}

// HoldUninstallGuard admits one exact protected signed copy and waits until its actual restricted worker owns the daemon lock.
func (installation *Installation) HoldUninstallGuard(ctx context.Context, staged *os.File) (_ *UninstallGuard, result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if staged == nil {
		return nil, errors.New("uninstall guard requires its retained staged maintenance image")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	acquisition, cancelAcquisition := context.WithTimeout(ctx, time.Minute)
	defer cancelAcquisition()
	guard := &UninstallGuard{settled: make(chan struct{})}
	guard.context, guard.cancel = context.WithCancelCause(ctx)
	defer func() {
		if result != nil {
			guard.cancel(result)
			if guard.child != nil {
				result = errors.Join(result, guard.Close())
			} else {
				result = errors.Join(result, guard.closeObjects())
			}
		}
	}()
	files, err := installation.retainUninstallGuardStage(staged)
	if err != nil {
		return nil, err
	}
	guard.files = files
	user := installation.selection.UserSID
	descriptor, err := windows.SecurityDescriptorFromString("O:" + user + "D:P(A;;GA;;;" + user + ")(A;;GA;;;SY)")
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	guard.ready, err = windows.CreateEvent(attributes, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	guard.release, err = windows.CreateEvent(attributes, 1, 0, nil)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, err
	}
	var readyChild, releaseChild windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), guard.ready, windows.CurrentProcess(), &readyChild, windows.EVENT_MODIFY_STATE, true, 0); err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(readyChild)) }()
	if err := windows.DuplicateHandle(windows.CurrentProcess(), guard.release, windows.CurrentProcess(), &releaseChild, windows.SYNCHRONIZE, true, 0); err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(releaseChild)) }()
	arguments := []string{"--hold-stopped", "--ready-handle=" + strconv.FormatUint(uint64(readyChild), 10), "--release-handle=" + strconv.FormatUint(uint64(releaseChild), 10)}
	guard.child, err = installation.startAdmitted(guard.files[len(guard.files)-1].Name(), "uninstall-guard", arguments, []windows.Handle{readyChild, releaseChild})
	if err != nil {
		return nil, fmt.Errorf("start restricted uninstall guard: %w", err)
	}
	go guard.observe()
	for {
		if err := acquisition.Err(); err != nil {
			return nil, fmt.Errorf("wait for uninstall daemon lock: %w", err)
		}
		if err := guard.context.Err(); err != nil {
			return nil, context.Cause(guard.context)
		}
		state, err := windows.WaitForMultipleObjects([]windows.Handle{guard.child.process, guard.ready}, false, 50)
		if err != nil {
			return nil, err
		}
		switch state {
		case windows.WAIT_OBJECT_0:
			return nil, errors.New("uninstall guard settled without retaining the daemon lock")
		case windows.WAIT_OBJECT_0 + 1:
			if err := guard.Check(); err != nil {
				return nil, err
			}
			return guard, nil
		case uint32(windows.WAIT_TIMEOUT):
		default:
			return nil, errors.New("uninstall guard returned an unexpected native readiness state")
		}
	}
}

// Context cancels when the caller cancels or the exact retained worker loses its lease, so cleanup can stop promptly.
func (guard *UninstallGuard) Context() context.Context { return guard.context }

// Check refuses another machine or payload effect after cancellation, release, or an observed worker exit.
func (guard *UninstallGuard) Check() error {
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if guard.closed || guard.closing.Load() {
		return errors.New("uninstall guard has been released")
	}
	if err := guard.context.Err(); err != nil {
		return context.Cause(guard.context)
	}
	state, err := windows.WaitForSingleObject(guard.child.process, 0)
	if err != nil {
		return err
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return errors.New("uninstall guard no longer retains the daemon lock")
	}
	return nil
}

// observe cancels guarded operations if their exact worker exits before the elevated transaction releases it.
func (guard *UninstallGuard) observe() {
	guard.outcome.code, guard.outcome.err = guard.child.Wait(context.Background())
	if !guard.closing.Load() {
		guard.cancel(fmt.Errorf("uninstall guard exited unexpectedly (exit %d): %w", guard.outcome.code, errors.Join(errors.New("daemon lock lease was lost"), guard.outcome.err)))
	}
	close(guard.settled)
}

// Close releases the worker after cleanup and retains native receipts until its exact process has settled.
func (guard *UninstallGuard) Close() error {
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if guard.closed {
		return nil
	}
	var leaseErr error
	if guard.context.Err() != nil {
		leaseErr = context.Cause(guard.context)
	}
	if !guard.closing.Load() {
		state, err := windows.WaitForSingleObject(guard.child.process, 0)
		if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
			leaseErr = errors.Join(leaseErr, errors.New("uninstall guard settled before release"), err)
		}
	}
	guard.closing.Store(true)
	releaseErr := windows.SetEvent(guard.release)
	if releaseErr != nil {
		guard.cancel(releaseErr)
		releaseErr = errors.Join(releaseErr, guard.child.abort())
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	select {
	case <-guard.settled:
	case <-timer.C:
		timeout := errors.New("uninstall guard did not settle after release")
		guard.cancel(timeout)
		abortErr := guard.child.abort()
		select {
		case <-guard.settled:
			releaseErr = errors.Join(releaseErr, timeout, abortErr)
		case <-time.After(2 * time.Second):
			return errors.Join(leaseErr, releaseErr, timeout, abortErr)
		}
	}
	if err := guard.child.Close(); err != nil {
		return errors.Join(leaseErr, releaseErr, guard.outcome.err, err)
	}
	var exitErr error
	if guard.outcome.code != 0 {
		exitErr = fmt.Errorf("uninstall guard exited with code %d", guard.outcome.code)
	}
	result := errors.Join(leaseErr, releaseErr, guard.outcome.err, exitErr, guard.closeObjects())
	guard.closed = true
	guard.cancel(context.Canceled)
	return result
}

// closeObjects releases partially created events and protected stage objects without deleting staging evidence.
func (guard *UninstallGuard) closeObjects() (result error) {
	for _, handle := range []windows.Handle{guard.release, guard.ready} {
		if handle != 0 {
			result = errors.Join(result, windows.CloseHandle(handle))
		}
	}
	guard.release, guard.ready = 0, 0
	for index := len(guard.files) - 1; index >= 0; index-- {
		result = errors.Join(result, guard.files[index].Close())
	}
	guard.files = nil
	return result
}

// RunUninstallGuard verifies actual unprivileged authority before an injected daemon preparation lease may open user state.
func RunUninstallGuard(ready, release windows.Handle, prepare func(func() error) error) (result error) {
	facts, err := InspectCurrentToken()
	if err != nil {
		return err
	}
	if facts.Elevation != 0 {
		return errors.New("uninstall guard requires actual non-elevated authority")
	}
	if ready == release {
		return errors.New("uninstall guard requires distinct inherited events")
	}
	if err := admitUninstallGuardEvent(ready, windows.EVENT_MODIFY_STATE); err != nil {
		return err
	}
	if err := admitUninstallGuardEvent(release, windows.SYNCHRONIZE); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(ready), windows.CloseHandle(release)) }()
	if err := windows.SetHandleInformation(ready, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return err
	}
	if err := windows.SetHandleInformation(release, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return err
	}
	return holdUninstallGuardEvents(ready, release, prepare)
}

// holdUninstallGuardEvents signals readiness only inside the held daemon byte-lock callback and releases only on its private event.
func holdUninstallGuardEvents(ready, release windows.Handle, prepare func(func() error) error) error {
	return prepare(func() error {
		if err := windows.SetEvent(ready); err != nil {
			return err
		}
		state, err := windows.WaitForSingleObject(release, windows.INFINITE)
		if err != nil {
			return err
		}
		if state != windows.WAIT_OBJECT_0 {
			return errors.New("uninstall guard received an unexpected release state")
		}
		return nil
	})
}

// uninstallObjectTypeName matches the native UNICODE_STRING prefix returned by ObjectTypeInformation.
type uninstallObjectTypeName struct {
	Length, MaximumLength uint16
	Buffer                *uint16
}

// admitUninstallGuardEvent admits only inherited Event capabilities with the exact direction-specific access mask.
func admitUninstallGuardEvent(handle windows.Handle, rights uint32) error {
	var flags uint32
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(handle), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 {
		return err
	}
	if flags&windows.HANDLE_FLAG_INHERIT == 0 {
		return errors.New("uninstall guard event was not inherited")
	}
	query := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
	var basic struct {
		Attributes, GrantedAccess, HandleCount, PointerCount uint32
		Reserved                                             [10]uint32
	}
	var length uint32
	status, _, _ := query.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&basic)), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&length)))
	if status != 0 || length != uint32(unsafe.Sizeof(basic)) || basic.GrantedAccess != rights {
		return fmt.Errorf("uninstall guard event has unexpected native access rights: status=%#x length=%d granted=%#x expected=%#x", status, length, basic.GrantedAccess, rights)
	}
	var information [1024]byte
	status, _, _ = query.Call(uintptr(handle), 2, uintptr(unsafe.Pointer(&information[0])), uintptr(len(information)), uintptr(unsafe.Pointer(&length)))
	if status != 0 || length < uint32(unsafe.Sizeof(uninstallObjectTypeName{})) || length > uint32(len(information)) {
		return errors.New("cannot inspect uninstall guard object type")
	}
	name := (*uninstallObjectTypeName)(unsafe.Pointer(&information[0]))
	start := uintptr(unsafe.Pointer(&information[0]))
	pointer := uintptr(unsafe.Pointer(name.Buffer))
	if name.Buffer == nil || name.Length%2 != 0 || name.Length > 128 || pointer < start || pointer+uintptr(name.Length) > start+uintptr(len(information)) {
		return errors.New("uninstall guard object type name has an invalid native layout")
	}
	kind := windows.UTF16ToString(unsafe.Slice(name.Buffer, int(name.Length)/2))
	runtime.KeepAlive(information)
	if kind != "Event" {
		return errors.New("uninstall guard capability is not a native event")
	}
	return nil
}

// retainUninstallGuardStage pins the only admitted copy layout before signature verification and restricted process creation.
func (installation *Installation) retainUninstallGuardStage(staged *os.File) (files []*os.File, result error) {
	defer func() {
		if result != nil {
			for index := len(files) - 1; index >= 0; index-- {
				result = errors.Join(result, files[index].Close())
			}
			files = nil
		}
	}()
	if _, err := installation.Path("uninstaller"); err != nil {
		return nil, err
	}
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	path := staged.Name()
	if err := admitUninstallGuardStagePath(path, system); err != nil {
		return nil, err
	}
	for _, current := range []string{system, filepath.Join(system, "Temp"), filepath.Dir(path), path} {
		directory := current != path
		file, err := openUninstallGuardObject(current, directory)
		if err != nil {
			return files, err
		}
		files = append(files, file)
		if current == path || current == filepath.Dir(path) {
			descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
			if err != nil {
				return files, err
			}
			if err := admitUninstallGuardStageDescriptor(descriptor); err != nil {
				return files, err
			}
		}
	}
	retained := files[len(files)-1]
	same, err := windowsfile.SameObject(windows.Handle(retained.Fd()), windows.Handle(staged.Fd()))
	if err != nil {
		return files, err
	}
	if !same {
		return files, errors.New("uninstall guard staged object changed before admission")
	}
	if err := installation.VerifyUninstallerCopy(retained); err != nil {
		return files, err
	}
	return files, nil
}

// admitUninstallGuardStagePath rejects caller temp paths and any unreviewed executable or directory spelling.
func admitUninstallGuardStagePath(path, system string) error {
	stage := filepath.Base(filepath.Dir(path))
	suffix, ok := strings.CutPrefix(stage, "HarborUninstall-")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.EqualFold(filepath.Base(path), "HarborUninstall.exe") || !ok || !identityPattern.MatchString(suffix) || !strings.EqualFold(filepath.Dir(filepath.Dir(path)), filepath.Join(system, "Temp")) {
		return errors.New("uninstall guard copy is outside protected Windows staging")
	}
	return nil
}

// openUninstallGuardObject denies write and delete sharing on every retained stage ancestor and its copied agent.
func openUninstallGuardObject(path string, directory bool) (*os.File, error) {
	access := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		access = windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	final, err := windowsfile.FinalPath(handle)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !strings.EqualFold(final, `\\?\`+path) || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && information.NumberOfLinks != 1) {
		return nil, errors.Join(errors.New("uninstall guard stage path, type, link or reparse identity differs"), file.Close())
	}
	return file, nil
}

// admitUninstallGuardStageDescriptor admits only the protected machine grants and inherited high mandatory label used by maintenance staging.
func admitUninstallGuardStageDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if owner == nil || owner.String() != administratorsSID || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("uninstall guard stage owner or ACL protection differs")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 2 {
		return errors.New("uninstall guard stage has unexpected access grants")
	}
	seen := map[string]bool{}
	for index := uint32(0); index < 2; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || ace.Mask != fileAllAccess || (principal != administratorsSID && principal != systemSID) || seen[principal] {
			return errors.New("uninstall guard stage ACL differs from its protected machine policy")
		}
		seen[principal] = true
	}
	sacl, _, err := descriptor.SACL()
	if err != nil {
		return err
	}
	if sacl == nil || sacl.AceCount != 1 {
		return errors.New("uninstall guard stage has no exact high mandatory label")
	}
	var label *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(sacl, 0, &label); err != nil {
		return err
	}
	if label.Header.AceType != 0x11 || label.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || label.Mask != 1 || (*windows.SID)(unsafe.Pointer(&label.SidStart)).String() != "S-1-16-12288" {
		return fmt.Errorf("uninstall guard stage high mandatory label differs: type=%#x flags=%#x mask=%#x sid=%s descriptor=%s", label.Header.AceType, label.Header.AceFlags, label.Mask, (*windows.SID)(unsafe.Pointer(&label.SidStart)).String(), descriptor.String())
	}
	return nil
}
