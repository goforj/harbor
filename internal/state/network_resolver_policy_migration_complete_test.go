package state

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/resolver"
	"gorm.io/gorm"
)

// TestNetworkResolverPolicyMigrationRetirementReplayCrossesDurableState proves a lost helper response leaves the schema-two plan replayable until daemon confirmation atomically records schema-one retirement.
func TestNetworkResolverPolicyMigrationRetirementReplayCrossesDurableState(t *testing.T) {
	fixture, approval, confirmation := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	store := networkResolverPolicyMigrationCompletionStore(fixture)

	plan, err := fixture.source.Resolve(t.Context(), resolverPlanRequest(approval))
	if err != nil {
		t.Fatalf("Resolve() before helper retirement error = %v", err)
	}
	if plan.Mutation != helper.OperationRetireResolver ||
		plan.TargetOwnership != fixture.request.SourceOwnership.Record ||
		plan.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		t.Fatalf("schema-two retirement plan = %#v", plan)
	}

	// A helper can retire protected ownership before its response reaches the daemon, so the daemon must retain the schema-two plan until confirmation.
	before := networkDataPlaneActivationTestProjection(t, fixture.database)
	if before.observation != fixture.request.SourceOwnership ||
		confirmation.ConfirmedOwnership.Record.SchemaVersion != ownership.IdentitySchemaVersion {
		t.Fatalf("lost-response ownership facts = projection %#v, helper %#v", before.observation, confirmation.ConfirmedOwnership)
	}
	if _, err := fixture.source.Resolve(t.Context(), resolverPlanRequest(approval)); err != nil {
		t.Fatalf("Resolve() during lost helper response error = %v", err)
	}

	completed, err := store.CompleteNetworkResolverPolicyMigration(t.Context(), confirmation)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverPolicyMigration() error = %v", err)
	}
	assertNetworkResolverPolicyMigrationCompleted(t, fixture, approval, confirmation, completed)
}

// TestStoreCompletesNetworkResolverPolicyMigrationWithHelperEvidence retires only the resolver-bound authority.
func TestStoreCompletesNetworkResolverPolicyMigrationWithHelperEvidence(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	store := networkResolverPolicyMigrationCompletionStore(fixture)

	result, err := store.CompleteNetworkResolverPolicyMigration(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverPolicyMigration() error = %v", err)
	}
	if result.Network.Replayed || result.Operation.Operation.State != domain.OperationSucceeded ||
		result.Operation.Operation.Phase != networkResolverPolicyMigrationCompletionSucceededPhase ||
		result.Network.Record.Stage != NetworkStageIdentity || result.Operation.Revision != result.NetworkRevision+1 {
		t.Fatalf("CompleteNetworkResolverPolicyMigration() = %#v", result)
	}
	if !result.Operation.Operation.FinishedAt.Equal(request.At) || !result.Network.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("completion times = operation %v, network %v, want %v", result.Operation.Operation.FinishedAt, result.Network.Record.UpdatedAt, request.At)
	}
	assertNetworkResolverPolicyMigrationCompleted(t, fixture, approval, request, result)

	replayed, err := store.ReplayNetworkResolverPolicyMigration(context.Background(), request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		t.Fatalf("ReplayNetworkResolverPolicyMigration() error = %v", err)
	}
	if !replayed.Network.Replayed ||
		!reflect.DeepEqual(replayed.Operation, result.Operation) ||
		replayed.NetworkRevision != result.NetworkRevision ||
		!reflect.DeepEqual(replayed.Network.Record, result.Network.Record) {
		t.Fatalf("ReplayNetworkResolverPolicyMigration() = %#v, want durable terminal result for %#v", replayed, result)
	}
	if _, err := store.ReplayNetworkResolverPolicyMigration(context.Background(), request.OperationID, request.ExpectedOperationRevision+1); err == nil {
		t.Fatal("ReplayNetworkResolverPolicyMigration() stale revision error = nil")
	}
}

