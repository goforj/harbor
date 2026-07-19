package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const darwinTestHelperExecutable = "/Library/PrivilegedHelperTools/com.goforj.harbor.helper"

// darwinTestCommand exposes deterministic start and exact-wait behavior.
type darwinTestCommand struct {
	startErr  error
	waitCode  int
	waitErr   error
	startHook func()
	waitHook  func()
	starts    int
	waits     int
}

// start records the child creation boundary and applies its configured result.
func (command *darwinTestCommand) start() error {
	command.starts++
	if command.startHook != nil {
		command.startHook()
	}
	return command.startErr
}

// wait records exact-child collection and applies its configured result.
func (command *darwinTestCommand) wait() (int, error) {
	command.waits++
	if command.waitHook != nil {
		command.waitHook()
	}
	return command.waitCode, command.waitErr
}

// darwinTestWriteCloser records the external form and supports deterministic pipe failures.
type darwinTestWriteCloser struct {
	bytes.Buffer
	maximumWrite int
	writeErr     error
	closeErr     error
	closed       bool
}

// Write records at most the configured prefix before returning its test-specific result.
func (writer *darwinTestWriteCloser) Write(body []byte) (int, error) {
	if writer.maximumWrite > 0 && len(body) > writer.maximumWrite {
		body = body[:writer.maximumWrite]
	}
	written, _ := writer.Buffer.Write(body)
	return written, writer.writeErr
}

// Close records that the authorization channel was terminated before child wait.
func (writer *darwinTestWriteCloser) Close() error {
	writer.closed = true
	return writer.closeErr
}

// darwinFailingResponseWriter makes caller-output failure deterministic after an exact child exit.
type darwinFailingResponseWriter struct {
	err error
}

// Write returns the configured caller-output failure.
func (writer darwinFailingResponseWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// darwinTestExternalForm returns a non-secret deterministic 32-byte authorization fixture.
func darwinTestExternalForm() darwinAuthorizationExternalForm {
	var form darwinAuthorizationExternalForm
	for index := range form {
		form[index] = byte(index + 1)
	}
	return form
}

// darwinTestPipe constructs a disposable read descriptor and observable parent write side.
func darwinTestPipe(t *testing.T, writer *darwinTestWriteCloser) darwinAuthorizationPipeFactory {
	t.Helper()
	return func() (darwinAuthorizationPipe, error) {
		reader, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("open test authorization reader: %v", err)
		}
		return darwinAuthorizationPipe{reader: reader, writer: writer}, nil
	}
}

// newDarwinTestTransport constructs the complete successful seam graph for one focused mutation.
func newDarwinTestTransport(
	t *testing.T,
	writer *darwinTestWriteCloser,
	command darwinCommand,
	mutate func(*darwinNativeTransport),
) *darwinNativeTransport {
	t.Helper()
	transport := newDarwinNativeTransport(
		darwinTestHelperExecutable,
		func(string) (darwinHelperMetadata, error) {
			return darwinHelperMetadata{mode: darwinHelperFileMode, ownerUID: 0, linkCount: 1}, nil
		},
		func() (darwinAuthorizationGrant, error) {
			return darwinAuthorizationGrant{
				externalForm: darwinTestExternalForm(),
				release:      func() error { return nil },
			}, nil
		},
		darwinTestPipe(t, writer),
		func(context.Context, darwinCommandSpec) darwinCommand { return command },
	)
	if mutate != nil {
		mutate(transport)
	}
	return transport
}

