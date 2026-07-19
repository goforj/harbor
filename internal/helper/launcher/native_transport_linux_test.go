//go:build linux

package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/helperpath"
)

// TestNewNativeTransportSelectsLinuxBackend verifies the public constructor exposes the reviewed platform transport.
func TestNewNativeTransportSelectsLinuxBackend(t *testing.T) {
	if _, ok := NewNativeTransport().(*linuxNativeTransport); !ok {
		t.Fatalf("NewNativeTransport() = %T", NewNativeTransport())
	}
}

// TestLinuxNativeTransportUsesFixedPrivateChannel verifies the opaque request exists only on bounded stdin.
func TestLinuxNativeTransportUsesFixedPrivateChannel(t *testing.T) {
	requestBody := []byte("opaque-request-that-must-remain-on-stdin\n")
	responseBody := []byte("bounded-helper-response\n")
	var specification linuxCommandSpec
	command := &scriptedLinuxCommand{}
	command.waitFunc = func() (int, error) {
		gotRequest, err := io.ReadAll(specification.standardInput)
		if err != nil {
			t.Fatalf("read command stdin: %v", err)
		}
		if !bytes.Equal(gotRequest, requestBody) {
			t.Fatalf("command stdin = %q", gotRequest)
		}
		written, err := specification.standardOutput.Write(responseBody)
		if err != nil || written != len(responseBody) {
			t.Fatalf("write command stdout = %d, %v", written, err)
		}
		return ExitCodeSucceeded, nil
	}

	inspectCalls := 0
	transport := newLinuxNativeTransport(
		func(path string) (linuxHelperMetadata, error) {
			inspectCalls++
			if path != helperpath.Executable() {
				t.Fatalf("inspected path = %q", path)
			}
			return validLinuxHelperMetadata(), nil
		},
		func(_ context.Context, got linuxCommandSpec) linuxCommand {
			specification = got
			return command
		},
	)

	var response bytes.Buffer
	result := transport.Invoke(context.Background(), bytes.NewReader(requestBody), &response)
	if inspectCalls != 1 || command.startCalls != 1 || command.waitCalls != 1 {
		t.Fatalf("inspect = %d, start = %d, wait = %d", inspectCalls, command.startCalls, command.waitCalls)
	}
	if result != (TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}) {
		t.Fatalf("Invoke() = %#v", result)
	}
	if !bytes.Equal(response.Bytes(), responseBody) {
		t.Fatalf("response = %q", response.Bytes())
	}
	if specification.executable != "/usr/bin/pkexec" {
		t.Fatalf("executable = %q", specification.executable)
	}
	if !reflect.DeepEqual(specification.arguments, []string{"/usr/libexec/harbor-helper"}) {
		t.Fatalf("arguments = %#v", specification.arguments)
	}
	if specification.standardError != io.Discard {
		t.Fatalf("stderr = %T, want io.Discard", specification.standardError)
	}
	if specification.waitDelay != linuxPipeWaitDelay {
		t.Fatalf("wait delay = %s", specification.waitDelay)
	}
	commandMetadata := specification.executable + strings.Join(specification.arguments, "\x00") + fmt.Sprintf("%#v", result)
	if strings.Contains(commandMetadata, string(requestBody)) {
		t.Fatal("command metadata exposed the opaque request")
	}
}

// TestNewOSLinuxCommandForwardsNoCallerEnvironment verifies pkexec receives no ticket-bearing environment or shell metadata.
func TestNewOSLinuxCommandForwardsNoCallerEnvironment(t *testing.T) {
	requestBody := "opaque-request-never-an-argument"
	specification := linuxCommandSpec{
		executable:     linuxPKExecExecutable,
		arguments:      []string{helperpath.Executable()},
		standardInput:  strings.NewReader(requestBody),
		standardOutput: io.Discard,
		standardError:  io.Discard,
		waitDelay:      linuxPipeWaitDelay,
	}
	wrapped, ok := newOSLinuxCommand(context.Background(), specification).(*osLinuxCommand)
	if !ok {
		t.Fatalf("newOSLinuxCommand() = %T", newOSLinuxCommand(context.Background(), specification))
	}
	command := wrapped.command
	if command.Path != linuxPKExecExecutable {
		t.Fatalf("command path = %q", command.Path)
	}
	wantArguments := []string{linuxPKExecExecutable, helperpath.Executable()}
	if !reflect.DeepEqual(command.Args, wantArguments) {
		t.Fatalf("command arguments = %#v, want %#v", command.Args, wantArguments)
	}
	if command.Env == nil || len(command.Env) != 0 {
		t.Fatalf("command environment = %#v, want explicit empty environment", command.Env)
	}
	if command.Dir != "/" {
		t.Fatalf("command working directory = %q", command.Dir)
	}
	if strings.Contains(strings.Join(command.Args, "\x00")+strings.Join(command.Env, "\x00"), requestBody) {
		t.Fatal("command arguments or environment exposed the request")
	}
	if command.Stdin != specification.standardInput || command.Stdout != io.Discard || command.Stderr != io.Discard {
		t.Fatal("command standard streams do not match the private channel")
	}
	if command.WaitDelay != linuxPipeWaitDelay {
		t.Fatalf("command wait delay = %s", command.WaitDelay)
	}
}

