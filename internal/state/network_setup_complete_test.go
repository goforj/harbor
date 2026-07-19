package state

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/migrations"
)

var networkSetupCompletionOwnershipProjectionMigrations = []string{
	"2026_07_19_140000_create_machine_ownership_projections",
	"2026_07_19_150000_add_machine_ownership_network_policy_fingerprint",
}

// TestStoreCompletesNetworkSetupAtomically proves plan retirement, identity initialization, and terminal history share one ordering boundary.
func TestStoreCompletesNetworkSetupAtomically(t *testing.T) {
	fixture := newNetworkSetupCompletionFixture(t)
	store := networkSetupCompletionStore(fixture)
	stagedRequest := networkSetupStageRequest(t, "operation-network-complete", "intent-network-complete")
	approval, err := fixture.journal.StageNetworkSetup(context.Background(), stagedRequest)
	if err != nil {
		t.Fatalf("StageNetworkSetup() error = %v", err)
	}
	request := networkSetupCompletionRequest(t, approval, stagedRequest.Ownership)

	result, err := store.CompleteNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkSetup() error = %v", err)
	}
	if result.Network.Replayed || result.Operation.Revision != 6 ||
		result.Operation.Operation.State != domain.OperationSucceeded ||
		result.Operation.Operation.Phase != networkSetupCompletionSucceededPhase ||
		result.Network.Record.Revision != 5 || result.Network.Record.Stage != NetworkStageIdentity {
		t.Fatalf("CompleteNetworkSetup() = %#v, want operation 6 around identity revision 5", result)
	}
	if result.Network.Record.Ownership.InstallationID != "installation-stage" ||
		result.Network.Record.Ownership.Generation != 1 ||
		result.Network.Record.Pool.Prefix() != netip.MustParsePrefix("127.88.0.8/29") ||
		result.Network.Record.Pool.Capacity() != networkSetupPoolIdentityCount {
		t.Fatalf("completed identity foundation = %#v", result.Network.Record)
	}

	history, err := fixture.journal.Transitions(context.Background(), approval.Operation.ID)
	if err != nil {
		t.Fatalf("Transitions() error = %v", err)
	}
	wantStates := []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
		domain.OperationRunning,
		domain.OperationSucceeded,
	}
	wantRevisions := []domain.Sequence{1, 2, 3, 4, 6}
	if len(history) != len(wantStates) {
		t.Fatalf("completed transition count = %d, want %d", len(history), len(wantStates))
	}
	for index := range history {
		if history[index].State != wantStates[index] || history[index].Sequence != wantRevisions[index] {
			t.Fatalf("completed transition %d = %#v", index+1, history[index])
		}
	}

	assertNetworkSetupStageDurableState(t, fixture, 6, 1, 5, 0)
	for table, want := range map[string]int64{
		"machine_ownership_projections": 1,
		"network_state":                 1,
		"network_pool_candidates":       networkSetupPoolIdentityCount,
		"network_setup_evidence":        2,
	} {
		if got := networkInitializeTestCount(t, fixture.database, table); got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
	var proofs []models.NetworkSetupEvidence
	if err := fixture.database.Order("component ASC").Find(&proofs).Error; err != nil {
		t.Fatalf("read completed network setup proofs: %v", err)
	}
	if len(proofs) != 2 || proofs[0].Component != string(NetworkSetupComponentLoopbackPool) ||
		!strings.HasPrefix(proofs[0].Evidence, networkSetupPoolEvidencePrefix) ||
		proofs[1].Component != string(NetworkSetupComponentMachineOwnership) ||
		!strings.HasPrefix(proofs[1].Evidence, networkSetupOwnershipEvidencePrefix) {
		t.Fatalf("completed network setup proofs = %#v", proofs)
	}
}

