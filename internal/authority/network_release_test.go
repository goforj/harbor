package authority

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingNetworkReleaseCoordinator records the caller-bound start request before returning its scripted result.
type recordingNetworkReleaseCoordinator struct {
	request reconcile.GlobalNetworkReleaseStartRequest
	record  state.OperationRecord
	err     error
}

// Start records the exact request so tests can assert the authority boundary.
func (c *recordingNetworkReleaseCoordinator) Start(_ context.Context, request reconcile.GlobalNetworkReleaseStartRequest) (state.OperationRecord, error) {
	c.request = request
	return c.record, c.err
}

// recordingNetworkReleasePlans records an exact durable plan selection before returning its scripted result.
type recordingNetworkReleasePlans struct {
	request domain.OperationID
	plan    state.GlobalNetworkReleasePlanRecord
	found   bool
	err     error
}

// ReadGlobalNetworkReleasePlan records the requested daemon operation identity.
func (r *recordingNetworkReleasePlans) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	r.request = operationID
	return r.plan, r.found, r.err
}

// TestNetworkReleaseAuthorityValidatesBeforeEntropy proves malformed client input cannot consume daemon identity entropy.
func TestNetworkReleaseAuthorityValidatesBeforeEntropy(t *testing.T) {
	plans := &recordingNetworkReleasePlans{}
	coordinator := &recordingNetworkReleaseCoordinator{}
	authority := newNetworkReleaseAuthority(plans, coordinator, func() (domain.OperationID, error) {
		t.Fatal("operation identity factory was called for an invalid request")
		return "", nil
	})
	_, err := authority.StartNetworkRelease(t.Context(), control.Caller{}, control.StartNetworkReleaseRequest{})
	if err == nil {
		t.Fatal("StartNetworkRelease() unexpectedly succeeded")
	}
	if coordinator.request.OperationID != "" || plans.request != "" {
		t.Fatalf("invalid request reached dependencies: %#v, %q", coordinator.request, plans.request)
	}
}

// TestNetworkReleaseAuthorityRejectsEntropyFailures prevents broken daemon IDs from reaching durable authority.
func TestNetworkReleaseAuthorityRejectsEntropyFailures(t *testing.T) {
	failure := errors.New("entropy unavailable")
	for _, test := range []struct {
		name    string
		factory func() (domain.OperationID, error)
		want    string
	}{
		{
			name: "failure",
			factory: func() (domain.OperationID, error) {
				return "", failure
			},
			want: "generate network release operation identity",
		},
		{
			name: "invalid ID",
			factory: func() (domain.OperationID, error) {
				return "", nil
			},
			want: "generated network release operation identity is invalid",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingNetworkReleaseCoordinator{}
			authority := newNetworkReleaseAuthority(&recordingNetworkReleasePlans{}, coordinator, test.factory)
			_, err := authority.StartNetworkRelease(t.Context(), control.Caller{}, control.StartNetworkReleaseRequest{IntentID: "intent-release"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StartNetworkRelease() error = %v, want %q", err, test.want)
			}
			if coordinator.request.OperationID != "" {
				t.Fatalf("invalid entropy reached coordinator: %#v", coordinator.request)
			}
		})
	}
}

