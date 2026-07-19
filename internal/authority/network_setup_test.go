package authority

import (
	"context"
	"encoding/hex"
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

const authorityNetworkSetupAddressCount = 8

// recordingNetworkSetupCoordinator captures authority requests and returns independently scripted results.
type recordingNetworkSetupCoordinator struct {
	mutex           sync.Mutex
	startResult     state.OperationRecord
	startErr        error
	prepareResult   ticketissuer.PoolResult
	prepareErr      error
	confirmResult   state.CompleteNetworkSetupResult
	confirmErr      error
	startRequests   []reconcile.NetworkSetupStartRequest
	prepareRequests []reconcile.NetworkSetupPrepareRequest
	confirmRequests []reconcile.NetworkSetupConfirmRequest
	nilContexts     int
}

// Start records one exact setup initiation before returning its scripted operation.
func (coordinator *recordingNetworkSetupCoordinator) Start(
	ctx context.Context,
	request reconcile.NetworkSetupStartRequest,
) (state.OperationRecord, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.startRequests = append(coordinator.startRequests, request)
	return coordinator.startResult, coordinator.startErr
}

// Prepare records one authenticated approval selection before returning its scripted helper metadata.
func (coordinator *recordingNetworkSetupCoordinator) Prepare(
	ctx context.Context,
	request reconcile.NetworkSetupPrepareRequest,
) (ticketissuer.PoolResult, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.prepareRequests = append(coordinator.prepareRequests, request)
	return coordinator.prepareResult, coordinator.prepareErr
}

// Confirm records one revision-bound helper proof before returning its scripted durable completion.
func (coordinator *recordingNetworkSetupCoordinator) Confirm(
	ctx context.Context,
	request reconcile.NetworkSetupConfirmRequest,
) (state.CompleteNetworkSetupResult, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if ctx == nil {
		coordinator.nilContexts++
	}
	coordinator.confirmRequests = append(coordinator.confirmRequests, request)
	return coordinator.confirmResult, coordinator.confirmErr
}

// testNetworkSetups supplies a required inert setup collaborator to tests outside this boundary.
func testNetworkSetups() networkSetupCoordinator {
	return new(recordingNetworkSetupCoordinator)
}

// requests returns isolated setup request snapshots for boundary assertions.
func (coordinator *recordingNetworkSetupCoordinator) requests() (
	[]reconcile.NetworkSetupStartRequest,
	[]reconcile.NetworkSetupPrepareRequest,
	[]reconcile.NetworkSetupConfirmRequest,
	int,
) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return append([]reconcile.NetworkSetupStartRequest(nil), coordinator.startRequests...),
		append([]reconcile.NetworkSetupPrepareRequest(nil), coordinator.prepareRequests...),
		append([]reconcile.NetworkSetupConfirmRequest(nil), coordinator.confirmRequests...),
		coordinator.nilContexts
}

// newAuthorityForNetworkSetupTest builds an authority with deterministic setup identities and time.
func newAuthorityForNetworkSetupTest(
	coordinator networkSetupCoordinator,
	now time.Time,
	newOperationID func() (domain.OperationID, error),
	newInstallationID func() (identity.InstallationID, error),
) *Authority {
	return newAuthorityWithIdentityFactories(
		&recordingStore{},
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{},
		func() time.Time { return now },
		func() (domain.ProjectID, error) { return "project-unused", nil },
		newOperationID,
		newInstallationID,
		testProjectLifecycles(),
		coordinator,
		testHTTPRoutes(),
	)
}

