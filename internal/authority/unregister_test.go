package authority

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// authorityTestUnregisterRecord returns one valid durable operation result for initiation mapping.
func authorityTestUnregisterRecord(t *testing.T) state.OperationRecord {
	t.Helper()
	return state.OperationRecord{
		Operation: validAuthoritySucceededOperation(t),
		Revision:  43,
	}
}

// newAuthorityForUnregisterTest builds authority with deterministic daemon operation identity.
func newAuthorityForUnregisterTest(
	coordinator projectUnregisterCoordinator,
	newOperationID func() (domain.OperationID, error),
) *Authority {
	return newAuthorityWithIdentityFactories(
		&recordingStore{},
		coordinator,
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{},
		time.Now,
		func() (domain.ProjectID, error) { return "project-unused", nil },
		newOperationID,
		testProjectLifecycles(),
	)
}

// TestAuthorityUnregisterProjectJoinsClientIntentWithDaemonOperationIdentity verifies the complete initiation boundary.
func TestAuthorityUnregisterProjectJoinsClientIntentWithDaemonOperationIdentity(t *testing.T) {
	record := authorityTestUnregisterRecord(t)
	coordinator := &recordingProjectUnregisterApprovals{startResult: record}
	authority := newAuthorityForUnregisterTest(
		coordinator,
		func() (domain.OperationID, error) { return "operation-candidate", nil },
	)
	request := control.UnregisterProjectRequest{
		ProjectID: record.Operation.ProjectID,
		IntentID:  record.Operation.IntentID,
	}

	result, err := authority.UnregisterProject(nil, control.Caller{}, request)
	if err != nil {
		t.Fatalf("UnregisterProject() error = %v", err)
	}
	want := control.ProjectUnregistration{Operation: record.Operation, Revision: record.Revision}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("unregistration = %#v, want %#v", result, want)
	}
	requests, nilContexts := coordinator.starts()
	if !reflect.DeepEqual(requests, []reconcile.StartRequest{{
		ProjectID:   request.ProjectID,
		OperationID: "operation-candidate",
		IntentID:    request.IntentID,
	}}) {
		t.Fatalf("coordinator requests = %#v", requests)
	}
	if nilContexts != 0 {
		t.Fatalf("coordinator nil contexts = %d, want 0", nilContexts)
	}
}

// TestAuthorityUnregisterProjectReplayCanReturnAnotherDaemonOperationID proves candidates are not client correlation authority.
func TestAuthorityUnregisterProjectReplayCanReturnAnotherDaemonOperationID(t *testing.T) {
	record := authorityTestUnregisterRecord(t)
	coordinator := &recordingProjectUnregisterApprovals{startResult: record}
	candidates := []domain.OperationID{"operation-candidate-one", "operation-candidate-two"}
	next := 0
	authority := newAuthorityForUnregisterTest(coordinator, func() (domain.OperationID, error) {
		operationID := candidates[next]
		next++
		return operationID, nil
	})
	request := control.UnregisterProjectRequest{
		ProjectID: record.Operation.ProjectID,
		IntentID:  record.Operation.IntentID,
	}

	first, firstErr := authority.UnregisterProject(t.Context(), control.Caller{}, request)
	second, secondErr := authority.UnregisterProject(t.Context(), control.Caller{}, request)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("replayed UnregisterProject() errors = %v / %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first, second) || first.Operation.ID != record.Operation.ID {
		t.Fatalf("replayed results = %#v / %#v, want durable operation %#v", first, second, record.Operation.ID)
	}
	requests, _ := coordinator.starts()
	if len(requests) != 2 || requests[0].OperationID != candidates[0] || requests[1].OperationID != candidates[1] {
		t.Fatalf("coordinator candidates = %#v, want %v", requests, candidates)
	}
}

// TestAuthorityUnregisterProjectStopsAtValidationAndIdentityFailures proves no partial coordinator call occurs.
func TestAuthorityUnregisterProjectStopsAtValidationAndIdentityFailures(t *testing.T) {
	identityFailure := errors.New("operation identity entropy unavailable")
	for _, test := range []struct {
		name      string
		request   control.UnregisterProjectRequest
		factory   func() (domain.OperationID, error)
		want      error
		wantCalls int
	}{
		{
			name:    "invalid request",
			request: control.UnregisterProjectRequest{},
			factory: func() (domain.OperationID, error) {
				t.Fatal("operation identity factory called for invalid request")
				return "", nil
			},
		},
		{
			name:    "identity failure",
			request: control.UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"},
			factory: func() (domain.OperationID, error) { return "", identityFailure },
			want:    identityFailure,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingProjectUnregisterApprovals{}
			authority := newAuthorityForUnregisterTest(coordinator, test.factory)
			_, err := authority.UnregisterProject(t.Context(), control.Caller{}, test.request)
			if err == nil {
				t.Fatal("UnregisterProject() error = nil")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("UnregisterProject() error = %v, want %v", err, test.want)
			}
			requests, _ := coordinator.starts()
			if len(requests) != test.wantCalls {
				t.Fatalf("coordinator calls = %d, want %d", len(requests), test.wantCalls)
			}
		})
	}
}