// TestDarwinNativeTransportExecutesFixedReviewedShape verifies the complete successful process and byte boundary.
func TestDarwinNativeTransportExecutesFixedReviewedShape(t *testing.T) {
	requestBody := "bounded helper request"
	responseBody := "bounded helper response"
	authorizationWriter := &darwinTestWriteCloser{maximumWrite: 5}
	command := &darwinTestCommand{waitCode: ExitCodeSucceeded}
	var capturedSpec darwinCommandSpec
	var capturedRequest string
	transport := newDarwinTestTransport(t, authorizationWriter, command, func(transport *darwinNativeTransport) {
		transport.newCommand = func(_ context.Context, specification darwinCommandSpec) darwinCommand {
			capturedSpec = specification
			body, err := io.ReadAll(specification.standardInput)
			if err != nil {
				t.Fatalf("read captured helper request: %v", err)
			}
			capturedRequest = string(body)
			command.startHook = func() {
				if _, err := io.WriteString(specification.standardOutput, responseBody); err != nil {
					t.Fatalf("write captured helper response: %v", err)
				}
			}
			return command
		}
	})

	var response bytes.Buffer
	result := transport.Invoke(t.Context(), strings.NewReader(requestBody), &response)
	if result != (TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}) {
		t.Fatalf("Invoke() result = %#v", result)
	}
	if command.starts != 1 || command.waits != 1 {
		t.Fatalf("process calls = start:%d wait:%d, want 1 each", command.starts, command.waits)
	}
	if capturedSpec.executable != darwinTestHelperExecutable || capturedSpec.authorization == nil {
		t.Fatalf("command spec = %#v, want fixed executable and FD 3 source", capturedSpec)
	}
	if capturedRequest != requestBody || response.String() != responseBody {
		t.Fatalf("request = %q, response = %q", capturedRequest, response.String())
	}
	wantExternalForm := darwinTestExternalForm()
	if !authorizationWriter.closed || !bytes.Equal(authorizationWriter.Bytes(), wantExternalForm[:]) {
		t.Fatalf("authorization = %x, closed = %t", authorizationWriter.Bytes(), authorizationWriter.closed)
	}
}

// TestDarwinNativeTransportRetainsAuthorizationThroughExactWait verifies credentials outlive their one child and are then destroyed.
func TestDarwinNativeTransportRetainsAuthorizationThroughExactWait(t *testing.T) {
	events := make([]string, 0, 3)
	released := false
	command := &darwinTestCommand{
		waitCode: ExitCodeSucceeded,
		startHook: func() {
			if released {
				t.Fatal("authorization was released before child start")
			}
			events = append(events, "start")
		},
		waitHook: func() {
			if released {
				t.Fatal("authorization was released before exact child wait")
			}
			events = append(events, "wait")
		},
	}
	transport := newDarwinTestTransport(t, &darwinTestWriteCloser{}, command, func(transport *darwinNativeTransport) {
		transport.preauthorize = func() (darwinAuthorizationGrant, error) {
			return darwinAuthorizationGrant{
				externalForm: darwinTestExternalForm(),
				release: func() error {
					released = true
					events = append(events, "release")
					return nil
				},
			}, nil
		}
	})

	result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
	if result != (TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}) {
		t.Fatalf("Invoke() result = %#v", result)
	}
	if !released || !reflect.DeepEqual(events, []string{"start", "wait", "release"}) {
		t.Fatalf("authorization lifecycle = %v, released = %t", events, released)
	}
}

// TestDarwinNativeTransportClassifiesAuthorizationReleaseFailuresAtTheChildBoundary verifies cleanup cannot overstate certainty.
func TestDarwinNativeTransportClassifiesAuthorizationReleaseFailuresAtTheChildBoundary(t *testing.T) {
	testErr := errors.New("authorization release failed")
	tests := []struct {
		name          string
		startErr      error
		want          TransportState
		wantWaitCount int
	}{
		{name: "before child", startErr: errors.New("exec failed"), want: TransportUnavailable},
		{name: "after child", want: TransportIndeterminate, wantWaitCount: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			releaseCount := 0
			command := &darwinTestCommand{startErr: test.startErr, waitCode: ExitCodeSucceeded}
			transport := newDarwinTestTransport(t, &darwinTestWriteCloser{}, command, func(transport *darwinNativeTransport) {
				transport.preauthorize = func() (darwinAuthorizationGrant, error) {
					return darwinAuthorizationGrant{
						externalForm: darwinTestExternalForm(),
						release: func() error {
							releaseCount++
							return testErr
						},
					}, nil
				}
			})

			result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
			if result.State != test.want || releaseCount != 1 || command.waits != test.wantWaitCount {
				t.Fatalf("result = %#v, releases = %d, waits = %d", result, releaseCount, command.waits)
			}
		})
	}
}