// authorityNetworkSetupApprovalOperation constructs one valid staged global setup operation.
func authorityNetworkSetupApprovalOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	at time.Time,
) state.OperationRecord {
	t.Helper()
	queued, err := domain.NewOperation(operationID, intentID, domain.OperationKindNetworkSetup, "", at)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	running, err := queued.Transition(domain.OperationRunning, "preparing", at, nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	approval, err := running.Transition(domain.OperationRequiresApproval, "awaiting approval", at, nil)
	if err != nil {
		t.Fatalf("Transition(requires approval) error = %v", err)
	}
	return state.OperationRecord{Operation: approval, Revision: 3}
}

// authorityNetworkSetupPool constructs all eight canonical candidates in one loopback /29.
func authorityNetworkSetupPool(t *testing.T, rawPrefix string) identity.Pool {
	t.Helper()
	prefix := netip.MustParsePrefix(rawPrefix)
	addresses := make([]netip.Addr, authorityNetworkSetupAddressCount)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	return pool
}

// authorityNetworkSetupEvidence constructs one complete canonical helper postcondition.
func authorityNetworkSetupEvidence(prefix netip.Prefix) helper.PoolMutationEvidence {
	identities := make([]helper.MutationEvidence, authorityNetworkSetupAddressCount)
	address := prefix.Addr()
	for index := range identities {
		identities[index] = helper.MutationEvidence{
			Changed: index%2 == 0,
			Address: address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: strings.Repeat("b", 64),
			},
		}
		address = address.Next()
	}
	return helper.PoolMutationEvidence{Pool: prefix.String(), Identities: identities}
}

// authorityNetworkSetupCompletion constructs one valid atomic completion three revisions after approval.
func authorityNetworkSetupCompletion(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	pool identity.Pool,
	at time.Time,
) state.CompleteNetworkSetupResult {
	t.Helper()
	approval := authorityNetworkSetupApprovalOperation(t, operationID, intentID, at)
	running, err := approval.Operation.Transition(domain.OperationRunning, "committing", at.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(committing) error = %v", err)
	}
	succeeded, err := running.Transition(domain.OperationSucceeded, "completed", at.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("Transition(completed) error = %v", err)
	}
	result := state.CompleteNetworkSetupResult{
		Operation: state.OperationRecord{Operation: succeeded, Revision: 6},
		Network: state.NetworkMutationResult{Record: state.NetworkRecord{
			Stage:       state.NetworkStageIdentity,
			Revision:    5,
			CreatedAt:   at.Add(time.Minute),
			UpdatedAt:   at.Add(time.Minute),
			Ownership:   identity.Ownership{InstallationID: "installation-authority", Generation: 1},
			Pool:        pool,
			Leases:      []identity.Lease{},
			Quarantines: []identity.Quarantine{},
			Reservations: state.DataPlaneReservations{
				Endpoints:            []state.EndpointReservation{},
				SuppressedProjectIDs: []domain.ProjectID{},
			},
		}},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("CompleteNetworkSetupResult.Validate() error = %v", err)
	}
	return result
}

// validAuthorityNetworkSetupPoolResult constructs one future helper capability for preparation tests.
func validAuthorityNetworkSetupPoolResult(now time.Time) ticketissuer.PoolResult {
	return ticketissuer.PoolResult{
		OperationID: "operation-network-setup",
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationEnsureLoopbackPool,
		Pool:        netip.MustParsePrefix("127.42.0.0/29"),
		ExpiresAt:   now.Add(time.Minute),
	}
}

