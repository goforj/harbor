package authority

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingNetworkReleaseCoordinator records the caller-bound start request before returning its scripted result.
type recordingNetworkReleaseCoordinator struct {
	request                 reconcile.GlobalNetworkReleaseStartRequest
	prepareRequest          reconcile.GlobalNetworkReleasePrepareLowPortsRequest
	confirmRequest          reconcile.GlobalNetworkReleaseConfirmLowPortsRequest
	prepareResolverRequest  reconcile.GlobalNetworkReleasePrepareResolverRequest
	confirmResolverRequest  reconcile.GlobalNetworkReleaseConfirmResolverRequest
	prepareTrustRequest     reconcile.GlobalNetworkReleasePrepareTrustRequest
	confirmTrustRequest     reconcile.GlobalNetworkReleaseConfirmTrustRequest
	prepareLoopbacksRequest reconcile.GlobalNetworkReleasePrepareLoopbacksRequest
	confirmLoopbacksRequest reconcile.GlobalNetworkReleaseConfirmLoopbacksRequest
	confirmOwnershipRequest reconcile.GlobalNetworkReleaseConfirmOwnershipRequest
	record                  state.OperationRecord
	prepareResult           ticketissuer.LowPortResult
	confirmResult           state.GlobalNetworkReleasePlanRecord
	prepareResolverResult   ticketissuer.ResolverResult
	confirmResolverResult   state.GlobalNetworkReleasePlanRecord
	prepareTrustResult      reconcile.GlobalNetworkReleaseTrustPreparation
	confirmTrustResult      state.GlobalNetworkReleasePlanRecord
	prepareLoopbacksResult  ticketissuer.PoolResult
	confirmLoopbacksResult  state.GlobalNetworkReleasePlanRecord
	confirmOwnershipResult  state.GlobalNetworkReleaseTerminalRecord
	err                     error
}

// Start records the exact request so tests can assert the authority boundary.
func (c *recordingNetworkReleaseCoordinator) Start(_ context.Context, request reconcile.GlobalNetworkReleaseStartRequest) (state.OperationRecord, error) {
	c.request = request
	return c.record, c.err
}

// PrepareLowPorts records one authenticated low-port capability request.
func (c *recordingNetworkReleaseCoordinator) PrepareLowPorts(_ context.Context, request reconcile.GlobalNetworkReleasePrepareLowPortsRequest) (ticketissuer.LowPortResult, error) {
	c.prepareRequest = request
	return c.prepareResult, c.err
}

// ConfirmLowPorts records one authenticated low-port release acknowledgement.
func (c *recordingNetworkReleaseCoordinator) ConfirmLowPorts(_ context.Context, request reconcile.GlobalNetworkReleaseConfirmLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	c.confirmRequest = request
	return c.confirmResult, c.err
}

// PrepareResolver records one authenticated resolver capability request.
func (c *recordingNetworkReleaseCoordinator) PrepareResolver(_ context.Context, request reconcile.GlobalNetworkReleasePrepareResolverRequest) (ticketissuer.ResolverResult, error) {
	c.prepareResolverRequest = request
	return c.prepareResolverResult, c.err
}

// ConfirmResolver records one authenticated resolver release acknowledgement.
func (c *recordingNetworkReleaseCoordinator) ConfirmResolver(_ context.Context, request reconcile.GlobalNetworkReleaseConfirmResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	c.confirmResolverRequest = request
	return c.confirmResolverResult, c.err
}

// PrepareTrust records one authenticated trust capability request.
func (c *recordingNetworkReleaseCoordinator) PrepareTrust(_ context.Context, request reconcile.GlobalNetworkReleasePrepareTrustRequest) (reconcile.GlobalNetworkReleaseTrustPreparation, error) {
	c.prepareTrustRequest = request
	return c.prepareTrustResult, c.err
}

// ConfirmTrust records one authenticated trust release acknowledgement.
func (c *recordingNetworkReleaseCoordinator) ConfirmTrust(_ context.Context, request reconcile.GlobalNetworkReleaseConfirmTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	c.confirmTrustRequest = request
	return c.confirmTrustResult, c.err
}

// PrepareLoopbacks records one authenticated loopback-pool capability request.
func (c *recordingNetworkReleaseCoordinator) PrepareLoopbacks(_ context.Context, request reconcile.GlobalNetworkReleasePrepareLoopbacksRequest) (ticketissuer.PoolResult, error) {
	c.prepareLoopbacksRequest = request
	return c.prepareLoopbacksResult, c.err
}

// ConfirmLoopbacks records one authenticated loopback-pool release acknowledgement.
func (c *recordingNetworkReleaseCoordinator) ConfirmLoopbacks(_ context.Context, request reconcile.GlobalNetworkReleaseConfirmLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	c.confirmLoopbacksRequest = request
	return c.confirmLoopbacksResult, c.err
}

// ConfirmOwnership records one authenticated ownership-release confirmation.
func (c *recordingNetworkReleaseCoordinator) ConfirmOwnership(_ context.Context, request reconcile.GlobalNetworkReleaseConfirmOwnershipRequest) (state.GlobalNetworkReleaseTerminalRecord, error) {
	c.confirmOwnershipRequest = request
	return c.confirmOwnershipResult, c.err
}

// recordingNetworkReleasePlans records an exact durable plan selection before returning its scripted result.
type recordingNetworkReleasePlans struct {
	request       domain.OperationID
	plan          state.GlobalNetworkReleasePlanRecord
	found         bool
	err           error
	terminal      state.GlobalNetworkReleaseTerminalRecord
	terminalFound bool
	terminalErr   error
}