// TestNewOSDarwinCommandHasNoAmbientProcessCapability verifies production exec metadata is entirely fixed.
func TestNewOSDarwinCommandHasNoAmbientProcessCapability(t *testing.T) {
	authorization, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open authorization descriptor: %v", err)
	}
	defer authorization.Close()
	request := strings.NewReader("request")
	response := &bytes.Buffer{}
	created := newOSDarwinCommand(t.Context(), darwinCommandSpec{
		executable:     darwinTestHelperExecutable,
		standardInput:  request,
		standardOutput: response,
		authorization:  authorization,
	})
	command, ok := created.(*osDarwinCommand)
	if !ok {
		t.Fatalf("newOSDarwinCommand() type = %T", created)
	}
	if command.command.Path != darwinTestHelperExecutable ||
		!reflect.DeepEqual(command.command.Args, []string{darwinTestHelperExecutable}) ||
		command.command.Env == nil || len(command.command.Env) != 0 ||
		command.command.Dir != "/" || command.command.Stdin != request || command.command.Stdout != response ||
		command.command.Stderr != io.Discard || len(command.command.ExtraFiles) != 1 ||
		command.command.ExtraFiles[0] != authorization || command.command.Cancel == nil {
		t.Fatalf("os/exec command contains ambient or unexpected process state: %#v", command.command)
	}
}

// TestValidInstalledDarwinHelperRequiresExactSetuidShape covers every immutable installer fact.
func TestValidInstalledDarwinHelperRequiresExactSetuidShape(t *testing.T) {
	valid := darwinHelperMetadata{mode: darwinHelperFileMode, ownerUID: 0, linkCount: 1}
	tests := []struct {
		name   string
		mutate func(*darwinHelperMetadata)
	}{
		{name: "ordinary executable", mutate: func(value *darwinHelperMetadata) { value.mode = 0o755 }},
		{name: "writable executable", mutate: func(value *darwinHelperMetadata) { value.mode = os.ModeSetuid | 0o775 }},
		{name: "directory", mutate: func(value *darwinHelperMetadata) { value.mode = os.ModeSetuid | os.ModeDir | 0o755 }},
		{name: "symlink", mutate: func(value *darwinHelperMetadata) { value.mode = os.ModeSetuid | os.ModeSymlink | 0o755 }},
		{name: "non-root owner", mutate: func(value *darwinHelperMetadata) { value.ownerUID = 501 }},
		{name: "second hard link", mutate: func(value *darwinHelperMetadata) { value.linkCount = 2 }},
	}
	if !validInstalledDarwinHelper(valid) {
		t.Fatal("valid helper metadata was rejected")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if validInstalledDarwinHelper(candidate) {
				t.Fatalf("metadata %#v was accepted", candidate)
			}
		})
	}
}

