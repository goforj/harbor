package reconcile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

var errNetworkSetupTest = errors.New("network setup test error")

// networkSetupTestClock provides deterministic coordinator admission time.
type networkSetupTestClock struct {
	now time.Time
}

// Now returns the fixture's deterministic instant.
func (clock networkSetupTestClock) Now() time.Time {
	return clock.now
}

// networkSetupTestJournal scripts each durable journal boundary independently.
type networkSetupTestJournal struct {
	operation func(context.Context, domain.OperationID) (state.OperationRecord, error)
	byIntent  func(context.Context, domain.IntentID) (state.OperationRecord, error)
	stage     func(context.Context, state.StageNetworkSetupRequest) (state.OperationRecord, error)
}

// Operation delegates one exact operation read to the fixture script.
func (journal *networkSetupTestJournal) Operation(ctx context.Context, id domain.OperationID) (state.OperationRecord, error) {
	return journal.operation(ctx, id)
}

// OperationByIntent delegates one intent lookup to the fixture script.
func (journal *networkSetupTestJournal) OperationByIntent(ctx context.Context, id domain.IntentID) (state.OperationRecord, error) {
	return journal.byIntent(ctx, id)
}

// StageNetworkSetup delegates one staging mutation to the fixture script.
func (journal *networkSetupTestJournal) StageNetworkSetup(ctx context.Context, request state.StageNetworkSetupRequest) (state.OperationRecord, error) {
	return journal.stage(ctx, request)
}

// networkSetupTestPlans scripts one immutable plan lookup.
type networkSetupTestPlans struct {
	resolve func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error)
}

// Resolve delegates one plan lookup to the fixture script.
func (plans *networkSetupTestPlans) Resolve(ctx context.Context, request ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
	return plans.resolve(ctx, request)
}

// networkSetupTestStore records one completion request through a fixture script.
type networkSetupTestStore struct {
	complete func(context.Context, state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error)
}

// CompleteNetworkSetup delegates one completion mutation to the fixture script.
func (store *networkSetupTestStore) CompleteNetworkSetup(ctx context.Context, request state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error) {
	return store.complete(ctx, request)
}

// networkSetupTestKeys scripts signing-key loading and close behavior.
type networkSetupTestKeys struct {
	private    ed25519.PrivateKey
	loadErr    error
	closeErr   error
	loadCalls  int
	closeCalls int
}

// LoadOrCreate returns the fixture's signing identity or load failure.
func (keys *networkSetupTestKeys) LoadOrCreate(context.Context) (ed25519.PrivateKey, error) {
	keys.loadCalls++
	return keys.private, keys.loadErr
}

// Close records lifecycle closure and returns the fixture's close failure.
func (keys *networkSetupTestKeys) Close() error {
	keys.closeCalls++
	return keys.closeErr
}

// networkSetupTestSelector scripts exact pool selection.
type networkSetupTestSelector struct {
	selectPool func(context.Context, identity.InstallationID, string) (identity.PoolSelection, error)
}

// Select delegates one safe pool scan to the fixture script.
func (selector *networkSetupTestSelector) Select(ctx context.Context, installationID identity.InstallationID, requester string) (identity.PoolSelection, error) {
	return selector.selectPool(ctx, installationID, requester)
}

// networkSetupTestIssuer scripts pool capability publication and closure.
type networkSetupTestIssuer struct {
	issue      func(context.Context, string, ticketissuer.PoolRequest) (ticketissuer.PoolResult, error)
	closeErr   error
	closeCalls int
}

// Issue delegates one pool capability publication to the fixture script.
func (issuer *networkSetupTestIssuer) Issue(ctx context.Context, requester string, request ticketissuer.PoolRequest) (ticketissuer.PoolResult, error) {
	return issuer.issue(ctx, requester, request)
}

// Close records issuer closure and returns the fixture's close failure.
func (issuer *networkSetupTestIssuer) Close() error {
	issuer.closeCalls++
	return issuer.closeErr
}

// networkSetupTestOwnership scripts the daemon's confirmed ownership projection.
type networkSetupTestOwnership struct {
	observe func(context.Context) (ownership.Observation, error)
}

