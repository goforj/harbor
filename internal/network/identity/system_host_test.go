package identity

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// systemHostTestAddress supplies controlled interface text without depending on the test runner's host configuration.
type systemHostTestAddress string

// Network identifies the fake as an IP address source for the standard library interface.
func (systemHostTestAddress) Network() string {
	return "ip"
}

// String returns the exact prefix text under test.
func (a systemHostTestAddress) String() string {
	return string(a)
}

// systemHostTestListener records closure while presenting one controlled bound address.
type systemHostTestListener struct {
	address  net.Addr
	closeErr error
	closes   *int
}

// Accept is unavailable because bind probes must close listeners before any connection can be accepted.
func (*systemHostTestListener) Accept() (net.Conn, error) {
	return nil, errors.New("test listener does not accept connections")
}

// Close records the release guarantee before returning the configured platform result.
func (l *systemHostTestListener) Close() error {
	*l.closes++
	return l.closeErr
}

// Addr returns the controlled socket identity used to verify exact binding.
func (l *systemHostTestListener) Addr() net.Addr {
	return l.address
}

// TestSystemHostObserveCanonicalizesExactInterfaceFacts verifies complete bounded observation without inferred ownership.
func TestSystemHostObserveCanonicalizesExactInterfaceFacts(t *testing.T) {
	pool := mustPool(t,
		"127.77.0.14",
		"127.77.0.11",
		"127.77.0.13",
		"127.77.0.10",
		"127.77.0.12",
	)
	ownership := mustOwnership(t, "installation-a", 9)
	interfaces := []net.Interface{
		{Index: 9, Name: "loopback-z", Flags: net.FlagLoopback},
		{Index: 3, Name: "loopback-a", Flags: net.FlagLoopback},
		{Index: 4, Name: "foreign", Flags: net.FlagUp},
	}
	addresses := map[int][]net.Addr{
		9: {
			systemHostTestAddress("127.77.0.11/32"),
			systemHostTestAddress("not-an-address"),
			systemHostTestAddress("127.77.0.10/32"),
			systemHostTestAddress("192.0.2.1/32"),
		},
		3: {
			systemHostTestAddress("127.77.0.12/24"),
			systemHostTestAddress("127.77.0.10/32"),
			systemHostTestAddress("::1/128"),
		},
		4: {systemHostTestAddress("127.77.0.13/32")},
	}
	host := newSystemHostTestObserver(interfaces, addresses)

	request := ObserveRequest{Pool: pool, Ownership: ownership}
	got, err := host.Observe(context.Background(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if gotAddresses := observedIdentityAddresses(got.Identities); !reflect.DeepEqual(gotAddresses, []string{
		"127.77.0.10",
		"127.77.0.11",
		"127.77.0.12",
		"127.77.0.13",
		"127.77.0.14",
	}) {
		t.Fatalf("Observe() addresses = %v", gotAddresses)
	}
	for index, identity := range got.Identities {
		if identity.Ownership != nil {
			t.Fatalf("Observe() identity %s inferred ownership %#v", identity.Address, identity.Ownership)
		}
		if len(identity.Evidence) > 320 {
			t.Fatalf("Observe() identity %s evidence has %d bytes", identity.Address, len(identity.Evidence))
		}
		if !strings.Contains(identity.Evidence, observationFingerprintPrefix) {
			t.Fatalf("Observe() identity %s evidence = %q", identity.Address, identity.Evidence)
		}
		wantPresent := index < 4
		if identity.Present != wantPresent {
			t.Fatalf("Observe() identity %s present = %t, want %t", identity.Address, identity.Present, wantPresent)
		}
	}
	if !strings.Contains(got.Identities[0].Evidence, "assignments=2;exact_loopback_32=2") {
		t.Fatalf("ambiguous exact evidence = %q", got.Identities[0].Evidence)
	}
	if !strings.Contains(got.Identities[2].Evidence, "assignments=1;exact_loopback_32=0") ||
		!strings.Contains(got.Identities[2].Evidence, "first_prefix_bits=24") {
		t.Fatalf("non-/32 evidence = %q", got.Identities[2].Evidence)
	}
	if !strings.Contains(got.Identities[3].Evidence, "assignments=1;exact_loopback_32=0") ||
		!strings.Contains(got.Identities[3].Evidence, "first_loopback=false") {
		t.Fatalf("non-loopback-interface evidence = %q", got.Identities[3].Evidence)
	}
	if !strings.Contains(got.Identities[4].Evidence, "state=absent") {
		t.Fatalf("absent evidence = %q", got.Identities[4].Evidence)
	}
	if gotConflictAddresses := conflictAddresses(got.Conflicts); !reflect.DeepEqual(gotConflictAddresses, []string{
		"127.77.0.10",
		"127.77.0.11",
		"127.77.0.12",
		"127.77.0.13",
	}) {
		t.Fatalf("Observe() conflicts = %v", gotConflictAddresses)
	}
	for _, conflict := range got.Conflicts {
		if conflict.Kind != ConflictKindAddress || conflict.Port != 0 || conflict.Detail == "" {
			t.Fatalf("Observe() conflict = %#v", conflict)
		}
	}

	reversedInterfaces := slices.Clone(interfaces)
	slices.Reverse(reversedInterfaces)
	reversedAddresses := make(map[int][]net.Addr, len(addresses))
	for index, values := range addresses {
		reversedAddresses[index] = slices.Clone(values)
		slices.Reverse(reversedAddresses[index])
	}
	reordered, err := newSystemHostTestObserver(reversedInterfaces, reversedAddresses).Observe(context.Background(), request)
	if err != nil {
		t.Fatalf("reordered Observe() error = %v", err)
	}
	if !reflect.DeepEqual(reordered, got) {
		t.Fatalf("reordered Observe() = %#v, want %#v", reordered, got)
	}
}

// TestSystemHostObserveValidatesBeforeReadingInterfaces verifies malformed authority cannot trigger host inspection.
func TestSystemHostObserveValidatesBeforeReadingInterfaces(t *testing.T) {
	calls := 0
	host := NewSystemHost()
	host.interfaces = func() ([]net.Interface, error) {
		calls++
		return nil, nil
	}
	_, err := host.Observe(context.Background(), ObserveRequest{})
	if err == nil || !strings.Contains(err.Error(), "prefix is invalid") {
		t.Fatalf("Observe() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Observe() interface calls = %d, want 0", calls)
	}
}

// TestSystemHostObservePreservesEnumerationFailures verifies incomplete host snapshots never become apparent absence.
func TestSystemHostObservePreservesEnumerationFailures(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	request := ObserveRequest{Pool: pool, Ownership: mustOwnership(t, "installation-a", 1)}

	t.Run("interfaces", func(t *testing.T) {
		host := NewSystemHost()
		host.interfaces = func() ([]net.Interface, error) {
			return nil, errors.New("interface failure")
		}
		_, err := host.Observe(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "list network interfaces: interface failure") {
			t.Fatalf("Observe() error = %v", err)
		}
	})

	t.Run("addresses", func(t *testing.T) {
		host := NewSystemHost()
		host.interfaces = func() ([]net.Interface, error) {
			return []net.Interface{{Index: 1, Name: "loopback", Flags: net.FlagLoopback}}, nil
		}
		host.addresses = func(net.Interface) ([]net.Addr, error) {
			return nil, errors.New("address failure")
		}
		_, err := host.Observe(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "list addresses for interface \"loopback\": address failure") {
			t.Fatalf("Observe() error = %v", err)
		}
	})
}

// TestSystemHostObserveHonorsCancellation verifies canceled work does not begin or return a partial snapshot.
func TestSystemHostObserveHonorsCancellation(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	request := ObserveRequest{Pool: pool, Ownership: mustOwnership(t, "installation-a", 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	host := NewSystemHost()
	host.interfaces = func() ([]net.Interface, error) {
		calls++
		return nil, nil
	}
	_, err := host.Observe(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe() error = %v, want context cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("Observe() interface calls = %d, want 0", calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	host.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 1, Name: "first", Flags: net.FlagLoopback},
			{Index: 2, Name: "second", Flags: net.FlagLoopback},
		}, nil
	}
	host.addresses = func(net.Interface) ([]net.Addr, error) {
		calls++
		cancel()
		return []net.Addr{systemHostTestAddress("127.77.0.10/32")}, nil
	}
	_, err = host.Observe(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe() mid-enumeration error = %v, want context cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("Observe() address calls = %d, want 1", calls)
	}
}

// TestSystemHostProbeSortsClosesAndBoundsEvidence verifies exact endpoints are released before canonical results return.
func TestSystemHostProbeSortsClosesAndBoundsEvidence(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	address := mustAddress(t, "127.77.0.10")
	request := ProbeRequest{Pool: pool, Address: address, Ports: []uint16{6379, 3306, 5432}}
	originalPorts := slices.Clone(request.Ports)
	var calls []string
	closes := 0
	host := NewSystemHost()
	host.listen = func(_ context.Context, network string, endpoint string) (net.Listener, error) {
		calls = append(calls, network+" "+endpoint)
		_, portText, err := net.SplitHostPort(endpoint)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error = %v", endpoint, err)
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			t.Fatalf("ParseUint(%q) error = %v", portText, err)
		}
		if port == 3306 {
			return nil, syscall.EADDRINUSE
		}
		return &systemHostTestListener{
			address: &net.TCPAddr{IP: net.ParseIP(address.String()), Port: int(port)},
			closes:  &closes,
		}, nil
	}

	result, err := host.Probe(nil, request)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.Address != address {
		t.Fatalf("Probe() address = %s, want %s", result.Address, address)
	}
	if got, want := calls, []string{
		"tcp4 127.77.0.10:3306",
		"tcp4 127.77.0.10:5432",
		"tcp4 127.77.0.10:6379",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Probe() calls = %v, want %v", got, want)
	}
	if closes != 2 {
		t.Fatalf("Probe() listener closes = %d, want 2", closes)
	}
	if !reflect.DeepEqual(request.Ports, originalPorts) {
		t.Fatalf("Probe() mutated request ports: got %v, want %v", request.Ports, originalPorts)
	}
	if got, want := result.Ports[0].Port, uint16(3306); got != want || result.Ports[0].Available {
		t.Fatalf("Probe() unavailable result = %#v", result.Ports[0])
	}
	if !strings.Contains(result.Ports[0].Evidence, "cause=address-in-use") {
		t.Fatalf("Probe() unavailable evidence = %q", result.Ports[0].Evidence)
	}
	for _, probe := range result.Ports {
		if len(probe.Evidence) > 200 || !strings.Contains(probe.Evidence, observationFingerprintPrefix) {
			t.Fatalf("Probe() evidence = %q", probe.Evidence)
		}
	}
	if !result.Ports[1].Available || !result.Ports[2].Available {
		t.Fatalf("Probe() available results = %#v", result.Ports)
	}
}

// TestSystemHostProbeValidatesBeforeBinding verifies invalid socket requests cannot reach platform APIs.
func TestSystemHostProbeValidatesBeforeBinding(t *testing.T) {
	calls := 0
	host := NewSystemHost()
	host.listen = func(context.Context, string, string) (net.Listener, error) {
		calls++
		return nil, errors.New("unexpected listen")
	}
	_, err := host.Probe(context.Background(), ProbeRequest{})
	if err == nil || !strings.Contains(err.Error(), "not IPv4 loopback") {
		t.Fatalf("Probe() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("Probe() listen calls = %d, want 0", calls)
	}
}

// TestSystemHostProbeRejectsMismatchedAndUnclosedListeners verifies availability requires an exact released socket.
func TestSystemHostProbeRejectsMismatchedAndUnclosedListeners(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	request := ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{3306}}

	t.Run("mismatched endpoint", func(t *testing.T) {
		closes := 0
		host := NewSystemHost()
		host.listen = func(context.Context, string, string) (net.Listener, error) {
			return &systemHostTestListener{
				address:  &net.TCPAddr{IP: net.ParseIP("127.77.0.11"), Port: 3306},
				closeErr: errors.New("mismatch close failure"),
				closes:   &closes,
			}, nil
		}
		_, err := host.Probe(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "instead of 127.77.0.10:3306") {
			t.Fatalf("Probe() error = %v", err)
		}
		if !strings.Contains(err.Error(), "close mismatched listener: mismatch close failure") {
			t.Fatalf("Probe() error omitted close failure = %v", err)
		}
		if closes != 1 {
			t.Fatalf("Probe() listener closes = %d, want 1", closes)
		}
	})

	t.Run("close failure", func(t *testing.T) {
		closes := 0
		host := NewSystemHost()
		host.listen = func(context.Context, string, string) (net.Listener, error) {
			return &systemHostTestListener{
				address:  &net.TCPAddr{IP: net.ParseIP("127.77.0.10"), Port: 3306},
				closeErr: errors.New("close failure"),
				closes:   &closes,
			}, nil
		}
		_, err := host.Probe(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "close listener 127.77.0.10:3306: close failure") {
			t.Fatalf("Probe() error = %v", err)
		}
		if closes != 1 {
			t.Fatalf("Probe() listener closes = %d, want 1", closes)
		}
	})
}

// TestSystemHostProbeHonorsCancellation verifies cancellation is not misreported as a foreign listener.
func TestSystemHostProbeHonorsCancellation(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	request := ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{3306}}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	host := NewSystemHost()
	host.listen = func(context.Context, string, string) (net.Listener, error) {
		calls++
		return nil, context.Canceled
	}
	cancel()
	_, err := host.Probe(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() error = %v, want context cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("Probe() listen calls = %d, want 0", calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	host.listen = func(context.Context, string, string) (net.Listener, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}
	_, err = host.Probe(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() in-listen error = %v, want context cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("Probe() listen calls = %d, want 1", calls)
	}

	ctx, cancel = context.WithCancel(context.Background())
	closes := 0
	calls = 0
	host.listen = func(_ context.Context, _ string, endpoint string) (net.Listener, error) {
		calls++
		_, portText, splitErr := net.SplitHostPort(endpoint)
		if splitErr != nil {
			t.Fatalf("SplitHostPort(%q) error = %v", endpoint, splitErr)
		}
		port, parseErr := strconv.Atoi(portText)
		if parseErr != nil {
			t.Fatalf("Atoi(%q) error = %v", portText, parseErr)
		}
		cancel()
		return &systemHostTestListener{
			address: &net.TCPAddr{IP: net.ParseIP("127.77.0.10"), Port: port},
			closes:  &closes,
		}, nil
	}
	request.Ports = []uint16{3306, 6379}
	_, err = host.Probe(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() between-ports error = %v, want context cancellation", err)
	}
	if calls != 1 || closes != 1 {
		t.Fatalf("Probe() between-ports calls = %d, closes = %d, want 1 and 1", calls, closes)
	}
}

// TestSystemHostProbeEvidenceDoesNotEchoPlatformErrors verifies arbitrary OS text cannot expand public evidence.
func TestSystemHostProbeEvidenceDoesNotEchoPlatformErrors(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	request := ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{3306}}
	secret := strings.Repeat("platform detail ", 1_000)
	host := NewSystemHost()
	host.listen = func(context.Context, string, string) (net.Listener, error) {
		return nil, errors.New(secret)
	}
	result, err := host.Probe(context.Background(), request)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.Ports[0].Available || len(result.Ports[0].Evidence) > 200 {
		t.Fatalf("Probe() result = %#v", result.Ports[0])
	}
	if strings.Contains(result.Ports[0].Evidence, "platform detail") {
		t.Fatalf("Probe() evidence echoed platform error = %q", result.Ports[0].Evidence)
	}
}

// TestBindFailureClassCoversPortableSocketCategories verifies platform errors remain actionable without exposing raw text.
func TestBindFailureClassCoversPortableSocketCategories(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "in use", err: syscall.EADDRINUSE, want: "address-in-use"},
		{name: "not assigned", err: syscall.EADDRNOTAVAIL, want: "address-not-available"},
		{name: "permission", err: os.ErrPermission, want: "permission-denied"},
		{name: "other", err: errors.New("other"), want: "bind-failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := bindFailureClass(test.err); got != test.want {
				t.Fatalf("bindFailureClass() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestCompareInterfaceAssignmentsCoversCanonicalTies verifies every fact participates in deterministic ordering.
func TestCompareInterfaceAssignmentsCoversCanonicalTies(t *testing.T) {
	base := interfaceAssignment{
		address:        mustAddress(t, "127.77.0.10"),
		prefixBits:     32,
		interfaceIndex: 4,
		interfaceName:  "loopback-b",
		loopback:       true,
	}
	tests := []struct {
		name  string
		left  interfaceAssignment
		right interfaceAssignment
		want  int
	}{
		{name: "equal", left: base, right: base, want: 0},
		{name: "address", left: base, right: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.address = mustAddress(t, "127.77.0.11") }), want: -1},
		{name: "name", left: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.interfaceName = "loopback-a" }), right: base, want: -1},
		{name: "index", left: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.interfaceIndex = 3 }), right: base, want: -1},
		{name: "prefix", left: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.prefixBits = 24 }), right: base, want: -8},
		{name: "loopback false", left: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.loopback = false }), right: base, want: -1},
		{name: "loopback true", left: base, right: mutateInterfaceAssignment(base, func(value *interfaceAssignment) { value.loopback = false }), want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := compareInterfaceAssignments(test.left, test.right); got != test.want {
				t.Fatalf("compareInterfaceAssignments() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestSystemHostProbeReleasesRealListener verifies the production adapter does not retain a successful bind.
func TestSystemHostProbeReleasesRealListener(t *testing.T) {
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	endpoint := reserved.Addr().(*net.TCPAddr).AddrPort()
	if err := reserved.Close(); err != nil {
		t.Fatalf("release test port: %v", err)
	}
	pool, err := NewPool(netip.MustParsePrefix("127.0.0.1/32"), []netip.Addr{endpoint.Addr()})
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	result, err := NewSystemHost().Probe(context.Background(), ProbeRequest{
		Pool: pool, Address: endpoint.Addr(), Ports: []uint16{endpoint.Port()},
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if !result.Ports[0].Available {
		t.Fatalf("Probe() result = %#v", result.Ports[0])
	}
	rebound, err := net.Listen("tcp4", endpoint.String())
	if err != nil {
		t.Fatalf("listen after Probe(): %v", err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatalf("close rebound listener: %v", err)
	}
}

// TestSystemHostObserveReadsRealInterfaces verifies the production observer can inspect the runner without mutation.
func TestSystemHostObserveReadsRealInterfaces(t *testing.T) {
	address := netip.MustParseAddr("127.0.0.1")
	pool, err := NewPool(netip.MustParsePrefix("127.0.0.1/32"), []netip.Addr{address})
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	result, err := NewSystemHost().Observe(context.Background(), ObserveRequest{
		Pool: pool, Ownership: mustOwnership(t, "installation-a", 1),
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if len(result.Identities) != 1 || result.Identities[0].Address != address || result.Identities[0].Evidence == "" {
		t.Fatalf("Observe() result = %#v", result)
	}
}

// newSystemHostTestObserver creates an observer whose platform enumeration can be reordered independently.
func newSystemHostTestObserver(interfaces []net.Interface, addresses map[int][]net.Addr) *SystemHost {
	host := NewSystemHost()
	host.interfaces = func() ([]net.Interface, error) {
		return slices.Clone(interfaces), nil
	}
	host.addresses = func(networkInterface net.Interface) ([]net.Addr, error) {
		return slices.Clone(addresses[networkInterface.Index]), nil
	}
	return host
}

// observedIdentityAddresses renders canonical identity ordering for concise assertions.
func observedIdentityAddresses(identities []ObservedIdentity) []string {
	result := make([]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, identity.Address.String())
	}
	return result
}

// conflictAddresses renders canonical conflict ordering for concise assertions.
func conflictAddresses(conflicts []Conflict) []string {
	result := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		result = append(result, conflict.Address.String())
	}
	return result
}

// mutateInterfaceAssignment creates one changed value without obscuring the field responsible for an ordering case.
func mutateInterfaceAssignment(value interfaceAssignment, mutate func(*interfaceAssignment)) interfaceAssignment {
	mutate(&value)
	return value
}
