package authority

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingProjectRuntimeRepairCoordinator retains exact authority requests and returns configured results.
type recordingProjectRuntimeRepairCoordinator struct {
	inspection      reconcile.ProjectRuntimeRepairInspection
	inspectionErr   error
	confirmation    state.ProjectRecord
	confirmationErr error
	inspections     []reconcile.ProjectRuntimeRepairInspectRequest
	confirmations   []reconcile.ProjectRuntimeRepairConfirmRequest
}

// Inspect records the caller-bound selection before returning its configured projection.
func (coordinator *recordingProjectRuntimeRepairCoordinator) Inspect(
	_ context.Context,
	request reconcile.ProjectRuntimeRepairInspectRequest,
) (reconcile.ProjectRuntimeRepairInspection, error) {
	coordinator.inspections = append(coordinator.inspections, request)
	return coordinator.inspection, coordinator.inspectionErr
}

// Confirm records the opaque selection before returning its configured durable completion.
func (coordinator *recordingProjectRuntimeRepairCoordinator) Confirm(
	_ context.Context,
	request reconcile.ProjectRuntimeRepairConfirmRequest,
) (state.ProjectRecord, error) {
	coordinator.confirmations = append(coordinator.confirmations, request)
	return coordinator.confirmation, coordinator.confirmationErr
}

// TestAuthorityProjectRuntimeRepairMapsCallerAndSafeResults verifies authority forwards both authenticated identity layers without native evidence.
func TestAuthorityProjectRuntimeRepairMapsCallerAndSafeResults(t *testing.T) {
	checkout := projectRuntimeRepairTestCheckout(t)
	at := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	project := projectRuntimeRepairTestProject(t, checkout, at)
	caller := projectRuntimeRepairTestCaller()
	inspectionID := reconcile.ProjectRuntimeRepairInspectionID(strings.Repeat("1", 64))
	fingerprint := reconcile.ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("a", 64))
	coordinator := &recordingProjectRuntimeRepairCoordinator{
		inspection: reconcile.ProjectRuntimeRepairInspection{
			ProjectID:   project.ID,
			Disposition: reconcile.ProjectRuntimeRepairInspectionConfirmable,
			Confirmable: &reconcile.ProjectRuntimeRepairConfirmable{
				Display: reconcile.ProjectRuntimeRepairDisplay{
					RootPID:      741,
					Command:      "forj dev",
					CheckoutRoot: checkout,
					Endpoint:     netip.MustParseAddrPort("127.0.0.72:39471"),
					ProcessCount: 3,
				},
				InspectionID: inspectionID,
				Fingerprint:  fingerprint,
				ExpiresAt:    at.Add(time.Minute),
			},
		},
		confirmation: state.ProjectRecord{Project: project, Revision: 19},
	}
	authority := projectRuntimeRepairTestAuthority(coordinator)

	inspection, err := authority.InspectProjectRuntimeRepair(t.Context(), caller, control.InspectProjectRuntimeRepairRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("InspectProjectRuntimeRepair() error = %v", err)
	}
	wantCaller := reconcile.ProjectRuntimeRepairCaller{UserID: "501", ProcessID: 4242, Role: rpc.RoleDesktop}
	if len(coordinator.inspections) != 1 || coordinator.inspections[0].ProjectID != project.ID || coordinator.inspections[0].Caller != wantCaller {
		t.Fatalf("inspection requests = %#v, want project %q caller %#v", coordinator.inspections, project.ID, wantCaller)
	}
	wantInspection := control.ProjectRuntimeRepairInspection{
		ProjectID:   project.ID,
		Disposition: control.ProjectRuntimeRepairInspectionConfirmable,
		Confirmable: &control.ProjectRuntimeRepairConfirmable{
			Candidate: control.ProjectRuntimeRepairDisplayFacts{
				Command:     "forj dev",
				Checkout:    checkout,
				Endpoint:    "127.0.0.72:39471",
				RootPID:     741,
				MemberCount: 3,
			},
			InspectionID:         control.ProjectRuntimeRepairInspectionID(inspectionID),
			CandidateFingerprint: control.ProjectRuntimeRepairCandidateFingerprint(fingerprint),
			ExpiresAt:            at.Add(time.Minute),
		},
	}
	if !reflect.DeepEqual(inspection, wantInspection) {
		t.Fatalf("inspection = %#v, want %#v", inspection, wantInspection)
	}

	confirmationRequest := control.ConfirmProjectRuntimeRepairRequest{
		ProjectID:    project.ID,
		InspectionID: control.ProjectRuntimeRepairInspectionID(inspectionID),
		Fingerprint:  control.ProjectRuntimeRepairCandidateFingerprint(fingerprint),
	}
	confirmation, err := authority.ConfirmProjectRuntimeRepair(t.Context(), caller, confirmationRequest)
	if err != nil {
		t.Fatalf("ConfirmProjectRuntimeRepair() error = %v", err)
	}
	if len(coordinator.confirmations) != 1 {
		t.Fatalf("confirmation requests = %#v, want one", coordinator.confirmations)
	}
	wantConfirmationRequest := reconcile.ProjectRuntimeRepairConfirmRequest{
		Caller:       wantCaller,
		ProjectID:    project.ID,
		InspectionID: inspectionID,
		Fingerprint:  fingerprint,
	}
	if coordinator.confirmations[0] != wantConfirmationRequest {
		t.Fatalf("confirmation request = %#v, want %#v", coordinator.confirmations[0], wantConfirmationRequest)
	}
	if !reflect.DeepEqual(confirmation, control.ProjectRuntimeRepairConfirmation{Project: project, Revision: 19}) {
		t.Fatalf("confirmation = %#v", confirmation)
	}
}

