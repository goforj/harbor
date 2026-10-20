//go:build windows

// Package main provides Harbor's fixed-path Windows maintenance agent.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsfile"
	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"golang.org/x/sys/windows"
)

const (
	maintenanceParentRights = windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION
	maintenanceStageSDDL    = "O:BAG:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)S:P(ML;OICI;NW;;;HI)"
)

var maintenanceStageName = regexp.MustCompile(`^HarborUninstall-[a-f0-9]{32}$`)

// maintenanceOperations separates allowlisted orchestration from native process and filesystem authority.
type maintenanceOperations struct {
	verify    func() error
	register  func(context.Context) error
	remove    func(context.Context) error
	bootstrap func(context.Context) error
	finish    func(context.Context, windows.Handle) error
}

// main accepts only signed-agent lifecycle operations with no caller-selected target paths or accounts.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	operations := maintenanceOperations{verify: verifyMaintenanceSession, register: registerMaintenanceTask, remove: removeMaintenanceTask, bootstrap: bootstrapUninstall, finish: finishUninstall}
	if err := runMaintenance(ctx, os.Args[1:], operations); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor maintenance: %v\n", err)
		os.Exit(1)
	}
}

// runMaintenance validates internal handle syntax before granting any native maintenance operation.
func runMaintenance(ctx context.Context, arguments []string, operations maintenanceOperations) error {
	if len(arguments) == 0 {
		return errors.New("a Windows maintenance operation is required")
	}
	action := arguments[0]
	var parent windows.Handle
	if action == "--finish-uninstall" {
		if len(arguments) != 2 {
			return errors.New("finish-uninstall requires its inherited parent receipt")
		}
		var err error
		parent, err = parseMaintenanceParent(arguments[1])
		if err != nil {
			return err
		}
	} else if len(arguments) != 1 || (action != "--register-task" && action != "--remove-task" && action != "--uninstall") {
		return errors.New("Windows maintenance operation or arguments are not admitted")
	}
	if err := operations.verify(); err != nil {
		return fmt.Errorf("admit maintenance session: %w", err)
	}
	switch action {
	case "--register-task":
		return operations.register(ctx)
	case "--remove-task":
		return operations.remove(ctx)
	case "--uninstall":
		return operations.bootstrap(ctx)
	case "--finish-uninstall":
		return operations.finish(ctx, parent)
	}
	return errors.New("Windows maintenance operation is not admitted")
}

// parseMaintenanceParent accepts one canonical native handle value only for the internal finishing role.
func parseMaintenanceParent(argument string) (windows.Handle, error) {
	text, ok := strings.CutPrefix(argument, "--parent-handle=")
	if !ok {
		return 0, errors.New("missing inherited uninstall parent handle")
	}
	value, err := strconv.ParseUint(text, 10, strconv.IntSize)
	if err != nil || value == 0 || value == uint64(^uintptr(0)) || strconv.FormatUint(value, 10) != text {
		return 0, errors.New("invalid inherited uninstall parent handle")
	}
	return windows.Handle(value), nil
}

// verifyMaintenanceSession refuses over-the-shoulder elevation and unprivileged machine mutation before admission.
func verifyMaintenanceSession() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("Windows maintenance requires elevated same-user approval")
	}
	return windowsinstallation.VerifyInteractiveSession()
}

// admitMaintenance binds the retained machine installation to the actual current account before child launch.
func admitMaintenance() (*windowsinstallation.Installation, error) {
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err == nil && user.User.Sid.String() != installation.Selection().UserSID {
		err = errors.New("maintenance user differs from the selected installation")
	}
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	return installation, nil
}

// registerMaintenanceTask delegates only the fixed signed policy's create-only task operation.
func registerMaintenanceTask(ctx context.Context) (result error) {
	installation, err := admitMaintenance()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	return installation.Task(ctx, "register")
}