// TestDarwinNativeTransportClassifiesNoChildOutcomes verifies only explicit cancellation becomes Declined.
func TestDarwinNativeTransportClassifiesNoChildOutcomes(t *testing.T) {
	testErr := errors.New("native failure")
	tests := []struct {
		name   string
		mutate func(*darwinNativeTransport)
		want   TransportState
	}{
		{
			name: "unsafe helper",
			mutate: func(transport *darwinNativeTransport) {
				transport.inspectHelper = func(string) (darwinHelperMetadata, error) { return darwinHelperMetadata{}, nil }
			},
			want: TransportUnavailable,
		},
		{
			name: "inspection failure",
			mutate: func(transport *darwinNativeTransport) {
				transport.inspectHelper = func(string) (darwinHelperMetadata, error) { return darwinHelperMetadata{}, testErr }
			},
			want: TransportUnavailable,
		},
		{
			name: "consent declined",
			mutate: func(transport *darwinNativeTransport) {
				transport.preauthorize = func() (darwinAuthorizationGrant, error) {
					return darwinAuthorizationGrant{declined: true}, nil
				}
			},
			want: TransportDeclined,
		},
		{
			name: "authorization failure",
			mutate: func(transport *darwinNativeTransport) {
				transport.preauthorize = func() (darwinAuthorizationGrant, error) {
					return darwinAuthorizationGrant{}, testErr
				}
			},
			want: TransportUnavailable,
		},
		{
			name: "ambiguous authorization",
			mutate: func(transport *darwinNativeTransport) {
				transport.preauthorize = func() (darwinAuthorizationGrant, error) {
					return darwinAuthorizationGrant{declined: true}, testErr
				}
			},
			want: TransportUnavailable,
		},
		{
			name: "pipe failure",
			mutate: func(transport *darwinNativeTransport) {
				transport.newPipe = func() (darwinAuthorizationPipe, error) { return darwinAuthorizationPipe{}, testErr }
			},
			want: TransportUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := &darwinTestCommand{}
			writer := &darwinTestWriteCloser{}
			transport := newDarwinTestTransport(t, writer, command, test.mutate)
			result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
			if result.State != test.want {
				t.Fatalf("Invoke() result = %#v, want state %d", result, test.want)
			}
			if command.starts != 0 || command.waits != 0 {
				t.Fatalf("process calls = start:%d wait:%d, want none", command.starts, command.waits)
			}
		})
	}
}

// TestDarwinNativeTransportWaitsAfterEveryStartedChild verifies post-start failures cannot produce a no-child classification.
func TestDarwinNativeTransportWaitsAfterEveryStartedChild(t *testing.T) {
	testErr := errors.New("post-start failure")
	tests := []struct {
		name      string
		configure func(*testing.T, *darwinTestWriteCloser, *darwinTestCommand, *darwinNativeTransport) io.Writer
	}{
		{
			name: "authorization write",
			configure: func(_ *testing.T, writer *darwinTestWriteCloser, _ *darwinTestCommand, _ *darwinNativeTransport) io.Writer {
				writer.writeErr = testErr
				return io.Discard
			},
		},
		{
			name: "authorization close",
			configure: func(_ *testing.T, writer *darwinTestWriteCloser, _ *darwinTestCommand, _ *darwinNativeTransport) io.Writer {
				writer.closeErr = testErr
				return io.Discard
			},
		},
		{
			name: "wait failure",
			configure: func(_ *testing.T, _ *darwinTestWriteCloser, command *darwinTestCommand, _ *darwinNativeTransport) io.Writer {
				command.waitErr = testErr
				return io.Discard
			},
		},
		{
			name: "unknown exit",
			configure: func(_ *testing.T, _ *darwinTestWriteCloser, command *darwinTestCommand, _ *darwinNativeTransport) io.Writer {
				command.waitCode = 73
				return io.Discard
			},
		},
		{
			name: "caller output",
			configure: func(_ *testing.T, _ *darwinTestWriteCloser, command *darwinTestCommand, transport *darwinNativeTransport) io.Writer {
				transport.newCommand = func(_ context.Context, specification darwinCommandSpec) darwinCommand {
					command.startHook = func() { _, _ = io.WriteString(specification.standardOutput, "response") }
					return command
				}
				return darwinFailingResponseWriter{err: testErr}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &darwinTestWriteCloser{}
			command := &darwinTestCommand{waitCode: ExitCodeSucceeded}
			transport := newDarwinTestTransport(t, writer, command, nil)
			response := test.configure(t, writer, command, transport)

			result := transport.Invoke(t.Context(), strings.NewReader("request"), response)
			if result.State != TransportIndeterminate {
				t.Fatalf("Invoke() result = %#v, want indeterminate", result)
			}
			if command.starts != 1 || command.waits != 1 {
				t.Fatalf("process calls = start:%d wait:%d, want 1 each", command.starts, command.waits)
			}
		})
	}
}