// TestAuthorityInspectProjectRuntimeRepairMapsFixedStates verifies safe native classifications retain their exact protocol shape.
func TestAuthorityInspectProjectRuntimeRepairMapsFixedStates(t *testing.T) {
	projectID := domain.ProjectID("project-orders")
	tests := []struct {
		name            string
		inspection      reconcile.ProjectRuntimeRepairInspection
		wantDisposition control.ProjectRuntimeRepairInspectionDisposition
		wantReason      control.ProjectRuntimeRepairNotActionableReason
	}{
		{
			name:            "unsupported",
			inspection:      reconcile.ProjectRuntimeRepairInspection{ProjectID: projectID, Disposition: reconcile.ProjectRuntimeRepairInspectionUnsupported},
			wantDisposition: control.ProjectRuntimeRepairInspectionUnsupported,
		},
		{
			name:            "missing",
			inspection:      reconcile.ProjectRuntimeRepairInspection{ProjectID: projectID, Disposition: reconcile.ProjectRuntimeRepairInspectionNotActionable, Reason: reconcile.ProjectRuntimeRepairReasonNone},
			wantDisposition: control.ProjectRuntimeRepairInspectionNotActionable,
			wantReason:      control.ProjectRuntimeRepairReasonNone,
		},
		{
			name:            "ambiguous",
			inspection:      reconcile.ProjectRuntimeRepairInspection{ProjectID: projectID, Disposition: reconcile.ProjectRuntimeRepairInspectionNotActionable, Reason: reconcile.ProjectRuntimeRepairReasonAmbiguous},
			wantDisposition: control.ProjectRuntimeRepairInspectionNotActionable,
			wantReason:      control.ProjectRuntimeRepairReasonAmbiguous,
		},
		{
			name:            "foreign",
			inspection:      reconcile.ProjectRuntimeRepairInspection{ProjectID: projectID, Disposition: reconcile.ProjectRuntimeRepairInspectionNotActionable, Reason: reconcile.ProjectRuntimeRepairReasonForeign},
			wantDisposition: control.ProjectRuntimeRepairInspectionNotActionable,
			wantReason:      control.ProjectRuntimeRepairReasonForeign,
		},
		{
			name:            "unreadable",
			inspection:      reconcile.ProjectRuntimeRepairInspection{ProjectID: projectID, Disposition: reconcile.ProjectRuntimeRepairInspectionNotActionable, Reason: reconcile.ProjectRuntimeRepairReasonUnreadable},
			wantDisposition: control.ProjectRuntimeRepairInspectionNotActionable,
			wantReason:      control.ProjectRuntimeRepairReasonUnreadable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingProjectRuntimeRepairCoordinator{inspection: test.inspection}
			authority := projectRuntimeRepairTestAuthority(coordinator)
			got, err := authority.InspectProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), control.InspectProjectRuntimeRepairRequest{ProjectID: projectID})
			if err != nil {
				t.Fatalf("InspectProjectRuntimeRepair() error = %v", err)
			}
			if got.Disposition != test.wantDisposition || got.Reason != test.wantReason || got.Confirmable != nil {
				t.Fatalf("inspection = %#v, want disposition %q reason %q", got, test.wantDisposition, test.wantReason)
			}
		})
	}
}

