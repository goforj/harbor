package authority

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// registrationDiscoverer returns one configured discovery while recording the requested path.
type registrationDiscoverer struct {
	discovery projectdiscovery.Discovery
	err       error
	paths     []string
}

// Discover records the selected path before returning the configured filesystem result.
func (discoverer *registrationDiscoverer) Discover(
	ctx context.Context,
	path string,
) (projectdiscovery.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return projectdiscovery.Discovery{}, err
	}
	discoverer.paths = append(discoverer.paths, path)
	return discoverer.discovery, discoverer.err
}

// TestAuthorityRegisterProjectJoinsDiscoveryAndAtomicState verifies authority owns the complete registration boundary.
func TestAuthorityRegisterProjectJoinsDiscoveryAndAtomicState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "orders")
	at := time.Date(2026, time.July, 18, 20, 0, 0, 123, time.FixedZone("offset", 3600))
	discovery := projectdiscovery.Discovery{Root: root, Name: "Orders API", Slug: "orders-api"}
	project, err := discovery.ProjectSnapshot("project-orders", at)
	if err != nil {
		t.Fatalf("build expected project: %v", err)
	}
	store := &recordingStore{registration: state.ProjectRegistration{
		Record:  state.ProjectRecord{Project: project, Revision: 9},
		Created: true,
	}}
	discoverer := &registrationDiscoverer{discovery: discovery}
	authority := newAuthorityWithRegistration(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, discoverer, func() time.Time { return at }, func() (domain.ProjectID, error) { return "project-orders", nil }, testProjectLifecycles(), testNetworkSetups(), testHTTPRoutes())
	request := control.RegisterProjectRequest{Path: root}

	registration, err := authority.RegisterProject(nil, control.Caller{}, request)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	want := control.ProjectRegistration{
		Project: project, Revision: 9, Created: true,
	}
	if !reflect.DeepEqual(registration, want) {
		t.Fatalf("registration = %#v, want %#v", registration, want)
	}
	if !reflect.DeepEqual(discoverer.paths, []string{root}) {
		t.Fatalf("discovery paths = %#v, want %q", discoverer.paths, root)
	}
	store.registrationMu.Lock()
	projects := append([]domain.ProjectSnapshot(nil), store.registrationProjects...)
	store.registrationMu.Unlock()
	if len(projects) != 1 || !reflect.DeepEqual(projects[0], project) {
		t.Fatalf("registered projects = %#v, want %#v", projects, project)
	}
}

// TestAuthorityRegisterProjectStopsAtValidationAndDiscoveryFailures proves no partial durable registration is attempted.
func TestAuthorityRegisterProjectStopsAtValidationAndDiscoveryFailures(t *testing.T) {
	root := t.TempDir()
	_, invalidProject := projectdiscovery.NewDiscoverer().Discover(t.Context(), root)
	if invalidProject == nil {
		t.Fatal("missing marker discovery error = nil")
	}
	resourceFailure := errors.New("filesystem resources unavailable")
	for _, test := range []struct {
		name             string
		request          control.RegisterProjectRequest
		discovery        *registrationDiscoverer
		want             error
		wantInvalidCode  bool
		wantUnclassified bool
	}{
		{name: "invalid request", request: control.RegisterProjectRequest{Path: "relative"}, discovery: &registrationDiscoverer{}},
		{name: "invalid project", request: control.RegisterProjectRequest{Path: root}, discovery: &registrationDiscoverer{err: invalidProject}, want: invalidProject, wantInvalidCode: true},
		{name: "resource failure", request: control.RegisterProjectRequest{Path: root}, discovery: &registrationDiscoverer{err: resourceFailure}, want: resourceFailure, wantUnclassified: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{}
			authority := newAuthorityWithRegistration(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, test.discovery, time.Now, func() (domain.ProjectID, error) { return "project-test", nil }, testProjectLifecycles(), testNetworkSetups(), testHTTPRoutes())
			_, err := authority.RegisterProject(t.Context(), control.Caller{}, test.request)
			if err == nil {
				t.Fatal("RegisterProject() error = nil")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("discovery error = %v, want %v", err, test.want)
			}
			if test.wantInvalidCode {
				var handlerError *session.HandlerError
				if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeInvalidRequest {
					t.Fatalf("discovery error = %#v, want invalid_request", err)
				}
			}
			if test.wantUnclassified {
				var handlerError *session.HandlerError
				if errors.As(err, &handlerError) {
					t.Fatalf("resource failure = %#v, must remain unclassified", err)
				}
			}
			if store.registrationCalls.Load() != 0 {
				t.Fatalf("registration calls = %d, want 0", store.registrationCalls.Load())
			}
		})
	}
}

