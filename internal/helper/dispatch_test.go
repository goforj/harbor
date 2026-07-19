package helper

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDispatcherDispatchAllowlistedOperations verifies both operation-specific handler paths use redeemed tickets.
func TestDispatcherDispatchAllowlistedOperations(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		operation Operation
		state     ObservationState
	}{
		{name: "ensure", operation: OperationEnsureLoopbackIdentity, state: ObservationOwned},
		{name: "release", operation: OperationReleaseLoopbackIdentity, state: ObservationAbsent},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference := testTicketReference()
			ticket := validTestTicket(now, test.operation)
			handler := newTestLoopbackHandler()
			dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), handler)
			response, err := dispatcher.Dispatch(nil, validTestRequest(reference))
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if !response.OK || response.Error != nil || response.Result == nil {
				t.Fatalf("unexpected response: %#v", response)
			}
			if response.Result.Operation != test.operation {
				t.Fatalf("operation = %q, want %q", response.Result.Operation, test.operation)
			}
			if response.Result.Evidence.Observation.State != test.state {
				t.Fatalf("state = %q, want %q", response.Result.Evidence.Observation.State, test.state)
			}
		})
	}
}

// TestDispatcherDispatchEnsuresLoopbackPool verifies one replay claim reaches one aggregate handler call and returns only pool evidence.
func TestDispatcherDispatchEnsuresLoopbackPool(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestPoolTicket(now)
	guard := newTestReplayGuard()
	handler := newTestLoopbackHandler()
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), guard, handler)

	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Result.Operation != OperationEnsureLoopbackPool || response.Result.Evidence != (MutationEvidence{}) || response.Result.PoolEvidence == nil {
		t.Fatalf("unexpected pool result: %#v", response.Result)
	}
	if !reflect.DeepEqual(*response.Result.PoolEvidence, handler.poolEvidence) {
		t.Fatalf("pool evidence = %#v, want %#v", *response.Result.PoolEvidence, handler.poolEvidence)
	}
	if guard.consumeCount() != 1 || handler.callCount() != 1 || handler.poolCallCount() != 1 {
		t.Fatalf("replay/handler/pool calls = %d/%d/%d, want 1/1/1", guard.consumeCount(), handler.callCount(), handler.poolCallCount())
	}
}

// TestDispatcherDispatchPassesOnlyOpaqueReferenceToRedeemer verifies request data cannot select an adapter or ticket.
func TestDispatcherDispatchPassesOnlyOpaqueReferenceToRedeemer(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := TicketReference(strings.Repeat("a", ticketReferenceLength))
	redeemer := newTestTicketRedeemer(reference, validTestTicket(now, OperationEnsureLoopbackIdentity))
	dispatcher := NewDispatcher(redeemer, newTestClock(now), newTestReplayGuard(), newTestLoopbackHandler())

	if _, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := redeemer.redeemedReferences(); len(got) != 1 || got[0] != reference {
		t.Fatalf("redeemed references = %#v, want only %q", got, reference)
	}
}

// TestDispatcherDispatchBoundsTicketRedemption proves a fixed adapter cannot hold the elevated helper indefinitely.
func TestDispatcherDispatchBoundsTicketRedemption(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	redeemer := newTestTicketRedeemer(reference, validTestTicket(now, OperationEnsureLoopbackIdentity))
	dispatcher := NewDispatcher(redeemer, newTestClock(now), newTestReplayGuard(), newTestLoopbackHandler())

	started := time.Now()
	if _, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	deadline := redeemer.redemptionDeadline()
	if deadline.IsZero() {
		t.Fatal("ticket redeemer received no deadline")
	}
	if duration := deadline.Sub(started); duration <= 0 || duration > MaxTicketRedemptionDuration+time.Second {
		t.Fatalf("redemption deadline duration = %s, want within %s", duration, MaxTicketRedemptionDuration)
	}
}

