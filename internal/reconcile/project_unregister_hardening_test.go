package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

// TestProjectUnregisterRequestValidationCoversIdentifierAndRevisionBounds verifies malformed selections fail before authority access.
func TestProjectUnregisterRequestValidationCoversIdentifierAndRevisionBounds(t *testing.T) {
	prepareTests := []struct {
		name    string
		request PrepareRequest
	}{
		{name: "operation", request: PrepareRequest{ExpectedOperationRevision: 1, RequesterIdentity: "501"}},
		{name: "zero revision", request: PrepareRequest{OperationID: "operation-1", RequesterIdentity: "501"}},
		{name: "large revision", request: PrepareRequest{OperationID: "operation-1", ExpectedOperationRevision: domain.MaximumSequence + 1, RequesterIdentity: "501"}},
		{name: "empty requester", request: PrepareRequest{OperationID: "operation-1", ExpectedOperationRevision: 1}},
		{name: "large requester", request: PrepareRequest{OperationID: "operation-1", ExpectedOperationRevision: 1, RequesterIdentity: strings.Repeat("a", helper.MaximumRequesterIdentityLength+1)}},
	}
	for _, test := range prepareTests {
		t.Run("prepare "+test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil {
				t.Fatal("expected PrepareRequest validation error")
			}
		})
	}

	confirmTests := []struct {
		name    string
		request ConfirmRequest
	}{
		{name: "operation", request: ConfirmRequest{ExpectedOperationRevision: 1}},
		{name: "zero revision", request: ConfirmRequest{OperationID: "operation-1"}},
		{name: "large revision", request: ConfirmRequest{OperationID: "operation-1", ExpectedOperationRevision: domain.MaximumSequence + 1}},
	}
	for _, test := range confirmTests {
		t.Run("confirm "+test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil {
				t.Fatal("expected ConfirmRequest validation error")
			}
		})
	}

	incomplete := (&ReleaseIncompleteError{OperationID: "operation-1", Remaining: 2}).Error()
	if !strings.Contains(incomplete, "operation-1") || !strings.Contains(incomplete, "2") {
		t.Fatalf("ReleaseIncompleteError = %q", incomplete)
	}
}

// TestProjectUnregisterCoordinatorRejectsNilDependencies verifies every authority fails fast, including typed nils.
func TestProjectUnregisterCoordinatorRejectsNilDependencies(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	var nilState *projectUnregisterTestState
	var nilPlans *projectUnregisterTestPlans
	var nilObserver *projectUnregisterTestObserver
	var nilWithdrawal *projectUnregisterTestWithdrawal
	var nilFactory IssuerFactory
	var nilClock *projectUnregisterTestClock
	tests := []struct {
		name  string
		build func()
	}{
		{name: "state", build: func() {
			NewProjectUnregisterCoordinator(nilState, fixture.state, fixture.plans, fixture.observer, fixture.withdrawal, fixture.issuers.Open, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "operations", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, nilState, fixture.plans, fixture.observer, fixture.withdrawal, fixture.issuers.Open, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "plans", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, fixture.state, nilPlans, fixture.observer, fixture.withdrawal, fixture.issuers.Open, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "observer", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, fixture.state, fixture.plans, nilObserver, fixture.withdrawal, fixture.issuers.Open, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "withdrawal", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, fixture.state, fixture.plans, fixture.observer, nilWithdrawal, fixture.issuers.Open, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "issuer factory", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, fixture.state, fixture.plans, fixture.observer, fixture.withdrawal, nilFactory, projectUnregisterTestClock{now: fixture.now})
		}},
		{name: "clock", build: func() {
			NewProjectUnregisterCoordinator(fixture.state, fixture.state, fixture.plans, fixture.observer, fixture.withdrawal, fixture.issuers.Open, nilClock)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor panic")
				}
			}()
			test.build()
		})
	}
}

