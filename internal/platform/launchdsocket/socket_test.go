package launchdsocket

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/network/ingressrelay"
)

// trackedListener records capability cleanup without opening another operating-system socket.
type trackedListener struct {
	address  net.Addr
	closes   int
	closeErr error
}

// Accept is unused because activation tests stop before the paired relay owns the listener.
func (listener *trackedListener) Accept() (net.Conn, error) {
	return nil, errors.New("tracked listener does not accept")
}

// Close records each release and returns the configured cleanup outcome.
func (listener *trackedListener) Close() error {
	listener.closes++
	return listener.closeErr
}

// Addr returns the native address copied from the validated descriptor observation.
func (listener *trackedListener) Addr() net.Addr {
	return listener.address
}

// activationHarness provides two live file descriptors and complete injectable native behavior.
type activationHarness struct {
	http          []*os.File
	https         []*os.File
	peers         []*os.File
	facts         map[uintptr]socketObservation
	inspectErrors map[uintptr]error
	convertErrors map[uintptr]error
	closeErrors   map[uintptr]error
	nilListeners  map[uintptr]bool
	listeners     map[uintptr]*trackedListener
	activateError map[string]error
	activations   []string
	inspections   []uintptr
	conversions   []uintptr
}

// newActivationHarness creates one descriptor for each fixed launchd socket name.
func newActivationHarness(t *testing.T) *activationHarness {
	t.Helper()
	harness := &activationHarness{
		facts:         make(map[uintptr]socketObservation),
		inspectErrors: make(map[uintptr]error),
		convertErrors: make(map[uintptr]error),
		closeErrors:   make(map[uintptr]error),
		nilListeners:  make(map[uintptr]bool),
		listeners:     make(map[uintptr]*trackedListener),
		activateError: make(map[string]error),
	}
	http := harness.newFile(t, validObservation(httpEndpoint))
	https := harness.newFile(t, validObservation(httpsEndpoint))
	harness.http = []*os.File{http}
	harness.https = []*os.File{https}
	return harness
}

// newFile creates a harmless pipe descriptor whose native facts remain fully controlled by the test.
func (harness *activationHarness) newFile(t *testing.T, observation socketObservation) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	harness.peers = append(harness.peers, writer)
	harness.facts[reader.Fd()] = observation
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader
}

// dependencies projects the scripted native behavior through the production portable core.
func (harness *activationHarness) dependencies() activationDependencies {
	return activationDependencies{
		activate: harness.activate,
		inspect:  harness.inspect,
		convert:  harness.convert,
		close:    harness.close,
	}
}

// activate returns only the two fixed descriptor collections understood by production.
func (harness *activationHarness) activate(name string) ([]*os.File, error) {
	harness.activations = append(harness.activations, name)
	switch name {
	case httpSocketName:
		return harness.http, harness.activateError[name]
	case httpsSocketName:
		return harness.https, harness.activateError[name]
	default:
		return nil, errors.New("unexpected launchd socket name")
	}
}

// inspect returns the bounded fact set selected for one descriptor.
func (harness *activationHarness) inspect(file *os.File) (socketObservation, error) {
	descriptor := file.Fd()
	harness.inspections = append(harness.inspections, descriptor)
	if err := harness.inspectErrors[descriptor]; err != nil {
		return socketObservation{}, err
	}
	return harness.facts[descriptor], nil
}

// convert returns an independently tracked listener or the scripted conversion failure.
func (harness *activationHarness) convert(file *os.File) (net.Listener, error) {
	descriptor := file.Fd()
	harness.conversions = append(harness.conversions, descriptor)
	if err := harness.convertErrors[descriptor]; err != nil {
		return nil, err
	}
	if harness.nilListeners[descriptor] {
		return nil, nil
	}
	listener := &trackedListener{address: net.TCPAddrFromAddrPort(harness.facts[descriptor].Local)}
	harness.listeners[descriptor] = listener
	return listener, nil
}