// TestDispatcherDispatchRejectsReplay verifies a ticket nonce remains single-use if redemption is repeated.
func TestDispatcherDispatchRejectsReplay(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), newTestLoopbackHandler())
	request := validTestRequest(reference)

	if _, err := dispatcher.Dispatch(context.Background(), request); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	response, err := dispatcher.Dispatch(context.Background(), request)
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("second dispatch error = %v, want ErrReplay", err)
	}
	if response.OK || response.Error == nil || response.Error.Code != ErrorCodeReplayedTicket {
		t.Fatalf("unexpected replay response: %#v", response)
	}
}

// TestDispatcherDispatchConcurrentReplay permits exactly one concurrent use of a redeemed ticket.
func TestDispatcherDispatchConcurrentReplay(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	handler := newTestLoopbackHandler()
	dispatcher := NewDispatcher(
		newTestTicketRedeemer(reference, validTestTicket(now, OperationEnsureLoopbackIdentity)),
		newTestClock(now),
		newTestReplayGuard(),
		handler,
	)
	request := validTestRequest(reference)
	const callers = 12

	var waitGroup sync.WaitGroup
	waitGroup.Add(callers)
	results := make(chan error, callers)
	for range callers {
		go func() {
			defer waitGroup.Done()
			_, err := dispatcher.Dispatch(context.Background(), request)
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)

	successes := 0
	replays := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrReplay):
			replays++
		default:
			t.Fatalf("unexpected dispatch error: %v", err)
		}
	}
	if successes != 1 || replays != callers-1 {
		t.Fatalf("successes/replays = %d/%d, want 1/%d", successes, replays, callers-1)
	}
	if got := handler.callCount(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

// TestDispatcherDispatchRejectsInvalidRequestBeforeRedemption verifies malformed references reach no trusted dependency.
func TestDispatcherDispatchRejectsInvalidRequestBeforeRedemption(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	redeemer := newTestTicketRedeemer(reference, validTestTicket(now, OperationEnsureLoopbackIdentity))
	clock := newTestClock(now)
	guard := newTestReplayGuard()
	dispatcher := NewDispatcher(redeemer, clock, guard, newTestLoopbackHandler())

	response, err := dispatcher.Dispatch(context.Background(), Request{Version: ProtocolVersion, TicketReference: "short"})
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeInvalidTicket {
		t.Fatalf("unexpected invalid-request result: response=%#v error=%v", response, err)
	}
	if redeemer.callCount() != 0 || clock.callCount() != 0 || guard.consumeCount() != 0 {
		t.Fatalf("redeemer/clock/replay calls = %d/%d/%d, want 0/0/0", redeemer.callCount(), clock.callCount(), guard.consumeCount())
	}
}

// TestDispatcherDispatchRejectsInvalidRedeemedTicketBeforeReplay verifies semantic ticket failure consumes no replay claim.
func TestDispatcherDispatchRejectsInvalidRedeemedTicketBeforeReplay(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	ticket.ExpiresAt = now
	guard := newTestReplayGuard()
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), guard, newTestLoopbackHandler())

	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeInvalidTicket {
		t.Fatalf("unexpected invalid-ticket result: response=%#v error=%v", response, err)
	}
	if guard.consumeCount() != 0 {
		t.Fatalf("replay consumes = %d, want 0", guard.consumeCount())
	}
}

// TestDispatcherDispatchRejectsUnboundPreAssignmentBeforeReplay keeps absent-state mutation authority out of handlers.
func TestDispatcherDispatchRejectsUnboundPreAssignmentBeforeReplay(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	ticket.ExpectedPreAssignment = nil
	guard := newTestReplayGuard()
	handler := newTestLoopbackHandler()
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), guard, handler)

	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeInvalidTicket {
		t.Fatalf("unexpected unbound-ticket result: response=%#v error=%v", response, err)
	}
	if guard.consumeCount() != 0 || handler.callCount() != 0 {
		t.Fatalf("replay/handler calls = %d/%d, want 0/0", guard.consumeCount(), handler.callCount())
	}
}