// removeMaintenanceTask verifies stopped user authority through a restricted worker before removing an exact owned task.
func removeMaintenanceTask(ctx context.Context) (result error) {
	installation, err := admitMaintenance()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	child, err := installation.Start("daemon-launcher", []string{"--verify-stopped"})
	if err != nil {
		return fmt.Errorf("start stopped-daemon verification: %w", err)
	}
	verification, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	code, waitErr := child.Wait(verification)
	if err := errors.Join(waitErr, child.Close()); err != nil {
		return fmt.Errorf("settle stopped-daemon verification: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("installed daemon is active or unverifiable (exit %d)", code)
	}
	return installation.Task(ctx, "remove")
}

// bootstrapUninstall launches an immutable protected copy and leaves original image unloading to ordinary process exit.
func bootstrapUninstall(ctx context.Context) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	installation, err := admitMaintenance()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	original, err := installation.Path("uninstaller")
	if err != nil {
		return err
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(current, original) {
		return errors.New("uninstall bootstrap must execute the admitted fixed Uninstall.exe")
	}
	source, err := openMaintenanceObject(original, false)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, source.Close()) }()
	if err := installation.VerifyUninstallerCopy(source); err != nil {
		return err
	}
	if err := admitMaintenanceImage(source); err != nil {
		return err
	}
	stage, err := createMaintenanceStage(installation, source)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeMaintenanceFiles(stage)) }()
	path := stage[len(stage)-1].Name()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := launchMaintenanceCopy(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "Harbor uninstall continues in protected staging: %s\n", path)
	return err
}

// createMaintenanceStage creates only one random protected Windows Temp child and its exact admitted agent copy.
func createMaintenanceStage(installation *windowsinstallation.Installation, source *os.File) (files []*os.File, result error) {
	defer func() {
		if result != nil {
			result = errors.Join(result, closeMaintenanceFiles(files))
			files = nil
		}
	}()
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	for _, path := range []string{system, filepath.Join(system, "Temp")} {
		file, err := openMaintenanceObject(path, true)
		if err != nil {
			return files, err
		}
		files = append(files, file)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return files, err
	}
	root := filepath.Join(system, "Temp", "HarborUninstall-"+hex.EncodeToString(random[:]))
	descriptor, err := windows.SecurityDescriptorFromString(maintenanceStageSDDL)
	if err != nil {
		return files, err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	if err := windows.CreateDirectory(windows.StringToUTF16Ptr(root), attributes); err != nil {
		return files, err
	}
	directory, err := openMaintenanceObject(root, true)
	if err != nil {
		return files, err
	}
	files = append(files, directory)
	if err := admitMaintenanceStage(directory); err != nil {
		return files, err
	}
	path := filepath.Join(root, "HarborUninstall.exe")
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, 0, attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return files, err
	}
	destination := os.NewFile(uintptr(handle), path)
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return files, errors.Join(err, destination.Close())
	}
	_, copyErr := io.Copy(destination, source)
	if err := errors.Join(copyErr, destination.Sync(), destination.Close()); err != nil {
		return files, err
	}
	retained, err := openMaintenanceObject(path, false)
	if err != nil {
		return files, err
	}
	files = append(files, retained)
	if err := admitMaintenanceStage(retained); err != nil {
		return files, err
	}
	if err := installation.VerifyUninstallerCopy(retained); err != nil {
		return files, err
	}
	return files, nil
}

// launchMaintenanceCopy inherits only a restricted original-process receipt and never selects a shell or mutable executable.
func launchMaintenanceCopy(path string) (result error) {
	var parent windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &parent, maintenanceParentRights, true, 0); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(parent)) }()
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&parent), unsafe.Sizeof(parent)); err != nil {
		return err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attributes.List()}
	argument := "--parent-handle=" + strconv.FormatUint(uint64(parent), 10)
	command, err := windows.UTF16FromString(windows.ComposeCommandLine([]string{path, "--finish-uninstall", argument}))
	if err != nil {
		return err
	}
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	environment, err := windows.UTF16FromString("SystemRoot=" + system)
	if err != nil {
		return err
	}
	environment = append(environment, 0)
	var process windows.ProcessInformation
	if err := windows.CreateProcess(windows.StringToUTF16Ptr(path), &command[0], nil, nil, true, windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_NO_WINDOW, &environment[0], windows.StringToUTF16Ptr(filepath.Dir(path)), &startup.StartupInfo, &process); err != nil {
		return fmt.Errorf("launch protected uninstall continuation: %w", err)
	}
	runtime.KeepAlive(command)
	runtime.KeepAlive(environment)
	return errors.Join(windows.CloseHandle(process.Thread), windows.CloseHandle(process.Process))
}