// TestNetworkReleaseAuthorityBindsCallerAndProjectsExactPlan proves start binds the transport user and exposes only durable progress.
func TestNetworkReleaseAuthorityBindsCallerAndProjectsExactPlan(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease)
	coordinator := &recordingNetworkReleaseCoordinator{record: plan.Operation}
	plans := &recordingNetworkReleasePlans{
		plan:  plan,
		found: true,
	}
	authority := newNetworkReleaseAuthority(plans, coordinator, func() (domain.OperationID, error) {
		return "operation-release", nil
	})
	caller := control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}}
	result, err := authority.StartNetworkRelease(t.Context(), caller, control.StartNetworkReleaseRequest{IntentID: "intent-release"})
	if err != nil {
		t.Fatalf("StartNetworkRelease() error = %v", err)
	}
	if coordinator.request.OperationID != "operation-release" || coordinator.request.IntentID != "intent-release" || coordinator.request.RequesterIdentity != "authenticated-user" {
		t.Fatalf("coordinator request = %#v", coordinator.request)
	}
	if plans.request != "operation-release" || result.Operation != plan.Operation.Operation || result.Revision != plan.Operation.Revision || result.Phase != control.NetworkReleasePhaseRuntimeRelease || result.CheckpointRevision != plan.CheckpointRevision || result.NetworkRevision != plan.NetworkRevision {
		t.Fatalf("projected release = %#v", result)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal release = %v", err)
	}
	if strings.Contains(string(payload), "authority") || strings.Contains(string(payload), "host") {
		t.Fatalf("control projection leaks durable authority: %s", payload)
	}
}

// TestNetworkReleaseAuthorityRejectsUncorrelatedStartResults prevents a coordinator result from crossing intent or operation ownership.
func TestNetworkReleaseAuthorityRejectsUncorrelatedStartResults(t *testing.T) {
	for _, test := range []struct {
		name         string
		started      state.OperationRecord
		plan         state.GlobalNetworkReleasePlanRecord
		wantPlanRead bool
	}{
		{
			name:    "returned intent",
			started: validNetworkReleasePlan("operation-release", "intent-other", state.GlobalNetworkReleasePlanPhaseRuntimeRelease).Operation,
			plan:    validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease),
		},
		{
			name:         "plan operation",
			started:      validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease).Operation,
			plan:         validNetworkReleasePlan("operation-other", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease),
			wantPlanRead: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plans := &recordingNetworkReleasePlans{
				plan:  test.plan,
				found: true,
			}
			authority := newNetworkReleaseAuthority(plans, &recordingNetworkReleaseCoordinator{record: test.started}, func() (domain.OperationID, error) {
				return "operation-release", nil
			})
			caller := control.Caller{
				Transport: local.PeerIdentity{
					UserID: "authenticated-user",
				},
			}
			if _, err := authority.StartNetworkRelease(t.Context(), caller, control.StartNetworkReleaseRequest{IntentID: "intent-release"}); err == nil {
				t.Fatal("StartNetworkRelease() unexpectedly succeeded")
			}
			if (plans.request != "") != test.wantPlanRead {
				t.Fatalf("plan read = %q, want read %t", plans.request, test.wantPlanRead)
			}
		})
	}
}

// TestNetworkReleaseAuthorityReadSelectsExactPlanAndMapsDurableErrors covers absent, mismatched, and reviewed durable error boundaries.
func TestNetworkReleaseAuthorityReadSelectsExactPlanAndMapsDurableErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		plans recordingNetworkReleasePlans
		code  rpc.ErrorCode
	}{
		{
			name:  "absent",
			plans: recordingNetworkReleasePlans{},
			code:  rpc.ErrorCodeNotFound,
		},
		{
			name: "missing",
			plans: recordingNetworkReleasePlans{
				err: &state.OperationNotFoundError{
					OperationID: "operation-release",
				},
			},
			code: rpc.ErrorCodeNotFound,
		},
		{
			name: "conflict",
			plans: recordingNetworkReleasePlans{
				err: &state.GlobalNetworkReleaseActiveError{},
			},
			code: rpc.ErrorCodeConflict,
		},
		{
			name: "mismatched",
			plans: recordingNetworkReleasePlans{
				plan:  validNetworkReleasePlan("operation-other", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease),
				found: true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newNetworkReleaseAuthority(&test.plans, &recordingNetworkReleaseCoordinator{}, func() (domain.OperationID, error) { return "operation-generated", nil })
			_, err := authority.ReadNetworkRelease(t.Context(), control.Caller{}, control.ReadNetworkReleaseRequest{OperationID: "operation-release"})
			if err == nil {
				t.Fatal("ReadNetworkRelease() unexpectedly succeeded")
			}
			if test.plans.request != "operation-release" {
				t.Fatalf("plan selector = %q", test.plans.request)
			}
			if test.code != "" {
				var handlerError *session.HandlerError
				if !errors.As(err, &handlerError) || handlerError.Code() != test.code {
					t.Fatalf("ReadNetworkRelease() error = %#v, want code %s", err, test.code)
				}
			}
		})
	}
}

