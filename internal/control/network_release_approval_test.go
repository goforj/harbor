package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// recordingNetworkReleaseApprovalAuthority records the narrow low-port and resolver approval calls made through a connected server.
type recordingNetworkReleaseApprovalAuthority struct {
	mu                    sync.Mutex
	preparation           NetworkReleaseApprovalPreparation
	resolverPreparation   NetworkReleaseResolverApprovalPreparation
	trustPreparation      NetworkReleaseTrustApprovalPreparation
	release               NetworkReleaseOperation
	resolverRelease       NetworkReleaseOperation
	trustRelease          NetworkReleaseOperation
	err                   error
	callers               []Caller
	prepares              []PrepareNetworkReleaseApprovalRequest
	confirmations         []ConfirmNetworkReleaseApprovalRequest
	resolverPrepares      []PrepareNetworkReleaseResolverApprovalRequest
	resolverConfirmations []ConfirmNetworkReleaseResolverApprovalRequest
	trustPrepares         []PrepareNetworkReleaseTrustApprovalRequest
	trustConfirmations    []ConfirmNetworkReleaseTrustApprovalRequest
}

// PrepareNetworkReleaseApproval records the authenticated caller and returns the configured preparation.
func (authority *recordingNetworkReleaseApprovalAuthority) PrepareNetworkReleaseApproval(
	_ context.Context,
	caller Caller,
	request PrepareNetworkReleaseApprovalRequest,
) (NetworkReleaseApprovalPreparation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.prepares = append(authority.prepares, request)
	return authority.preparation, authority.err
}

// ConfirmNetworkReleaseApproval records the authenticated caller and returns the configured release projection.
func (authority *recordingNetworkReleaseApprovalAuthority) ConfirmNetworkReleaseApproval(
	_ context.Context,
	caller Caller,
	request ConfirmNetworkReleaseApprovalRequest,
) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.confirmations = append(authority.confirmations, request)
	return authority.release, authority.err
}

// PrepareNetworkReleaseResolverApproval records the authenticated caller and returns the configured resolver preparation.
func (authority *recordingNetworkReleaseApprovalAuthority) PrepareNetworkReleaseResolverApproval(
	_ context.Context,
	caller Caller,
	request PrepareNetworkReleaseResolverApprovalRequest,
) (NetworkReleaseResolverApprovalPreparation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.resolverPrepares = append(authority.resolverPrepares, request)
	return authority.resolverPreparation, authority.err
}

// ConfirmNetworkReleaseResolverApproval records the authenticated caller and returns the configured resolver release projection.
func (authority *recordingNetworkReleaseApprovalAuthority) ConfirmNetworkReleaseResolverApproval(
	_ context.Context,
	caller Caller,
	request ConfirmNetworkReleaseResolverApprovalRequest,
) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.resolverConfirmations = append(authority.resolverConfirmations, request)
	return authority.resolverRelease, authority.err
}

// PrepareNetworkReleaseTrustApproval records the authenticated caller and returns the configured trust preparation.
func (authority *recordingNetworkReleaseApprovalAuthority) PrepareNetworkReleaseTrustApproval(
	_ context.Context,
	caller Caller,
	request PrepareNetworkReleaseTrustApprovalRequest,
) (NetworkReleaseTrustApprovalPreparation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.trustPrepares = append(authority.trustPrepares, request)
	return authority.trustPreparation, authority.err
}

// ConfirmNetworkReleaseTrustApproval records the authenticated caller and returns the configured trust release projection.
func (authority *recordingNetworkReleaseApprovalAuthority) ConfirmNetworkReleaseTrustApproval(
	_ context.Context,
	caller Caller,
	request ConfirmNetworkReleaseTrustApprovalRequest,
) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.trustConfirmations = append(authority.trustConfirmations, request)
	return authority.trustRelease, authority.err
}

// TestNetworkReleaseApprovalStableProtocolNames fixes the reviewed capability and method identities.
func TestNetworkReleaseApprovalStableProtocolNames(t *testing.T) {
	if CapabilityNetworkReleaseApprovalV1 != "control.network-release-approval.v1" {
		t.Fatalf("capability = %q", CapabilityNetworkReleaseApprovalV1)
	}
	if CapabilityNetworkReleaseResolverApprovalV1 != "control.network-release-resolver-approval.v1" {
		t.Fatalf("resolver capability = %q", CapabilityNetworkReleaseResolverApprovalV1)
	}
	if CapabilityNetworkReleaseTrustApprovalV1 != "control.network-release-trust-approval.v1" {
		t.Fatalf("trust capability = %q", CapabilityNetworkReleaseTrustApprovalV1)
	}
	if methodNetworkReleaseLowPortPrepare != "control.v1.network.release.low-port.prepare" ||
		methodNetworkReleaseLowPortConfirm != "control.v1.network.release.low-port.confirm" ||
		methodNetworkReleaseResolverPrepare != "control.v1.network.release.resolver.prepare" ||
		methodNetworkReleaseResolverConfirm != "control.v1.network.release.resolver.confirm" ||
		methodNetworkReleaseTrustPrepare != "control.v1.network.release.trust.prepare" ||
		methodNetworkReleaseTrustConfirm != "control.v1.network.release.trust.confirm" {
		t.Fatalf(
			"methods = %q, %q, %q, %q, %q, %q",
			methodNetworkReleaseLowPortPrepare,
			methodNetworkReleaseLowPortConfirm,
			methodNetworkReleaseResolverPrepare,
			methodNetworkReleaseResolverConfirm,
			methodNetworkReleaseTrustPrepare,
			methodNetworkReleaseTrustConfirm,
		)
	}
}