// TestDispatcherDispatchConsumesFailedMutation verifies a handler failure cannot make a ticket reusable.
func TestDispatcherDispatchConsumesFailedMutation(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	handler := newTestLoopbackHandler()
	handler.err = errors.New("platform failure")
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), handler)
	request := validTestRequest(reference)

	response, err := dispatcher.Dispatch(context.Background(), request)
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
		t.Fatalf("unexpected mutation failure: response=%#v error=%v", response, err)
	}
	response, err = dispatcher.Dispatch(context.Background(), request)
	if !errors.Is(err, ErrReplay) || response.Error == nil || response.Error.Code != ErrorCodeReplayedTicket {
		t.Fatalf("unexpected replay after failure: response=%#v error=%v", response, err)
	}
}

// TestDispatcherDispatchBoundsHandlerByExpiry proves mutation work cannot outlive ticket admission.
func TestDispatcherDispatchBoundsHandlerByExpiry(t *testing.T) {
	now := time.Now().UTC()
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	ticket.ExpiresAt = now.Add(5 * time.Millisecond)
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), blockingLoopbackHandler{})

	started := time.Now()
	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dispatch error = %v, want context deadline exceeded", err)
	}
	if response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
		t.Fatalf("unexpected deadline response: %#v", response)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("expiry dispatch took %s, want under one second", elapsed)
	}
}

// TestDispatcherDispatchMapsUnavailableBoundaries verifies all fail-closed seed adapters have stable errors.
func TestDispatcherDispatchMapsUnavailableBoundaries(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	request := validTestRequest(reference)
	clock := newTestClock(now)

	redemptionDispatcher := NewDispatcher(UnavailableTicketRedeemer{}, clock, newTestReplayGuard(), newTestLoopbackHandler())
	response, err := redemptionDispatcher.Dispatch(context.Background(), request)
	if !errors.Is(err, ErrTicketRedemptionUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeAuthenticationUnavailable {
		t.Fatalf("unexpected redemption-unavailable result: response=%#v error=%v", response, err)
	}

	replayDispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), clock, UnavailableReplayGuard{}, newTestLoopbackHandler())
	response, err = replayDispatcher.Dispatch(context.Background(), request)
	if !errors.Is(err, ErrReplayProtectionUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeReplayProtectionUnavailable {
		t.Fatalf("unexpected replay-unavailable result: response=%#v error=%v", response, err)
	}

	mutationDispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), clock, newTestReplayGuard(), UnavailableLoopbackIdentityHandler{})
	response, err = mutationDispatcher.Dispatch(context.Background(), request)
	if !errors.Is(err, ErrMutationUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeMutationUnavailable {
		t.Fatalf("unexpected mutation-unavailable result: response=%#v error=%v", response, err)
	}

	if _, err := (UnavailableLoopbackIdentityHandler{}).ReleaseLoopbackIdentity(context.Background(), ticket); !errors.Is(err, ErrMutationUnavailable) {
		t.Fatalf("release unavailable error = %v, want ErrMutationUnavailable", err)
	}
	if _, err := (UnavailableLoopbackIdentityHandler{}).EnsureLoopbackPool(context.Background(), validTestPoolTicket(now)); !errors.Is(err, ErrMutationUnavailable) {
		t.Fatalf("pool unavailable error = %v, want ErrMutationUnavailable", err)
	}
}

// TestDispatcherDispatchRejectsReferenceOutcomes verifies lookup details remain opaque and reach no replay state.
func TestDispatcherDispatchRejectsReferenceOutcomes(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
	}{
		{name: "unknown", err: ErrTicketReferenceUnknown},
		{name: "stale", err: ErrTicketReferenceStale},
		{name: "redeemed", err: ErrTicketReferenceRedeemed},
		{name: "adapter failure", err: errors.New("private adapter detail")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redeemer := newTestTicketRedeemer(testTicketReference(), validTestTicket(now, OperationEnsureLoopbackIdentity))
			redeemer.err = test.err
			clock := newTestClock(now)
			guard := newTestReplayGuard()
			dispatcher := NewDispatcher(redeemer, clock, guard, newTestLoopbackHandler())

			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(testTicketReference()))
			if !errors.Is(err, ErrTicketRedemptionFailed) {
				t.Fatalf("dispatch error = %v, want redemption failure", err)
			}
			if test.err != nil && (test.name == "unknown" || test.name == "stale" || test.name == "redeemed") && !errors.Is(err, test.err) {
				t.Fatalf("dispatch error = %v, want preserved outcome %v", err, test.err)
			}
			if response.Error == nil || response.Error.Code != ErrorCodeAuthenticationFailed || response.Error.Message != "helper ticket redemption failed" {
				t.Fatalf("unexpected reference response: %#v", response)
			}
			if clock.callCount() != 0 || guard.consumeCount() != 0 {
				t.Fatalf("clock/replay calls = %d/%d, want 0/0", clock.callCount(), guard.consumeCount())
			}
		})
	}
}

