package reconcile

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// managedPublicationLifecycleState supplies only the durable reads used by the publication observer while leaving
// the rest of the lifecycle surface intentionally unavailable to this focused boundary test.
type managedPublicationLifecycleState struct {
	projectLifecycleState
	leaseState *primaryLeaseTestState
	session    domain.ProjectSession
}

// Project returns the registered project revision shared with the primary-lease fixture.
func (source *managedPublicationLifecycleState) Project(_ context.Context, _ domain.ProjectID) (state.ProjectRecord, error) {
	return source.leaseState.project, nil
}

// ActiveProjectSession returns the exact attached process fence used by the barrier.
func (source *managedPublicationLifecycleState) ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error) {
	return source.session, nil
}

// managedPublicationTestSupervisor supplies static service intent and one exact host-port observation.
type managedPublicationTestSupervisor struct {
	projectProcessSupervisor
	descriptor projectprocess.ProjectDescriptorObservation
	ports      projectprocess.ServicePortObservation
}

// ObserveProjectDescriptor returns the descriptor that the managed process authenticated against.
func (supervisor *managedPublicationTestSupervisor) ObserveProjectDescriptor(context.Context, string) (projectprocess.ProjectDescriptorObservation, error) {
	return supervisor.descriptor, nil
}

// ObserveServicePorts returns the selected service's current host publication.
func (supervisor *managedPublicationTestSupervisor) ObserveServicePorts(context.Context, domain.ProjectID, domain.SessionID, domain.ServiceID) (projectprocess.ServicePortObservation, error) {
	return supervisor.ports, nil
}

// TestObserveManagedPublicationsRepairsMissingServiceReservationInResolverStage proves DNS-stage authority is enough for native service endpoints.
func TestObserveManagedPublicationsRepairsMissingServiceReservationInResolverStage(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.project.Project.State = domain.ProjectStarting
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	fixture.state.network.Stage = state.NetworkStageResolver
	fixture.state.network.Reservations.Listeners = state.SharedListenerReservations{}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("resolver-stage fixture Validate() error = %v", err)
	}
	session := managedPublicationTestSession(fixture.state.project.Project.ID)
	stateSource := &managedPublicationLifecycleState{leaseState: fixture.state, session: session}
	descriptor := projectprocess.ProjectDescriptorObservation{
		ServiceRequirementsSupported: true,
		ServiceRequirements: []goforj.ServiceRequirement{{
			ID: "requirement.mysql.database", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerCompose,
			Lifecycle: goforj.ServiceRequirementLifecycleProject,
			Endpoints: []goforj.ServiceEndpoint{{
				ID: "requirement.mysql.database.endpoint.tcp", Protocol: goforj.ServiceEndpointProtocolTCP,
				NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost,
			}},
		}},
	}
	supervisor := &managedPublicationTestSupervisor{
		descriptor: descriptor,
		ports: projectprocess.ServicePortObservation{
			Supported: true, Available: true,
			Ports: []projectprocess.ServicePort{{Address: "127.0.0.1", Private: 3306, Public: 43106, Protocol: "tcp", Replica: 1}},
		},
	}
	coordinator := &ProjectLifecycleCoordinator{
		state:         stateSource,
		supervisor:    supervisor,
		primaryLeases: fixture.coordinator,
	}
	fence := managedPublicationTestFence(fixture.state.project.Project.ID, session)

	publications, err := coordinator.ObserveManagedPublicationsForPhase(t.Context(), fixture.state.project.Project.ID, session.ID, fence, true)
	if err != nil {
		t.Fatalf("ObserveManagedPublicationsForPhase() error = %v", err)
	}
	if len(publications) != 1 || publications[0].EndpointID != "service:requirement.mysql.database.endpoint.tcp" {
		t.Fatalf("publications = %#v, want one MySQL publication", publications)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("service reservation repair writes = %d, want 1", len(fixture.state.replaceCalls))
	}
	if _, found := endpointByID(fixture.state.network.Reservations.Endpoints, publications[0].EndpointID); !found {
		t.Fatalf("durable reservations = %#v, missing %q", fixture.state.network.Reservations.Endpoints, publications[0].EndpointID)
	}
}