// TestStoreReplaysExactNetworkSetupCompletionAfterRestart proves a committed completion does not require its deleted plan to replay.
func TestStoreReplaysExactNetworkSetupCompletionAfterRestart(t *testing.T) {
	fixture := newNetworkSetupCompletionFixture(t)
	store := networkSetupCompletionStore(fixture)
	stagedRequest := networkSetupStageRequest(t, "operation-network-replay-completion", "intent-network-replay-completion")
	approval, err := fixture.journal.StageNetworkSetup(context.Background(), stagedRequest)
	if err != nil {
		t.Fatalf("StageNetworkSetup() error = %v", err)
	}
	request := networkSetupCompletionRequest(t, approval, stagedRequest.Ownership)
	first, err := store.CompleteNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("first CompleteNetworkSetup() error = %v", err)
	}
	fixture.restart(t)
	store = networkSetupCompletionStore(fixture)

	replayed, err := store.CompleteNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed CompleteNetworkSetup() error = %v", err)
	}
	if !replayed.Network.Replayed || replayed.Operation.Revision != first.Operation.Revision ||
		replayed.Network.Record.Revision != first.Network.Record.Revision ||
		replayed.Operation.Operation.ID != first.Operation.Operation.ID {
		t.Fatalf("replayed CompleteNetworkSetup() = %#v, want original completion", replayed)
	}
	assertNetworkSetupStageDurableState(t, fixture, 6, 1, 5, 0)
	if got := networkInitializeTestCount(t, fixture.database, "network_state"); got != 1 {
		t.Fatalf("network state count after replay = %d, want 1", got)
	}
	if got := networkInitializeTestCount(t, fixture.database, "machine_ownership_projections"); got != 1 {
		t.Fatalf("machine ownership projection count after replay = %d, want 1", got)
	}
}

// TestStoreRejectsNetworkSetupCompletionReplayProjectionConflict proves terminal replay remains bound to helper-confirmed authority.
func TestStoreRejectsNetworkSetupCompletionReplayProjectionConflict(t *testing.T) {
	fixture, store, request := stagedNetworkSetupCompletionFixture(t)
	if _, err := store.CompleteNetworkSetup(context.Background(), request); err != nil {
		t.Fatalf("CompleteNetworkSetup() error = %v", err)
	}
	networkSetupStageExec(
		t,
		fixture.database,
		"UPDATE machine_ownership_projections SET confirmed_at = ? WHERE id = 1",
		request.At.Add(time.Second),
	)

	_, err := store.CompleteNetworkSetup(context.Background(), request)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "projection differs") {
		t.Fatalf("replay projection conflict error = %v, want CorruptStateError", err)
	}
}