// TestAuthorityStartNetworkSetupJoinsAuthenticatedCallerAndDaemonIdentities verifies the exact initiation boundary and replay projection.
func TestAuthorityStartNetworkSetupJoinsAuthenticatedCallerAndDaemonIdentities(t *testing.T) {
	at := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	record := authorityNetworkSetupApprovalOperation(t, "operation-existing", "intent-network-setup", at)
	coordinator := &recordingNetworkSetupCoordinator{startResult: record}
	authority := newAuthorityForNetworkSetupTest(
		coordinator,
		at,
		func() (domain.OperationID, error) { return "operation-proposed", nil },
		func() (identity.InstallationID, error) { return "installation-proposed", nil },
	)
	caller := controlCaller([]rpc.Capability{control.CapabilityNetworkSetupV1})
	caller.Transport.UserID = "S-1-5-21-1000"
	caller.Session.BuildVersion = "must-not-own-the-machine"
	request := control.StartNetworkSetupRequest{IntentID: record.Operation.IntentID}

	result, err := authority.StartNetworkSetup(nil, caller, request)
	if err != nil {
		t.Fatalf("StartNetworkSetup() error = %v", err)
	}
	want := control.NetworkSetupOperation{Operation: record.Operation, Revision: record.Revision}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("StartNetworkSetup() = %#v, want %#v", result, want)
	}
	starts, prepares, confirms, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkSetupStartRequest{
		OperationID:       "operation-proposed",
		IntentID:          request.IntentID,
		InstallationID:    "installation-proposed",
		RequesterIdentity: caller.Transport.UserID,
	}
	if !reflect.DeepEqual(starts, []reconcile.NetworkSetupStartRequest{wantRequest}) {
		t.Fatalf("coordinator start requests = %#v, want %#v", starts, wantRequest)
	}
	if len(prepares) != 0 || len(confirms) != 0 || nilContexts != 0 {
		t.Fatalf("unexpected prepare/confirm/nil-context counts = %d/%d/%d", len(prepares), len(confirms), nilContexts)
	}
}

// TestAuthorityStartNetworkSetupValidatesBeforeEntropy proves malformed client intent cannot consume either daemon identity source.
func TestAuthorityStartNetworkSetupValidatesBeforeEntropy(t *testing.T) {
	coordinator := new(recordingNetworkSetupCoordinator)
	operationCalls := 0
	installationCalls := 0
	authority := newAuthorityForNetworkSetupTest(
		coordinator,
		time.Now(),
		func() (domain.OperationID, error) { operationCalls++; return "operation-unused", nil },
		func() (identity.InstallationID, error) { installationCalls++; return "installation-unused", nil },
	)

	if _, err := authority.StartNetworkSetup(t.Context(), control.Caller{}, control.StartNetworkSetupRequest{}); err == nil {
		t.Fatal("StartNetworkSetup() error = nil, want invalid request")
	}
	starts, _, _, _ := coordinator.requests()
	if operationCalls != 0 || installationCalls != 0 || len(starts) != 0 {
		t.Fatalf("operation/installation/coordinator calls = %d/%d/%d, want 0/0/0", operationCalls, installationCalls, len(starts))
	}
}

// TestAuthorityStartNetworkSetupStopsAtIdentityFactoryFailures verifies no malformed or unavailable daemon identity reaches reconciliation.
func TestAuthorityStartNetworkSetupStopsAtIdentityFactoryFailures(t *testing.T) {
	sentinel := errors.New("entropy unavailable")
	for _, test := range []struct {
		name                  string
		operationFactory      func() (domain.OperationID, error)
		installationFactory   func() (identity.InstallationID, error)
		wantInstallationCalls int
	}{
		{
			name:                "operation error",
			operationFactory:    func() (domain.OperationID, error) { return "", sentinel },
			installationFactory: func() (identity.InstallationID, error) { return "installation-unused", nil },
		},
		{
			name:                "invalid operation",
			operationFactory:    func() (domain.OperationID, error) { return "", nil },
			installationFactory: func() (identity.InstallationID, error) { return "installation-unused", nil },
		},
		{
			name:                  "installation error",
			operationFactory:      func() (domain.OperationID, error) { return "operation-proposed", nil },
			installationFactory:   func() (identity.InstallationID, error) { return "", sentinel },
			wantInstallationCalls: 1,
		},
		{
			name:                  "invalid installation",
			operationFactory:      func() (domain.OperationID, error) { return "operation-proposed", nil },
			installationFactory:   func() (identity.InstallationID, error) { return "-invalid", nil },
			wantInstallationCalls: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := new(recordingNetworkSetupCoordinator)
			installationCalls := 0
			authority := newAuthorityForNetworkSetupTest(
				coordinator,
				time.Now(),
				test.operationFactory,
				func() (identity.InstallationID, error) {
					installationCalls++
					return test.installationFactory()
				},
			)
			caller := control.Caller{}
			caller.Transport.UserID = "501"

			if _, err := authority.StartNetworkSetup(t.Context(), caller, control.StartNetworkSetupRequest{IntentID: "intent-network-setup"}); err == nil {
				t.Fatal("StartNetworkSetup() error = nil")
			}
			starts, _, _, _ := coordinator.requests()
			if installationCalls != test.wantInstallationCalls || len(starts) != 0 {
				t.Fatalf("installation/coordinator calls = %d/%d, want %d/0", installationCalls, len(starts), test.wantInstallationCalls)
			}
		})
	}
}