// TestNetworkReleaseResolverApprovalValidationAndCorrelation confines resolver release to owned-absent evidence and trust progress.
func TestNetworkReleaseResolverApprovalValidationAndCorrelation(t *testing.T) {
	preparation := validNetworkReleaseResolverApprovalPreparation()
	if err := preparation.Validate(); err != nil {
		t.Fatalf("preparation.Validate() error = %v", err)
	}
	confirmation := validNetworkReleaseResolverApprovalConfirmation()
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("confirmation.Validate() error = %v", err)
	}
	request := PrepareNetworkReleaseResolverApprovalRequest{
		OperationID:                preparation.OperationID,
		ExpectedCheckpointRevision: preparation.CheckpointRevision,
	}
	if err := validateNetworkReleaseResolverApprovalPreparationCorrelation(request, preparation); err != nil {
		t.Fatalf("validateNetworkReleaseResolverApprovalPreparationCorrelation() error = %v", err)
	}
	release := validNetworkReleaseApprovalRelease(t)
	release.Phase = NetworkReleasePhaseTrust
	if err := validateNetworkReleaseResolverApprovalConfirmationCorrelation(confirmation, release); err != nil {
		t.Fatalf("validateNetworkReleaseResolverApprovalConfirmationCorrelation() error = %v", err)
	}
	confirmation.ResolverEvidence.Postcondition = helper.ResolverPostconditionExact
	if err := confirmation.Validate(); err == nil {
		t.Fatal("confirmation.Validate() unexpectedly succeeded")
	}
	preparation.PublicationDisposition = "unknown"
	if err := preparation.Validate(); err == nil {
		t.Fatal("preparation.Validate() accepted an unknown publication disposition")
	}
	release.Phase = NetworkReleasePhaseResolver
	if err := validateNetworkReleaseResolverApprovalConfirmationCorrelation(validNetworkReleaseResolverApprovalConfirmation(), release); err == nil {
		t.Fatal("validateNetworkReleaseResolverApprovalConfirmationCorrelation() unexpectedly succeeded")
	}
}

