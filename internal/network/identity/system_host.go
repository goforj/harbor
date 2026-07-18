package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const observationFingerprintPrefix = "sha256="

// SystemHost reads the operating system's network state without retaining sockets or changing host configuration.
type SystemHost struct {
	interfaces func() ([]net.Interface, error)
	addresses  func(net.Interface) ([]net.Addr, error)
	listen     func(context.Context, string, string) (net.Listener, error)
}

// NewSystemHost creates the production read-only adapter backed by Go's platform network APIs.
func NewSystemHost() *SystemHost {
	return &SystemHost{
		interfaces: net.Interfaces,
		addresses: func(networkInterface net.Interface) ([]net.Addr, error) {
			return networkInterface.Addrs()
		},
		listen: func(ctx context.Context, network string, address string) (net.Listener, error) {
			listenConfig := net.ListenConfig{}
			return listenConfig.Listen(ctx, network, address)
		},
	}
}

// Observe reports explicit interface assignments for every candidate without inferring Harbor ownership from address presence.
func (h *SystemHost) Observe(ctx context.Context, request ObserveRequest) (Observation, error) {
	if err := request.Validate(); err != nil {
		return Observation{}, fmt.Errorf("observe system identities: %w", err)
	}
	ctx = normalizeHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, fmt.Errorf("observe system identities: %w", err)
	}

	assignments, err := h.interfaceAssignments(ctx)
	if err != nil {
		return Observation{}, err
	}

	observation := Observation{
		Identities: make([]ObservedIdentity, 0, request.Pool.Capacity()),
		Conflicts:  make([]Conflict, 0, request.Pool.Capacity()),
	}
	for _, candidate := range request.Pool.Candidates() {
		matches := matchingAssignments(candidate, assignments)
		present := len(matches) > 0
		observation.Identities = append(observation.Identities, ObservedIdentity{
			Address:  candidate,
			Present:  present,
			Evidence: formatAssignmentEvidence(candidate, matches),
		})
		if present {
			observation.Conflicts = append(observation.Conflicts, Conflict{
				Address: candidate,
				Kind:    ConflictKindAddress,
				Detail:  "candidate is explicitly assigned without protected Harbor ownership evidence",
			})
		}
	}
	return observation, nil
}

// Probe proves whether every requested TCP socket can be bound exactly, closing each listener before returning.
func (h *SystemHost) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if err := request.Validate(); err != nil {
		return ProbeResult{}, fmt.Errorf("probe system identity: %w", err)
	}
	ctx = normalizeHostContext(ctx)
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, fmt.Errorf("probe system identity: %w", err)
	}

	address := request.Address.Unmap()
	ports := slices.Clone(request.Ports)
	slices.Sort(ports)
	result := ProbeResult{Address: address, Ports: make([]PortProbe, 0, len(ports))}
	for _, port := range ports {
		if err := ctx.Err(); err != nil {
			return ProbeResult{}, fmt.Errorf("probe system identity: %w", err)
		}
		probe, err := h.probePort(ctx, address, port)
		if err != nil {
			return ProbeResult{}, err
		}
		result.Ports = append(result.Ports, probe)
	}
	return result, nil
}

// interfaceAssignment preserves the exact interface fact needed to distinguish an explicit /32 from loopback routability.
type interfaceAssignment struct {
	address        netip.Addr
	prefixBits     int
	interfaceIndex int
	interfaceName  string
	loopback       bool
}

// interfaceAssignments reads a complete interface snapshot because a partial snapshot could authorize an unsafe ensure.
func (h *SystemHost) interfaceAssignments(ctx context.Context) ([]interfaceAssignment, error) {
	interfaces, err := h.interfaces()
	if err != nil {
		return nil, fmt.Errorf("observe system identities: list network interfaces: %w", err)
	}

	assignments := make([]interfaceAssignment, 0)
	for _, networkInterface := range interfaces {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("observe system identities: %w", err)
		}
		addresses, err := h.addresses(networkInterface)
		if err != nil {
			return nil, fmt.Errorf("observe system identities: list addresses for interface %q: %w", networkInterface.Name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				continue
			}
			parsedAddress := prefix.Addr().Unmap()
			if !parsedAddress.IsValid() || !parsedAddress.Is4() || !parsedAddress.IsLoopback() {
				continue
			}
			assignments = append(assignments, interfaceAssignment{
				address:        parsedAddress,
				prefixBits:     prefix.Bits(),
				interfaceIndex: networkInterface.Index,
				interfaceName:  networkInterface.Name,
				loopback:       networkInterface.Flags&net.FlagLoopback != 0,
			})
		}
	}
	slices.SortFunc(assignments, compareInterfaceAssignments)
	return assignments, nil
}

// matchingAssignments returns all explicit facts for one candidate so malformed or ambiguous presence is never reported as absence.
func matchingAssignments(candidate netip.Addr, assignments []interfaceAssignment) []interfaceAssignment {
	return slices.DeleteFunc(slices.Clone(assignments), func(assignment interfaceAssignment) bool {
		return assignment.address != candidate
	})
}

