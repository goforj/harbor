//go:build linux

package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/helperpath"
)

const (
	linuxPKExecExecutable = "/usr/bin/pkexec"
	linuxPipeWaitDelay    = time.Second
	linuxHelperFileMode   = fs.FileMode(0o755)
)

const (
	linuxPKExecDeclinedExit    = 126
	linuxPKExecUnavailableExit = 127
)

// linuxNativeTransport binds one request to the installer-owned helper through polkit's fixed broker.
type linuxNativeTransport struct {
	inspectHelper linuxHelperInspector
	newCommand    linuxCommandFactory
}

// linuxHelperMetadata contains only the immutable installation facts needed before consent opens.
type linuxHelperMetadata struct {
	mode      fs.FileMode
	ownerUID  uint32
	ownerGID  uint32
	linkCount uint64
}

// linuxHelperInspector isolates the no-follow installation check from lifecycle classification tests.
type linuxHelperInspector func(string) (linuxHelperMetadata, error)

// linuxCommandSpec prevents the request from entering arguments, environment, or diagnostic output.
type linuxCommandSpec struct {
	executable     string
	arguments      []string
	standardInput  io.Reader
	standardOutput io.Writer
	standardError  io.Writer
	waitDelay      time.Duration
}

// linuxCommand exposes only the process boundaries needed to distinguish no-child and ambiguous states.
type linuxCommand interface {
	start() error
	wait() (int, error)
}

// linuxCommandFactory keeps process creation replaceable without making production paths configurable.
type linuxCommandFactory func(context.Context, linuxCommandSpec) linuxCommand

// osLinuxCommand adapts os/exec while keeping its diagnostic errors out of launcher results.
type osLinuxCommand struct {
	command *exec.Cmd
}

// newNativeTransport selects Linux's reviewed pkexec transport.
func newNativeTransport() Transport {
	return newLinuxNativeTransport(inspectInstalledLinuxHelper, newOSLinuxCommand)
}

// newLinuxNativeTransport fails fast when its private native seams are not wired.
func newLinuxNativeTransport(inspectHelper linuxHelperInspector, newCommand linuxCommandFactory) *linuxNativeTransport {
	if inspectHelper == nil {
		panic("launcher Linux transport requires a helper inspector")
	}
	if newCommand == nil {
		panic("launcher Linux transport requires a command factory")
	}
	return &linuxNativeTransport{inspectHelper: inspectHelper, newCommand: newCommand}
}

// Invoke opens native consent only after the fixed helper installation proves safe for unprivileged callers.
func (transport *linuxNativeTransport) Invoke(ctx context.Context, request io.Reader, response io.Writer) TransportResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || requiredInterfaceIsNil(request) || requiredInterfaceIsNil(response) {
		return TransportResult{State: TransportUnavailable}
	}

	helperExecutable := helperpath.Executable()
	metadata, err := transport.inspectHelper(helperExecutable)
	if err != nil || !validInstalledLinuxHelper(metadata) {
		return TransportResult{State: TransportUnavailable}
	}
	if ctx.Err() != nil {
		return TransportResult{State: TransportUnavailable}
	}

	capturedResponse := &boundedResponseWriter{}
	command := transport.newCommand(ctx, linuxCommandSpec{
		executable:     linuxPKExecExecutable,
		arguments:      []string{helperExecutable},
		standardInput:  io.LimitReader(request, helper.MaxRequestBytes+1),
		standardOutput: capturedResponse,
		standardError:  io.Discard,
		waitDelay:      linuxPipeWaitDelay,
	})
	if err := command.start(); err != nil {
		return TransportResult{State: TransportUnavailable}
	}

	exitCode, err := command.wait()
	if ctx.Err() != nil || err != nil {
		return TransportResult{State: TransportIndeterminate}
	}

	switch exitCode {
	case linuxPKExecDeclinedExit:
		return TransportResult{State: TransportDeclined}
	case linuxPKExecUnavailableExit:
		return TransportResult{State: TransportUnavailable}
	case ExitCodeSucceeded, ExitCodeHelperFailed:
		body := capturedResponse.Bytes()
		if len(body) != 0 {
			written, writeErr := response.Write(body)
			if writeErr != nil || written != len(body) {
				return TransportResult{State: TransportIndeterminate}
			}
		}
		return TransportResult{State: TransportCompleted, ExitCode: exitCode}
	default:
		return TransportResult{State: TransportIndeterminate}
	}
}

// inspectInstalledLinuxHelper uses lstat so a caller-controlled symlink can never pass preflight.
func inspectInstalledLinuxHelper(path string) (linuxHelperMetadata, error) {
	if path == "" {
		return linuxHelperMetadata{}, errors.New("installed Linux helper path is empty")
	}
	information, err := os.Lstat(path)
	if err != nil {
		return linuxHelperMetadata{}, err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		return linuxHelperMetadata{}, fmt.Errorf("inspect installed Linux helper: native file status is unavailable")
	}
	return linuxHelperMetadata{
		mode:      information.Mode(),
		ownerUID:  status.Uid,
		ownerGID:  status.Gid,
		linkCount: uint64(status.Nlink),
	}, nil
}

// validInstalledLinuxHelper permits only the exact root-owned, single-link executable shape installed by Harbor.
func validInstalledLinuxHelper(metadata linuxHelperMetadata) bool {
	return metadata.mode == linuxHelperFileMode && metadata.ownerUID == 0 && metadata.ownerGID == 0 && metadata.linkCount == 1
}

// newOSLinuxCommand constructs an absolute-path process without a shell or PATH lookup.
func newOSLinuxCommand(ctx context.Context, specification linuxCommandSpec) linuxCommand {
	command := exec.CommandContext(ctx, specification.executable, specification.arguments...)
	// Neither process needs caller configuration, so no environment or working directory crosses the privilege boundary.
	command.Env = []string{}
	command.Dir = "/"
	command.Stdin = specification.standardInput
	command.Stdout = specification.standardOutput
	command.Stderr = specification.standardError
	command.WaitDelay = specification.waitDelay
	return &osLinuxCommand{command: command}
}

// start marks the only boundary after which cancellation or wait failures can hide helper effects.
func (command *osLinuxCommand) start() error {
	return command.command.Start()
}

// wait normalizes an observed numeric process exit while preserving signals and pipe failures as ambiguous.
func (command *osLinuxCommand) wait() (int, error) {
	err := command.command.Wait()
	if err == nil {
		return command.command.ProcessState.ExitCode(), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() >= 0 {
		return exitError.ExitCode(), nil
	}
	return -1, err
}