// TestNetworkReleaseResolverApprovalDecodersRejectAmbiguousJSON keeps resolver authority bounded at every transport boundary.
func TestNetworkReleaseResolverApprovalDecodersRejectAmbiguousJSON(t *testing.T) {
	evidence, err := json.Marshal(validNetworkReleaseResolverApprovalEvidence())
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := json.Marshal(networkReleaseResolverApprovalPreparationResponse{
		Preparation: validNetworkReleaseResolverApprovalPreparation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"operation_id":"operation-release"}`,
		`{"operation_id":"operation-release","operation_id":"operation-release","expected_checkpoint_revision":6}`,
		`{"operation_id":"operation-release","expected_checkpoint_revision":6,"extra":true}`,
		`{"operation_id":"operation-release","expected_checkpoint_revision":6}{}`,
	} {
		if _, err := decodePrepareNetworkReleaseResolverApprovalRequest([]byte(payload)); err == nil {
			t.Fatalf("decodePrepareNetworkReleaseResolverApprovalRequest(%q) unexpectedly succeeded", payload)
		}
	}
	for _, payload := range []string{
		`{"operation_id":"operation-release","expected_checkpoint_revision":6}`,
		`{"operation_id":"operation-release","expected_checkpoint_revision":6,"resolver_evidence":` + string(evidence) + `,"resolver_evidence":` + string(evidence) + `}`,
		`{"operation_id":"operation-release","expected_checkpoint_revision":6,"resolver_evidence":` + string(evidence) + `,"extra":true}`,
		`{"operation_id":"operation-release","expected_checkpoint_revision":6,"resolver_evidence":` + string(evidence) + `}{}`,
	} {
		if _, err := decodeConfirmNetworkReleaseResolverApprovalRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeConfirmNetworkReleaseResolverApprovalRequest(%q) unexpectedly succeeded", payload)
		}
	}
	if err := decodeNetworkReleaseResolverApprovalPreparationResponse(preparation, &networkReleaseResolverApprovalPreparationResponse{}); err != nil {
		t.Fatalf("decodeNetworkReleaseResolverApprovalPreparationResponse() error = %v", err)
	}
	unknownTicketField := strings.Replace(
		string(preparation),
		`"expires_at"`,
		`"unexpected":true,"expires_at"`,
		1,
	)
	if err := decodeNetworkReleaseResolverApprovalPreparationResponse([]byte(unknownTicketField), &networkReleaseResolverApprovalPreparationResponse{}); err == nil {
		t.Fatal("decodeNetworkReleaseResolverApprovalPreparationResponse() unexpectedly accepted an unknown ticket field")
	}
}

// TestNetworkReleaseApprovalValidation confines every public value to release-low-ports and owned-absent evidence.
func TestNetworkReleaseApprovalValidation(t *testing.T) {
	preparation := validNetworkReleaseApprovalPreparation()
	confirmation := validNetworkReleaseApprovalConfirmation()
	for _, test := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "prepare request",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					OperationID:                preparation.OperationID,
					ExpectedCheckpointRevision: preparation.CheckpointRevision,
				}).Validate()
			},
		},
		{
			name:     "ticket",
			validate: preparation.Ticket.Validate,
		},
		{
			name:     "preparation",
			validate: preparation.Validate,
		},
		{
			name:     "confirmation",
			validate: confirmation.Validate,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
	for _, test := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "empty operation",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					ExpectedCheckpointRevision: 6,
				}).Validate()
			},
		},
		{
			name: "zero checkpoint",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					OperationID: preparation.OperationID,
				}).Validate()
			},
		},
		{
			name: "wrong ticket operation",
			validate: func() error {
				value := preparation.Ticket
				value.Operation = helper.OperationEnsureLowPorts
				return value.Validate()
			},
		},
		{
			name: "ticket non UTC expiry",
			validate: func() error {
				value := preparation.Ticket
				value.ExpiresAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("local", 3600))
				return value.Validate()
			},
		},
		{
			name: "ticket operation mismatch",
			validate: func() error {
				value := preparation
				value.Ticket.OperationID = "other"
				return value.Validate()
			},
		},
		{
			name: "exact evidence",
			validate: func() error {
				value := confirmation
				value.LowPortEvidence.Postcondition = helper.LowPortPostconditionExact
				return value.Validate()
			},
		},
		{
			name: "bad evidence fingerprint",
			validate: func() error {
				value := confirmation
				value.LowPortEvidence.PolicyFingerprint = "bad"
				return value.Validate()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil {
				t.Fatal("Validate() accepted invalid approval data")
			}
		})
	}
}

// TestNetworkReleaseApprovalCapabilityIsIndependent keeps approval negotiation independent from start/read and rejects typed nil authorities.
func TestNetworkReleaseApprovalCapabilityIsIndependent(t *testing.T) {
	if containsCapability(daemonCapabilities(false, true, false), CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("release start/read enabled approval")
	}
	if !containsCapability(daemonCapabilities(false, false, true), CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("approval was not independently enabled")
	}
	if !containsCapability(daemonCapabilities(false, false, true), CapabilityNetworkReleaseResolverApprovalV1) {
		t.Fatal("resolver approval was not independently enabled")
	}
	if !containsCapability(daemonCapabilities(false, false, true), CapabilityNetworkReleaseTrustApprovalV1) {
		t.Fatal("trust approval was not independently enabled")
	}
	for _, authority := range []NetworkReleaseApprovalAuthority{
		nil,
		(*recordingNetworkReleaseApprovalAuthority)(nil),
	} {
		if !networkReleaseApprovalAuthorityIsNil(authority) {
			t.Fatalf("nil approval authority %T was enabled", authority)
		}
	}
}

// TestNetworkReleaseApprovalConnectedCalls proves exact caller propagation and selection correlation on all approval methods.
func TestNetworkReleaseApprovalConnectedCalls(t *testing.T) {
	preparation := validNetworkReleaseApprovalPreparation()
	release := validNetworkReleaseApprovalRelease(t)
	resolverPreparation := validNetworkReleaseResolverApprovalPreparation()
	resolverRelease := release
	resolverRelease.Phase = NetworkReleasePhaseTrust
	trustTicket := validNetworkReleaseTrustApprovalTicket()
	trustPreparation := NetworkReleaseTrustApprovalPreparation{
		OperationID:            trustTicket.OperationID,
		CheckpointRevision:     7,
		Disposition:            NetworkReleaseTrustOwned,
		PublicationDisposition: NetworkReleaseTrustPublicationDurable,
		Ticket:                 &trustTicket,
	}
	trustRelease := release
	trustRelease.Operation.ID = trustPreparation.OperationID
	trustRelease.Phase = NetworkReleasePhaseLoopbacks
	trustRelease.CheckpointRevision = trustPreparation.CheckpointRevision + 1
	authority := &recordingNetworkReleaseApprovalAuthority{
		preparation:         preparation,
		resolverPreparation: resolverPreparation,
		release:             release,
		resolverRelease:     resolverRelease,
		trustPreparation:    trustPreparation,
		trustRelease:        trustRelease,
	}
	running := newNetworkReleaseApprovalRunningClient(t, authority)
	if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("approval capability was not negotiated")
	}
	if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseResolverApprovalV1) {
		t.Fatal("resolver approval capability was not negotiated")
	}
	if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseTrustApprovalV1) {
		t.Fatal("trust approval capability was not negotiated")
	}
	prepared, err := running.client.PrepareNetworkReleaseApproval(
		t.Context(),
		PrepareNetworkReleaseApprovalRequest{
			OperationID:                preparation.OperationID,
			ExpectedCheckpointRevision: preparation.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseApproval() error = %v", err)
	}
	confirmed, err := running.client.ConfirmNetworkReleaseApproval(
		t.Context(),
		validNetworkReleaseApprovalConfirmation(),
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseApproval() error = %v", err)
	}
	resolverPrepared, err := running.client.PrepareNetworkReleaseResolverApproval(
		t.Context(),
		PrepareNetworkReleaseResolverApprovalRequest{
			OperationID:                resolverPreparation.OperationID,
			ExpectedCheckpointRevision: resolverPreparation.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseResolverApproval() error = %v", err)
	}
	resolverConfirmed, err := running.client.ConfirmNetworkReleaseResolverApproval(
		t.Context(),
		validNetworkReleaseResolverApprovalConfirmation(),
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseResolverApproval() error = %v", err)
	}
	trustPrepared, err := running.client.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                trustPreparation.OperationID,
			ExpectedCheckpointRevision: trustPreparation.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseTrustApproval() error = %v", err)
	}
	trustEvidence := validNetworkReleaseTrustEvidence()
	trustConfirmed, err := running.client.ConfirmNetworkReleaseTrustApproval(
		t.Context(),
		ConfirmNetworkReleaseTrustApprovalRequest{
			OperationID:                trustPreparation.OperationID,
			ExpectedCheckpointRevision: trustPreparation.CheckpointRevision,
			TrustEvidence:              &trustEvidence,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseTrustApproval() error = %v", err)
	}
	preexistingPreparation := trustPreparation
	preexistingPreparation.Disposition = NetworkReleaseTrustPreexistingUnowned
	preexistingPreparation.PublicationDisposition = NetworkReleaseTrustPublicationNotRequired
	preexistingPreparation.Ticket = nil
	authority.mu.Lock()
	authority.trustPreparation = preexistingPreparation
	authority.mu.Unlock()
	preexistingPrepared, err := running.client.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                preexistingPreparation.OperationID,
			ExpectedCheckpointRevision: preexistingPreparation.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseTrustApproval() for preexisting trust error = %v", err)
	}
	preexistingConfirmed, err := running.client.ConfirmNetworkReleaseTrustApproval(
		t.Context(),
		ConfirmNetworkReleaseTrustApprovalRequest{
			OperationID:                preexistingPreparation.OperationID,
			ExpectedCheckpointRevision: preexistingPreparation.CheckpointRevision,
			TrustEvidence:              nil,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseTrustApproval() for preexisting trust error = %v", err)
	}
	if prepared != preparation ||
		confirmed.Operation.ID != release.Operation.ID ||
		confirmed.Phase != NetworkReleasePhaseResolver ||
		confirmed.CheckpointRevision != release.CheckpointRevision ||
		resolverPrepared != resolverPreparation ||
		resolverConfirmed.Operation.ID != resolverRelease.Operation.ID ||
		resolverConfirmed.Phase != NetworkReleasePhaseTrust ||
		resolverConfirmed.CheckpointRevision != resolverRelease.CheckpointRevision ||
		!reflect.DeepEqual(trustPrepared, trustPreparation) ||
		trustConfirmed.Operation.ID != trustRelease.Operation.ID ||
		trustConfirmed.Phase != NetworkReleasePhaseLoopbacks ||
		trustConfirmed.CheckpointRevision != trustRelease.CheckpointRevision ||
		!reflect.DeepEqual(preexistingPrepared, preexistingPreparation) ||
		preexistingConfirmed.Operation.ID != trustRelease.Operation.ID ||
		preexistingConfirmed.Phase != NetworkReleasePhaseLoopbacks ||
		preexistingConfirmed.CheckpointRevision != trustRelease.CheckpointRevision {
		t.Fatalf(
			"results = %#v %#v %#v %#v %#v %#v %#v %#v",
			prepared,
			confirmed,
			resolverPrepared,
			resolverConfirmed,
			trustPrepared,
			trustConfirmed,
			preexistingPrepared,
			preexistingConfirmed,
		)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	expectedTrustPrepare := PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                trustPreparation.OperationID,
		ExpectedCheckpointRevision: trustPreparation.CheckpointRevision,
	}
	expectedPreexistingPrepare := PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                preexistingPreparation.OperationID,
		ExpectedCheckpointRevision: preexistingPreparation.CheckpointRevision,
	}
	if len(authority.prepares) != 1 ||
		len(authority.confirmations) != 1 ||
		len(authority.resolverPrepares) != 1 ||
		len(authority.resolverConfirmations) != 1 ||
		len(authority.trustPrepares) != 2 ||
		len(authority.trustConfirmations) != 2 ||
		len(authority.callers) != 8 {
		t.Fatalf(
			"calls = %#v %#v %#v %#v %#v %#v %#v",
			authority.prepares,
			authority.confirmations,
			authority.resolverPrepares,
			authority.resolverConfirmations,
			authority.trustPrepares,
			authority.trustConfirmations,
			authority.callers,
		)
	}
	if authority.trustPrepares[0] != expectedTrustPrepare ||
		authority.trustPrepares[1] != expectedPreexistingPrepare ||
		authority.trustConfirmations[0].TrustEvidence == nil ||
		*authority.trustConfirmations[0].TrustEvidence != trustEvidence ||
		authority.trustConfirmations[1].TrustEvidence != nil {
		t.Fatalf("trust calls = %#v %#v", authority.trustPrepares, authority.trustConfirmations)
	}
	for _, caller := range authority.callers {
		if caller.Transport.UserID != testClientPeer.UserID {
			t.Fatalf("caller = %#v", caller)
		}
	}
}

// TestNetworkReleaseApprovalClientRejectsUnnegotiatedCapability proves no approval authority request is dispatched to old daemons.
func TestNetworkReleaseApprovalClientRejectsUnnegotiatedCapability(t *testing.T) {
	running := newNetworkReleaseApprovalRunningClient(t, nil)
	if containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("absent approval authority negotiated capability")
	}
	if containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseResolverApprovalV1) {
		t.Fatal("absent approval authority negotiated resolver capability")
	}
	if containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseTrustApprovalV1) {
		t.Fatal("absent approval authority negotiated trust capability")
	}
	if _, err := running.client.PrepareNetworkReleaseApproval(
		t.Context(),
		PrepareNetworkReleaseApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 6,
		},
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := running.client.ConfirmNetworkReleaseApproval(
		t.Context(),
		validNetworkReleaseApprovalConfirmation(),
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("confirm error = %v", err)
	}
	if _, err := running.client.PrepareNetworkReleaseResolverApproval(
		t.Context(),
		PrepareNetworkReleaseResolverApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 6,
		},
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("resolver prepare error = %v", err)
	}
	if _, err := running.client.ConfirmNetworkReleaseResolverApproval(
		t.Context(),
		validNetworkReleaseResolverApprovalConfirmation(),
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("resolver confirm error = %v", err)
	}
	if _, err := running.client.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-network-release",
			ExpectedCheckpointRevision: 7,
		},
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("trust prepare error = %v", err)
	}
	if _, err := running.client.ConfirmNetworkReleaseTrustApproval(
		t.Context(),
		ConfirmNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-network-release",
			ExpectedCheckpointRevision: 7,
			TrustEvidence:              nil,
		},
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("trust confirm error = %v", err)
	}
}

// TestNetworkReleaseResolverApprovalClientRejectsLegacyLowPortCapability proves a pre-resolver approval daemon cannot be mistaken for resolver support.
func TestNetworkReleaseResolverApprovalClientRejectsLegacyLowPortCapability(t *testing.T) {
	authority := &recordingNetworkReleaseApprovalAuthority{
		resolverPreparation: validNetworkReleaseResolverApprovalPreparation(),
	}
	running := newNetworkReleaseApprovalRunningClient(t, authority)
	running.client.peer.Session.Capabilities = []rpc.Capability{
		CapabilityNetworkReleaseApprovalV1,
	}
	_, err := running.client.PrepareNetworkReleaseResolverApproval(
		t.Context(),
		PrepareNetworkReleaseResolverApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 6,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("resolver prepare error = %v", err)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if len(authority.resolverPrepares) != 0 {
		t.Fatalf("resolver prepare calls = %d, want zero", len(authority.resolverPrepares))
	}
}

// TestNetworkReleaseTrustApprovalClientRejectsLegacyResolverCapability proves older approval capabilities cannot authorize trust release.
func TestNetworkReleaseTrustApprovalClientRejectsLegacyResolverCapability(t *testing.T) {
	running := newNetworkReleaseApprovalRunningClient(t, &recordingNetworkReleaseApprovalAuthority{})
	running.client.peer.Session.Capabilities = []rpc.Capability{
		CapabilityNetworkReleaseResolverApprovalV1,
	}
	_, err := running.client.PrepareNetworkReleaseTrustApproval(
		t.Context(),
		PrepareNetworkReleaseTrustApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 6,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("trust prepare error = %v", err)
	}
}

// TestNetworkReleaseTrustApprovalDecodersAndCorrelation preserve optional evidence and ticket boundaries.
func TestNetworkReleaseTrustApprovalDecodersAndCorrelation(t *testing.T) {
	evidence, err := json.Marshal(validNetworkReleaseTrustEvidence())
	if err != nil {
		t.Fatal(err)
	}
	ticket := validNetworkReleaseTrustApprovalTicket()
	preparation := NetworkReleaseTrustApprovalPreparation{
		OperationID:            ticket.OperationID,
		CheckpointRevision:     7,
		Disposition:            NetworkReleaseTrustOwned,
		PublicationDisposition: NetworkReleaseTrustPublicationDurable,
		Ticket:                 &ticket,
	}
	preparationJSON, err := json.Marshal(preparation)
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(networkReleaseTrustApprovalPreparationResponse{
		Preparation: preparation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeNetworkReleaseTrustApprovalPreparationResponse(response, &networkReleaseTrustApprovalPreparationResponse{}); err != nil {
		t.Fatalf("decodeNetworkReleaseTrustApprovalPreparationResponse() error = %v", err)
	}
	preexisting := preparation
	preexisting.Disposition = NetworkReleaseTrustPreexistingUnowned
	preexisting.PublicationDisposition = NetworkReleaseTrustPublicationNotRequired
	preexisting.Ticket = nil
	preexistingResponse, err := json.Marshal(networkReleaseTrustApprovalPreparationResponse{
		Preparation: preexisting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeNetworkReleaseTrustApprovalPreparationResponse(
		preexistingResponse,
		&networkReleaseTrustApprovalPreparationResponse{},
	); err != nil {
		t.Fatalf("decode preexisting trust preparation error = %v", err)
	}
	ownedNullTicket := preparation
	ownedNullTicket.Ticket = nil
	ownedNullTicketResponse, err := json.Marshal(networkReleaseTrustApprovalPreparationResponse{
		Preparation: ownedNullTicket,
	})
	if err != nil {
		t.Fatal(err)
	}
	preexistingWithTicket := preexisting
	preexistingWithTicket.Ticket = &ticket
	preexistingWithTicketResponse, err := json.Marshal(networkReleaseTrustApprovalPreparationResponse{
		Preparation: preexistingWithTicket,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"unexpected":true}`),
		[]byte(`{"preparation":` + string(preparationJSON) + `,"preparation":{}}`),
		[]byte(`{"preparation":` + string(preparationJSON) + `} null`),
		[]byte(`{"preparation":{"operation_id":"operation-network-release","checkpoint_revision":7,"disposition":"owned","publication_disposition":"durable"}}`),
		[]byte(`{"preparation":{"operation_id":"operation-network-release","operation_id":"other","checkpoint_revision":7,"disposition":"owned","publication_disposition":"durable","ticket":null}}`),
		[]byte(`{"preparation":{"operation_id":"operation-network-release","checkpoint_revision":7,"disposition":"owned","publication_disposition":"durable","ticket":null,"unexpected":true}}`),
		ownedNullTicketResponse,
		preexistingWithTicketResponse,
		[]byte(strings.Replace(string(response), `"reference"`, `"reference":"duplicate","reference"`, 1)),
		[]byte(strings.Replace(string(response), `"expires_at"`, `"unexpected":true,"expires_at"`, 1)),
		[]byte(`{"preparation":{"operation_id":"operation-network-release","checkpoint_revision":7,"disposition":"owned","publication_disposition":"durable","ticket":{}}}`),
	} {
		if err := decodeNetworkReleaseTrustApprovalPreparationResponse(
			payload,
			&networkReleaseTrustApprovalPreparationResponse{},
		); err == nil {
			t.Fatalf("trust preparation decoder accepted %s", payload)
		}
	}
	for _, payload := range []string{
		`{"operation_id":"operation-network-release","expected_checkpoint_revision":7}`,
		`{"operation_id":"operation-network-release","operation_id":"other","expected_checkpoint_revision":7}`,
		`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":null,"extra":true}`,
		`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":null}{}`,
		`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":{}}`,
		`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":` + strings.Replace(string(evidence), `"owned_absent"`, `"exact"`, 1) + `}`,
	} {
		if _, err := decodeConfirmNetworkReleaseTrustApprovalRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeConfirmNetworkReleaseTrustApprovalRequest(%q) unexpectedly succeeded", payload)
		}
	}
	nilRequest, err := decodeConfirmNetworkReleaseTrustApprovalRequest(
		[]byte(`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":null}`),
	)
	if err != nil || nilRequest.TrustEvidence != nil {
		t.Fatalf("decode null trust evidence = %#v, %v", nilRequest, err)
	}
	decodedEvidence, err := decodeConfirmNetworkReleaseTrustApprovalRequest(
		[]byte(`{"operation_id":"operation-network-release","expected_checkpoint_revision":7,"trust_evidence":` + string(evidence) + `}`),
	)
	if err != nil || decodedEvidence.TrustEvidence == nil || *decodedEvidence.TrustEvidence != validNetworkReleaseTrustEvidence() {
		t.Fatalf("decode trust evidence = %#v, %v", decodedEvidence, err)
	}
	request := ConfirmNetworkReleaseTrustApprovalRequest{
		OperationID:                preparation.OperationID,
		ExpectedCheckpointRevision: preparation.CheckpointRevision,
	}
	release := validNetworkReleaseApprovalRelease(t)
	release.Operation.ID = preparation.OperationID
	release.Phase = NetworkReleasePhaseLoopbacks
	release.CheckpointRevision = preparation.CheckpointRevision + 1
	if err := validateNetworkReleaseTrustApprovalConfirmationCorrelation(request, release); err != nil {
		t.Fatalf("validateNetworkReleaseTrustApprovalConfirmationCorrelation() error = %v", err)
	}
	for _, mutate := range []func(*NetworkReleaseOperation){
		func(value *NetworkReleaseOperation) {
			value.Operation.ID = "operation-other"
		},
		func(value *NetworkReleaseOperation) {
			value.CheckpointRevision = request.ExpectedCheckpointRevision
		},
		func(value *NetworkReleaseOperation) {
			value.Phase = NetworkReleasePhaseTrust
		},
	} {
		value := release
		mutate(&value)
		if err := validateNetworkReleaseTrustApprovalConfirmationCorrelation(request, value); err == nil {
			t.Fatal("trust confirmation correlation accepted the wrong boundary")
		}
	}
	prepareRequest := PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                preparation.OperationID,
		ExpectedCheckpointRevision: preparation.CheckpointRevision,
	}
	for _, mutate := range []func(*NetworkReleaseTrustApprovalPreparation){
		func(value *NetworkReleaseTrustApprovalPreparation) {
			value.OperationID = "operation-other"
		},
		func(value *NetworkReleaseTrustApprovalPreparation) {
			value.CheckpointRevision++
		},
	} {
		value := preparation
		mutate(&value)
		if err := validateNetworkReleaseTrustApprovalPreparationCorrelation(prepareRequest, value); err == nil {
			t.Fatal("trust preparation correlation accepted the wrong boundary")
		}
	}
}