// TestStoreRejectsNetworkSetupCompletionConflicts verifies operation, plan, network, and correlated proof mismatches fail before mutation.
func TestStoreRejectsNetworkSetupCompletionConflicts(t *testing.T) {
	t.Run("stale revision", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		request.ExpectedOperationRevision--
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		var stale *StaleRevisionError
		if !errors.As(err, &stale) {
			t.Fatalf("stale completion error = %v, want StaleRevisionError", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("ownership plan", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		request.ConfirmedOwnership.Record.InstallationID = "installation-other"
		request.ConfirmedOwnership.Fingerprint = networkSetupCompletionOwnershipFingerprint(t, request.ConfirmedOwnership.Record)
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "does not match the durable plan") {
			t.Fatalf("ownership-plan conflict error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("missing plan", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		networkSetupStageExec(t, fixture.database, "DELETE FROM network_setup_plans")
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "singleton plan is missing") {
			t.Fatalf("missing-plan error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 0)
	})

	t.Run("wrong operation kind", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		networkSetupStageExec(t, fixture.database, "UPDATE operations SET kind = 'host.setup'")
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "not an active global network setup") {
			t.Fatalf("operation-kind error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("wrong operation state", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		networkSetupStageExec(t, fixture.database, "UPDATE operations SET state = 'running', phase = 'running'")
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "state does not match latest transition") {
			t.Fatalf("operation-state error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("staging history", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		networkSetupStageExec(t, fixture.database, "UPDATE operation_transitions SET phase = 'changed' WHERE ordinal = 2")
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "staged lifecycle") {
			t.Fatalf("staging-history error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})

	t.Run("network state", func(t *testing.T) {
		fixture, store, request := stagedNetworkSetupCompletionFixture(t)
		at := request.At
		networkSetupStageExec(t, fixture.database, `INSERT INTO network_state
			(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length,
			dns_suffix, created_at, updated_at, revision)
			VALUES (1, 'identity', 'installation-existing', 1, '127.90.0.8', 29, '.test', ?, ?, 3)`, at, at)
		_, err := store.CompleteNetworkSetup(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "network state already exists") {
			t.Fatalf("network-state conflict error = %v", err)
		}
		assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	})
}

// TestStoreNetworkSetupCompletionRollsBackLateFailure proves the final transition failure restores plan, history, sequence, and network state.
func TestStoreNetworkSetupCompletionRollsBackLateFailure(t *testing.T) {
	fixture, store, request := stagedNetworkSetupCompletionFixture(t)
	networkSetupStageExec(t, fixture.database, `CREATE TRIGGER fail_network_setup_success
		BEFORE INSERT ON operation_transitions
		WHEN NEW.operation_id = 'operation-network-completion-fixture' AND NEW.state = 'succeeded'
		BEGIN
			SELECT RAISE(ABORT, 'forced network setup success failure');
		END`)

	_, err := store.CompleteNetworkSetup(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "forced network setup success failure") {
		t.Fatalf("late completion failure error = %v", err)
	}
	assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	for _, table := range networkTableNames() {
		if got := networkInitializeTestCount(t, fixture.database, table); got != 0 {
			t.Fatalf("%s count after rollback = %d, want 0", table, got)
		}
	}
	if got := networkInitializeTestCount(t, fixture.database, "machine_ownership_projections"); got != 0 {
		t.Fatalf("machine ownership projection count after rollback = %d, want 0", got)
	}
	persisted, err := fixture.journal.Operation(context.Background(), request.OperationID)
	if err != nil {
		t.Fatalf("read rolled-back setup operation: %v", err)
	}
	if persisted.Revision != 3 || persisted.Operation.State != domain.OperationRequiresApproval {
		t.Fatalf("rolled-back setup operation = %#v", persisted)
	}
}

// TestStoreDirectNetworkInitializationCannotBypassSetupPlan verifies both public initialization paths preserve staged approval authority.
func TestStoreDirectNetworkInitializationCannotBypassSetupPlan(t *testing.T) {
	fixture, store, request := stagedNetworkSetupCompletionFixture(t)
	identityRequest, err := networkIdentityRequestFromSetupCompletion(request)
	if err != nil {
		t.Fatalf("derive identity request: %v", err)
	}
	if _, err := store.InitializeNetworkIdentity(context.Background(), identityRequest); err == nil ||
		!strings.Contains(err.Error(), "blocked by staged setup operation") {
		t.Fatalf("InitializeNetworkIdentity() bypass error = %v", err)
	}
	if _, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest()); err == nil ||
		!strings.Contains(err.Error(), "blocked by staged setup operation") {
		t.Fatalf("InitializeNetwork() bypass error = %v", err)
	}
	assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
	if got := networkInitializeTestCount(t, fixture.database, "network_state"); got != 0 {
		t.Fatalf("network state count after direct bypass attempts = %d, want 0", got)
	}
}

// TestCompleteNetworkSetupResultValidation covers the public terminal aggregate contract independently of storage.
func TestCompleteNetworkSetupResultValidation(t *testing.T) {
	_, store, request := stagedNetworkSetupCompletionFixture(t)
	valid, err := store.CompleteNetworkSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkSetup() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*CompleteNetworkSetupResult)
		want   string
	}{
		{name: "operation", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Operation.ID = ""
		}, want: "operation ID"},
		{name: "kind", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Operation.Kind = domain.OperationKindProjectStart
			result.Operation.Operation.ProjectID = "project-other"
		}, want: "must be global kind"},
		{name: "project", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Operation.ProjectID = "project-other"
		}, want: "must not identify a project"},
		{name: "state", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Operation.State = domain.OperationRunning
			result.Operation.Operation.FinishedAt = nil
		}, want: "operation state"},
		{name: "phase", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Operation.Phase = networkSetupCompletionRunningPhase
		}, want: "operation phase"},
		{name: "network", mutate: func(result *CompleteNetworkSetupResult) {
			result.Network.Record.Ownership.InstallationID = ""
		}, want: "installation ID"},
		{name: "stage", mutate: func(result *CompleteNetworkSetupResult) {
			result.Network = NetworkMutationResult{
				Record: networkInitializationProjection(networkMutationTestInitializeRequest(), 1),
			}
		}, want: "identity network stage"},
		{name: "revision", mutate: func(result *CompleteNetworkSetupResult) {
			result.Operation.Revision = result.Network.Record.Revision + 2
		}, want: "not contiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.mutate(&result)
			if err := result.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestCompleteNetworkSetupRequestValidationAndCancellation covers every independent proof boundary before mutation.
func TestCompleteNetworkSetupRequestValidationAndCancellation(t *testing.T) {
	fixture, store, valid := stagedNetworkSetupCompletionFixture(t)
	tests := []struct {
		name   string
		mutate func(*CompleteNetworkSetupRequest)
		want   string
	}{
		{name: "operation", mutate: func(request *CompleteNetworkSetupRequest) {
			request.OperationID = ""
		}, want: "operation ID"},
		{name: "revision", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ExpectedOperationRevision = 0
		}, want: "revision must be positive"},
		{name: "time", mutate: func(request *CompleteNetworkSetupRequest) {
			request.At = time.Time{}
		}, want: "time must not be zero"},
		{name: "ownership missing", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ConfirmedOwnership.Exists = false
		}, want: "ownership is missing"},
		{name: "ownership record", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ConfirmedOwnership.Record.InstallationID = ""
		}, want: "ownership record"},
		{name: "ownership generation", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ConfirmedOwnership.Record.Generation = 2
		}, want: "generation is 2"},
		{name: "ownership fingerprint", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ConfirmedOwnership.Fingerprint = strings.Repeat("0", 64)
		}, want: "fingerprint does not match"},
		{name: "ownership pool shape", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ConfirmedOwnership.Record.LoopbackPoolPrefix = "127.88.0.8/30"
			request.ConfirmedOwnership.Fingerprint = networkSetupCompletionOwnershipFingerprint(t, request.ConfirmedOwnership.Record)
		}, want: "not a canonical IPv4-loopback /29"},
		{name: "helper pool", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Pool = "127.88.0.16/29"
		}, want: "pool evidence does not match"},
		{name: "helper identity count", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Identities = request.HelperPoolEvidence.Identities[:7]
		}, want: "contains 7 identities"},
		{name: "helper address", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Identities[2].Address = "127.88.0.15"
		}, want: "does not match address"},
		{name: "helper state", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Identities[2].Observation.State = helper.ObservationAbsent
		}, want: "is not owned"},
		{name: "helper fingerprint", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Identities[2].Observation.Fingerprint = "invalid"
		}, want: "fingerprint is invalid"},
		{name: "daemon identity count", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ObservedPool = request.ObservedPool[:7]
		}, want: "contain 7 identities"},
		{name: "daemon address", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ObservedPool[2].Address = netip.MustParseAddr("127.88.0.15")
		}, want: "does not match address"},
		{name: "daemon state", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ObservedPool[2] = networkSetupCompletionObservation(netip.MustParseAddr("127.88.0.10"), loopback.StateAbsent)
		}, want: "state is"},
		{name: "daemon facts", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ObservedPool[2].Assignments[0].PrefixLength = 31
		}, want: "fingerprint daemon"},
		{name: "daemon fingerprint", mutate: func(request *CompleteNetworkSetupRequest) {
			request.HelperPoolEvidence.Identities[2].Observation.Fingerprint = strings.Repeat("0", 64)
		}, want: "helper and daemon"},
		{name: "daemon interface", mutate: func(request *CompleteNetworkSetupRequest) {
			request.ObservedPool[2].Loopback.Name = "lo-other"
			request.ObservedPool[2].Assignments[0].InterfaceName = "lo-other"
			request.ObservedPool[2].Assignments[0].Linux.Label = "lo-other"
			fingerprint, _ := request.ObservedPool[2].Fingerprint()
			request.HelperPoolEvidence.Identities[2].Observation.Fingerprint = fingerprint
		}, want: "do not share"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneCompleteNetworkSetupRequest(valid)
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CompleteNetworkSetup(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CompleteNetworkSetup() error = %v, want context.Canceled", err)
	}
	assertNetworkSetupStageDurableState(t, fixture, 3, 1, 3, 1)
}

