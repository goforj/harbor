package authority

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingNetworkResolverSetupCoordinator captures authority requests and returns independently scripted results.
type recordingNetworkResolverSetupCoordinator struct {
	mutex           sync.Mutex
	startResult     state.OperationRecord
	startErr        error
	prepareResult   ticketissuer.ResolverResult
	prepareErr      error
	confirmResult   state.CompleteNetworkResolverSetupResult
	confirmErr      error
	startRequests   []reconcile.NetworkResolverSetupStartRequest
	prepareRequests []reconcile.NetworkResolverSetupPrepareRequest
	confirmRequests []reconcile.NetworkResolverSetupConfirmRequest
	nilContexts     int
}

// Start records one authenticated resolver initiation before returning its scripted operation.
func (coordinator *recordingNetworkResolverSetupCoordinator) Start(
	ctx context.Context,
	request reconcile.NetworkResolverSetupStartRequest,
) (state.OperationRecord, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.startRequests = append(coordinator.startRequests, request)
	return coordinator.startResult, coordinator.startErr
}

// Prepare records one authenticated resolver approval selection before returning scripted helper metadata.
func (coordinator *recordingNetworkResolverSetupCoordinator) Prepare(
	ctx context.Context,
	request reconcile.NetworkResolverSetupPrepareRequest,
) (ticketissuer.ResolverResult, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.prepareRequests = append(coordinator.prepareRequests, request)
	return coordinator.prepareResult, coordinator.prepareErr
}

// Confirm records one revision-bound resolver proof before returning its scripted durable completion.
func (coordinator *recordingNetworkResolverSetupCoordinator) Confirm(
	ctx context.Context,
	request reconcile.NetworkResolverSetupConfirmRequest,
) (state.CompleteNetworkResolverSetupResult, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.confirmRequests = append(coordinator.confirmRequests, request)
	return coordinator.confirmResult, coordinator.confirmErr
}

// testNetworkResolverSetups supplies a required inert resolver collaborator to tests outside this boundary.
func testNetworkResolverSetups() networkResolverSetupCoordinator {
	return new(recordingNetworkResolverSetupCoordinator)
}

// requests returns isolated resolver request snapshots for boundary assertions.
func (coordinator *recordingNetworkResolverSetupCoordinator) requests() (
	[]reconcile.NetworkResolverSetupStartRequest,
	[]reconcile.NetworkResolverSetupPrepareRequest,
	[]reconcile.NetworkResolverSetupConfirmRequest,
	int,
) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return append([]reconcile.NetworkResolverSetupStartRequest(nil), coordinator.startRequests...),
		append([]reconcile.NetworkResolverSetupPrepareRequest(nil), coordinator.prepareRequests...),
		append([]reconcile.NetworkResolverSetupConfirmRequest(nil), coordinator.confirmRequests...),
		coordinator.nilContexts
}

// newAuthorityForNetworkResolverSetupTest builds an authority with deterministic resolver operation identity and time.
func newAuthorityForNetworkResolverSetupTest(
	coordinator networkResolverSetupCoordinator,
	now time.Time,
	newOperationID func() (domain.OperationID, error),
) *Authority {
	return newAuthorityWithIdentityFactories(
		&recordingStore{},
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{},
		func() time.Time { return now },
		func() (domain.ProjectID, error) { return "project-unused", nil },
		newOperationID,
		func() (identity.InstallationID, error) { return "installation-unused", nil },
		testProjectLifecycles(),
		testNetworkSetups(),
		coordinator,
		testHTTPRoutes(),
	)
}

// authorityNetworkResolverSetupApprovalOperation constructs one valid staged global resolver operation.
func authorityNetworkResolverSetupApprovalOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	at time.Time,
) state.OperationRecord {
	t.Helper()
	queued, err := domain.NewOperation(operationID, intentID, domain.OperationKindNetworkResolverSetup, "", at)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	running, err := queued.Transition(domain.OperationRunning, "preparing resolver", at, nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	approval, err := running.Transition(domain.OperationRequiresApproval, "awaiting resolver approval", at, nil)
	if err != nil {
		t.Fatalf("Transition(requires approval) error = %v", err)
	}
	return state.OperationRecord{Operation: approval, Revision: 3}
}

// authorityNetworkResolverSetupEvidence constructs one canonical exact resolver postcondition.
func authorityNetworkResolverSetupEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.ResolverPostconditionExact,
	}
}

