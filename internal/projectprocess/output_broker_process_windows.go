//go:build windows

package projectprocess

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// prepareOutputBrokerProcess passes inheritable Windows handles explicitly because ExtraFiles is Unix-only.
func prepareOutputBrokerProcess(executable, configPath string, stdout, stderr *os.File) (*outputBrokerProcess, []string, error) {
	for name, file := range map[string]*os.File{"stdout": stdout, "stderr": stderr} {
		if file == nil {
			return nil, nil, fmt.Errorf("output broker %s pipe is required", name)
		}
		if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return nil, nil, fmt.Errorf("mark output broker %s handle inheritable: %w", name, err)
		}
	}
	stdoutHandle := strconv.FormatUint(uint64(stdout.Fd()), 10)
	stderrHandle := strconv.FormatUint(uint64(stderr.Fd()), 10)
	arguments := []string{executable, "--config", configPath, "--stdout-fd", stdoutHandle, "--stderr-fd", stderrHandle}
	command := exec.Command(executable, arguments[1:]...)
	command.Dir = filepath.Dir(executable)
	command.Env = []string{}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(stdout.Fd()), syscall.Handle(stderr.Fd())},
	}
	process := &outputBrokerProcess{}
	process.start = func() error {
		if err := command.Start(); err != nil {
			return err
		}
		process.command = command.Process
		return nil
	}
	process.wait = command.Wait
	return process, arguments, nil
}
