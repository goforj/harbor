package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectruntime"
	"github.com/goforj/harbor/internal/state"
)

// continuityRuntime adds optional adoption without allowing recovery to launch or prepare a replacement.
type continuityRuntime struct {
	*projectLifecycleRecoverySupervisor
	handle      *genericRuntimeHandle
	adoptErr    error
	adoptions   []projectruntime.SessionAdoptionRequest
	detachErr   error
	detached    bool
	settlements []projectruntime.SessionAdoptionRequest
}

// AdoptSession returns only the configured exact prior handle for the restart boundary.
func (runtime *continuityRuntime) AdoptSession(_ context.Context, request projectruntime.SessionAdoptionRequest) (projectruntime.Handle, error) {
	runtime.adoptions = append(runtime.adoptions, request)
	if runtime.adoptErr != nil {
		return nil, runtime.adoptErr
	}
	return runtime.handle, nil
}

// DetachSession simulates releasing observation while leaving the original process live.
func (runtime *continuityRuntime) DetachSession(_ context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	if runtime.detachErr != nil {
		return runtime.detachErr
	}
	if runtime.handle.info.ProjectID != projectID || runtime.handle.info.SessionID != sessionID {
		return errors.New("wrong detach fence")
	}
	runtime.detached = true
	return nil
}

// SettleSession records complete-scope settlement for interrupted operations.
func (runtime *continuityRuntime) SettleSession(_ context.Context, request projectruntime.SessionAdoptionRequest) (projectruntime.PriorProcessSettlement, error) {
	runtime.settlements = append(runtime.settlements, request)
	return projectruntime.PriorProcessSettlement{Outcome: projectruntime.PriorProcessSettlementTerminated}, nil
}

// Stop completes the exact adopted handle through ordinary explicit project stop.
func (runtime *continuityRuntime) Stop(_ context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	if runtime.handle.info.ProjectID != projectID || runtime.handle.info.SessionID != sessionID {
		return errors.New("wrong stop fence")
	}
	runtime.handle.complete(projectruntime.Exit{StopRequested: true, ExitedAt: runtime.handle.info.StartedAt})
	return nil
}

// Close settles only sessions whose continuity boundary was not relinquished.
func (runtime *continuityRuntime) Close(ctx context.Context) error {
	if !runtime.detached && runtime.handle != nil {
		return runtime.Stop(ctx, runtime.handle.info.ProjectID, runtime.handle.info.SessionID)
	}
	return nil
}

// seedContinuitySession retains valid durable broker facts without impersonating a native process.
func seedContinuitySession(t *testing.T, store *state.Store, journal *state.OperationJournal, ready bool) (projectLifecycleRecoverySeed, *continuityRuntime) {
	t.Helper()
	broker := &domain.OutputBrokerSession{
		EndpointReference: filepath.Join(t.TempDir(), "broker.sock"), ManifestPath: filepath.Join(t.TempDir(), "broker.json"), CredentialDigest: strings.Repeat("b", 64),
		Process: domain.ProcessEvidence{PID: 4343, BirthToken: "broker-birth", ExecutableIdentity: filepath.Join(t.TempDir(), "broker"), ArgumentDigest: strings.Repeat("c", 64)},
	}
	seed := seedProjectLifecycleRecoveryStartWithOutputBroker(t, store, journal, true, broker)
	if ready {
		seed = completeProjectLifecycleRecoveryStart(t, store, seed)
	}
	runtime := &continuityRuntime{projectLifecycleRecoverySupervisor: &projectLifecycleRecoverySupervisor{}, handle: &genericRuntimeHandle{
		info: projectruntime.Info{ProjectID: seed.project.ID, SessionID: seed.session.ID, CheckoutRoot: seed.project.Path, StartedAt: seed.recoverAt.Add(time.Minute),
			Evidence: projectruntime.Evidence{PID: seed.evidence.PID, BirthToken: seed.evidence.BirthToken, ExecutableIdentity: seed.evidence.ExecutableIdentity, ArgumentDigest: seed.evidence.ArgumentDigest}}, done: make(chan struct{}),
	}}
	return seed, runtime
}