// TestNetworkReleaseApprovalDecodersRejectAmbiguousJSON covers outer, nested evidence, response, and bounded request decoding.
func TestNetworkReleaseApprovalDecodersRejectAmbiguousJSON(t *testing.T) {
	evidence, err := json.Marshal(validNetworkReleaseApprovalEvidence())
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := json.Marshal(validNetworkReleaseApprovalPreparation())
	if err != nil {
		t.Fatal(err)
	}
	release, err := json.Marshal(networkReleaseResponse{
		Release: validNetworkReleaseOperation(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		decode  func([]byte) error
		payload string
	}{
		{
			name:    "prepare unknown",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"extra":true}`,
		},
		{
			name:    "prepare duplicate",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","operation_id":"other","expected_checkpoint_revision":6}`,
		},
		{
			name:    "prepare missing",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release"}`,
		},
		{
			name:    "prepare trailing",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6} null`,
		},
		{
			name:   "confirm unknown",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `,"extra":true}`,
		},
		{
			name:   "confirm duplicate",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `,"low_port_evidence":` + string(evidence) + `}`,
		},
		{
			name:    "confirm missing",
			decode:  decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6}`,
		},
		{
			name:   "confirm trailing",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `} []`,
		},
		{
			name:   "nested evidence unknown",
			decode: decodeConfirmApprovalError,
			payload: strings.Replace(
				`{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":`+string(evidence)+`}`,
				`"postcondition":"owned_absent"`,
				`"postcondition":"owned_absent","extra":true`,
				1,
			),
		},
		{
			name:    "response unknown",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{"extra":true}`,
		},
		{
			name:   "response duplicate",
			decode: decodeApprovalPreparationResponseError,
			payload: `{"preparation":` + string(preparation) + `,"preparation":` +
				string(preparation) + `}`,
		},
		{
			name:    "response missing",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{}`,
		},
		{
			name:    "response trailing",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{"preparation":` + string(preparation) + `} null`,
		},
		{
			name:   "response nested duplicate",
			decode: decodeApprovalPreparationResponseError,
			payload: strings.Replace(
				`{"preparation":`+string(preparation)+`}`,
				`"operation_id":"operation-release"`,
				`"operation_id":"operation-release","operation_id":"other"`,
				1,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted ambiguous JSON")
			}
		})
	}
	oversized := `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
		string(evidence) + strings.Repeat(" ", maximumNetworkReleaseApprovalConfirmationRequestBytes) + `}`
	if _, err := decodeConfirmNetworkReleaseApprovalRequest([]byte(oversized)); err == nil {
		t.Fatal("confirmation decoder accepted oversized request")
	}
	oversizedResponse := `{"preparation":` + string(preparation) + `}` +
		strings.Repeat(" ", helper.MaxResponseBytes)
	if err := decodeApprovalPreparationResponseError([]byte(oversizedResponse)); err == nil {
		t.Fatal("preparation response decoder accepted an oversized response")
	}
	oversizedReleaseResponse := string(release) + strings.Repeat(" ", maximumNetworkReleaseResponseBytes)
	if err := decodeNetworkReleaseResponse(
		[]byte(oversizedReleaseResponse),
		&networkReleaseResponse{},
	); err == nil {
		t.Fatal("release response decoder accepted an oversized response")
	}
}