// TestDispatcherDispatchRejectsAdmissionBindingMismatch protects every independently authenticated binding.
func TestDispatcherDispatchRejectsAdmissionBindingMismatch(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	tests := []struct {
		name   string
		mutate func(*TicketAdmission)
	}{
		{name: "wrong reference", mutate: func(admission *TicketAdmission) {
			admission.TicketReference = TicketReference(strings.Repeat("b", ticketReferenceLength))
		}},
		{name: "wrong requester", mutate: func(admission *TicketAdmission) { admission.RequesterIdentity = "uid-2000" }},
		{name: "wrong installation", mutate: func(admission *TicketAdmission) { admission.InstallationID = "other-installation" }},
		{name: "wrong generation", mutate: func(admission *TicketAdmission) { admission.OwnershipGeneration++ }},
		{name: "wrong pool", mutate: func(admission *TicketAdmission) { admission.ApprovedPool = "127.78.0.0/24" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redemption := redemptionForTicket(reference, ticket)
			test.mutate(&redemption.Admission)
			redeemer := newTestTicketRedeemer(reference, ticket)
			redeemer.redemption = redemption
			clock := newTestClock(now)
			guard := newTestReplayGuard()
			handler := newTestLoopbackHandler()
			dispatcher := NewDispatcher(redeemer, clock, guard, handler)

			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if !errors.Is(err, ErrTicketRedemptionFailed) {
				t.Fatalf("dispatch error = %v, want redemption failure", err)
			}
			if response.Error == nil || response.Error.Code != ErrorCodeAuthenticationFailed {
				t.Fatalf("unexpected redemption response: %#v", response)
			}
			if clock.callCount() != 0 || guard.consumeCount() != 0 || handler.callCount() != 0 {
				t.Fatalf("clock/guard/handler calls = %d/%d/%d, want 0/0/0", clock.callCount(), guard.consumeCount(), handler.callCount())
			}
		})
	}
}

// TestTicketRedemptionValidateLeavesSignedFieldsToRedeemer keeps admission bindings from duplicating signed-ticket authentication.
func TestTicketRedemptionValidateLeavesSignedFieldsToRedeemer(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	tests := []struct {
		name   string
		mutate func(*Ticket)
	}{
		{name: "version", mutate: func(ticket *Ticket) { ticket.Version++ }},
		{name: "operation", mutate: func(ticket *Ticket) {
			ticket.Operation = OperationReleaseLoopbackIdentity
			ticket.ExpectedObservation.State = ObservationOwned
		}},
		{name: "address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "127.77.0.11" }},
		{name: "observation", mutate: func(ticket *Ticket) { ticket.ExpectedObservation.Fingerprint = strings.Repeat("b", fingerprintLength) }},
		{name: "nonce", mutate: func(ticket *Ticket) { ticket.Nonce = strings.Repeat("z", minimumNonceLength) }},
		{name: "expiry", mutate: func(ticket *Ticket) { ticket.ExpiresAt = ticket.ExpiresAt.Add(time.Second) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redemption := redemptionForTicket(reference, validTestTicket(now, OperationEnsureLoopbackIdentity))
			test.mutate(&redemption.Ticket)
			if err := redemption.validate(reference); err != nil {
				t.Fatalf("validate redemption: %v", err)
			}
		})
	}
}

// TestDispatcherDispatchReadsClockAfterRedemption proves blocking redemption cannot leave admission with stale time.
func TestDispatcherDispatchReadsClockAfterRedemption(t *testing.T) {
	issuedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(issuedAt, OperationEnsureLoopbackIdentity)
	redeemer := newTestTicketRedeemer(reference, ticket)
	redemptionCompleted := false
	redeemer.beforeReturn = func() { redemptionCompleted = true }
	clock := newTestClock(issuedAt)
	clock.beforeNow = func() {
		if !redemptionCompleted {
			t.Fatal("clock read before redemption completed")
		}
	}
	dispatcher := NewDispatcher(redeemer, clock, newTestReplayGuard(), newTestLoopbackHandler())

	if _, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if clock.callCount() != 1 {
		t.Fatalf("clock calls = %d, want 1", clock.callCount())
	}
}

// TestDispatcherDispatchRejectsTicketExpiredDuringRedemption verifies time is not captured before redemption blocks.
func TestDispatcherDispatchRejectsTicketExpiredDuringRedemption(t *testing.T) {
	issuedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(issuedAt, OperationEnsureLoopbackIdentity)
	clock := newTestClock(ticket.ExpiresAt.Add(time.Nanosecond))
	guard := newTestReplayGuard()
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), clock, guard, newTestLoopbackHandler())

	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeInvalidTicket {
		t.Fatalf("unexpected expired result: response=%#v error=%v", response, err)
	}
	if clock.callCount() != 1 || guard.consumeCount() != 0 {
		t.Fatalf("clock/replay calls = %d/%d, want 1/0", clock.callCount(), guard.consumeCount())
	}
}