// TestAuthorityUnregisterProjectRejectsMismatchedAndInvalidResults proves coordinator output cannot cross intent boundaries.
func TestAuthorityUnregisterProjectRejectsMismatchedAndInvalidResults(t *testing.T) {
	record := authorityTestUnregisterRecord(t)
	request := control.UnregisterProjectRequest{
		ProjectID: record.Operation.ProjectID,
		IntentID:  record.Operation.IntentID,
	}
	for _, test := range []struct {
		name   string
		mutate func(*state.OperationRecord)
	}{
		{name: "project", mutate: func(result *state.OperationRecord) { result.Operation.ProjectID = "project-other" }},
		{name: "intent", mutate: func(result *state.OperationRecord) { result.Operation.IntentID = "intent-other" }},
		{name: "kind", mutate: func(result *state.OperationRecord) { result.Operation.Kind = "project.refresh" }},
		{name: "revision", mutate: func(result *state.OperationRecord) { result.Revision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := record
			test.mutate(&invalid)
			authority := newAuthorityForUnregisterTest(
				&recordingProjectUnregisterApprovals{startResult: invalid},
				func() (domain.OperationID, error) { return "operation-candidate", nil },
			)
			if _, err := authority.UnregisterProject(t.Context(), control.Caller{}, request); err == nil {
				t.Fatal("UnregisterProject() error = nil")
			} else {
				var handlerError *session.HandlerError
				if errors.As(err, &handlerError) {
					t.Fatalf("invalid coordinator output = %#v, must remain internal", err)
				}
			}
		})
	}
}