// TestAuthorityStartNetworkSetupRejectsUncorrelatedAndInvalidResults verifies coordinator readback cannot escape the requested intent.
func TestAuthorityStartNetworkSetupRejectsUncorrelatedAndInvalidResults(t *testing.T) {
	at := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*state.OperationRecord)
	}{
		{name: "intent", mutate: func(record *state.OperationRecord) { record.Operation.IntentID = "intent-other" }},
		{name: "revision", mutate: func(record *state.OperationRecord) { record.Revision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := authorityNetworkSetupApprovalOperation(t, "operation-network-setup", "intent-network-setup", at)
			test.mutate(&record)
			authority := newAuthorityForNetworkSetupTest(
				&recordingNetworkSetupCoordinator{startResult: record},
				at,
				func() (domain.OperationID, error) { return "operation-proposed", nil },
				func() (identity.InstallationID, error) { return "installation-proposed", nil },
			)
			caller := control.Caller{}
			caller.Transport.UserID = "501"

			_, err := authority.StartNetworkSetup(t.Context(), caller, control.StartNetworkSetupRequest{IntentID: "intent-network-setup"})
			assertInternalNetworkSetupError(t, err)
		})
	}
}

// TestAuthorityPrepareNetworkSetupApprovalBindsTransportIdentityAndMapsTicket verifies the non-secret helper projection.
func TestAuthorityPrepareNetworkSetupApprovalBindsTransportIdentityAndMapsTicket(t *testing.T) {
	now := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	prepared := validAuthorityNetworkSetupPoolResult(now)
	coordinator := &recordingNetworkSetupCoordinator{prepareResult: prepared}
	authority := newAuthorityForNetworkSetupTest(
		coordinator,
		now,
		func() (domain.OperationID, error) { return "operation-unused", nil },
		func() (identity.InstallationID, error) { return "installation-unused", nil },
	)
	caller := controlCaller([]rpc.Capability{control.CapabilityNetworkSetupV1})
	caller.Transport.UserID = "501"
	caller.Session.BuildVersion = "must-not-be-used"
	request := control.PrepareNetworkSetupApprovalRequest{OperationID: prepared.OperationID, ExpectedOperationRevision: 3}

	result, err := authority.PrepareNetworkSetupApproval(nil, caller, request)
	if err != nil {
		t.Fatalf("PrepareNetworkSetupApproval() error = %v", err)
	}
	want := control.NetworkSetupApprovalPreparation{
		OperationID:       request.OperationID,
		OperationRevision: request.ExpectedOperationRevision,
		Ticket: control.NetworkSetupApprovalTicket{
			OperationID: prepared.OperationID,
			Reference:   prepared.Reference,
			Operation:   prepared.Operation,
			Pool:        prepared.Pool.String(),
			ExpiresAt:   prepared.ExpiresAt,
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("PrepareNetworkSetupApproval() = %#v, want %#v", result, want)
	}
	starts, prepares, confirms, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkSetupPrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	}
	if !reflect.DeepEqual(prepares, []reconcile.NetworkSetupPrepareRequest{wantRequest}) {
		t.Fatalf("coordinator prepare requests = %#v, want %#v", prepares, wantRequest)
	}
	if len(starts) != 0 || len(confirms) != 0 || nilContexts != 0 {
		t.Fatalf("unexpected start/confirm/nil-context counts = %d/%d/%d", len(starts), len(confirms), nilContexts)
	}
}

