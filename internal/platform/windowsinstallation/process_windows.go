//go:build windows

package windowsinstallation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsjob"
	"golang.org/x/sys/windows"
)

// Child retains original native handles for exactly one admitted product process and its private job.
type Child struct {
	// PID is observational only; lifecycle operations use the retained original process handle.
	PID uint32
	// Facts records the actual suspended child's admitted effective authority.
	Facts   TokenFacts
	process windows.Handle
	job     windows.Handle
}

// Start launches only a compiled admitted role and checks its actual token before its first instruction.
func (installation *Installation) Start(role string, arguments []string) (_ *Child, result error) {
	if err := admitRoleArguments(role, arguments); err != nil {
		return nil, err
	}
	path, err := installation.Path(role)
	if err != nil {
		return nil, err
	}
	return installation.startAdmitted(path, role, arguments, nil)
}

// startAdmitted shares actual-child token and lifetime admission with the fixed signed maintenance guard capability.
func (installation *Installation) startAdmitted(path, role string, arguments []string, inherited []windows.Handle) (_ *Child, result error) {
	var child *Child
	defer func() {
		if result != nil && child != nil {
			result = errors.Join(result, child.abort(), child.Close())
		}
	}()
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		return nil, fmt.Errorf("open installed source token: %w", err)
	}
	defer func() { result = errors.Join(result, source.Close()) }()
	user, err := source.GetTokenUser()
	if err != nil {
		return nil, err
	}
	var session uint32
	if err := tokenInteger(source, windows.TokenSessionId, &session); err != nil {
		return nil, err
	}
	if session == 0 || user.User.Sid.String() == systemSID || user.User.Sid.String() != installation.selection.UserSID {
		return nil, errors.New("installed product launch requires its interactive user session")
	}
	token, err := restrictedRuntimeToken(source)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, token.Close()) }()
	environment, err := runtimeEnvironment(token)
	if err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Desktop: windows.StringToUTF16Ptr(`winsta0\default`)}}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	startup.ProcThreadAttributeList = attributes.List()
	var streams []windows.Handle
	defer func() {
		for _, handle := range streams {
			result = errors.Join(result, windows.CloseHandle(handle))
		}
	}()
	if role == "cli" {
		for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
			var handle windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &handle, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
				return nil, err
			}
			streams = append(streams, handle)
		}
	} else if role == "uninstall-guard" {
		// Windows console initialization must not inherit unspecified elevated standard-handle values alongside the event capabilities.
		security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
		for _, access := range []uint32{windows.GENERIC_READ, windows.GENERIC_WRITE, windows.GENERIC_WRITE} {
			handle, err := windows.CreateFile(windows.StringToUTF16Ptr(`\\?\GLOBALROOT\Device\Null`), access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				return nil, fmt.Errorf("open fixed null standard stream: %w", err)
			}
			streams = append(streams, handle)
		}
	}
	if len(streams) != 0 {
		startup.Flags = windows.STARTF_USESTDHANDLES
		startup.StdInput, startup.StdOutput, startup.StdErr = streams[0], streams[1], streams[2]
	}
	inheritedHandles := append(append([]windows.Handle(nil), streams...), inherited...)
	inherit := len(inheritedHandles) != 0
	if len(inheritedHandles) != 0 {
		if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inheritedHandles[0]), uintptr(len(inheritedHandles))*unsafe.Sizeof(inheritedHandles[0])); err != nil {
			return nil, err
		}
	}
	command, err := windows.UTF16FromString(windows.ComposeCommandLine(append([]string{path}, arguments...)))
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if role == "daemon" || role == "daemon-launcher" || role == "uninstall-guard" {
		flags |= windows.CREATE_NO_WINDOW
	}
	var created windows.ProcessInformation
	if err := windows.CreateProcessAsUser(token, windows.StringToUTF16Ptr(path), &command[0], nil, nil, inherit, flags, &environment[0], windows.StringToUTF16Ptr(filepath.Dir(path)), &startup.StartupInfo, &created); err != nil {
		return nil, fmt.Errorf("create suspended installed %s: %w", role, err)
	}
	child = &Child{PID: created.ProcessId, process: created.Process}
	defer func() {
		result = errors.Join(result, windows.CloseHandle(created.Thread))
		runtime.KeepAlive(environment)
		runtime.KeepAlive(command)
		runtime.KeepAlive(streams)
		runtime.KeepAlive(inheritedHandles)
	}()
	var actual windows.Token
	if err := windows.OpenProcessToken(created.Process, windows.TOKEN_QUERY, &actual); err != nil {
		return nil, err
	}
	facts, inspectErr := inspectRuntimeToken(actual)
	if err := errors.Join(inspectErr, actual.Close()); err != nil {
		return nil, err
	}
	if facts.UserSID != user.User.Sid.String() || facts.Session != session {
		return nil, errors.New("installed child changed the intended user SID or session")
	}
	child.Facts = facts
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	child.job = job
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = installedRoleJobLimits(role)
	if err := windowsjob.Set(job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(job, created.Process); err != nil {
		return nil, err
	}
	if _, err := windows.ResumeThread(created.Thread); err != nil {
		return nil, err
	}
	return child, nil
}

