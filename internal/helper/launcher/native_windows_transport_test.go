package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const windowsTestHelperExecutable = `C:\Program Files\GoForj\Harbor\harbor-helper.exe`

// testWindowsProcess provides deterministic PID, exact-wait, and handle-release behavior.
type testWindowsProcess struct {
	pid      uint32
	pidErr   error
	waitCode int
	waitErr  error
	closeErr error
	waitGate chan struct{}
	waits    atomic.Int32
	closes   atomic.Int32
}

// processID returns the configured exact process identity.
func (process *testWindowsProcess) processID() (uint32, error) {
	return process.pid, process.pidErr
}

// wait blocks until the test helper lifecycle reaches its configured completion boundary.
func (process *testWindowsProcess) wait() (int, error) {
	process.waits.Add(1)
	if process.waitGate != nil {
		<-process.waitGate
	}
	return process.waitCode, process.waitErr
}

// close records release of the retained process object.
func (process *testWindowsProcess) close() error {
	process.closes.Add(1)
	return process.closeErr
}

// testWindowsConnection records one request message and exposes one response stream.
type testWindowsConnection struct {
	response       io.Reader
	request        bytes.Buffer
	pid            uint32
	pidErr         error
	writeErr       error
	shortWrite     bool
	closeWriteErr  error
	closeErr       error
	closeWriteCall atomic.Int32
	writeCalls     atomic.Int32
	closeCalls     atomic.Int32
	finish         func()
	finishOnce     sync.Once
}

// Read forwards the configured response and releases the process when the stream terminates.
func (connection *testWindowsConnection) Read(body []byte) (int, error) {
	written, err := connection.response.Read(body)
	if err != nil {
		connection.finishOnce.Do(connection.finish)
	}
	return written, err
}

// Write records exactly one request message or applies its configured failure.
func (connection *testWindowsConnection) Write(body []byte) (int, error) {
	connection.writeCalls.Add(1)
	if connection.shortWrite && len(body) > 0 {
		body = body[:len(body)-1]
	}
	written, _ := connection.request.Write(body)
	return written, connection.writeErr
}

// Close releases the simulated helper so exact wait can complete after an exchange failure.
func (connection *testWindowsConnection) Close() error {
	connection.closeCalls.Add(1)
	connection.finishOnce.Do(connection.finish)
	return connection.closeErr
}

// closeWrite records the message-mode end marker sent after the request.
func (connection *testWindowsConnection) closeWrite() error {
	connection.closeWriteCall.Add(1)
	return connection.closeWriteErr
}

// clientProcessID returns the kernel identity configured for the accepted client.
func (connection *testWindowsConnection) clientProcessID() (uint32, error) {
	return connection.pid, connection.pidErr
}

// testWindowsListener provides one fixed route and one optionally blocked accept.
type testWindowsListener struct {
	name       string
	connection windowsPipeConnection
	acceptErr  error
	closeErr   error
	acceptGate chan struct{}
	finish     func()
	closeOnce  sync.Once
	closes     atomic.Int32
}

// path returns the fixed test route.
func (listener *testWindowsListener) path() string {
	return listener.name
}

// accept returns one configured client after any test gate opens.
func (listener *testWindowsListener) accept() (windowsPipeConnection, error) {
	if listener.acceptGate != nil {
		<-listener.acceptGate
	}
	return listener.connection, listener.acceptErr
}

// close releases blocked accept and any process configured to fail before connection.
func (listener *testWindowsListener) close() error {
	listener.closes.Add(1)
	listener.closeOnce.Do(func() {
		if listener.acceptGate != nil {
			close(listener.acceptGate)
		}
		if listener.finish != nil {
			listener.finish()
		}
	})
	return listener.closeErr
}

// errorWindowsResponseReader makes response-stream failure deterministic.
type errorWindowsResponseReader struct {
	err error
}

