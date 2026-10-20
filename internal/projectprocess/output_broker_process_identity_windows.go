//go:build windows

package projectprocess

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/goforj/harbor/internal/domain"
	"golang.org/x/sys/windows"
)

// observePersistedOutputBrokerProcessEvidence binds image and arguments to a retained native process handle.
func observePersistedOutputBrokerProcessEvidence(pid int64) (domain.ProcessEvidence, error) {
	if pid <= 0 || pid > int64(^uint32(0)) {
		return domain.ProcessEvidence{}, errors.New("output broker PID is outside the Windows range")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	defer windows.CloseHandle(handle)
	birth, err := processBirthTokenFromHandle(handle, windows.GetProcessTimes)
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	image := make([]uint16, 32768)
	length := uint32(len(image))
	if err := windows.QueryFullProcessImageName(handle, 0, &image[0], &length); err != nil {
		return domain.ProcessEvidence{}, err
	}
	executable, err := canonicalExecutable(windows.UTF16ToString(image[:length]))
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	arguments, err := readWindowsProcessArguments(handle)
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return domain.ProcessEvidence{}, errors.Join(errors.New("output broker ended during identity observation"), err)
	}
	evidence := domain.ProcessEvidence{PID: pid, BirthToken: birth, ExecutableIdentity: executable, ArgumentDigest: digestArguments(arguments)}
	return evidence, evidence.Validate()
}

// readWindowsProcessArguments bounds the kernel-returned UNICODE_STRING before dereferencing its buffer.
func readWindowsProcessArguments(handle windows.Handle) ([]string, error) {
	buffer := make([]byte, 65536+unsafe.Sizeof(windows.NTUnicodeString{}))
	var returned uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned); err != nil {
		return nil, fmt.Errorf("read output broker command line: %w", err)
	}
	if returned < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) || returned > uint32(len(buffer)) {
		return nil, errors.New("output broker command line has an invalid native record length")
	}
	record := (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0]))
	base, start := uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(record.Buffer))
	if record.Length == 0 || record.Length%2 != 0 || record.Length > record.MaximumLength || start < base+unsafe.Sizeof(*record) ||
		start > base+uintptr(returned) || uintptr(record.Length) > base+uintptr(returned)-start {
		return nil, errors.New("output broker command line has an invalid native string range")
	}
	commandLine := record.String()
	if strings.ContainsRune(commandLine, 0) {
		return nil, errors.New("output broker command line contains an embedded NUL")
	}
	arguments, err := windows.DecomposeCommandLine(commandLine)
	if err != nil || len(arguments) == 0 || len(arguments) > 32 {
		return nil, errors.Join(errors.New("output broker argument vector is unreadable or too large"), err)
	}
	return arguments, nil
}