// finishUninstall authenticates both its protected copied image and the original inherited process before fixed product removal.
func finishUninstall(ctx context.Context, parent windows.Handle) (result error) {
	defer func() { result = errors.Join(result, windows.CloseHandle(parent)) }()
	installation, err := admitMaintenance()
	if err != nil {
		return err
	}
	files, err := retainMaintenanceStage(installation)
	if err != nil {
		return errors.Join(err, installation.Close())
	}
	defer func() { result = errors.Join(result, closeMaintenanceFiles(files)) }()
	selection := installation.Selection()
	var originalIdentity, continuationIdentity *maintenanceProcessIdentity
	defer func() {
		result = errors.Join(result, recordMaintenanceResult(filepath.Dir(files[len(files)-1].Name()), selection, originalIdentity, continuationIdentity, result))
	}()
	continuationIdentity, err = inspectMaintenanceProcess(windows.CurrentProcess())
	if err != nil {
		return errors.Join(err, installation.Close())
	}
	original, err := installation.Path("uninstaller")
	if err == nil {
		err = admitMaintenanceParent(parent, original, installation.Selection().UserSID)
	}
	if err == nil {
		originalIdentity, err = inspectMaintenanceProcess(parent)
	}
	if err == nil {
		err = waitMaintenanceParent(ctx, parent)
	}
	if err = errors.Join(err, installation.Close()); err != nil {
		return err
	}
	return windowsinstallation.Uninstall(ctx)
}

// retainMaintenanceStage admits only this exact fixed copy name beneath a canonical protected Windows Temp staging directory.
func retainMaintenanceStage(installation *windowsinstallation.Installation) (files []*os.File, result error) {
	defer func() {
		if result != nil {
			result = errors.Join(result, closeMaintenanceFiles(files))
			files = nil
		}
	}()
	current, err := os.Executable()
	if err != nil {
		return nil, err
	}
	system, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	if err := admitMaintenanceStagePath(current, system); err != nil {
		return nil, err
	}
	for _, path := range []string{system, filepath.Join(system, "Temp"), filepath.Dir(current), current} {
		directory := path != current
		file, err := openMaintenanceObject(path, directory)
		if err != nil {
			return files, err
		}
		files = append(files, file)
		if path == current || path == filepath.Dir(current) {
			if err := admitMaintenanceStage(file); err != nil {
				return files, err
			}
		}
	}
	if err := installation.VerifyUninstallerCopy(files[len(files)-1]); err != nil {
		return files, err
	}
	if err := admitMaintenanceImage(files[len(files)-1]); err != nil {
		return files, err
	}
	return files, nil
}

// admitMaintenanceStagePath rejects caller Temp overrides, traversal, alternate file names, and copied agents outside protected staging.
func admitMaintenanceStagePath(current, system string) error {
	if !filepath.IsAbs(current) || filepath.Clean(current) != current || !strings.EqualFold(filepath.Base(current), "HarborUninstall.exe") || !maintenanceStageName.MatchString(filepath.Base(filepath.Dir(current))) || !strings.EqualFold(filepath.Dir(filepath.Dir(current)), filepath.Join(system, "Temp")) {
		return errors.New("uninstall continuation is outside canonical protected Windows staging")
	}
	return nil
}

// openMaintenanceObject retains an exact non-reparse native object with write and delete sharing denied.
func openMaintenanceObject(path string, directory bool) (*os.File, error) {
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
		return nil, errors.Join(errors.New("uninstall staging object has a canonical path, type, link or reparse conflict"), file.Close())
	}
	return file, nil
}

// admitMaintenanceStage refuses ordinary-user mutation or a missing high-integrity write barrier on a staged object.
func admitMaintenanceStage(file *os.File) error {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return admitMaintenanceDescriptor(descriptor)
}

// admitMaintenanceDescriptor accepts only the explicit protected machine ACL and high mandatory label used for this staging protocol.
func admitMaintenanceDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if owner == nil || owner.String() != "S-1-5-32-544" || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("uninstall staging owner or ACL protection differs")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 2 {
		return errors.New("uninstall staging ACL has unexpected grants")
	}
	seen := map[string]bool{}
	for index := uint32(0); index < 2; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || ace.Mask != 0x1f01ff || (sid != "S-1-5-32-544" && sid != "S-1-5-18") || seen[sid] {
			return errors.New("uninstall staging ACL differs from the protected machine policy")
		}
		seen[sid] = true
	}
	sacl, _, err := descriptor.SACL()
	if err != nil {
		return err
	}
	if sacl == nil || sacl.AceCount != 1 {
		return errors.New("uninstall staging has no exact high mandatory label")
	}
	var label *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(sacl, 0, &label); err != nil {
		return err
	}
	if label.Header.AceType != 0x11 || label.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || label.Mask != 1 || (*windows.SID)(unsafe.Pointer(&label.SidStart)).String() != "S-1-16-12288" {
		return errors.New("uninstall staging high mandatory label differs")
	}
	return nil
}