// TestProjectUnregisterPlanValidationRejectsEveryAuthorityMismatch verifies complete durable plan sets are exact and homogeneous.
func TestProjectUnregisterPlanValidationRejectsEveryAuthorityMismatch(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *projectUnregisterFixture)
		want      string
	}{
		{name: "enumeration error", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			fixture.plans.requestsError = errors.New("enumeration failed")
		}, want: "enumeration failed"},
		{name: "empty set", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			fixture.plans.requests = nil
		}, want: "plan set is empty"},
		{name: "foreign operation request", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			fixture.plans.requests[0].OperationID = "operation-other"
		}, want: "belongs to another operation"},
		{name: "duplicate lease key", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			fixture.plans.requests = append(fixture.plans.requests, fixture.plans.requests[0])
		}, want: "duplicate lease key"},
		{name: "resolution error", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			fixture.plans.resolveError = errors.New("resolution failed")
		}, want: "resolution failed"},
		{name: "invalid plan", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			plan := fixture.plans.plans[fixture.leases[0].Key]
			plan.Requirements = nil
			fixture.plans.plans[fixture.leases[0].Key] = plan
		}, want: "invalid approval plan"},
		{name: "plan differs from request", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			plan := fixture.plans.plans[fixture.leases[0].Key]
			plan.OperationID = "operation-other"
			fixture.plans.plans[fixture.leases[0].Key] = plan
		}, want: "differs from its request"},
		{name: "not an active release", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			plan := fixture.plans.plans[fixture.leases[0].Key]
			plan.Mutation = helper.OperationEnsureLoopbackIdentity
			fixture.plans.plans[fixture.leases[0].Key] = plan
		}, want: "not an active loopback release"},
		{name: "multiple projects", configure: configureMultipleProjectPlan, want: "spans multiple projects"},
		{name: "duplicate address", configure: func(_ *testing.T, fixture *projectUnregisterFixture) {
			plan := fixture.plans.plans[fixture.leases[1].Key]
			plan.Lease.Address = fixture.leases[0].Address
			fixture.plans.plans[fixture.leases[1].Key] = plan
		}, want: "duplicate address"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			test.configure(t, fixture)
			_, err := fixture.coordinator.Prepare(context.Background(), projectUnregisterPrepareRequest(fixture))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want %q", err, test.want)
			}
			if calls, _ := fixture.observer.callSnapshot(); len(calls) != 0 {
				t.Fatalf("invalid plans reached host observation: %v", calls)
			}
			if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
				t.Fatalf("invalid plans opened %d issuers", openCalls)
			}
		})
	}
}

// TestProjectUnregisterRecoverValidatesRequiresApprovalPlans verifies restart recovery detects corrupt consent authority without prompting.
func TestProjectUnregisterRecoverValidatesRequiresApprovalPlans(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.plans.requests = nil
	err := fixture.coordinator.Recover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "validate approval plans") || !strings.Contains(err.Error(), "plan set is empty") {
		t.Fatalf("Recover() error = %v", err)
	}
	if calls, _ := fixture.observer.callSnapshot(); len(calls) != 0 {
		t.Fatalf("approval validation observed host state: %v", calls)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("approval validation opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterPrepareHandlesIssuerFailures verifies lazy machine-global authority closes and fails safely.
func TestProjectUnregisterPrepareHandlesIssuerFailures(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture)
		want      string
		open      int
		issue     int
		close     int
	}{
		{name: "open", configure: func(fixture *projectUnregisterFixture) {
			fixture.issuers.openError = errors.New("open failed")
		}, want: "open failed", open: 1},
		{name: "nil issuer", configure: func(fixture *projectUnregisterFixture) {
			fixture.coordinator.issuers = func() (TicketIssuer, error) { return nil, nil }
		}, want: "factory returned no issuer"},
		{name: "typed nil issuer", configure: func(fixture *projectUnregisterFixture) {
			fixture.coordinator.issuers = func() (TicketIssuer, error) {
				var issuer *projectUnregisterTestIssuer
				return issuer, nil
			}
		}, want: "factory returned no issuer"},
		{name: "issue", configure: func(fixture *projectUnregisterFixture) {
			fixture.issuers.issueError = errors.New("issue failed")
		}, want: "issue failed", open: 1, issue: 1, close: 1},
		{name: "close", configure: func(fixture *projectUnregisterFixture) {
			fixture.issuers.closeError = errors.New("close failed")
		}, want: "close failed", open: 1, issue: 1, close: 1},
		{name: "invalid result", configure: func(fixture *projectUnregisterFixture) {
			result := ticketissuer.Result{}
			fixture.issuers.resultOverride = &result
		}, want: "result is invalid", open: 1, issue: 1, close: 1},
		{name: "mismatched result", configure: func(fixture *projectUnregisterFixture) {
			result := projectUnregisterIssuerResult(fixture, 0)
			result.OperationID = "operation-other"
			fixture.issuers.resultOverride = &result
		}, want: "differs from requested release", open: 1, issue: 1, close: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			test.configure(fixture)
			_, err := fixture.coordinator.Prepare(context.Background(), projectUnregisterPrepareRequest(fixture))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want %q", err, test.want)
			}
			openCalls, issueCalls, closeCalls := fixture.issuers.snapshot()
			if openCalls != test.open || len(issueCalls) != test.issue || closeCalls != test.close {
				t.Fatalf("issuer calls = open %d, issue %d, close %d", openCalls, len(issueCalls), closeCalls)
			}
		})
	}

	if !nilTicketIssuer(nil) {
		t.Fatal("nil issuer was accepted")
	}
	var typedNil *projectUnregisterTestIssuer
	if !nilTicketIssuer(typedNil) {
		t.Fatal("typed nil issuer was accepted")
	}
	if nilTicketIssuer(projectUnregisterValueIssuer{}) {
		t.Fatal("non-nil value issuer was rejected")
	}
}