// Observe delegates one confirmed ownership projection read to the fixture script.
func (observer *networkSetupTestOwnership) Observe(ctx context.Context) (ownership.Observation, error) {
	return observer.observe(ctx)
}

// networkSetupTestLoopback scripts exact native address observations.
type networkSetupTestLoopback struct {
	observe func(context.Context, netip.Addr) (loopback.Observation, error)
}

// Observe delegates one loopback address read to the fixture script.
func (observer *networkSetupTestLoopback) Observe(ctx context.Context, address netip.Addr) (loopback.Observation, error) {
	return observer.observe(ctx, address)
}

// TestNetworkSetupStartStagesAndReplays verifies new authority construction and the side-effect-free intent fast path.
func TestNetworkSetupStartStagesAndReplays(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	pool := networkSetupTestPool(t, "127.91.0.8/29")
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	keys := &networkSetupTestKeys{private: privateKey}
	var stagedRequest state.StageNetworkSetupRequest
	journal := &networkSetupTestJournal{
		operation: networkSetupUnexpectedOperation,
		byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
			return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-setup"}
		},
		stage: func(_ context.Context, request state.StageNetworkSetupRequest) (state.OperationRecord, error) {
			stagedRequest = request
			return networkSetupApprovalFromQueued(t, request.Operation), nil
		},
	}
	selectorCalls := 0
	selector := &networkSetupTestSelector{selectPool: func(_ context.Context, installationID identity.InstallationID, requester string) (identity.PoolSelection, error) {
		selectorCalls++
		if installationID != "installation-setup" || requester != "501" {
			t.Fatalf("Select() identity = %q/%q", installationID, requester)
		}
		return identity.PoolSelection{Pool: pool}, nil
	}}
	coordinator := networkSetupTestCoordinator(now, journal, networkSetupUnusedPlans(), networkSetupUnusedStore(), func() (SigningKeyStore, error) {
		return keys, nil
	}, selector, networkSetupUnusedIssuerFactory(), networkSetupUnusedOwnership(), networkSetupUnusedLoopback())

	request := NetworkSetupStartRequest{
		OperationID: "operation-setup", IntentID: "intent-setup", InstallationID: "installation-setup", RequesterIdentity: "501",
	}
	started, err := coordinator.Start(nil, request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if started.Operation.State != domain.OperationRequiresApproval || started.Revision != 3 {
		t.Fatalf("Start() = %#v", started)
	}
	wantVerifier := base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))
	if stagedRequest.Operation.ID != request.OperationID || stagedRequest.Operation.IntentID != request.IntentID ||
		stagedRequest.Operation.Kind != domain.OperationKindNetworkSetup || stagedRequest.Operation.ProjectID != "" ||
		!stagedRequest.Operation.RequestedAt.Equal(now) || stagedRequest.Ownership != (ownership.Record{
		SchemaVersion: ownership.CurrentSchemaVersion, InstallationID: "installation-setup", OwnerIdentity: "501",
		Generation: 1, LoopbackPoolPrefix: pool.Prefix().String(), TicketVerifierKey: wantVerifier,
	}) {
		t.Fatalf("StageNetworkSetup() request = %#v", stagedRequest)
	}
	if keys.loadCalls != 1 || keys.closeCalls != 1 || selectorCalls != 1 {
		t.Fatalf("new Start() side effects = load %d, close %d, select %d", keys.loadCalls, keys.closeCalls, selectorCalls)
	}

	replayed := started
	journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) { return replayed, nil }
	journal.stage = func(context.Context, state.StageNetworkSetupRequest) (state.OperationRecord, error) {
		t.Fatal("replayed Start() staged durable authority")
		return state.OperationRecord{}, nil
	}
	coordinator.keys = func() (SigningKeyStore, error) {
		t.Fatal("replayed Start() opened signing keys")
		return nil, nil
	}
	selector.selectPool = func(context.Context, identity.InstallationID, string) (identity.PoolSelection, error) {
		t.Fatal("replayed Start() scanned pools")
		return identity.PoolSelection{}, nil
	}
	got, err := coordinator.Start(context.Background(), NetworkSetupStartRequest{
		OperationID: "operation-proposed", IntentID: request.IntentID, InstallationID: "installation-other", RequesterIdentity: "502",
	})
	if err != nil || !reflect.DeepEqual(got, replayed) {
		t.Fatalf("replayed Start() = %#v, %v, want %#v", got, err, replayed)
	}

	journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-other"}
	}
	_, err = coordinator.Start(context.Background(), NetworkSetupStartRequest{
		OperationID: "operation-mismatch", IntentID: "intent-mismatch", InstallationID: "installation-mismatch", RequesterIdentity: "501",
	})
	if err == nil {
		t.Fatal("Start(mismatched missing intent) error = nil")
	}
}

