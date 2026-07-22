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

// lifecycleLowPortHandler records one selected low-port mutation and its cleanup.
type lifecycleLowPortHandler struct {
	events   *[]string
	closeErr error
}

// EnsureLowPorts returns evidence bound to the admitted policy and ownership target.
func (handler *lifecycleLowPortHandler) EnsureLowPorts(
	_ context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.LowPortMutationEvidence, error) {
	*handler.events = append(*handler.events, "ensure low ports")
	return helper.LowPortMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
		OwnershipFingerprint:   admission.TargetOwnershipFingerprint,
		ObservationFingerprint: strings.Repeat("e", 64),
		Postcondition:          helper.LowPortPostconditionExact,
	}, nil
}

// ReleaseLowPorts returns absent evidence bound to the admitted policy and ownership target.
func (handler *lifecycleLowPortHandler) ReleaseLowPorts(
	_ context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.LowPortMutationEvidence, error) {
	*handler.events = append(*handler.events, "release low ports")
	return helper.LowPortMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
		OwnershipFingerprint:   admission.TargetOwnershipFingerprint,
		ObservationFingerprint: strings.Repeat("e", 64),
		Postcondition:          helper.LowPortPostconditionOwnedAbsent,
	}, nil
}

// Close records release of the selected native low-port boundary.
func (handler *lifecycleLowPortHandler) Close() error {
	*handler.events = append(*handler.events, "close low-port handler")
	return handler.closeErr
}

// TestProductionDependenciesIncludeLowPortComposition proves every platform makes availability an explicit choice.
func TestProductionDependenciesIncludeLowPortComposition(t *testing.T) {
	dependencies := productionDependencies()
	handler, err := dependencies.openLowPortHandler()
	if err != nil {
		t.Fatalf("openLowPortHandler() error = %v", err)
	}
	if handler == nil {
		t.Fatal("openLowPortHandler() handler = nil")
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("handler.Close() error = %v", err)
	}
}

// TestRunKeepsLowPortAuthorityLazy proves an admitted loopback ticket never opens an unrelated low-port handler.
func TestRunKeepsLowPortAuthorityLazy(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := testRedemption(now)
	body, err := json.Marshal(helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make([]string, 0, 10)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
		t.Fatal("low-port handler opened for loopback ticket")
		return nil, nil
	}
	dependencies.openResolverHandler = func() (closingResolverHandler, error) {
		t.Fatal("resolver handler opened for loopback ticket")
		return nil, nil
	}
	var output bytes.Buffer
	if err := run(context.Background(), bytes.NewReader(body), &output, fixedClock{now: now}, dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !slices.Contains(events, "ensure loopback identity") || slices.Contains(events, "open low-port handler") {
		t.Fatalf("events = %#v", events)
	}
}

// TestRunOpensOnlyAdmittedLowPortAuthorityAndClosesIt proves lazy selection retains cleanup ordering.
func TestRunOpensOnlyAdmittedLowPortAuthorityAndClosesIt(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := operationLifecycleRedemption(t, now, helper.OperationEnsureLowPorts)
	events := make([]string, 0, 12)
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
		events = append(events, "open low-port handler")
		return &lifecycleLowPortHandler{
			events: &events,
		}, nil
	}
	dependencies.openResolverHandler = func() (closingResolverHandler, error) {
		t.Fatal("resolver handler opened for low-port ticket")
		return nil, nil
	}
	dependencies.openTrustHandler = func() (closingTrustHandler, error) {
		t.Fatal("trust handler opened for low-port ticket")
		return nil, nil
	}
	dependencies.newLoopbackIdentityHandler = func() helper.LoopbackIdentityHandler {
		t.Fatal("loopback handler constructed for low-port ticket")
		return nil
	}

	response, err := runTrustLifecycle(t, now, reference, dependencies)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !response.OK || response.Result == nil || response.Result.LowPortEvidence == nil {
		t.Fatalf("response = %#v", response)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"redeem ticket",
		"consume replay claim",
		"open low-port handler",
		"ensure low ports",
		"close low-port handler",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunJoinsLazyLowPortCloseFailureAfterWritingSuccess preserves response and cleanup error semantics.
func TestRunJoinsLazyLowPortCloseFailureAfterWritingSuccess(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	reference, redemption := operationLifecycleRedemption(t, now, helper.OperationEnsureLowPorts)
	events := make([]string, 0, 12)
	closeErr := errors.New("low-port close failed")
	dependencies := successfulTestDependencies(&events, redemption)
	dependencies.openLowPortHandler = func() (closingLowPortHandler, error) {
		return &lifecycleLowPortHandler{
			events:   &events,
			closeErr: closeErr,
		}, nil
	}

	response, err := runTrustLifecycle(t, now, reference, dependencies)
	if !errors.Is(err, closeErr) {
		t.Fatalf("run() error = %v, want %v", err, closeErr)
	}
	if !response.OK || response.Result == nil || response.Result.LowPortEvidence == nil {
		t.Fatalf("response = %#v, want successful low-port result", response)
	}
}

var _ closingLowPortHandler = (*lifecycleLowPortHandler)(nil)