// TestNetworkResolverPolicyMigrationRequestsValidateRejectsInvalidAdmissionFacts verifies completion and recovery share selection and post-state fences while only completion accepts helper evidence.
func TestNetworkResolverPolicyMigrationRequestsValidateRejectsInvalidAdmissionFacts(t *testing.T) {
	for _, test := range []struct {
		name                string
		mutate              func(*CompleteNetworkResolverPolicyMigrationRequest, *RecoverNetworkResolverPolicyMigrationRequest, *networkResolverPolicyMigrationFixture)
		wantCompleteFailure bool
		wantRecoverFailure  bool
	}{
		{
			name: "operation selection",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				completion.OperationID = ""
				recovery.OperationID = ""
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
		{
			name: "operation revision selection",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				completion.ExpectedOperationRevision = 0
				recovery.ExpectedOperationRevision = 0
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
		{
			name: "completion time selection",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				completion.At = time.Time{}
				recovery.At = time.Time{}
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
		{
			name: "helper postcondition evidence",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				completion.ResolverEvidence.Postcondition = helper.ResolverPostconditionExact
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  false,
		},
		{
			name: "helper fingerprint evidence",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				completion.ResolverEvidence.PolicyFingerprint = strings.Repeat("A", 64)
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  false,
		},
		{
			name: "legacy resolver poststate",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				observed := networkResolverSetupCompletionObservation(t, fixture.request.SourceOwnership.Record.InstallationID, fixture.request.Policy)
				completion.ObservedResolver = observed
				recovery.ObservedResolver = observed
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
		{
			name: "foreign resolver poststate",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				observed := networkResolverSetupCompletionObservation(t, fixture.request.SourceOwnership.Record.InstallationID, fixture.request.Policy)
				observed.Rules = append([]resolver.RuleFact(nil), observed.Rules...)
				observed.Rules[0].Owner = nil
				completion.ObservedResolver = observed
				recovery.ObservedResolver = observed
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
		{
			name: "schema two ownership poststate",
			mutate: func(completion *CompleteNetworkResolverPolicyMigrationRequest, recovery *RecoverNetworkResolverPolicyMigrationRequest, fixture *networkResolverPolicyMigrationFixture) {
				confirmed := completion.ConfirmedOwnership
				confirmed.Record.SchemaVersion = ownership.NetworkPolicySchemaVersion
				confirmed.Record.NetworkPolicyFingerprint = fixture.request.SourceOwnership.Record.NetworkPolicyFingerprint
				fingerprint, err := confirmed.Record.Fingerprint()
				if err != nil {
					t.Fatalf("fingerprint schema-two ownership: %v", err)
				}
				confirmed.Fingerprint = fingerprint
				completion.ConfirmedOwnership = confirmed
				recovery.ConfirmedOwnership = confirmed
			},
			wantCompleteFailure: true,
			wantRecoverFailure:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, completion := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
			recovery := RecoverNetworkResolverPolicyMigrationRequest{
				OperationID:               completion.OperationID,
				ExpectedOperationRevision: completion.ExpectedOperationRevision,
				ObservedResolver:          completion.ObservedResolver,
				ConfirmedOwnership:        completion.ConfirmedOwnership,
				At:                        completion.At,
			}
			test.mutate(&completion, &recovery, fixture)
			if gotFailure := completion.Validate() != nil; gotFailure != test.wantCompleteFailure {
				t.Fatalf("CompleteNetworkResolverPolicyMigrationRequest.Validate() failure = %t, want %t", gotFailure, test.wantCompleteFailure)
			}
			if gotFailure := recovery.Validate() != nil; gotFailure != test.wantRecoverFailure {
				t.Fatalf("RecoverNetworkResolverPolicyMigrationRequest.Validate() failure = %t, want %t", gotFailure, test.wantRecoverFailure)
			}
		})
	}
}

// TestCompleteNetworkResolverPolicyMigrationResultValidateRejectsInconsistentTerminalFacts verifies callers cannot publish a non-contiguous terminal result.
func TestCompleteNetworkResolverPolicyMigrationResultValidateRejectsInconsistentTerminalFacts(t *testing.T) {
	fixture, _, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	result, err := networkResolverPolicyMigrationCompletionStore(fixture).CompleteNetworkResolverPolicyMigration(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverPolicyMigration() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*CompleteNetworkResolverPolicyMigrationResult)
	}{
		{
			name: "operation kind",
			mutate: func(result *CompleteNetworkResolverPolicyMigrationResult) {
				result.Operation.Operation.Kind = domain.OperationKindNetworkResolverSetup
			},
		},
		{
			name: "operation state",
			mutate: func(result *CompleteNetworkResolverPolicyMigrationResult) {
				result.Operation.Operation.State = domain.OperationRunning
			},
		},
		{
			name: "network revision",
			mutate: func(result *CompleteNetworkResolverPolicyMigrationResult) {
				result.NetworkRevision++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := result
			test.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("CompleteNetworkResolverPolicyMigrationResult.Validate() unexpectedly succeeded")
			}
		})
	}
}

// TestRecoverNetworkResolverPolicyMigrationRejectsSchemaTwoAbsentState keeps the staged helper capability reissuable.
func TestRecoverNetworkResolverPolicyMigrationRejectsSchemaTwoAbsentState(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	store := networkResolverPolicyMigrationCompletionStore(fixture)
	beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
	beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)

	recovery := RecoverNetworkResolverPolicyMigrationRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ObservedResolver:          request.ObservedResolver,
		ConfirmedOwnership:        fixture.request.SourceOwnership,
		At:                        request.At,
	}
	if _, err := store.RecoverNetworkResolverPolicyMigration(context.Background(), recovery); err == nil || !strings.Contains(err.Error(), "not schema one") {
		t.Fatalf("RecoverNetworkResolverPolicyMigration() error = %v, want schema-one rejection", err)
	}
	if _, err := fixture.source.Resolve(context.Background(), resolverPlanRequest(approval)); err != nil {
		t.Fatalf("Resolve() after rejected recovery error = %v", err)
	}
	if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatal("schema-two absent recovery changed network rows")
	}
	if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
		t.Fatal("schema-two absent recovery changed ownership projection")
	}
}