// TestNetworkSetupStartClosesKeysOnFailures verifies external key resources close before selection or staging failures escape.
func TestNetworkSetupStartClosesKeysOnFailures(t *testing.T) {
	tests := []struct {
		name      string
		loadErr   error
		closeErr  error
		selectErr error
	}{
		{name: "load", loadErr: errNetworkSetupTest},
		{name: "close", closeErr: errNetworkSetupTest},
		{name: "select", selectErr: errNetworkSetupTest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keys := &networkSetupTestKeys{
				private: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize)), loadErr: test.loadErr, closeErr: test.closeErr,
			}
			stageCalls := 0
			journal := &networkSetupTestJournal{
				operation: networkSetupUnexpectedOperation,
				byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
					return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-failure"}
				},
				stage: func(context.Context, state.StageNetworkSetupRequest) (state.OperationRecord, error) {
					stageCalls++
					return state.OperationRecord{}, nil
				},
			}
			selectorCalls := 0
			selector := &networkSetupTestSelector{selectPool: func(context.Context, identity.InstallationID, string) (identity.PoolSelection, error) {
				selectorCalls++
				return identity.PoolSelection{Pool: networkSetupTestPool(t, "127.92.0.8/29")}, test.selectErr
			}}
			coordinator := networkSetupTestCoordinator(time.Now().UTC(), journal, networkSetupUnusedPlans(), networkSetupUnusedStore(), func() (SigningKeyStore, error) {
				return keys, nil
			}, selector, networkSetupUnusedIssuerFactory(), networkSetupUnusedOwnership(), networkSetupUnusedLoopback())
			_, err := coordinator.Start(t.Context(), NetworkSetupStartRequest{
				OperationID: "operation-failure", IntentID: "intent-failure", InstallationID: "installation-failure", RequesterIdentity: "501",
			})
			if !errors.Is(err, errNetworkSetupTest) {
				t.Fatalf("Start() error = %v, want sentinel", err)
			}
			if keys.closeCalls != 1 || stageCalls != 0 {
				t.Fatalf("failure lifecycle = close %d, stage %d", keys.closeCalls, stageCalls)
			}
			if (test.loadErr != nil || test.closeErr != nil) && selectorCalls != 0 {
				t.Fatalf("Select() calls = %d after key failure", selectorCalls)
			}
		})
	}
}