// TestNetworkReleaseAuthorityMapsStartErrors preserves cancellation and reviewed durable error categories at initiation.
func TestNetworkReleaseAuthorityMapsStartErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code rpc.ErrorCode
	}{
		{
			name: "missing",
			err: &state.OperationNotFoundError{
				OperationID: "operation-release",
			},
			code: rpc.ErrorCodeNotFound,
		},
		{
			name: "conflict",
			err: &state.IntentConflictError{
				IntentID: "intent-release",
			},
			code: rpc.ErrorCodeConflict,
		},
		{
			name: "canceled",
			err:  context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingNetworkReleaseCoordinator{
				err: test.err,
			}
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				coordinator,
				func() (domain.OperationID, error) {
					return "operation-release", nil
				},
			)
			caller := control.Caller{
				Transport: local.PeerIdentity{
					UserID: "authenticated-user",
				},
			}
			_, err := authority.StartNetworkRelease(t.Context(), caller, control.StartNetworkReleaseRequest{IntentID: "intent-release"})
			if err == nil {
				t.Fatal("StartNetworkRelease() unexpectedly succeeded")
			}
			if test.code == "" {
				if !errors.Is(err, test.err) {
					t.Fatalf("StartNetworkRelease() error = %v, want %v", err, test.err)
				}
				return
			}
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.code {
				t.Fatalf("StartNetworkRelease() error = %#v, want code %s", err, test.code)
			}
		})
	}
}

// TestNetworkReleaseAuthorityProjectsEveryPhase keeps public phase names locked to the fixed durable sequence.
func TestNetworkReleaseAuthorityProjectsEveryPhase(t *testing.T) {
	for _, test := range []struct {
		phase state.GlobalNetworkReleasePlanPhase
		want  control.NetworkReleasePhase
	}{
		{
			phase: state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
			want:  control.NetworkReleasePhaseRuntimeRelease,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseLowPorts,
			want:  control.NetworkReleasePhaseLowPorts,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseResolver,
			want:  control.NetworkReleasePhaseResolver,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseTrust,
			want:  control.NetworkReleasePhaseTrust,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseLoopbacks,
			want:  control.NetworkReleasePhaseLoopbacks,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseVerifyEffects,
			want:  control.NetworkReleasePhaseVerifyEffects,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseOwnership,
			want:  control.NetworkReleasePhaseOwnership,
		},
		{
			phase: state.GlobalNetworkReleasePlanPhaseProjection,
			want:  control.NetworkReleasePhaseProjection,
		},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			result, err := networkReleaseOperation(validNetworkReleasePlan("operation-release", "intent-release", test.phase))
			if err != nil || result.Phase != test.want {
				t.Fatalf("networkReleaseOperation() = %#v, %v; want phase %q", result, err, test.want)
			}
		})
	}
}