// close releases the real descriptor before returning any scripted cleanup failure.
func (harness *activationHarness) close(file *os.File) error {
	descriptor := file.Fd()
	return errors.Join(file.Close(), harness.closeErrors[descriptor])
}

// validObservation returns the exact fact shape accepted for one fixed endpoint.
func validObservation(endpoint netip.AddrPort) socketObservation {
	return socketObservation{
		IPv4:  true,
		TCP:   true,
		Local: endpoint,
	}
}

// requireClosed proves an original activated descriptor can no longer be used after the call.
func requireClosed(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		return
	}
	if descriptor := file.Fd(); descriptor != ^uintptr(0) {
		t.Fatalf("activated descriptor remains open: Fd() = %#x", descriptor)
	}
}

// requireHarnessFilesClosed proves every descriptor returned by the selected activations was released.
func requireHarnessFilesClosed(t *testing.T, harness *activationHarness) {
	t.Helper()
	for _, file := range append(append([]*os.File{}, harness.http...), harness.https...) {
		requireClosed(t, file)
	}
}

// TestActivateIngressConvertsOnlyTheFixedExactPair verifies names, inspection order, and descriptor transfer.
func TestActivateIngressConvertsOnlyTheFixedExactPair(t *testing.T) {
	harness := newActivationHarness(t)
	httpDescriptor := harness.http[0].Fd()
	httpsDescriptor := harness.https[0].Fd()

	listeners, err := activateIngress(harness.dependencies())
	if err != nil {
		t.Fatalf("activateIngress() error = %v", err)
	}
	if got, want := harness.activations, []string{httpSocketName, httpsSocketName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("activation names = %v, want %v", got, want)
	}
	if got, want := harness.inspections, []uintptr{httpDescriptor, httpsDescriptor}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inspected descriptors = %v, want %v", got, want)
	}
	if got, want := harness.conversions, []uintptr{httpDescriptor, httpsDescriptor}; !reflect.DeepEqual(got, want) {
		t.Fatalf("converted descriptors = %v, want %v", got, want)
	}
	if listeners.HTTP != harness.listeners[httpDescriptor] || listeners.HTTPS != harness.listeners[httpsDescriptor] {
		t.Fatalf("listeners = %#v, want exact converted listener pair", listeners)
	}
	requireHarnessFilesClosed(t, harness)
	if err := closeIngressListeners(listeners); err != nil {
		t.Fatalf("closeIngressListeners() error = %v", err)
	}
}

// TestActivateIngressRejectsActivationFailuresAndCardinality verifies no later capability is acquired after an invalid step.
func TestActivateIngressRejectsActivationFailuresAndCardinality(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *activationHarness)
		want        string
		activations []string
	}{
		{
			name: "HTTP activation failure",
			mutate: func(_ *testing.T, harness *activationHarness) {
				harness.activateError[httpSocketName] = errors.New("HTTP unavailable")
			},
			want:        "HTTP unavailable",
			activations: []string{httpSocketName},
		},
		{
			name: "missing HTTP descriptor",
			mutate: func(_ *testing.T, harness *activationHarness) {
				harness.http = []*os.File{}
			},
			want:        "received 0 descriptors",
			activations: []string{httpSocketName},
		},
		{
			name: "multiple HTTP descriptors",
			mutate: func(t *testing.T, harness *activationHarness) {
				harness.http = append(harness.http, harness.newFile(t, validObservation(httpEndpoint)))
			},
			want:        "received 2 descriptors",
			activations: []string{httpSocketName},
		},
		{
			name: "HTTPS activation failure",
			mutate: func(_ *testing.T, harness *activationHarness) {
				harness.activateError[httpsSocketName] = errors.New("HTTPS unavailable")
			},
			want:        "HTTPS unavailable",
			activations: []string{httpSocketName, httpsSocketName},
		},
		{
			name: "missing HTTPS descriptor",
			mutate: func(_ *testing.T, harness *activationHarness) {
				harness.https = []*os.File{}
			},
			want:        "received 0 descriptors",
			activations: []string{httpSocketName, httpsSocketName},
		},
		{
			name: "multiple HTTPS descriptors",
			mutate: func(t *testing.T, harness *activationHarness) {
				harness.https = append(harness.https, harness.newFile(t, validObservation(httpsEndpoint)))
			},
			want:        "received 2 descriptors",
			activations: []string{httpSocketName, httpsSocketName},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newActivationHarness(t)
			test.mutate(t, harness)
			_, err := activateIngress(harness.dependencies())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("activateIngress() error = %v, want substring %q", err, test.want)
			}
			if !reflect.DeepEqual(harness.activations, test.activations) {
				t.Fatalf("activation names = %v, want %v", harness.activations, test.activations)
			}
			for _, file := range append(append([]*os.File{}, harness.http...), harness.https...) {
				if file != nil && containsFile(test.activations, harness, file) {
					requireClosed(t, file)
				}
			}
			if len(harness.conversions) != 0 {
				t.Fatalf("conversion reached invalid activation: %v", harness.conversions)
			}
		})
	}
}