// TestProjectUnregisterObserverRejectsErrorsAndEveryConflictState verifies native facts fail closed before issuing authority.
func TestProjectUnregisterObserverRejectsErrorsAndEveryConflictState(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture, netip.Addr)
		state     loopback.State
		want      string
	}{
		{name: "observer error", configure: func(fixture *projectUnregisterFixture, address netip.Addr) {
			fixture.observer.errors[address] = errors.New("observe failed")
		}, want: "observe failed"},
		{name: "different address", configure: func(fixture *projectUnregisterFixture, address netip.Addr) {
			fixture.observer.facts[address] = projectUnregisterAbsentObservation(netip.MustParseAddr("127.77.0.99"))
		}, want: "address differs"},
		{name: "invalid fingerprint", configure: func(fixture *projectUnregisterFixture, address netip.Addr) {
			observation := projectUnregisterExactObservation(address)
			observation.Loopback.NativeLoopback = false
			fixture.observer.facts[address] = observation
		}, want: "validate loopback observation"},
		{name: "foreign", configure: configureObserverConflict(loopback.StateForeign), state: loopback.StateForeign},
		{name: "non host prefix", configure: configureObserverConflict(loopback.StateNonHostPrefix), state: loopback.StateNonHostPrefix},
		{name: "attribute conflict", configure: configureObserverConflict(loopback.StateAttributeConflict), state: loopback.StateAttributeConflict},
		{name: "ambiguous", configure: configureObserverConflict(loopback.StateAmbiguous), state: loopback.StateAmbiguous},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			address := fixture.leases[0].Address
			test.configure(fixture, address)
			_, err := fixture.coordinator.Prepare(context.Background(), projectUnregisterPrepareRequest(fixture))
			if test.state != "" {
				var conflict *HostStateConflictError
				if !errors.As(err, &conflict) || conflict.State != test.state || conflict.Address != address {
					t.Fatalf("Prepare() error = %v, want %q conflict", err, test.state)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want %q", err, test.want)
			}
			if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
				t.Fatalf("invalid host facts opened %d issuers", openCalls)
			}
		})
	}
}

// TestProjectUnregisterPublicMethodsHonorCancellationAndNilContexts verifies cancellation is checked before durable authority.
func TestProjectUnregisterPublicMethodsHonorCancellationAndNilContexts(t *testing.T) {
	tests := []struct {
		name string
		call func(*projectUnregisterFixture, context.Context) error
	}{
		{name: "prepare", call: func(fixture *projectUnregisterFixture, ctx context.Context) error {
			_, err := fixture.coordinator.Prepare(ctx, projectUnregisterPrepareRequest(fixture))
			return err
		}},
		{name: "confirm", call: func(fixture *projectUnregisterFixture, ctx context.Context) error {
			_, err := fixture.coordinator.Confirm(ctx, ConfirmRequest{OperationID: fixture.operationID, ExpectedOperationRevision: fixture.revision})
			return err
		}},
		{name: "recover", call: func(fixture *projectUnregisterFixture, ctx context.Context) error {
			return fixture.coordinator.Recover(ctx)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := test.call(fixture, ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("call error = %v, want context canceled", err)
			}
			if snapshot := fixture.state.snapshot(); snapshot.runtimeCalls != 0 || snapshot.activeCalls != 0 || snapshot.releaseCalls != 0 {
				t.Fatalf("cancelled call reached durable state = %#v", snapshot)
			}
		})
	}

	fixture := newProjectUnregisterFixture(t)
	fixture.setAllObservations(loopback.StateAbsent)
	if _, err := fixture.coordinator.Prepare(nil, projectUnregisterPrepareRequest(fixture)); err != nil {
		t.Fatalf("Prepare(nil) error = %v", err)
	}
}