// TestLinuxNativeTransportMapsDocumentedPKExecExits verifies only documented no-helper results avoid ambiguity.
func TestLinuxNativeTransportMapsDocumentedPKExecExits(t *testing.T) {
	waitFailure := errors.New("wait failed")
	tests := []struct {
		name         string
		exitCode     int
		waitErr      error
		want         TransportResult
		wantResponse bool
	}{
		{name: "helper success", exitCode: 0, want: TransportResult{State: TransportCompleted, ExitCode: 0}, wantResponse: true},
		{name: "helper failure", exitCode: 1, want: TransportResult{State: TransportCompleted, ExitCode: 1}, wantResponse: true},
		{name: "consent declined", exitCode: 126, want: TransportResult{State: TransportDeclined}},
		{name: "authorization unavailable", exitCode: 127, want: TransportResult{State: TransportUnavailable}},
		{name: "unexpected exit", exitCode: 2, want: TransportResult{State: TransportIndeterminate}},
		{name: "signal without exit", exitCode: -1, want: TransportResult{State: TransportIndeterminate}},
		{name: "ambiguous wait failure", exitCode: 0, waitErr: waitFailure, want: TransportResult{State: TransportIndeterminate}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := &scriptedLinuxCommand{waitCode: test.exitCode, waitErr: test.waitErr}
			var specification linuxCommandSpec
			command.waitFunc = func() (int, error) {
				_, _ = specification.standardOutput.Write([]byte("native-output"))
				return command.waitCode, command.waitErr
			}
			transport := validLinuxTransport(command, &specification)
			var response bytes.Buffer
			result := transport.Invoke(context.Background(), strings.NewReader("request"), &response)
			if result != test.want {
				t.Fatalf("Invoke() = %#v, want %#v", result, test.want)
			}
			if got := response.Len() != 0; got != test.wantResponse {
				t.Fatalf("response length = %d, wantResponse = %t", response.Len(), test.wantResponse)
			}
		})
	}
}

// TestLinuxNativeTransportTreatsStartFailureAsUnavailable verifies no failed exec attempt is mistaken for a child effect.
func TestLinuxNativeTransportTreatsStartFailureAsUnavailable(t *testing.T) {
	command := &scriptedLinuxCommand{startErr: errors.New("pkexec is absent")}
	transport := validLinuxTransport(command, nil)
	var response bytes.Buffer
	result := transport.Invoke(context.Background(), strings.NewReader("request"), &response)
	if result.State != TransportUnavailable || command.waitCalls != 0 || response.Len() != 0 {
		t.Fatalf("result = %#v, wait calls = %d, response = %q", result, command.waitCalls, response.Bytes())
	}
}

// TestLinuxNativeTransportRejectsMalformedInstallationsBeforeConsent verifies exact installer metadata is mandatory.
func TestLinuxNativeTransportRejectsMalformedInstallationsBeforeConsent(t *testing.T) {
	statFailure := errors.New("lstat failed")
	tests := []struct {
		name     string
		metadata linuxHelperMetadata
		err      error
	}{
		{name: "missing", err: statFailure},
		{name: "directory", metadata: linuxHelperMetadata{mode: fs.ModeDir | 0o755, ownerUID: 0, linkCount: 1}},
		{name: "owner executable only", metadata: linuxHelperMetadata{mode: 0o700, ownerUID: 0, linkCount: 1}},
		{name: "group writable", metadata: linuxHelperMetadata{mode: 0o775, ownerUID: 0, linkCount: 1}},
		{name: "setuid", metadata: linuxHelperMetadata{mode: fs.ModeSetuid | 0o755, ownerUID: 0, linkCount: 1}},
		{name: "non-root owner", metadata: linuxHelperMetadata{mode: 0o755, ownerUID: 1000, linkCount: 1}},
		{name: "non-root group", metadata: linuxHelperMetadata{mode: 0o755, ownerUID: 0, ownerGID: 1000, linkCount: 1}},
		{name: "zero links", metadata: linuxHelperMetadata{mode: 0o755, ownerUID: 0, linkCount: 0}},
		{name: "hard linked", metadata: linuxHelperMetadata{mode: 0o755, ownerUID: 0, linkCount: 2}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commandCalls := 0
			transport := newLinuxNativeTransport(
				func(path string) (linuxHelperMetadata, error) {
					if path != helperpath.Executable() {
						t.Fatalf("inspected path = %q", path)
					}
					return test.metadata, test.err
				},
				func(context.Context, linuxCommandSpec) linuxCommand {
					commandCalls++
					return &scriptedLinuxCommand{}
				},
			)
			result := transport.Invoke(context.Background(), strings.NewReader("request"), io.Discard)
			if result.State != TransportUnavailable || commandCalls != 0 {
				t.Fatalf("result = %#v, command calls = %d", result, commandCalls)
			}
		})
	}
}

