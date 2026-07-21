package projectprocess

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// unattributedRuntimeTestControl records native policy effects for confirmation tests.
type unattributedRuntimeTestControl struct {
	inspection       unattributedRuntimeNativeInspection
	inspectErr       error
	gracefulSignaled bool
	gracefulErr      error
	settled          bool
	settledErr       error
	inspectCalls     int
	gracefulCalls    int
	settledCalls     int
}

// control exposes deterministic native hooks through the production adapter seam.
func (control *unattributedRuntimeTestControl) control() unattributedRuntimeControl {
	return unattributedRuntimeControl{
		inspect: func(_ context.Context, _ RuntimeRepairTarget) (unattributedRuntimeNativeInspection, error) {
			control.inspectCalls++
			inspection := control.inspection
			if inspection.Observation != nil {
				observation := inspection.Observation.clone()
				inspection.Observation = &observation
			}
			return inspection, control.inspectErr
		},
		graceful: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			control.gracefulCalls++
			return control.gracefulSignaled, control.gracefulErr
		},
		settled: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			control.settledCalls++
			return control.settled, control.settledErr
		},
	}
}

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

// TestUnattributedRuntimeRepairConfirmSettles proves explicit confirmation signals only after exact reinspection and waits for settlement.
func TestUnattributedRuntimeRepairConfirmSettles(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	inspection, err := unattributedRuntimeInspection(unattributedRuntimeNativeInspection{
		State:       RuntimeRepairInspectionActionable,
		Observation: &observation,
	})
	if err != nil {
		t.Fatalf("unattributedRuntimeInspection() error = %v", err)
	}
	control := &unattributedRuntimeTestControl{
		inspection:       unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
		settled:          true,
	}
	repairer := newUnattributedRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), *inspection.Candidate)
	if err != nil || confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, %v; want settled signal", confirmation, err)
	}
	if control.inspectCalls != 1 || control.gracefulCalls != 1 || control.settledCalls != 1 {
		t.Fatalf("native calls = inspect %d, graceful %d, settled %d; want one each", control.inspectCalls, control.gracefulCalls, control.settledCalls)
	}
}

// TestUnattributedRuntimeRepairConfirmRejectsDriftWithoutSignal proves a changed scope consumes no signal authority.
func TestUnattributedRuntimeRepairConfirmRejectsDriftWithoutSignal(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	inspection, err := unattributedRuntimeInspection(unattributedRuntimeNativeInspection{
		State:       RuntimeRepairInspectionActionable,
		Observation: &observation,
	})
	if err != nil {
		t.Fatalf("unattributedRuntimeInspection() error = %v", err)
	}
	control := &unattributedRuntimeTestControl{inspection: unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionAmbiguous}}
	repairer := newUnattributedRuntimeRepairerWithControl(control.control(), time.Second, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), *inspection.Candidate)
	if err != nil || confirmation.State != RuntimeRepairConfirmationDrifted || confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, %v; want zero-signal drift", confirmation, err)
	}
	if control.gracefulCalls != 0 || control.settledCalls != 0 {
		t.Fatalf("native calls after drift = graceful %d, settled %d; want zero", control.gracefulCalls, control.settledCalls)
	}
}

// TestUnattributedRuntimeRepairConfirmReportsUnsettledScope proves a successful signal cannot be reported as settled without postconditions.
func TestUnattributedRuntimeRepairConfirmReportsUnsettledScope(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	inspection, err := unattributedRuntimeInspection(unattributedRuntimeNativeInspection{
		State:       RuntimeRepairInspectionActionable,
		Observation: &observation,
	})
	if err != nil {
		t.Fatalf("unattributedRuntimeInspection() error = %v", err)
	}
	control := &unattributedRuntimeTestControl{
		inspection:       unattributedRuntimeNativeInspection{State: RuntimeRepairInspectionActionable, Observation: &observation},
		gracefulSignaled: true,
	}
	repairer := newUnattributedRuntimeRepairerWithControl(control.control(), 2*time.Millisecond, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), *inspection.Candidate)
	if !errors.Is(err, ErrRuntimeRepairNotSettled) || confirmation.State != RuntimeRepairConfirmationFailed || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, %v; want signaled unsettled failure", confirmation, err)
	}
}
