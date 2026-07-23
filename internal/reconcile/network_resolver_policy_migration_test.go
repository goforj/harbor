package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/state"
)

// TestNetworkResolverPolicyMigrationRequestsRejectUnscopedInput keeps the coordinator's public control surface bounded before dependencies run.
func TestNetworkResolverPolicyMigrationRequestsRejectUnscopedInput(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "start empty requester",
			err: NetworkResolverPolicyMigrationStartRequest{
				OperationID: "migration-operation",
				IntentID:    "migration-intent",
			}.Validate(),
		},
		{
			name: "prepare zero revision",
			err: NetworkResolverPolicyMigrationPrepareRequest{
				OperationID:       "migration-operation",
				RequesterIdentity: "owner",
			}.Validate(),
		},
		{
			name: "confirm non-absent evidence",
			err: NetworkResolverPolicyMigrationConfirmRequest{
				OperationID:               "migration-operation",
				ExpectedOperationRevision: 1,
				ResolverEvidence: helper.ResolverMutationEvidence{
					Postcondition: helper.ResolverPostconditionExact,
				},
			}.Validate(),
		},
		{
			name: "confirm empty requester",
			err: NetworkResolverPolicyMigrationConfirmRequest{
				OperationID:               "migration-operation",
				ExpectedOperationRevision: 1,
				ResolverEvidence: helper.ResolverMutationEvidence{
					Postcondition: helper.ResolverPostconditionOwnedAbsent,
				},
			}.Validate(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestNetworkResolverPolicyMigrationStartReplayKeepsDerivedOwnershipApproval requires a retry to obtain fresh privileged proof.
func TestNetworkResolverPolicyMigrationStartReplayKeepsDerivedOwnershipApproval(t *testing.T) {
	fixture := newNetworkResolverPolicyMigrationReplayFixture(t, true)
	got, err := fixture.coordinator.Start(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got != fixture.operation {
		t.Fatalf("operation = %#v, want %#v", got, fixture.operation)
	}
}

// TestNetworkResolverPolicyMigrationStartRefreshesAuthorityAfterTransientDrift retries stage on exact authority drift.
func TestNetworkResolverPolicyMigrationStartRefreshesAuthorityAfterTransientDrift(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	fixture.network.Stage = state.NetworkStageResolver
	reads := 0
	fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
		reads++
		return fixture.network, true, nil
	}
	policy, err := networkplan.BuildLegacyMacOS(networkplan.Request{
		Platform:             networkplan.PlatformMacOS,
		InstallationID:       fixture.network.Ownership.InstallationID,
		Pool:                 fixture.network.Pool,
		AuthorityFingerprint: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("networkplan.BuildLegacyMacOS() error = %v", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("policy.Fingerprint() error = %v", err)
	}
	source := fixture.source
	source.Record.SchemaVersion = ownership.NetworkPolicySchemaVersion
	source.Record.NetworkPolicyFingerprint = policyFingerprint
	source.Fingerprint = networkResolverSetupTestOwnershipFingerprint(t, source.Record)
	fixture.source = source

	stageCalls := 0
	journal := &networkResolverPolicyMigrationTestJournal{
		byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
			return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-policy-migration"}
		},
		stage: func(_ context.Context, request state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error) {
			stageCalls++
			if stageCalls == 1 {
				return state.OperationRecord{}, errors.New("network resolver policy migration authority differs from the exact resolver stage")
			}
			return networkResolverPolicyMigrationTestApproval(request.Operation, 3), nil
		},
	}

	coordinator := NewNetworkResolverPolicyMigrationCoordinator(
		journal,
		fixture.networkSource,
		nil,
		nil,
		fixture.roots,
		nil,
		fixture.ownership,
		nil,
		networkplan.PlatformMacOS,
		networkResolverSetupTestClock{now: fixture.now},
	)
	got, err := coordinator.Start(t.Context(), NetworkResolverPolicyMigrationStartRequest{
		OperationID:       "operation-policy-migration",
		IntentID:          "intent-policy-migration",
		RequesterIdentity: fixture.source.Record.OwnerIdentity,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got.Operation.State != domain.OperationRequiresApproval || got.Operation.Phase != string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval) || got.Revision != 3 {
		t.Fatalf("Start() staged operation = %#v", got)
	}
	if stageCalls != 2 {
		t.Fatalf("stage calls = %d, want 2", stageCalls)
	}
	if reads != 2 {
		t.Fatalf("legacy authority reads = %d, want 2", reads)
	}
}

// TestNetworkResolverPolicyMigrationStartClearsStoppedProjectNativeEndpoints makes the legacy migration reachable after projects have reserved native ports.
func TestNetworkResolverPolicyMigrationStartClearsStoppedProjectNativeEndpoints(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	fixture.network.Stage = state.NetworkStageResolver
	policy, err := networkplan.BuildLegacyMacOS(networkplan.Request{
		Platform:             networkplan.PlatformMacOS,
		InstallationID:       fixture.network.Ownership.InstallationID,
		Pool:                 fixture.network.Pool,
		AuthorityFingerprint: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("networkplan.BuildLegacyMacOS() error = %v", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("policy.Fingerprint() error = %v", err)
	}
	source := fixture.source
	source.Record.SchemaVersion = ownership.NetworkPolicySchemaVersion
	source.Record.NetworkPolicyFingerprint = policyFingerprint
	source.Fingerprint = networkResolverSetupTestOwnershipFingerprint(t, source.Record)
	fixture.source = source

	projectID := domain.ProjectID("project-alpha")
	leaseKey, err := identity.NewPrimaryKey(projectID)
	if err != nil {
		t.Fatalf("identity.NewPrimaryKey() error = %v", err)
	}
	address := fixture.network.Pool.Candidates()[0]
	fixture.network.Leases = []identity.Lease{{
		Key:       leaseKey,
		Address:   address,
		Ownership: fixture.network.Ownership,
	}}
	fixture.network.Reservations.Endpoints = []state.EndpointReservation{{
		Key:        state.EndpointReservationKey{ProjectID: projectID, EndpointID: "mysql"},
		Protocol:   state.EndpointProtocolTCP,
		Host:       "mysql.alpha.test",
		Public:     netip.AddrPortFrom(address, 3306),
		Identity:   &leaseKey,
		Generation: 1,
	}}
	network := fixture.network
	fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
		return network, true, nil
	}

	clearCalls := 0
	store := &networkResolverPolicyMigrationReplayStore{
		clear: func(_ context.Context, request state.ClearResolverStageNativeTCPEndpointsRequest) (state.NetworkMutationResult, error) {
			clearCalls++
			if request.ExpectedNetworkRevision != network.Revision {
				t.Fatalf("clear expected network revision = %d, want %d", request.ExpectedNetworkRevision, network.Revision)
			}
			network.Revision++
			network.UpdatedAt = request.At
			network.Reservations.Endpoints = []state.EndpointReservation{}
			return state.NetworkMutationResult{Record: network}, nil
		},
	}
	journal := &networkResolverPolicyMigrationTestJournal{
		byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
			return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-policy-migration"}
		},
		stage: func(_ context.Context, request state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error) {
			if request.ExpectedNetworkRevision != network.Revision {
				t.Fatalf("staged network revision = %d, want refreshed %d", request.ExpectedNetworkRevision, network.Revision)
			}
			return networkResolverPolicyMigrationTestApproval(request.Operation, 4), nil
		},
	}
	coordinator := NewNetworkResolverPolicyMigrationCoordinator(
		journal,
		fixture.networkSource,
		nil,
		store,
		fixture.roots,
		nil,
		fixture.ownership,
		nil,
		networkplan.PlatformMacOS,
		networkResolverSetupTestClock{now: fixture.now},
	)

	got, err := coordinator.Start(t.Context(), NetworkResolverPolicyMigrationStartRequest{
		OperationID:       "operation-policy-migration",
		IntentID:          "intent-policy-migration",
		RequesterIdentity: fixture.source.Record.OwnerIdentity,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if clearCalls != 1 {
		t.Fatalf("endpoint clear calls = %d, want 1", clearCalls)
	}
	if got.Operation.State != domain.OperationRequiresApproval || got.Revision != 4 {
		t.Fatalf("Start() staged operation = %#v", got)
	}
}

// TestNetworkResolverPolicyMigrationStartReplayKeepsAbsentSchemaTwoApproval lets a restart reissue the exact pending retirement.
func TestNetworkResolverPolicyMigrationStartReplayKeepsAbsentSchemaTwoApproval(t *testing.T) {
	fixture := newNetworkResolverPolicyMigrationReplayFixture(t, false)
	got, err := fixture.coordinator.Start(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got != fixture.operation {
		t.Fatalf("operation = %#v, want %#v", got, fixture.operation)
	}
}

// TestNetworkResolverPolicyMigrationStartCompletedReplayBindsProjectedOwner keeps terminal progress private to the machine owner.
func TestNetworkResolverPolicyMigrationStartCompletedReplayBindsProjectedOwner(t *testing.T) {
	fixture := newNetworkResolverPolicyMigrationReplayFixture(t, true)
	completedAt := fixture.operation.Operation.RequestedAt.Add(time.Minute)
	completed := fixture.operation
	completed.Operation.State = domain.OperationSucceeded
	completed.Operation.Phase = "completed"
	completed.Operation.FinishedAt = &completedAt
	completed.Revision = 5
	fixture.coordinator.operations = networkResolverPolicyMigrationReplayJournal{
		operation: completed,
	}

	got, err := fixture.coordinator.Start(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("Start() completed replay error = %v", err)
	}
	if got != completed {
		t.Fatalf("Start() completed replay = %#v, want %#v", got, completed)
	}

	fixture.request.RequesterIdentity = "other-owner"
	if _, err := fixture.coordinator.Start(t.Context(), fixture.request); err == nil ||
		!strings.Contains(err.Error(), "authenticated requester") {
		t.Fatalf("Start() caller mismatch error = %v", err)
	}
}

// TestNetworkResolverPolicyMigrationConfirmReplaysCompletedResult verifies a lost terminal response never requires the retired plan.
func TestNetworkResolverPolicyMigrationConfirmReplaysCompletedResult(t *testing.T) {
	fixture := newNetworkResolverPolicyMigrationReplayFixture(t, true)
	completedAt := fixture.operation.Operation.RequestedAt.Add(time.Minute)
	completed := fixture.operation
	completed.Operation.State = domain.OperationSucceeded
	completed.Operation.Phase = "completed"
	completed.Operation.FinishedAt = &completedAt
	completed.Revision = 5
	replayed := state.CompleteNetworkResolverPolicyMigrationResult{
		Operation:       completed,
		NetworkRevision: 4,
		Network: state.NetworkMutationResult{
			Record: state.NetworkRecord{
				Revision: 4,
				Stage:    state.NetworkStageIdentity,
			},
			Replayed: true,
		},
	}
	store := &networkResolverPolicyMigrationReplayStore{replay: replayed}
	coordinator := NewNetworkResolverPolicyMigrationCoordinator(
		networkResolverPolicyMigrationReplayJournal{operation: completed},
		nil,
		nil,
		store,
		nil,
		nil,
		networkResolverPolicyMigrationReplayOwnership{observation: ownership.Observation{
			Exists: true,
			Record: ownership.Record{
				OwnerIdentity: fixture.request.RequesterIdentity,
			},
		}},
		nil,
		networkplan.PlatformMacOS,
		networkResolverSetupTestClock{now: completedAt},
	)
	request := NetworkResolverPolicyMigrationConfirmRequest{
		OperationID:               completed.Operation.ID,
		ExpectedOperationRevision: 3,
		RequesterIdentity:         fixture.request.RequesterIdentity,
		ResolverEvidence: helper.ResolverMutationEvidence{
			Postcondition: helper.ResolverPostconditionOwnedAbsent,
		},
	}

	got, err := coordinator.Confirm(t.Context(), request)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if !reflect.DeepEqual(got, replayed) || store.replayCalls != 1 || store.completeCalls != 0 {
		t.Fatalf("Confirm() = %#v, replay calls %d, completion calls %d", got, store.replayCalls, store.completeCalls)
	}

	request.RequesterIdentity = "other-owner"
	if _, err := coordinator.Confirm(t.Context(), request); err == nil || !strings.Contains(err.Error(), "authenticated requester") {
		t.Fatalf("Confirm() caller mismatch error = %v", err)
	}
	if store.replayCalls != 1 {
		t.Fatalf("Confirm() caller mismatch replay calls = %d, want 1", store.replayCalls)
	}
}

// networkResolverPolicyMigrationReplayFixture supplies only the dependencies Start uses for an approval replay.
type networkResolverPolicyMigrationReplayFixture struct {
	coordinator *NetworkResolverPolicyMigrationCoordinator
	request     NetworkResolverPolicyMigrationStartRequest
	operation   state.OperationRecord
	store       *networkResolverPolicyMigrationReplayStore
}

// newNetworkResolverPolicyMigrationReplayFixture creates an exact absent legacy resolver state with either schema-two or derived schema-one ownership.
func newNetworkResolverPolicyMigrationReplayFixture(t *testing.T, derived bool) *networkResolverPolicyMigrationReplayFixture {
	t.Helper()
	base := newNetworkResolverSetupTestFixture(t)
	policy, err := networkplan.BuildLegacyMacOS(networkplan.Request{
		Platform:             networkplan.PlatformMacOS,
		InstallationID:       base.network.Ownership.InstallationID,
		Pool:                 base.network.Pool,
		AuthorityFingerprint: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	target := base.source.Record
	target.SchemaVersion = ownership.NetworkPolicySchemaVersion
	target.NetworkPolicyFingerprint = policyFingerprint
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	started := base.now.Add(-time.Minute)
	op := domain.Operation{
		ID:          "migration-operation",
		IntentID:    "migration-intent",
		Kind:        domain.OperationKindNetworkResolverPolicyMigration,
		State:       domain.OperationRequiresApproval,
		Phase:       string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval),
		RequestedAt: base.now.Add(-2 * time.Minute),
		StartedAt:   &started,
	}
	operation := state.OperationRecord{
		Operation: op,
		Revision:  3,
	}
	replacement := policy
	replacement.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
	replacementFingerprint, err := replacement.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	plan := ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposePolicyMigration,
		Operation:                          op,
		OperationRevision:                  operation.Revision,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval,
		Mutation:                           helper.OperationRetireResolver,
		ExpectedSourceOwnershipFingerprint: targetFingerprint,
		ReplacementPolicyFingerprint:       replacementFingerprint,
		TargetOwnership:                    target,
		Policy:                             policy,
	}
	request, err := resolver.NewRequest(target.InstallationID, policy)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := ownership.Observation{
		Exists:      true,
		Record:      target,
		Fingerprint: targetFingerprint,
	}
	if derived {
		confirmed, err = derivedPolicyMigrationOwnership(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	journal := networkResolverPolicyMigrationReplayJournal{operation: operation}
	store := &networkResolverPolicyMigrationReplayStore{operation: operation}
	coordinator := NewNetworkResolverPolicyMigrationCoordinator(
		journal,
		nil,
		networkResolverPolicyMigrationReplayPlans{plan: plan},
		store,
		nil,
		nil,
		networkResolverPolicyMigrationReplayOwnership{observation: confirmed},
		networkResolverPolicyMigrationReplayResolver{observation: resolver.Observation{
			Request:  request,
			Complete: true,
			Rules:    []resolver.RuleFact{},
		}},
		networkplan.PlatformMacOS,
		networkResolverSetupTestClock{now: base.now},
	)
	return &networkResolverPolicyMigrationReplayFixture{
		coordinator: coordinator,
		request: NetworkResolverPolicyMigrationStartRequest{
			OperationID:       op.ID,
			IntentID:          op.IntentID,
			RequesterIdentity: target.OwnerIdentity,
		},
		operation: operation,
		store:     store,
	}
}

// networkResolverPolicyMigrationReplayJournal returns one pending operation by intent.
type networkResolverPolicyMigrationReplayJournal struct {
	operation state.OperationRecord
}

// Operation returns the fixture operation for terminal confirmation replay.
func (journal networkResolverPolicyMigrationReplayJournal) Operation(context.Context, domain.OperationID) (state.OperationRecord, error) {
	return journal.operation, nil
}

// OperationByIntent returns the pending migration operation.
func (journal networkResolverPolicyMigrationReplayJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return journal.operation, nil
}

// StageNetworkResolverPolicyMigration is not used by Start replay.
func (journal networkResolverPolicyMigrationReplayJournal) StageNetworkResolverPolicyMigration(context.Context, state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, nil
}

// networkResolverPolicyMigrationReplayPlans returns the committed migration plan.
type networkResolverPolicyMigrationReplayPlans struct {
	plan ticketissuer.ResolverPlan
}

// networkResolverPolicyMigrationTestJournal scripts one migration operation read and stale-stage staging boundary.
type networkResolverPolicyMigrationTestJournal struct {
	operation func(context.Context, domain.OperationID) (state.OperationRecord, error)
	byIntent  func(context.Context, domain.IntentID) (state.OperationRecord, error)
	stage     func(context.Context, state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error)
}

// Operation returns the fixture operation by operation ID.
func (journal *networkResolverPolicyMigrationTestJournal) Operation(ctx context.Context, id domain.OperationID) (state.OperationRecord, error) {
	return journal.operation(ctx, id)
}

// OperationByIntent returns the fixture operation by intent.
func (journal *networkResolverPolicyMigrationTestJournal) OperationByIntent(ctx context.Context, id domain.IntentID) (state.OperationRecord, error) {
	return journal.byIntent(ctx, id)
}

// StageNetworkResolverPolicyMigration returns the fixture staged operation.
func (journal *networkResolverPolicyMigrationTestJournal) StageNetworkResolverPolicyMigration(ctx context.Context, request state.StageNetworkResolverPolicyMigrationRequest) (state.OperationRecord, error) {
	return journal.stage(ctx, request)
}

// networkResolverPolicyMigrationTestApproval maps staged migration operation inputs to an approval lifecycle.
func networkResolverPolicyMigrationTestApproval(operation domain.Operation, revision domain.Sequence) state.OperationRecord {
	requestedAt := operation.RequestedAt
	operation.State = domain.OperationRequiresApproval
	operation.Phase = string(ticketissuer.ResolverCheckpointPhasePolicyMigrationApproval)
	operation.StartedAt = &requestedAt
	return state.OperationRecord{Operation: operation, Revision: revision}
}

// Resolve returns the fixture plan.
func (plans networkResolverPolicyMigrationReplayPlans) Resolve(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
	return plans.plan, nil
}

// networkResolverPolicyMigrationReplayStore supplies the otherwise unused completion dependency.
type networkResolverPolicyMigrationReplayStore struct {
	operation     state.OperationRecord
	replay        state.CompleteNetworkResolverPolicyMigrationResult
	clear         func(context.Context, state.ClearResolverStageNativeTCPEndpointsRequest) (state.NetworkMutationResult, error)
	completeCalls int
	replayCalls   int
}

// ClearResolverStageNativeTCPEndpoints delegates stopped-project endpoint retirement when a start fixture needs it.
func (store *networkResolverPolicyMigrationReplayStore) ClearResolverStageNativeTCPEndpoints(
	ctx context.Context,
	request state.ClearResolverStageNativeTCPEndpointsRequest,
) (state.NetworkMutationResult, error) {
	return store.clear(ctx, request)
}

// CompleteNetworkResolverPolicyMigration records unexpected terminal replay completion calls.
func (store *networkResolverPolicyMigrationReplayStore) CompleteNetworkResolverPolicyMigration(context.Context, state.CompleteNetworkResolverPolicyMigrationRequest) (state.CompleteNetworkResolverPolicyMigrationResult, error) {
	store.completeCalls++
	return state.CompleteNetworkResolverPolicyMigrationResult{}, nil
}

// ReplayNetworkResolverPolicyMigration returns the configured durable terminal result.
func (store *networkResolverPolicyMigrationReplayStore) ReplayNetworkResolverPolicyMigration(context.Context, domain.OperationID, domain.Sequence) (state.CompleteNetworkResolverPolicyMigrationResult, error) {
	store.replayCalls++
	return store.replay, nil
}

// networkResolverPolicyMigrationReplayOwnership returns the durable ownership projection.
type networkResolverPolicyMigrationReplayOwnership struct {
	observation ownership.Observation
}

// Observe returns the fixture projected ownership fact.
func (observer networkResolverPolicyMigrationReplayOwnership) Observe(context.Context) (ownership.Observation, error) {
	return observer.observation, nil
}

// networkResolverPolicyMigrationReplayResolver returns complete absent native resolver facts.
type networkResolverPolicyMigrationReplayResolver struct {
	observation resolver.Observation
}

// Observe returns the fixture resolver observation.
func (observer networkResolverPolicyMigrationReplayResolver) Observe(context.Context, resolver.Request) (resolver.Observation, error) {
	return observer.observation, nil
}

// TestValidatePolicyMigrationPlanRejectsDifferentMutation keeps setup and release authority from being reused for retirement.
func TestValidatePolicyMigrationPlanRejectsDifferentMutation(t *testing.T) {
	plan := ticketissuer.ResolverPlan{
		Purpose:   ticketissuer.ResolverPlanPurposePolicyMigration,
		Mutation:  helper.OperationReleaseResolver,
		Operation: domain.Operation{ID: "migration-operation"},
	}
	err := validatePolicyMigrationPlan(plan, "migration-operation", 1)
	if err == nil || !strings.Contains(err.Error(), "requested policy migration retirement") {
		t.Fatalf("validatePolicyMigrationPlan() error = %v, want retirement rejection", err)
	}
}
