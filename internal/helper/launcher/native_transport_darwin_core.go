package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"

	"github.com/goforj/harbor/internal/helper"
)

const darwinAuthorizationExternalFormLength = 32

const darwinHelperFileMode = fs.FileMode(0o755) | os.ModeSetuid

// darwinNativeTransport binds one request to a preauthorized execution of the fixed setuid helper.
type darwinNativeTransport struct {
	helperExecutable string
	inspectHelper    darwinHelperInspector
	preauthorize     darwinAuthorizationProvider
	newPipe          darwinAuthorizationPipeFactory
	newCommand       darwinCommandFactory
}

// darwinHelperMetadata contains only the immutable installation facts required before consent opens.
type darwinHelperMetadata struct {
	mode      fs.FileMode
	ownerUID  uint32
	linkCount uint64
}

// darwinHelperInspector isolates no-follow helper validation from consent and process tests.
type darwinHelperInspector func(string) (darwinHelperMetadata, error)

// darwinAuthorizationExternalForm is the complete transferable Authorization Services reference.
type darwinAuthorizationExternalForm [darwinAuthorizationExternalFormLength]byte

// darwinAuthorizationGrant retains preauthorized execute authority until its exact child has completed.
type darwinAuthorizationGrant struct {
	externalForm darwinAuthorizationExternalForm
	declined     bool
	release      func() error
}

// darwinAuthorizationProvider preauthorizes execute authority and distinguishes user dismissal before child creation.
type darwinAuthorizationProvider func() (darwinAuthorizationGrant, error)

// darwinAuthorizationPipe contains the only extra descriptor inherited by the helper.
type darwinAuthorizationPipe struct {
	reader *os.File
	writer io.WriteCloser
}

// darwinAuthorizationPipeFactory creates one private channel whose read side becomes child FD 3.
type darwinAuthorizationPipeFactory func() (darwinAuthorizationPipe, error)

// darwinCommandSpec contains only fixed process metadata and the three reviewed byte streams.
type darwinCommandSpec struct {
	executable     string
	standardInput  io.Reader
	standardOutput io.Writer
	authorization  *os.File
}

// darwinCommand exposes only exact-child start and wait boundaries.
type darwinCommand interface {
	start() error
	wait() (int, error)
}

// darwinCommandFactory keeps process creation replaceable without making production paths or arguments configurable.
type darwinCommandFactory func(context.Context, darwinCommandSpec) darwinCommand

// osDarwinCommand adapts direct os/exec execution to the narrow lifecycle boundary.
type osDarwinCommand struct {
	command *exec.Cmd
}

// newDarwinNativeTransport fails fast when any private native seam is absent.
func newDarwinNativeTransport(
	helperExecutable string,
	inspectHelper darwinHelperInspector,
	preauthorize darwinAuthorizationProvider,
	newPipe darwinAuthorizationPipeFactory,
	newCommand darwinCommandFactory,
) *darwinNativeTransport {
	if helperExecutable == "" {
		panic("launcher Darwin transport requires a fixed helper executable")
	}
	if inspectHelper == nil {
		panic("launcher Darwin transport requires a helper inspector")
	}
	if preauthorize == nil {
		panic("launcher Darwin transport requires an authorization provider")
	}
	if newPipe == nil {
		panic("launcher Darwin transport requires an authorization pipe factory")
	}
	if newCommand == nil {
		panic("launcher Darwin transport requires a command factory")
	}
	return &darwinNativeTransport{
		helperExecutable: helperExecutable,
		inspectHelper:    inspectHelper,
		preauthorize:     preauthorize,
		newPipe:          newPipe,
		newCommand:       newCommand,
	}
}

