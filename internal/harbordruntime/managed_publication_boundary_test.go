package harbordruntime

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// TestPlanVerifiedManagedNativeRoutesUsesHarborOwnedReservations proves a ready attached session can plan only its durable endpoints.
func TestPlanVerifiedManagedNativeRoutesUsesHarborOwnedReservations(t *testing.T) {
	source, request := managedPublicationBoundaryFixture()

	routes, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request)
	if err != nil {
		t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v", err)
	}
	want := []dataplane.NativeRoute{{
		ID:       "orders:service:mysql",
		Host:     "mysql.orders.test",
		Listen:   netip.MustParseAddrPort("127.77.0.10:3306"),
		Upstream: netip.MustParseAddrPort("127.0.0.1:43106"),
	}}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
	if source.runtimeCalls != 2 || source.sessionCalls != 2 {
		t.Fatalf("durable reads = runtime %d/session %d, want two of each", source.runtimeCalls, source.sessionCalls)
	}
}

// TestPlanVerifiedManagedNativeRoutesRejectsCallerSuppliedTopology proves an observed endpoint cannot smuggle an unreserved route.
func TestPlanVerifiedManagedNativeRoutesRejectsCallerSuppliedTopology(t *testing.T) {
	source, request := managedPublicationBoundaryFixture()
	request.Publications[0].EndpointID = "service:unowned"

	if _, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request); err == nil || !strings.Contains(err.Error(), "no durable reservation") {
		t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v, want unowned reservation rejection", err)
	}
}

// TestPlanVerifiedManagedNativeRoutesRequiresFullNetworkOwnership keeps identity and resolver setup from becoming native publication authority.
func TestPlanVerifiedManagedNativeRoutesRequiresFullNetworkOwnership(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*state.RuntimeState)
	}{
		{
			name: "uninitialized",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Network = validControllerUninitializedNetwork()
				runtimeState.NetworkInitialized = false
				runtimeState.Snapshot.Projects[0].State = domain.ProjectStopped
			},
		},
		{
			name: "identity stage",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Snapshot.Projects[0].State = domain.ProjectStopped
				runtimeState.Network.Stage = state.NetworkStageIdentity
				runtimeState.Network.Reservations = state.DataPlaneReservations{
					Endpoints:            []state.EndpointReservation{},
					SuppressedProjectIDs: []domain.ProjectID{},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, request := managedPublicationBoundaryFixture()
			test.mutate(&source.runtimeValues[0])
			source.runtimeValues[1] = source.runtimeValues[0]

			if _, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request); err == nil || !strings.Contains(err.Error(), "full Harbor network ownership") {
				t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v, want full-network rejection", err)
			}
		})
	}
}

// TestPlanVerifiedManagedNativeRoutesRequiresReadyProject keeps stopping and transitional projects from publishing native routes.
func TestPlanVerifiedManagedNativeRoutesRequiresReadyProject(t *testing.T) {
	source, request := managedPublicationBoundaryFixture()
	source.runtimeValues[0].Snapshot.Projects[0].State = domain.ProjectStopped
	source.runtimeValues[1] = source.runtimeValues[0]

	if _, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v, want project readiness rejection", err)
	}
}

// TestPlanVerifiedManagedNativeRoutesRejectsSessionFenceFailures keeps missing, replaced, and unattached sessions out of publication authority.
func TestPlanVerifiedManagedNativeRoutesRejectsSessionFenceFailures(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*domain.ProjectSession)
		sessionErr error
		want       string
		wantTyped  func(error) bool
	}{
		{
			name:       "missing",
			sessionErr: &state.ProjectSessionNotFoundError{ProjectID: "orders", SessionID: "session-orders"},
			wantTyped: func(err error) bool {
				var missing *state.ProjectSessionNotFoundError
				return errors.As(err, &missing)
			},
		},
		{
			name: "different session",
			mutate: func(session *domain.ProjectSession) {
				session.ID = "session-other"
			},
			wantTyped: func(err error) bool {
				var missing *state.ProjectSessionNotFoundError
				return errors.As(err, &missing)
			},
		},
		{
			name: "different project",
			mutate: func(session *domain.ProjectSession) {
				session.ProjectID = "other"
			},
			want: "belongs to project",
		},
		{
			name: "stale generation",
			mutate: func(session *domain.ProjectSession) {
				session.Generation++
			},
			wantTyped: func(err error) bool {
				var stale *state.StaleSessionGenerationError
				return errors.As(err, &stale)
			},
		},
		{
			name: "awaiting attach",
			mutate: func(session *domain.ProjectSession) {
				session.State = domain.SessionAwaitingAttach
			},
			want: "not attached",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, request := managedPublicationBoundaryFixture()
			if test.mutate != nil {
				test.mutate(&source.sessionValues[0])
			}
			source.sessionValues[1] = source.sessionValues[0]
			source.sessionErr = test.sessionErr

			err := func() error {
				_, planErr := PlanVerifiedManagedNativeRoutes(t.Context(), source, request)
				return planErr
			}()
			if err == nil {
				t.Fatal("PlanVerifiedManagedNativeRoutes() error = nil")
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if test.wantTyped != nil && !test.wantTyped(err) {
				t.Fatalf("error = %v, want typed fence failure", err)
			}
		})
	}
}

