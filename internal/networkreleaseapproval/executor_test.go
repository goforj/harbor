package networkreleaseapproval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestRequestValidateRejectsNonHelperCheckpoints ensures no caller can request consent for coordinator-owned work.
func TestRequestValidateRejectsNonHelperCheckpoints(t *testing.T) {
	valid := Request{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
		Phase:                      control.NetworkReleasePhaseLowPorts,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for _, phase := range []control.NetworkReleasePhase{
		control.NetworkReleasePhaseRuntimeRelease,
		control.NetworkReleasePhaseVerifyEffects,
		control.NetworkReleasePhaseProjection,
	} {
		invalid := valid
		invalid.Phase = phase
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate() accepted non-helper phase %q", phase)
		}
	}
}

// TestExecuteDoesNotRedeemIndeterminatePublication prevents a possibly unpublished ticket from causing a native mutation.
func TestExecuteDoesNotRedeemIndeterminatePublication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		phase     control.NetworkReleasePhase
		configure func(*releaseApprovalClient)
	}{
		{
			name:  "resolver",
			phase: control.NetworkReleasePhaseResolver,
			configure: func(client *releaseApprovalClient) {
				client.resolver = releaseResolverPreparation(control.NetworkReleaseResolverPublicationIndeterminate)
			},
		},
		{
			name:  "trust",
			phase: control.NetworkReleasePhaseTrust,
			configure: func(client *releaseApprovalClient) {
				client.trust = releaseTrustPreparation(control.NetworkReleaseTrustPublicationIndeterminate)
			},
		},
		{
			name:  "loopback",
			phase: control.NetworkReleasePhaseLoopbacks,
			configure: func(client *releaseApprovalClient) {
				client.loopback = releaseLoopbackPreparation(control.NetworkReleaseLoopbackPublicationIndeterminate)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &releaseApprovalClient{}
			test.configure(client)
			helperLauncher := &releaseApprovalLauncher{}
			outcome, err := New(client, helperLauncher).Execute(context.Background(), Request{
				OperationID:                releaseApprovalOperationID,
				ExpectedCheckpointRevision: releaseApprovalRevision,
				Phase:                      test.phase,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome != (Outcome{State: Indeterminate}) {
				t.Fatalf("Execute() outcome = %#v, want indeterminate", outcome)
			}
			if helperLauncher.calls() != 0 || client.confirmCalls != 0 {
				t.Fatalf("launch/confirm calls = %d/%d, want 0/0", helperLauncher.calls(), client.confirmCalls)
			}
		})
	}
}

// TestExecuteOwnershipReconcilesIndeterminatePublication redeems the sole returned reference before permitting any later retry.
func TestExecuteOwnershipReconcilesIndeterminatePublication(t *testing.T) {
	t.Parallel()

	request := Request{
		OperationID:                releaseApprovalOperationID,
		ExpectedCheckpointRevision: releaseApprovalRevision,
		Phase:                      control.NetworkReleasePhaseOwnership,
	}
	preparation := releaseOwnershipPreparation(control.NetworkReleaseOwnershipPublicationIndeterminate)
	for _, test := range []struct {
		name         string
		launched     launcher.Outcome
		want         State
		confirmation bool
	}{
		{
			name:         "published",
			launched:     ownershipSuccessOutcome(preparation),
			want:         Succeeded,
			confirmation: true,
		},
		{
			name: "not proven",
			launched: launcher.Outcome{
				State: launcher.Unavailable,
			},
			want: Indeterminate,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &releaseApprovalClient{
				ownership: preparation,
				confirmed: releaseOwnershipTerminal(t),
			}
			helperLauncher := &releaseApprovalLauncher{
				ownershipOutcome: test.launched,
			}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), request)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome.State != test.want {
				t.Fatalf("Execute() state = %q, want %q", outcome.State, test.want)
			}
			if helperLauncher.ownershipCalls != 1 {
				t.Fatalf("ownership launch calls = %d, want 1", helperLauncher.ownershipCalls)
			}
			if got := client.confirmCalls == 1; got != test.confirmation {
				t.Fatalf("confirmation = %t, want %t", got, test.confirmation)
			}
		})
	}
}

const (
	releaseApprovalOperationID domain.OperationID = "operation-network-release"
	releaseApprovalRevision    domain.Sequence    = 8
)

var releaseApprovalExpiry = time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