// TestDispatcherDispatchValidatesMutationEvidence rejects mismatched adapter postconditions.
func TestDispatcherDispatchValidatesMutationEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*MutationEvidence)
	}{
		{name: "wrong address", mutate: func(evidence *MutationEvidence) { evidence.Address = "127.77.0.11" }},
		{name: "wrong state", mutate: func(evidence *MutationEvidence) { evidence.Observation.State = ObservationAbsent }},
		{name: "invalid fingerprint", mutate: func(evidence *MutationEvidence) { evidence.Observation.Fingerprint = "bad" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference := testTicketReference()
			ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
			handler := newTestLoopbackHandler()
			test.mutate(&handler.ensureEvidence)
			dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), handler)
			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
				t.Fatalf("unexpected evidence result: response=%#v error=%v", response, err)
			}
		})
	}
}

// TestDispatcherDispatchValidatesPoolMutationEvidence rejects incomplete, unordered, or unowned aggregate postconditions.
func TestDispatcherDispatchValidatesPoolMutationEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*PoolMutationEvidence)
	}{
		{name: "wrong pool", mutate: func(evidence *PoolMutationEvidence) { evidence.Pool = "127.77.0.16/29" }},
		{name: "wrong count", mutate: func(evidence *PoolMutationEvidence) { evidence.Identities = evidence.Identities[:7] }},
		{name: "wrong address", mutate: func(evidence *PoolMutationEvidence) { evidence.Identities[3].Address = "127.77.0.12" }},
		{name: "wrong order", mutate: func(evidence *PoolMutationEvidence) {
			evidence.Identities[2], evidence.Identities[3] = evidence.Identities[3], evidence.Identities[2]
		}},
		{name: "wrong state", mutate: func(evidence *PoolMutationEvidence) { evidence.Identities[5].Observation.State = ObservationAbsent }},
		{name: "invalid fingerprint", mutate: func(evidence *PoolMutationEvidence) { evidence.Identities[6].Observation.Fingerprint = "bad" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference := testTicketReference()
			ticket := validTestPoolTicket(now)
			handler := newTestLoopbackHandler()
			test.mutate(&handler.poolEvidence)
			dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), handler)
			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
				t.Fatalf("unexpected evidence result: response=%#v error=%v", response, err)
			}
			if handler.callCount() != 1 || handler.poolCallCount() != 1 {
				t.Fatalf("handler/pool calls = %d/%d, want 1/1", handler.callCount(), handler.poolCallCount())
			}
		})
	}
}

