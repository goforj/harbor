package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectruntime"
)

// readinessAttemptFixture expires individual probes without terminating the supervised process.
type readinessAttemptFixture struct {
	calls     atomic.Int32
	budget    atomic.Int64
	always    bool
	invalid   bool
	lateReady bool
	endDaemon context.CancelFunc
}

// Probe distinguishes an ordinary malformed reply from a request that outlives its attempt budget.
func (probe *readinessAttemptFixture) Probe(ctx context.Context) (projectruntime.ReadinessState, error) {
	call := probe.calls.Add(1)
	if call == 1 {
		deadline, _ := ctx.Deadline()
		probe.budget.Store(int64(time.Until(deadline)))
	}
	if probe.invalid {
		return "", errors.New("wrong App identity")
	}
	if call == 1 || probe.always {
		<-ctx.Done()
		if probe.endDaemon != nil {
			probe.endDaemon()
		}
		if probe.lateReady {
			return projectruntime.ReadinessReady, nil
		}
		return "", ctx.Err()
	}
	return projectruntime.ReadinessReady, nil
}

// TestReadinessAttemptDeadlinesPreserveStartupPolicy separates probe patience from lifecycle validity.
func TestReadinessAttemptDeadlinesPreserveStartupPolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		always    bool
		invalid   bool
		lateReady bool
		endDaemon bool
		state     domain.ProjectState
		problem   domain.ProblemCode
	}{
		{name: "slow attempt then ready", state: domain.ProjectReady},
		{name: "overall deadline remains bounded", always: true, state: domain.ProjectFailed, problem: "project.readiness.timeout"},
		{name: "ready after deadline is rejected", lateReady: true, state: domain.ProjectFailed, problem: "project.readiness.timeout"},
		{name: "invalid reply still fails", invalid: true, state: domain.ProjectFailed, problem: "project.readiness.invalid"},
		{name: "daemon ending prevents retries", endDaemon: true, state: domain.ProjectFailed, problem: "project.readiness.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			root, port := newProjectLifecycleIntegrationCheckout(t)
			project := registerProjectLifecycleIntegrationProject(t, store, root)
			address := netip.MustParseAddr("127.0.0.1")
			initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, address)
			installProjectLifecycleIntegrationForj(t, port)
			supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 100 * time.Millisecond})
			coordinator, discoverer := newProjectLifecycleIntegrationCoordinator(t, store, journal, supervisor, address, uint16(port))
			probe := &readinessAttemptFixture{always: test.always, invalid: test.invalid, lateReady: test.lateReady}
			if test.endDaemon {
				probe.endDaemon = coordinator.cancel
			}
			discoverer.readiness = probe
			if test.always || test.lateReady {
				coordinator.startupTimeout = 50 * time.Millisecond
				coordinator.readinessInterval = time.Millisecond
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := coordinator.Close(ctx); err != nil {
					t.Errorf("close readiness coordinator: %v", err)
				}
			})
			if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
				ProjectID: project.ID, OperationID: "operation-attempt", IntentID: "intent-attempt",
			}); err != nil {
				t.Fatal(err)
			}
			waitForProjectLifecycleState(t, store, project.ID, test.state)
			if test.state == domain.ProjectReady {
				if probe.calls.Load() != 2 {
					t.Fatalf("readiness attempts = %d, want 2", probe.calls.Load())
				}
				return
			}
			operation := waitForProjectLifecycleOperationState(t, journal, "intent-attempt", domain.OperationFailed)
			if operation.Operation.Problem == nil || operation.Operation.Problem.Code != test.problem {
				t.Fatalf("readiness failure = %#v, want %s", operation.Operation.Problem, test.problem)
			}
			if test.always || test.lateReady {
				if probe.calls.Load() != 1 {
					t.Fatalf("exhausted startup budget retried readiness %d times", probe.calls.Load())
				}
				if budget := time.Duration(probe.budget.Load()); budget > coordinator.startupTimeout {
					t.Fatalf("probe budget = %s, exceeds startup budget %s", budget, coordinator.startupTimeout)
				}
			}
			if test.endDaemon && probe.calls.Load() != 1 {
				t.Fatalf("daemon shutdown retried readiness %d times", probe.calls.Load())
			}
		})
	}
}