// ReadGlobalNetworkReleaseTerminal records the requested terminal operation identity.
func (r *recordingNetworkReleasePlans) ReadGlobalNetworkReleaseTerminal(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleaseTerminalRecord, bool, error) {
	r.request = operationID
	return r.terminal, r.terminalFound, r.terminalErr
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
	setNetworkReleasePlanOwner(&plan, "authenticated-user")
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

// TestNetworkReleaseAuthorityReadsTerminalForItsOriginalOwner proves projection deletion preserves only owner-bound successful replay.
func TestNetworkReleaseAuthorityReadsTerminalForItsOriginalOwner(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseProjection)
	completedAt := plan.Operation.Operation.RequestedAt.Add(time.Second)
	operation, err := plan.Operation.Operation.Transition(domain.OperationSucceeded, "network released", completedAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	terminal := state.GlobalNetworkReleaseTerminalRecord{
		Operation: state.OperationRecord{
			Operation: operation,
			Revision:  plan.CheckpointRevision + 1,
		},
		OwnerIdentity:            "authenticated-user",
		SourceCheckpointRevision: plan.CheckpointRevision,
		NetworkRevision:          plan.NetworkRevision,
	}
	plans := &recordingNetworkReleasePlans{terminal: terminal, terminalFound: true}
	authority := newNetworkReleaseAuthority(plans, &recordingNetworkReleaseCoordinator{}, func() (domain.OperationID, error) {
		return "operation-unused", nil
	})
	caller := control.Caller{Transport: local.PeerIdentity{UserID: terminal.OwnerIdentity}}
	result, err := authority.ReadNetworkRelease(t.Context(), caller, control.ReadNetworkReleaseRequest{OperationID: operation.ID})
	if err != nil {
		t.Fatalf("ReadNetworkRelease() error = %v", err)
	}
	if result.Operation != operation || result.Revision != terminal.Operation.Revision || result.Phase != control.NetworkReleasePhaseProjection || result.CheckpointRevision != terminal.SourceCheckpointRevision || result.NetworkRevision != terminal.NetworkRevision {
		t.Fatalf("ReadNetworkRelease() = %#v", result)
	}
	other := control.Caller{Transport: local.PeerIdentity{UserID: "different-user"}}
	if _, err := authority.ReadNetworkRelease(t.Context(), other, control.ReadNetworkReleaseRequest{OperationID: operation.ID}); err == nil {
		t.Fatal("ReadNetworkRelease() allowed a different user to read the terminal")
	}
}

// TestNetworkReleaseAuthorityStartsTerminalReplayAcrossEquivalentTimeLocations proves replay correlation compares operation timestamps by instant.
func TestNetworkReleaseAuthorityStartsTerminalReplayAcrossEquivalentTimeLocations(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseProjection)
	completed, err := plan.Operation.Operation.Transition(domain.OperationSucceeded, "network released", plan.Operation.Operation.RequestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	started := state.OperationRecord{
		Operation: completed,
		Revision:  plan.CheckpointRevision + 1,
	}
	stored := completed
	location := time.FixedZone("stored", 0)
	requestedAt := stored.RequestedAt.In(location)
	startedAt := stored.StartedAt.In(location)
	finishedAt := stored.FinishedAt.In(location)
	stored.RequestedAt = requestedAt
	stored.StartedAt = &startedAt
	stored.FinishedAt = &finishedAt
	plans := &recordingNetworkReleasePlans{
		terminal: state.GlobalNetworkReleaseTerminalRecord{
			Operation: state.OperationRecord{
				Operation: stored,
				Revision:  started.Revision,
			},
			OwnerIdentity:            "authenticated-user",
			SourceCheckpointRevision: plan.CheckpointRevision,
			NetworkRevision:          plan.NetworkRevision,
		},
		terminalFound: true,
	}
	authority := newNetworkReleaseAuthority(plans, &recordingNetworkReleaseCoordinator{record: started}, func() (domain.OperationID, error) {
		return "operation-unused", nil
	})
	caller := control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}}
	if _, err := authority.StartNetworkRelease(t.Context(), caller, control.StartNetworkReleaseRequest{IntentID: started.Operation.IntentID}); err != nil {
		t.Fatalf("StartNetworkRelease() error = %v", err)
	}
}

// TestNetworkReleaseAuthorityConfirmsOwnershipAsTerminalOperation proves a successful confirmation returns the completed fence without requiring another start.
func TestNetworkReleaseAuthorityConfirmsOwnershipAsTerminalOperation(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseOwnership)
	completed, err := plan.Operation.Operation.Transition(domain.OperationSucceeded, "network released", plan.Operation.Operation.RequestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingNetworkReleaseCoordinator{
		confirmOwnershipResult: state.GlobalNetworkReleaseTerminalRecord{
			Operation: state.OperationRecord{
				Operation: completed,
				Revision:  plan.CheckpointRevision + 1,
			},
			OwnerIdentity:            "authenticated-user",
			SourceCheckpointRevision: plan.CheckpointRevision,
			NetworkRevision:          plan.NetworkRevision,
		},
	}
	authority := newNetworkReleaseAuthority(&recordingNetworkReleasePlans{}, coordinator, func() (domain.OperationID, error) {
		return "operation-unused", nil
	})
	result, err := authority.ConfirmNetworkReleaseOwnership(
		t.Context(),
		control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}},
		control.ConfirmNetworkReleaseOwnershipRequest{
			OperationID:                plan.Operation.Operation.ID,
			ExpectedCheckpointRevision: plan.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseOwnership() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || result.CheckpointRevision != plan.CheckpointRevision {
		t.Fatalf("ConfirmNetworkReleaseOwnership() = %#v", result)
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

// TestNetworkReleaseAuthorityHidesPlansFromOtherUsers prevents authenticated callers from probing another owner's release operation.
func TestNetworkReleaseAuthorityHidesPlansFromOtherUsers(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseLowPorts)
	setNetworkReleasePlanOwner(&plan, "owner-user")
	plans := &recordingNetworkReleasePlans{
		plan:  plan,
		found: true,
	}
	authority := newNetworkReleaseAuthority(
		plans,
		&recordingNetworkReleaseCoordinator{},
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)

	_, err := authority.ReadNetworkRelease(
		t.Context(),
		control.Caller{Transport: local.PeerIdentity{UserID: "other-user"}},
		control.ReadNetworkReleaseRequest{OperationID: "operation-release"},
	)
	if err == nil {
		t.Fatal("ReadNetworkRelease() unexpectedly succeeded")
	}
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeNotFound {
		t.Fatalf("ReadNetworkRelease() error = %#v, want not found", err)
	}
}

// TestNetworkReleaseAuthorityBindsCallerToLowPortApproval proves helper authority is selected solely by the transport identity.
func TestNetworkReleaseAuthorityBindsCallerToLowPortApproval(t *testing.T) {
	caller := control.Caller{
		Transport: local.PeerIdentity{UserID: "authenticated-user"},
	}
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareResult: validNetworkReleaseLowPortResult("operation-release"),
		confirmResult: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseResolver),
	}
	coordinator.confirmResult.CheckpointRevision = 13
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	prepare := control.PrepareNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 12,
	}
	if _, err := authority.PrepareNetworkReleaseApproval(t.Context(), caller, prepare); err != nil {
		t.Fatalf("PrepareNetworkReleaseApproval() error = %v", err)
	}
	confirm := control.ConfirmNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 12,
		LowPortEvidence:            validNetworkReleaseLowPortEvidence(),
	}
	result, err := authority.ConfirmNetworkReleaseApproval(t.Context(), caller, confirm)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseApproval() error = %v", err)
	}
	if coordinator.prepareRequest.RequesterIdentity != caller.Transport.UserID || coordinator.confirmRequest.RequesterIdentity != caller.Transport.UserID {
		t.Fatalf("approval requester identities = %q, %q", coordinator.prepareRequest.RequesterIdentity, coordinator.confirmRequest.RequesterIdentity)
	}
	if coordinator.prepareRequest.ExpectedCheckpointRevision != prepare.ExpectedCheckpointRevision || coordinator.confirmRequest.ExpectedCheckpointRevision != confirm.ExpectedCheckpointRevision {
		t.Fatalf("approval checkpoint requests = %#v, %#v", coordinator.prepareRequest, coordinator.confirmRequest)
	}
	if result.Phase != control.NetworkReleasePhaseResolver || result.CheckpointRevision != 13 {
		t.Fatalf("confirmation result = %#v", result)
	}
}

// TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredLowPortTickets keeps opaque helper output scoped to the selected operation and lifetime.
func TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredLowPortTickets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.LowPortResult)
	}{
		{
			name: "other operation",
			mutate: func(result *ticketissuer.LowPortResult) {
				result.OperationID = "operation-other"
			},
		},
		{
			name: "expired",
			mutate: func(result *ticketissuer.LowPortResult) {
				result.ExpiresAt = time.Now().UTC().Add(-time.Second)
			},
		},
		{
			name: "malformed",
			mutate: func(result *ticketissuer.LowPortResult) {
				result.PolicyFingerprint = "invalid"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validNetworkReleaseLowPortResult("operation-release")
			test.mutate(&result)
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{prepareResult: result},
				func() (domain.OperationID, error) { return "operation-generated", nil },
			)
			_, err := authority.PrepareNetworkReleaseApproval(
				t.Context(),
				control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}},
				control.PrepareNetworkReleaseApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
				},
			)
			if err == nil {
				t.Fatal("PrepareNetworkReleaseApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityRejectsUnadvancedLowPortConfirmation ensures confirmation cannot claim resolver release without advancing its exact checkpoint.
func TestNetworkReleaseAuthorityRejectsUnadvancedLowPortConfirmation(t *testing.T) {
	for _, test := range []struct {
		name string
		plan state.GlobalNetworkReleasePlanRecord
	}{
		{
			name: "operation",
			plan: validNetworkReleasePlan("operation-other", "intent-release", state.GlobalNetworkReleasePlanPhaseResolver),
		},
		{
			name: "checkpoint",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseResolver),
		},
		{
			name: "phase",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseLowPorts),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{confirmResult: test.plan},
				func() (domain.OperationID, error) { return "operation-generated", nil },
			)
			_, err := authority.ConfirmNetworkReleaseApproval(
				t.Context(),
				control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}},
				control.ConfirmNetworkReleaseApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
					LowPortEvidence:            validNetworkReleaseLowPortEvidence(),
				},
			)
			if err == nil {
				t.Fatal("ConfirmNetworkReleaseApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityMapsLowPortCheckpointConflicts preserves stale checkpoint semantics at both approval boundaries.
func TestNetworkReleaseAuthorityMapsLowPortCheckpointConflicts(t *testing.T) {
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		&recordingNetworkReleaseCoordinator{err: &state.StaleRevisionError{}},
		func() (domain.OperationID, error) { return "operation-generated", nil },
	)
	caller := control.Caller{
		Transport: local.PeerIdentity{UserID: "authenticated-user"},
	}
	for _, call := range []struct {
		name string
		call func() error
	}{
		{
			name: "prepare",
			call: func() error {
				_, err := authority.PrepareNetworkReleaseApproval(
					t.Context(),
					caller,
					control.PrepareNetworkReleaseApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
					},
				)
				return err
			},
		},
		{
			name: "confirm",
			call: func() error {
				_, err := authority.ConfirmNetworkReleaseApproval(
					t.Context(),
					caller,
					control.ConfirmNetworkReleaseApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
						LowPortEvidence:            validNetworkReleaseLowPortEvidence(),
					},
				)
				return err
			},
		},
	} {
		t.Run(call.name, func(t *testing.T) {
			err := call.call()
			if err == nil {
				t.Fatal("approval unexpectedly succeeded")
			}
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
				t.Fatalf("approval error = %#v, want conflict", err)
			}
		})
	}
}

// TestNetworkReleaseAuthorityBindsCallerToResolverApproval proves resolver helper authority is selected solely by the transport identity.
func TestNetworkReleaseAuthorityBindsCallerToResolverApproval(t *testing.T) {
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareResolverResult: validNetworkReleaseResolverResult("operation-release"),
		confirmResolverResult: validNetworkReleasePlan(
			"operation-release",
			"intent-release",
			state.GlobalNetworkReleasePlanPhaseTrust,
		),
	}
	coordinator.confirmResolverResult.CheckpointRevision = 13
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	prepare := control.PrepareNetworkReleaseResolverApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 12,
	}
	preparation, err := authority.PrepareNetworkReleaseResolverApproval(t.Context(), caller, prepare)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseResolverApproval() error = %v", err)
	}
	confirm := control.ConfirmNetworkReleaseResolverApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 12,
		ResolverEvidence:           validNetworkReleaseResolverEvidence(),
	}
	result, err := authority.ConfirmNetworkReleaseResolverApproval(t.Context(), caller, confirm)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseResolverApproval() error = %v", err)
	}
	if coordinator.prepareResolverRequest.RequesterIdentity != caller.Transport.UserID || coordinator.confirmResolverRequest.RequesterIdentity != caller.Transport.UserID {
		t.Fatalf("resolver approval requester identities = %q, %q", coordinator.prepareResolverRequest.RequesterIdentity, coordinator.confirmResolverRequest.RequesterIdentity)
	}
	if coordinator.prepareResolverRequest.ExpectedCheckpointRevision != prepare.ExpectedCheckpointRevision || coordinator.confirmResolverRequest.ExpectedCheckpointRevision != confirm.ExpectedCheckpointRevision {
		t.Fatalf("resolver approval checkpoint requests = %#v, %#v", coordinator.prepareResolverRequest, coordinator.confirmResolverRequest)
	}
	if preparation.PublicationDisposition != control.NetworkReleaseResolverPublicationDurable ||
		preparation.Ticket.OperationID != prepare.OperationID ||
		preparation.Ticket.Operation != helper.OperationReleaseResolver {
		t.Fatalf("resolver preparation = %#v", preparation)
	}
	if result.Phase != control.NetworkReleasePhaseTrust || result.CheckpointRevision != 13 {
		t.Fatalf("resolver confirmation result = %#v", result)
	}
}

