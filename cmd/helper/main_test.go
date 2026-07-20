package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// TestProductionDependenciesExposeFixedComposition proves the entrypoint declares every required authority adapter.
func TestProductionDependenciesExposeFixedComposition(t *testing.T) {
	dependencies := productionDependencies()
	if dependencies.authorizeInvocation == nil || dependencies.openTicketRedeemer == nil ||
		dependencies.openReplayGuard == nil || dependencies.newLoopbackIdentityHandler == nil ||
		dependencies.newResolverHandler == nil {
		t.Fatal("production dependencies are incomplete")
	}
	if handler := dependencies.newLoopbackIdentityHandler(); handler == nil {
		t.Fatal("production loopback handler is nil")
	}
	if handler := dependencies.newResolverHandler(); handler == nil {
		t.Fatal("production resolver handler is nil")
	}
}

// TestRunOpensCompleteAuthorityBeforeReading proves one request cannot be consumed under partial durable composition.
func TestRunOpensCompleteAuthorityBeforeReading(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference, redemption := testRedemption(now)
	request := helper.Request{Version: helper.ProtocolVersion, TicketReference: reference}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	events := make([]string, 0, 10)
	reader := &recordingReader{reader: bytes.NewReader(body), events: &events}
	dependencies := successfulTestDependencies(&events, redemption)
	var output bytes.Buffer
	if err := run(context.Background(), reader, &output, fixedClock{now: now}, dependencies); err != nil {
		t.Fatalf("run: %v", err)
	}

	var response helper.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"new loopback handler",
		"new resolver handler",
		"read request",
		"redeem ticket",
		"consume replay claim",
		"ensure loopback identity",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunFailsFastWithoutAdmissionDependency verifies incomplete production assembly cannot fall through to durable authority.
func TestRunFailsFastWithoutAdmissionDependency(t *testing.T) {
	reader := &recordingReader{reader: bytes.NewReader([]byte("{}")), events: &[]string{}}
	defer func() {
		if recover() == nil {
			t.Fatal("run() did not panic without its wired admission dependency")
		}
		if reader.recorded {
			t.Fatal("request was read without an admission dependency")
		}
	}()
	_ = run(context.Background(), reader, io.Discard, fixedClock{now: time.Now().UTC()}, runtimeDependencies{})
}

// TestRunStopsBeforeDurableAuthorityWhenInvocationAdmissionFails proves invalid native consent reaches no request authority.
func TestRunStopsBeforeDurableAuthorityWhenInvocationAdmissionFails(t *testing.T) {
	authorizationErr := errors.New("authorization denied")
	events := make([]string, 0, 1)
	dependencies := runtimeDependencies{
		authorizeInvocation: func() error {
			events = append(events, "authorize invocation")
			return authorizationErr
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			t.Fatal("ticket redeemer opened after invocation admission failed")
			return nil, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			t.Fatal("replay guard opened after invocation admission failed")
			return nil, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			t.Fatal("loopback handler constructed after invocation admission failed")
			return nil
		},
	}
	reader := &recordingReader{reader: bytes.NewReader([]byte("{}")), events: &events}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if !errors.Is(err, authorizationErr) {
		t.Fatalf("run error = %v, want %v", err, authorizationErr)
	}
	if !slices.Equal(events, []string{"authorize invocation"}) {
		t.Fatalf("events = %#v, want admission only", events)
	}
	if reader.recorded || output.Len() != 0 {
		t.Fatalf("request recorded = %t, output = %q, want neither", reader.recorded, output.String())
	}
}

// TestRunStopsBeforeReadingWhenRedeemerOpenFails verifies absent authentication authority reaches no other boundary.
func TestRunStopsBeforeReadingWhenRedeemerOpenFails(t *testing.T) {
	openErr := errors.New("ticket redeemer open failed")
	events := make([]string, 0, 1)
	dependencies := runtimeDependencies{
		authorizeInvocation: func() error {
			events = append(events, "authorize invocation")
			return nil
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			events = append(events, "open ticket redeemer")
			return nil, openErr
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			t.Fatal("replay guard was opened without authentication authority")
			return nil, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			t.Fatal("loopback handler was constructed without authentication authority")
			return nil
		},
	}
	reader := &recordingReader{reader: bytes.NewReader([]byte("{}")), events: &events}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if !errors.Is(err, openErr) {
		t.Fatalf("run error = %v, want redeemer open failure", err)
	}
	wantEvents := []string{"authorize invocation", "open ticket redeemer"}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no response before complete composition", output.String())
	}
}