// TestNetworkReleaseApprovalCorrelationsRejectWrongOperationCheckpointAndPhase preserves caller-owned optimistic boundaries.
func TestNetworkReleaseApprovalCorrelationsRejectWrongOperationCheckpointAndPhase(t *testing.T) {
	prepare := PrepareNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
	}
	if err := validateNetworkReleaseApprovalPreparationCorrelation(
		prepare,
		validNetworkReleaseApprovalPreparation(),
	); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NetworkReleaseApprovalPreparation){
		func(value *NetworkReleaseApprovalPreparation) {
			value.OperationID = "other"
		},
		func(value *NetworkReleaseApprovalPreparation) {
			value.CheckpointRevision++
		},
	} {
		value := validNetworkReleaseApprovalPreparation()
		mutate(&value)
		if err := validateNetworkReleaseApprovalPreparationCorrelation(prepare, value); err == nil {
			t.Fatal("preparation correlation accepted wrong boundary")
		}
	}
	confirm := validNetworkReleaseApprovalConfirmation()
	for _, mutate := range []func(*NetworkReleaseOperation){
		func(value *NetworkReleaseOperation) {
			value.Operation.ID = "other"
		},
		func(value *NetworkReleaseOperation) {
			value.CheckpointRevision = confirm.ExpectedCheckpointRevision
		},
		func(value *NetworkReleaseOperation) {
			value.Phase = NetworkReleasePhaseTrust
		},
	} {
		value := validNetworkReleaseApprovalRelease(t)
		mutate(&value)
		if err := validateNetworkReleaseApprovalConfirmationCorrelation(confirm, value); err == nil {
			t.Fatal("confirmation correlation accepted wrong release boundary")
		}
	}
}