// TestDarwinNativeTransportStartFailureRemainsUnavailable verifies a proved exec failure closes both pipe ends without waiting.
func TestDarwinNativeTransportStartFailureRemainsUnavailable(t *testing.T) {
	testErr := errors.New("exec failed")
	writer := &darwinTestWriteCloser{}
	command := &darwinTestCommand{startErr: testErr}
	transport := newDarwinTestTransport(t, writer, command, nil)

	result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
	if result.State != TransportUnavailable || command.starts != 1 || command.waits != 0 || !writer.closed {
		t.Fatalf("result = %#v, starts = %d, waits = %d, writer closed = %t", result, command.starts, command.waits, writer.closed)
	}
}

// TestWriteDarwinAuthorizationExternalFormHandlesPartialAndInvalidWriters proves exactly 32 bytes are required.
func TestWriteDarwinAuthorizationExternalFormHandlesPartialAndInvalidWriters(t *testing.T) {
	form := darwinTestExternalForm()
	partial := &darwinTestWriteCloser{maximumWrite: 3}
	if err := writeDarwinAuthorizationExternalForm(partial, form); err != nil {
		t.Fatalf("partial write error = %v", err)
	}
	if !bytes.Equal(partial.Bytes(), form[:]) {
		t.Fatalf("partial output = %x, want %x", partial.Bytes(), form)
	}

	zeroWriter := zeroProgressDarwinWriter{}
	if err := writeDarwinAuthorizationExternalForm(zeroWriter, form); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero-progress error = %v, want %v", err, io.ErrNoProgress)
	}
}

// zeroProgressDarwinWriter violates the Writer progress contract for fail-closed coverage.
type zeroProgressDarwinWriter struct{}

// Write reports no error and no progress.
func (zeroProgressDarwinWriter) Write([]byte) (int, error) {
	return 0, nil
}

// TestNewDarwinNativeTransportRequiresDependencies verifies malformed production assembly fails before consent.
func TestNewDarwinNativeTransportRequiresDependencies(t *testing.T) {
	inspect := func(string) (darwinHelperMetadata, error) { return darwinHelperMetadata{}, nil }
	authorize := func() (darwinAuthorizationGrant, error) {
		return darwinAuthorizationGrant{release: func() error { return nil }}, nil
	}
	pipe := func() (darwinAuthorizationPipe, error) { return darwinAuthorizationPipe{}, nil }
	command := func(context.Context, darwinCommandSpec) darwinCommand { return &darwinTestCommand{} }
	tests := []struct {
		name  string
		build func()
	}{
		{name: "helper path", build: func() { newDarwinNativeTransport("", inspect, authorize, pipe, command) }},
		{name: "helper inspector", build: func() { newDarwinNativeTransport(darwinTestHelperExecutable, nil, authorize, pipe, command) }},
		{name: "authorization", build: func() { newDarwinNativeTransport(darwinTestHelperExecutable, inspect, nil, pipe, command) }},
		{name: "pipe", build: func() { newDarwinNativeTransport(darwinTestHelperExecutable, inspect, authorize, nil, command) }},
		{name: "command", build: func() { newDarwinNativeTransport(darwinTestHelperExecutable, inspect, authorize, pipe, nil) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("newDarwinNativeTransport() did not panic")
				}
			}()
			test.build()
		})
	}
}

var _ darwinCommand = (*darwinTestCommand)(nil)
var _ io.WriteCloser = (*darwinTestWriteCloser)(nil)