// TestNetworkSetupPrepareValidatesPlanAndIssuerResult verifies correlation, owner admission, and issuer closure.
func TestNetworkSetupPrepareValidatesPlanAndIssuerResult(t *testing.T) {
	now := time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)
	plan := networkSetupTestPlan(t, "operation-prepare", 3, "501", "127.93.0.8/29")
	result := ticketissuer.PoolResult{
		OperationID: plan.OperationID, Reference: helper.TicketReference(strings.Repeat("b", 64)),
		Operation: helper.OperationEnsureLoopbackPool, Pool: plan.Pool.Prefix(), ExpiresAt: now.Add(time.Minute),
	}
	issuer := &networkSetupTestIssuer{issue: func(_ context.Context, requester string, request ticketissuer.PoolRequest) (ticketissuer.PoolResult, error) {
		if requester != plan.Ownership.OwnerIdentity || request.OperationID != plan.OperationID {
			t.Fatalf("Issue() = %q/%#v", requester, request)
		}
		return result, nil
	}}
	openCalls := 0
	coordinator := networkSetupTestCoordinator(now, networkSetupUnusedJournal(), &networkSetupTestPlans{
		resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) { return plan, nil },
	}, networkSetupUnusedStore(), networkSetupUnusedKeyFactory(), networkSetupUnusedSelector(), func() (PoolIssuer, error) {
		openCalls++
		return issuer, nil
	}, networkSetupUnusedOwnership(), networkSetupUnusedLoopback())

	got, err := coordinator.Prepare(nil, NetworkSetupPrepareRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: "501",
	})
	if err != nil || got != result || issuer.closeCalls != 1 || openCalls != 1 {
		t.Fatalf("Prepare() = %#v, %v; closes %d opens %d", got, err, issuer.closeCalls, openCalls)
	}

	_, err = coordinator.Prepare(t.Context(), NetworkSetupPrepareRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: "502",
	})
	if err == nil || openCalls != 1 {
		t.Fatalf("Prepare(owner mismatch) error = %v, opens %d", err, openCalls)
	}

	issuer.issue = func(context.Context, string, ticketissuer.PoolRequest) (ticketissuer.PoolResult, error) {
		wrong := result
		wrong.Pool = netip.MustParsePrefix("127.94.0.8/29")
		return wrong, nil
	}
	_, err = coordinator.Prepare(t.Context(), NetworkSetupPrepareRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: "501",
	})
	if err == nil || issuer.closeCalls != 2 {
		t.Fatalf("Prepare(result mismatch) error = %v, closes %d", err, issuer.closeCalls)
	}

	issuer.issue = func(context.Context, string, ticketissuer.PoolRequest) (ticketissuer.PoolResult, error) {
		return result, nil
	}
	issuer.closeErr = errNetworkSetupTest
	_, err = coordinator.Prepare(t.Context(), NetworkSetupPrepareRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: "501",
	})
	if !errors.Is(err, errNetworkSetupTest) || issuer.closeCalls != 3 {
		t.Fatalf("Prepare(close failure) error = %v, closes %d", err, issuer.closeCalls)
	}
}

// TestNetworkSetupConfirmFreshAndTerminalReplay verifies exact-eight observation and original-time replay composition.
func TestNetworkSetupConfirmFreshAndTerminalReplay(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	plan := networkSetupTestPlan(t, "operation-confirm", 3, "501", "127.95.0.8/29")
	approval := networkSetupApprovalOperation(t, plan.OperationID, "intent-confirm", now.Add(-time.Minute))
	evidence := networkSetupTestEvidence(plan.Pool.Prefix())
	journal := &networkSetupTestJournal{
		operation: func(context.Context, domain.OperationID) (state.OperationRecord, error) { return approval, nil },
		byIntent:  networkSetupUnexpectedIntent,
		stage:     networkSetupUnexpectedStage,
	}
	plansCalls := 0
	plans := &networkSetupTestPlans{resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
		plansCalls++
		return plan, nil
	}}
	var observed []netip.Addr
	loopbackObserver := &networkSetupTestLoopback{observe: func(_ context.Context, address netip.Addr) (loopback.Observation, error) {
		observed = append(observed, address)
		return networkSetupTestObservation(address), nil
	}}
	var completion state.CompleteNetworkSetupRequest
	store := &networkSetupTestStore{complete: func(_ context.Context, request state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error) {
		completion = request
		return networkSetupTestCompletionResult(t, approval, plan.Pool, now, false), nil
	}}
	ownershipCalls := 0
	owner := &networkSetupTestOwnership{observe: func(context.Context) (ownership.Observation, error) {
		ownershipCalls++
		return ownership.Observation{}, nil
	}}
	coordinator := networkSetupTestCoordinator(now, journal, plans, store, networkSetupUnusedKeyFactory(), networkSetupUnusedSelector(), networkSetupUnusedIssuerFactory(), owner, loopbackObserver)

	result, err := coordinator.Confirm(nil, NetworkSetupConfirmRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, HelperPoolEvidence: evidence,
	})
	if err != nil || result.Operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("Confirm(fresh) = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(observed, plan.Pool.Candidates()) || ownershipCalls != 0 || plansCalls != 1 {
		t.Fatalf("fresh observations = %#v, ownership calls %d, plans %d", observed, ownershipCalls, plansCalls)
	}
	fingerprint, _ := plan.Ownership.Fingerprint()
	if completion.ConfirmedOwnership != (ownership.Observation{Exists: true, Record: plan.Ownership, Fingerprint: fingerprint}) ||
		!reflect.DeepEqual(completion.HelperPoolEvidence, evidence) || !completion.At.Equal(now) {
		t.Fatalf("fresh completion request = %#v", completion)
	}

	finishedAt := now.Add(2 * time.Minute)
	succeeded := networkSetupSucceededOperation(t, approval, finishedAt)
	journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) { return succeeded, nil }
	projected := ownership.Observation{Exists: true, Record: plan.Ownership, Fingerprint: fingerprint}
	owner.observe = func(context.Context) (ownership.Observation, error) {
		ownershipCalls++
		return projected, nil
	}
	plans.resolve = func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
		t.Fatal("terminal Confirm() resolved a retired plan")
		return ticketissuer.PoolPlan{}, nil
	}
	observed = nil
	store.complete = func(_ context.Context, request state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error) {
		completion = request
		return networkSetupTestCompletionResult(t, approval, plan.Pool, finishedAt, true), nil
	}
	_, err = coordinator.Confirm(t.Context(), NetworkSetupConfirmRequest{
		OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, HelperPoolEvidence: evidence,
	})
	if err != nil {
		t.Fatalf("Confirm(terminal) error = %v", err)
	}
	if ownershipCalls != 1 || !reflect.DeepEqual(observed, plan.Pool.Candidates()) || !completion.At.Equal(finishedAt) || completion.ConfirmedOwnership != projected {
		t.Fatalf("terminal replay = ownership %d, observed %#v, completion %#v", ownershipCalls, observed, completion)
	}
}