// stagedNetworkSetupCompletionFixture stages one plan and returns its matching store and completion proof.
func stagedNetworkSetupCompletionFixture(
	t *testing.T,
) (*networkSetupStageFixture, *Store, CompleteNetworkSetupRequest) {
	t.Helper()
	fixture := newNetworkSetupCompletionFixture(t)
	store := networkSetupCompletionStore(fixture)
	stagedRequest := networkSetupStageRequest(t, "operation-network-completion-fixture", "intent-network-completion-fixture")
	approval, err := fixture.journal.StageNetworkSetup(context.Background(), stagedRequest)
	if err != nil {
		t.Fatalf("stage network setup completion fixture: %v", err)
	}
	return fixture, store, networkSetupCompletionRequest(t, approval, stagedRequest.Ownership)
}

// newNetworkSetupCompletionFixture extends the staging schema with the projection committed at setup completion.
func newNetworkSetupCompletionFixture(t *testing.T) *networkSetupStageFixture {
	t.Helper()
	fixture := newNetworkSetupStageFixture(t)
	for _, name := range networkSetupCompletionOwnershipProjectionMigrations {
		found := false
		for _, migration := range migrations.GetMigrations() {
			if migration.App() != "harbord" || migration.Connection() != "default" || migration.Name() != name {
				continue
			}
			if err := migration.Up(fixture.database); err != nil {
				t.Fatalf("apply network setup completion migration %s: %v", migration.Name(), err)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("network setup completion migration %q is not registered", name)
		}
	}
	return fixture
}

// networkSetupCompletionStore constructs the aggregate store with the journal's exact mutation coordinator.
func networkSetupCompletionStore(fixture *networkSetupStageFixture) *Store {
	return NewStore(
		models.NewHarborStateRepo(fixture.connections),
		models.NewProjectRepo(fixture.connections),
		models.NewProjectSessionRepo(fixture.connections),
		models.NewNetworkStateRepo(fixture.connections),
		fixture.journal.mutations,
	)
}

// networkSetupCompletionRequest builds matching confirmed ownership, helper evidence, and fresh daemon observations.
func networkSetupCompletionRequest(
	t *testing.T,
	approval OperationRecord,
	record ownership.Record,
) CompleteNetworkSetupRequest {
	t.Helper()
	pool, err := networkSetupIdentityPool(record.LoopbackPoolPrefix)
	if err != nil {
		t.Fatalf("construct completion pool: %v", err)
	}
	observations := make([]loopback.Observation, 0, pool.Capacity())
	evidence := make([]helper.MutationEvidence, 0, pool.Capacity())
	for _, address := range pool.Candidates() {
		observation := networkSetupCompletionObservation(address, loopback.StateExact)
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint completion observation %s: %v", address, err)
		}
		observations = append(observations, observation)
		evidence = append(evidence, helper.MutationEvidence{
			Changed: true,
			Address: address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: fingerprint,
			},
		})
	}
	return CompleteNetworkSetupRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ConfirmedOwnership: ownership.Observation{
			Exists:      true,
			Record:      record,
			Fingerprint: networkSetupCompletionOwnershipFingerprint(t, record),
		},
		HelperPoolEvidence: helper.PoolMutationEvidence{
			Pool:       pool.Prefix().String(),
			Identities: evidence,
		},
		ObservedPool: observations,
		At:           networkSetupStageTime().Add(time.Minute),
	}
}