// compareInterfaceAssignments gives evidence a canonical order independent of operating-system enumeration order.
func compareInterfaceAssignments(left interfaceAssignment, right interfaceAssignment) int {
	if compared := left.address.Compare(right.address); compared != 0 {
		return compared
	}
	if left.interfaceName != right.interfaceName {
		return strings.Compare(left.interfaceName, right.interfaceName)
	}
	if left.interfaceIndex != right.interfaceIndex {
		return left.interfaceIndex - right.interfaceIndex
	}
	if left.prefixBits != right.prefixBits {
		return left.prefixBits - right.prefixBits
	}
	return boolCompare(left.loopback, right.loopback)
}

// boolCompare keeps false-before-true ordering explicit without converting booleans to unstable text.
func boolCompare(left bool, right bool) int {
	if left == right {
		return 0
	}
	if !left {
		return -1
	}
	return 1
}

// formatAssignmentEvidence combines bounded diagnostic metadata with a fingerprint over every canonical interface fact.
func formatAssignmentEvidence(address netip.Addr, assignments []interfaceAssignment) string {
	exactLoopback := 0
	for _, assignment := range assignments {
		if assignment.prefixBits == 32 && assignment.loopback {
			exactLoopback++
		}
	}
	state := "absent"
	if len(assignments) > 0 {
		state = "present"
	}
	fingerprint := assignmentFingerprint(address, assignments)
	evidence := fmt.Sprintf(
		"state=%s;address=%s;assignments=%d;exact_loopback_32=%d;%s%s",
		state,
		address,
		len(assignments),
		exactLoopback,
		observationFingerprintPrefix,
		fingerprint,
	)
	if len(assignments) == 0 {
		return evidence
	}
	first := assignments[0]
	return fmt.Sprintf(
		"%s;first_interface_index=%d;first_interface_name_sha256=%s;first_prefix_bits=%d;first_loopback=%t",
		evidence,
		first.interfaceIndex,
		stringFingerprint(first.interfaceName),
		first.prefixBits,
		first.loopback,
	)
}

// assignmentFingerprint commits evidence to every match without allowing interface names to expand the returned diagnostic.
func assignmentFingerprint(address netip.Addr, assignments []interfaceAssignment) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "address=%s\n", address)
	for _, assignment := range assignments {
		_, _ = fmt.Fprintf(
			hash,
			"interface=%s\x00index=%d\x00prefix=%d\x00loopback=%t\n",
			assignment.interfaceName,
			assignment.interfaceIndex,
			assignment.prefixBits,
			assignment.loopback,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// stringFingerprint makes unbounded or unprintable platform text useful without echoing it into evidence.
func stringFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// probePort opens only the requested TCP4 endpoint and closes it before reporting availability.
func (h *SystemHost) probePort(ctx context.Context, address netip.Addr, port uint16) (PortProbe, error) {
	endpoint := net.JoinHostPort(address.String(), strconv.Itoa(int(port)))
	listener, err := h.listen(ctx, "tcp4", endpoint)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return PortProbe{}, fmt.Errorf("probe system identity: bind %s: %w", endpoint, contextErr)
		}
		cause := bindFailureClass(err)
		return PortProbe{
			Port:      port,
			Available: false,
			Evidence:  formatProbeEvidence(address, port, false, cause, err),
		}, nil
	}

	want := netip.AddrPortFrom(address, port)
	bound, valid := listener.Addr().(*net.TCPAddr)
	got := netip.AddrPort{}
	if valid {
		boundAddress := bound.AddrPort()
		got = netip.AddrPortFrom(boundAddress.Addr().Unmap(), boundAddress.Port())
	}
	if !valid || got != want {
		actual := listener.Addr().String()
		closeErr := listener.Close()
		mismatchErr := fmt.Errorf("listener bound %q instead of %s", actual, want)
		if closeErr != nil {
			mismatchErr = errors.Join(mismatchErr, fmt.Errorf("close mismatched listener: %w", closeErr))
		}
		return PortProbe{}, fmt.Errorf("probe system identity: %w", mismatchErr)
	}
	if err := listener.Close(); err != nil {
		return PortProbe{}, fmt.Errorf("probe system identity: close listener %s: %w", endpoint, err)
	}
	return PortProbe{
		Port:      port,
		Available: true,
		Evidence:  formatProbeEvidence(address, port, true, "none", nil),
	}, nil
}

// bindFailureClass exposes an actionable bounded category while the fingerprint retains exact diagnostic identity.
func bindFailureClass(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "address-in-use"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address-not-available"
	case errors.Is(err, os.ErrPermission):
		return "permission-denied"
	default:
		return "bind-failed"
	}
}

// formatProbeEvidence returns fixed-shape evidence even when the operating system supplies an unbounded error message.
func formatProbeEvidence(address netip.Addr, port uint16, available bool, cause string, bindErr error) string {
	state := "unavailable"
	if available {
		state = "available"
	}
	fingerprintInput := fmt.Sprintf("address=%s\nport=%d\nstate=%s\ncause=%s\n", address, port, state, cause)
	if bindErr != nil {
		fingerprintInput += fmt.Sprintf("error_type=%T\nerror=%s\n", bindErr, bindErr)
	}
	return fmt.Sprintf(
		"state=%s;address=%s;port=%d;cause=%s;%s%s",
		state,
		address,
		port,
		cause,
		observationFingerprintPrefix,
		stringFingerprint(fingerprintInput),
	)
}

// normalizeHostContext lets local callers omit a context without weakening request validation or cancellation behavior.
func normalizeHostContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var (
	_ HostObserver = (*SystemHost)(nil)
	_ HostProber   = (*SystemHost)(nil)
)