// TestNetworkSetupRejectsInvalidInputAndUncorrelatedObservations verifies zero downstream calls at fail-closed boundaries.
func TestNetworkSetupRejectsInvalidInputAndUncorrelatedObservations(t *testing.T) {
	var operationCalls atomic.Int32
	journal := networkSetupUnusedJournal()
	journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
		operationCalls.Add(1)
		return state.OperationRecord{}, errNetworkSetupTest
	}
	coordinator := networkSetupTestCoordinator(time.Now().UTC(), journal, networkSetupUnusedPlans(), networkSetupUnusedStore(), networkSetupUnusedKeyFactory(), networkSetupUnusedSelector(), networkSetupUnusedIssuerFactory(), networkSetupUnusedOwnership(), networkSetupUnusedLoopback())

	invalidEvidence := networkSetupTestEvidence(netip.MustParsePrefix("127.96.0.8/29"))
	invalidEvidence.Identities = invalidEvidence.Identities[:7]
	_, err := coordinator.Confirm(t.Context(), NetworkSetupConfirmRequest{
		OperationID: "operation-invalid", ExpectedOperationRevision: 3, HelperPoolEvidence: invalidEvidence,
	})
	if err == nil || operationCalls.Load() != 0 {
		t.Fatalf("Confirm(invalid evidence) error = %v, operation calls %d", err, operationCalls.Load())
	}

	byIntentCalls := 0
	journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		byIntentCalls++
		return state.OperationRecord{}, nil
	}
	_, err = coordinator.Start(t.Context(), NetworkSetupStartRequest{IntentID: "intent", InstallationID: "installation", RequesterIdentity: "501"})
	if err == nil || byIntentCalls != 0 {
		t.Fatalf("Start(invalid) error = %v, intent calls %d", err, byIntentCalls)
	}

	planCalls := 0
	coordinator.plans = &networkSetupTestPlans{resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
		planCalls++
		return ticketissuer.PoolPlan{}, nil
	}}
	_, err = coordinator.Prepare(t.Context(), NetworkSetupPrepareRequest{OperationID: "operation", RequesterIdentity: "501"})
	if err == nil || planCalls != 0 {
		t.Fatalf("Prepare(invalid) error = %v, plan calls %d", err, planCalls)
	}
}