// validAuthorityNetworkResolverSetupResult constructs one future helper capability for preparation tests.
func validAuthorityNetworkResolverSetupResult(now time.Time) ticketissuer.ResolverResult {
	return ticketissuer.ResolverResult{
		OperationID:          "operation-network-resolver-setup",
		Reference:            helper.TicketReference(strings.Repeat("d", 64)),
		Operation:            helper.OperationEnsureResolver,
		PolicyFingerprint:    strings.Repeat("a", 64),
		OwnershipFingerprint: strings.Repeat("b", 64),
		ExpiresAt:            now.Add(time.Minute),
	}
}

// authorityNetworkResolverSetupCompletion constructs a valid completion whose current network may have advanced past resolver setup.
func authorityNetworkResolverSetupCompletion(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	currentStage state.NetworkStage,
	currentNetworkRevision domain.Sequence,
	at time.Time,
) state.CompleteNetworkResolverSetupResult {
	t.Helper()
	approval := authorityNetworkResolverSetupApprovalOperation(t, operationID, intentID, at)
	running, err := approval.Operation.Transition(domain.OperationRunning, "committing resolver", at.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(committing) error = %v", err)
	}
	succeeded, err := running.Transition(domain.OperationSucceeded, "completed", at.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(completed) error = %v", err)
	}
	network := state.NetworkRecord{
		Stage:       currentStage,
		Revision:    currentNetworkRevision,
		CreatedAt:   at,
		UpdatedAt:   at.Add(time.Minute),
		Ownership:   identity.Ownership{InstallationID: "installation-authority", Generation: 1},
		Pool:        authorityNetworkSetupPool(t, "127.42.0.0/29"),
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: state.DataPlaneReservations{
			Endpoints:            []state.EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
	if currentStage == state.NetworkStageFull {
		network.Reservations.Listeners = authorityNetworkResolverSetupListeners(at.Add(time.Minute))
	}
	result := state.CompleteNetworkResolverSetupResult{
		Operation:       state.OperationRecord{Operation: succeeded, Revision: 6},
		NetworkRevision: 5,
		Network:         state.NetworkMutationResult{Record: network, Replayed: true},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("CompleteNetworkResolverSetupResult.Validate() error = %v", err)
	}
	return result
}

// authorityNetworkResolverSetupListeners constructs one direct listener generation outside the project pool.
func authorityNetworkResolverSetupListeners(at time.Time) state.SharedListenerReservations {
	listener := func(endpoint string) state.ListenerReservation {
		socket := netip.MustParseAddrPort(endpoint)
		return state.ListenerReservation{
			Mode:       state.ListenerModeDirect,
			Advertised: socket,
			Bind:       socket,
			Generation: 1,
			VerifiedAt: at,
		}
	}
	return state.SharedListenerReservations{
		DNS:   listener("127.0.0.1:53"),
		HTTP:  listener("127.0.0.1:80"),
		HTTPS: listener("127.0.0.1:443"),
	}
}

// TestAuthorityStartNetworkResolverSetupBindsAuthenticatedIdentityAndAllowsIntentReplay verifies daemon identity cannot replace caller ownership.
func TestAuthorityStartNetworkResolverSetupBindsAuthenticatedIdentityAndAllowsIntentReplay(t *testing.T) {
	at := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	replayed := authorityNetworkResolverSetupApprovalOperation(t, "operation-existing", "intent-network-resolver-setup", at)
	coordinator := &recordingNetworkResolverSetupCoordinator{startResult: replayed}
	authority := newAuthorityForNetworkResolverSetupTest(
		coordinator,
		at,
		func() (domain.OperationID, error) { return "operation-proposed", nil },
	)
	caller := control.Caller{}
	caller.Transport.UserID = "S-1-5-21-1000"

	result, err := authority.StartNetworkResolverSetup(nil, caller, control.StartNetworkResolverSetupRequest{
		IntentID: replayed.Operation.IntentID,
	})
	if err != nil {
		t.Fatalf("StartNetworkResolverSetup() error = %v", err)
	}
	want := control.NetworkResolverSetupOperation{Operation: replayed.Operation, Revision: replayed.Revision}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("StartNetworkResolverSetup() = %#v, want %#v", result, want)
	}
	starts, prepares, confirms, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkResolverSetupStartRequest{
		OperationID:       "operation-proposed",
		IntentID:          replayed.Operation.IntentID,
		RequesterIdentity: caller.Transport.UserID,
	}
	if !reflect.DeepEqual(starts, []reconcile.NetworkResolverSetupStartRequest{wantRequest}) {
		t.Fatalf("resolver coordinator start requests = %#v, want %#v", starts, wantRequest)
	}
	if len(prepares) != 0 || len(confirms) != 0 || nilContexts != 0 {
		t.Fatalf("unexpected prepare/confirm/nil-context counts = %d/%d/%d", len(prepares), len(confirms), nilContexts)
	}
}

// TestAuthorityStartNetworkResolverSetupValidatesBeforeEntropy prevents malformed client input from consuming daemon identity.
func TestAuthorityStartNetworkResolverSetupValidatesBeforeEntropy(t *testing.T) {
	coordinator := new(recordingNetworkResolverSetupCoordinator)
	identityCalls := 0
	authority := newAuthorityForNetworkResolverSetupTest(
		coordinator,
		time.Now(),
		func() (domain.OperationID, error) {
			identityCalls++
			return "operation-unused", nil
		},
	)

	if _, err := authority.StartNetworkResolverSetup(t.Context(), control.Caller{}, control.StartNetworkResolverSetupRequest{}); err == nil {
		t.Fatal("StartNetworkResolverSetup() error = nil")
	}
	starts, _, _, _ := coordinator.requests()
	if identityCalls != 0 || len(starts) != 0 {
		t.Fatalf("identity/coordinator calls = %d/%d, want 0/0", identityCalls, len(starts))
	}
}

// TestAuthorityPrepareNetworkResolverSetupApprovalMapsOnlyBoundedTicketMetadata verifies caller binding and non-secret projection.
func TestAuthorityPrepareNetworkResolverSetupApprovalMapsOnlyBoundedTicketMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	prepared := validAuthorityNetworkResolverSetupResult(now)
	coordinator := &recordingNetworkResolverSetupCoordinator{prepareResult: prepared}
	authority := newAuthorityForNetworkResolverSetupTest(
		coordinator,
		now,
		func() (domain.OperationID, error) { return "operation-unused", nil },
	)
	caller := control.Caller{}
	caller.Transport.UserID = "501"
	request := control.PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               prepared.OperationID,
		ExpectedOperationRevision: 3,
	}

	result, err := authority.PrepareNetworkResolverSetupApproval(nil, caller, request)
	if err != nil {
		t.Fatalf("PrepareNetworkResolverSetupApproval() error = %v", err)
	}
	want := control.NetworkResolverSetupApprovalPreparation{
		OperationID:       request.OperationID,
		OperationRevision: request.ExpectedOperationRevision,
		Ticket: control.NetworkResolverSetupApprovalTicket{
			OperationID:                prepared.OperationID,
			Reference:                  prepared.Reference,
			Operation:                  prepared.Operation,
			PolicyFingerprint:          prepared.PolicyFingerprint,
			TargetOwnershipFingerprint: prepared.OwnershipFingerprint,
			ExpiresAt:                  prepared.ExpiresAt,
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("PrepareNetworkResolverSetupApproval() = %#v, want %#v", result, want)
	}
	_, prepares, _, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkResolverSetupPrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	}
	if !reflect.DeepEqual(prepares, []reconcile.NetworkResolverSetupPrepareRequest{wantRequest}) || nilContexts != 0 {
		t.Fatalf("resolver coordinator prepare requests = %#v, want %#v; nil contexts = %d", prepares, wantRequest, nilContexts)
	}
}