// TestClassifyProjectUnregisterErrorCoversInitiationFailures verifies client-selected and daemon-owned failures stay distinct.
func TestClassifyProjectUnregisterErrorCoversInitiationFailures(t *testing.T) {
	operationID := domain.OperationID("operation-remove")
	projectID := domain.ProjectID("project-orders")
	intentID := domain.IntentID("intent-remove")
	tests := []struct {
		name     string
		cause    error
		wantCode rpc.ErrorCode
	}{
		{
			name: "intent conflict",
			cause: &state.IntentConflictError{
				IntentID:            intentID,
				ExistingOperationID: operationID,
				ExistingKind:        domain.OperationKindProjectUnregister,
				ExistingProjectID:   "project-other",
				RequestedKind:       domain.OperationKindProjectUnregister,
				RequestedProjectID:  projectID,
			},
			wantCode: rpc.ErrorCodeConflict,
		},
		{name: "stale operation", cause: &state.StaleRevisionError{OperationID: operationID, Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "project busy", cause: &state.ProjectBusyError{ProjectID: projectID, OperationIDs: []domain.OperationID{"operation-other"}}, wantCode: rpc.ErrorCodeConflict},
		{name: "project revision", cause: &state.ProjectRevisionConflictError{ProjectID: projectID, Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "network revision", cause: &state.NetworkRevisionConflictError{Expected: 41, Actual: 42}, wantCode: rpc.ErrorCodeConflict},
		{name: "network projects", cause: &state.NetworkProjectSetConflictError{Expected: []domain.ProjectID{projectID}, Actual: []domain.ProjectID{"project-other"}}, wantCode: rpc.ErrorCodeConflict},
		{name: "network replacement", cause: &state.NetworkProjectReplacementConflictError{ProjectID: projectID, Difference: "lease ownership"}, wantCode: rpc.ErrorCodeConflict},
		{name: "release facts", cause: &state.ProjectNetworkReleaseConflictError{ProjectID: projectID, OperationID: operationID, Difference: "lease set"}, wantCode: rpc.ErrorCodeConflict},
		{name: "release incomplete", cause: &state.ProjectNetworkReleaseIncompleteError{ProjectID: projectID, OperationID: operationID, State: state.ProjectNetworkReleaseReleasing}, wantCode: rpc.ErrorCodeConflict},
		{name: "active release", cause: &state.ProjectNetworkReleaseActiveError{ProjectID: projectID, OperationID: operationID, State: state.ProjectNetworkReleaseReleasing, Action: "unregister project"}, wantCode: rpc.ErrorCodeConflict},
		{name: "host state", cause: &reconcile.HostStateConflictError{Address: netip.MustParseAddr("127.77.0.11"), State: loopback.StateForeign}, wantCode: rpc.ErrorCodeConflict},
		{name: "release progress", cause: &reconcile.ReleaseIncompleteError{OperationID: operationID, Remaining: 1}, wantCode: rpc.ErrorCodeConflict},
		{name: "withdrawal", cause: fmt.Errorf("runtime: %w", harbordruntime.ErrProjectWithdrawalUnverified), wantCode: rpc.ErrorCodeConflict},
		{name: "project missing", cause: &state.ProjectNotFoundError{ProjectID: projectID}, wantCode: rpc.ErrorCodeNotFound},
		{name: "daemon operation missing", cause: &state.OperationNotFoundError{OperationID: operationID}},
		{name: "intent lookup missing", cause: &state.OperationIntentNotFoundError{IntentID: intentID}},
		{name: "daemon operation collision", cause: &state.OperationIDConflictError{OperationID: operationID, ExistingIntentID: "intent-other", RequestedIntentID: intentID}},
		{name: "release boundary missing", cause: &state.ProjectNetworkReleaseNotFoundError{ProjectID: projectID, OperationID: operationID}},
		{name: "network initialization", cause: &state.NetworkInitializationConflictError{ActualRevision: 41, Difference: "listener plan"}},
		{name: "network not initialized", cause: &state.NetworkNotInitializedError{}},
		{name: "cancelled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "unknown", cause: errors.New("database unavailable")},
		{name: "corrupt missing project", cause: &state.CorruptStateError{Entity: "project", Key: string(projectID), Cause: &state.ProjectNotFoundError{ProjectID: projectID}}},
		{name: "corrupt conflict", cause: &state.CorruptStateError{Entity: "network", Key: "singleton", Cause: &state.NetworkRevisionConflictError{Expected: 41, Actual: 42}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("start boundary: %w", test.cause)
			classified := classifyProjectUnregisterError(wrapped)
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
				t.Fatalf("classified error = %#v, want %q", classified, test.wantCode)
			}
		})
	}
}

// TestAuthorityUnregisterProjectMapsReviewedErrors verifies classifier output survives the authority boundary.
func TestAuthorityUnregisterProjectMapsReviewedErrors(t *testing.T) {
	request := control.UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}
	for _, test := range []struct {
		name string
		err  error
		code rpc.ErrorCode
	}{
		{name: "missing", err: &state.ProjectNotFoundError{ProjectID: request.ProjectID}, code: rpc.ErrorCodeNotFound},
		{name: "busy", err: &state.ProjectBusyError{ProjectID: request.ProjectID, OperationIDs: []domain.OperationID{"operation-peer"}}, code: rpc.ErrorCodeConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newAuthorityForUnregisterTest(
				&recordingProjectUnregisterApprovals{startErr: test.err},
				func() (domain.OperationID, error) { return "operation-candidate", nil },
			)
			_, err := authority.UnregisterProject(t.Context(), control.Caller{}, request)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.code || !errors.Is(err, test.err) {
				t.Fatalf("UnregisterProject() error = %#v, want %q wrapping cause", err, test.code)
			}
		})
	}
}

// TestNewOpaqueOperationIDReturnsIndependentDaemonIdentities verifies the production 128-bit format.
func TestNewOpaqueOperationIDReturnsIndependentDaemonIdentities(t *testing.T) {
	first, err := newOpaqueOperationID()
	if err != nil {
		t.Fatalf("newOpaqueOperationID() first error = %v", err)
	}
	second, err := newOpaqueOperationID()
	if err != nil {
		t.Fatalf("newOpaqueOperationID() second error = %v", err)
	}
	for _, operationID := range []domain.OperationID{first, second} {
		if err := operationID.Validate(); err != nil {
			t.Fatalf("generated operation ID %q is invalid: %v", operationID, err)
		}
		if !strings.HasPrefix(string(operationID), "operation-") || len(operationID) != 42 {
			t.Fatalf("operation ID = %q, want operation- plus 32 hexadecimal characters", operationID)
		}
	}
	if first == second {
		t.Fatalf("generated operation IDs are equal: %q", first)
	}
}

// TestAuthorityIdentityFactoryConstructionRejectsNilOperationFactory keeps daemon identity authority fail-fast.
func TestAuthorityIdentityFactoryConstructionRejectsNilOperationFactory(t *testing.T) {
	var nilOperationIDFactory func() (domain.OperationID, error)
	assertAuthorityPanic(t, func() {
		newAuthorityWithIdentityFactories(
			&recordingStore{},
			testProjectUnregisterApprovals(),
			buildinfo.Info{},
			&registrationDiscoverer{},
			time.Now,
			newOpaqueProjectID,
			nilOperationIDFactory,
			testProjectLifecycles(),
		)
	})
}