// Read returns the configured response failure.
func (reader errorWindowsResponseReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// testWindowsLifecycle creates one helper process gate shared by its pipe and exact wait.
func testWindowsLifecycle() (chan struct{}, func()) {
	gate := make(chan struct{})
	var once sync.Once
	return gate, func() { once.Do(func() { close(gate) }) }
}

// validWindowsTestMetadata returns the complete trusted installation shape.
func validWindowsTestMetadata() windowsHelperMetadata {
	return windowsHelperMetadata{
		regular:          true,
		linkCount:        1,
		finalPathMatches: true,
		ownerTrusted:     true,
		daclProtected:    true,
		daclRestricted:   true,
		signatureTrusted: true,
	}
}

// TestWindowsNativeTransportUsesOneCorrelatedPipeAndExactProcess verifies the complete successful boundary.
func TestWindowsNativeTransportUsesOneCorrelatedPipeAndExactProcess(t *testing.T) {
	gate, finish := testWindowsLifecycle()
	process := &testWindowsProcess{pid: 4242, waitCode: ExitCodeSucceeded, waitGate: gate}
	connection := &testWindowsConnection{
		response: strings.NewReader("helper response"),
		pid:      process.pid,
		finish:   finish,
	}
	listener := &testWindowsListener{name: `\\.\pipe\goforj-harbor-helper-route`, connection: connection}
	inspectionCloses := 0
	var launchedExecutable string
	var launchedPipe string
	transport := newWindowsNativeTransport(
		windowsTestHelperExecutable,
		func(path string) (windowsHelperInspection, error) {
			if path != windowsTestHelperExecutable {
				t.Fatalf("inspect path = %q", path)
			}
			return windowsHelperInspection{
				metadata: validWindowsTestMetadata(),
				close: func() error {
					inspectionCloses++
					return nil
				},
			}, nil
		},
		func() (windowsPipeListener, error) { return listener, nil },
		func(executable string, pipeName string) (windowsLaunchOutcome, error) {
			launchedExecutable = executable
			launchedPipe = pipeName
			return windowsLaunchOutcome{process: process, created: true}, nil
		},
	)

	var response bytes.Buffer
	result := transport.Invoke(t.Context(), strings.NewReader("helper request"), &response)
	if result != (TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}) {
		t.Fatalf("Invoke() result = %#v", result)
	}
	if launchedExecutable != windowsTestHelperExecutable || launchedPipe != listener.name {
		t.Fatalf("launch = (%q, %q)", launchedExecutable, launchedPipe)
	}
	if connection.request.String() != "helper request" || response.String() != "helper response" {
		t.Fatalf("request = %q, response = %q", connection.request.String(), response.String())
	}
	if connection.writeCalls.Load() != 1 || connection.closeWriteCall.Load() != 1 || connection.closeCalls.Load() != 1 {
		t.Fatalf("connection calls = write:%d close-write:%d close:%d", connection.writeCalls.Load(), connection.closeWriteCall.Load(), connection.closeCalls.Load())
	}
	if process.waits.Load() != 1 || process.closes.Load() != 1 || inspectionCloses != 1 || listener.closes.Load() != 1 {
		t.Fatalf("lifecycle = waits:%d process-close:%d inspection-close:%d listener-close:%d", process.waits.Load(), process.closes.Load(), inspectionCloses, listener.closes.Load())
	}
}

// TestWindowsNativeTransportClassifiesOnlyProvedNoChildFailures verifies consent dismissal is the sole Declined path.
func TestWindowsNativeTransportClassifiesOnlyProvedNoChildFailures(t *testing.T) {
	testErr := errors.New("native failure")
	tests := []struct {
		name    string
		inspect windowsHelperInspector
		pipe    windowsPipeFactory
		launch  windowsElevatedLauncher
		want    TransportState
	}{
		{
			name: "unsafe helper",
			inspect: func(string) (windowsHelperInspection, error) {
				return windowsHelperInspection{metadata: windowsHelperMetadata{}, close: func() error { return nil }}, nil
			},
			want: TransportUnavailable,
		},
		{
			name:    "inspection failure",
			inspect: func(string) (windowsHelperInspection, error) { return windowsHelperInspection{}, testErr },
			want:    TransportUnavailable,
		},
		{
			name: "pipe failure",
			pipe: func() (windowsPipeListener, error) {
				return nil, testErr
			},
			want: TransportUnavailable,
		},
		{
			name: "UAC declined",
			launch: func(string, string) (windowsLaunchOutcome, error) {
				return windowsLaunchOutcome{declined: true}, nil
			},
			want: TransportDeclined,
		},
		{
			name: "launch unavailable",
			launch: func(string, string) (windowsLaunchOutcome, error) {
				return windowsLaunchOutcome{}, testErr
			},
			want: TransportUnavailable,
		},
		{
			name: "created process handle unavailable",
			launch: func(string, string) (windowsLaunchOutcome, error) {
				return windowsLaunchOutcome{created: true}, testErr
			},
			want: TransportIndeterminate,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection := test.inspect
			if inspection == nil {
				inspection = func(string) (windowsHelperInspection, error) {
					return windowsHelperInspection{metadata: validWindowsTestMetadata(), close: func() error { return nil }}, nil
				}
			}
			pipe := test.pipe
			if pipe == nil {
				pipe = func() (windowsPipeListener, error) {
					return &testWindowsListener{name: "pipe"}, nil
				}
			}
			launch := test.launch
			if launch == nil {
				launch = func(string, string) (windowsLaunchOutcome, error) {
					t.Fatal("launch reached unexpectedly")
					return windowsLaunchOutcome{}, nil
				}
			}
			transport := newWindowsNativeTransport(windowsTestHelperExecutable, inspection, pipe, launch)
			result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
			if result.State != test.want {
				t.Fatalf("Invoke() result = %#v, want state %d", result, test.want)
			}
		})
	}
}