// TestRunClosesRedeemerWhenReplayOpenFails verifies partial composition leaves stdin untouched and releases retained authority.
func TestRunClosesRedeemerWhenReplayOpenFails(t *testing.T) {
	openErr := errors.New("replay open failed")
	closeErr := errors.New("redeemer close failed")
	events := make([]string, 0, 3)
	dependencies := runtimeDependencies{
		authorizeInvocation: func() error {
			events = append(events, "authorize invocation")
			return nil
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			events = append(events, "open ticket redeemer")
			return &testTicketRedeemer{events: &events, closeErr: closeErr}, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			events = append(events, "open replay guard")
			return nil, openErr
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			t.Fatal("loopback handler was constructed after replay composition failed")
			return nil
		},
	}
	reader := &recordingReader{reader: bytes.NewReader([]byte("{}")), events: &events}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
		t.Fatalf("run error = %v, want open and close failures", err)
	}
	wantEvents := []string{"authorize invocation", "open ticket redeemer", "open replay guard", "close ticket redeemer"}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no response before complete composition", output.String())
	}
}

// TestRunClosesEveryAuthorityAfterServeFailure proves response failure cannot strand retained privileged handles.
func TestRunClosesEveryAuthorityAfterServeFailure(t *testing.T) {
	redeemerCloseErr := errors.New("redeemer close failed")
	replayCloseErr := errors.New("replay close failed")
	events := make([]string, 0, 6)
	dependencies := runtimeDependencies{
		authorizeInvocation: func() error {
			events = append(events, "authorize invocation")
			return nil
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			events = append(events, "open ticket redeemer")
			return &testTicketRedeemer{events: &events, closeErr: redeemerCloseErr}, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			events = append(events, "open replay guard")
			return &testReplayGuard{events: &events, closeErr: replayCloseErr}, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			events = append(events, "new loopback handler")
			return &testLoopbackHandler{events: &events}
		},
		newResolverHandler: func() helper.ResolverHandler {
			events = append(events, "new resolver handler")
			return helper.UnavailableResolverHandler{}
		},
	}
	reader := &recordingReader{reader: bytes.NewReader(nil), events: &events}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if err == nil || !errors.Is(err, redeemerCloseErr) || !errors.Is(err, replayCloseErr) {
		t.Fatalf("run error = %v, want request and both close failures", err)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"new loopback handler",
		"new resolver handler",
		"read request",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	var response helper.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.OK || response.Error == nil || response.Error.Code != helper.ErrorCodeInvalidJSON {
		t.Fatalf("unexpected response: %#v", response)
	}
}

// fixedClock supplies deterministic trusted time to entrypoint tests.
type fixedClock struct {
	now time.Time
}

// Now returns the deterministic main-package test time.
func (c fixedClock) Now() time.Time {
	return c.now
}

// recordingReader records the first attempt to consume caller-controlled bytes.
type recordingReader struct {
	reader   *bytes.Reader
	events   *[]string
	recorded bool
}

// Read records the authority ordering before forwarding request bytes.
func (reader *recordingReader) Read(buffer []byte) (int, error) {
	if !reader.recorded {
		reader.recorded = true
		*reader.events = append(*reader.events, "read request")
	}
	return reader.reader.Read(buffer)
}

// testTicketRedeemer supplies one authenticated redemption while exposing lifecycle ordering.
type testTicketRedeemer struct {
	redemption helper.TicketRedemption
	events     *[]string
	closeErr   error
}

// Redeem returns the independently bound test authority.
func (redeemer *testTicketRedeemer) Redeem(_ context.Context, _ helper.TicketReference) (helper.TicketRedemption, error) {
	*redeemer.events = append(*redeemer.events, "redeem ticket")
	return redeemer.redemption, nil
}

// Close records release of the retained ticket topology.
func (redeemer *testTicketRedeemer) Close() error {
	*redeemer.events = append(*redeemer.events, "close ticket redeemer")
	return redeemer.closeErr
}

// testReplayGuard records durable replay admission and lifecycle ordering.
type testReplayGuard struct {
	events   *[]string
	closeErr error
}

// Consume records the single-use admission boundary.
func (guard *testReplayGuard) Consume(_ context.Context, _ helper.ReplayClaim) error {
	*guard.events = append(*guard.events, "consume replay claim")
	return nil
}

// Close records release of the retained replay directory.
func (guard *testReplayGuard) Close() error {
	*guard.events = append(*guard.events, "close replay guard")
	return guard.closeErr
}

// testLoopbackHandler returns bounded evidence without touching the host network.
type testLoopbackHandler struct {
	events *[]string
}

// EnsureLoopbackIdentity returns a verified owned postcondition for the approved address.
func (handler *testLoopbackHandler) EnsureLoopbackIdentity(_ context.Context, ticket helper.Ticket) (helper.MutationEvidence, error) {
	*handler.events = append(*handler.events, "ensure loopback identity")
	return helper.MutationEvidence{
		Changed: true,
		Address: ticket.ApprovedAddress,
		Observation: helper.ExpectedObservation{
			State:       helper.ObservationOwned,
			Fingerprint: strings.Repeat("d", 64),
		},
	}, nil
}

// EnsureLoopbackPool returns verified owned postconditions for every approved pool identity.
func (handler *testLoopbackHandler) EnsureLoopbackPool(_ context.Context, ticket helper.Ticket) (helper.PoolMutationEvidence, error) {
	*handler.events = append(*handler.events, "ensure loopback pool")
	identities := make([]helper.MutationEvidence, 0, len(ticket.ExpectedLoopbackPool.Identities))
	for _, identity := range ticket.ExpectedLoopbackPool.Identities {
		identities = append(identities, helper.MutationEvidence{
			Changed: true,
			Address: identity.Address,
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: strings.Repeat("d", 64),
			},
		})
	}
	return helper.PoolMutationEvidence{
		Pool:       ticket.ApprovedPool,
		Identities: identities,
	}, nil
}

