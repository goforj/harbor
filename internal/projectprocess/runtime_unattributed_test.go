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
	forceSignaled    bool
	forceErr         error
	settled          bool
	settledErr       error
	afterSettled     func()
	inspectCalls     int
	gracefulCalls    int
	forceCalls       int
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
		force: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			control.forceCalls++
			return control.forceSignaled, control.forceErr
		},
		settled: func(_ context.Context, _ unattributedRuntimeReceipt) (bool, error) {
			control.settledCalls++
			if control.afterSettled != nil {
				control.afterSettled()
			}
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

// TestUnattributedRuntimeObservationAllowsProjectOwnedListenerRoot proves a stale app listener can be correlated by exact checkout ownership after its forj ancestor is gone.
func TestUnattributedRuntimeObservationAllowsProjectOwnedListenerRoot(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	listener := observation.Members[1]
	listener.ParentPID = observation.RootParent.PID
	listener.ProcessGroupID = 201
	listener.SessionID = 202
	listener.CommandExact = false
	observation.RootKind = unattributedRuntimeRootProjectListener
	observation.Root = listener
	observation.Members = []runtimeRepairProcessFact{listener}
	if err := observation.validate(); err != nil {
		t.Fatalf("project-owned listener observation error = %v", err)
	}
	display := unattributedRuntimeDisplay(observation)
	if display.Command != runtimeRepairProjectListenerCommand {
		t.Fatalf("project-owned listener display command = %q, want %q", display.Command, runtimeRepairProjectListenerCommand)
	}
	if err := display.Validate(); err != nil {
		t.Fatalf("project-owned listener display validation error = %v", err)
	}
}

// TestUnattributedRuntimeObservationAllowsExactLeasedAddressRoot proves a same-user listener remains actionable when its working directory no longer identifies the checkout.
func TestUnattributedRuntimeObservationAllowsExactLeasedAddressRoot(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	listener := observation.Members[1]
	listener.ParentPID = observation.RootParent.PID
	listener.ProcessGroupID = listener.PID
	listener.SessionID = listener.PID
	listener.WorkingDirectory = "/private/tmp/unrelated-working-directory"
	observation.RootKind = unattributedRuntimeRootProjectAddress
	observation.Root = listener
	observation.Members = []runtimeRepairProcessFact{listener}
	if err := observation.validate(); err != nil {
		t.Fatalf("exact leased-address observation error = %v", err)
	}
	if display := unattributedRuntimeDisplay(observation); display.Command != runtimeRepairProjectListenerCommand {
		t.Fatalf("exact leased-address display command = %q, want %q", display.Command, runtimeRepairProjectListenerCommand)
	}
}

// TestUnattributedRuntimeObservationAllowsExactLeasedForjScope proves an exact forj dev ancestor remains actionable when its checkout path is no longer available as process metadata.
func TestUnattributedRuntimeObservationAllowsExactLeasedForjScope(t *testing.T) {
	observation := unattributedRuntimeTestObservation(t)
	observation.RootKind = unattributedRuntimeRootProjectForj
	observation.Root.WorkingDirectory = "/private/tmp/unrelated-working-directory"
	observation.Members[0] = observation.Root
	if err := observation.validate(); err != nil {
		t.Fatalf("exact leased forj observation error = %v", err)
	}
	if display := unattributedRuntimeDisplay(observation); display.Command != runtimeRepairCommand {
		t.Fatalf("exact leased forj display command = %q, want %q", display.Command, runtimeRepairCommand)
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

	projectOwned := unattributedRuntimeTestObservation(t)
	projectOwned.RootKind = unattributedRuntimeRootProjectListener
	projectOwned.Root = projectOwned.Members[1]
	projectOwned.Members = []runtimeRepairProcessFact{projectOwned.Root}
	projectOwned.Root.PID = projectOwned.Root.PID + 1
	if err := projectOwned.validate(); err == nil {
		t.Fatal("project-owned listener root PID drift validation error = nil")
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
	if control.forceCalls != 1 {
		t.Fatalf("force calls = %d, want one bounded escalation", control.forceCalls)
	}
}

// TestUnattributedRuntimeRepairConfirmEscalatesAfterGracefulNonconvergence proves an exact no-session scope gets one bounded forceful pass.
func TestUnattributedRuntimeRepairConfirmEscalatesAfterGracefulNonconvergence(t *testing.T) {
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
		forceSignaled:    true,
	}
	control.afterSettled = func() {
		if control.forceCalls > 0 {
			control.settled = true
		}
	}
	repairer := newUnattributedRuntimeRepairerWithControl(control.control(), 2*time.Millisecond, time.Millisecond)
	confirmation, err := repairer.Confirm(context.Background(), *inspection.Candidate)
	if err != nil || confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, %v; want settled confirmation", confirmation, err)
	}
	if control.gracefulCalls != 1 || control.forceCalls != 1 {
		t.Fatalf("signals = graceful %d, force %d; want one each", control.gracefulCalls, control.forceCalls)
	}
}