// TestAuthorityProjectRuntimeRepairRejectsInvalidAndMalformedValues verifies neither client nor coordinator can bypass control validation.
func TestAuthorityProjectRuntimeRepairRejectsInvalidAndMalformedValues(t *testing.T) {
	coordinator := &recordingProjectRuntimeRepairCoordinator{}
	authority := projectRuntimeRepairTestAuthority(coordinator)
	_, inspectErr := authority.InspectProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), control.InspectProjectRuntimeRepairRequest{})
	assertProjectRuntimeRepairHandlerCode(t, inspectErr, rpc.ErrorCodeInvalidRequest)
	_, confirmErr := authority.ConfirmProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), control.ConfirmProjectRuntimeRepairRequest{})
	assertProjectRuntimeRepairHandlerCode(t, confirmErr, rpc.ErrorCodeInvalidRequest)
	if len(coordinator.inspections) != 0 || len(coordinator.confirmations) != 0 {
		t.Fatalf("invalid requests reached coordinator: inspections=%d confirmations=%d", len(coordinator.inspections), len(coordinator.confirmations))
	}

	coordinator.inspection = reconcile.ProjectRuntimeRepairInspection{
		ProjectID:   "project-orders",
		Disposition: reconcile.ProjectRuntimeRepairInspectionConfirmable,
	}
	if _, err := authority.InspectProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), control.InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}); err == nil {
		t.Fatal("InspectProjectRuntimeRepair() malformed result error = nil")
	}
	checkout := projectRuntimeRepairTestCheckout(t)
	coordinator.confirmation = state.ProjectRecord{Project: projectRuntimeRepairTestProject(t, checkout, time.Now()), Revision: 0}
	validConfirm := control.ConfirmProjectRuntimeRepairRequest{
		ProjectID:    "project-orders",
		InspectionID: control.ProjectRuntimeRepairInspectionID(strings.Repeat("1", 64)),
		Fingerprint:  control.ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("a", 64)),
	}
	if _, err := authority.ConfirmProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), validConfirm); err == nil {
		t.Fatal("ConfirmProjectRuntimeRepair() malformed result error = nil")
	}
}