// TestNetworkReleaseAuthorityRejectsMalformedPlanProjection ensures invalid durable values never reach the public protocol.
func TestNetworkReleaseAuthorityRejectsMalformedPlanProjection(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "phase",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Phase = "unknown"
			},
		},
		{
			name: "operation",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.State = domain.OperationSucceeded
			},
		},
		{
			name: "checkpoint",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.CheckpointRevision = 0
			},
		},
		{
			name: "network revision",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.NetworkRevision = plan.Operation.Revision
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseRuntimeRelease)
			test.mutate(&plan)
			if _, err := networkReleaseOperation(plan); err == nil {
				t.Fatal("networkReleaseOperation() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityPreservesCancellation prevents a canceled request from being recast as a control failure.
func TestNetworkReleaseAuthorityPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plans := &recordingNetworkReleasePlans{err: context.Canceled}
	authority := newNetworkReleaseAuthority(plans, &recordingNetworkReleaseCoordinator{}, func() (domain.OperationID, error) { return "operation-generated", nil })
	_, err := authority.ReadNetworkRelease(ctx, control.Caller{}, control.ReadNetworkReleaseRequest{OperationID: "operation-release"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadNetworkRelease() error = %v", err)
	}
}

// TestNetworkReleaseAuthorityRejectsInvalidReadBeforeDurableAccess prevents malformed selectors from reaching state.
func TestNetworkReleaseAuthorityRejectsInvalidReadBeforeDurableAccess(t *testing.T) {
	plans := &recordingNetworkReleasePlans{}
	authority := newNetworkReleaseAuthority(plans, &recordingNetworkReleaseCoordinator{}, func() (domain.OperationID, error) {
		return "operation-generated", nil
	})
	_, err := authority.ReadNetworkRelease(t.Context(), control.Caller{}, control.ReadNetworkReleaseRequest{})
	if err == nil {
		t.Fatal("ReadNetworkRelease() unexpectedly succeeded")
	}
	if plans.request != "" {
		t.Fatalf("invalid read reached durable state with %q", plans.request)
	}
}

// TestNewNetworkReleaseAuthorityRequiresEveryDependency fails fast on incomplete or typed-nil adapter wiring.
func TestNewNetworkReleaseAuthorityRequiresEveryDependency(t *testing.T) {
	var nilPlans *recordingNetworkReleasePlans
	var nilCoordinator *recordingNetworkReleaseCoordinator
	for _, test := range []struct {
		name    string
		plans   networkReleasePlanReader
		coord   networkReleaseCoordinator
		factory func() (domain.OperationID, error)
	}{
		{
			name:    "plans",
			plans:   nil,
			coord:   &recordingNetworkReleaseCoordinator{},
			factory: func() (domain.OperationID, error) { return "operation-generated", nil },
		},
		{
			name:    "coordinator",
			plans:   &recordingNetworkReleasePlans{},
			coord:   nil,
			factory: func() (domain.OperationID, error) { return "operation-generated", nil },
		},
		{
			name:    "typed nil plans",
			plans:   nilPlans,
			coord:   &recordingNetworkReleaseCoordinator{},
			factory: func() (domain.OperationID, error) { return "operation-generated", nil },
		},
		{
			name:    "typed nil coordinator",
			plans:   &recordingNetworkReleasePlans{},
			coord:   nilCoordinator,
			factory: func() (domain.OperationID, error) { return "operation-generated", nil },
		},
		{
			name:    "nil factory",
			plans:   &recordingNetworkReleasePlans{},
			coord:   &recordingNetworkReleaseCoordinator{},
			factory: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("newNetworkReleaseAuthority() did not panic")
				}
			}()
			newNetworkReleaseAuthority(test.plans, test.coord, test.factory)
		})
	}
}

// validNetworkReleasePlan constructs the smallest complete durable release plan needed by adapter tests.
func validNetworkReleasePlan(operationID domain.OperationID, intentID domain.IntentID, phase state.GlobalNetworkReleasePlanPhase) state.GlobalNetworkReleasePlanRecord {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	started := now.Add(time.Second)
	return state.GlobalNetworkReleasePlanRecord{
		Operation: state.OperationRecord{
			Operation: domain.Operation{
				ID:          operationID,
				IntentID:    intentID,
				Kind:        domain.OperationKindNetworkRelease,
				State:       domain.OperationRunning,
				Phase:       "releasing network runtime",
				RequestedAt: now,
				StartedAt:   &started,
			},
			Revision: 11,
		},
		Phase:              phase,
		CheckpointRevision: 12,
		NetworkRevision:    10,
	}
}