// TestNetworkReleaseAuthorityReturnsIndeterminateResolverReference preserves the sole reference available after uncertain publication.
func TestNetworkReleaseAuthorityReturnsIndeterminateResolverReference(t *testing.T) {
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareResolverResult: validNetworkReleaseResolverResult("operation-release"),
		err:                   ticketissuer.ErrResolverPublicationIndeterminate,
	}
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseResolverApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseResolverApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseResolverApproval() error = %v", err)
	}
	if preparation.PublicationDisposition != control.NetworkReleaseResolverPublicationIndeterminate ||
		preparation.Ticket.Reference != coordinator.prepareResolverResult.Reference {
		t.Fatalf("preparation = %#v", preparation)
	}
}

// TestNetworkReleaseAuthorityRejectsUnadvancedResolverConfirmation ensures confirmation cannot claim trust release without advancing its exact checkpoint.
func TestNetworkReleaseAuthorityRejectsUnadvancedResolverConfirmation(t *testing.T) {
	for _, test := range []struct {
		name string
		plan state.GlobalNetworkReleasePlanRecord
	}{
		{
			name: "operation",
			plan: validNetworkReleasePlan("operation-other", "intent-release", state.GlobalNetworkReleasePlanPhaseTrust),
		},
		{
			name: "checkpoint",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseTrust),
		},
		{
			name: "phase",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseResolver),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{confirmResolverResult: test.plan},
				func() (domain.OperationID, error) { return "operation-generated", nil },
			)
			_, err := authority.ConfirmNetworkReleaseResolverApproval(
				t.Context(),
				control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}},
				control.ConfirmNetworkReleaseResolverApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
					ResolverEvidence:           validNetworkReleaseResolverEvidence(),
				},
			)
			if err == nil {
				t.Fatal("ConfirmNetworkReleaseResolverApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredResolverTickets keeps opaque helper output scoped to the selected operation and lifetime.
func TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredResolverTickets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.ResolverResult)
	}{
		{
			name: "other operation",
			mutate: func(result *ticketissuer.ResolverResult) {
				result.OperationID = "operation-other"
			},
		},
		{
			name: "expired",
			mutate: func(result *ticketissuer.ResolverResult) {
				result.ExpiresAt = time.Now().UTC().Add(-time.Second)
			},
		},
		{
			name: "malformed",
			mutate: func(result *ticketissuer.ResolverResult) {
				result.PolicyFingerprint = "invalid"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validNetworkReleaseResolverResult("operation-release")
			test.mutate(&result)
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{prepareResolverResult: result},
				func() (domain.OperationID, error) { return "operation-generated", nil },
			)
			_, err := authority.PrepareNetworkReleaseResolverApproval(
				t.Context(),
				control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}},
				control.PrepareNetworkReleaseResolverApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
				},
			)
			if err == nil {
				t.Fatal("PrepareNetworkReleaseResolverApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityMapsResolverCheckpointConflicts preserves stale checkpoint semantics at both resolver approval boundaries.
func TestNetworkReleaseAuthorityMapsResolverCheckpointConflicts(t *testing.T) {
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		&recordingNetworkReleaseCoordinator{err: &state.StaleRevisionError{}},
		func() (domain.OperationID, error) { return "operation-generated", nil },
	)
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	for _, call := range []struct {
		name string
		call func() error
	}{
		{
			name: "prepare",
			call: func() error {
				_, err := authority.PrepareNetworkReleaseResolverApproval(
					t.Context(),
					caller,
					control.PrepareNetworkReleaseResolverApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
					},
				)
				return err
			},
		},
		{
			name: "confirm",
			call: func() error {
				_, err := authority.ConfirmNetworkReleaseResolverApproval(
					t.Context(),
					caller,
					control.ConfirmNetworkReleaseResolverApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
						ResolverEvidence:           validNetworkReleaseResolverEvidence(),
					},
				)
				return err
			},
		},
	} {
		t.Run(call.name, func(t *testing.T) {
			err := call.call()
			if err == nil {
				t.Fatal("resolver approval unexpectedly succeeded")
			}
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
				t.Fatalf("resolver approval error = %#v, want conflict", err)
			}
		})
	}
}

// TestClassifyNetworkReleaseErrorMapsResolverPublicationIndeterminate preserves a retriable conflict when resolver capability publication has an unknown outcome.
func TestClassifyNetworkReleaseErrorMapsResolverPublicationIndeterminate(t *testing.T) {
	classified := classifyNetworkReleaseError(ticketissuer.ErrResolverPublicationIndeterminate)
	var handlerError *session.HandlerError
	if !errors.As(classified, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
		t.Fatalf("classifyNetworkReleaseError() = %#v, want conflict", classified)
	}
}