// closeMaintenanceFiles releases exact retained objects in reverse order without deleting ambiguous staging remnants.
func closeMaintenanceFiles(files []*os.File) (result error) {
	for index := len(files) - 1; index >= 0; index-- {
		result = errors.Join(result, files[index].Close())
	}
	return result
}

// admitMaintenanceParent requires the inherited restricted process receipt to name this child's actual original uninstaller parent.
func admitMaintenanceParent(parent windows.Handle, original, userSID string) error {
	var flags uint32
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(parent), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 {
		return err
	}
	if flags&windows.HANDLE_FLAG_INHERIT == 0 {
		return errors.New("uninstall parent receipt was not inherited")
	}
	var object [128]byte
	var length uint32
	status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject").Call(uintptr(parent), 0, uintptr(unsafe.Pointer(&object[0])), uintptr(len(object)), uintptr(unsafe.Pointer(&length)))
	if status != 0 || length < 8 || binary.LittleEndian.Uint32(object[4:8]) != maintenanceParentRights {
		return errors.New("uninstall parent receipt has unexpected native access rights")
	}
	var basic [6]uintptr
	status, _, _ = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationProcess").Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&basic[0])), unsafe.Sizeof(basic), uintptr(unsafe.Pointer(&length)))
	if status != 0 {
		return errors.New("cannot inspect uninstall continuation parent identity")
	}
	pid, err := windows.GetProcessId(parent)
	if err != nil {
		return err
	}
	if uintptr(pid) != basic[5] {
		return errors.New("inherited uninstall receipt does not identify the actual parent")
	}
	image := make([]uint16, 32768)
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(parent, 0, &image[0], &size); err != nil {
		return err
	}
	if !strings.EqualFold(windows.UTF16ToString(image[:size]), original) {
		return errors.New("uninstall parent is not the fixed admitted Uninstall.exe")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(parent, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	parentUser, userErr := token.GetTokenUser()
	var parentSession, currentSession uint32
	sessionErr := maintenanceTokenSession(token, &parentSession)
	closeErr := token.Close()
	if err := errors.Join(userErr, sessionErr, closeErr, maintenanceTokenSession(windows.GetCurrentProcessToken(), &currentSession)); err != nil {
		return err
	}
	if parentUser.User.Sid.String() != userSID || parentSession == 0 || parentSession != currentSession {
		return errors.New("uninstall parent user or interactive session differs")
	}
	var parentBirth, currentBirth, exit, kernel, used windows.Filetime
	if err := windows.GetProcessTimes(parent, &parentBirth, &exit, &kernel, &used); err != nil {
		return err
	}
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &currentBirth, &exit, &kernel, &used); err != nil {
		return err
	}
	if parentBirth.Nanoseconds() >= currentBirth.Nanoseconds() {
		return errors.New("uninstall parent receipt has no earlier native birth")
	}
	return windows.SetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0)
}

// maintenanceTokenSession obtains a checked native scalar without inheriting caller-supplied session identifiers.
func maintenanceTokenSession(token windows.Token, session *uint32) error {
	var length uint32
	if err := windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(session)), 4, &length); err != nil {
		return err
	}
	if length != 4 {
		return errors.New("uninstall token session has an unexpected native size")
	}
	return nil
}

// waitMaintenanceParent waits only the retained original process and never signals a PID or arbitrary process.
func waitMaintenanceParent(ctx context.Context, parent windows.Handle) error {
	deadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		if err := deadline.Err(); err != nil {
			return fmt.Errorf("wait for original uninstall image to unload: %w", err)
		}
		state, err := windows.WaitForSingleObject(parent, 50)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return errors.New("uninstall parent returned an unexpected native wait state")
		}
	}
}