// TestProjectUnregisterPrepareRechecksWithdrawalBeforeIssuing verifies route republishing closes the narrowed issue window.
func TestProjectUnregisterPrepareRechecksWithdrawalBeforeIssuing(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.withdrawal.err = errors.New("route returned")
	fixture.withdrawal.failAt = 2
	_, err := fixture.coordinator.Prepare(context.Background(), projectUnregisterPrepareRequest(fixture))
	if err == nil || !strings.Contains(err.Error(), "route returned") {
		t.Fatalf("Prepare() error = %v", err)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("second withdrawal failure opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterWithdrawalRequiresInitializedReadableState verifies the live gate is bound to durable network authority.
func TestProjectUnregisterWithdrawalRequiresInitializedReadableState(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture)
		want      string
	}{
		{name: "read error", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.runtimeError = errors.New("runtime failed")
		}, want: "runtime failed"},
		{name: "not initialized", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.runtime.NetworkInitialized = false
		}, want: "not initialized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			test.configure(fixture)
			_, err := fixture.coordinator.Prepare(context.Background(), projectUnregisterPrepareRequest(fixture))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterConfirmFailureBoundariesKeepApprovalDurable verifies confirmation fails before or at exact transition boundaries.
func TestProjectUnregisterConfirmFailureBoundariesKeepApprovalDurable(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture)
		request   func(*projectUnregisterFixture) ConfirmRequest
		want      string
	}{
		{name: "invalid request", request: func(*projectUnregisterFixture) ConfirmRequest { return ConfirmRequest{} }, want: "operation ID"},
		{name: "plan resolution", configure: func(fixture *projectUnregisterFixture) {
			fixture.plans.requests = nil
		}, want: "plan set is empty"},
		{name: "first withdrawal", configure: func(fixture *projectUnregisterFixture) {
			fixture.withdrawal.err = errors.New("first withdrawal failed")
		}, want: "first withdrawal failed"},
		{name: "observation", configure: func(fixture *projectUnregisterFixture) {
			fixture.observer.errors[fixture.leases[0].Address] = errors.New("confirmation observation failed")
		}, want: "confirmation observation failed"},
		{name: "second withdrawal", configure: func(fixture *projectUnregisterFixture) {
			fixture.setAllObservations(loopback.StateAbsent)
			fixture.withdrawal.err = errors.New("second withdrawal failed")
			fixture.withdrawal.failAt = 2
		}, want: "second withdrawal failed"},
		{name: "resume", configure: func(fixture *projectUnregisterFixture) {
			fixture.setAllObservations(loopback.StateAbsent)
			fixture.state.resumeError = errors.New("resume failed")
		}, want: "resume failed"},
		{name: "synchronous finish", configure: func(fixture *projectUnregisterFixture) {
			fixture.setAllObservations(loopback.StateAbsent)
			fixture.state.releaseError = errors.New("finish release read failed")
		}, want: "finish release read failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			if test.configure != nil {
				test.configure(fixture)
			}
			request := ConfirmRequest{OperationID: fixture.operationID, ExpectedOperationRevision: fixture.revision}
			if test.request != nil {
				request = test.request(fixture)
			}
			_, err := fixture.coordinator.Confirm(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Confirm() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterRecoverHandlesEveryRestartFailureBoundary verifies recovery aggregates errors without opening helper authority.
func TestProjectUnregisterRecoverHandlesEveryRestartFailureBoundary(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture)
		want      string
	}{
		{name: "active operation read", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.activeError = errors.New("active operations failed")
		}, want: "active operations failed"},
		{name: "unsupported active state", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.active[0].Operation.State = domain.OperationSucceeded
		}, want: "unsupported active state"},
		{name: "release read", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			fixture.state.releaseError = errors.New("release read failed")
		}, want: "release read failed"},
		{name: "release owner", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			release := fixture.state.releases[fixture.operationID]
			release.ProjectID = "project-other"
			fixture.state.releases[fixture.operationID] = release
		}, want: "owner differs"},
		{name: "unsupported release state", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			release := fixture.state.releases[fixture.operationID]
			release.State = state.ProjectNetworkReleaseState("future")
			fixture.state.releases[fixture.operationID] = release
		}, want: "unsupported"},
		{name: "withdrawal", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			fixture.withdrawal.err = errors.New("recovery withdrawal failed")
		}, want: "recovery withdrawal failed"},
		{name: "observation", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			fixture.observer.errors[fixture.leases[0].Address] = errors.New("recovery observation failed")
		}, want: "recovery observation failed"},
		{name: "empty retained leases", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			release := fixture.state.releases[fixture.operationID]
			release.ActiveLeases = nil
			fixture.state.releases[fixture.operationID] = release
		}, want: "no retained leases"},
		{name: "restage", configure: func(fixture *projectUnregisterFixture) {
			fixture.setRunningOperation()
			fixture.state.stageError = errors.New("restage failed")
		}, want: "restage failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			test.configure(fixture)
			err := fixture.coordinator.Recover(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Recover() error = %v, want %q", err, test.want)
			}
			if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
				t.Fatalf("recovery opened %d issuers", openCalls)
			}
		})
	}

	fixture := newProjectUnregisterFixture(t)
	fixture.state.active = []state.OperationRecord{projectUnregisterOtherOperation(fixture.now)}
	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover(other operation) error = %v", err)
	}
}