// releaseApprovalClient supplies phase-specific preparations and records confirmations.
type releaseApprovalClient struct {
	resolver     control.NetworkReleaseResolverApprovalPreparation
	trust        control.NetworkReleaseTrustApprovalPreparation
	loopback     control.NetworkReleaseLoopbackApprovalPreparation
	ownership    control.NetworkReleaseOwnershipApprovalPreparation
	confirmed    control.NetworkReleaseOperation
	confirmCalls int
}

// PrepareNetworkReleaseApproval implements Client.
func (*releaseApprovalClient) PrepareNetworkReleaseApproval(context.Context, control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error) {
	return control.NetworkReleaseApprovalPreparation{}, nil
}

// ConfirmNetworkReleaseApproval implements Client.
func (client *releaseApprovalClient) ConfirmNetworkReleaseApproval(context.Context, control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error) {
	client.confirmCalls++
	return control.NetworkReleaseOperation{}, nil
}

// PrepareNetworkReleaseResolverApproval implements Client.
func (client *releaseApprovalClient) PrepareNetworkReleaseResolverApproval(context.Context, control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error) {
	return client.resolver, nil
}

// ConfirmNetworkReleaseResolverApproval implements Client.
func (client *releaseApprovalClient) ConfirmNetworkReleaseResolverApproval(context.Context, control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error) {
	client.confirmCalls++
	return control.NetworkReleaseOperation{}, nil
}

// PrepareNetworkReleaseTrustApproval implements Client.
func (client *releaseApprovalClient) PrepareNetworkReleaseTrustApproval(context.Context, control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error) {
	return client.trust, nil
}

// ConfirmNetworkReleaseTrustApproval implements Client.
func (client *releaseApprovalClient) ConfirmNetworkReleaseTrustApproval(context.Context, control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error) {
	client.confirmCalls++
	return control.NetworkReleaseOperation{}, nil
}

// PrepareNetworkReleaseLoopbackApproval implements Client.
func (client *releaseApprovalClient) PrepareNetworkReleaseLoopbackApproval(context.Context, control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error) {
	return client.loopback, nil
}

// ConfirmNetworkReleaseLoopbackApproval implements Client.
func (client *releaseApprovalClient) ConfirmNetworkReleaseLoopbackApproval(context.Context, control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error) {
	client.confirmCalls++
	return control.NetworkReleaseOperation{}, nil
}

// PrepareNetworkReleaseOwnershipApproval implements Client.
func (client *releaseApprovalClient) PrepareNetworkReleaseOwnershipApproval(context.Context, control.PrepareNetworkReleaseOwnershipApprovalRequest) (control.NetworkReleaseOwnershipApprovalPreparation, error) {
	return client.ownership, nil
}

// ConfirmNetworkReleaseOwnershipApproval implements Client.
func (client *releaseApprovalClient) ConfirmNetworkReleaseOwnershipApproval(context.Context, control.ConfirmNetworkReleaseOwnershipApprovalRequest) (control.NetworkReleaseOperation, error) {
	client.confirmCalls++
	return client.confirmed, nil
}

// releaseApprovalLauncher records every native helper invocation.
type releaseApprovalLauncher struct {
	lowPortCalls     int
	resolverCalls    int
	trustCalls       int
	loopbackCalls    int
	ownershipCalls   int
	ownershipOutcome launcher.Outcome
	ownershipErr     error
}

// InvokeLowPorts implements HelperLauncher.
func (helperLauncher *releaseApprovalLauncher) InvokeLowPorts(context.Context, launcher.LowPortLaunchTicket) (launcher.Outcome, error) {
	helperLauncher.lowPortCalls++
	return launcher.Outcome{}, nil
}

// InvokeResolver implements HelperLauncher.
func (helperLauncher *releaseApprovalLauncher) InvokeResolver(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error) {
	helperLauncher.resolverCalls++
	return launcher.Outcome{}, nil
}

// InvokeTrust implements HelperLauncher.
func (helperLauncher *releaseApprovalLauncher) InvokeTrust(context.Context, launcher.TrustLaunchTicket) (launcher.Outcome, error) {
	helperLauncher.trustCalls++
	return launcher.Outcome{}, nil
}

// InvokePool implements HelperLauncher.
func (helperLauncher *releaseApprovalLauncher) InvokePool(context.Context, launcher.PoolLaunchTicket) (launcher.Outcome, error) {
	helperLauncher.loopbackCalls++
	return launcher.Outcome{}, nil
}