// TestAuthorityPrepareNetworkSetupApprovalRejectsUncorrelatedAndInvalidResults verifies all helper metadata before projection.
func TestAuthorityPrepareNetworkSetupApprovalRejectsUncorrelatedAndInvalidResults(t *testing.T) {
	now := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.PoolResult)
	}{
		{name: "operation", mutate: func(result *ticketissuer.PoolResult) { result.OperationID = "operation-other" }},
		{name: "expiry", mutate: func(result *ticketissuer.PoolResult) { result.ExpiresAt = now }},
		{name: "pool", mutate: func(result *ticketissuer.PoolResult) { result.Pool = netip.MustParsePrefix("127.42.0.1/29") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := validAuthorityNetworkSetupPoolResult(now)
			test.mutate(&prepared)
			authority := newAuthorityForNetworkSetupTest(
				&recordingNetworkSetupCoordinator{prepareResult: prepared},
				now,
				func() (domain.OperationID, error) { return "operation-unused", nil },
				func() (identity.InstallationID, error) { return "installation-unused", nil },
			)
			caller := control.Caller{}
			caller.Transport.UserID = "501"

			_, err := authority.PrepareNetworkSetupApproval(t.Context(), caller, control.PrepareNetworkSetupApprovalRequest{
				OperationID: "operation-network-setup", ExpectedOperationRevision: 3,
			})
			assertInternalNetworkSetupError(t, err)
		})
	}
}

// TestAuthorityConfirmNetworkSetupApprovalPassesEvidenceAndMapsCompletion verifies the exact atomic terminal projection.
func TestAuthorityConfirmNetworkSetupApprovalPassesEvidenceAndMapsCompletion(t *testing.T) {
	at := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	pool := authorityNetworkSetupPool(t, "127.42.0.0/29")
	completed := authorityNetworkSetupCompletion(t, "operation-network-setup", "intent-network-setup", pool, at)
	coordinator := &recordingNetworkSetupCoordinator{confirmResult: completed}
	authority := newAuthorityForNetworkSetupTest(
		coordinator,
		at,
		func() (domain.OperationID, error) { return "operation-unused", nil },
		func() (identity.InstallationID, error) { return "installation-unused", nil },
	)
	request := control.ConfirmNetworkSetupApprovalRequest{
		OperationID:               completed.Operation.Operation.ID,
		ExpectedOperationRevision: 3,
		PoolEvidence:              authorityNetworkSetupEvidence(pool.Prefix()),
	}

	result, err := authority.ConfirmNetworkSetupApproval(nil, control.Caller{}, request)
	if err != nil {
		t.Fatalf("ConfirmNetworkSetupApproval() error = %v", err)
	}
	want := control.NetworkSetupApprovalConfirmation{
		Operation:       completed.Operation.Operation,
		Revision:        completed.Operation.Revision,
		NetworkRevision: completed.Network.Record.Revision,
		Pool:            pool.Prefix().String(),
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("ConfirmNetworkSetupApproval() = %#v, want %#v", result, want)
	}
	starts, prepares, confirms, nilContexts := coordinator.requests()
	wantRequest := reconcile.NetworkSetupConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		HelperPoolEvidence:        request.PoolEvidence,
	}
	if !reflect.DeepEqual(confirms, []reconcile.NetworkSetupConfirmRequest{wantRequest}) {
		t.Fatalf("coordinator confirm requests = %#v, want %#v", confirms, wantRequest)
	}
	if len(starts) != 0 || len(prepares) != 0 || nilContexts != 0 {
		t.Fatalf("unexpected start/prepare/nil-context counts = %d/%d/%d", len(starts), len(prepares), nilContexts)
	}
}