// TestWindowsNativeTransportWaitsAfterEveryCreatedProcess verifies post-creation anomalies never claim no child.
func TestWindowsNativeTransportWaitsAfterEveryCreatedProcess(t *testing.T) {
	testErr := errors.New("post-creation failure")
	tests := []struct {
		name           string
		configure      func(*testWindowsProcess, *testWindowsConnection, *testWindowsListener, *windowsHelperInspection)
		launchDeclined bool
		launchErr      error
	}{
		{name: "launch error after creation", launchErr: testErr},
		{name: "decline flag after creation", launchDeclined: true},
		{name: "process ID read", configure: func(process *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			process.pidErr = testErr
		}},
		{name: "zero process ID", configure: func(process *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			process.pid = 0
		}},
		{name: "PID mismatch", configure: func(process *testWindowsProcess, connection *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			connection.pid = process.pid + 1
		}},
		{name: "request write", configure: func(_ *testWindowsProcess, connection *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			connection.writeErr = testErr
		}},
		{name: "request half-close", configure: func(_ *testWindowsProcess, connection *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			connection.closeWriteErr = testErr
		}},
		{name: "response read", configure: func(_ *testWindowsProcess, connection *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			connection.response = errorWindowsResponseReader{err: testErr}
		}},
		{name: "wait", configure: func(process *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			process.waitErr = testErr
		}},
		{name: "unknown exit", configure: func(process *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			process.waitCode = 73
		}},
		{name: "process close", configure: func(process *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, _ *windowsHelperInspection) {
			process.closeErr = testErr
		}},
		{name: "inspection close", configure: func(_ *testWindowsProcess, _ *testWindowsConnection, _ *testWindowsListener, inspection *windowsHelperInspection) {
			inspection.close = func() error { return testErr }
		}},
		{name: "listener close", configure: func(_ *testWindowsProcess, _ *testWindowsConnection, listener *testWindowsListener, _ *windowsHelperInspection) {
			listener.closeErr = testErr
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate, finish := testWindowsLifecycle()
			process := &testWindowsProcess{pid: 77, waitCode: ExitCodeSucceeded, waitGate: gate}
			connection := &testWindowsConnection{response: strings.NewReader("response"), pid: process.pid, finish: finish}
			listener := &testWindowsListener{name: "pipe", connection: connection}
			inspection := windowsHelperInspection{metadata: validWindowsTestMetadata(), close: func() error { return nil }}
			if test.configure != nil {
				test.configure(process, connection, listener, &inspection)
			}
			transport := newWindowsNativeTransport(
				windowsTestHelperExecutable,
				func(string) (windowsHelperInspection, error) { return inspection, nil },
				func() (windowsPipeListener, error) { return listener, nil },
				func(string, string) (windowsLaunchOutcome, error) {
					return windowsLaunchOutcome{process: process, created: true, declined: test.launchDeclined}, test.launchErr
				},
			)

			result := transport.Invoke(t.Context(), strings.NewReader("request"), io.Discard)
			if result.State != TransportIndeterminate || process.waits.Load() != 1 || process.closes.Load() != 1 {
				t.Fatalf("result = %#v, waits = %d, closes = %d", result, process.waits.Load(), process.closes.Load())
			}
		})
	}
}

