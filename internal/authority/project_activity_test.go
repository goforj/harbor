package authority

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// TestAuthorityProjectActivityProjectsOnlyCurrentBoundedOutput verifies the control boundary drops process ownership details.
func TestAuthorityProjectActivityProjectsOnlyCurrentBoundedOutput(t *testing.T) {
	lifecycle := &recordingProjectLifecycle{activity: reconcile.ProjectActivity{
		ProjectID: "project-orders",
		Session: &reconcile.ProjectSessionActivity{
			ID:         "session-current",
			State:      domain.SessionAttached,
			Generation: 4,
			Output: projectprocess.OutputChunk{
				Available:  true,
				Reset:      true,
				Truncated:  true,
				HasMore:    true,
				NextCursor: 18,
				Text:       "ready\n",
			},
		},
	}}
	authority := projectActivityTestAuthority(lifecycle)
	request := control.ProjectActivityRequest{
		ProjectID:        "project-orders",
		SessionID:        "session-prior",
		Cursor:           90,
		WaitMilliseconds: 25_000,
	}
	got, err := authority.ProjectActivity(t.Context(), control.Caller{}, request)
	if err != nil {
		t.Fatalf("ProjectActivity() error = %v", err)
	}
	wantRequest := reconcile.ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-prior",
		Cursor:    90,
		Wait:      25 * time.Second,
	}
	if !reflect.DeepEqual(lifecycle.activities, []reconcile.ProjectActivityRequest{wantRequest}) {
		t.Fatalf("coordinator requests = %#v", lifecycle.activities)
	}
	if got.ProjectID != "project-orders" || got.Session == nil || got.Session.ID != "session-current" ||
		got.Session.State != domain.SessionAttached || got.Session.Generation != 4 {
		t.Fatalf("project activity = %#v", got)
	}
	wantOutput := control.ProjectOutputChunk{
		Available: true, Reset: true, Truncated: true, HasMore: true, NextCursor: 18, Text: "ready\n",
	}
	if got.Session.Output != wantOutput {
		t.Fatalf("project output = %#v, want %#v", got.Session.Output, wantOutput)
	}
}

// TestAuthorityProjectActivityClassifiesReviewedFailures verifies invalid and unknown selections retain stable control codes.
func TestAuthorityProjectActivityClassifiesReviewedFailures(t *testing.T) {
	missing := &state.ProjectNotFoundError{ProjectID: "project-orders"}
	privateFailure := errors.New("sqlite details")
	for _, test := range []struct {
		name      string
		request   control.ProjectActivityRequest
		cause     error
		wantCode  rpc.ErrorCode
		wantCause error
	}{
		{name: "invalid", request: control.ProjectActivityRequest{}, wantCode: rpc.ErrorCodeInvalidRequest},
		{name: "missing", request: control.ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0}, cause: missing, wantCode: rpc.ErrorCodeNotFound, wantCause: missing},
		{name: "internal", request: control.ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0}, cause: privateFailure, wantCause: privateFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := &recordingProjectLifecycle{activityErr: test.cause}
			_, err := projectActivityTestAuthority(lifecycle).ProjectActivity(t.Context(), control.Caller{}, test.request)
			if test.wantCode == "" {
				if !errors.Is(err, test.wantCause) {
					t.Fatalf("ProjectActivity() error = %#v, want %v", err, test.wantCause)
				}
				return
			}
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.wantCode {
				t.Fatalf("ProjectActivity() error = %#v, want %s", err, test.wantCode)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("ProjectActivity() error = %#v, want wrapping %v", err, test.wantCause)
			}
			if test.name == "invalid" && len(lifecycle.activities) != 0 {
				t.Fatalf("invalid request reached coordinator: %#v", lifecycle.activities)
			}
		})
	}
}

// TestAuthorityProjectActivityBoundsEscapedOutput verifies authority returns a continuation rather than an oversized wire value.
func TestAuthorityProjectActivityBoundsEscapedOutput(t *testing.T) {
	text := strings.Repeat("\"\\\n\t", projectprocess.MaximumOutputChunkBytes/4)
	lifecycle := &recordingProjectLifecycle{activity: reconcile.ProjectActivity{
		ProjectID: "project-orders",
		Session: &reconcile.ProjectSessionActivity{
			ID: "session-current", State: domain.SessionAttached, Generation: 1,
			Output: projectprocess.OutputChunk{Available: true, NextCursor: uint64(len(text)), Text: text},
		},
	}}
	got, err := projectActivityTestAuthority(lifecycle).ProjectActivity(
		t.Context(), control.Caller{}, control.ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0},
	)
	if err != nil {
		t.Fatalf("ProjectActivity() error = %v", err)
	}
	if got.Session == nil || !got.Session.Output.HasMore || got.Session.Output.Text == text ||
		got.Session.Output.NextCursor != uint64(len(got.Session.Output.Text)) {
		t.Fatalf("bounded activity = %#v", got)
	}
}

// projectActivityTestAuthority supplies initialized collaborators around one lifecycle recorder.
func projectActivityTestAuthority(lifecycle *recordingProjectLifecycle) *Authority {
	return newAuthorityWithIdentityFactories(
		&recordingStore{},
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{},
		time.Now,
		func() (domain.ProjectID, error) { return "project-unused", nil },
		func() (domain.OperationID, error) { return "operation-unused", nil },
		func() (identity.InstallationID, error) { return "installation-unused", nil },
		lifecycle,
		testNetworkSetups(),
		testNetworkResolverSetups(),
		testHTTPRoutes(),
	)
}