// InvokeOwnership implements HelperLauncher.
func (helperLauncher *releaseApprovalLauncher) InvokeOwnership(context.Context, launcher.OwnershipLaunchTicket) (launcher.Outcome, error) {
	helperLauncher.ownershipCalls++
	return helperLauncher.ownershipOutcome, helperLauncher.ownershipErr
}

// calls reports every native helper invocation across release phases.
func (helperLauncher *releaseApprovalLauncher) calls() int {
	return helperLauncher.lowPortCalls + helperLauncher.resolverCalls + helperLauncher.trustCalls + helperLauncher.loopbackCalls + helperLauncher.ownershipCalls
}

// releaseResolverPreparation returns a valid resolver preparation with the requested publication state.
func releaseResolverPreparation(disposition control.NetworkReleaseResolverPublicationDisposition) control.NetworkReleaseResolverApprovalPreparation {
	return control.NetworkReleaseResolverApprovalPreparation{
		OperationID:            releaseApprovalOperationID,
		CheckpointRevision:     releaseApprovalRevision,
		PublicationDisposition: disposition,
		Ticket: control.NetworkReleaseResolverApprovalTicket{
			OperationID:                releaseApprovalOperationID,
			Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
			Operation:                  helper.OperationReleaseResolver,
			PolicyFingerprint:          strings.Repeat("b", 64),
			TargetOwnershipFingerprint: strings.Repeat("c", 64),
			ExpiresAt:                  releaseApprovalExpiry,
		},
	}
}

// releaseTrustPreparation returns a valid owned trust preparation with the requested publication state.
func releaseTrustPreparation(disposition control.NetworkReleaseTrustPublicationDisposition) control.NetworkReleaseTrustApprovalPreparation {
	return control.NetworkReleaseTrustApprovalPreparation{
		OperationID:            releaseApprovalOperationID,
		CheckpointRevision:     releaseApprovalRevision,
		Disposition:            control.NetworkReleaseTrustOwned,
		PublicationDisposition: disposition,
		Ticket: &control.NetworkReleaseTrustApprovalTicket{
			OperationID:                releaseApprovalOperationID,
			Reference:                  helper.TicketReference(strings.Repeat("d", 64)),
			Operation:                  helper.OperationReleaseTrust,
			PolicyFingerprint:          strings.Repeat("e", 64),
			TargetOwnershipFingerprint: strings.Repeat("f", 64),
			AuthorityFingerprint:       strings.Repeat("1", 64),
			Mechanism:                  networkpolicy.DarwinCurrentUserTrust,
			ExpiresAt:                  releaseApprovalExpiry,
		},
	}
}

// releaseLoopbackPreparation returns a valid loopback preparation with the requested publication state.
func releaseLoopbackPreparation(disposition control.NetworkReleaseLoopbackPublicationDisposition) control.NetworkReleaseLoopbackApprovalPreparation {
	return control.NetworkReleaseLoopbackApprovalPreparation{
		OperationID:            releaseApprovalOperationID,
		CheckpointRevision:     releaseApprovalRevision,
		PublicationDisposition: disposition,
		Ticket: control.NetworkReleaseLoopbackApprovalTicket{
			OperationID: releaseApprovalOperationID,
			Reference:   helper.TicketReference(strings.Repeat("2", 64)),
			Operation:   helper.OperationReleaseLoopbackPool,
			Pool:        "127.77.0.8/29",
			ExpiresAt:   releaseApprovalExpiry,
		},
	}
}