// TestAuthorityConfirmNetworkResolverSetupApprovalPreservesHistoricalRevisionAfterFullProgression verifies restart replay does not report a later aggregate revision.
func TestAuthorityConfirmNetworkResolverSetupApprovalPreservesHistoricalRevisionAfterFullProgression(t *testing.T) {
	at := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	completed := authorityNetworkResolverSetupCompletion(
		t,
		"operation-network-resolver-setup",
		"intent-network-resolver-setup",
		state.NetworkStageFull,
		9,
		at,
	)
	coordinator := &recordingNetworkResolverSetupCoordinator{confirmResult: completed}
	authority := newAuthorityForNetworkResolverSetupTest(
		coordinator,
		at,
		func() (domain.OperationID, error) { return "operation-unused", nil },
	)
	request := control.ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               completed.Operation.Operation.ID,
		ExpectedOperationRevision: 3,
		ResolverEvidence:          authorityNetworkResolverSetupEvidence(),
	}

	result, err := authority.ConfirmNetworkResolverSetupApproval(nil, control.Caller{}, request)
	if err != nil {
		t.Fatalf("ConfirmNetworkResolverSetupApproval() error = %v", err)
	}
	want := control.NetworkResolverSetupApprovalConfirmation{
		Operation:       completed.Operation.Operation,
		Revision:        completed.Operation.Revision,
		NetworkRevision: completed.NetworkRevision,
	}
	if !reflect.DeepEqual(result, want) || result.NetworkRevision == completed.Network.Record.Revision {
		t.Fatalf("ConfirmNetworkResolverSetupApproval() = %#v, want historical %#v with current network revision %d", result, want, completed.Network.Record.Revision)
	}
	_, _, confirms, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkResolverSetupConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ResolverEvidence:          request.ResolverEvidence,
	}
	if !reflect.DeepEqual(confirms, []reconcile.NetworkResolverSetupConfirmRequest{wantRequest}) || nilContexts != 0 {
		t.Fatalf("resolver coordinator confirm requests = %#v, want %#v; nil contexts = %d", confirms, wantRequest, nilContexts)
	}
}