// TestNetworkReleaseAuthorityBindsCallerAndProjectsTrustApproval proves trust authority is scoped to the authenticated user and selected checkpoint.
func TestNetworkReleaseAuthorityBindsCallerAndProjectsTrustApproval(t *testing.T) {
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	evidence := validNetworkReleaseTrustEvidence()
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareTrustResult: reconcile.GlobalNetworkReleaseTrustPreparation{
			Disposition: state.GlobalNetworkReleaseTrustOwned,
			Ticket:      trustTicket(validNetworkReleaseTrustResult("operation-release")),
		},
		confirmTrustResult: validNetworkReleasePlan(
			"operation-release",
			"intent-release",
			state.GlobalNetworkReleasePlanPhaseLoopbacks,
		),
	}
	coordinator.confirmTrustResult.CheckpointRevision = 13
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		caller,
		control.PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseTrustApproval() error = %v", err)
	}
	result, err := authority.ConfirmNetworkReleaseTrustApproval(
		t.Context(),
		caller,
		control.ConfirmNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
			TrustEvidence:              &evidence,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseTrustApproval() error = %v", err)
	}
	if coordinator.prepareTrustRequest.RequesterIdentity != caller.Transport.UserID ||
		coordinator.confirmTrustRequest.RequesterIdentity != caller.Transport.UserID {
		t.Fatalf("trust approval requester identities = %q, %q", coordinator.prepareTrustRequest.RequesterIdentity, coordinator.confirmTrustRequest.RequesterIdentity)
	}
	if coordinator.prepareTrustRequest.OperationID != "operation-release" ||
		coordinator.prepareTrustRequest.ExpectedCheckpointRevision != 12 ||
		coordinator.confirmTrustRequest.OperationID != "operation-release" ||
		coordinator.confirmTrustRequest.ExpectedCheckpointRevision != 12 {
		t.Fatalf("trust approval requests = %#v, %#v", coordinator.prepareTrustRequest, coordinator.confirmTrustRequest)
	}
	if preparation.Disposition != control.NetworkReleaseTrustOwned ||
		preparation.PublicationDisposition != control.NetworkReleaseTrustPublicationDurable ||
		preparation.Ticket == nil {
		t.Fatalf("trust preparation = %#v", preparation)
	}
	if preparation.Ticket.OperationID != coordinator.prepareTrustResult.Ticket.OperationID ||
		preparation.Ticket.Reference != coordinator.prepareTrustResult.Ticket.Reference ||
		preparation.Ticket.Operation != coordinator.prepareTrustResult.Ticket.Operation ||
		preparation.Ticket.PolicyFingerprint != coordinator.prepareTrustResult.Ticket.PolicyFingerprint ||
		preparation.Ticket.TargetOwnershipFingerprint != coordinator.prepareTrustResult.Ticket.OwnershipFingerprint ||
		preparation.Ticket.AuthorityFingerprint != coordinator.prepareTrustResult.Ticket.AuthorityFingerprint ||
		preparation.Ticket.Mechanism != coordinator.prepareTrustResult.Ticket.Mechanism ||
		!preparation.Ticket.ExpiresAt.Equal(coordinator.prepareTrustResult.Ticket.ExpiresAt) {
		t.Fatalf("projected trust ticket = %#v, want %#v", preparation.Ticket, coordinator.prepareTrustResult.Ticket)
	}
	if coordinator.confirmTrustRequest.TrustEvidence != evidence {
		t.Fatalf("trust evidence = %#v, want %#v", coordinator.confirmTrustRequest.TrustEvidence, evidence)
	}
	if result.Phase != control.NetworkReleasePhaseLoopbacks || result.CheckpointRevision != 13 {
		t.Fatalf("trust confirmation result = %#v", result)
	}
}

// TestNetworkReleaseAuthorityReturnsIndeterminateTrustReference preserves the sole reference available after uncertain publication.
func TestNetworkReleaseAuthorityReturnsIndeterminateTrustReference(t *testing.T) {
	result := validNetworkReleaseTrustResult("operation-release")
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareTrustResult: reconcile.GlobalNetworkReleaseTrustPreparation{
			Disposition: state.GlobalNetworkReleaseTrustOwned,
			Ticket:      trustTicket(result),
		},
		err: ticketissuer.ErrTrustPublicationIndeterminate,
	}
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseTrustApproval() error = %v", err)
	}
	if preparation.PublicationDisposition != control.NetworkReleaseTrustPublicationIndeterminate ||
		preparation.Ticket == nil ||
		preparation.Ticket.Reference != result.Reference {
		t.Fatalf("trust preparation = %#v", preparation)
	}
}

// TestNetworkReleaseAuthorityProjectsPreexistingTrustWithoutTicket preserves unowned native trust without issuing removal authority.
func TestNetworkReleaseAuthorityProjectsPreexistingTrustWithoutTicket(t *testing.T) {
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		&recordingNetworkReleaseCoordinator{
			prepareTrustResult: reconcile.GlobalNetworkReleaseTrustPreparation{
				Disposition: state.GlobalNetworkReleaseTrustPreexistingUnowned,
			},
		},
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseTrustApproval() error = %v", err)
	}
	if preparation.Disposition != control.NetworkReleaseTrustPreexistingUnowned ||
		preparation.PublicationDisposition != control.NetworkReleaseTrustPublicationNotRequired ||
		preparation.Ticket != nil {
		t.Fatalf("trust preparation = %#v", preparation)
	}
}

// TestNetworkReleaseAuthorityRejectsIndeterminatePreexistingTrust prevents uncertain publication from being normalized into preservation.
func TestNetworkReleaseAuthorityRejectsIndeterminatePreexistingTrust(t *testing.T) {
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		&recordingNetworkReleaseCoordinator{
			prepareTrustResult: reconcile.GlobalNetworkReleaseTrustPreparation{
				Disposition: state.GlobalNetworkReleaseTrustPreexistingUnowned,
			},
			err: ticketissuer.ErrTrustPublicationIndeterminate,
		},
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	_, err := authority.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err == nil {
		t.Fatal("PrepareNetworkReleaseTrustApproval() unexpectedly succeeded")
	}
}

// TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredTrustTickets keeps opaque helper output scoped to the selected operation and lifetime.
func TestNetworkReleaseAuthorityRejectsUncorrelatedOrExpiredTrustTickets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.TrustResult)
	}{
		{
			name: "other operation",
			mutate: func(result *ticketissuer.TrustResult) {
				result.OperationID = "operation-other"
			},
		},
		{
			name: "expired",
			mutate: func(result *ticketissuer.TrustResult) {
				result.ExpiresAt = time.Now().UTC().Add(-time.Second)
			},
		},
		{
			name: "malformed",
			mutate: func(result *ticketissuer.TrustResult) {
				result.AuthorityFingerprint = "invalid"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validNetworkReleaseTrustResult("operation-release")
			test.mutate(&result)
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{
					prepareTrustResult: reconcile.GlobalNetworkReleaseTrustPreparation{
						Disposition: state.GlobalNetworkReleaseTrustOwned,
						Ticket:      trustTicket(result),
					},
				},
				func() (domain.OperationID, error) {
					return "operation-generated", nil
				},
			)
			_, err := authority.PrepareNetworkReleaseTrustApproval(
				t.Context(),
				control.Caller{
					Transport: local.PeerIdentity{
						UserID: "authenticated-user",
					},
				},
				control.PrepareNetworkReleaseTrustApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
				},
			)
			if err == nil {
				t.Fatal("PrepareNetworkReleaseTrustApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityForwardsNilTrustEvidenceAsZero keeps the optional control field compatible with reconciliation input.
func TestNetworkReleaseAuthorityForwardsNilTrustEvidenceAsZero(t *testing.T) {
	plan := validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseLoopbacks)
	plan.CheckpointRevision = 13
	coordinator := &recordingNetworkReleaseCoordinator{
		confirmTrustResult: plan,
	}
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	_, err := authority.ConfirmNetworkReleaseTrustApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.ConfirmNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseTrustApproval() error = %v", err)
	}
	if coordinator.confirmTrustRequest.TrustEvidence != (helper.TrustMutationEvidence{}) {
		t.Fatalf("trust evidence = %#v, want zero evidence", coordinator.confirmTrustRequest.TrustEvidence)
	}
}

// TestNetworkReleaseAuthorityRejectsUnadvancedTrustConfirmation ensures confirmation cannot claim loopback release without advancing its exact checkpoint.
func TestNetworkReleaseAuthorityRejectsUnadvancedTrustConfirmation(t *testing.T) {
	for _, test := range []struct {
		name string
		plan state.GlobalNetworkReleasePlanRecord
	}{
		{
			name: "operation",
			plan: validNetworkReleasePlan("operation-other", "intent-release", state.GlobalNetworkReleasePlanPhaseLoopbacks),
		},
		{
			name: "checkpoint",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseLoopbacks),
		},
		{
			name: "phase",
			plan: validNetworkReleasePlan("operation-release", "intent-release", state.GlobalNetworkReleasePlanPhaseTrust),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name != "checkpoint" {
				test.plan.CheckpointRevision = 13
			}
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{
					confirmTrustResult: test.plan,
				},
				func() (domain.OperationID, error) {
					return "operation-generated", nil
				},
			)
			_, err := authority.ConfirmNetworkReleaseTrustApproval(
				t.Context(),
				control.Caller{
					Transport: local.PeerIdentity{
						UserID: "authenticated-user",
					},
				},
				control.ConfirmNetworkReleaseTrustApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
				},
			)
			if err == nil {
				t.Fatal("ConfirmNetworkReleaseTrustApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityMapsTrustCheckpointConflicts preserves stale checkpoint semantics at both trust approval boundaries.
func TestNetworkReleaseAuthorityMapsTrustCheckpointConflicts(t *testing.T) {
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		&recordingNetworkReleaseCoordinator{
			err: &state.StaleRevisionError{},
		},
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	for _, call := range []struct {
		name string
		call func() error
	}{
		{
			name: "prepare",
			call: func() error {
				_, err := authority.PrepareNetworkReleaseTrustApproval(
					t.Context(),
					caller,
					control.PrepareNetworkReleaseTrustApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
					},
				)
				return err
			},
		},
		{
			name: "confirm",
			call: func() error {
				_, err := authority.ConfirmNetworkReleaseTrustApproval(
					t.Context(),
					caller,
					control.ConfirmNetworkReleaseTrustApprovalRequest{
						OperationID:                "operation-release",
						ExpectedCheckpointRevision: 12,
					},
				)
				return err
			},
		},
	} {
		t.Run(call.name, func(t *testing.T) {
			err := call.call()
			var handlerError *session.HandlerError
			if err == nil || !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
				t.Fatalf("trust approval error = %#v, want conflict", err)
			}
		})
	}
}

// TestClassifyNetworkReleaseErrorMapsTrustPublicationIndeterminate preserves a retriable conflict when trust capability publication has an unknown outcome.
func TestClassifyNetworkReleaseErrorMapsTrustPublicationIndeterminate(t *testing.T) {
	classified := classifyNetworkReleaseError(ticketissuer.ErrTrustPublicationIndeterminate)
	var handlerError *session.HandlerError
	if !errors.As(classified, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
		t.Fatalf("classifyNetworkReleaseError() = %#v, want conflict", classified)
	}
}

// TestNetworkReleaseAuthorityBindsCallerAndProjectsLoopbackApproval proves loopback authority is scoped to the authenticated user and selected checkpoint.
func TestNetworkReleaseAuthorityBindsCallerAndProjectsLoopbackApproval(t *testing.T) {
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	evidence := validNetworkReleaseLoopbackEvidence()
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareLoopbacksResult: validNetworkReleaseLoopbackResult("operation-release"),
		confirmLoopbacksResult: validNetworkReleasePlan(
			"operation-release",
			"intent-release",
			state.GlobalNetworkReleasePlanPhaseOwnership,
		),
	}
	coordinator.confirmLoopbacksResult.CheckpointRevision = 13
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseLoopbackApproval(
		t.Context(),
		caller,
		control.PrepareNetworkReleaseLoopbackApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseLoopbackApproval() error = %v", err)
	}
	result, err := authority.ConfirmNetworkReleaseLoopbackApproval(
		t.Context(),
		caller,
		control.ConfirmNetworkReleaseLoopbackApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
			LoopbackEvidence:           evidence,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseLoopbackApproval() error = %v", err)
	}
	if coordinator.prepareLoopbacksRequest.RequesterIdentity != caller.Transport.UserID ||
		coordinator.confirmLoopbacksRequest.RequesterIdentity != caller.Transport.UserID {
		t.Fatalf("loopback requester identities = %q, %q", coordinator.prepareLoopbacksRequest.RequesterIdentity, coordinator.confirmLoopbacksRequest.RequesterIdentity)
	}
	if coordinator.prepareLoopbacksRequest.OperationID != "operation-release" ||
		coordinator.prepareLoopbacksRequest.ExpectedCheckpointRevision != 12 ||
		coordinator.confirmLoopbacksRequest.OperationID != "operation-release" ||
		coordinator.confirmLoopbacksRequest.ExpectedCheckpointRevision != 12 {
		t.Fatalf("loopback approval requests = %#v, %#v", coordinator.prepareLoopbacksRequest, coordinator.confirmLoopbacksRequest)
	}
	if preparation.OperationID != "operation-release" ||
		preparation.CheckpointRevision != 12 ||
		preparation.PublicationDisposition != control.NetworkReleaseLoopbackPublicationDurable {
		t.Fatalf("loopback preparation = %#v", preparation)
	}
	if preparation.Ticket.OperationID != coordinator.prepareLoopbacksResult.OperationID ||
		preparation.Ticket.Reference != coordinator.prepareLoopbacksResult.Reference ||
		preparation.Ticket.Operation != coordinator.prepareLoopbacksResult.Operation ||
		preparation.Ticket.Pool != coordinator.prepareLoopbacksResult.Pool.String() ||
		!preparation.Ticket.ExpiresAt.Equal(coordinator.prepareLoopbacksResult.ExpiresAt) {
		t.Fatalf("projected loopback ticket = %#v, want %#v", preparation.Ticket, coordinator.prepareLoopbacksResult)
	}
	if coordinator.confirmLoopbacksRequest.LoopbackEvidence.Pool != evidence.Pool ||
		len(coordinator.confirmLoopbacksRequest.LoopbackEvidence.Identities) != len(evidence.Identities) ||
		result.Phase != control.NetworkReleasePhaseOwnership ||
		result.CheckpointRevision != 13 {
		t.Fatalf("loopback confirmation = %#v, result = %#v", coordinator.confirmLoopbacksRequest, result)
	}
}