// TestStoreRecoversNetworkResolverPolicyMigrationWithSchemaOneAbsence finalizes a lost helper response from independent facts.
func TestStoreRecoversNetworkResolverPolicyMigrationWithSchemaOneAbsence(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	store := networkResolverPolicyMigrationCompletionStore(fixture)
	recovery := RecoverNetworkResolverPolicyMigrationRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ObservedResolver:          request.ObservedResolver,
		ConfirmedOwnership:        request.ConfirmedOwnership,
		At:                        request.At,
	}

	result, err := store.RecoverNetworkResolverPolicyMigration(context.Background(), recovery)
	if err != nil {
		t.Fatalf("RecoverNetworkResolverPolicyMigration() error = %v", err)
	}
	if result.Network.Replayed || result.Network.Record.Stage != NetworkStageIdentity {
		t.Fatalf("recovered migration = %#v", result)
	}
	assertNetworkResolverPolicyMigrationCompleted(t, fixture, approval, request, result)
}

// TestRecoverNetworkResolverPolicyMigrationRejectsLegacyAndForeignResolverFacts preserves retry authority on ambiguous observations.
func TestRecoverNetworkResolverPolicyMigrationRejectsLegacyAndForeignResolverFacts(t *testing.T) {
	for _, test := range []struct {
		name     string
		observed resolver.Observation
	}{
		{
			name:     "legacy present",
			observed: resolver.Observation{},
		},
		{
			name:     "foreign resolver",
			observed: resolver.Observation{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, approval, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
			store := networkResolverPolicyMigrationCompletionStore(fixture)
			observed := networkResolverSetupCompletionObservation(t, fixture.request.SourceOwnership.Record.InstallationID, fixture.request.Policy)
			if test.name == "foreign resolver" {
				observed.Rules[0].Owner = nil
			}
			beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
			beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)
			recovery := RecoverNetworkResolverPolicyMigrationRequest{
				OperationID:               request.OperationID,
				ExpectedOperationRevision: request.ExpectedOperationRevision,
				ObservedResolver:          observed,
				ConfirmedOwnership:        request.ConfirmedOwnership,
				At:                        request.At,
			}

			if _, err := store.RecoverNetworkResolverPolicyMigration(context.Background(), recovery); err == nil || !strings.Contains(err.Error(), "not exact owned absence") {
				t.Fatalf("RecoverNetworkResolverPolicyMigration() error = %v, want resolver rejection", err)
			}
			if _, err := fixture.source.Resolve(context.Background(), resolverPlanRequest(approval)); err != nil {
				t.Fatalf("Resolve() after rejected recovery error = %v", err)
			}
			if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatal("rejected recovery changed network rows")
			}
			if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
				t.Fatal("rejected recovery changed ownership projection")
			}
		})
	}
}

