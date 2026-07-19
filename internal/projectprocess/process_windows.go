//go:build windows

package projectprocess

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformProcess owns the Windows Job Object and query handle for one launched command tree.
type platformProcess struct {
	mu      sync.Mutex
	job     windows.Handle
	process windows.Handle
}

// jobObjectBasicAccountingInformation matches the stable Win32 layout needed to observe active descendants.
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// preparePlatformProcess creates a kill-on-close Job Object before starting the independent process group.
func preparePlatformProcess(command *exec.Cmd) (*platformProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
	return &platformProcess{job: job}, nil
}

// attach assigns the process tree to Harbor's Job Object and captures its creation time.
func (process *platformProcess) attach(child *os.Process) (string, error) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(child.Pid),
	)
	if err != nil {
		return "", err
	}
	if err := windows.AssignProcessToJobObject(process.job, handle); err != nil {
		windows.CloseHandle(handle)
		return "", err
	}
	var creation windows.Filetime
	var exit windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		windows.CloseHandle(handle)
		return "", err
	}
	process.mu.Lock()
	process.process = handle
	process.mu.Unlock()
	return fmt.Sprintf("windows:%016x", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime)), nil
}

// resume releases Harbor's launch suspension only after the process is owned by the kill-on-close Job Object.
func (process *platformProcess) resume(child *os.Process) error {
	thread, err := primaryThreadHandle(uint32(child.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)

	previousCount, err := windows.ResumeThread(thread)
	if err != nil {
		return err
	}
	if previousCount != 1 {
		return fmt.Errorf("resume forj primary thread: unexpected suspension count %d", previousCount)
	}
	return nil
}

// primaryThreadHandle finds the only thread a newly created suspended process can own before Harbor resumes it.
func primaryThreadHandle(pid uint32) (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return 0, err
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return 0, err
			}
			return thread, nil
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return 0, fmt.Errorf("find suspended forj primary thread for process %d", pid)
}

// graceful sends CTRL_BREAK to the independent process group when the current session owns a usable console.
func (process *platformProcess) graceful(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}

// force terminates the Job Object so descendants cannot survive a parent-only kill.
func (process *platformProcess) force(pid int) error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(process.job, 1)
}

// treeAlive reports whether Harbor still owns an open Job Object for the process tree.
func (process *platformProcess) treeAlive(pid int) (bool, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.job == 0 {
		return false, nil
	}
	information := jobObjectBasicAccountingInformation{}
	err := windows.QueryInformationJobObject(
		process.job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
		nil,
	)
	if err != nil {
		return false, err
	}
	return information.ActiveProcesses != 0, nil
}

// close releases process and Job Object handles only after the command has been reaped.
func (process *platformProcess) close() {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.process != 0 {
		windows.CloseHandle(process.process)
		process.process = 0
	}
	if process.job != 0 {
		windows.CloseHandle(process.job)
		process.job = 0
	}
}