// ReleaseLoopbackIdentity returns a verified absent postcondition for the approved address.
func (handler *testLoopbackHandler) ReleaseLoopbackIdentity(_ context.Context, ticket helper.Ticket) (helper.MutationEvidence, error) {
	*handler.events = append(*handler.events, "release loopback identity")
	return helper.MutationEvidence{
		Changed: true,
		Address: ticket.ApprovedAddress,
		Observation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("e", 64),
		},
	}, nil
}

// successfulTestDependencies creates the complete in-memory authority graph used by the entrypoint test.
func successfulTestDependencies(events *[]string, redemption helper.TicketRedemption) runtimeDependencies {
	return runtimeDependencies{
		authorizeInvocation: func() error {
			*events = append(*events, "authorize invocation")
			return nil
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			*events = append(*events, "open ticket redeemer")
			return &testTicketRedeemer{redemption: redemption, events: events}, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			*events = append(*events, "open replay guard")
			return &testReplayGuard{events: events}, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			*events = append(*events, "new loopback handler")
			return &testLoopbackHandler{events: events}
		},
		newResolverHandler: func() helper.ResolverHandler {
			*events = append(*events, "new resolver handler")
			return helper.UnavailableResolverHandler{}
		},
	}
}

// testRedemption builds one canonical ticket and its independently authenticated admission binding.
func testRedemption(now time.Time) (helper.TicketReference, helper.TicketRedemption) {
	reference := helper.TicketReference(strings.Repeat("a", 64))
	ticket := helper.Ticket{
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackIdentity,
		InstallationID:         "harbor-helper-test",
		RequesterIdentity:      "test-requester",
		OwnershipGeneration:    7,
		OwnershipSchemaVersion: 1,
		ApprovedPool:           "127.77.0.0/24",
		ApprovedAddress:        "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("b", 64),
		},
		ExpectedPreAssignment: &helper.ExpectedPreAssignment{
			Fingerprint:  strings.Repeat("d", 64),
			Requirements: []helper.SocketRequirement{},
		},
		Nonce:     strings.Repeat("c", 32),
		ExpiresAt: now.Add(time.Minute),
	}
	return reference, helper.TicketRedemption{
		Ticket: ticket,
		Admission: helper.TicketAdmission{
			TicketReference:          reference,
			RequesterIdentity:        ticket.RequesterIdentity,
			InstallationID:           ticket.InstallationID,
			OwnershipGeneration:      ticket.OwnershipGeneration,
			OwnershipSchemaVersion:   ticket.OwnershipSchemaVersion,
			NetworkPolicyFingerprint: ticket.NetworkPolicyFingerprint,
			ApprovedPool:             ticket.ApprovedPool,
		},
	}
}

var _ io.Reader = (*recordingReader)(nil)
var _ helper.LoopbackIdentityHandler = (*testLoopbackHandler)(nil)