// TestNetworkSetupConfirmRejectsMismatchCancellationAndSerializes verifies host correlation, cancellation, and one-process ordering.
func TestNetworkSetupConfirmRejectsMismatchCancellationAndSerializes(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	plan := networkSetupTestPlan(t, "operation-serialized", 3, "501", "127.97.0.8/29")
	approval := networkSetupApprovalOperation(t, plan.OperationID, "intent-serialized", now)
	storeCalls := 0
	journal := &networkSetupTestJournal{
		operation: func(context.Context, domain.OperationID) (state.OperationRecord, error) { return approval, nil },
		byIntent:  networkSetupUnexpectedIntent,
		stage:     networkSetupUnexpectedStage,
	}
	loopbackObserver := &networkSetupTestLoopback{observe: func(_ context.Context, address netip.Addr) (loopback.Observation, error) {
		return networkSetupTestObservation(address.Next()), nil
	}}
	coordinator := networkSetupTestCoordinator(now, journal, &networkSetupTestPlans{
		resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) { return plan, nil },
	}, &networkSetupTestStore{complete: func(context.Context, state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error) {
		storeCalls++
		return state.CompleteNetworkSetupResult{}, nil
	}}, networkSetupUnusedKeyFactory(), networkSetupUnusedSelector(), networkSetupUnusedIssuerFactory(), networkSetupUnusedOwnership(), loopbackObserver)
	request := NetworkSetupConfirmRequest{OperationID: plan.OperationID, ExpectedOperationRevision: 3, HelperPoolEvidence: networkSetupTestEvidence(plan.Pool.Prefix())}
	_, err := coordinator.Confirm(t.Context(), request)
	if err == nil || storeCalls != 0 {
		t.Fatalf("Confirm(address mismatch) error = %v, store calls %d", err, storeCalls)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = coordinator.Confirm(cancelled, request)
	if !errors.Is(err, context.Canceled) || storeCalls != 0 {
		t.Fatalf("Confirm(cancelled) error = %v, store calls %d", err, storeCalls)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		close(entered)
		<-release
		return approval, nil
	}
	prepareEntered := make(chan struct{}, 1)
	coordinator.plans = &networkSetupTestPlans{resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
		prepareEntered <- struct{}{}
		return plan, nil
	}}
	startDone := make(chan error, 1)
	go func() {
		_, startErr := coordinator.Start(context.Background(), NetworkSetupStartRequest{
			OperationID: "operation-proposed", IntentID: approval.Operation.IntentID, InstallationID: "installation-serialized", RequesterIdentity: "501",
		})
		startDone <- startErr
	}()
	<-entered
	prepareDone := make(chan error, 1)
	go func() {
		_, prepareErr := coordinator.Prepare(context.Background(), NetworkSetupPrepareRequest{
			OperationID: plan.OperationID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: "501",
		})
		prepareDone <- prepareErr
	}()
	select {
	case <-prepareEntered:
		t.Fatal("Prepare() crossed the serialized Start() boundary")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-startDone; err != nil {
		t.Fatalf("serialized Start() error = %v", err)
	}
	select {
	case <-prepareEntered:
	case <-time.After(time.Second):
		t.Fatal("Prepare() did not resume after Start()")
	}
	// The unused issuer fails after plan resolution, which is sufficient to prove lock ordering.
	if err := <-prepareDone; err == nil {
		t.Fatal("serialized Prepare() unexpectedly succeeded")
	}
}

// networkSetupTestCoordinator wires explicit fixture dependencies through the production constructor.
func networkSetupTestCoordinator(
	now time.Time,
	journal NetworkSetupOperationJournal,
	plans NetworkSetupPlanSource,
	store NetworkSetupStore,
	keys SigningKeyStoreFactory,
	selector PoolSelector,
	issuers PoolIssuerFactory,
	owner OwnershipObserver,
	loopbackObserver LoopbackObserver,
) *NetworkSetupCoordinator {
	return NewNetworkSetupCoordinator(journal, plans, store, keys, selector, issuers, owner, loopbackObserver, networkSetupTestClock{now: now})
}

// networkSetupTestPool constructs one complete canonical /29 fixture pool.
func networkSetupTestPool(t *testing.T, rawPrefix string) identity.Pool {
	t.Helper()
	prefix := netip.MustParsePrefix(rawPrefix)
	addresses := make([]netip.Addr, networkSetupPoolAddressCount)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	return pool
}

// networkSetupTestOwnershipRecord constructs one valid generation-one owner for a pool.
func networkSetupTestOwnershipRecord(t *testing.T, requester string, pool identity.Pool) ownership.Record {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	record := ownership.Record{
		SchemaVersion: ownership.CurrentSchemaVersion, InstallationID: "installation-test", OwnerIdentity: requester,
		Generation: 1, LoopbackPoolPrefix: pool.Prefix().String(),
		TicketVerifierKey: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("ownership.Record.Validate() error = %v", err)
	}
	return record
}