// TestNewDispatcherRequiresDependencies verifies invalid security wiring fails immediately.
func TestNewDispatcherRequiresDependencies(t *testing.T) {
	tests := []struct {
		name     string
		redeemer TicketRedeemer
		clock    Clock
		guard    ReplayGuard
		handler  LoopbackIdentityHandler
	}{
		{name: "redeemer", clock: newTestClock(time.Now()), guard: newTestReplayGuard(), handler: newTestLoopbackHandler()},
		{name: "clock", redeemer: newTestTicketRedeemer(testTicketReference(), Ticket{}), guard: newTestReplayGuard(), handler: newTestLoopbackHandler()},
		{name: "replay guard", redeemer: newTestTicketRedeemer(testTicketReference(), Ticket{}), clock: newTestClock(time.Now()), handler: newTestLoopbackHandler()},
		{name: "handler", redeemer: newTestTicketRedeemer(testTicketReference(), Ticket{}), clock: newTestClock(time.Now()), guard: newTestReplayGuard()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor panic")
				}
			}()
			NewDispatcher(test.redeemer, test.clock, test.guard, test.handler)
		})
	}
}

type testTicketRedeemer struct {
	mutex        sync.Mutex
	redemption   TicketRedemption
	err          error
	references   []TicketReference
	deadline     time.Time
	beforeReturn func()
}

// newTestTicketRedeemer constructs a fixed adapter bound to one authenticated redemption.
func newTestTicketRedeemer(reference TicketReference, ticket Ticket) *testTicketRedeemer {
	return &testTicketRedeemer{redemption: redemptionForTicket(reference, ticket)}
}

// Redeem records only the opaque reference and returns the fixed test adapter's result.
func (r *testTicketRedeemer) Redeem(ctx context.Context, reference TicketReference) (TicketRedemption, error) {
	if err := ctx.Err(); err != nil {
		return TicketRedemption{}, err
	}
	r.mutex.Lock()
	r.references = append(r.references, reference)
	r.deadline, _ = ctx.Deadline()
	redemption := r.redemption
	err := r.err
	beforeReturn := r.beforeReturn
	r.mutex.Unlock()
	if beforeReturn != nil {
		beforeReturn()
	}
	if err != nil {
		return TicketRedemption{}, err
	}
	return redemption, nil
}

// redemptionDeadline returns the bounded context deadline observed by the adapter.
func (r *testTicketRedeemer) redemptionDeadline() time.Time {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.deadline
}

// callCount returns the number of redemption attempts observed by the test adapter.
func (r *testTicketRedeemer) callCount() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.references)
}

// redeemedReferences returns an isolated copy of every opaque reference presented to the adapter.
func (r *testTicketRedeemer) redeemedReferences() []TicketReference {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]TicketReference(nil), r.references...)
}

// redemptionForTicket keeps independent admission fixtures visibly separate from signed ticket construction.
func redemptionForTicket(reference TicketReference, ticket Ticket) TicketRedemption {
	return TicketRedemption{
		Ticket: ticket,
		Admission: TicketAdmission{
			TicketReference:     reference,
			RequesterIdentity:   "uid-1000",
			InstallationID:      "harbor-test-installation",
			OwnershipGeneration: 7,
			ApprovedPool:        ticket.ApprovedPool,
		},
	}
}

type testClock struct {
	mutex     sync.Mutex
	now       time.Time
	calls     int
	beforeNow func()
}

// newTestClock constructs a deterministic trusted clock.
func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

// Now records the read and returns the configured instant.
func (c *testClock) Now() time.Time {
	c.mutex.Lock()
	c.calls++
	now := c.now
	beforeNow := c.beforeNow
	c.mutex.Unlock()
	if beforeNow != nil {
		beforeNow()
	}
	return now
}

// callCount returns the number of trusted clock reads.
func (c *testClock) callCount() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.calls
}

type testReplayGuard struct {
	mutex    sync.Mutex
	consumed map[ReplayKey]struct{}
	count    int
}

// newTestReplayGuard constructs an atomic in-memory guard scoped only to one test process.
func newTestReplayGuard() *testReplayGuard {
	return &testReplayGuard{consumed: make(map[ReplayKey]struct{})}
}

