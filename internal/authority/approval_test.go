package authority

import (
	"context"
	"encoding/json"
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
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingProjectUnregisterApprovals captures authority requests while returning deterministic coordinator results.
type recordingProjectUnregisterApprovals struct {
	mutex           sync.Mutex
	startResult     state.OperationRecord
	startErr        error
	prepareResult   reconcile.PrepareResult
	prepareErr      error
	confirmResult   state.OperationRecord
	confirmErr      error
	startRequests   []reconcile.StartRequest
	prepareRequests []reconcile.PrepareRequest
	confirmRequests []reconcile.ConfirmRequest
	nilContexts     int
}

// Start records the daemon and client identities joined at the coordinator boundary.
func (approvals *recordingProjectUnregisterApprovals) Start(
	ctx context.Context,
	request reconcile.StartRequest,
) (state.OperationRecord, error) {
	approvals.mutex.Lock()
	defer approvals.mutex.Unlock()
	if ctx == nil {
		approvals.nilContexts++
	}
	approvals.startRequests = append(approvals.startRequests, request)
	return approvals.startResult, approvals.startErr
}

// Prepare records the authenticated coordinator request without retaining control-layer caller data.
func (approvals *recordingProjectUnregisterApprovals) Prepare(
	ctx context.Context,
	request reconcile.PrepareRequest,
) (reconcile.PrepareResult, error) {
	approvals.mutex.Lock()
	defer approvals.mutex.Unlock()
	if ctx == nil {
		approvals.nilContexts++
	}
	approvals.prepareRequests = append(approvals.prepareRequests, request)
	return approvals.prepareResult, approvals.prepareErr
}

// Confirm records the revision-bound confirmation request before returning its durable operation.
func (approvals *recordingProjectUnregisterApprovals) Confirm(
	ctx context.Context,
	request reconcile.ConfirmRequest,
) (state.OperationRecord, error) {
	approvals.mutex.Lock()
	defer approvals.mutex.Unlock()
	if ctx == nil {
		approvals.nilContexts++
	}
	approvals.confirmRequests = append(approvals.confirmRequests, request)
	return approvals.confirmResult, approvals.confirmErr
}

// calls returns isolated request snapshots for boundary assertions.
func (approvals *recordingProjectUnregisterApprovals) calls() (
	[]reconcile.PrepareRequest,
	[]reconcile.ConfirmRequest,
	int,
) {
	approvals.mutex.Lock()
	defer approvals.mutex.Unlock()
	return append([]reconcile.PrepareRequest(nil), approvals.prepareRequests...),
		append([]reconcile.ConfirmRequest(nil), approvals.confirmRequests...),
		approvals.nilContexts
}

// starts returns isolated initiation requests for daemon identity and idempotency assertions.
func (approvals *recordingProjectUnregisterApprovals) starts() ([]reconcile.StartRequest, int) {
	approvals.mutex.Lock()
	defer approvals.mutex.Unlock()
	return append([]reconcile.StartRequest(nil), approvals.startRequests...), approvals.nilContexts
}

// testProjectUnregisterApprovals supplies an inert required collaborator to authority tests outside this file.
func testProjectUnregisterApprovals() *recordingProjectUnregisterApprovals {
	return &recordingProjectUnregisterApprovals{}
}

// validAuthorityPrepareResult returns one partially released operation with non-secret helper launch metadata.
func validAuthorityPrepareResult() reconcile.PrepareResult {
	ticket := ticketissuer.Result{
		OperationID: "operation-remove",
		LeaseKey: identity.LeaseKey{
			ProjectID:   "project-orders",
			SecondaryID: "admin",
		},
		Reference: helper.TicketReference(strings.Repeat("a", 64)),
		Operation: helper.OperationReleaseLoopbackIdentity,
		Address:   netip.MustParseAddr("127.77.0.11"),
		ExpiresAt: time.Date(2026, time.July, 19, 12, 1, 0, 0, time.UTC),
	}
	return reconcile.PrepareResult{
		OperationID:       ticket.OperationID,
		OperationRevision: 41,
		ProjectID:         ticket.LeaseKey.ProjectID,
		TotalLeases:       2,
		ReleasedLeases:    1,
		PendingLeases:     1,
		Ticket:            &ticket,
	}
}

// validAuthoritySucceededOperation returns one succeeded project unregister operation for confirmation mapping.
func validAuthoritySucceededOperation(t *testing.T) domain.Operation {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 19, 11, 55, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		"operation-remove",
		"intent-remove",
		domain.OperationKindProjectUnregister,
		"project-orders",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(
		domain.OperationRunning,
		"releasing network",
		requestedAt.Add(time.Minute),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	operation, err = operation.Transition(
		domain.OperationSucceeded,
		"project unregistered",
		requestedAt.Add(2*time.Minute),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(succeeded) error = %v", err)
	}
	return operation
}

// TestAuthorityPrepareProjectUnregisterApprovalUsesTransportIdentity verifies payload and session data cannot select helper ownership.
func TestAuthorityPrepareProjectUnregisterApprovalUsesTransportIdentity(t *testing.T) {
	prepared := validAuthorityPrepareResult()
	approvals := &recordingProjectUnregisterApprovals{prepareResult: prepared}
	authority := newAuthority(&recordingStore{}, approvals, buildinfo.Info{Version: "dev"})
	request := control.PrepareProjectUnregisterApprovalRequest{
		OperationID:               prepared.OperationID,
		ExpectedOperationRevision: prepared.OperationRevision,
	}
	if _, present := reflect.TypeOf(request).FieldByName("RequesterIdentity"); present {
		t.Fatal("control request exposes requester identity")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(payload), "requester") {
		t.Fatalf("control request payload contains requester authority: %s", payload)
	}
	caller := controlCaller([]rpc.Capability{control.CapabilityV1})
	caller.Transport.UserID = "S-1-5-21-1000"
	caller.Session.BuildVersion = "must-not-be-used-as-identity"

	result, err := authority.PrepareProjectUnregisterApproval(nil, caller, request)
	if err != nil {
		t.Fatalf("PrepareProjectUnregisterApproval() error = %v", err)
	}
	want := control.ProjectUnregisterApprovalPreparation{
		OperationID:       prepared.OperationID,
		OperationRevision: prepared.OperationRevision,
		ProjectID:         prepared.ProjectID,
		TotalLeases:       prepared.TotalLeases,
		ReleasedLeases:    prepared.ReleasedLeases,
		PendingLeases:     prepared.PendingLeases,
		Ticket: &control.HelperApprovalTicket{
			OperationID: prepared.Ticket.OperationID,
			LeaseKey: control.HelperApprovalLeaseKey{
				ProjectID:   prepared.Ticket.LeaseKey.ProjectID,
				SecondaryID: prepared.Ticket.LeaseKey.SecondaryID,
			},
			Reference: prepared.Ticket.Reference,
			Operation: prepared.Ticket.Operation,
			Address:   prepared.Ticket.Address.String(),
			ExpiresAt: prepared.Ticket.ExpiresAt,
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("preparation = %#v, want %#v", result, want)
	}
	prepareRequests, confirmRequests, nilContexts := approvals.calls()
	if len(prepareRequests) != 1 || !reflect.DeepEqual(prepareRequests[0], reconcile.PrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	}) {
		t.Fatalf("coordinator prepare requests = %#v, want exact transport identity", prepareRequests)
	}
	if len(confirmRequests) != 0 || nilContexts != 0 {
		t.Fatalf("coordinator confirm requests / nil contexts = %d / %d, want 0 / 0", len(confirmRequests), nilContexts)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal preparation: %v", err)
	}
	if strings.Contains(string(encoded), caller.Transport.UserID) || strings.Contains(string(encoded), caller.Session.BuildVersion) {
		t.Fatalf("preparation echoed caller identity: %s", encoded)
	}
}

// TestAuthorityPrepareProjectUnregisterApprovalOmitsCompletedTicket verifies completed release progress never invents helper authority.
func TestAuthorityPrepareProjectUnregisterApprovalOmitsCompletedTicket(t *testing.T) {
	prepared := validAuthorityPrepareResult()
	prepared.ReleasedLeases = prepared.TotalLeases
	prepared.PendingLeases = 0
	prepared.Ticket = nil
	approvals := &recordingProjectUnregisterApprovals{prepareResult: prepared}
	authority := newAuthority(&recordingStore{}, approvals, buildinfo.Info{Version: "dev"})

	result, err := authority.PrepareProjectUnregisterApproval(t.Context(), control.Caller{}, control.PrepareProjectUnregisterApprovalRequest{
		OperationID:               prepared.OperationID,
		ExpectedOperationRevision: prepared.OperationRevision,
	})
	if err != nil {
		t.Fatalf("PrepareProjectUnregisterApproval() error = %v", err)
	}
	if result.Ticket != nil || result.PendingLeases != 0 || result.ReleasedLeases != result.TotalLeases {
		t.Fatalf("completed preparation = %#v, want released progress without ticket", result)
	}
}

// TestAuthorityConfirmProjectUnregisterApprovalMapsSucceededOperation verifies confirmation returns the exact durable completion record.
func TestAuthorityConfirmProjectUnregisterApprovalMapsSucceededOperation(t *testing.T) {
	record := state.OperationRecord{Operation: validAuthoritySucceededOperation(t), Revision: 43}
	approvals := &recordingProjectUnregisterApprovals{confirmResult: record}
	authority := newAuthority(&recordingStore{}, approvals, buildinfo.Info{Version: "dev"})
	request := control.ConfirmProjectUnregisterApprovalRequest{
		OperationID:               record.Operation.ID,
		ExpectedOperationRevision: 41,
	}

	result, err := authority.ConfirmProjectUnregisterApproval(nil, control.Caller{}, request)
	if err != nil {
		t.Fatalf("ConfirmProjectUnregisterApproval() error = %v", err)
	}
	want := control.ProjectUnregisterApprovalConfirmation{
		Operation: record.Operation,
		Revision:  record.Revision,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("confirmation = %#v, want %#v", result, want)
	}
	prepareRequests, confirmRequests, nilContexts := approvals.calls()
	if len(prepareRequests) != 0 || len(confirmRequests) != 1 || !reflect.DeepEqual(confirmRequests[0], reconcile.ConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	}) {
		t.Fatalf("coordinator prepare / confirm requests = %#v / %#v", prepareRequests, confirmRequests)
	}
	if nilContexts != 0 {
		t.Fatalf("coordinator nil contexts = %d, want 0", nilContexts)
	}
}

// TestAuthorityProjectUnregisterApprovalValidatesInputBeforeCoordination verifies malformed selections cannot reach reconciliation.
func TestAuthorityProjectUnregisterApprovalValidatesInputBeforeCoordination(t *testing.T) {
	approvals := testProjectUnregisterApprovals()
	authority := newAuthority(&recordingStore{}, approvals, buildinfo.Info{Version: "dev"})

	if _, err := authority.PrepareProjectUnregisterApproval(t.Context(), control.Caller{}, control.PrepareProjectUnregisterApprovalRequest{}); err == nil {
		t.Fatal("PrepareProjectUnregisterApproval() error = nil, want invalid request")
	}
	if _, err := authority.ConfirmProjectUnregisterApproval(t.Context(), control.Caller{}, control.ConfirmProjectUnregisterApprovalRequest{}); err == nil {
		t.Fatal("ConfirmProjectUnregisterApproval() error = nil, want invalid request")
	}
	prepareRequests, confirmRequests, _ := approvals.calls()
	if len(prepareRequests) != 0 || len(confirmRequests) != 0 {
		t.Fatalf("coordinator requests after invalid input = %#v / %#v, want none", prepareRequests, confirmRequests)
	}
}

// TestAuthorityProjectUnregisterApprovalClassifiesCoordinatorFailures verifies both calls route reviewed causes through the shared wire taxonomy.
func TestAuthorityProjectUnregisterApprovalClassifiesCoordinatorFailures(t *testing.T) {
	prepared := validAuthorityPrepareResult()
	prepareCause := &state.StaleRevisionError{
		OperationID: prepared.OperationID,
		Expected:    prepared.OperationRevision,
		Actual:      prepared.OperationRevision + 1,
	}
	prepareAuthority := newAuthority(
		&recordingStore{},
		&recordingProjectUnregisterApprovals{prepareErr: prepareCause},
		buildinfo.Info{Version: "dev"},
	)
	_, prepareErr := prepareAuthority.PrepareProjectUnregisterApproval(
		t.Context(),
		control.Caller{},
		control.PrepareProjectUnregisterApprovalRequest{
			OperationID:               prepared.OperationID,
			ExpectedOperationRevision: prepared.OperationRevision,
		},
	)
	assertAuthorityHandlerError(t, prepareErr, prepareCause, rpc.ErrorCodeConflict)

	confirmCause := &state.OperationNotFoundError{OperationID: prepared.OperationID}
	confirmAuthority := newAuthority(
		&recordingStore{},
		&recordingProjectUnregisterApprovals{confirmErr: confirmCause},
		buildinfo.Info{Version: "dev"},
	)
	_, confirmErr := confirmAuthority.ConfirmProjectUnregisterApproval(
		t.Context(),
		control.Caller{},
		control.ConfirmProjectUnregisterApprovalRequest{
			OperationID:               prepared.OperationID,
			ExpectedOperationRevision: prepared.OperationRevision,
		},
	)
	assertAuthorityHandlerError(t, confirmErr, confirmCause, rpc.ErrorCodeNotFound)
}

// TestAuthorityPrepareProjectUnregisterApprovalRejectsMismatchedAndInvalidResults verifies coordinator output cannot cross operation boundaries.
func TestAuthorityPrepareProjectUnregisterApprovalRejectsMismatchedAndInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*reconcile.PrepareResult)
	}{
		{name: "operation", mutate: func(result *reconcile.PrepareResult) { result.OperationID = "operation-other" }},
		{name: "revision", mutate: func(result *reconcile.PrepareResult) { result.OperationRevision++ }},
		{name: "progress", mutate: func(result *reconcile.PrepareResult) { result.PendingLeases++ }},
		{name: "ticket project", mutate: func(result *reconcile.PrepareResult) { result.Ticket.LeaseKey.ProjectID = "project-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := validAuthorityPrepareResult()
			request := control.PrepareProjectUnregisterApprovalRequest{
				OperationID:               prepared.OperationID,
				ExpectedOperationRevision: prepared.OperationRevision,
			}
			test.mutate(&prepared)
			authority := newAuthority(
				&recordingStore{},
				&recordingProjectUnregisterApprovals{prepareResult: prepared},
				buildinfo.Info{Version: "dev"},
			)

			if _, err := authority.PrepareProjectUnregisterApproval(t.Context(), control.Caller{}, request); err == nil {
				t.Fatal("PrepareProjectUnregisterApproval() error = nil, want rejected coordinator result")
			} else {
				var handlerError *session.HandlerError
				if errors.As(err, &handlerError) {
					t.Fatalf("coordinator result error = %#v, must remain internal", err)
				}
			}
		})
	}
}

