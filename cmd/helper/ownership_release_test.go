package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// TestRunOpensAndClosesOwnershipAuthorityOnlyAfterReleaseAdmission proves ownership mutation remains lazy and retained through execution.
func TestRunOpensAndClosesOwnershipAuthorityOnlyAfterReleaseAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference, redemption := testOwnershipReleaseRedemption(now)
	request, err := json.Marshal(helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	events := make([]string, 0, 12)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openOwnershipHandler = func() (closingOwnershipHandler, error) {
		events = append(events, "open ownership handler")
		return &testOwnershipHandler{events: &events}, nil
	}
	reader := &recordingReader{
		reader: bytes.NewReader(request),
		events: &events,
	}
	var output bytes.Buffer
	if err := run(context.Background(), reader, &output, fixedClock{now: now}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"read request",
		"redeem ticket",
		"consume replay claim",
		"open ownership handler",
		"release network ownership",
		"close ownership handler",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestExecuteAdmittedOwnershipClosesOpenedHandlerAfterFailure proves lifecycle cleanup retains ownership close failures after execution rejects input.
func TestExecuteAdmittedOwnershipClosesOpenedHandlerAfterFailure(t *testing.T) {
	events := make([]string, 0, 2)
	closeErr := errors.New("close failed")
	authorities := &runtimeAuthorities{}
	dependencies := unusedRuntimeDependencies(t)
	dependencies.openOwnershipHandler = func() (closingOwnershipHandler, error) {
		events = append(events, "open ownership handler")
		return &testOwnershipHandler{
			events:   &events,
			closeErr: closeErr,
		}, nil
	}
	_, err := executeAdmittedOwnership(t.Context(), helper.AdmittedOwnershipOperation{}, dependencies, authorities)
	if err == nil {
		t.Fatal("executeAdmittedOwnership() error = nil")
	}
	if close := authorities.close(); !errors.Is(close, closeErr) {
		t.Fatalf("authorities.close() error = %v, want %v", close, closeErr)
	}
	if !slices.Equal(events, []string{"open ownership handler", "close ownership handler"}) {
		t.Fatalf("events = %#v", events)
	}
}

// testOwnershipReleaseRedemption returns an independently correlated ownership-release redemption fixture.
func testOwnershipReleaseRedemption(now time.Time) (helper.TicketReference, helper.TicketRedemption) {
	reference := helper.TicketReference(strings.Repeat("a", 64))
	fingerprint := strings.Repeat("f", 64)
	ticket := helper.Ticket{
		Version:                      helper.ProtocolVersion,
		Operation:                    helper.OperationReleaseNetworkOwnership,
		InstallationID:               "harbor-helper-test",
		RequesterIdentity:            "test-requester",
		OwnershipGeneration:          7,
		OwnershipSchemaVersion:       2,
		NetworkPolicyFingerprint:     strings.Repeat("e", 64),
		ApprovedPool:                 "127.77.0.0/24",
		ReleaseOperationID:           "release-network-ownership",
		ReleaseOperationRevision:     1,
		ReleaseCheckpointRevision:    2,
		ExpectedOwnershipFingerprint: fingerprint,
		Nonce:                        strings.Repeat("c", 32),
		ExpiresAt:                    now.Add(time.Minute),
	}
	return reference, helper.TicketRedemption{
		Ticket: ticket,
		Admission: helper.TicketAdmission{
			TicketReference:            reference,
			RequesterIdentity:          ticket.RequesterIdentity,
			InstallationID:             ticket.InstallationID,
			OwnershipGeneration:        ticket.OwnershipGeneration,
			OwnershipSchemaVersion:     ticket.OwnershipSchemaVersion,
			NetworkPolicyFingerprint:   ticket.NetworkPolicyFingerprint,
			ApprovedPool:               ticket.ApprovedPool,
			OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
			OwnershipFingerprint:       fingerprint,
			TargetOwnershipFingerprint: fingerprint,
			PostOwnershipFingerprint:   fingerprint,
			TicketVerifierKey:          "test-verifier-key",
		},
	}
}

// testOwnershipHandler supplies evidence and lifecycle events without opening protected storage.
type testOwnershipHandler struct {
	events     *[]string
	releaseErr error
	closeErr   error
}

// ReleaseNetworkOwnership records the selected ownership effect and returns exact correlated evidence.
func (handler *testOwnershipHandler) ReleaseNetworkOwnership(_ context.Context, ticket helper.Ticket, _ helper.TicketAdmission) (helper.OwnershipMutationEvidence, error) {
	*handler.events = append(*handler.events, "release network ownership")
	if handler.releaseErr != nil {
		return helper.OwnershipMutationEvidence{}, handler.releaseErr
	}
	return helper.OwnershipMutationEvidence{
		ReleaseOperationID:           ticket.ReleaseOperationID,
		ReleaseOperationRevision:     ticket.ReleaseOperationRevision,
		ReleaseCheckpointRevision:    ticket.ReleaseCheckpointRevision,
		ReleasedOwnershipFingerprint: ticket.ExpectedOwnershipFingerprint,
		Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
	}, nil
}

// Close records ownership authority teardown and preserves the configured result.
func (handler *testOwnershipHandler) Close() error {
	*handler.events = append(*handler.events, "close ownership handler")
	return handler.closeErr
}