// TestObserveManagedPublicationsRejectsIncompleteNetworkAuthority proves a pre-full barrier fails before publishing a misleading reservation.
func TestObserveManagedPublicationsRejectsIncompleteNetworkAuthority(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.12")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.project.Project.State = domain.ProjectStarting
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	fixture.state.network.Stage = state.NetworkStageIdentity
	session := managedPublicationTestSession(fixture.state.project.Project.ID)
	stateSource := &managedPublicationLifecycleState{leaseState: fixture.state, session: session}
	descriptor := projectprocess.ProjectDescriptorObservation{
		ServiceRequirementsSupported: true,
		ServiceRequirements: []goforj.ServiceRequirement{{
			ID: "requirement.mysql.database", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerCompose,
			Lifecycle: goforj.ServiceRequirementLifecycleProject,
			Endpoints: []goforj.ServiceEndpoint{{
				ID: "requirement.mysql.database.endpoint.tcp", Protocol: goforj.ServiceEndpointProtocolTCP,
				NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost,
			}},
		}},
	}
	supervisor := &managedPublicationTestSupervisor{
		descriptor: descriptor,
		ports: projectprocess.ServicePortObservation{
			Supported: true, Available: true,
			Ports: []projectprocess.ServicePort{{Address: "127.0.0.1", Private: 3306, Public: 43106, Protocol: "tcp", Replica: 1}},
		},
	}
	coordinator := &ProjectLifecycleCoordinator{state: stateSource, supervisor: supervisor, primaryLeases: fixture.coordinator}
	fence := managedPublicationTestFence(fixture.state.project.Project.ID, session)

	_, err := coordinator.ObserveManagedPublicationsForPhase(t.Context(), fixture.state.project.Project.ID, session.ID, fence, true)
	if err == nil {
		t.Fatal("ObserveManagedPublicationsForPhase() error = nil, want pre-full network authority error")
	}
	if !strings.Contains(err.Error(), "resolver stage is required") {
		t.Fatalf("ObserveManagedPublicationsForPhase() error = %v, want network-stage explanation", err)
	}
	if strings.Contains(err.Error(), "no exact durable reservation") {
		t.Fatalf("ObserveManagedPublicationsForPhase() error = %v, retained misleading reservation error", err)
	}
	if len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("pre-full service reservation writes = %d, want 0", len(fixture.state.replaceCalls))
	}
}

// managedPublicationTestSession creates a valid attached Harbor-owned process fence for observer tests.
func managedPublicationTestSession(projectID domain.ProjectID) domain.ProjectSession {
	at := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	return domain.ProjectSession{
		ID: "session-managed-publication", ProjectID: projectID, Owner: domain.SessionOwnerHarbor, State: domain.SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64), CredentialDigest: strings.Repeat("b", 64), Generation: 2,
		Process:   &domain.ProcessEvidence{PID: 4102, BirthToken: "birth-managed-publication", ExecutableIdentity: "/tmp/forj", ArgumentDigest: strings.Repeat("c", 64)},
		CreatedAt: at, UpdatedAt: at,
	}
}

// managedPublicationTestFence binds the observer request to the attached session.
func managedPublicationTestFence(projectID domain.ProjectID, session domain.ProjectSession) harbordruntime.ManagedPublicationFence {
	return harbordruntime.ManagedPublicationFence{ProjectID: projectID, SessionID: session.ID, SessionGeneration: session.Generation}
}

// endpointByID finds one project endpoint without relying on its durable ordering.
func endpointByID(endpoints []state.EndpointReservation, endpointID string) (state.EndpointReservation, bool) {
	for _, endpoint := range endpoints {
		if endpoint.Key.EndpointID == endpointID {
			return endpoint, true
		}
	}
	return state.EndpointReservation{}, false
}