// TestAuthorityConfirmProjectUnregisterApprovalRejectsMismatchedAndInvalidResults verifies only the requested succeeded operation is returned.
func TestAuthorityConfirmProjectUnregisterApprovalRejectsMismatchedAndInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.OperationRecord)
	}{
		{name: "operation", mutate: func(record *state.OperationRecord) { record.Operation.ID = "operation-other" }},
		{name: "state", mutate: func(record *state.OperationRecord) {
			record.Operation.State = domain.OperationRunning
			record.Operation.Phase = "still releasing"
			record.Operation.FinishedAt = nil
		}},
		{name: "revision", mutate: func(record *state.OperationRecord) { record.Revision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := state.OperationRecord{Operation: validAuthoritySucceededOperation(t), Revision: 43}
			request := control.ConfirmProjectUnregisterApprovalRequest{
				OperationID:               record.Operation.ID,
				ExpectedOperationRevision: 41,
			}
			test.mutate(&record)
			authority := newAuthority(
				&recordingStore{},
				&recordingProjectUnregisterApprovals{confirmResult: record},
				buildinfo.Info{Version: "dev"},
			)

			if _, err := authority.ConfirmProjectUnregisterApproval(t.Context(), control.Caller{}, request); err == nil {
				t.Fatal("ConfirmProjectUnregisterApproval() error = nil, want rejected coordinator result")
			} else {
				var handlerError *session.HandlerError
				if errors.As(err, &handlerError) {
					t.Fatalf("coordinator result error = %#v, must remain internal", err)
				}
			}
		})
	}
}

