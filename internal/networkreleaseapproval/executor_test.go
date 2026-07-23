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
		control.NetworkReleasePhaseOwnership,
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

// releaseApprovalLauncher records every native helper invocation.
type releaseApprovalLauncher struct {
	lowPortCalls  int
	resolverCalls int
	trustCalls    int
	loopbackCalls int
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

// calls reports every native helper invocation across release phases.
func (helperLauncher *releaseApprovalLauncher) calls() int {
	return helperLauncher.lowPortCalls + helperLauncher.resolverCalls + helperLauncher.trustCalls + helperLauncher.loopbackCalls
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
