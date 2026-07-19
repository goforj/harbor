package resolver

// State is the fail-closed overall classification of one resolver observation.
type State string

const (
	// StateAbsent means no owned or foreign rule claims the requested suffix.
	StateAbsent State = "absent"
	// StateExact means one exact owned rule and no foreign claim are present.
	StateExact State = "exact"
	// StateOwnedDrifted means one uniquely owned rule is safely identifiable but needs repair.
	StateOwnedDrifted State = "owned-drifted"
	// StateForeign means at least one rule not owned by this request claims the suffix.
	StateForeign State = "foreign"
	// StateAmbiguous means more than one rule carries this request's exact ownership marker.
	StateAmbiguous State = "ambiguous"
	// StateIndeterminate means the native rule set was incomplete or truncated.
	StateIndeterminate State = "indeterminate"
)

// OwnedState classifies only artifacts carrying this request's exact ownership marker.
type OwnedState string

const (
	// OwnedStateAbsent means no rule carries the request's ownership marker.
	OwnedStateAbsent OwnedState = "absent"
	// OwnedStateExact means one marked rule has the complete requested native shape.
	OwnedStateExact OwnedState = "exact"
	// OwnedStateDrifted means one marked rule differs from the requested native shape.
	OwnedStateDrifted OwnedState = "drifted"
	// OwnedStateAmbiguous means multiple rules carry the same ownership marker.
	OwnedStateAmbiguous OwnedState = "ambiguous"
)

// Assessment separates owned-artifact state from coexisting foreign suffix claims.
type Assessment struct {
	State        State
	Owned        OwnedState
	ForeignCount int
}

// Classify validates raw facts and recomputes their owned and overall state.
func (o Observation) Classify() (Assessment, error) {
	if err := o.Validate(); err != nil {
		return Assessment{}, err
	}
	return classifyValidated(o), nil
}

// classifyValidated classifies facts after their bounded representation has already been proven valid.
func classifyValidated(o Observation) Assessment {
	ownedCount := 0
	exactOwnedCount := 0
	foreignCount := 0
	for _, rule := range o.Rules {
		if markerMatchesRequest(rule.Owner, o.Request) {
			ownedCount++
			if ruleMatchesRequest(rule, o.Request) {
				exactOwnedCount++
			}
			continue
		}
		foreignCount++
	}

	assessment := Assessment{
		Owned:        classifyOwnedState(ownedCount, exactOwnedCount),
		ForeignCount: foreignCount,
	}
	assessment.State = classifyOverallState(o, assessment)
	return assessment
}

// classifyOwnedState requires one uniquely marked artifact before it can be exact or repairable.
func classifyOwnedState(ownedCount int, exactOwnedCount int) OwnedState {
	switch {
	case ownedCount == 0:
		return OwnedStateAbsent
	case ownedCount > 1:
		return OwnedStateAmbiguous
	case exactOwnedCount == 1:
		return OwnedStateExact
	default:
		return OwnedStateDrifted
	}
}

// classifyOverallState makes incomplete evidence authoritative before reporting definite conflicts.
func classifyOverallState(observation Observation, assessment Assessment) State {
	if !observation.Complete || observation.Truncated {
		return StateIndeterminate
	}
	if assessment.Owned == OwnedStateAmbiguous {
		return StateAmbiguous
	}
	if assessment.ForeignCount > 0 {
		return StateForeign
	}
	switch assessment.Owned {
	case OwnedStateAbsent:
		return StateAbsent
	case OwnedStateExact:
		return StateExact
	case OwnedStateDrifted:
		return StateOwnedDrifted
	default:
		return StateIndeterminate
	}
}

// markerMatchesRequest recognizes only the current exact installation and policy marker.
func markerMatchesRequest(marker *OwnerMarker, request Request) bool {
	return marker != nil && *marker == request.OwnerMarker()
}

// ruleMatchesRequest checks every platform-neutral semantic and native-shape fact required for ownership.
func ruleMatchesRequest(rule RuleFact, request Request) bool {
	return rule.Mechanism == request.Mechanism() &&
		rule.Namespace == request.Suffix() &&
		len(rule.Servers) == 1 &&
		rule.Servers[0] == request.Endpoint() &&
		rule.RouteOnly &&
		rule.NativeExact
}