// TestAuthorityConfirmNetworkSetupApprovalRejectsUncorrelatedAndInvalidResults verifies durable output cannot cross operation, revision, or pool boundaries.
func TestAuthorityConfirmNetworkSetupApprovalRejectsUncorrelatedAndInvalidResults(t *testing.T) {
	at := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	pool := authorityNetworkSetupPool(t, "127.42.0.0/29")
	for _, test := range []struct {
		name   string
		mutate func(*state.CompleteNetworkSetupResult)
	}{
		{name: "operation", mutate: func(result *state.CompleteNetworkSetupResult) { result.Operation.Operation.ID = "operation-other" }},
		{name: "revisions", mutate: func(result *state.CompleteNetworkSetupResult) {
			result.Network.Record.Revision++
			result.Operation.Revision++
		}},
		{name: "pool", mutate: func(result *state.CompleteNetworkSetupResult) {
			result.Network.Record.Pool = authorityNetworkSetupPool(t, "127.43.0.0/29")
		}},
		{name: "network", mutate: func(result *state.CompleteNetworkSetupResult) { result.Network.Record.Leases = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			completed := authorityNetworkSetupCompletion(t, "operation-network-setup", "intent-network-setup", pool, at)
			test.mutate(&completed)
			authority := newAuthorityForNetworkSetupTest(
				&recordingNetworkSetupCoordinator{confirmResult: completed},
				at,
				func() (domain.OperationID, error) { return "operation-unused", nil },
				func() (identity.InstallationID, error) { return "installation-unused", nil },
			)
			request := control.ConfirmNetworkSetupApprovalRequest{
				OperationID:               "operation-network-setup",
				ExpectedOperationRevision: 3,
				PoolEvidence:              authorityNetworkSetupEvidence(pool.Prefix()),
			}

			_, err := authority.ConfirmNetworkSetupApproval(t.Context(), control.Caller{}, request)
			assertInternalNetworkSetupError(t, err)
		})
	}
}

// TestAuthorityNetworkSetupValidatesSelectionsBeforeCoordination verifies malformed approval requests never reach reconciliation.
func TestAuthorityNetworkSetupValidatesSelectionsBeforeCoordination(t *testing.T) {
	coordinator := new(recordingNetworkSetupCoordinator)
	authority := newAuthorityForNetworkSetupTest(
		coordinator,
		time.Now(),
		func() (domain.OperationID, error) { return "operation-unused", nil },
		func() (identity.InstallationID, error) { return "installation-unused", nil },
	)

	if _, err := authority.PrepareNetworkSetupApproval(t.Context(), control.Caller{}, control.PrepareNetworkSetupApprovalRequest{}); err == nil {
		t.Fatal("PrepareNetworkSetupApproval() error = nil")
	}
	if _, err := authority.ConfirmNetworkSetupApproval(t.Context(), control.Caller{}, control.ConfirmNetworkSetupApprovalRequest{}); err == nil {
		t.Fatal("ConfirmNetworkSetupApproval() error = nil")
	}
	_, prepares, confirms, _ := coordinator.requests()
	if len(prepares) != 0 || len(confirms) != 0 {
		t.Fatalf("invalid prepare/confirm requests reached coordinator: %#v / %#v", prepares, confirms)
	}
}