// TestProjectUnregisterFinishRunningRejectsMalformedOrIncompleteBoundaries verifies teardown never skips a durable prerequisite.
func TestProjectUnregisterFinishRunningRejectsMalformedOrIncompleteBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture)
		want      string
	}{
		{name: "release read", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.releaseError = errors.New("finish read failed")
		}, want: "finish read failed"},
		{name: "missing release", configure: func(fixture *projectUnregisterFixture) {
			delete(fixture.state.releases, fixture.operationID)
		}, want: "was not found"},
		{name: "release owner", configure: func(fixture *projectUnregisterFixture) {
			release := fixture.state.releases[fixture.operationID]
			release.ProjectID = "project-other"
			fixture.state.releases[fixture.operationID] = release
		}, want: "owner differs"},
		{name: "withdrawal", configure: func(fixture *projectUnregisterFixture) {
			fixture.withdrawal.err = errors.New("finish withdrawal failed")
		}, want: "finish withdrawal failed"},
		{name: "project read", configure: func(fixture *projectUnregisterFixture) {
			fixture.state.projectError = errors.New("project read failed")
		}, want: "project read failed"},
		{name: "release observation", configure: func(fixture *projectUnregisterFixture) {
			fixture.observer.errors[fixture.leases[0].Address] = errors.New("finish observation failed")
		}, want: "finish observation failed"},
		{name: "remaining exact", configure: func(*projectUnregisterFixture) {}, want: "still has 2"},
		{name: "generation exhausted", configure: func(fixture *projectUnregisterFixture) {
			fixture.setAllObservations(loopback.StateAbsent)
			fixture.state.runtime.Network.Ownership.Generation = uint64(^uint(0) >> 1)
		}, want: "generation is exhausted"},
		{name: "complete network", configure: func(fixture *projectUnregisterFixture) {
			fixture.setAllObservations(loopback.StateAbsent)
			fixture.state.completeNetworkError = errors.New("complete network failed")
		}, want: "complete network failed"},
		{name: "not complete", configure: func(fixture *projectUnregisterFixture) {
			release := fixture.state.releases[fixture.operationID]
			release.State = state.ProjectNetworkReleaseState("future")
			fixture.state.releases[fixture.operationID] = release
		}, want: "is not complete"},
		{name: "complete project", configure: func(fixture *projectUnregisterFixture) {
			configureCompletedProjectRelease(fixture)
			fixture.state.completeProjectError = errors.New("complete project failed")
		}, want: "complete project failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			fixture.setRunningOperation()
			test.configure(fixture)
			_, err := fixture.coordinator.finishRunning(context.Background(), fixture.state.active[0])
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("finishRunning() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterCompletionRequestValidatesDerivedEvidence verifies completion facts remain exact and persistence-valid.
func TestProjectUnregisterCompletionRequestValidatesDerivedEvidence(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	observations := projectUnregisterCompletionObservations(t, fixture)
	request, err := fixture.coordinator.completionRequest(
		fixture.state.runtime,
		fixture.state.project,
		fixture.state.active[0],
		fixture.state.releases[fixture.operationID],
		observations,
	)
	if err != nil {
		t.Fatalf("completionRequest() error = %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("derived request Validate() error = %v", err)
	}

	tests := []struct {
		name      string
		configure func(*projectUnregisterFixture, *[]plannedObservation)
		want      string
	}{
		{name: "generation exhausted", configure: func(fixture *projectUnregisterFixture, _ *[]plannedObservation) {
			fixture.state.runtime.Network.Ownership.Generation = uint64(^uint(0) >> 1)
		}, want: "generation is exhausted"},
		{name: "non absent evidence", configure: func(fixture *projectUnregisterFixture, observations *[]plannedObservation) {
			observation := projectUnregisterExactObservation(fixture.leases[0].Address)
			fingerprint, fingerprintErr := observation.Fingerprint()
			if fingerprintErr != nil {
				t.Fatalf("fingerprint exact observation: %v", fingerprintErr)
			}
			(*observations)[0].observation = observation
			(*observations)[0].fingerprint = fingerprint
		}, want: "still has 1"},
		{name: "missing retained lease", configure: func(_ *projectUnregisterFixture, observations *[]plannedObservation) {
			*observations = (*observations)[:1]
		}, want: "differs from retained leases"},
		{name: "mismatched retained lease", configure: func(_ *projectUnregisterFixture, observations *[]plannedObservation) {
			(*observations)[0].lease.Ownership.Generation++
		}, want: "differs from retained leases"},
		{name: "derived request validation", configure: func(fixture *projectUnregisterFixture, _ *[]plannedObservation) {
			fixture.state.project.Revision = 0
		}, want: "derive project network release completion"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			fixture.setRunningOperation()
			observations := projectUnregisterCompletionObservations(t, fixture)
			test.configure(fixture, &observations)
			_, err := fixture.coordinator.completionRequest(
				fixture.state.runtime,
				fixture.state.project,
				fixture.state.active[0],
				fixture.state.releases[fixture.operationID],
				observations,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("completionRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterCompletionTimeAndGenerationUseEveryDurableLowerBound verifies monotonic derived facts.
func TestProjectUnregisterCompletionTimeAndGenerationUseEveryDurableLowerBound(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	fixture.coordinator.clock = projectUnregisterTestClock{now: fixture.now.Add(-2 * time.Hour)}
	fixture.state.runtime.Network.UpdatedAt = fixture.now.Add(-90 * time.Minute)
	release := fixture.state.releases[fixture.operationID]
	release.BeganAt = fixture.now.Add(-60 * time.Minute)
	release.ActiveLeases[0].LeasedAt = fixture.now.Add(-30 * time.Minute)
	release.ActiveLeases[1].LeasedAt = fixture.now.Add(-20 * time.Minute)
	release.ActiveLeases[1].Generation = 35
	fixture.state.releases[fixture.operationID] = release

	request, err := fixture.coordinator.completionRequest(
		fixture.state.runtime,
		fixture.state.project,
		fixture.state.active[0],
		release,
		projectUnregisterCompletionObservations(t, fixture),
	)
	if err != nil {
		t.Fatalf("completionRequest() error = %v", err)
	}
	if request.At != release.ActiveLeases[1].LeasedAt || request.CompletionGeneration != 36 {
		t.Fatalf("completion lower bounds = at %s, generation %d", request.At, request.CompletionGeneration)
	}
	if got := fixture.coordinator.operationTime(fixture.now.Add(-time.Hour), fixture.now); got != fixture.now {
		t.Fatalf("operationTime() = %s, want %s", got, fixture.now)
	}
}

// configureCompletedProjectRelease moves a fixture marker to the post-network-completion crash boundary.
func configureCompletedProjectRelease(fixture *projectUnregisterFixture) {
	release := fixture.state.releases[fixture.operationID]
	release.State = state.ProjectNetworkReleaseCompleted
	release.ActiveLeases = []state.NetworkLeaseEnsure{}
	release.Endpoints = []state.EndpointReservation{}
	release.Completion = &state.ProjectNetworkReleaseCompletion{
		Generation:  21,
		CompletedAt: fixture.now.Add(-time.Minute),
		Evidence:    "verified host release",
	}
	fixture.state.releases[fixture.operationID] = release
}

// projectUnregisterValueIssuer supplies a non-nil non-pointer implementation for reflection boundary tests.
type projectUnregisterValueIssuer struct{}

// Issue returns no capability because reflection tests never invoke this value issuer.
func (projectUnregisterValueIssuer) Issue(context.Context, string, ticketissuer.Request) (ticketissuer.Result, error) {
	return ticketissuer.Result{}, errors.New("value issuer is test-only")
}

// Close performs no work for the reflection boundary test double.
func (projectUnregisterValueIssuer) Close() error {
	return nil
}

// projectUnregisterPrepareRequest returns the fixture's canonical interactive selection.
func projectUnregisterPrepareRequest(fixture *projectUnregisterFixture) PrepareRequest {
	return PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	}
}

// projectUnregisterIssuerResult returns one complete valid result for a fixture lease.
func projectUnregisterIssuerResult(fixture *projectUnregisterFixture, leaseIndex int) ticketissuer.Result {
	lease := fixture.leases[leaseIndex]
	return ticketissuer.Result{
		OperationID: fixture.operationID,
		LeaseKey:    lease.Key,
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationReleaseLoopbackIdentity,
		Address:     lease.Address,
		ExpiresAt:   fixture.now.Add(time.Minute),
	}
}

// configureMultipleProjectPlan moves the secondary plan to another valid project while preserving release semantics.
func configureMultipleProjectPlan(t *testing.T, fixture *projectUnregisterFixture) {
	t.Helper()
	otherKey, err := identity.NewPrimaryKey("project-beta")
	if err != nil {
		t.Fatalf("create other project key: %v", err)
	}
	originalKey := fixture.leases[1].Key
	plan := fixture.plans.plans[originalKey]
	plan.Lease.Key = otherKey
	delete(fixture.plans.plans, originalKey)
	fixture.plans.plans[otherKey] = plan
	fixture.plans.requests[1].LeaseKey = otherKey
}

// configureObserverConflict returns a fixture mutation producing one valid classified conflict observation.
func configureObserverConflict(conflictState loopback.State) func(*projectUnregisterFixture, netip.Addr) {
	return func(fixture *projectUnregisterFixture, address netip.Addr) {
		observation := projectUnregisterExactObservation(address)
		switch conflictState {
		case loopback.StateForeign:
			observation = projectUnregisterForeignObservation(address)
		case loopback.StateNonHostPrefix:
			observation.Assignments[0].PrefixLength = 31
			observation.State = loopback.StateNonHostPrefix
		case loopback.StateAttributeConflict:
			observation.Assignments[0].Linux.Flags = 0
			observation.State = loopback.StateAttributeConflict
		case loopback.StateAmbiguous:
			foreign := projectUnregisterForeignObservation(address)
			observation.Assignments = append(observation.Assignments, foreign.Assignments[0])
			observation.State = loopback.StateAmbiguous
		default:
			panic("unsupported conflict state")
		}
		fixture.observer.facts[address] = observation
	}
}

// projectUnregisterCompletionObservations derives valid absent fingerprints for direct completion tests.
func projectUnregisterCompletionObservations(t *testing.T, fixture *projectUnregisterFixture) []plannedObservation {
	t.Helper()
	observations := make([]plannedObservation, 0, len(fixture.leases))
	for _, lease := range fixture.leases {
		observation := projectUnregisterAbsentObservation(lease.Address)
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint absent observation: %v", err)
		}
		observations = append(observations, plannedObservation{
			request: ticketissuer.Request{OperationID: fixture.operationID, LeaseKey: lease.Key},
			lease:   lease, observation: observation, fingerprint: fingerprint,
		})
	}
	return observations
}

// projectUnregisterOtherOperation returns one active record ignored by unregister recovery.
func projectUnregisterOtherOperation(now time.Time) state.OperationRecord {
	operation := projectUnregisterTestOperation(now, "project-other", "operation-other", domain.OperationQueued, 1)
	operation.Operation.Kind = domain.OperationKind("project.start")
	return operation
}