// TestInspectInstalledLinuxHelperDoesNotFollowSymlinks verifies the production inspector observes the directory entry itself.
func TestInspectInstalledLinuxHelperDoesNotFollowSymlinks(t *testing.T) {
	directory := t.TempDir()
	executable := directory + "/helper"
	if err := os.WriteFile(executable, []byte("helper"), 0o755); err != nil {
		t.Fatalf("write helper fixture: %v", err)
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatalf("chmod helper fixture: %v", err)
	}
	metadata, err := inspectInstalledLinuxHelper(executable)
	if err != nil {
		t.Fatalf("inspect helper fixture: %v", err)
	}
	if metadata.mode != 0o755 || metadata.ownerUID != uint32(os.Getuid()) || metadata.ownerGID != uint32(os.Getgid()) || metadata.linkCount != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}

	link := directory + "/helper-link"
	if err := os.Symlink(executable, link); err != nil {
		t.Fatalf("create helper symlink: %v", err)
	}
	linkMetadata, err := inspectInstalledLinuxHelper(link)
	if err != nil {
		t.Fatalf("inspect helper symlink: %v", err)
	}
	if linkMetadata.mode&fs.ModeSymlink == 0 || validInstalledLinuxHelper(linkMetadata) {
		t.Fatalf("symlink metadata = %#v", linkMetadata)
	}
}

// TestLinuxNativeTransportCancellationBoundaries verifies only cancellation before Start can prove no child exists.
func TestLinuxNativeTransportCancellationBoundaries(t *testing.T) {
	t.Run("before preflight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		inspectCalls := 0
		transport := newLinuxNativeTransport(
			func(string) (linuxHelperMetadata, error) {
				inspectCalls++
				return validLinuxHelperMetadata(), nil
			},
			func(context.Context, linuxCommandSpec) linuxCommand {
				t.Fatal("command factory called after cancellation")
				return nil
			},
		)
		if result := transport.Invoke(ctx, strings.NewReader("request"), io.Discard); result.State != TransportUnavailable || inspectCalls != 0 {
			t.Fatalf("result = %#v, inspect calls = %d", result, inspectCalls)
		}
	})

	t.Run("after preflight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		transport := newLinuxNativeTransport(
			func(string) (linuxHelperMetadata, error) {
				cancel()
				return validLinuxHelperMetadata(), nil
			},
			func(context.Context, linuxCommandSpec) linuxCommand {
				t.Fatal("command factory called after cancellation")
				return nil
			},
		)
		if result := transport.Invoke(ctx, strings.NewReader("request"), io.Discard); result.State != TransportUnavailable {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("failed start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		command := &scriptedLinuxCommand{startFunc: func() error {
			cancel()
			return context.Canceled
		}}
		transport := validLinuxTransport(command, nil)
		if result := transport.Invoke(ctx, strings.NewReader("request"), io.Discard); result.State != TransportUnavailable || command.waitCalls != 0 {
			t.Fatalf("result = %#v, wait calls = %d", result, command.waitCalls)
		}
	})

	t.Run("after start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var specification linuxCommandSpec
		command := &scriptedLinuxCommand{waitFunc: func() (int, error) {
			_, _ = specification.standardOutput.Write([]byte("response"))
			cancel()
			return ExitCodeSucceeded, nil
		}}
		transport := validLinuxTransport(command, &specification)
		var response bytes.Buffer
		if result := transport.Invoke(ctx, strings.NewReader("request"), &response); result.State != TransportIndeterminate || response.Len() != 0 {
			t.Fatalf("result = %#v, response = %q", result, response.Bytes())
		}
	})
}