// TestAuthorityNetworkSetupMapsCoordinatorErrors verifies each public setup phase applies the reviewed taxonomy.
func TestAuthorityNetworkSetupMapsCoordinatorErrors(t *testing.T) {
	now := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	pool := authorityNetworkSetupPool(t, "127.42.0.0/29")
	caller := control.Caller{}
	caller.Transport.UserID = "501"
	for _, test := range []struct {
		name      string
		cause     error
		wantCode  rpc.ErrorCode
		configure func(*recordingNetworkSetupCoordinator, error)
		call      func(*Authority) error
	}{
		{
			name: "start conflict", cause: &state.IntentConflictError{IntentID: "intent-network-setup"}, wantCode: rpc.ErrorCodeConflict,
			configure: func(coordinator *recordingNetworkSetupCoordinator, cause error) {
				coordinator.startErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.StartNetworkSetup(t.Context(), caller, control.StartNetworkSetupRequest{IntentID: "intent-network-setup"})
				return err
			},
		},
		{
			name: "prepare missing", cause: &state.OperationNotFoundError{OperationID: "operation-network-setup"}, wantCode: rpc.ErrorCodeNotFound,
			configure: func(coordinator *recordingNetworkSetupCoordinator, cause error) {
				coordinator.prepareErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.PrepareNetworkSetupApproval(t.Context(), caller, control.PrepareNetworkSetupApprovalRequest{
					OperationID: "operation-network-setup", ExpectedOperationRevision: 3,
				})
				return err
			},
		},
		{
			name: "confirm stale", cause: &state.StaleRevisionError{OperationID: "operation-network-setup", Expected: 3, Actual: 4}, wantCode: rpc.ErrorCodeConflict,
			configure: func(coordinator *recordingNetworkSetupCoordinator, cause error) {
				coordinator.confirmErr = cause
			},
			call: func(authority *Authority) error {
				_, err := authority.ConfirmNetworkSetupApproval(t.Context(), caller, control.ConfirmNetworkSetupApprovalRequest{
					OperationID: "operation-network-setup", ExpectedOperationRevision: 3,
					PoolEvidence: authorityNetworkSetupEvidence(pool.Prefix()),
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := new(recordingNetworkSetupCoordinator)
			test.configure(coordinator, test.cause)
			authority := newAuthorityForNetworkSetupTest(
				coordinator,
				now,
				func() (domain.OperationID, error) { return "operation-proposed", nil },
				func() (identity.InstallationID, error) { return "installation-proposed", nil },
			)
			err := test.call(authority)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.wantCode || !errors.Is(err, test.cause) {
				t.Fatalf("authority error = %#v, want %s wrapping %v", err, test.wantCode, test.cause)
			}
		})
	}
}

// TestClassifyNetworkSetupErrorCoversReviewedFailures verifies safe state failures remain distinct from internal failures.
func TestClassifyNetworkSetupErrorCoversReviewedFailures(t *testing.T) {
	operationID := domain.OperationID("operation-network-setup")
	intentID := domain.IntentID("intent-network-setup")
	observationAddress := netip.MustParseAddr("127.77.10.8")
	for _, test := range []struct {
		name     string
		cause    error
		wantCode rpc.ErrorCode
	}{
		{name: "intent conflict", cause: &state.IntentConflictError{IntentID: intentID}, wantCode: rpc.ErrorCodeConflict},
		{name: "stale revision", cause: &state.StaleRevisionError{OperationID: operationID, Expected: 3, Actual: 4}, wantCode: rpc.ErrorCodeConflict},
		{name: "network foundation", cause: &state.NetworkInitializationConflictError{ActualRevision: 5, Difference: "installation ownership"}, wantCode: rpc.ErrorCodeConflict},
		{name: "pool exhaustion", cause: &identity.PoolSelectionExhaustionError{CandidatePools: 4, AssignmentBlockedPools: 4}, wantCode: rpc.ErrorCodeConflict},
		{name: "operation missing", cause: &state.OperationNotFoundError{OperationID: operationID}, wantCode: rpc.ErrorCodeNotFound},
		{
			name: "host observation",
			cause: ticketissuer.NewPoolObservationError(
				ticketissuer.PoolObservationHostConflicts,
				observationAddress,
				errors.New("observe Darwin host conflicts: route lookup failed"),
			),
			wantCode: rpc.ErrorCodeNetworkObservationFailed,
		},
		{
			name: "invalid observation stage",
			cause: ticketissuer.NewPoolObservationError(
				"filesystem",
				observationAddress,
				errors.New("observe Darwin host conflicts: route lookup failed"),
			),
		},
		{
			name: "invalid observation address",
			cause: ticketissuer.NewPoolObservationError(
				ticketissuer.PoolObservationHostConflicts,
				netip.MustParseAddr("127.78.10.8"),
				errors.New("observe Darwin host conflicts: route lookup failed"),
			),
		},
		{name: "privileged helper missing", cause: &ticketissuer.PoolPrerequisiteMissingError{}, wantCode: rpc.ErrorCodePrivilegedHelperRequired},
		{name: "privileged helper unsafe", cause: fmt.Errorf("validate installer boundary: %w", ticketspool.ErrUnsafePath), wantCode: rpc.ErrorCodePrivilegedHelperUnsafe},
		{name: "daemon operation collision", cause: &state.OperationIDConflictError{OperationID: operationID, ExistingIntentID: "intent-other", RequestedIntentID: intentID}},
		{name: "intent lookup missing", cause: &state.OperationIntentNotFoundError{IntentID: intentID}},
		{name: "cancelled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "unknown", cause: errors.New("database unavailable")},
		{name: "corrupt conflict", cause: &state.CorruptStateError{Entity: "network", Key: "singleton", Cause: &state.StaleRevisionError{OperationID: operationID, Expected: 3, Actual: 4}}},
		{name: "corrupt missing", cause: &state.CorruptStateError{Entity: "operation", Key: string(operationID), Cause: &state.OperationNotFoundError{OperationID: operationID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("network setup boundary: %w", test.cause)
			classified := classifyNetworkSetupError(wrapped)
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

// TestNewOpaqueInstallationIDReturnsIndependentCanonicalIdentities verifies the production 128-bit format.
func TestNewOpaqueInstallationIDReturnsIndependentCanonicalIdentities(t *testing.T) {
	first, err := newOpaqueInstallationID()
	if err != nil {
		t.Fatalf("newOpaqueInstallationID() first error = %v", err)
	}
	second, err := newOpaqueInstallationID()
	if err != nil {
		t.Fatalf("newOpaqueInstallationID() second error = %v", err)
	}
	for _, installationID := range []identity.InstallationID{first, second} {
		if err := installationID.Validate(); err != nil {
			t.Fatalf("generated installation ID %q is invalid: %v", installationID, err)
		}
		const prefix = "installation-"
		if !strings.HasPrefix(string(installationID), prefix) {
			t.Fatalf("installation ID = %q, want %q prefix", installationID, prefix)
		}
		hexValue := strings.TrimPrefix(string(installationID), prefix)
		decoded, err := hex.DecodeString(hexValue)
		if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != hexValue {
			t.Fatalf("installation ID = %q, want 128-bit lowercase hexadecimal suffix", installationID)
		}
	}
	if first == second {
		t.Fatalf("generated installation IDs are equal: %q", first)
	}
}

// TestAuthorityIdentityFactoryConstructionRejectsNilInstallationFactory keeps installation authority fail-fast.
func TestAuthorityIdentityFactoryConstructionRejectsNilInstallationFactory(t *testing.T) {
	var nilInstallationIDFactory func() (identity.InstallationID, error)
	assertAuthorityPanic(t, func() {
		newAuthorityWithIdentityFactories(
			&recordingStore{},
			testProjectUnregisterApprovals(),
			buildinfo.Info{},
			&registrationDiscoverer{},
			time.Now,
			newOpaqueProjectID,
			newOpaqueOperationID,
			nilInstallationIDFactory,
			testProjectLifecycles(),
			testNetworkSetups(),
			testHTTPRoutes(),
		)
	})
}

// assertInternalNetworkSetupError verifies coordinator-output failures remain daemon-internal.
func assertInternalNetworkSetupError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("network setup error = nil")
	}
	var handlerError *session.HandlerError
	if errors.As(err, &handlerError) {
		t.Fatalf("network setup error = %#v, want internal", err)
	}
}