// networkSetupCompletionOwnershipFingerprint returns the validated full-record digest used as confirmed helper evidence.
func networkSetupCompletionOwnershipFingerprint(t *testing.T, record ownership.Record) string {
	t.Helper()
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint completion ownership: %v", err)
	}
	return fingerprint
}

// networkSetupCompletionObservation returns one canonical Linux absent or exact daemon-side observation.
func networkSetupCompletionObservation(address netip.Addr, state loopback.State) loopback.Observation {
	observation := loopback.Observation{
		Address: address,
		Loopback: loopback.InterfaceFact{
			Name: "lo", Index: 1, Kind: loopback.InterfaceKindLinuxNative, NativeLoopback: true,
		},
		State:       loopback.StateAbsent,
		Assignments: []loopback.AssignmentFact{},
	}
	if state == loopback.StateExact {
		observation.State = loopback.StateExact
		observation.Assignments = []loopback.AssignmentFact{{
			Address: address, PrefixLength: 32, InterfaceName: "lo", InterfaceIndex: 1,
			NativeLoopback: true, InterfaceKind: loopback.InterfaceKindLinuxNative,
			Linux: &loopback.LinuxAssignmentFact{
				Scope: loopback.LinuxAddressScopeHost, Flags: 1 << 7, Label: "lo", AddressMatchesLocal: true,
				CacheInfoPresent: true, ValidLifetimeSeconds: ^uint32(0), PreferredLifetimeSeconds: ^uint32(0),
			},
		}}
	}
	return observation
}