// Invoke preauthorizes one execution and classifies every anomaly after child start as indeterminate.
func (transport *darwinNativeTransport) Invoke(ctx context.Context, request io.Reader, response io.Writer) (result TransportResult) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || requiredInterfaceIsNil(request) || requiredInterfaceIsNil(response) {
		return TransportResult{State: TransportUnavailable}
	}

	metadata, err := transport.inspectHelper(transport.helperExecutable)
	if err != nil || !validInstalledDarwinHelper(metadata) {
		return TransportResult{State: TransportUnavailable}
	}
	grant, err := transport.preauthorize()
	if grant.declined {
		if err != nil {
			return TransportResult{State: TransportUnavailable}
		}
		return TransportResult{State: TransportDeclined}
	}
	if err != nil || grant.release == nil {
		return TransportResult{State: TransportUnavailable}
	}
	childStarted := false
	defer func() {
		if err := grant.release(); err != nil {
			if childStarted {
				result = TransportResult{State: TransportIndeterminate}
				return
			}
			result = TransportResult{State: TransportUnavailable}
		}
	}()
	if ctx.Err() != nil {
		return TransportResult{State: TransportUnavailable}
	}

	authorization, err := transport.newPipe()
	if err != nil || authorization.reader == nil || requiredInterfaceIsNil(authorization.writer) {
		closeDarwinAuthorizationPipe(authorization)
		return TransportResult{State: TransportUnavailable}
	}
	capturedResponse := &boundedResponseWriter{}
	command := transport.newCommand(ctx, darwinCommandSpec{
		executable:     transport.helperExecutable,
		standardInput:  io.LimitReader(request, helper.MaxRequestBytes+1),
		standardOutput: capturedResponse,
		authorization:  authorization.reader,
	})
	if requiredInterfaceIsNil(command) || command.start() != nil {
		closeDarwinAuthorizationPipe(authorization)
		return TransportResult{State: TransportUnavailable}
	}
	childStarted = true

	readerCloseErr := authorization.reader.Close()
	writeErr := writeDarwinAuthorizationExternalForm(authorization.writer, grant.externalForm)
	writerCloseErr := authorization.writer.Close()
	exitCode, waitErr := command.wait()
	if readerCloseErr != nil || writeErr != nil || writerCloseErr != nil || waitErr != nil || ctx.Err() != nil {
		return TransportResult{State: TransportIndeterminate}
	}
	if exitCode != ExitCodeSucceeded && exitCode != ExitCodeHelperFailed {
		return TransportResult{State: TransportIndeterminate}
	}
	body := capturedResponse.Bytes()
	if len(body) != 0 {
		written, err := response.Write(body)
		if err != nil || written != len(body) {
			return TransportResult{State: TransportIndeterminate}
		}
	}
	return TransportResult{State: TransportCompleted, ExitCode: exitCode}
}

// validInstalledDarwinHelper accepts only the installer-owned direct setuid executable shape.
func validInstalledDarwinHelper(metadata darwinHelperMetadata) bool {
	return metadata.mode == darwinHelperFileMode && metadata.ownerUID == 0 && metadata.linkCount == 1
}

// newDarwinAuthorizationPipe creates the sole capability channel inherited by the fixed helper.
func newDarwinAuthorizationPipe() (darwinAuthorizationPipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return darwinAuthorizationPipe{}, err
	}
	return darwinAuthorizationPipe{reader: reader, writer: writer}, nil
}

// closeDarwinAuthorizationPipe releases both parent handles before any no-child result is returned.
func closeDarwinAuthorizationPipe(pipe darwinAuthorizationPipe) {
	if pipe.reader != nil {
		_ = pipe.reader.Close()
	}
	if !requiredInterfaceIsNil(pipe.writer) {
		_ = pipe.writer.Close()
	}
}

// writeDarwinAuthorizationExternalForm writes all and only the 32 external-form bytes before closing the pipe.
func writeDarwinAuthorizationExternalForm(writer io.Writer, externalForm darwinAuthorizationExternalForm) error {
	remaining := externalForm[:]
	for len(remaining) > 0 {
		written, err := writer.Write(remaining)
		if written < 0 || written > len(remaining) {
			return errors.New("authorization pipe returned an invalid write count")
		}
		remaining = remaining[written:]
		if err != nil {
			return fmt.Errorf("write authorization external form: %w", err)
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

// newOSDarwinCommand constructs an absolute-path process with no caller-selected arguments, environment, or working directory.
func newOSDarwinCommand(ctx context.Context, specification darwinCommandSpec) darwinCommand {
	command := exec.CommandContext(ctx, specification.executable)
	command.Args = []string{specification.executable}
	command.Env = []string{}
	command.Dir = "/"
	command.Stdin = specification.standardInput
	command.Stdout = specification.standardOutput
	command.Stderr = io.Discard
	command.ExtraFiles = []*os.File{specification.authorization}
	return &osDarwinCommand{command: command}
}

// start marks the only boundary after which a helper effect may be hidden by lifecycle failure.
func (command *osDarwinCommand) start() error {
	return command.command.Start()
}

// wait returns only an exact numeric child exit and treats signals or wait failures as ambiguous.
func (command *osDarwinCommand) wait() (int, error) {
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