// admitMaintenanceImage compares the native mapped image object with the retained signed file instead of trusting executable path text.
func admitMaintenanceImage(expected *os.File) (result error) {
	var module windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &module); err != nil {
		return err
	}
	name := make([]uint16, 32768)
	length, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetMappedFileNameW").Call(uintptr(windows.CurrentProcess()), uintptr(module), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if length == 0 {
		return fmt.Errorf("observe native maintenance image mapping: %w", err)
	}
	if length >= uintptr(len(name)) {
		return errors.New("native maintenance image path exceeds bounds")
	}
	mapped := windows.UTF16ToString(name[:length])
	if !strings.HasPrefix(mapped, `\Device\`) {
		return errors.New("native maintenance image is outside a fixed Windows device")
	}
	path := `\\?\GLOBALROOT` + mapped
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(handle)) }()
	same, err := windowsfile.SameObject(handle, windows.Handle(expected.Fd()))
	if err != nil {
		return err
	}
	if !same {
		return errors.New("native executing maintenance image differs from the retained admitted file")
	}
	return nil
}

// maintenanceResult retains the outcome only in the protected disposable staging directory, never in Harbor user state.
type maintenanceResult struct {
	SchemaVersion  int                         `json:"schema_version"`
	ProductID      string                      `json:"product_id"`
	InstallationID string                      `json:"installation_id"`
	BundleDigest   string                      `json:"bundle_digest"`
	Status         string                      `json:"status"`
	OriginalParent *maintenanceProcessIdentity `json:"original_parent,omitempty"`
	Continuation   *maintenanceProcessIdentity `json:"continuation,omitempty"`
	Error          string                      `json:"error,omitempty"`
}

// recordMaintenanceResult creates an immutable one-shot outcome so detached continuation failure remains observable.
func recordMaintenanceResult(root string, selection windowsinstallation.Selection, original, continuation *maintenanceProcessIdentity, failure error) (result error) {
	receipt := maintenanceReceipt(selection, original, continuation, failure)
	descriptor, err := windows.SecurityDescriptorFromString(maintenanceStageSDDL)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	path := filepath.Join(root, "result.json")
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_WRITE, windows.FILE_SHARE_READ, attributes, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return fmt.Errorf("create protected uninstall result: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer func() { result = errors.Join(result, file.Close()) }()
	if err := json.NewEncoder(file).Encode(receipt); err != nil {
		return err
	}
	return file.Sync()
}

// maintenanceProcessIdentity is observational evidence only and never authorizes later process signaling.
type maintenanceProcessIdentity struct {
	PID         uint32 `json:"pid"`
	NativeBirth string `json:"native_birth"`
	UserSID     string `json:"user_sid"`
	Session     uint32 `json:"session"`
}

// inspectMaintenanceProcess records native identity through an already retained process handle.
func inspectMaintenanceProcess(process windows.Handle) (_ *maintenanceProcessIdentity, result error) {
	pid, err := windows.GetProcessId(process)
	if err != nil {
		return nil, err
	}
	var birth, exit, kernel, used windows.Filetime
	if err := windows.GetProcessTimes(process, &birth, &exit, &kernel, &used); err != nil {
		return nil, err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, token.Close()) }()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	var session uint32
	if err := maintenanceTokenSession(token, &session); err != nil {
		return nil, err
	}
	ticks := uint64(birth.HighDateTime)<<32 | uint64(birth.LowDateTime)
	return &maintenanceProcessIdentity{PID: pid, NativeBirth: strconv.FormatUint(ticks, 10), UserSID: user.User.Sid.String(), Session: session}, nil
}

// maintenanceReceipt reports completion only after the fixed uninstall core and all earlier admission steps succeeded.
func maintenanceReceipt(selection windowsinstallation.Selection, original, continuation *maintenanceProcessIdentity, failure error) maintenanceResult {
	receipt := maintenanceResult{SchemaVersion: 1, ProductID: windowsinstallation.ProductID, InstallationID: selection.InstallationID, BundleDigest: selection.BundleDigest, Status: "complete", OriginalParent: original, Continuation: continuation}
	if failure != nil {
		receipt.Status = "failed"
		receipt.Error = failure.Error()
		if len(receipt.Error) > 4096 {
			receipt.Error = receipt.Error[:4096]
		}
	}
	return receipt
}
