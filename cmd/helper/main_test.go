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
		dependencies.openResolverHandler == nil || dependencies.openTrustHandler == nil || dependencies.openAdministratorTrustHandler == nil ||
		dependencies.openLowPortHandler == nil || dependencies.transitionTrustIdentity == nil ||
		dependencies.transitionAdministratorTrustIdentity == nil {
		t.Fatal("production dependencies are incomplete")
	}
	if handler := dependencies.newLoopbackIdentityHandler(); handler == nil {
		t.Fatal("production loopback handler is nil")
	}
}

// TestRunOpensAdmissionAuthorityBeforeReading proves one request cannot be consumed before durable admission is ready.
func TestRunOpensAdmissionAuthorityBeforeReading(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference, redemption := testRedemption(now)
	request := helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	events := make([]string, 0, 10)
	reader := &recordingReader{
		reader: bytes.NewReader(body),
		events: &events,
	}
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
		"read request",
		"redeem ticket",
		"consume replay claim",
		"new loopback handler",
		"ensure loopback identity",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

// TestRunFailsFastWithoutRequiredDependency verifies incomplete assembly reaches no authority or caller input.
func TestRunFailsFastWithoutRequiredDependency(t *testing.T) {
	for _, test := range []struct {
		name   string
		clock  helper.Clock
		mutate func(*runtimeDependencies)
	}{
		{
			name: "clock",
			mutate: func(*runtimeDependencies) {
			},
		},
		{
			name:  "invocation authorizer",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.authorizeInvocation = nil
			},
		},
		{
			name:  "ticket redeemer factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openTicketRedeemer = nil
			},
		},
		{
			name:  "replay guard factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openReplayGuard = nil
			},
		},
		{
			name:  "loopback handler factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.newLoopbackIdentityHandler = nil
			},
		},
		{
			name:  "resolver handler factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openResolverHandler = nil
			},
		},
		{
			name:  "trust handler factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openTrustHandler = nil
			},
		},
		{
			name:  "administrator trust handler factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openAdministratorTrustHandler = nil
			},
		},
		{
			name:  "low-port handler factory",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.openLowPortHandler = nil
			},
		},
		{
			name:  "trust identity transition",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.transitionTrustIdentity = nil
			},
		},
		{
			name:  "administrator trust identity transition",
			clock: fixedClock{now: time.Now().UTC()},
			mutate: func(dependencies *runtimeDependencies) {
				dependencies.transitionAdministratorTrustIdentity = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			_, redemption := testRedemption(now)
			events := make([]string, 0, 1)
			dependencies := successfulTestDependencies(&events, redemption)
			test.mutate(&dependencies)
			reader := &recordingReader{
				reader: bytes.NewReader([]byte("{}")),
				events: &events,
			}
			var recovered any
			func() {
				defer func() {
					recovered = recover()
				}()
				_ = run(context.Background(), reader, io.Discard, test.clock, dependencies)
			}()
			if recovered == nil {
				t.Fatal("run() did not panic without its wired dependency")
			}
			if reader.recorded || len(events) != 0 {
				t.Fatalf("request recorded = %t, events = %#v, want neither", reader.recorded, events)
			}
		})
	}
}

