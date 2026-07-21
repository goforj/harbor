package projectprocess

import (
	"net/netip"
	"testing"
)

// unattributedRuntimeTestObservation adapts the shared synthetic process scope to the no-session contract.
func unattributedRuntimeTestObservation(t *testing.T) unattributedRuntimeNativeObservation {
	t.Helper()
	base := runtimeRepairTestObservation(t)
	return unattributedRuntimeNativeObservation{
		Target:     base.Target,
		DaemonUID:  base.DaemonUID,
		Root:       base.Root,
		RootParent: base.RootParent,
		Members:    append([]runtimeRepairProcessFact(nil), base.Members...),
		Listener:   base.Listener,
	}
}

// TestUnattributedRuntimeInspectionProjectionKeepsReceiptOpaque proves actionable facts are reduced to one validated display and digest.
func TestUnattributedRuntimeInspectionProjectionKeepsReceiptOpaque(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	inspection, err := unattributedRuntimeInspection(unattributedRuntimeNativeInspection{
		State:       RuntimeRepairInspectionActionable,
		Observation: &observation,
	})
	if err != nil {
		t.Fatalf("unattributedRuntimeInspection() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Candidate == nil {
		t.Fatalf("inspection = %#v, want actionable candidate", inspection)
	}
	if err := inspection.Validate(); err != nil {
		t.Fatalf("inspection.Validate() error = %v", err)
	}
	clone := inspection.Candidate.Clone()
	if err := clone.Validate(); err != nil {
		t.Fatalf("candidate.Clone().Validate() error = %v", err)
	}
	clone.Display.RootPID++
	if err := clone.Validate(); err == nil {
		t.Fatal("mutated candidate.Validate() error = nil")
	}
}

// TestUnattributedRuntimeObservationAllowsNonDedicatedRoot proves the separate action does not silently require a Harbor-created session.
func TestUnattributedRuntimeObservationAllowsNonDedicatedRoot(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	observation.Root.ProcessGroupID = 200
	observation.Root.SessionID = 201
	observation.Members[0] = observation.Root
	observation.Members[1].ProcessGroupID = 202
	observation.Members[1].SessionID = 201
	if err := observation.validate(); err != nil {
		t.Fatalf("unattributed observation with non-dedicated root error = %v", err)
	}
}

// TestUnattributedRuntimeObservationRejectsUnsafeScopes proves foreign and detached descendants cannot become candidates.
func TestUnattributedRuntimeObservationRejectsUnsafeScopes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*unattributedRuntimeNativeObservation)
	}{
		{name: "foreign descendant", mutate: func(observation *unattributedRuntimeNativeObservation) {
			observation.Members[1].EffectiveUID++
		}},
		{name: "detached descendant", mutate: func(observation *unattributedRuntimeNativeObservation) {
			observation.Members[1].ParentPID = observation.RootParent.PID
		}},
		{name: "parent inside scope", mutate: func(observation *unattributedRuntimeNativeObservation) {
			observation.RootParent.PID = observation.Members[1].PID
		}},
		{name: "listener endpoint drift", mutate: func(observation *unattributedRuntimeNativeObservation) {
			observation.Listener.Endpoint = netip.AddrPortFrom(observation.Target.Endpoint.Addr(), observation.Target.Endpoint.Port()+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := unattributedRuntimeTestObservation(t)
			test.mutate(&observation)
			if err := observation.validate(); err == nil {
				t.Fatal("observation.validate() error = nil")
			}
		})
	}
}