// networkSetupTestPlan constructs one valid bootstrap approval plan.
func networkSetupTestPlan(t *testing.T, operationID domain.OperationID, revision domain.Sequence, requester, prefix string) ticketissuer.PoolPlan {
	t.Helper()
	pool := networkSetupTestPool(t, prefix)
	plan := ticketissuer.PoolPlan{
		OperationID: operationID, OperationRevision: revision, OperationState: domain.OperationRequiresApproval,
		Mode: ticketissuer.PoolModeBootstrap, Ownership: networkSetupTestOwnershipRecord(t, requester, pool), Pool: pool,
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("PoolPlan.Validate() error = %v", err)
	}
	return plan
}

// networkSetupApprovalOperation creates the fixed staged approval lifecycle used by confirmation tests.
func networkSetupApprovalOperation(t *testing.T, operationID domain.OperationID, intentID domain.IntentID, at time.Time) state.OperationRecord {
	t.Helper()
	queued, err := domain.NewOperation(operationID, intentID, domain.OperationKindNetworkSetup, "", at)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return networkSetupApprovalFromQueued(t, queued)
}

// networkSetupApprovalFromQueued advances one queued fixture through the fixed setup staging lifecycle.
func networkSetupApprovalFromQueued(t *testing.T, queued domain.Operation) state.OperationRecord {
	t.Helper()
	running, err := queued.Transition(domain.OperationRunning, "preparing", queued.RequestedAt, nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	approval, err := running.Transition(domain.OperationRequiresApproval, "awaiting approval", queued.RequestedAt, nil)
	if err != nil {
		t.Fatalf("Transition(approval) error = %v", err)
	}
	return state.OperationRecord{Operation: approval, Revision: 3}
}

// networkSetupSucceededOperation advances one approval fixture to its original terminal projection.
func networkSetupSucceededOperation(t *testing.T, approval state.OperationRecord, finishedAt time.Time) state.OperationRecord {
	t.Helper()
	running, err := approval.Operation.Transition(domain.OperationRunning, "committing", finishedAt, nil)
	if err != nil {
		t.Fatalf("Transition(committing) error = %v", err)
	}
	succeeded, err := running.Transition(domain.OperationSucceeded, "completed", finishedAt, nil)
	if err != nil {
		t.Fatalf("Transition(completed) error = %v", err)
	}
	return state.OperationRecord{Operation: succeeded, Revision: 6}
}

// networkSetupTestEvidence constructs canonical owned helper evidence without tying it to daemon facts.
func networkSetupTestEvidence(prefix netip.Prefix) helper.PoolMutationEvidence {
	identities := make([]helper.MutationEvidence, networkSetupPoolAddressCount)
	address := prefix.Addr()
	for index := range identities {
		identities[index] = helper.MutationEvidence{
			Changed: true, Address: address.String(),
			Observation: helper.ExpectedObservation{State: helper.ObservationOwned, Fingerprint: strings.Repeat("a", 64)},
		}
		address = address.Next()
	}
	return helper.PoolMutationEvidence{Pool: prefix.String(), Identities: identities}
}

// networkSetupTestObservation constructs one valid exact Linux loopback observation.
func networkSetupTestObservation(address netip.Addr) loopback.Observation {
	return loopback.Observation{
		Address:  address,
		Loopback: loopback.InterfaceFact{Name: "lo", Index: 1, Kind: loopback.InterfaceKindLinuxNative, NativeLoopback: true},
		State:    loopback.StateExact,
		Assignments: []loopback.AssignmentFact{{
			Address: address, PrefixLength: 32, InterfaceName: "lo", InterfaceIndex: 1,
			NativeLoopback: true, InterfaceKind: loopback.InterfaceKindLinuxNative,
			Linux: &loopback.LinuxAssignmentFact{
				Scope: loopback.LinuxAddressScopeHost, Flags: 1 << 7, Label: "lo", AddressMatchesLocal: true,
				CacheInfoPresent: true, ValidLifetimeSeconds: ^uint32(0), PreferredLifetimeSeconds: ^uint32(0),
			},
		}},
	}
}

// networkSetupTestCompletionResult constructs one valid identity-stage completion readback.
func networkSetupTestCompletionResult(
	t *testing.T,
	approval state.OperationRecord,
	pool identity.Pool,
	finishedAt time.Time,
	replayed bool,
) state.CompleteNetworkSetupResult {
	t.Helper()
	succeeded := networkSetupSucceededOperation(t, approval, finishedAt)
	result := state.CompleteNetworkSetupResult{
		Operation: succeeded,
		Network: state.NetworkMutationResult{
			Replayed: replayed,
			Record: state.NetworkRecord{
				Stage: state.NetworkStageIdentity, Revision: 5, CreatedAt: finishedAt, UpdatedAt: finishedAt,
				Ownership: identity.Ownership{InstallationID: "installation-test", Generation: 1}, Pool: pool,
				Leases: []identity.Lease{}, Quarantines: []identity.Quarantine{},
				Reservations: state.DataPlaneReservations{Endpoints: []state.EndpointReservation{}, SuppressedProjectIDs: []domain.ProjectID{}},
			},
		},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("CompleteNetworkSetupResult.Validate() error = %v", err)
	}
	return result
}

// networkSetupUnusedJournal returns a complete journal that fails if an unexpected path consumes it.
func networkSetupUnusedJournal() *networkSetupTestJournal {
	return &networkSetupTestJournal{operation: networkSetupUnexpectedOperation, byIntent: networkSetupUnexpectedIntent, stage: networkSetupUnexpectedStage}
}

// networkSetupUnusedPlans returns a plan source that reports unexpected consumption.
func networkSetupUnusedPlans() *networkSetupTestPlans {
	return &networkSetupTestPlans{resolve: func(context.Context, ticketissuer.PoolRequest) (ticketissuer.PoolPlan, error) {
		return ticketissuer.PoolPlan{}, errNetworkSetupTest
	}}
}

// networkSetupUnusedStore returns a completion store that reports unexpected consumption.
func networkSetupUnusedStore() *networkSetupTestStore {
	return &networkSetupTestStore{complete: func(context.Context, state.CompleteNetworkSetupRequest) (state.CompleteNetworkSetupResult, error) {
		return state.CompleteNetworkSetupResult{}, errNetworkSetupTest
	}}
}

// networkSetupUnusedKeyFactory returns a key factory that reports unexpected consumption.
func networkSetupUnusedKeyFactory() SigningKeyStoreFactory {
	return func() (SigningKeyStore, error) { return nil, errNetworkSetupTest }
}

// networkSetupUnusedSelector returns a selector that reports unexpected consumption.
func networkSetupUnusedSelector() *networkSetupTestSelector {
	return &networkSetupTestSelector{selectPool: func(context.Context, identity.InstallationID, string) (identity.PoolSelection, error) {
		return identity.PoolSelection{}, errNetworkSetupTest
	}}
}

// networkSetupUnusedIssuerFactory returns an issuer factory that reports unexpected consumption.
func networkSetupUnusedIssuerFactory() PoolIssuerFactory {
	return func() (PoolIssuer, error) { return nil, errNetworkSetupTest }
}

// networkSetupUnusedOwnership returns an observer that reports unexpected consumption.
func networkSetupUnusedOwnership() *networkSetupTestOwnership {
	return &networkSetupTestOwnership{observe: func(context.Context) (ownership.Observation, error) {
		return ownership.Observation{}, errNetworkSetupTest
	}}
}

// networkSetupUnusedLoopback returns a native observer that reports unexpected consumption.
func networkSetupUnusedLoopback() *networkSetupTestLoopback {
	return &networkSetupTestLoopback{observe: func(context.Context, netip.Addr) (loopback.Observation, error) {
		return loopback.Observation{}, errNetworkSetupTest
	}}
}

// networkSetupUnexpectedOperation reports an unexpected journal operation read.
func networkSetupUnexpectedOperation(context.Context, domain.OperationID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errNetworkSetupTest
}

// networkSetupUnexpectedIntent reports an unexpected journal intent read.
func networkSetupUnexpectedIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errNetworkSetupTest
}

// networkSetupUnexpectedStage reports an unexpected journal staging mutation.
func networkSetupUnexpectedStage(context.Context, state.StageNetworkSetupRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errNetworkSetupTest
}
