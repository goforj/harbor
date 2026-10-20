//go:build windows

package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/windowsjob"
	"golang.org/x/sys/windows"
)

// rootAlive observes the original retained process object rather than a reusable numeric PID.
func (process *platformProcess) rootAlive(int) (bool, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.process == 0 {
		return false, nil
	}
	state, err := windows.WaitForSingleObject(process.process, 0)
	if err != nil {
		return false, err
	}
	if state != windows.WAIT_OBJECT_0 && state != uint32(windows.WAIT_TIMEOUT) {
		return false, errors.New("retained session process returned an unexpected wait state")
	}
	return state == uint32(windows.WAIT_TIMEOUT), nil
}

// settleRetainedSessionScope retires a surviving job only after its exact root birth and membership are proved.
func settleRetainedSessionScope(ctx context.Context, session domain.ProjectSession, directory string) (PriorProcessSettlement, bool, error) {
	if session.OutputBroker == nil {
		return PriorProcessSettlement{}, false, nil
	}
	platform, child, err := adoptPlatformProcess(ctx, session, directory)
	if errors.Is(err, ErrSessionAdoptionUnsupported) {
		return PriorProcessSettlement{}, false, nil
	}
	if err != nil {
		// Absence of the exact job proves that its kill-on-last-close lifetime has already ended.
		job, jobErr := openOwnedSessionJob(session.ProjectID, session.ID, directory, sessionJobQuery)
		if jobErr == nil {
			windows.CloseHandle(job)
		} else if errors.Is(jobErr, windows.ERROR_FILE_NOT_FOUND) {
			return PriorProcessSettlement{}, false, nil
		}
		return PriorProcessSettlement{}, true, err
	}
	defer platform.close()
	defer child.Release()
	if err := platform.force(child.Pid); err != nil {
		return PriorProcessSettlement{}, true, err
	}
	deadline := time.NewTimer(forceSettlementPeriod)
	defer deadline.Stop()
	ticker := time.NewTicker(forceSettlementPoll)
	defer ticker.Stop()
	for {
		alive, err := platform.treeAlive(child.Pid)
		if err != nil {
			return PriorProcessSettlement{}, true, err
		}
		if !alive {
			return PriorProcessSettlement{Outcome: PriorProcessSettlementTerminated}, true, nil
		}
		select {
		case <-ctx.Done():
			return PriorProcessSettlement{}, true, ctx.Err()
		case <-deadline.C:
			return PriorProcessSettlement{}, true, errors.New("retained session job did not settle")
		case <-ticker.C:
		}
	}
}

const (
	sessionJobQuery     = 0x0004
	sessionJobTerminate = 0x0008
)

var (
	createSessionJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateJobObjectW")
	openSessionJob   = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")
	processInJob     = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
)

// prepareSessionPlatformProcess establishes a separate kill-on-last-close job whose lifetime can be retained by the broker.
func prepareSessionPlatformProcess(command *exec.Cmd, projectID domain.ProjectID, sessionID domain.SessionID, directory string, broker bool) (*platformProcess, error) {
	if !broker {
		return preparePlatformProcess(command)
	}
	name, err := windows.UTF16PtrFromString(sessionScopeName(projectID, sessionID, directory))
	if err != nil {
		return nil, err
	}
	handle, _, nativeErr := createSessionJob.Call(0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("create session job: %w", nativeErr)
	}
	job := windows.Handle(handle)
	if errors.Is(nativeErr, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(job)
		return nil, errors.New("session job already exists; refusing to mutate an existing scope")
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if err := windowsjob.Set(job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	flags, err := sessionProcessCreationFlags()
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED | flags}
	return &platformProcess{job: job, retainedScope: true}, nil
}

// sessionProcessCreationFlags leaves unrelated outer jobs intact and explicitly leaves Harbor's permissive lifetime wrappers.
func sessionProcessCreationFlags() (uint32, error) {
	var member int32
	ok, _, nativeErr := processInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&member)))
	if ok == 0 {
		return 0, fmt.Errorf("observe current process job: %w", nativeErr)
	}
	if member == 0 {
		return 0, nil
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windowsjob.Query(0, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		return 0, fmt.Errorf("observe parent job lifetime policy: %w", err)
	}
	return sessionBreakawayFlags(limits.BasicLimitInformation.LimitFlags), nil
}

// sessionBreakawayFlags requests explicit breakaway only where the enclosing job actually permits it.
func sessionBreakawayFlags(limits uint32) uint32 {
	if limits&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		return windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return 0
}