// TestRunStopsBeforeDurableAuthorityWhenInvocationAdmissionFails proves invalid native consent reaches no request authority.
func TestRunStopsBeforeDurableAuthorityWhenInvocationAdmissionFails(t *testing.T) {
	authorizationErr := errors.New("authorization denied")
	events := make([]string, 0, 1)
	dependencies := unusedRuntimeDependencies(t)
	dependencies.authorizeInvocation = func() error {
		events = append(events, "authorize invocation")
		return authorizationErr
	}
	reader := &recordingReader{
		reader: bytes.NewReader([]byte("{}")),
		events: &events,
	}
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
	dependencies := unusedRuntimeDependencies(t)
	dependencies.authorizeInvocation = func() error {
		events = append(events, "authorize invocation")
		return nil
	}
	dependencies.openTicketRedeemer = func() (closingTicketRedeemer, error) {
		events = append(events, "open ticket redeemer")
		return nil, openErr
	}
	reader := &recordingReader{
		reader: bytes.NewReader([]byte("{}")),
		events: &events,
	}
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
	dependencies := unusedRuntimeDependencies(t)
	dependencies.authorizeInvocation = func() error {
		events = append(events, "authorize invocation")
		return nil
	}
	dependencies.openTicketRedeemer = func() (closingTicketRedeemer, error) {
		events = append(events, "open ticket redeemer")
		return &testTicketRedeemer{
			events:   &events,
			closeErr: closeErr,
		}, nil
	}
	dependencies.openReplayGuard = func() (closingReplayGuard, error) {
		events = append(events, "open replay guard")
		return nil, openErr
	}
	reader := &recordingReader{
		reader: bytes.NewReader([]byte("{}")),
		events: &events,
	}
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

// TestRunLeavesLazyResolverClosedWhenRequestDecodingFails proves malformed requests do not construct unselected native handlers.
func TestRunLeavesLazyResolverClosedWhenRequestDecodingFails(t *testing.T) {
	events := make([]string, 0, 4)
	dependencies := unusedRuntimeDependencies(t)
	dependencies.authorizeInvocation = func() error {
		events = append(events, "authorize invocation")
		return nil
	}
	dependencies.openTicketRedeemer = func() (closingTicketRedeemer, error) {
		events = append(events, "open ticket redeemer")
		return &testTicketRedeemer{events: &events}, nil
	}
	dependencies.openReplayGuard = func() (closingReplayGuard, error) {
		events = append(events, "open replay guard")
		return &testReplayGuard{events: &events}, nil
	}
	reader := &recordingReader{
		reader: bytes.NewReader([]byte("{}")),
		events: &events,
	}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if err == nil {
		t.Fatal("run() error = nil")
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
		"read request",
		"close replay guard",
		"close ticket redeemer",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	if !reader.recorded || output.Len() == 0 {
		t.Fatalf("request recorded = %t, output = %q, want response", reader.recorded, output.String())
	}
}

// TestRunClosesEveryAuthorityAfterServeFailure proves response failure cannot strand retained privileged handles.
func TestRunClosesEveryAuthorityAfterServeFailure(t *testing.T) {
	redeemerCloseErr := errors.New("redeemer close failed")
	replayCloseErr := errors.New("replay close failed")
	events := make([]string, 0, 6)
	dependencies := unusedRuntimeDependencies(t)
	dependencies.authorizeInvocation = func() error {
		events = append(events, "authorize invocation")
		return nil
	}
	dependencies.openTicketRedeemer = func() (closingTicketRedeemer, error) {
		events = append(events, "open ticket redeemer")
		return &testTicketRedeemer{
			events:   &events,
			closeErr: redeemerCloseErr,
		}, nil
	}
	dependencies.openReplayGuard = func() (closingReplayGuard, error) {
		events = append(events, "open replay guard")
		return &testReplayGuard{
			events:   &events,
			closeErr: replayCloseErr,
		}, nil
	}
	reader := &recordingReader{
		reader: bytes.NewReader(nil),
		events: &events,
	}
	var output bytes.Buffer
	err := run(context.Background(), reader, &output, fixedClock{now: time.Now().UTC()}, dependencies)
	if err == nil || !errors.Is(err, redeemerCloseErr) || !errors.Is(err, replayCloseErr) {
		t.Fatalf("run error = %v, want request and both close failures", err)
	}
	wantEvents := []string{
		"authorize invocation",
		"open ticket redeemer",
		"open replay guard",
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
	redeemErr  error
}

// Redeem returns the independently bound test authority.
func (redeemer *testTicketRedeemer) Redeem(_ context.Context, _ helper.TicketReference) (helper.TicketRedemption, error) {
	*redeemer.events = append(*redeemer.events, "redeem ticket")
	return redeemer.redemption, redeemer.redeemErr
}

// Close records release of the retained ticket topology.
func (redeemer *testTicketRedeemer) Close() error {
	*redeemer.events = append(*redeemer.events, "close ticket redeemer")
	return redeemer.closeErr
}

// testReplayGuard records durable replay admission and lifecycle ordering.
type testReplayGuard struct {
	events     *[]string
	closeErr   error
	consumeErr error
}

// Consume records the single-use admission boundary.
func (guard *testReplayGuard) Consume(_ context.Context, _ helper.ReplayClaim) error {
	*guard.events = append(*guard.events, "consume replay claim")
	return guard.consumeErr
}

// Close records release of the retained replay directory.
func (guard *testReplayGuard) Close() error {
	*guard.events = append(*guard.events, "close replay guard")
	return guard.closeErr
}

// testResolverHandler keeps resolver mutation unavailable while exposing protected-store lifetime ordering.
type testResolverHandler struct {
	helper.UnavailableResolverHandler
	events   *[]string
	closeErr error
}

// Close records release of the retained resolver ownership store.
func (handler *testResolverHandler) Close() error {
	*handler.events = append(*handler.events, "close resolver handler")
	return handler.closeErr
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

// unusedRuntimeDependencies returns complete wiring whose callbacks fail if a test reaches an unconfigured boundary.
func unusedRuntimeDependencies(t *testing.T) runtimeDependencies {
	t.Helper()
	return runtimeDependencies{
		authorizeInvocation: func() error {
			t.Fatal("invocation authorizer was not configured for this test")
			return nil
		},
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			t.Fatal("ticket redeemer factory was not configured for this test")
			return nil, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			t.Fatal("replay guard factory was not configured for this test")
			return nil, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			t.Fatal("loopback handler factory was not configured for this test")
			return nil
		},
		openResolverHandler: func() (closingResolverHandler, error) {
			t.Fatal("resolver handler factory was not configured for this test")
			return nil, nil
		},
		openTrustHandler: func() (closingTrustHandler, error) {
			t.Fatal("trust handler factory was not configured for this test")
			return nil, nil
		},
		openAdministratorTrustHandler: func() (closingTrustHandler, error) {
			t.Fatal("administrator trust handler factory was not configured for this test")
			return nil, nil
		},
		openLowPortHandler: func() (closingLowPortHandler, error) {
			t.Fatal("low-port handler factory was not configured for this test")
			return nil, nil
		},
		transitionTrustIdentity: func(string) error {
			t.Fatal("trust identity transition was not configured for this test")
			return nil
		},
		transitionAdministratorTrustIdentity: func(string) error {
			t.Fatal("administrator trust identity transition was not configured for this test")
			return nil
		},
	}
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
			return &testTicketRedeemer{
				redemption: redemption,
				events:     events,
			}, nil
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			*events = append(*events, "open replay guard")
			return &testReplayGuard{events: events}, nil
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			*events = append(*events, "new loopback handler")
			return &testLoopbackHandler{events: events}
		},
		openResolverHandler: func() (closingResolverHandler, error) {
			*events = append(*events, "open resolver handler")
			return &testResolverHandler{events: events}, nil
		},
		openTrustHandler: func() (closingTrustHandler, error) {
			return helper.UnavailableTrustHandler{}, nil
		},
		openAdministratorTrustHandler: func() (closingTrustHandler, error) {
			return helper.UnavailableTrustHandler{}, nil
		},
		openLowPortHandler: func() (closingLowPortHandler, error) {
			return unavailableClosingLowPortHandler{}, nil
		},
		transitionTrustIdentity: func(string) error {
			return errors.New("test trust identity transition is not configured")
		},
		transitionAdministratorTrustIdentity: func(string) error {
			return errors.New("test administrator trust identity transition is not configured")
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
			TicketReference:            reference,
			RequesterIdentity:          ticket.RequesterIdentity,
			InstallationID:             ticket.InstallationID,
			OwnershipGeneration:        ticket.OwnershipGeneration,
			OwnershipSchemaVersion:     ticket.OwnershipSchemaVersion,
			NetworkPolicyFingerprint:   ticket.NetworkPolicyFingerprint,
			ApprovedPool:               ticket.ApprovedPool,
			OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
			OwnershipFingerprint:       strings.Repeat("f", 64),
			TargetOwnershipFingerprint: strings.Repeat("f", 64),
			PostOwnershipFingerprint:   strings.Repeat("f", 64),
			TicketVerifierKey:          "test-verifier-key",
		},
	}
}

var _ io.Reader = (*recordingReader)(nil)
var _ helper.LoopbackIdentityHandler = (*testLoopbackHandler)(nil)