// TestStoreReplaysNetworkResolverPolicyMigrationAfterLaterResolverAndFullSetup preserves later network authority.
func TestStoreReplaysNetworkResolverPolicyMigrationAfterLaterResolverAndFullSetup(t *testing.T) {
	fixture, _, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
	store := networkResolverPolicyMigrationCompletionStore(fixture)
	completed, err := store.CompleteNetworkResolverPolicyMigration(context.Background(), request)
	if err != nil {
		t.Fatalf("complete migration: %v", err)
	}
	replacement := fixture.request.Policy
	replacement.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
	target := networkDataPlaneActivationTestOwnership(t, replacement).Record
	_, sourceFingerprint, err := resolverSetupSourceOwnership(target)
	if err != nil {
		t.Fatalf("derive later resolver source: %v", err)
	}
	operation, err := domain.NewOperation("operation-policy-migration-later-resolver", "intent-policy-migration-later-resolver", domain.OperationKindNetworkResolverSetup, "", request.At.Add(time.Minute))
	if err != nil {
		t.Fatalf("create later resolver operation: %v", err)
	}
	staged, err := fixture.journal.StageNetworkResolverSetup(context.Background(), StageNetworkResolverSetupRequest{
		Operation:                          operation,
		ExpectedNetworkRevision:            completed.NetworkRevision,
		ExpectedSourceOwnershipFingerprint: sourceFingerprint,
		TargetOwnership:                    target,
		Policy:                             replacement,
	})
	if err != nil {
		t.Fatalf("stage later resolver setup: %v", err)
	}
	exact := networkResolverSetupCompletionObservation(t, target.InstallationID, replacement)
	exactFingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint later resolver observation: %v", err)
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint later resolver ownership: %v", err)
	}
	laterRequest := CompleteNetworkResolverSetupRequest{
		OperationID:               staged.Operation.ID,
		ExpectedOperationRevision: staged.Revision,
		ResolverEvidence: helper.ResolverMutationEvidence{
			Changed:                true,
			PolicyFingerprint:      target.NetworkPolicyFingerprint,
			OwnershipFingerprint:   targetFingerprint,
			ObservationFingerprint: exactFingerprint,
			Postcondition:          helper.ResolverPostconditionExact,
		},
		ObservedResolver: exact,
		At:               request.At.Add(2 * time.Minute),
	}
	resolverResult, err := store.CompleteNetworkResolverSetup(context.Background(), laterRequest)
	if err != nil {
		t.Fatalf("complete later resolver setup: %v", err)
	}
	proof, err := networkResolverSetupCompletionProof(laterRequest, target.Generation)
	if err != nil {
		t.Fatalf("derive later resolver proof: %v", err)
	}
	initial := networkMutationTestInitializeRequest()
	fullRequest := ActivateNetworkDataPlaneRequest{
		ExpectedNetworkRevision: resolverResult.NetworkRevision,
		ConfirmedOwnership:      networkDataPlaneActivationTestObservation(t, target),
		Policy:                  replacement,
		Setup: []NetworkSetupProof{
			proof,
			initial.Setup[3],
		},
		Listeners: networkDataPlaneActivationTestListeners(replacement, initial),
		At:        request.At.Add(3 * time.Minute),
	}
	fullRequest.Setup[1].VerifiedAt = fullRequest.At
	fullRequest.Listeners.DNS.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTP.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTPS.VerifiedAt = fullRequest.At
	full, err := store.ActivateNetworkDataPlane(context.Background(), fullRequest)
	if err != nil {
		t.Fatalf("activate later full setup: %v", err)
	}

	replayed, err := store.CompleteNetworkResolverPolicyMigration(context.Background(), request)
	if err != nil {
		t.Fatalf("replay after later full setup: %v", err)
	}
	if !replayed.Network.Replayed || replayed.NetworkRevision != completed.NetworkRevision || replayed.Network.Record.Stage != NetworkStageFull || replayed.Network.Record.Revision != full.Record.Revision {
		t.Fatalf("replay after later full setup = %#v", replayed)
	}
}

// TestStoreNetworkResolverPolicyMigrationCompletionRollsBackForcedFailures keeps the staged plan and all authority atomic.
func TestStoreNetworkResolverPolicyMigrationCompletionRollsBackForcedFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{
			name:    "resolver proof",
			trigger: "CREATE TRIGGER fail_policy_migration BEFORE DELETE ON network_setup_evidence WHEN OLD.component = 'resolver' BEGIN SELECT RAISE(ABORT, 'forced policy migration proof failure'); END",
		},
		{
			name:    "network root",
			trigger: "CREATE TRIGGER fail_policy_migration BEFORE UPDATE ON network_state WHEN NEW.stage = 'identity' BEGIN SELECT RAISE(ABORT, 'forced policy migration root failure'); END",
		},
		{
			name:    "ownership projection",
			trigger: "CREATE TRIGGER fail_policy_migration BEFORE UPDATE ON machine_ownership_projections WHEN NEW.ownership_schema_version = 1 BEGIN SELECT RAISE(ABORT, 'forced policy migration ownership failure'); END",
		},
		{
			name:    "succeeded transition",
			trigger: "CREATE TRIGGER fail_policy_migration BEFORE INSERT ON operation_transitions WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'forced policy migration success failure'); END",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, approval, request := stagedNetworkResolverPolicyMigrationCompletionFixture(t)
			store := networkResolverPolicyMigrationCompletionStore(fixture)
			beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
			beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)
			policyMigrationExec(t, fixture.database, test.trigger)

			if _, err := store.CompleteNetworkResolverPolicyMigration(context.Background(), request); err == nil || !strings.Contains(err.Error(), "forced policy migration") {
				t.Fatalf("CompleteNetworkResolverPolicyMigration() error = %v", err)
			}
			if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatal("forced failure changed network rows")
			}
			if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
				t.Fatal("forced failure changed ownership projection")
			}
			if _, err := fixture.source.Resolve(context.Background(), resolverPlanRequest(approval)); err != nil {
				t.Fatalf("Resolve() after forced failure error = %v", err)
			}
		})
	}
}

