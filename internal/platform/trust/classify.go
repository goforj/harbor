package trust

// State is the fail-closed overall classification of one trust observation.
type State string

const (
	// StateAbsent means no relevant trust entry claims the requested certificate.
	StateAbsent State = "absent"
	// StateExact means one exact owned entry and no conflicting entry are present.
	StateExact State = "exact"
	// StateOwnedDrifted means one uniquely owned entry is safely identifiable but needs repair.
	StateOwnedDrifted State = "owned-drifted"
	// StateForeign means at least one relevant entry is not owned by this request.
	StateForeign State = "foreign"
	// StateAmbiguous means more than one entry carries this request's exact ownership marker.
	StateAmbiguous State = "ambiguous"
	// StateIndeterminate means native evidence was incomplete or truncated.
	StateIndeterminate State = "indeterminate"
)

// OwnedState classifies only entries carrying this request's exact ownership marker.
type OwnedState string

const (
	// OwnedStateAbsent means no entry carries the request's ownership marker.
	OwnedStateAbsent OwnedState = "absent"
	// OwnedStateExact means one marked entry has the complete requested native shape.
	OwnedStateExact OwnedState = "exact"
	// OwnedStateDrifted means one marked entry differs from the requested native shape.
	OwnedStateDrifted OwnedState = "drifted"
	// OwnedStateAmbiguous means multiple entries carry the same ownership marker.
	OwnedStateAmbiguous OwnedState = "ambiguous"
)

// Assessment separates owned-artifact state from coexisting foreign trust claims.
type Assessment struct {
	State        State
	Owned        OwnedState
	ForeignCount int
}

// Classify validates facts and recomputes their owned and overall state.
func (observation Observation) Classify() (Assessment, error) {
	if err := observation.Validate(); err != nil {
		return Assessment{}, err
	}
	return classifyValidated(observation), nil
}

// classifyValidated classifies facts after their bounded representation has already been proven valid.
func classifyValidated(observation Observation) Assessment {
	ownedCount := 0
	exactOwnedCount := 0
	foreignCount := 0
	for _, entry := range observation.Entries {
		if markerMatchesRequest(entry.Owner, observation.Request) {
			ownedCount++
			if entryMatchesRequest(entry, observation.Request) {
				exactOwnedCount++
			}
			continue
		}
		if entryConflictsWithRequest(entry, observation.Request) {
			foreignCount++
		}
	}

	assessment := Assessment{
		Owned:        classifyOwnedState(ownedCount, exactOwnedCount),
		ForeignCount: foreignCount,
	}
	assessment.State = classifyOverallState(observation, assessment)
	return assessment
}

// classifyOwnedState requires one uniquely marked entry before it can be exact or repairable.
func classifyOwnedState(ownedCount, exactOwnedCount int) OwnedState {
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

// classifyOverallState makes incomplete evidence authoritative before reporting conflicts.
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

// markerMatchesRequest recognizes only the current exact installation, mechanism, and CA marker.
func markerMatchesRequest(marker *OwnerMarker, request Request) bool {
	return marker != nil && *marker == request.OwnerMarker()
}

// entryMatchesRequest checks every semantic and native-shape fact required for exact ownership.
func entryMatchesRequest(entry Entry, request Request) bool {
	return entry.Mechanism == request.Mechanism() &&
		entry.CertificateFingerprint == request.AuthorityFingerprint() &&
		entry.NativeExact
}

// entryConflictsWithRequest ignores unrelated roots while retaining same-CA and competing Harbor claims.
func entryConflictsWithRequest(entry Entry, request Request) bool {
	return entry.CertificateFingerprint == request.AuthorityFingerprint() || entry.Owner != nil
}

// onlyPreexistingIdenticalEntries identifies an already trusted unowned CA that Harbor must preserve and may reuse.
func onlyPreexistingIdenticalEntries(observation Observation) bool {
	found := false
	for _, entry := range observation.Entries {
		if entry.Owner != nil {
			return false
		}
		if entry.CertificateFingerprint != observation.Request.AuthorityFingerprint() {
			continue
		}
		if !entry.NativeExact {
			return false
		}
		found = true
	}
	return found
}