// containsFile reports whether the test's activation sequence transferred ownership of one file.
func containsFile(activations []string, harness *activationHarness, target *os.File) bool {
	for _, name := range activations {
		var files []*os.File
		if name == httpSocketName {
			files = harness.http
		} else {
			files = harness.https
		}
		for _, file := range files {
			if file == target {
				return true
			}
		}
	}
	return false
}

// TestActivateIngressRejectsInvalidDescriptorIdentity verifies nil, closed, and duplicated descriptors never reach inspection.
func TestActivateIngressRejectsInvalidDescriptorIdentity(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		harness := newActivationHarness(t)
		harness.http = []*os.File{nil}
		_, err := activateIngress(harness.dependencies())
		if err == nil || !strings.Contains(err.Error(), "descriptor file is nil") {
			t.Fatalf("activateIngress() error = %v", err)
		}
		requireClosed(t, harness.https[0])
	})

	t.Run("closed", func(t *testing.T) {
		harness := newActivationHarness(t)
		if err := harness.http[0].Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		_, err := activateIngress(harness.dependencies())
		if err == nil || !strings.Contains(err.Error(), "descriptor is closed") {
			t.Fatalf("activateIngress() error = %v", err)
		}
		requireClosed(t, harness.https[0])
	})

	t.Run("duplicate", func(t *testing.T) {
		harness := newActivationHarness(t)
		duplicate := os.NewFile(harness.http[0].Fd(), "duplicate-launchd-descriptor")
		if duplicate == nil {
			t.Fatal("os.NewFile() returned nil")
		}
		harness.https = []*os.File{duplicate}
		_, err := activateIngress(harness.dependencies())
		if err == nil || !strings.Contains(err.Error(), "duplicate descriptor") {
			t.Fatalf("activateIngress() error = %v", err)
		}
		requireClosed(t, harness.http[0])
		requireClosed(t, duplicate)
		if len(harness.inspections) != 0 || len(harness.conversions) != 0 {
			t.Fatalf("duplicate descriptor reached native operations: inspections %v conversions %v", harness.inspections, harness.conversions)
		}
	})
}

// TestActivateIngressRejectsUnsafeSocketFacts verifies every native protocol and bind invariant independently.
func TestActivateIngressRejectsUnsafeSocketFacts(t *testing.T) {
	tests := []struct {
		name   string
		https  bool
		mutate func(*socketObservation)
		want   string
	}{
		{name: "not IPv4", mutate: func(value *socketObservation) { value.IPv4 = false }, want: "not an IPv4"},
		{name: "not TCP", mutate: func(value *socketObservation) { value.TCP = false }, want: "not a TCP"},
		{name: "wrong HTTP address", mutate: func(value *socketObservation) { value.Local = netip.MustParseAddrPort("127.0.0.2:80") }, want: "want exactly 127.0.0.1:80"},
		{name: "wrong HTTPS port", https: true, mutate: func(value *socketObservation) { value.Local = netip.MustParseAddrPort("127.0.0.1:444") }, want: "want exactly 127.0.0.1:443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newActivationHarness(t)
			file := harness.http[0]
			if test.https {
				file = harness.https[0]
			}
			observation := harness.facts[file.Fd()]
			test.mutate(&observation)
			harness.facts[file.Fd()] = observation

			_, err := activateIngress(harness.dependencies())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("activateIngress() error = %v, want substring %q", err, test.want)
			}
			requireHarnessFilesClosed(t, harness)
			if len(harness.conversions) != 0 {
				t.Fatalf("conversion reached unsafe socket facts: %v", harness.conversions)
			}
		})
	}
}