// TestNetworkReleaseAuthorityReturnsIndeterminateLoopbackReference preserves the sole reference available after uncertain publication.
func TestNetworkReleaseAuthorityReturnsIndeterminateLoopbackReference(t *testing.T) {
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareLoopbacksResult: validNetworkReleaseLoopbackResult("operation-release"),
		err:                    ticketissuer.ErrPoolPublicationIndeterminate,
	}
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseLoopbackApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseLoopbackApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseLoopbackApproval() error = %v", err)
	}
	if preparation.PublicationDisposition != control.NetworkReleaseLoopbackPublicationIndeterminate ||
		preparation.Ticket.Reference != coordinator.prepareLoopbacksResult.Reference {
		t.Fatalf("loopback preparation = %#v", preparation)
	}
}

// TestNetworkReleaseAuthorityRejectsInvalidLoopbackApprovalResults prevents malformed or uncorrelated capability metadata from reaching callers.
func TestNetworkReleaseAuthorityRejectsInvalidLoopbackApprovalResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.PoolResult)
	}{
		{
			name: "operation",
			mutate: func(result *ticketissuer.PoolResult) {
				result.OperationID = "operation-other"
			},
		},
		{
			name: "wrong helper operation",
			mutate: func(result *ticketissuer.PoolResult) {
				result.Operation = helper.OperationEnsureLoopbackPool
			},
		},
		{
			name: "expired",
			mutate: func(result *ticketissuer.PoolResult) {
				result.ExpiresAt = time.Now().UTC().Add(-time.Second)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := validNetworkReleaseLoopbackResult("operation-release")
			test.mutate(&result)
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{
					prepareLoopbacksResult: result,
				},
				func() (domain.OperationID, error) {
					return "operation-generated", nil
				},
			)
			if _, err := authority.PrepareNetworkReleaseLoopbackApproval(
				t.Context(),
				control.Caller{
					Transport: local.PeerIdentity{
						UserID: "authenticated-user",
					},
				},
				control.PrepareNetworkReleaseLoopbackApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
				},
			); err == nil {
				t.Fatal("PrepareNetworkReleaseLoopbackApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityRejectsInvalidLoopbackConfirmations ensures confirmation advances only
// the selected checkpoint into ownership after effect verification.
func TestNetworkReleaseAuthorityRejectsInvalidLoopbackConfirmations(t *testing.T) {
	for _, test := range []struct {
		name string
		plan state.GlobalNetworkReleasePlanRecord
	}{
		{
			name: "operation",
			plan: validNetworkReleasePlan(
				"operation-other",
				"intent-release",
				state.GlobalNetworkReleasePlanPhaseOwnership,
			),
		},
		{
			name: "checkpoint",
			plan: validNetworkReleasePlan(
				"operation-release",
				"intent-release",
				state.GlobalNetworkReleasePlanPhaseOwnership,
			),
		},
		{
			name: "phase",
			plan: validNetworkReleasePlan(
				"operation-release",
				"intent-release",
				state.GlobalNetworkReleasePlanPhaseVerifyEffects,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name != "checkpoint" {
				test.plan.CheckpointRevision = 13
			}
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{
					confirmLoopbacksResult: test.plan,
				},
				func() (domain.OperationID, error) {
					return "operation-generated", nil
				},
			)
			if _, err := authority.ConfirmNetworkReleaseLoopbackApproval(
				t.Context(),
				control.Caller{
					Transport: local.PeerIdentity{
						UserID: "authenticated-user",
					},
				},
				control.ConfirmNetworkReleaseLoopbackApprovalRequest{
					OperationID:                "operation-release",
					ExpectedCheckpointRevision: 12,
					LoopbackEvidence:           validNetworkReleaseLoopbackEvidence(),
				},
			); err == nil {
				t.Fatal("ConfirmNetworkReleaseLoopbackApproval() unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkReleaseAuthorityMapsLoopbackApprovalErrors preserves cancellation and contention classifications at both loopback approval boundaries.
func TestNetworkReleaseAuthorityMapsLoopbackApprovalErrors(t *testing.T) {
	caller := control.Caller{
		Transport: local.PeerIdentity{
			UserID: "authenticated-user",
		},
	}
	for _, test := range []struct {
		name string
		err  error
		code rpc.ErrorCode
	}{
		{
			name: "stale",
			err:  &state.StaleRevisionError{},
			code: rpc.ErrorCodeConflict,
		},
		{
			name: "indeterminate",
			err:  ticketissuer.ErrPoolPublicationIndeterminate,
			code: rpc.ErrorCodeConflict,
		},
		{
			name: "canceled",
			err:  context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newNetworkReleaseAuthority(
				&recordingNetworkReleasePlans{},
				&recordingNetworkReleaseCoordinator{
					err: test.err,
				},
				func() (domain.OperationID, error) {
					return "operation-generated", nil
				},
			)
			for _, call := range []func() error{
				func() error {
					_, err := authority.PrepareNetworkReleaseLoopbackApproval(
						t.Context(),
						caller,
						control.PrepareNetworkReleaseLoopbackApprovalRequest{
							OperationID:                "operation-release",
							ExpectedCheckpointRevision: 12,
						},
					)
					return err
				},
				func() error {
					_, err := authority.ConfirmNetworkReleaseLoopbackApproval(
						t.Context(),
						caller,
						control.ConfirmNetworkReleaseLoopbackApprovalRequest{
							OperationID:                "operation-release",
							ExpectedCheckpointRevision: 12,
							LoopbackEvidence:           validNetworkReleaseLoopbackEvidence(),
						},
					)
					return err
				},
			} {
				err := call()
				if test.err == context.Canceled {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("loopback approval error = %v, want cancellation", err)
					}
					continue
				}
				var handlerError *session.HandlerError
				if !errors.As(err, &handlerError) || handlerError.Code() != test.code {
					t.Fatalf("loopback approval error = %#v, want %s", err, test.code)
				}
			}
		})
	}
}

// TestNetworkReleaseAuthorityReturnsZeroLoopbackResultsForOrdinaryErrors prevents partially returned authority from escaping an unsuccessful coordinator call.
func TestNetworkReleaseAuthorityReturnsZeroLoopbackResultsForOrdinaryErrors(t *testing.T) {
	coordinator := &recordingNetworkReleaseCoordinator{
		prepareLoopbacksResult: validNetworkReleaseLoopbackResult("operation-release"),
		confirmLoopbacksResult: validNetworkReleasePlan(
			"operation-release",
			"intent-release",
			state.GlobalNetworkReleasePlanPhaseVerifyEffects,
		),
		err: errors.New("publisher unavailable"),
	}
	authority := newNetworkReleaseAuthority(
		&recordingNetworkReleasePlans{},
		coordinator,
		func() (domain.OperationID, error) {
			return "operation-generated", nil
		},
	)
	preparation, err := authority.PrepareNetworkReleaseLoopbackApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.PrepareNetworkReleaseLoopbackApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
		},
	)
	if err == nil || preparation != (control.NetworkReleaseLoopbackApprovalPreparation{}) {
		t.Fatalf("PrepareNetworkReleaseLoopbackApproval() = %#v, %v; want zero result and error", preparation, err)
	}
	result, err := authority.ConfirmNetworkReleaseLoopbackApproval(
		t.Context(),
		control.Caller{
			Transport: local.PeerIdentity{
				UserID: "authenticated-user",
			},
		},
		control.ConfirmNetworkReleaseLoopbackApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 12,
			LoopbackEvidence:           validNetworkReleaseLoopbackEvidence(),
		},
	)
	if err == nil || result != (control.NetworkReleaseOperation{}) {
		t.Fatalf("ConfirmNetworkReleaseLoopbackApproval() = %#v, %v; want zero result and error", result, err)
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

// setNetworkReleasePlanOwner sets the retained plan owner used by the authority access boundary.
func setNetworkReleasePlanOwner(plan *state.GlobalNetworkReleasePlanRecord, owner string) {
	plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity = owner
}

// validNetworkReleaseLowPortResult constructs one unexpired release-low-ports ticket result.
func validNetworkReleaseLowPortResult(operationID domain.OperationID) ticketissuer.LowPortResult {
	return ticketissuer.LowPortResult{
		OperationID:            operationID,
		Reference:              helper.TicketReference(strings.Repeat("a", 64)),
		Operation:              helper.OperationReleaseLowPorts,
		PolicyFingerprint:      strings.Repeat("b", 64),
		OwnershipFingerprint:   strings.Repeat("c", 64),
		ObservationFingerprint: strings.Repeat("d", 64),
		ExpiresAt:              time.Now().UTC().Add(time.Minute),
	}
}

// validNetworkReleaseResolverResult constructs one unexpired release-resolver ticket result.
func validNetworkReleaseResolverResult(operationID domain.OperationID) ticketissuer.ResolverResult {
	return ticketissuer.ResolverResult{
		OperationID:          operationID,
		Reference:            helper.TicketReference(strings.Repeat("e", 64)),
		Operation:            helper.OperationReleaseResolver,
		PolicyFingerprint:    strings.Repeat("b", 64),
		OwnershipFingerprint: strings.Repeat("c", 64),
		ExpiresAt:            time.Now().UTC().Add(time.Minute),
	}
}

// validNetworkReleaseTrustResult constructs one unexpired release-trust ticket result.
func validNetworkReleaseTrustResult(operationID domain.OperationID) ticketissuer.TrustResult {
	return ticketissuer.TrustResult{
		OperationID:          operationID,
		Reference:            helper.TicketReference(strings.Repeat("f", 64)),
		Operation:            helper.OperationReleaseTrust,
		PolicyFingerprint:    strings.Repeat("b", 64),
		OwnershipFingerprint: strings.Repeat("c", 64),
		AuthorityFingerprint: strings.Repeat("d", 64),
		Mechanism:            networkpolicy.UbuntuSystemTrust,
		ExpiresAt:            time.Now().UTC().Add(time.Minute),
	}
}

// trustTicket returns a distinct ticket pointer so coordinator fixtures retain the exact scripted result.
func trustTicket(result ticketissuer.TrustResult) *ticketissuer.TrustResult {
	return &result
}

// validNetworkReleaseLowPortEvidence constructs one exact owned-absent release receipt.
func validNetworkReleaseLowPortEvidence() helper.LowPortMutationEvidence {
	return helper.LowPortMutationEvidence{
		PolicyFingerprint:      strings.Repeat("b", 64),
		OwnershipFingerprint:   strings.Repeat("c", 64),
		ObservationFingerprint: strings.Repeat("d", 64),
		Postcondition:          helper.LowPortPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseResolverEvidence constructs one exact owned-absent release receipt.
func validNetworkReleaseResolverEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		PolicyFingerprint:      strings.Repeat("b", 64),
		OwnershipFingerprint:   strings.Repeat("c", 64),
		ObservationFingerprint: strings.Repeat("d", 64),
		Postcondition:          helper.ResolverPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseTrustEvidence constructs one exact owned-absent release receipt.
func validNetworkReleaseTrustEvidence() helper.TrustMutationEvidence {
	return helper.TrustMutationEvidence{
		AuthorityFingerprint:   strings.Repeat("d", 64),
		ObservationFingerprint: strings.Repeat("e", 64),
		Mechanism:              networkpolicy.UbuntuSystemTrust,
		Postcondition:          helper.TrustPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseLoopbackResult constructs one unexpired loopback-pool release ticket result.
func validNetworkReleaseLoopbackResult(operationID domain.OperationID) ticketissuer.PoolResult {
	return ticketissuer.PoolResult{
		OperationID: operationID,
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationReleaseLoopbackPool,
		Pool:        netip.MustParsePrefix("127.77.0.8/29"),
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
	}
}

// validNetworkReleaseLoopbackEvidence constructs a complete canonical owned-absent loopback-pool receipt.
func validNetworkReleaseLoopbackEvidence() helper.PoolMutationEvidence {
	pool := netip.MustParsePrefix("127.77.0.8/29")
	identities := make([]helper.MutationEvidence, 0, 8)
	address := pool.Addr()
	for range 8 {
		identities = append(identities, helper.MutationEvidence{
			Address: address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: strings.Repeat("b", 64),
			},
		})
		address = address.Next()
	}
	return helper.PoolMutationEvidence{
		Pool:       pool.String(),
		Identities: identities,
	}
}
