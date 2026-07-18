package identity

import (
	"context"
	"fmt"
	"net/netip"
)

// ObserveRequest asks a platform adapter to inspect only the selected candidate pool.
type ObserveRequest struct {
	Pool      Pool
	Ownership Ownership
}

// Validate rejects observation requests that lack a bounded pool or current owner.
func (r ObserveRequest) Validate() error {
	if err := r.Pool.Validate(); err != nil {
		return err
	}
	return r.Ownership.Validate()
}

// ObservedIdentity describes an exact candidate's current host presence and ownership evidence.
type ObservedIdentity struct {
	Address   netip.Addr
	Present   bool
	Ownership *Ownership
	Evidence  string
}

// Observation is the semantic host state consumed by the identity planner.
type Observation struct {
	Identities []ObservedIdentity
	Conflicts  []Conflict
}

// HostObserver reads loopback identity state without changing it.
type HostObserver interface {
	Observe(context.Context, ObserveRequest) (Observation, error)
}

// ProbeRequest asks whether every required port can bind on one exact candidate.
type ProbeRequest struct {
	Pool    Pool
	Address netip.Addr
	Ports   []uint16
}

// Validate rejects probes that could escape IPv4 loopback or omit the required socket identity.
func (r ProbeRequest) Validate() error {
	address := r.Address.Unmap()
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("identity probe: address %s is not IPv4 loopback", r.Address)
	}
	if err := r.Pool.Validate(); err != nil {
		return err
	}
	if !r.Pool.Contains(address) {
		return fmt.Errorf("identity probe: address %s is not a pool candidate", address)
	}
	if len(r.Ports) == 0 {
		return fmt.Errorf("identity probe: at least one port is required")
	}
	seen := make(map[uint16]struct{}, len(r.Ports))
	for _, port := range r.Ports {
		if port == 0 {
			return fmt.Errorf("identity probe: port zero is not a stable socket identity")
		}
		if _, duplicate := seen[port]; duplicate {
			return fmt.Errorf("identity probe: duplicate port %d", port)
		}
		seen[port] = struct{}{}
	}
	return nil
}

// PortProbe reports whether one exact address and port can be claimed safely.
type PortProbe struct {
	Port      uint16
	Available bool
	Evidence  string
}

// ProbeResult contains the bind evidence for every requested native port.
type ProbeResult struct {
	Address netip.Addr
	Ports   []PortProbe
}

// HostProber checks bindability without retaining a listener or mutating address configuration.
type HostProber interface {
	Probe(context.Context, ProbeRequest) (ProbeResult, error)
}

// MutationAction limits platform changes to ensuring or releasing one exact identity.
type MutationAction string

const (
	// MutationActionEnsure makes an owned identity available without affecting foreign state.
	MutationActionEnsure MutationAction = "ensure"
	// MutationActionRelease removes an identity only when its current ownership evidence still matches.
	MutationActionRelease MutationAction = "release"
)

// Mutation asks a platform adapter to apply one ownership-checked identity effect.
type Mutation struct {
	Action MutationAction
	Pool   Pool
	Lease  Lease
}

// Validate rejects unbounded actions and malformed lease ownership before platform code runs.
func (m Mutation) Validate() error {
	switch m.Action {
	case MutationActionEnsure, MutationActionRelease:
	default:
		return fmt.Errorf("identity mutation: action %q is unsupported", m.Action)
	}
	if err := m.Pool.Validate(); err != nil {
		return err
	}
	if err := m.Lease.Validate(); err != nil {
		return err
	}
	if !m.Pool.Contains(m.Lease.Address) {
		return fmt.Errorf("identity mutation: address %s is not a pool candidate", m.Lease.Address)
	}
	return nil
}

// MutationResult records the exact effect and opaque ownership evidence returned by the platform.
type MutationResult struct {
	Action   MutationAction
	Lease    Lease
	Changed  bool
	Evidence string
}

// HostMutator applies one semantic loopback identity effect without accepting commands or paths.
type HostMutator interface {
	Mutate(context.Context, Mutation) (MutationResult, error)
}