// TestExecuteOwnershipMapsEveryNativeConclusion keeps terminal release evidence behind one approval boundary.
func TestExecuteOwnershipMapsEveryNativeConclusion(t *testing.T) {
	t.Parallel()
	request := Request{
		OperationID:                releaseApprovalOperationID,
		ExpectedCheckpointRevision: releaseApprovalRevision,
		Phase:                      control.NetworkReleasePhaseOwnership,
	}
	preparation := releaseOwnershipPreparation(control.NetworkReleaseOwnershipPublicationDurable)
	terminal := releaseOwnershipTerminal(t)
	tests := []struct {
		name    string
		outcome launcher.Outcome
		err     error
		want    State
		confirm bool
	}{
		{
			name: "declined",
			outcome: launcher.Outcome{
				State: launcher.Declined,
			},
			want: Declined,
		},
		{
			name: "unavailable",
			outcome: launcher.Outcome{
				State: launcher.Unavailable,
			},
			want: Unavailable,
		},
		{
			name: "helper failure",
			outcome: launcher.Outcome{
				State: launcher.HelperFailed,
				Exit: &launcher.ProcessExit{
					Code: launcher.ExitCodeHelperFailed,
				},
				Response: ownershipFailureResponse(),
			},
			want: HelperFailed,
		},
		{
			name: "indeterminate",
			outcome: launcher.Outcome{
				State: launcher.Indeterminate,
			},
			want: Indeterminate,
		},
		{
			name:    "success",
			outcome: ownershipSuccessOutcome(preparation),
			want:    Succeeded,
			confirm: true,
		},
		{
			name: "launcher failure",
			err:  context.DeadlineExceeded,
			want: Indeterminate,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &releaseApprovalClient{
				ownership: preparation,
				confirmed: terminal,
			}
			helperLauncher := &releaseApprovalLauncher{
				ownershipOutcome: test.outcome,
				ownershipErr:     test.err,
			}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), request)
			if test.err != nil {
				if err == nil || outcome.State != Indeterminate {
					t.Fatalf("Execute() = %#v, %v", outcome, err)
				}
				return
			}
			if err != nil || outcome.State != test.want || (test.confirm != (client.confirmCalls == 1)) {
				t.Fatalf("Execute() = %#v, %v; confirmations = %d", outcome, err, client.confirmCalls)
			}
		})
	}
}

// releaseOwnershipPreparation returns one complete terminal ownership capability with the requested publication state.
func releaseOwnershipPreparation(disposition control.NetworkReleaseOwnershipPublicationDisposition) control.NetworkReleaseOwnershipApprovalPreparation {
	return control.NetworkReleaseOwnershipApprovalPreparation{
		OperationID:            releaseApprovalOperationID,
		CheckpointRevision:     releaseApprovalRevision,
		PublicationDisposition: disposition,
		Ticket: control.NetworkReleaseOwnershipApprovalTicket{
			OperationID:          releaseApprovalOperationID,
			OperationRevision:    releaseApprovalRevision - 1,
			CheckpointRevision:   releaseApprovalRevision,
			Reference:            helper.TicketReference(strings.Repeat("3", 64)),
			Operation:            helper.OperationReleaseNetworkOwnership,
			OwnershipFingerprint: strings.Repeat("4", 64),
			ExpiresAt:            releaseApprovalExpiry,
		},
	}
}

// releaseOwnershipTerminal returns the projection-retired terminal release operation.
func releaseOwnershipTerminal(t *testing.T) control.NetworkReleaseOperation {
	t.Helper()
	operation, err := domain.NewOperation(releaseApprovalOperationID, "intent-network-release", domain.OperationKindNetworkRelease, "", releaseApprovalExpiry)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "releasing network ownership", releaseApprovalExpiry, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationSucceeded, "network released", releaseApprovalExpiry.Add(time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	return control.NetworkReleaseOperation{
		Operation:          operation,
		Revision:           releaseApprovalRevision + 1,
		Phase:              control.NetworkReleasePhaseProjection,
		CheckpointRevision: releaseApprovalRevision,
		NetworkRevision:    1,
	}
}

// ownershipSuccessOutcome returns one helper result correlated to preparation.
func ownershipSuccessOutcome(preparation control.NetworkReleaseOwnershipApprovalPreparation) launcher.Outcome {
	return launcher.Outcome{
		State: launcher.Succeeded,
		Exit: &launcher.ProcessExit{
			Code: launcher.ExitCodeSucceeded,
		},
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation: helper.OperationReleaseNetworkOwnership,
				OwnershipEvidence: &helper.OwnershipMutationEvidence{
					ReleaseOperationID:           string(preparation.Ticket.OperationID),
					ReleaseOperationRevision:     uint64(preparation.Ticket.OperationRevision),
					ReleaseCheckpointRevision:    uint64(preparation.Ticket.CheckpointRevision),
					ReleasedOwnershipFingerprint: preparation.Ticket.OwnershipFingerprint,
					Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
				},
			},
		},
	}
}

// ownershipFailureResponse returns one bounded helper failure.
func ownershipFailureResponse() helper.Response {
	return helper.Response{
		Version: helper.ProtocolVersion,
		Error: &helper.ResponseError{
			Code:    helper.ErrorCodeAuthenticationFailed,
			Message: "ownership release denied",
		},
	}
}