// Consume atomically rejects claims already seen by this test guard.
func (g *testReplayGuard) Consume(ctx context.Context, claim ReplayClaim) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.count++
	if _, exists := g.consumed[claim.Key]; exists {
		return ErrReplay
	}
	g.consumed[claim.Key] = struct{}{}
	return nil
}

// consumeCount returns the number of admission attempts observed by the test guard.
func (g *testReplayGuard) consumeCount() int {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return g.count
}

type testLoopbackHandler struct {
	mutex           sync.Mutex
	ensureEvidence  MutationEvidence
	poolEvidence    PoolMutationEvidence
	releaseEvidence MutationEvidence
	err             error
	calls           int
	poolCalls       int
}

type blockingLoopbackHandler struct{}

// EnsureLoopbackIdentity waits for the ticket-derived context deadline without mutating the host.
func (blockingLoopbackHandler) EnsureLoopbackIdentity(ctx context.Context, _ Ticket) (MutationEvidence, error) {
	<-ctx.Done()
	return MutationEvidence{}, ctx.Err()
}

// EnsureLoopbackPool waits for the ticket-derived context deadline without mutating the host.
func (blockingLoopbackHandler) EnsureLoopbackPool(ctx context.Context, _ Ticket) (PoolMutationEvidence, error) {
	<-ctx.Done()
	return PoolMutationEvidence{}, ctx.Err()
}

// ReleaseLoopbackIdentity waits for the ticket-derived context deadline without mutating the host.
func (blockingLoopbackHandler) ReleaseLoopbackIdentity(ctx context.Context, _ Ticket) (MutationEvidence, error) {
	<-ctx.Done()
	return MutationEvidence{}, ctx.Err()
}

// newTestLoopbackHandler constructs a handler with canonical ensure and release postconditions.
func newTestLoopbackHandler() *testLoopbackHandler {
	return &testLoopbackHandler{
		ensureEvidence: MutationEvidence{
			Changed: true,
			Address: "127.77.0.10",
			Observation: ExpectedObservation{
				State:       ObservationOwned,
				Fingerprint: strings.Repeat("b", fingerprintLength),
			},
		},
		poolEvidence: testPoolMutationEvidence("127.77.0.8/29"),
		releaseEvidence: MutationEvidence{
			Changed: true,
			Address: "127.77.0.10",
			Observation: ExpectedObservation{
				State:       ObservationAbsent,
				Fingerprint: strings.Repeat("c", fingerprintLength),
			},
		},
	}
}

// EnsureLoopbackIdentity returns the configured ensure evidence without touching the host.
func (h *testLoopbackHandler) EnsureLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.calls++
	return h.ensureEvidence, h.err
}

// EnsureLoopbackPool returns the configured aggregate evidence without touching the host.
func (h *testLoopbackHandler) EnsureLoopbackPool(context.Context, Ticket) (PoolMutationEvidence, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.calls++
	h.poolCalls++
	return h.poolEvidence, h.err
}

// ReleaseLoopbackIdentity returns the configured release evidence without touching the host.
func (h *testLoopbackHandler) ReleaseLoopbackIdentity(context.Context, Ticket) (MutationEvidence, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.calls++
	return h.releaseEvidence, h.err
}

// callCount returns the number of mutation calls observed by the test handler.
func (h *testLoopbackHandler) callCount() int {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return h.calls
}

// poolCallCount returns the number of aggregate pool calls observed by the test handler.
func (h *testLoopbackHandler) poolCallCount() int {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return h.poolCalls
}

// testPoolMutationEvidence returns eight canonical owned postconditions for one /29.
func testPoolMutationEvidence(poolText string) PoolMutationEvidence {
	pool := netip.MustParsePrefix(poolText)
	identities := make([]MutationEvidence, 0, loopbackPoolIdentities)
	address := pool.Addr()
	for range loopbackPoolIdentities {
		identities = append(identities, MutationEvidence{
			Changed: true,
			Address: address.String(),
			Observation: ExpectedObservation{
				State:       ObservationOwned,
				Fingerprint: strings.Repeat("d", fingerprintLength),
			},
		})
		address = address.Next()
	}
	return PoolMutationEvidence{Pool: pool.String(), Identities: identities}
}
