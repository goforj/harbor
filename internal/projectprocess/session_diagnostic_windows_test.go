//go:build windows

package projectprocess

import (
	"fmt"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"golang.org/x/sys/windows"
)

// sessionTestProcessDiagnostics retains query-only process objects without retaining or modifying any job lifetime.
func sessionTestProcessDiagnostics(session domain.ProjectSession) (func() string, func()) {
	type observedProcess struct {
		name   string
		handle windows.Handle
		err    error
	}
	processes := []observedProcess{}
	for _, item := range []struct {
		name     string
		evidence domain.ProcessEvidence
	}{{"root", *session.Process}, {"broker", session.OutputBroker.Process}} {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(item.evidence.PID))
		processes = append(processes, observedProcess{name: item.name, handle: handle, err: err})
	}
	describe := func() string {
		facts := []string{}
		for _, process := range processes {
			if process.err != nil {
				facts = append(facts, fmt.Sprintf("%s open=%v", process.name, process.err))
				continue
			}
			state, waitErr := windows.WaitForSingleObject(process.handle, 0)
			var code uint32
			codeErr := windows.GetExitCodeProcess(process.handle, &code)
			facts = append(facts, fmt.Sprintf("%s wait=%d waitErr=%v exit=%#x exitErr=%v", process.name, state, waitErr, code, codeErr))
		}
		return strings.Join(facts, "; ")
	}
	release := func() {
		for _, process := range processes {
			if process.err == nil {
				_ = windows.CloseHandle(process.handle)
			}
		}
	}
	return describe, release
}