// TestAuthorityProjectRuntimeRepairClassifiesCoordinatorFailures verifies clients receive only stable retry categories.
func TestAuthorityProjectRuntimeRepairClassifiesCoordinatorFailures(t *testing.T) {
	resourceFailure := errors.New("repair storage unavailable")
	tests := []struct {
		name         string
		err          error
		wantCode     rpc.ErrorCode
		wantIdentity error
	}{
		{name: "project missing", err: &state.ProjectNotFoundError{ProjectID: "project-orders"}, wantCode: rpc.ErrorCodeNotFound},
		{name: "plan missing", err: &reconcile.ProjectRuntimeRepairPlanNotFoundError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "plan mismatch", err: &reconcile.ProjectRuntimeRepairPlanMismatchError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "plan expired", err: &reconcile.ProjectRuntimeRepairPlanExpiredError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "plan capacity", err: &reconcile.ProjectRuntimeRepairPlanCapacityError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "durable drift", err: &reconcile.ProjectRuntimeRepairDurableDriftError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "discovery drift", err: &reconcile.ProjectRuntimeRepairDiscoveryDriftError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "native drift", err: &reconcile.ProjectRuntimeRepairNativeDriftError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "native failure", err: &reconcile.ProjectRuntimeRepairNativeFailureError{}, wantCode: rpc.ErrorCodeConflict},
		{name: "backend drift", err: projectprocess.ErrRuntimeRepairDrift, wantCode: rpc.ErrorCodeConflict},
		{name: "backend not settled", err: projectprocess.ErrRuntimeRepairNotSettled, wantCode: rpc.ErrorCodeConflict},
		{name: "cancelled", err: context.Canceled, wantIdentity: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, wantIdentity: context.DeadlineExceeded},
		{name: "resource failure", err: resourceFailure, wantIdentity: resourceFailure},
	}
	request := control.InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingProjectRuntimeRepairCoordinator{inspectionErr: test.err}
			authority := projectRuntimeRepairTestAuthority(coordinator)
			_, err := authority.InspectProjectRuntimeRepair(t.Context(), projectRuntimeRepairTestCaller(), request)
			if test.wantCode != "" {
				assertProjectRuntimeRepairHandlerCode(t, err, test.wantCode)
				return
			}
			if !errors.Is(err, test.wantIdentity) {
				t.Fatalf("InspectProjectRuntimeRepair() error = %v, want %v", err, test.wantIdentity)
			}
			var handlerError *session.HandlerError
			if errors.As(err, &handlerError) {
				t.Fatalf("InspectProjectRuntimeRepair() error = %#v, want unclassified identity", err)
			}
		})
	}
}

// projectRuntimeRepairTestAuthority constructs a fully wired private authority and replaces only its repair seam.
func projectRuntimeRepairTestAuthority(coordinator projectRuntimeRepairCoordinator) *Authority {
	authority := newAuthority(
		&recordingStore{},
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		testProjectLifecycles(),
		testNetworkSetups(),
		testNetworkResolverSetups(),
		testHTTPRoutes(),
	)
	authority.runtimeRepair = coordinator
	return authority
}

// projectRuntimeRepairTestCaller returns one complete desktop identity for caller-binding assertions.
func projectRuntimeRepairTestCaller() control.Caller {
	return control.Caller{
		Transport: local.PeerIdentity{UserID: "501", ProcessID: 4242},
		Session:   session.Peer{Role: rpc.RoleDesktop},
	}
}

// projectRuntimeRepairTestCheckout returns an existing canonical directory suitable for public display validation.
func projectRuntimeRepairTestCheckout(t *testing.T) string {
	t.Helper()
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	return checkout
}

// projectRuntimeRepairTestProject builds one valid stopped projection at the requested checkout.
func projectRuntimeRepairTestProject(t *testing.T, checkout string, at time.Time) domain.ProjectSnapshot {
	t.Helper()
	project, err := (projectdiscovery.Discovery{Root: checkout, Name: "Orders API", Slug: "orders-api"}).ProjectSnapshot("project-orders", at)
	if err != nil {
		t.Fatalf("ProjectSnapshot() error = %v", err)
	}
	return project
}

// assertProjectRuntimeRepairHandlerCode verifies one authority error carries only the expected reviewed RPC category.
func assertProjectRuntimeRepairHandlerCode(t *testing.T, err error, want rpc.ErrorCode) {
	t.Helper()
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != want {
		t.Fatalf("handler error = %#v, want code %q", err, want)
	}
}
