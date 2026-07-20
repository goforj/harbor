package authority

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// TestAuthorityProjectLifecycleDelegatesDaemonIdentityAndClientIntent proves control cannot choose process-operation identity.
func TestAuthorityProjectLifecycleDelegatesDaemonIdentityAndClientIntent(t *testing.T) {
	at := time.Date(2026, time.July, 19, 7, 0, 0, 0, time.UTC)
	startOperation, err := domain.NewOperation("operation-fixed", "intent-start", domain.OperationKindProjectStart, "project-orders", at)
	if err != nil {
		t.Fatalf("NewOperation(start) error = %v", err)
	}
	stopOperation, err := domain.NewOperation("operation-fixed", "intent-stop", domain.OperationKindProjectStop, "project-orders", at)
	if err != nil {
		t.Fatalf("NewOperation(stop) error = %v", err)
	}
	lifecycle := &recordingProjectLifecycle{
		startRecord: state.OperationRecord{Operation: startOperation, Revision: 12},
		stopRecord:  state.OperationRecord{Operation: stopOperation, Revision: 13},
	}
	authority := newAuthorityWithIdentityFactories(
		&recordingStore{},
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{},
		func() time.Time { return at },
		func() (domain.ProjectID, error) { return "project-unused", nil },
		func() (domain.OperationID, error) { return "operation-fixed", nil },
		func() (identity.InstallationID, error) { return "installation-unused", nil },
		lifecycle,
		testNetworkSetups(),
		testNetworkResolverSetups(),
		testHTTPRoutes(),
	)

	start, err := authority.StartProject(t.Context(), control.Caller{}, control.StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start"})
	if err != nil {
		t.Fatalf("StartProject() error = %v", err)
	}
	stop, err := authority.StopProject(t.Context(), control.Caller{}, control.StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop"})
	if err != nil {
		t.Fatalf("StopProject() error = %v", err)
	}
	if start.Operation != startOperation || start.Revision != 12 || stop.Operation != stopOperation || stop.Revision != 13 {
		t.Fatalf("lifecycle results = %#v / %#v", start, stop)
	}
	if !reflect.DeepEqual(lifecycle.starts, []reconcile.ProjectStartRequest{{
		ProjectID: "project-orders", OperationID: "operation-fixed", IntentID: "intent-start",
	}}) || !reflect.DeepEqual(lifecycle.stops, []reconcile.ProjectStopRequest{{
		ProjectID: "project-orders", OperationID: "operation-fixed", IntentID: "intent-stop",
	}}) {
		t.Fatalf("lifecycle requests = %#v / %#v", lifecycle.starts, lifecycle.stops)
	}
}

// TestAuthorityProjectLifecycleClassifiesReviewedFailures keeps request-state errors stable at the control boundary.
func TestAuthorityProjectLifecycleClassifiesReviewedFailures(t *testing.T) {
	missing := &state.ProjectNotFoundError{ProjectID: "project-orders"}
	conflict := &state.ProjectSessionActiveError{ProjectID: "project-orders", SessionID: "session-current"}
	for _, test := range []struct {
		name      string
		cause     error
		wantCode  rpc.ErrorCode
		configure func(*recordingProjectLifecycle)
		call      func(*Authority) error
	}{
		{
			name: "start not found", cause: missing, wantCode: rpc.ErrorCodeNotFound,
			configure: func(lifecycle *recordingProjectLifecycle) { lifecycle.startErr = missing },
			call: func(authority *Authority) error {
				_, err := authority.StartProject(t.Context(), control.Caller{}, control.StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start"})
				return err
			},
		},
		{
			name: "stop conflict", cause: conflict, wantCode: rpc.ErrorCodeConflict,
			configure: func(lifecycle *recordingProjectLifecycle) { lifecycle.stopErr = conflict },
			call: func(authority *Authority) error {
				_, err := authority.StopProject(t.Context(), control.Caller{}, control.StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop"})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := new(recordingProjectLifecycle)
			test.configure(lifecycle)
			authority := newAuthorityWithIdentityFactories(
				&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, &registrationDiscoverer{}, time.Now,
				func() (domain.ProjectID, error) { return "project-unused", nil },
				func() (domain.OperationID, error) { return "operation-fixed", nil },
				func() (identity.InstallationID, error) { return "installation-unused", nil }, lifecycle, testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes(),
			)
			err := test.call(authority)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.wantCode || !errors.Is(err, test.cause) {
				t.Fatalf("lifecycle error = %#v, want %s wrapping %v", err, test.wantCode, test.cause)
			}
		})
	}
}

// TestAuthorityProjectLifecycleRejectsMalformedRequestsBeforeIdentityGeneration proves invalid control data cannot consume authority.
func TestAuthorityProjectLifecycleRejectsMalformedRequestsBeforeIdentityGeneration(t *testing.T) {
	lifecycle := new(recordingProjectLifecycle)
	authority := newAuthorityWithIdentityFactories(
		&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{}, &registrationDiscoverer{}, time.Now,
		func() (domain.ProjectID, error) { return "project-unused", nil },
		func() (domain.OperationID, error) { return "", errors.New("identity factory must not run") },
		func() (identity.InstallationID, error) { return "installation-unused", nil }, lifecycle, testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes(),
	)
	_, startErr := authority.StartProject(t.Context(), control.Caller{}, control.StartProjectRequest{})
	_, stopErr := authority.StopProject(t.Context(), control.Caller{}, control.StopProjectRequest{})
	for _, err := range []error{startErr, stopErr} {
		var handlerError *session.HandlerError
		if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeInvalidRequest {
			t.Fatalf("invalid lifecycle request error = %#v", err)
		}
	}
	if len(lifecycle.starts) != 0 || len(lifecycle.stops) != 0 {
		t.Fatalf("invalid lifecycle requests reached coordinator: %#v / %#v", lifecycle.starts, lifecycle.stops)
	}
}
