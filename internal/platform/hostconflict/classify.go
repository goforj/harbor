package hostconflict

import "net/netip"

// State is the fail-closed classification of one authority-fact set.
type State string

const (
	// StateSafe means every required fact set is complete and no conflict is present.
	StateSafe State = "safe"
	// StateConflict means at least one raw fact proves the candidate is unsafe.
	StateConflict State = "conflict"
	// StateIndeterminate means bounded observation could not prove the candidate safe.
	StateIndeterminate State = "indeterminate"
)

// Assessment contains the recomputed component and overall classifications.
type Assessment struct {
	State   State
	Routes  State
	Sockets State
	Policy  State
}

// Classify validates raw facts and recomputes their fail-closed assessment.
//
// StateSafe proves only this pre-assignment route, socket, and policy snapshot.
// Callers must separately compose assignment, ownership, resolver, and
// post-assignment retained-bind evidence for complete admission.
func (o Observation) Classify() (Assessment, error) {
	if err := o.Validate(); err != nil {
		return Assessment{}, err
	}
	assessment := Assessment{
		Routes:  classifyRoutes(o),
		Sockets: classifySockets(o),
		Policy:  classifyPolicy(o),
	}
	assessment.State = combineStates(assessment.Routes, assessment.Sockets, assessment.Policy)
	return assessment, nil
}

// classifyRoutes accepts the unassigned candidate's ordinary 127.0.0.0/8 baseline and fails closed on every ambiguity.
//
// PurposePreAssignment excludes an already-owned /32 by construction. A repair
// observer must use a future purpose that also proves assignment ownership
// instead of weakening this pool-admission rule.
func classifyRoutes(observation Observation) State {
	snapshot := observation.Routes
	baseline := netip.MustParsePrefix("127.0.0.0/8")
	baselineCount := 0
	selectedMatches := 0
	conflict := false
	indeterminate := !snapshot.Complete || snapshot.Truncated || snapshot.Selected == nil

	for _, fact := range snapshot.Matching {
		if fact.Destination == baseline {
			baselineCount++
		}
		if fact.Destination.Bits() > baseline.Bits() {
			if fact.Normalization == RouteNormalizationMacOSCloneUnresolved {
				indeterminate = true
			} else {
				conflict = true
			}
		}
		if snapshot.Selected != nil && fact == *snapshot.Selected {
			selectedMatches++
		}
	}

	if baselineCount > 1 || selectedMatches > 1 {
		conflict = true
	}
	if baselineCount == 0 {
		if snapshot.Complete {
			conflict = true
		} else {
			indeterminate = true
		}
	}
	if snapshot.Selected != nil {
		selected := *snapshot.Selected
		if !selected.NativeLoopback || !noGateway(selected.Gateway) {
			conflict = true
		}
		if selected.Normalization == RouteNormalizationMacOSCloneUnresolved {
			indeterminate = true
		} else if selected.Destination != baseline {
			conflict = true
		}
	}
	if conflict {
		return StateConflict
	}
	if indeterminate {
		return StateIndeterminate
	}
	return StateSafe
}

// classifySockets distinguishes definite endpoint conflicts from incomplete enumeration.
func classifySockets(observation Observation) State {
	conflict := false
	for _, fact := range observation.Sockets.Endpoints {
		if fact.Protocol == SocketProtocolTCP && !fact.TCPAccepting {
			continue
		}
		if fact.Address == observation.Request.Candidate() || fact.Address == netip.IPv4Unspecified() {
			conflict = true
		}
		if fact.Address == netip.IPv6Unspecified() && fact.IPv6Only != IPv6OnlyEnabled {
			conflict = true
		}
	}
	if conflict {
		return StateConflict
	}
	if !observation.Sockets.Complete || observation.Sockets.Truncated {
		return StateIndeterminate
	}
	return StateSafe
}

// classifyPolicy treats non-loopback route_localnet enablement as a loss of loopback isolation.
func classifyPolicy(observation Observation) State {
	if observation.Scope.Platform != PlatformLinux {
		return StateSafe
	}
	facts := observation.Policy.Linux
	// ip_nonlocal_bind changes where a process may bind, but it does not itself
	// prove a current endpoint conflict. Socket enumeration supplies that fact;
	// fingerprinting the setting prevents a ticket from surviving a policy change.
	for _, fact := range facts.RouteLocalnet {
		if !sameInterfaceAuthority(PlatformLinux, fact.Interface, observation.Loopback.Interface) && fact.Enabled {
			return StateConflict
		}
	}
	if !facts.Complete || facts.Truncated {
		return StateIndeterminate
	}
	return StateSafe
}

// combineStates makes a definite conflict authoritative while requiring every component to prove safety.
func combineStates(states ...State) State {
	combined := StateSafe
	for _, state := range states {
		if state == StateConflict {
			return StateConflict
		}
		if state == StateIndeterminate {
			combined = StateIndeterminate
		}
	}
	return combined
}
