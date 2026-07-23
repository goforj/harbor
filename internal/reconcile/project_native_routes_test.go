package reconcile

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/projectprocess"
)

// projectNativeRouteTestRuntime supplies exact per-service Docker port observations.
type projectNativeRouteTestRuntime struct {
	observations map[domain.ServiceID]projectprocess.ServicePortObservation
}

// ObserveServicePorts returns the configured observation for one logical service.
func (runtime *projectNativeRouteTestRuntime) ObserveServicePorts(
	_ context.Context,
	_ domain.ProjectID,
	_ domain.SessionID,
	serviceID domain.ServiceID,
) (projectprocess.ServicePortObservation, error) {
	return runtime.observations[serviceID], nil
}

// projectNativeRouteTestReconciler captures one project-scoped route replacement.
type projectNativeRouteTestReconciler struct {
	projectID domain.ProjectID
	routes    []dataplane.NativeRoute
}

// Reconcile accepts the durable HTTP route edge outside this focused native fixture.
func (*projectNativeRouteTestReconciler) Reconcile(context.Context) error {
	return nil
}

// ReconcileProjectNativeRoutes captures the exact native replacement.
func (reconciler *projectNativeRouteTestReconciler) ReconcileProjectNativeRoutes(
	_ context.Context,
	projectID domain.ProjectID,
	routes []dataplane.NativeRoute,
) error {
	reconciler.projectID = projectID
	reconciler.routes = append([]dataplane.NativeRoute(nil), routes...)
	return nil
}

// TestReconcileObservedNativeServiceRoutesPublishesMySQLOnTheProjectAddress proves Docker observation is sufficient for native DNS.
func TestReconcileObservedNativeServiceRoutesPublishesMySQLOnTheProjectAddress(t *testing.T) {
	project := projectNativeRouteTestProject()
	session := projectNativeRouteTestSession(project.ID)
	primary := netip.MustParseAddr("127.77.59.75")
	runtime := &projectNativeRouteTestRuntime{observations: map[domain.ServiceID]projectprocess.ServicePortObservation{
		"mysql": {
			Supported: true,
			Available: true,
			Ports: []projectprocess.ServicePort{{
				Address:  primary.String(),
				Private:  3306,
				Public:   3306,
				Protocol: "tcp",
				Replica:  1,
			}},
		},
	}}
	routes := new(projectNativeRouteTestReconciler)
	coordinator := &ProjectLifecycleCoordinator{
		runtimeCapabilities: runtime,
		routes:              routes,
	}

	if err := coordinator.reconcileObservedNativeServiceRoutes(
		t.Context(),
		project,
		session,
		primary,
		project.Services,
		project.Resources,
	); err != nil {
		t.Fatalf("reconcileObservedNativeServiceRoutes() error = %v", err)
	}
	want := dataplane.NativeRoute{
		ID:       string(project.ID) + ":service:mysql",
		Host:     "mysql.test.test",
		Listen:   netip.MustParseAddrPort("127.77.59.75:3306"),
		Upstream: netip.MustParseAddrPort("127.77.59.75:3306"),
		Direct:   true,
	}
	if routes.projectID != project.ID || len(routes.routes) != 1 || routes.routes[0] != want {
		t.Fatalf("native route replacement = %q %#v, want %q %#v", routes.projectID, routes.routes, project.ID, []dataplane.NativeRoute{want})
	}
}

// TestReconcileObservedNativeServiceRoutesLeavesHTTPServiceNamesOnSharedIngress prevents conflicting Mailpit DNS authority.
func TestReconcileObservedNativeServiceRoutesLeavesHTTPServiceNamesOnSharedIngress(t *testing.T) {
	project := projectNativeRouteTestProject()
	project.Services = append(project.Services, domain.ServiceSnapshot{
		ID:        "mailpit",
		Name:      "Mailpit",
		Kind:      "compose",
		State:     domain.EntityReady,
		Owner:     domain.ServiceOwnerCompose,
		Selection: domain.ServiceSelected,
	})
	project.Resources = append(project.Resources, domain.ResourceSnapshot{
		ID:   "mailpit",
		Name: "Mailpit",
		Kind: "mail",
		Owner: domain.ResourceOwner{
			Kind:      domain.ResourceOwnedByService,
			ServiceID: "mailpit",
		},
		URL: "http://127.77.59.75:8025",
	})
	session := projectNativeRouteTestSession(project.ID)
	runtime := &projectNativeRouteTestRuntime{observations: map[domain.ServiceID]projectprocess.ServicePortObservation{
		"mailpit": {
			Supported: true,
			Available: true,
			Ports: []projectprocess.ServicePort{{
				Address:  "127.77.59.75",
				Private:  8025,
				Public:   8025,
				Protocol: "tcp",
				Replica:  1,
			}},
		},
	}}
	routes := new(projectNativeRouteTestReconciler)
	coordinator := &ProjectLifecycleCoordinator{
		runtimeCapabilities: runtime,
		routes:              routes,
	}

	if err := coordinator.reconcileObservedNativeServiceRoutes(
		t.Context(),
		project,
		session,
		netip.MustParseAddr("127.77.59.75"),
		project.Services,
		project.Resources,
	); err != nil {
		t.Fatalf("reconcileObservedNativeServiceRoutes() error = %v", err)
	}
	if len(routes.routes) != 0 {
		t.Fatalf("native routes = %#v, want Mailpit retained on HTTP ingress", routes.routes)
	}
}

// projectNativeRouteTestProject returns one ready project with App HTTP and MySQL service facts.
func projectNativeRouteTestProject() domain.ProjectSnapshot {
	at := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	return domain.ProjectSnapshot{
		ID:        "project-test",
		Name:      "test",
		Path:      "/workspace/test",
		Slug:      "test",
		State:     domain.ProjectReady,
		UpdatedAt: at,
		Apps: []domain.AppSnapshot{{
			ID:       "app",
			Name:     "App",
			State:    domain.EntityReady,
			Active:   true,
			Required: true,
		}},
		Services: []domain.ServiceSnapshot{{
			ID:        "mysql",
			Name:      "MySQL",
			Kind:      "compose",
			State:     domain.EntityReady,
			Owner:     domain.ServiceOwnerCompose,
			Selection: domain.ServiceSelected,
		}},
		Resources: []domain.ResourceSnapshot{{
			ID:   "app-http",
			Name: "App",
			Kind: "application",
			Owner: domain.ResourceOwner{
				Kind:  domain.ResourceOwnedByApp,
				AppID: "app",
			},
			URL: "http://127.77.59.75:3000",
		}},
	}
}

// projectNativeRouteTestSession returns one valid attached process fence.
func projectNativeRouteTestSession(projectID domain.ProjectID) domain.ProjectSession {
	at := time.Date(2026, time.July, 23, 18, 0, 0, 0, time.UTC)
	return domain.ProjectSession{
		ID:               "session-test",
		ProjectID:        projectID,
		Owner:            domain.SessionOwnerHarbor,
		State:            domain.SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64),
		CredentialDigest: strings.Repeat("b", 64),
		Generation:       1,
		Process: &domain.ProcessEvidence{
			PID:                100,
			BirthToken:         "birth-test",
			ExecutableIdentity: "/tmp/forj",
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: at,
		UpdatedAt: at,
	}
}