// retainsProcessScope requests a broker-held query handle only for independently created session jobs.
func (process *platformProcess) retainsProcessScope() bool { return process.retainedScope }

// openOwnedSessionJob reopens a lifecycle-derived name without accepting arbitrary caller-supplied objects.
func openOwnedSessionJob(projectID domain.ProjectID, sessionID domain.SessionID, directory string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(sessionScopeName(projectID, sessionID, directory))
	if err != nil {
		return 0, err
	}
	handle, _, nativeErr := openSessionJob.Call(uintptr(access), 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return 0, fmt.Errorf("open retained session job: %w", nativeErr)
	}
	job := windows.Handle(handle)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windowsjob.Query(job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	flags := limits.BasicLimitInformation.LimitFlags
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 || flags&(windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK|windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK) != 0 {
		windows.CloseHandle(job)
		return 0, errors.New("retained session job has unexpected lifetime or descendant policy")
	}
	return job, nil
}

// retainOutputBrokerProcessScope keeps a query-only handle open while the exact project's inherited output pipes are live.
func retainOutputBrokerProcessScope(config OutputBrokerRuntimeConfig) (func(), error) {
	if !config.RetainProcessScope {
		return func() {}, nil
	}
	job, err := openOwnedSessionJob(config.ProjectID, config.SessionID, config.OutputDirectory, sessionJobQuery)
	if err != nil {
		return nil, err
	}
	return func() { windows.CloseHandle(job) }, nil
}

// adoptPlatformProcess restores a native handle only after exact birth and membership in the deterministic retained job agree.
func adoptPlatformProcess(ctx context.Context, session domain.ProjectSession, directory string) (*platformProcess, *os.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	config, err := ReadOutputBrokerLaunchConfig(session.OutputBroker.ManifestPath)
	if err != nil {
		return nil, nil, err
	}
	if !config.RetainProcessScope {
		job, jobErr := openOwnedSessionJob(session.ProjectID, session.ID, directory, sessionJobQuery)
		if jobErr == nil {
			windows.CloseHandle(job)
			return nil, nil, errors.New("retained native session job is missing its lifetime metadata")
		}
		if !errors.Is(jobErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil, jobErr
		}
		return nil, nil, ErrSessionAdoptionUnsupported
	}
	if config.ProjectID != session.ProjectID || config.SessionID != session.ID || config.OutputDirectory != directory ||
		DigestOutputBrokerTicket(config.AttachmentTicket) != session.OutputBroker.CredentialDigest {
		return nil, nil, errors.New("retained job manifest differs from the durable session")
	}
	if session.Process.PID > int64(^uint32(0)) {
		return nil, nil, errors.New("retained session PID exceeds the Windows range")
	}
	job, err := openOwnedSessionJob(session.ProjectID, session.ID, directory, sessionJobQuery|sessionJobTerminate)
	if err != nil {
		return nil, nil, err
	}
	platform := &platformProcess{job: job, retainedScope: true}
	platform.process, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(session.Process.PID))
	if err == nil {
		var birth string
		birth, err = processBirthTokenFromHandle(platform.process, windows.GetProcessTimes)
		if err == nil && birth != session.Process.BirthToken {
			err = errors.New("retained session process birth changed")
		}
	}
	if err == nil {
		var member int32
		ok, _, nativeErr := processInJob.Call(uintptr(platform.process), uintptr(job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 {
			err = fmt.Errorf("observe retained session job membership: %w", nativeErr)
		} else if member == 0 {
			err = errors.New("retained session process is not a member of its job")
		}
	}
	if err != nil {
		platform.close()
		return nil, nil, err
	}
	child, err := os.FindProcess(int(session.Process.PID))
	if err != nil {
		platform.close()
		return nil, nil, err
	}
	return platform, child, nil
}

// platformProcessCanDetach requires both the broker's native identity and a job that outlives the daemon's local handle.
func platformProcessCanDetach(platform *platformProcess, info Info) error {
	if !platform.retainedScope || info.OutputBroker == nil {
		return ErrSessionAdoptionUnsupported
	}
	config, err := ReadOutputBrokerLaunchConfig(info.OutputBroker.ManifestPath)
	if err != nil {
		return err
	}
	if !config.RetainProcessScope || config.ProjectID != info.ProjectID || config.SessionID != info.SessionID ||
		DigestOutputBrokerTicket(config.AttachmentTicket) != info.OutputBroker.TicketDigest {
		return errors.New("session broker did not retain the expected native job")
	}
	_, err = ObservePersistedOutputBrokerProcessEvidence(info.OutputBroker.Process)
	return err
}