// TestActivateIngressClosesEverythingAfterInspectionOrConversionFailure verifies every partial output is retired.
func TestActivateIngressClosesEverythingAfterInspectionOrConversionFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*activationHarness)
		want   string
	}{
		{
			name: "HTTP inspection",
			mutate: func(harness *activationHarness) {
				harness.inspectErrors[harness.http[0].Fd()] = errors.New("inspect HTTP")
			},
			want: "inspect HTTP",
		},
		{
			name: "HTTPS inspection",
			mutate: func(harness *activationHarness) {
				harness.inspectErrors[harness.https[0].Fd()] = errors.New("inspect HTTPS")
			},
			want: "inspect HTTPS",
		},
		{
			name: "HTTP conversion",
			mutate: func(harness *activationHarness) {
				harness.convertErrors[harness.http[0].Fd()] = errors.New("convert HTTP")
			},
			want: "convert HTTP",
		},
		{
			name: "HTTPS conversion",
			mutate: func(harness *activationHarness) {
				harness.convertErrors[harness.https[0].Fd()] = errors.New("convert HTTPS")
			},
			want: "convert HTTPS",
		},
		{
			name: "nil HTTP listener",
			mutate: func(harness *activationHarness) {
				harness.nilListeners[harness.http[0].Fd()] = true
			},
			want: "listener conversion returned nil",
		},
		{
			name: "original HTTP close",
			mutate: func(harness *activationHarness) {
				harness.closeErrors[harness.http[0].Fd()] = errors.New("close HTTP original")
			},
			want: "close HTTP original",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newActivationHarness(t)
			httpDescriptor := harness.http[0].Fd()
			test.mutate(harness)
			_, err := activateIngress(harness.dependencies())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("activateIngress() error = %v, want substring %q", err, test.want)
			}
			requireHarnessFilesClosed(t, harness)
			if listener := harness.listeners[httpDescriptor]; listener != nil && listener.closes != 1 {
				t.Fatalf("converted HTTP listener close calls = %d, want 1", listener.closes)
			}
		})
	}
}

// TestActivateIngressPreservesCleanupFailures verifies native effect ambiguity is not hidden by the primary failure.
func TestActivateIngressPreservesCleanupFailures(t *testing.T) {
	harness := newActivationHarness(t)
	harness.activateError[httpSocketName] = errors.New("activation failed")
	harness.closeErrors[harness.http[0].Fd()] = errors.New("cleanup failed")
	_, err := activateIngress(harness.dependencies())
	if err == nil || !strings.Contains(err.Error(), "activation failed") || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("activateIngress() error = %v, want activation and cleanup failures", err)
	}
	requireClosed(t, harness.http[0])
}

// TestCloseIngressListenersRetainsBothFailures verifies paired converted-listener cleanup is complete.
func TestCloseIngressListenersRetainsBothFailures(t *testing.T) {
	httpFailure := errors.New("HTTP close failed")
	httpsFailure := errors.New("HTTPS close failed")
	http := &trackedListener{closeErr: httpFailure}
	https := &trackedListener{closeErr: httpsFailure}
	err := closeIngressListeners(ingressrelay.Listeners{HTTP: http, HTTPS: https})
	if !errors.Is(err, httpFailure) || !errors.Is(err, httpsFailure) || http.closes != 1 || https.closes != 1 {
		t.Fatalf("closeIngressListeners() = %v, closes HTTP %d HTTPS %d", err, http.closes, https.closes)
	}
}