// TestNetworkReleaseApprovalHandlerRejectsUnnegotiatedAndAuthorityFailures preserves wire error categories.
func TestNetworkReleaseApprovalHandlerRejectsUnnegotiatedAndAuthorityFailures(t *testing.T) {
	server := &Server{
		config: ServerConfig{
			NetworkReleaseApprovalAuthority: &recordingNetworkReleaseApprovalAuthority{
				err: errors.New("authority failure"),
			},
		},
	}
	handler := server.networkReleaseLowPortPrepareHandler(testClientPeer)
	payload, err := json.Marshal(PrepareNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := session.Peer{
		Role:     rpc.RoleCLI,
		Protocol: protocolV1,
		Capabilities: []rpc.Capability{
			CapabilityV1,
		},
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodePermissionDenied)
	peer.Capabilities = []rpc.Capability{
		CapabilityV1,
		CapabilityNetworkReleaseApprovalV1,
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodeInternal)
	server.config.NetworkReleaseApprovalAuthority = &recordingNetworkReleaseApprovalAuthority{}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodeInternal)
}

// TestNetworkReleaseResolverApprovalHandlerRequiresResolverCapability prevents the legacy low-port capability from authorizing resolver methods.
func TestNetworkReleaseResolverApprovalHandlerRequiresResolverCapability(t *testing.T) {
	server := &Server{
		config: ServerConfig{
			NetworkReleaseApprovalAuthority: &recordingNetworkReleaseApprovalAuthority{
				resolverPreparation: validNetworkReleaseResolverApprovalPreparation(),
			},
		},
	}
	handler := server.networkReleaseResolverPrepareHandler(testClientPeer)
	payload, err := json.Marshal(PrepareNetworkReleaseResolverApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := session.Peer{
		Role:     rpc.RoleCLI,
		Protocol: protocolV1,
		Capabilities: []rpc.Capability{
			CapabilityV1,
			CapabilityNetworkReleaseApprovalV1,
		},
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodePermissionDenied)
	peer.Capabilities = []rpc.Capability{
		CapabilityV1,
		CapabilityNetworkReleaseResolverApprovalV1,
	}
	if _, err := handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	}); err != nil {
		t.Fatalf("resolver preparation handler error = %v", err)
	}
}