// Wait observes only the retained child and retires that exact launch if its caller cancels.
func (child *Child) Wait(ctx context.Context) (uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if child.process == 0 {
		return 0, errors.New("installed child receipt is closed")
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, errors.Join(err, child.abort())
		}
		state, err := windows.WaitForSingleObject(child.process, 50)
		if err != nil {
			return 0, err
		}
		if state == windows.WAIT_OBJECT_0 {
			var code uint32
			if err := windows.GetExitCodeProcess(child.process, &code); err != nil {
				return 0, err
			}
			return code, nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return 0, errors.New("installed child returned an unexpected wait state")
		}
	}
}

// Close refuses to discard ownership of a still-live process instead of silently detaching its job.
func (child *Child) Close() error {
	if child.process == 0 {
		return nil
	}
	state, err := windows.WaitForSingleObject(child.process, 0)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return errors.New("installed child must settle before its receipt closes")
	}
	result := windows.CloseHandle(child.process)
	child.process = 0
	if child.job != 0 {
		result = errors.Join(result, windows.CloseHandle(child.job))
		child.job = 0
	}
	return result
}

// abort retires only an original process created by this launcher, including failure before its first instruction.
func (child *Child) abort() error {
	state, err := windows.WaitForSingleObject(child.process, 0)
	if err != nil {
		return err
	}
	if state == windows.WAIT_OBJECT_0 {
		return nil
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return errors.New("installed child returned an unexpected retirement state")
	}
	if err := windows.TerminateProcess(child.process, 1); err != nil {
		return err
	}
	state, err = windows.WaitForSingleObject(child.process, 10000)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return errors.New("installed child did not settle within the retirement bound")
	}
	return nil
}

// admitRoleArguments prevents a machine task or stable launcher from selecting arbitrary executables or worker modes.
func admitRoleArguments(role string, arguments []string) error {
	switch role {
	case "desktop":
		if len(arguments) == 0 {
			return nil
		}
	case "daemon-launcher":
		if len(arguments) == 1 && (arguments[0] == "--installed-worker" || arguments[0] == "--verify-stopped") {
			return nil
		}
	case "daemon":
		if len(arguments) == 1 && (arguments[0] == "migrate" || arguments[0] == "--foreground") {
			return nil
		}
	case "cli":
		return nil
	}
	return errors.New("installed product role or arguments are not admitted")
}

// runtimeEnvironment obtains the user's registered environment instead of inheriting elevated task-service overrides.
func runtimeEnvironment(token windows.Token) ([]uint16, error) {
	environment, err := token.Environ(false)
	if err != nil {
		return nil, fmt.Errorf("build selected user runtime environment: %w", err)
	}
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, fmt.Errorf("resolve selected user LocalAppData: %w", err)
	}
	filtered := environment[:0]
	for _, entry := range environment {
		if strings.EqualFold(strings.SplitN(entry, "=", 2)[0], "LOCALAPPDATA") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, "LOCALAPPDATA="+local)
	sort.Slice(filtered, func(i, j int) bool { return strings.ToUpper(filtered[i]) < strings.ToUpper(filtered[j]) })
	return utf16.Encode([]rune(strings.Join(filtered, "\x00") + "\x00\x00")), nil
}

// installedRoleJobLimits permits explicit project breakaway only from the two daemon lifetime wrappers.
func installedRoleJobLimits(role string) uint32 {
	flags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	if role == "daemon" || role == "daemon-launcher" {
		flags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	}
	return flags
}