// TestAuthorityNetworkResolverSetupRejectsUncorrelatedCoordinatorResults prevents replay or helper metadata from crossing operation boundaries.
func TestAuthorityNetworkResolverSetupRejectsUncorrelatedCoordinatorResults(t *testing.T) {
	now := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	t.Run("start intent", func(t *testing.T) {
		result := authorityNetworkResolverSetupApprovalOperation(t, "operation-resolver", "intent-other", now)
		authority := newAuthorityForNetworkResolverSetupTest(
			&recordingNetworkResolverSetupCoordinator{startResult: result},
			now,
			func() (domain.OperationID, error) { return "operation-proposed", nil },
		)
		_, err := authority.StartNetworkResolverSetup(t.Context(), control.Caller{}, control.StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"})
		assertInternalNetworkResolverSetupError(t, err)
	})
	t.Run("prepare operation", func(t *testing.T) {
		result := validAuthorityNetworkResolverSetupResult(now)
		result.OperationID = "operation-other"
		authority := newAuthorityForNetworkResolverSetupTest(
			&recordingNetworkResolverSetupCoordinator{prepareResult: result},
			now,
			func() (domain.OperationID, error) { return "operation-unused", nil },
		)
		caller := control.Caller{}
		caller.Transport.UserID = "501"
		_, err := authority.PrepareNetworkResolverSetupApproval(t.Context(), caller, control.PrepareNetworkResolverSetupApprovalRequest{
			OperationID: "operation-network-resolver-setup", ExpectedOperationRevision: 3,
		})
		assertInternalNetworkResolverSetupError(t, err)
	})
	t.Run("confirm historical revision", func(t *testing.T) {
		result := authorityNetworkResolverSetupCompletion(t, "operation-network-resolver-setup", "intent-resolver", state.NetworkStageResolver, 5, now)
		result.NetworkRevision++
		authority := newAuthorityForNetworkResolverSetupTest(
			&recordingNetworkResolverSetupCoordinator{confirmResult: result},
			now,
			func() (domain.OperationID, error) { return "operation-unused", nil },
		)
		_, err := authority.ConfirmNetworkResolverSetupApproval(t.Context(), control.Caller{}, control.ConfirmNetworkResolverSetupApprovalRequest{
			OperationID: "operation-network-resolver-setup", ExpectedOperationRevision: 3,
			ResolverEvidence: authorityNetworkResolverSetupEvidence(),
		})
		assertInternalNetworkResolverSetupError(t, err)
	})
}

// TestAuthorityNetworkResolverSetupValidatesSelectionsBeforeCoordination keeps malformed approval input outside reconciliation.
func TestAuthorityNetworkResolverSetupValidatesSelectionsBeforeCoordination(t *testing.T) {
	coordinator := new(recordingNetworkResolverSetupCoordinator)
	authority := newAuthorityForNetworkResolverSetupTest(
		coordinator,
		time.Now(),
		func() (domain.OperationID, error) { return "operation-unused", nil },
	)

	if _, err := authority.PrepareNetworkResolverSetupApproval(t.Context(), control.Caller{}, control.PrepareNetworkResolverSetupApprovalRequest{}); err == nil {
		t.Fatal("PrepareNetworkResolverSetupApproval() error = nil")
	}
	if _, err := authority.ConfirmNetworkResolverSetupApproval(t.Context(), control.Caller{}, control.ConfirmNetworkResolverSetupApprovalRequest{}); err == nil {
		t.Fatal("ConfirmNetworkResolverSetupApproval() error = nil")
	}
	_, prepares, confirms, _ := coordinator.requests()
	if len(prepares) != 0 || len(confirms) != 0 {
		t.Fatalf("invalid resolver prepare/confirm requests reached coordinator: %#v / %#v", prepares, confirms)
	}
}

// TestAuthorityNetworkResolverSetupMapsCoordinatorErrors verifies each public phase applies the reviewed resolver taxonomy.
func TestAuthorityNetworkResolverSetupMapsCoordinatorErrors(t *testing.T) {
	now := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	caller := control.Caller{}
	caller.Transport.UserID = "501"
	for _, test := range []struct {
		name      string
		cause     error
		wantCode  rpc.ErrorCode
		configure func(*recordingNetworkResolverSetupCoordinator, error)
		call      func(*Authority) error
	}{
		{
			name: "start conflict", cause: &state.IntentConflictError{IntentID: "intent-network-resolver-setup"}, wantCode: rpc.ErrorCodeConflict,
			configure: func(coordinator *recordingNetworkResolverSetupCoordinator, cause error) {
				coordinator.startErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.StartNetworkResolverSetup(t.Context(), caller, control.StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"})
				return err
			},
		},
		{
			name: "prepare missing", cause: &state.OperationNotFoundError{OperationID: "operation-network-resolver-setup"}, wantCode: rpc.ErrorCodeNotFound,
			configure: func(coordinator *recordingNetworkResolverSetupCoordinator, cause error) {
				coordinator.prepareErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.PrepareNetworkResolverSetupApproval(t.Context(), caller, control.PrepareNetworkResolverSetupApprovalRequest{
					OperationID: "operation-network-resolver-setup", ExpectedOperationRevision: 3,
				})
				return err
			},
		},
		{
			name: "prepare helper missing", cause: fmt.Errorf("open resolver spool: %w", ticketspool.ErrNotInstalled), wantCode: rpc.ErrorCodePrivilegedHelperRequired,
			configure: func(coordinator *recordingNetworkResolverSetupCoordinator, cause error) {
				coordinator.prepareErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.PrepareNetworkResolverSetupApproval(t.Context(), caller, control.PrepareNetworkResolverSetupApprovalRequest{
					OperationID: "operation-network-resolver-setup", ExpectedOperationRevision: 3,
				})
				return err
			},
		},
		{
			name: "confirm stale", cause: &state.StaleRevisionError{OperationID: "operation-network-resolver-setup", Expected: 3, Actual: 4}, wantCode: rpc.ErrorCodeConflict,
			configure: func(coordinator *recordingNetworkResolverSetupCoordinator, cause error) {
				coordinator.confirmErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.ConfirmNetworkResolverSetupApproval(t.Context(), caller, control.ConfirmNetworkResolverSetupApprovalRequest{
					OperationID: "operation-network-resolver-setup", ExpectedOperationRevision: 3,
					ResolverEvidence: authorityNetworkResolverSetupEvidence(),
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := new(recordingNetworkResolverSetupCoordinator)
			test.configure(coordinator, test.cause)
			authority := newAuthorityForNetworkResolverSetupTest(
				coordinator,
				now,
				func() (domain.OperationID, error) { return "operation-proposed", nil },
			)
			err := test.call(authority)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.wantCode || !errors.Is(err, test.cause) {
				t.Fatalf("authority error = %#v, want %s wrapping %v", err, test.wantCode, test.cause)
			}
		})
	}
}

// TestClassifyNetworkResolverSetupErrorCoversReviewedFailures keeps safe resolver failures distinct from daemon internals.
func TestClassifyNetworkResolverSetupErrorCoversReviewedFailures(t *testing.T) {
	operationID := domain.OperationID("operation-network-resolver-setup")
	intentID := domain.IntentID("intent-network-resolver-setup")
	for _, test := range []struct {
		name     string
		cause    error
		wantCode rpc.ErrorCode
	}{
		{name: "intent conflict", cause: &state.IntentConflictError{IntentID: intentID}, wantCode: rpc.ErrorCodeConflict},
		{name: "stale revision", cause: &state.StaleRevisionError{OperationID: operationID, Expected: 3, Actual: 4}, wantCode: rpc.ErrorCodeConflict},
		{name: "network missing", cause: &state.NetworkNotInitializedError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "network revision", cause: &state.NetworkRevisionConflictError{Expected: 4, Actual: 5}, wantCode: rpc.ErrorCodeConflict},
		{name: "resolver activation", cause: &state.NetworkResolverActivationConflictError{ActualRevision: 5, Difference: "resolver proof"}, wantCode: rpc.ErrorCodeConflict},
		{name: "completion conflict", cause: &state.NetworkResolverSetupCompletionConflictError{OperationID: operationID, Difference: "resolver evidence"}, wantCode: rpc.ErrorCodeConflict},
		{name: "publication indeterminate", cause: ticketissuer.ErrResolverPublicationIndeterminate, wantCode: rpc.ErrorCodeConflict},
		{name: "operation missing", cause: &state.OperationNotFoundError{OperationID: operationID}, wantCode: rpc.ErrorCodeNotFound},
		{name: "privileged helper missing", cause: fmt.Errorf("open resolver spool: %w", ticketspool.ErrNotInstalled), wantCode: rpc.ErrorCodePrivilegedHelperRequired},
		{name: "privileged helper unsafe", cause: fmt.Errorf("open resolver spool: %w", ticketspool.ErrUnsafePath), wantCode: rpc.ErrorCodePrivilegedHelperUnsafe},
		{name: "operation collision", cause: &state.OperationIDConflictError{OperationID: operationID, ExistingIntentID: "intent-other", RequestedIntentID: intentID}},
		{name: "cancelled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "unknown", cause: errors.New("database unavailable")},
		{name: "corrupt conflict", cause: &state.CorruptStateError{Entity: "network", Key: "singleton", Cause: &state.NetworkRevisionConflictError{Expected: 4, Actual: 5}}},
		{name: "corrupt missing", cause: &state.CorruptStateError{Entity: "operation", Key: string(operationID), Cause: &state.OperationNotFoundError{OperationID: operationID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("network resolver setup boundary: %w", test.cause)
			classified := classifyNetworkResolverSetupError(wrapped)
			if !errors.Is(classified, test.cause) {
				t.Fatalf("classified error = %v, want original cause %v", classified, test.cause)
			}
			var handlerError *session.HandlerError
			if test.wantCode == "" {
				if errors.As(classified, &handlerError) {
					t.Fatalf("classified error = %#v, want internal", classified)
				}
				return
			}
			if !errors.As(classified, &handlerError) || handlerError.Code() != test.wantCode {
				t.Fatalf("classified error = %#v, want %s", classified, test.wantCode)
			}
		})
	}
}

// assertInternalNetworkResolverSetupError verifies coordinator-output failures remain daemon-internal.
func assertInternalNetworkResolverSetupError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("network resolver setup error = nil")
	}
	var handlerError *session.HandlerError
	if errors.As(err, &handlerError) {
		t.Fatalf("network resolver setup error = %#v, want internal", err)
	}
}