// stagedNetworkResolverPolicyMigrationCompletionFixture stages one legacy retirement and returns matching helper facts.
func stagedNetworkResolverPolicyMigrationCompletionFixture(t *testing.T) (*networkResolverPolicyMigrationFixture, OperationRecord, CompleteNetworkResolverPolicyMigrationRequest) {
	t.Helper()
	fixture := newNetworkResolverPolicyMigrationFixture(t)
	approval, err := fixture.journal.StageNetworkResolverPolicyMigration(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("stage policy migration completion fixture: %v", err)
	}
	request, err := resolver.NewRequest(fixture.request.SourceOwnership.Record.InstallationID, fixture.request.Policy)
	if err != nil {
		t.Fatalf("construct absent resolver observation request: %v", err)
	}
	observed := resolver.Observation{
		Request:  request,
		Complete: true,
	}
	observationFingerprint, err := observed.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint absent resolver observation: %v", err)
	}
	confirmed := derivedNetworkResolverPolicyMigrationOwnership(fixture.request.SourceOwnership.Record)
	return fixture, approval, CompleteNetworkResolverPolicyMigrationRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence: helper.ResolverMutationEvidence{
			Changed:                true,
			PolicyFingerprint:      fixture.request.SourceOwnership.Record.NetworkPolicyFingerprint,
			OwnershipFingerprint:   confirmed.Fingerprint,
			ObservationFingerprint: observationFingerprint,
			Postcondition:          helper.ResolverPostconditionOwnedAbsent,
		},
		ObservedResolver:   observed,
		ConfirmedOwnership: confirmed,
		At:                 fixture.request.Operation.RequestedAt.Add(time.Minute),
	}
}

// networkResolverPolicyMigrationCompletionStore rebuilds the aggregate over the staged fixture database.
func networkResolverPolicyMigrationCompletionStore(fixture *networkResolverPolicyMigrationFixture) *Store {
	connections := fixture.journal.mutations.connections
	return NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
	)
}

// resolverPlanRequest selects one staged resolver policy migration plan.
func resolverPlanRequest(approval OperationRecord) ticketissuer.ResolverRequest {
	return ticketissuer.ResolverRequest{OperationID: approval.Operation.ID}
}

// assertNetworkResolverPolicyMigrationCompleted verifies the retirement removes the legacy proof and singleton plan.
func assertNetworkResolverPolicyMigrationCompleted(t *testing.T, fixture *networkResolverPolicyMigrationFixture, approval OperationRecord, request CompleteNetworkResolverPolicyMigrationRequest, result CompleteNetworkResolverPolicyMigrationResult) {
	t.Helper()
	if result.Operation.Operation.ID != approval.Operation.ID {
		t.Fatalf("completed operation ID = %q, want %q", result.Operation.Operation.ID, approval.Operation.ID)
	}
	rows := networkDataPlaneActivationTestRows(t, fixture.database)
	for _, proof := range rows.SetupEvidence {
		if proof.Component == string(NetworkSetupComponentResolver) {
			t.Fatalf("completed migration retained resolver proof %#v", proof)
		}
	}
	projection := networkDataPlaneActivationTestProjection(t, fixture.database)
	if projection.observation != request.ConfirmedOwnership || !projection.confirmedAt.Equal(request.At) {
		t.Fatalf("completed migration projection = %#v", projection)
	}
	var plans int64
	if err := fixture.database.Model(&models.NetworkResolverPolicyMigrationPlan{}).Count(&plans).Error; err != nil {
		t.Fatalf("count policy migration plans: %v", err)
	}
	if plans != 0 {
		t.Fatalf("completed migration plans = %d, want 0", plans)
	}
}

// policyMigrationExec installs one SQLite fault trigger for transaction rollback coverage.
func policyMigrationExec(t *testing.T, database *gorm.DB, statement string) {
	t.Helper()
	if err := database.Exec(statement).Error; err != nil {
		t.Fatalf("install policy migration fault trigger: %v", err)
	}
}