// TestWindowsNativeTransportCancellationClosesPendingAcceptAndWaits verifies cancellation cannot abandon a child handle.
func TestWindowsNativeTransportCancellationClosesPendingAcceptAndWaits(t *testing.T) {
	gate, finish := testWindowsLifecycle()
	process := &testWindowsProcess{pid: 99, waitCode: ExitCodeHelperFailed, waitGate: gate}
	listener := &testWindowsListener{
		name:       "pipe",
		acceptErr:  errors.New("listener closed"),
		acceptGate: make(chan struct{}),
		finish:     finish,
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := newWindowsNativeTransport(
		windowsTestHelperExecutable,
		func(string) (windowsHelperInspection, error) {
			return windowsHelperInspection{metadata: validWindowsTestMetadata(), close: func() error { return nil }}, nil
		},
		func() (windowsPipeListener, error) { return listener, nil },
		func(string, string) (windowsLaunchOutcome, error) {
			cancel()
			return windowsLaunchOutcome{process: process, created: true}, nil
		},
	)

	result := transport.Invoke(ctx, strings.NewReader("request"), io.Discard)
	if result.State != TransportIndeterminate || process.waits.Load() != 1 || process.closes.Load() != 1 || listener.closes.Load() == 0 {
		t.Fatalf("result = %#v, waits = %d, process closes = %d, listener closes = %d", result, process.waits.Load(), process.closes.Load(), listener.closes.Load())
	}
}

// TestValidInstalledWindowsHelperRequiresEveryIntegrityFact covers the complete pre-consent installation gate.
func TestValidInstalledWindowsHelperRequiresEveryIntegrityFact(t *testing.T) {
	valid := validWindowsTestMetadata()
	tests := []struct {
		name   string
		mutate func(*windowsHelperMetadata)
	}{
		{name: "not regular", mutate: func(value *windowsHelperMetadata) { value.regular = false }},
		{name: "reparse", mutate: func(value *windowsHelperMetadata) { value.reparse = true }},
		{name: "hard link", mutate: func(value *windowsHelperMetadata) { value.linkCount = 2 }},
		{name: "redirected", mutate: func(value *windowsHelperMetadata) { value.finalPathMatches = false }},
		{name: "owner", mutate: func(value *windowsHelperMetadata) { value.ownerTrusted = false }},
		{name: "inherited DACL", mutate: func(value *windowsHelperMetadata) { value.daclProtected = false }},
		{name: "broad DACL", mutate: func(value *windowsHelperMetadata) { value.daclRestricted = false }},
		{name: "signature", mutate: func(value *windowsHelperMetadata) { value.signatureTrusted = false }},
	}
	if !validInstalledWindowsHelper(valid) {
		t.Fatal("valid Windows helper metadata was rejected")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if validInstalledWindowsHelper(candidate) {
				t.Fatalf("metadata %#v was accepted", candidate)
			}
		})
	}
}

// TestNewWindowsNativeTransportRequiresDependencies verifies malformed native assembly fails before consent.
func TestNewWindowsNativeTransportRequiresDependencies(t *testing.T) {
	inspect := func(string) (windowsHelperInspection, error) { return windowsHelperInspection{}, nil }
	pipe := func() (windowsPipeListener, error) { return nil, nil }
	launch := func(string, string) (windowsLaunchOutcome, error) { return windowsLaunchOutcome{}, nil }
	tests := []func(){
		func() { newWindowsNativeTransport("", inspect, pipe, launch) },
		func() { newWindowsNativeTransport(windowsTestHelperExecutable, nil, pipe, launch) },
		func() { newWindowsNativeTransport(windowsTestHelperExecutable, inspect, nil, launch) },
		func() { newWindowsNativeTransport(windowsTestHelperExecutable, inspect, pipe, nil) },
	}
	for index, build := range tests {
		t.Run(string(rune('A'+index)), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("newWindowsNativeTransport() did not panic")
				}
			}()
			build()
		})
	}
}

var _ windowsElevatedProcess = (*testWindowsProcess)(nil)
var _ windowsPipeConnection = (*testWindowsConnection)(nil)
var _ windowsPipeListener = (*testWindowsListener)(nil)
var _ io.Reader = errorWindowsResponseReader{}