// TestLinuxNativeTransportBoundsBothProtocolStreams verifies a noisy peer cannot grow retained transport state.
func TestLinuxNativeTransportBoundsBothProtocolStreams(t *testing.T) {
	requestBody := bytes.Repeat([]byte{'r'}, helper.MaxRequestBytes*2)
	responseBody := bytes.Repeat([]byte{'s'}, helper.MaxResponseBytes*2)
	var specification linuxCommandSpec
	command := &scriptedLinuxCommand{waitFunc: func() (int, error) {
		gotRequest, err := io.ReadAll(specification.standardInput)
		if err != nil {
			t.Fatalf("read bounded request: %v", err)
		}
		if len(gotRequest) != helper.MaxRequestBytes+1 {
			t.Fatalf("request bytes = %d", len(gotRequest))
		}
		written, err := specification.standardOutput.Write(responseBody)
		if err != nil || written != len(responseBody) {
			t.Fatalf("write oversized response = %d, %v", written, err)
		}
		return ExitCodeSucceeded, nil
	}}
	transport := validLinuxTransport(command, &specification)
	var response bytes.Buffer
	result := transport.Invoke(context.Background(), bytes.NewReader(requestBody), &response)
	if result.State != TransportCompleted {
		t.Fatalf("result = %#v", result)
	}
	if response.Len() != helper.MaxResponseBytes+1 {
		t.Fatalf("response bytes = %d", response.Len())
	}
}

// TestLinuxNativeTransportTreatsResponseWriteFailuresAsIndeterminate verifies completed effects are not hidden by client I/O.
func TestLinuxNativeTransportTreatsResponseWriteFailuresAsIndeterminate(t *testing.T) {
	tests := []struct {
		name   string
		writer io.Writer
	}{
		{name: "error", writer: linuxWriterFunc(func([]byte) (int, error) { return 0, errors.New("write failed") })},
		{name: "short write", writer: linuxWriterFunc(func(body []byte) (int, error) { return len(body) - 1, nil })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var specification linuxCommandSpec
			command := &scriptedLinuxCommand{waitFunc: func() (int, error) {
				_, _ = specification.standardOutput.Write([]byte("response"))
				return ExitCodeSucceeded, nil
			}}
			transport := validLinuxTransport(command, &specification)
			if result := transport.Invoke(context.Background(), strings.NewReader("request"), test.writer); result.State != TransportIndeterminate {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

// TestNewLinuxNativeTransportRequiresPrivateSeams verifies missing production wiring fails before invocation.
func TestNewLinuxNativeTransportRequiresPrivateSeams(t *testing.T) {
	tests := []struct {
		name  string
		build func()
	}{
		{name: "inspector", build: func() {
			newLinuxNativeTransport(nil, func(context.Context, linuxCommandSpec) linuxCommand { return &scriptedLinuxCommand{} })
		}},
		{name: "command factory", build: func() {
			newLinuxNativeTransport(func(string) (linuxHelperMetadata, error) { return validLinuxHelperMetadata(), nil }, nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor panic")
				}
			}()
			test.build()
		})
	}
}

// validLinuxHelperMetadata returns the only installer shape admitted before pkexec opens.
func validLinuxHelperMetadata() linuxHelperMetadata {
	return linuxHelperMetadata{mode: linuxHelperFileMode, ownerUID: 0, ownerGID: 0, linkCount: 1}
}

// validLinuxTransport builds one transport whose installation preflight succeeds deterministically.
func validLinuxTransport(command linuxCommand, captured *linuxCommandSpec) *linuxNativeTransport {
	return newLinuxNativeTransport(
		func(string) (linuxHelperMetadata, error) { return validLinuxHelperMetadata(), nil },
		func(_ context.Context, specification linuxCommandSpec) linuxCommand {
			if captured != nil {
				*captured = specification
			}
			return command
		},
	)
}

type scriptedLinuxCommand struct {
	startCalls int
	waitCalls  int
	startErr   error
	waitCode   int
	waitErr    error
	startFunc  func() error
	waitFunc   func() (int, error)
}

// start records the point at which the fake may represent a native child.
func (command *scriptedLinuxCommand) start() error {
	command.startCalls++
	if command.startFunc != nil {
		return command.startFunc()
	}
	return command.startErr
}

// wait returns the scripted process observation after a successful start.
func (command *scriptedLinuxCommand) wait() (int, error) {
	command.waitCalls++
	if command.waitFunc != nil {
		return command.waitFunc()
	}
	return command.waitCode, command.waitErr
}

type linuxWriterFunc func([]byte) (int, error)

// Write adapts one test function to an io.Writer.
func (writer linuxWriterFunc) Write(body []byte) (int, error) {
	return writer(body)
}