// TestClassifyProjectUnregisterApprovalErrorCoversReviewedFailures verifies only the audited lifecycle taxonomy reaches stable wire codes.
func TestClassifyProjectUnregisterApprovalErrorCoversReviewedFailures(t *testing.T) {
	operationID := domain.OperationID("operation-remove")
	projectID := domain.ProjectID("project-orders")
	tests := []struct {
		name     string
		cause    error
		wantCode rpc.ErrorCode
	}{
		{name: "stale operation revision", cause: &state.StaleRevisionError{OperationID: operationID, Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "project busy", cause: &state.ProjectBusyError{ProjectID: projectID, OperationIDs: []domain.OperationID{"operation-other"}}, wantCode: rpc.ErrorCodeConflict},
		{name: "project revision", cause: &state.ProjectRevisionConflictError{ProjectID: projectID, Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "network revision", cause: &state.NetworkRevisionConflictError{Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "release facts", cause: &state.ProjectNetworkReleaseConflictError{ProjectID: projectID, OperationID: operationID, Difference: "lease set"}, wantCode: rpc.ErrorCodeConflict},
		{name: "durable release incomplete", cause: &state.ProjectNetworkReleaseIncompleteError{ProjectID: projectID, OperationID: operationID, State: state.ProjectNetworkReleaseReleasing}, wantCode: rpc.ErrorCodeConflict},
		{name: "active release", cause: &state.ProjectNetworkReleaseActiveError{ProjectID: projectID, OperationID: operationID, State: state.ProjectNetworkReleaseReleasing, Action: "unregister project"}, wantCode: rpc.ErrorCodeConflict},
		{name: "host state", cause: &reconcile.HostStateConflictError{Address: netip.MustParseAddr("127.77.0.11"), State: loopback.StateForeign}, wantCode: rpc.ErrorCodeConflict},
		{name: "release progress", cause: &reconcile.ReleaseIncompleteError{OperationID: operationID, Remaining: 1}, wantCode: rpc.ErrorCodeConflict},
		{name: "withdrawal verification", cause: fmt.Errorf("runtime observation: %w", harbordruntime.ErrProjectWithdrawalUnverified), wantCode: rpc.ErrorCodeConflict},
		{name: "operation missing", cause: &state.OperationNotFoundError{OperationID: operationID}, wantCode: rpc.ErrorCodeNotFound},
		{name: "project missing", cause: &state.ProjectNotFoundError{ProjectID: projectID}, wantCode: rpc.ErrorCodeNotFound},
		{name: "release missing", cause: &state.ProjectNetworkReleaseNotFoundError{ProjectID: projectID, OperationID: operationID}, wantCode: rpc.ErrorCodeNotFound},
		{name: "cancelled", cause: fmt.Errorf("coordinator: %w", context.Canceled)},
		{name: "deadline", cause: fmt.Errorf("coordinator: %w", context.DeadlineExceeded)},
		{name: "unknown", cause: errors.New("database unavailable")},
		{name: "corruption around not found", cause: &state.CorruptStateError{Entity: "operation", Key: string(operationID), Cause: &state.OperationNotFoundError{OperationID: operationID}}},
		{name: "corruption around conflict", cause: &state.CorruptStateError{Entity: "network", Key: "singleton", Cause: &state.NetworkRevisionConflictError{Expected: 41, Actual: 42}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("approval boundary: %w", test.cause)
			classified := classifyProjectUnregisterApprovalError(wrapped)
			if !errors.Is(classified, test.cause) {
				t.Fatalf("classified error = %v, want original cause %v", classified, test.cause)
			}
			var handlerError *session.HandlerError
			if test.wantCode == "" {
				if errors.As(classified, &handlerError) {
					t.Fatalf("classified error = %#v, want unclassified/internal", classified)
				}
				return
			}
			if !errors.As(classified, &handlerError) || handlerError.Code() != test.wantCode {
				t.Fatalf("classified error = %#v, want %q", classified, test.wantCode)
			}
		})
	}
}

// TestAuthorityConstructionRejectsNilDependencies verifies production and interface constructors fail before dispatch.
func TestAuthorityConstructionRejectsNilDependencies(t *testing.T) {
	var typedNilStore *recordingStore
	var typedNilApprovals *recordingProjectUnregisterApprovals
	var typedNilDiscoverer *registrationDiscoverer
	var nilClock func() time.Time
	var nilProjectIDFactory func() (domain.ProjectID, error)
	for _, test := range []struct {
		name string
		call func()
	}{
		{name: "production store", call: func() { NewAuthority(nil, new(reconcile.ProjectUnregisterCoordinator)) }},
		{name: "production approvals", call: func() { NewAuthority(new(state.Store), nil) }},
		{name: "private store", call: func() { newAuthority(nil, testProjectUnregisterApprovals(), buildinfo.Info{}) }},
		{name: "private approvals", call: func() { newAuthority(&recordingStore{}, nil, buildinfo.Info{}) }},
		{name: "typed private store", call: func() { newAuthority(typedNilStore, testProjectUnregisterApprovals(), buildinfo.Info{}) }},
		{name: "typed private approvals", call: func() { newAuthority(&recordingStore{}, typedNilApprovals, buildinfo.Info{}) }},
		{name: "registration discoverer", call: func() {
			newAuthorityWithRegistration(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, nil, time.Now, newOpaqueProjectID)
		}},
		{name: "typed registration discoverer", call: func() {
			newAuthorityWithRegistration(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, typedNilDiscoverer, time.Now, newOpaqueProjectID)
		}},
		{name: "registration clock", call: func() {
			newAuthorityWithRegistration(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, &registrationDiscoverer{}, nilClock, newOpaqueProjectID)
		}},
		{name: "registration project ID factory", call: func() {
			newAuthorityWithRegistration(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, &registrationDiscoverer{}, time.Now, nilProjectIDFactory)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertAuthorityPanic(t, test.call)
		})
	}
}

// assertAuthorityHandlerError verifies one local cause is retained under its reviewed wire classification.
func assertAuthorityHandlerError(t *testing.T, err error, cause error, code rpc.ErrorCode) {
	t.Helper()
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != code || !errors.Is(err, cause) {
		t.Fatalf("authority error = %#v, want %q wrapping %v", err, code, cause)
	}
}

// assertAuthorityPanic fails when required authority wiring is allowed to remain absent.
func assertAuthorityPanic(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("constructor did not panic")
		}
	}()
	call()
}
