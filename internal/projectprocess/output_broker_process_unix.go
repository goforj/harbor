//go:build darwin || linux

package projectprocess

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// prepareOutputBrokerProcess uses Unix ExtraFiles so the broker receives read ends without exposing them in argv.
func prepareOutputBrokerProcess(executable, configPath string, stdout, stderr *os.File) (*outputBrokerProcess, []string, error) {
	arguments := []string{executable, "--config", configPath, "--stdout-fd", "3", "--stderr-fd", "4"}
	command := exec.Command(executable, arguments[1:]...)
	command.Dir = string(filepath.Separator)
	command.Env = []string{}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.ExtraFiles = []*os.File{stdout, stderr}
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