// TestNetworkReleaseTrustApprovalHandlerRequiresTrustCapability prevents resolver approval from authorizing trust methods.
func TestNetworkReleaseTrustApprovalHandlerRequiresTrustCapability(t *testing.T) {
	ticket := validNetworkReleaseTrustApprovalTicket()
	preparation := NetworkReleaseTrustApprovalPreparation{
		OperationID:            ticket.OperationID,
		CheckpointRevision:     7,
		Disposition:            NetworkReleaseTrustOwned,
		PublicationDisposition: NetworkReleaseTrustPublicationDurable,
		Ticket:                 &ticket,
	}
	server := &Server{
		config: ServerConfig{
			NetworkReleaseApprovalAuthority: &recordingNetworkReleaseApprovalAuthority{
				trustPreparation: preparation,
			},
		},
	}
	handler := server.networkReleaseTrustPrepareHandler(testClientPeer)
	payload, err := json.Marshal(PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                preparation.OperationID,
		ExpectedCheckpointRevision: preparation.CheckpointRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := session.Peer{
		Role:     rpc.RoleCLI,
		Protocol: protocolV1,
		Capabilities: []rpc.Capability{
			CapabilityV1,
			CapabilityNetworkReleaseResolverApprovalV1,
		},
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodePermissionDenied)
	peer.Capabilities = []rpc.Capability{
		CapabilityV1,
		CapabilityNetworkReleaseTrustApprovalV1,
	}
	if _, err := handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	}); err != nil {
		t.Fatalf("trust preparation handler error = %v", err)
	}
}