// TestAuthorityRegisterProjectStopsAtIdentityGenerationFailure proves an entropy failure cannot reach durable registration.
func TestAuthorityRegisterProjectStopsAtIdentityGenerationFailure(t *testing.T) {
	root := t.TempDir()
	want := errors.New("entropy unavailable")
	store := &recordingStore{}
	authority := newAuthorityWithRegistration(
		store,
		testProjectUnregisterApprovals(),
		buildinfo.Info{Version: "dev"},
		&registrationDiscoverer{discovery: projectdiscovery.Discovery{Root: root, Name: "Orders", Slug: "orders"}},
		time.Now,
		func() (domain.ProjectID, error) { return "", want },
		testProjectLifecycles(),
		testNetworkSetups(),
		testHTTPRoutes(),
	)

	_, err := authority.RegisterProject(t.Context(), control.Caller{}, control.RegisterProjectRequest{Path: root})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "generate project identity") {
		t.Fatalf("registration error = %v, want wrapped identity generation failure", err)
	}
	if store.registrationCalls.Load() != 0 {
		t.Fatalf("registration calls = %d, want 0", store.registrationCalls.Load())
	}
}

// TestNewOpaqueProjectIDReturnsIndependentValidIdentities verifies production registration uses 128-bit random identifiers in the domain format.
func TestNewOpaqueProjectIDReturnsIndependentValidIdentities(t *testing.T) {
	first, err := newOpaqueProjectID()
	if err != nil {
		t.Fatalf("generate first project ID: %v", err)
	}
	second, err := newOpaqueProjectID()
	if err != nil {
		t.Fatalf("generate second project ID: %v", err)
	}
	for _, projectID := range []domain.ProjectID{first, second} {
		if err := projectID.Validate(); err != nil {
			t.Fatalf("generated project ID %q is invalid: %v", projectID, err)
		}
		if !strings.HasPrefix(string(projectID), "project-") || len(projectID) != 40 {
			t.Fatalf("generated project ID = %q, want project- plus 32 hexadecimal characters", projectID)
		}
	}
	if first == second {
		t.Fatalf("generated project IDs are equal: %q", first)
	}
}

// TestAuthorityRegisterProjectClassifiesStateConflict verifies authority preserves the typed RPC conflict around its state cause.
func TestAuthorityRegisterProjectClassifiesStateConflict(t *testing.T) {
	root := t.TempDir()
	discovery := projectdiscovery.Discovery{Root: root, Name: "Orders", Slug: "orders"}
	conflict := &state.ProjectRegistrationConflictError{Kind: state.ProjectRegistrationConflictSlug}
	store := &recordingStore{registrationErr: conflict}
	authority := newAuthorityWithRegistration(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, &registrationDiscoverer{discovery: discovery}, time.Now, func() (domain.ProjectID, error) { return "project-orders", nil }, testProjectLifecycles(), testNetworkSetups(), testHTTPRoutes())

	_, err := authority.RegisterProject(t.Context(), control.Caller{}, control.RegisterProjectRequest{Path: root})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict || !errors.Is(err, conflict) {
		t.Fatalf("registration error = %#v, want conflict wrapping state cause", err)
	}
}

// TestAuthorityRegisterProjectClassifiesActiveNetworkRelease verifies a retry blocked by teardown is a state conflict.
func TestAuthorityRegisterProjectClassifiesActiveNetworkRelease(t *testing.T) {
	root := t.TempDir()
	discovery := projectdiscovery.Discovery{Root: root, Name: "Orders", Slug: "orders"}
	active := &state.ProjectNetworkReleaseActiveError{
		ProjectID:   "project-orders",
		OperationID: "operation-release",
		State:       state.ProjectNetworkReleaseReleasing,
		Action:      "register project",
	}
	store := &recordingStore{registrationErr: active}
	authority := newAuthorityWithRegistration(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, &registrationDiscoverer{discovery: discovery}, time.Now, func() (domain.ProjectID, error) { return "project-orders", nil }, testProjectLifecycles(), testNetworkSetups(), testHTTPRoutes())

	_, err := authority.RegisterProject(t.Context(), control.Caller{}, control.RegisterProjectRequest{Path: root})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict || !errors.Is(err, active) {
		t.Fatalf("registration error = %#v, want conflict wrapping active network release", err)
	}
}