// TestPlanVerifiedManagedNativeRoutesRejectsDurableAuthorityDrift proves a pure plan cannot cross a reservation update.
func TestPlanVerifiedManagedNativeRoutesRejectsDurableAuthorityDrift(t *testing.T) {
	source, request := managedPublicationBoundaryFixture()
	source.runtimeValues[1].Network.Reservations.Endpoints[0].Host = "mysql-replaced.orders.test"

	if _, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request); err == nil || !errors.Is(err, managedPublicationAuthorityChanged) {
		t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v, want durable authority drift", err)
	}
}

// TestPlanVerifiedManagedNativeRoutesRejectsSessionAuthorityDrift proves a replacement session cannot inherit an old observation.
func TestPlanVerifiedManagedNativeRoutesRejectsSessionAuthorityDrift(t *testing.T) {
	source, request := managedPublicationBoundaryFixture()
	source.sessionValues[1].Generation++

	if _, err := PlanVerifiedManagedNativeRoutes(t.Context(), source, request); err == nil || !errors.Is(err, managedPublicationAuthorityChanged) {
		t.Fatalf("PlanVerifiedManagedNativeRoutes() error = %v, want session authority drift", err)
	}
}

// managedPublicationBoundaryFixture builds a valid full-network aggregate and one attached session for boundary tests.
func managedPublicationBoundaryFixture() (*managedPublicationBoundaryTestSource, ManagedNativeRoutePlanRequest) {
	runtimeState := initializedControllerRuntimeState()
	runtimeState.Snapshot.Projects[0].State = domain.ProjectReady
	runtimeState.Network.Reservations.Endpoints = []state.EndpointReservation{
		managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
	}
	secondRuntimeState := runtimeState
	secondRuntimeState.Network.Reservations.Endpoints = []state.EndpointReservation{
		cloneManagedPublicationReservation(runtimeState.Network.Reservations.Endpoints[0]),
	}
	session := managedPublicationBoundarySession()
	return &managedPublicationBoundaryTestSource{
			runtimeValues: []state.RuntimeState{runtimeState, secondRuntimeState},
			sessionValues: []domain.ProjectSession{session, session},
		}, ManagedNativeRoutePlanRequest{
			Fence: managedPublicationTestFence(),
			Publications: []ManagedEndpointPublication{
				managedEndpointPublication(managedPublicationTestFence(), "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
			},
		}
}

// managedPublicationBoundarySession creates process evidence that satisfies the attached-session contract.
func managedPublicationBoundarySession() domain.ProjectSession {
	createdAt := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	return domain.ProjectSession{
		ID:               "session-orders",
		ProjectID:        "orders",
		Owner:            domain.SessionOwnerHarbor,
		State:            domain.SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64),
		CredentialDigest: strings.Repeat("b", 64),
		Generation:       7,
		Process: &domain.ProcessEvidence{
			PID:                1,
			BirthToken:         "birth",
			ExecutableIdentity: "/usr/bin/forj",
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

// managedPublicationBoundaryTestSource supplies deterministic durable snapshots to boundary tests.
type managedPublicationBoundaryTestSource struct {
	runtimeValues []state.RuntimeState
	sessionValues []domain.ProjectSession
	runtimeErr    error
	sessionErr    error
	runtimeCalls  int
	sessionCalls  int
}

// RuntimeState returns the next test runtime snapshot, retaining the final value for revalidation reads.
func (source *managedPublicationBoundaryTestSource) RuntimeState(context.Context) (state.RuntimeState, error) {
	source.runtimeCalls++
	if source.runtimeErr != nil {
		return state.RuntimeState{}, source.runtimeErr
	}
	return source.runtimeValues[min(source.runtimeCalls-1, len(source.runtimeValues)-1)], nil
}

// ActiveProjectSession returns the next test session, retaining the final value for revalidation reads.
func (source *managedPublicationBoundaryTestSource) ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error) {
	source.sessionCalls++
	if source.sessionErr != nil {
		return domain.ProjectSession{}, source.sessionErr
	}
	return source.sessionValues[min(source.sessionCalls-1, len(source.sessionValues)-1)], nil
}