// validNetworkReleaseApprovalPreparation returns one canonical ticket preparation fixture.
func validNetworkReleaseApprovalPreparation() NetworkReleaseApprovalPreparation {
	return NetworkReleaseApprovalPreparation{
		OperationID:        "operation-release",
		CheckpointRevision: 6,
		Ticket: NetworkReleaseApprovalTicket{
			OperationID:                "operation-release",
			Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
			Operation:                  helper.OperationReleaseLowPorts,
			PolicyFingerprint:          strings.Repeat("b", 64),
			TargetOwnershipFingerprint: strings.Repeat("c", 64),
			ObservationFingerprint:     strings.Repeat("d", 64),
			ExpiresAt:                  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}
}

// validNetworkReleaseApprovalEvidence returns canonical helper evidence for the release-only owned-absent postcondition.
func validNetworkReleaseApprovalEvidence() helper.LowPortMutationEvidence {
	return helper.LowPortMutationEvidence{
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.LowPortPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseResolverApprovalPreparation returns one canonical resolver ticket preparation fixture.
func validNetworkReleaseResolverApprovalPreparation() NetworkReleaseResolverApprovalPreparation {
	return NetworkReleaseResolverApprovalPreparation{
		OperationID:            "operation-release",
		CheckpointRevision:     6,
		PublicationDisposition: NetworkReleaseResolverPublicationDurable,
		Ticket: NetworkReleaseResolverApprovalTicket{
			OperationID:                "operation-release",
			Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
			Operation:                  helper.OperationReleaseResolver,
			PolicyFingerprint:          strings.Repeat("b", 64),
			TargetOwnershipFingerprint: strings.Repeat("c", 64),
			ExpiresAt:                  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}
}

// validNetworkReleaseResolverApprovalEvidence returns canonical helper evidence for the release-only owned-absent resolver postcondition.
func validNetworkReleaseResolverApprovalEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.ResolverPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseResolverApprovalConfirmation returns a canonical confirmation at the retained resolver checkpoint.
func validNetworkReleaseResolverApprovalConfirmation() ConfirmNetworkReleaseResolverApprovalRequest {
	return ConfirmNetworkReleaseResolverApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
		ResolverEvidence:           validNetworkReleaseResolverApprovalEvidence(),
	}
}

// validNetworkReleaseApprovalConfirmation returns a canonical confirmation at the retained low-port checkpoint.
func validNetworkReleaseApprovalConfirmation() ConfirmNetworkReleaseApprovalRequest {
	return ConfirmNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
		LowPortEvidence:            validNetworkReleaseApprovalEvidence(),
	}
}

// validNetworkReleaseApprovalRelease returns the resolver checkpoint that must follow a successful low-port confirmation.
func validNetworkReleaseApprovalRelease(t *testing.T) NetworkReleaseOperation {
	release := validNetworkReleaseOperation(t)
	release.Phase = NetworkReleasePhaseResolver
	release.CheckpointRevision = 7
	return release
}

// newNetworkReleaseApprovalRunningClient connects a real local client and server around the optional approval authority.
func newNetworkReleaseApprovalRunningClient(
	t *testing.T,
	authority NetworkReleaseApprovalAuthority,
) runningControlClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{
		Conn: clientStream,
		peer: testDaemonPeer,
	}
	serverConnection := &testLocalConn{
		Conn: serverStream,
		peer: testClientPeer,
	}
	server, err := newServer(ServerConfig{
		Authority:                       &recordingAuthority{},
		NetworkReleaseApprovalAuthority: authority,
		RequestShutdown:                 func() {},
	}, testBuild)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, serverConnection)
	}()
	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return clientConnection, nil
		},
	}, testBuild)
	if err != nil {
		cancel()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatal(err)
	}
	running := runningControlClient{
		client:     client,
		cancel:     cancel,
		serverDone: done,
	}
	t.Cleanup(func() {
		running.close(t)
	})
	return running
}

// decodePrepareApprovalError adapts the typed decoder to the strict JSON test table.
func decodePrepareApprovalError(payload []byte) error {
	_, err := decodePrepareNetworkReleaseApprovalRequest(payload)
	return err
}

// decodeConfirmApprovalError adapts the typed decoder to the strict JSON test table.
func decodeConfirmApprovalError(payload []byte) error {
	_, err := decodeConfirmNetworkReleaseApprovalRequest(payload)
	return err
}

// decodeApprovalPreparationResponseError adapts the typed response decoder to the strict JSON test table.
func decodeApprovalPreparationResponseError(payload []byte) error {
	return decodeNetworkReleaseApprovalPreparationResponse(payload, &networkReleaseApprovalPreparationResponse{})
}