// TestProjectSessionContinuityPreservesReadySessionAndSupportsExplicitStop covers both shutdown continuity and actual user intent.
func TestProjectSessionContinuityPreservesReadySessionAndSupportsExplicitStop(t *testing.T) {
	for _, explicitStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "daemon shutdown", true: "explicit project stop"}[explicitStop], func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed, runtime := seedContinuitySession(t, store, journal, true)
			coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, store, runtime, netip.MustParseAddr("127.0.0.1"))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := coordinator.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatalf("repeated recovery: %v", err)
			}
			if err := coordinator.Resume(t.Context()); err != nil {
				t.Fatal(err)
			}
			current, err := store.ActiveProjectSession(t.Context(), seed.project.ID)
			if err != nil || !reflect.DeepEqual(current, seed.session) {
				t.Fatalf("recovery changed the durable session: %#v, %v", current, err)
			}
			if len(runtime.adoptions) != 1 || len(runtime.settled) != 0 || len(runtime.settlements) != 0 {
				t.Fatalf("recovery did not adopt exclusively: %#v", runtime)
			}
			if explicitStop {
				if _, err := coordinator.Stop(t.Context(), ProjectStopRequest{ProjectID: seed.project.ID, OperationID: "operation-adopted-stop", IntentID: "intent-adopted-stop"}); err != nil {
					t.Fatal(err)
				}
				waitForProjectLifecycleOperationState(t, journal, "intent-adopted-stop", domain.OperationSucceeded)
				waitForProjectLifecycleState(t, store, seed.project.ID, domain.ProjectStopped)
			} else {
				if err := coordinator.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				current, err := store.ActiveProjectSession(t.Context(), seed.project.ID)
				if err != nil || !reflect.DeepEqual(current, seed.session) {
					t.Fatalf("daemon shutdown retired the session: %#v, %v", current, err)
				}
				if !runtime.detached {
					t.Fatal("ready environment was not detached")
				}
				select {
				case <-runtime.handle.Done():
					t.Fatal("shutdown stopped the preserved process")
				default:
				}
			}
		})
	}
}

// TestProjectSessionContinuityRejectsLiveAmbiguousOwnership isolates the project without signaling an unproved process.
func TestProjectSessionContinuityRejectsLiveAmbiguousOwnership(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	seed, runtime := seedContinuitySession(t, store, journal, true)
	runtime.adoptErr = errors.New("broker identity changed")
	runtime.observation = projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessPresent}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, store, runtime, netip.MustParseAddr("127.0.0.1"))
	defer coordinator.Close(t.Context())
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	project, err := store.Project(t.Context(), seed.project.ID)
	if err != nil || project.Project.State != domain.ProjectUnavailable {
		t.Fatalf("ambiguous project = %#v, %v", project, err)
	}
	if len(runtime.settled) != 0 || len(runtime.settlements) != 0 {
		t.Fatal("ambiguous recovery attempted process settlement")
	}
}

// TestProjectSessionContinuitySettlesInterruptedStartScope keeps an incomplete launch retryable without adopting it as Ready.
func TestProjectSessionContinuitySettlesInterruptedStartScope(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	seed, runtime := seedContinuitySession(t, store, journal, false)
	coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, store, runtime, netip.MustParseAddr("127.0.0.1"))
	defer coordinator.Close(t.Context())
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.adoptions) != 0 || len(runtime.settlements) != 1 || runtime.settlements[0].Session.ID != seed.session.ID || len(runtime.settled) != 0 {
		t.Fatalf("interrupted start did not settle its complete session: %#v", runtime)
	}
	operation, err := journal.Operation(t.Context(), seed.operation.Operation.ID)
	if err != nil || operation.Operation.State != domain.OperationFailed {
		t.Fatalf("interrupted operation = %#v, %v", operation, err)
	}
}

// TestRecoveredRuntimePlanRequiresExactPrimaryResource keeps refreshed resource and native routes tied to the surviving lease.
func TestRecoveredRuntimePlanRequiresExactPrimaryResource(t *testing.T) {
	resource := domain.ResourceSnapshot{ID: "app-http", Name: "Application", Kind: "application", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.42:3000"}
	project := domain.ProjectSnapshot{ID: "project-recovered", Resources: []domain.ResourceSnapshot{resource}}
	leases := []identity.Lease{{Key: identity.LeaseKey{ProjectID: project.ID}, Address: netip.MustParseAddr("127.77.0.42")}}
	plan := runtimePlanForRecoveredResources(project, leases)
	if plan.NetworkAssignment.Address != leases[0].Address || plan.NetworkAssignment.PrimaryPort != 3000 || plan.Presentation.AppID != "app" || plan.Presentation.ResourceURL != resource.URL {
		t.Fatalf("lost recovered observation plan: %#v", plan)
	}
	for _, mutate := range []func(*domain.ResourceSnapshot){func(r *domain.ResourceSnapshot) { r.ID = "other" }, func(r *domain.ResourceSnapshot) { r.Owner.Kind = domain.ResourceOwnedByService }, func(r *domain.ResourceSnapshot) { r.URL = "http://[invalid" }, func(r *domain.ResourceSnapshot) { r.URL = "http://127.77.0.43:3000" }, func(r *domain.ResourceSnapshot) { r.URL = "http://127.77.0.42" }, func(r *domain.ResourceSnapshot) { r.URL = "http://127.77.0.42:0" }} {
		changed := resource
		mutate(&changed)
		project.Resources = []domain.ResourceSnapshot{changed}
		if actual := runtimePlanForRecoveredResources(project, leases); actual.NetworkAssignment.Address.IsValid() {
			t.Fatalf("unadmitted recovered resource: %#v", changed)
		}
	}
	project.Resources = []domain.ResourceSnapshot{resource}
	if actual := runtimePlanForRecoveredResources(project, nil); actual.NetworkAssignment.Address.IsValid() {
		t.Fatal("resource acquired address authority without a lease")
	}
}
