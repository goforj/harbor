package state

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
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/platform/resolver"
	"gorm.io/gorm"
)

// TestCompleteNetworkResolverSetupRequestRejectsIncompleteProof covers every independent confirmation boundary.
func TestCompleteNetworkResolverSetupRequestRejectsIncompleteProof(t *testing.T) {
	fixture, approval, valid := stagedNetworkResolverSetupCompletionFixture(t)
	tests := []struct {
		name   string
		want   string
		mutate func(*CompleteNetworkResolverSetupRequest)
	}{
		{name: "operation", want: "operation ID", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.OperationID = ""
		}},
		{name: "revision", want: "revision must be positive", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ExpectedOperationRevision = 0
		}},
		{name: "revision exhaustion", want: "leave room for three", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ExpectedOperationRevision = domain.MaximumSequence - 2
		}},
		{name: "time", want: "time must not be zero", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.At = time.Time{}
		}},
		{name: "policy fingerprint", want: "policy fingerprint is invalid", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.PolicyFingerprint = "invalid"
		}},
		{name: "ownership fingerprint", want: "ownership fingerprint is invalid", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.OwnershipFingerprint = strings.Repeat("A", 64)
		}},
		{name: "observation fingerprint", want: "observation fingerprint is invalid", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.ObservationFingerprint = strings.Repeat("0", 63)
		}},
		{name: "postcondition", want: "must prove the exact resolver policy", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.Postcondition = helper.ResolverPostconditionOwnedAbsent
		}},
		{name: "observation shape", want: "observed network resolver", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ObservedResolver.Truncated = true
		}},
		{name: "observation state", want: "want one exact owned rule", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ObservedResolver.Rules = nil
		}},
		{name: "observation policy", want: "policy does not match helper evidence", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.PolicyFingerprint = strings.Repeat("0", 64)
		}},
		{name: "observation correlation", want: "does not match helper evidence", mutate: func(request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.ObservationFingerprint = strings.Repeat("0", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneCompleteNetworkResolverSetupRequest(valid)
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompleteNetworkResolverSetupRequest.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.store.CompleteNetworkResolverSetup(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled CompleteNetworkResolverSetup() error = %v, want context.Canceled", err)
	}
	assertNetworkResolverSetupCompletionState(t, fixture, approval, 4, 3, 1, NetworkStageIdentity, ownership.IdentitySchemaVersion)
}

// TestStoreCompletesNetworkResolverSetupAtomically proves the plan, two operation edges, and resolver revision share one transaction.
func TestStoreCompletesNetworkResolverSetupAtomically(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverSetupCompletionFixture(t)
	beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
	beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)

	result, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverSetup() error = %v", err)
	}
	if result.Network.Replayed || result.Operation.Revision != 7 || result.NetworkRevision != 6 || result.Network.Record.Revision != 6 ||
		result.Operation.Operation.State != domain.OperationSucceeded || result.Network.Record.Stage != NetworkStageResolver {
		t.Fatalf("CompleteNetworkResolverSetup() = %#v, want operation 7 around resolver revision 6", result)
	}
	if !result.Operation.Operation.FinishedAt.Equal(request.At) || !result.Network.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("completion times = operation %v, network %v, want %v", result.Operation.Operation.FinishedAt, result.Network.Record.UpdatedAt, request.At)
	}
	assertNetworkResolverSetupCompletionState(t, fixture, approval, 7, 5, 0, NetworkStageResolver, ownership.NetworkPolicySchemaVersion)

	afterRows := networkDataPlaneActivationTestRows(t, fixture.database)
	afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database)
	if len(beforeRows.SetupEvidence) != 2 || len(afterRows.SetupEvidence) != 3 {
		t.Fatalf("resolver proof cardinality changed from %d to %d, want 2 to 3", len(beforeRows.SetupEvidence), len(afterRows.SetupEvidence))
	}
	if !reflect.DeepEqual(beforeRows.Candidates, afterRows.Candidates) ||
		!reflect.DeepEqual(beforeRows.Leases, afterRows.Leases) ||
		!reflect.DeepEqual(beforeRows.Releases, afterRows.Releases) ||
		len(afterRows.Listeners) != 0 || len(afterRows.Endpoints) != 0 {
		t.Fatal("resolver setup completion changed identity-stage topology")
	}
	if beforeProjection.observation.Record.SchemaVersion != ownership.IdentitySchemaVersion ||
		afterProjection.observation.Record != fixture.request.TargetOwnership ||
		afterProjection.observation.Fingerprint != request.ResolverEvidence.OwnershipFingerprint ||
		!afterProjection.confirmedAt.Equal(request.At) {
		t.Fatalf("completed ownership projection = %#v", afterProjection)
	}
	expectedProof, err := networkResolverSetupCompletionProof(request, fixture.request.TargetOwnership.Generation)
	if err != nil {
		t.Fatalf("derive expected completion proof: %v", err)
	}
	if err := requireExactCompletedNetworkResolverProof(
		afterRows.SetupEvidence,
		expectedProof,
		request.OperationID,
		NetworkStageResolver,
	); err != nil {
		t.Fatalf("completed resolver proof error = %v", err)
	}

	history, err := fixture.journal.Transitions(context.Background(), request.OperationID)
	if err != nil {
		t.Fatalf("read completed resolver transitions: %v", err)
	}
	wantStates := []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
		domain.OperationRunning,
		domain.OperationSucceeded,
	}
	wantSequences := []domain.Sequence{2, 3, 4, 5, 7}
	for index := range history {
		if history[index].State != wantStates[index] || history[index].Sequence != wantSequences[index] {
			t.Fatalf("completed transition %d = %#v", index+1, history[index])
		}
	}
}

// TestStoreReplaysExactNetworkResolverSetupCompletionAfterRestart proves terminal replay does not require the retired plan.
func TestStoreReplaysExactNetworkResolverSetupCompletionAfterRestart(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverSetupCompletionFixture(t)
	first, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("first CompleteNetworkResolverSetup() error = %v", err)
	}
	fixture.store = restartNetworkResolverSetupCompletionStore(fixture)

	replayed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed CompleteNetworkResolverSetup() error = %v", err)
	}
	if !replayed.Network.Replayed || !reflect.DeepEqual(replayed.Operation, first.Operation) ||
		!reflect.DeepEqual(replayed.Network.Record, first.Network.Record) {
		t.Fatalf("replayed completion = %#v, want %#v with replay marker", replayed, first)
	}
	assertNetworkResolverSetupCompletionState(t, fixture, approval, 7, 5, 0, NetworkStageResolver, ownership.NetworkPolicySchemaVersion)
}

// TestStoreReplaysNetworkResolverSetupCompletionAfterFullProgression proves retries remain tied to historical revision six.
func TestStoreReplaysNetworkResolverSetupCompletionAfterFullProgression(t *testing.T) {
	fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
	completed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverSetup() error = %v", err)
	}
	expectedProof, err := networkResolverSetupCompletionProof(request, fixture.request.TargetOwnership.Generation)
	if err != nil {
		t.Fatalf("derive completed resolver proof: %v", err)
	}
	fullRequest := networkDataPlaneActivationTestRequest(t, completed.NetworkRevision)
	fullRequest.ConfirmedOwnership = networkDataPlaneActivationTestObservation(t, fixture.request.TargetOwnership)
	fullRequest.Policy = fixture.request.Policy
	fullRequest.Setup[0] = expectedProof
	fullRequest.At = request.At.Add(time.Minute)
	fullRequest.Setup[1].VerifiedAt = fullRequest.At
	fullRequest.Listeners.DNS.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTP.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTPS.VerifiedAt = fullRequest.At
	full, err := fixture.store.ActivateNetworkDataPlane(context.Background(), fullRequest)
	if err != nil {
		t.Fatalf("ActivateNetworkDataPlane() error = %v", err)
	}
	if full.Record.Stage != NetworkStageFull || full.Record.Revision != 8 {
		t.Fatalf("full network = %#v, want full revision 8", full)
	}

	fixture.store = restartNetworkResolverSetupCompletionStore(fixture)
	replayed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("replay after full progression error = %v", err)
	}
	if !replayed.Network.Replayed || replayed.NetworkRevision != completed.NetworkRevision ||
		replayed.Operation.Revision != completed.Operation.Revision ||
		replayed.Network.Record.Stage != NetworkStageFull || replayed.Network.Record.Revision != full.Record.Revision {
		t.Fatalf("replay after full progression = %#v", replayed)
	}
}

// TestStoreRejectsNetworkResolverSetupCompletionConflicts verifies stale, planned, lifecycle, and timing mismatches preserve staging.
func TestStoreRejectsNetworkResolverSetupCompletionConflicts(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		assert func(error) bool
		mutate func(*testing.T, *networkResolverSetupFixture, *CompleteNetworkResolverSetupRequest)
	}{
		{name: "stale revision", want: "stale", mutate: func(_ *testing.T, _ *networkResolverSetupFixture, request *CompleteNetworkResolverSetupRequest) {
			request.ExpectedOperationRevision--
		}, assert: func(err error) bool {
			var stale *StaleRevisionError
			return errors.As(err, &stale)
		}},
		{name: "target ownership", want: "target ownership", mutate: func(_ *testing.T, _ *networkResolverSetupFixture, request *CompleteNetworkResolverSetupRequest) {
			request.ResolverEvidence.OwnershipFingerprint = strings.Repeat("0", 64)
		}},
		{name: "resolver policy", want: "resolver policy", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, request *CompleteNetworkResolverSetupRequest) {
			alternate := networkResolverSetupMacOSPolicy(t, fixture.request.Policy.AuthorityFingerprint)
			request.ObservedResolver = networkResolverSetupCompletionObservation(t, fixture.request.TargetOwnership.InstallationID, alternate)
			policyFingerprint, err := alternate.Fingerprint()
			if err != nil {
				t.Fatalf("fingerprint alternate resolver policy: %v", err)
			}
			observationFingerprint, err := request.ObservedResolver.Fingerprint()
			if err != nil {
				t.Fatalf("fingerprint alternate resolver observation: %v", err)
			}
			request.ResolverEvidence.PolicyFingerprint = policyFingerprint
			request.ResolverEvidence.ObservationFingerprint = observationFingerprint
		}},
		{name: "observed installation", want: "observed resolver authority", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, request *CompleteNetworkResolverSetupRequest) {
			request.ObservedResolver = networkResolverSetupCompletionObservation(t, "installation-other", fixture.request.Policy)
			fingerprint, err := request.ObservedResolver.Fingerprint()
			if err != nil {
				t.Fatalf("fingerprint alternate resolver observation: %v", err)
			}
			request.ResolverEvidence.ObservationFingerprint = fingerprint
		}},
		{name: "missing plan", want: "singleton plan is missing", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, _ *CompleteNetworkResolverSetupRequest) {
			networkResolverSetupExec(t, fixture.database, "DELETE FROM network_resolver_setup_plans")
		}, assert: func(err error) bool {
			var corrupt *CorruptStateError
			return errors.As(err, &corrupt)
		}},
		{name: "wrong operation kind", want: "not an active global", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, _ *CompleteNetworkResolverSetupRequest) {
			networkResolverSetupExec(t, fixture.database, "UPDATE operations SET kind = 'host.setup' WHERE id = ?", fixture.request.Operation.ID)
		}},
		{name: "wrong operation state", want: "state does not match latest transition", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, _ *CompleteNetworkResolverSetupRequest) {
			networkResolverSetupExec(t, fixture.database, "UPDATE operations SET state = 'running', phase = 'running' WHERE id = ?", fixture.request.Operation.ID)
		}},
		{name: "staging history", want: "staged lifecycle", mutate: func(t *testing.T, fixture *networkResolverSetupFixture, _ *CompleteNetworkResolverSetupRequest) {
			networkResolverSetupExec(t, fixture.database, "UPDATE operation_transitions SET phase = 'changed' WHERE operation_id = ? AND ordinal = 2", fixture.request.Operation.ID)
		}},
		{name: "completion time", want: "precedes the operation request", mutate: func(_ *testing.T, _ *networkResolverSetupFixture, request *CompleteNetworkResolverSetupRequest) {
			request.At = networkResolverSetupTime().Add(-time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, approval, request := stagedNetworkResolverSetupCompletionFixture(t)
			test.mutate(t, fixture, &request)
			_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
			if test.assert != nil {
				if !test.assert(err) {
					t.Fatalf("%s error = %v, want %s classification", test.name, err, test.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want containing %q", test.name, err, test.want)
			}
			if test.name == "wrong operation state" {
				assertNetworkResolverSetupCounts(t, fixture, 4, 1, 3, 1)
			} else {
				assertNetworkResolverSetupCompletionState(t, fixture, approval, 4, 3, resolverPlanCountAfterMutation(test.name), NetworkStageIdentity, ownership.IdentitySchemaVersion)
			}
		})
	}
}

// TestStoreRejectsMissingNetworkResolverSetupCompletionOperation preserves typed selection failure without mutation.
func TestStoreRejectsMissingNetworkResolverSetupCompletionOperation(t *testing.T) {
	fixture, approval, request := stagedNetworkResolverSetupCompletionFixture(t)
	request.OperationID = "operation-resolver-missing"
	_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	var missing *OperationNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("missing resolver completion operation error = %v, want OperationNotFoundError", err)
	}
	assertNetworkResolverSetupCompletionState(t, fixture, approval, 4, 3, 1, NetworkStageIdentity, ownership.IdentitySchemaVersion)
}

// TestStoreRejectsNetworkResolverSetupCompletionReplayDrift distinguishes conflicting retries from corrupt terminal state.
func TestStoreRejectsNetworkResolverSetupCompletionReplayDrift(t *testing.T) {
	t.Run("retained plan", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		var plan models.NetworkResolverSetupPlan
		if err := fixture.database.First(&plan, networkResolverSetupPlanSingletonID).Error; err != nil {
			t.Fatalf("read staged resolver plan: %v", err)
		}
		completed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		if err != nil {
			t.Fatalf("complete resolver setup: %v", err)
		}
		plan.OperationRevision = int(completed.Operation.Revision)
		plan.NetworkRevision = int(completed.NetworkRevision)
		if err := fixture.database.Create(&plan).Error; err != nil {
			t.Fatalf("seed impossible retained resolver plan: %v", err)
		}
		_, err = fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "retains its singleton plan") {
			t.Fatalf("retained-plan replay error = %v, want corruption", err)
		}
	})

	t.Run("helper evidence", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		if _, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request); err != nil {
			t.Fatalf("complete resolver setup: %v", err)
		}
		request.ResolverEvidence.Changed = !request.ResolverEvidence.Changed
		_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		var conflict *NetworkResolverSetupCompletionConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "resolver proof" {
			t.Fatalf("helper evidence replay error = %v, want resolver-proof conflict", err)
		}
	})

	t.Run("completion time", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		if _, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request); err != nil {
			t.Fatalf("complete resolver setup: %v", err)
		}
		request.At = request.At.Add(time.Second)
		_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		var conflict *NetworkResolverSetupCompletionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("completion-time replay error = %v, want conflict", err)
		}
	})

	t.Run("missing resolver proof", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		if _, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request); err != nil {
			t.Fatalf("complete resolver setup: %v", err)
		}
		networkResolverSetupExec(t, fixture.database, "DELETE FROM network_setup_evidence WHERE component = 'resolver'")
		_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "required component is missing") {
			t.Fatalf("missing resolver proof replay error = %v, want corruption", err)
		}
	})

	t.Run("terminal history", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		if _, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request); err != nil {
			t.Fatalf("complete resolver setup: %v", err)
		}
		networkResolverSetupExec(t, fixture.database, "UPDATE operation_transitions SET phase = 'changed' WHERE operation_id = ? AND ordinal = 2", request.OperationID)
		_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) {
			t.Fatalf("terminal-history replay error = %v, want corruption", err)
		}
	})
}

// TestStoreNetworkResolverSetupCompletionRollsBackEveryWriteFault proves no partial authority escapes a late failure.
func TestStoreNetworkResolverSetupCompletionRollsBackEveryWriteFault(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{name: "resolver proof", trigger: "CREATE TRIGGER fail_resolver_setup_completion BEFORE INSERT ON network_setup_evidence WHEN NEW.component = 'resolver' BEGIN SELECT RAISE(ABORT, 'forced resolver proof failure'); END"},
		{name: "network root", trigger: "CREATE TRIGGER fail_resolver_setup_completion BEFORE UPDATE ON network_state WHEN NEW.stage = 'resolver' BEGIN SELECT RAISE(ABORT, 'forced resolver root failure'); END"},
		{name: "ownership projection", trigger: "CREATE TRIGGER fail_resolver_setup_completion BEFORE UPDATE ON machine_ownership_projections WHEN NEW.ownership_schema_version = 2 BEGIN SELECT RAISE(ABORT, 'forced resolver ownership failure'); END"},
		{name: "succeeded transition", trigger: "CREATE TRIGGER fail_resolver_setup_completion BEFORE INSERT ON operation_transitions WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'forced resolver success failure'); END"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, approval, request := stagedNetworkResolverSetupCompletionFixture(t)
			beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
			beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)
			networkResolverSetupExec(t, fixture.database, test.trigger)

			_, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "forced resolver") {
				t.Fatalf("%s failure error = %v", test.name, err)
			}
			assertNetworkResolverSetupCompletionState(t, fixture, approval, 4, 3, 1, NetworkStageIdentity, ownership.IdentitySchemaVersion)
			if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatalf("%s failure changed network rows", test.name)
			}
			if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
				t.Fatalf("%s failure changed ownership projection", test.name)
			}
		})
	}
}

// TestCompleteNetworkResolverSetupResultValidation covers the terminal aggregate contract independently of storage.
func TestCompleteNetworkResolverSetupResultValidation(t *testing.T) {
	fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
	valid, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverSetup() error = %v", err)
	}
	tests := []struct {
		name   string
		want   string
		mutate func(*CompleteNetworkResolverSetupResult)
	}{
		{name: "operation", want: "operation ID", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Operation.ID = ""
		}},
		{name: "kind", want: "must be global kind", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Operation.Kind = domain.OperationKindNetworkSetup
		}},
		{name: "project", want: "must not identify a project", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Operation.ProjectID = "project-other"
		}},
		{name: "state", want: "operation state", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Operation.State = domain.OperationRunning
			result.Operation.Operation.FinishedAt = nil
		}},
		{name: "phase", want: "operation phase", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Operation.Phase = networkResolverSetupCompletionRunningPhase
		}},
		{name: "network", want: "installation ID", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Network.Record.Ownership.InstallationID = ""
		}},
		{name: "stage", want: "resolver or later full", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Network.Record.Stage = NetworkStageIdentity
		}},
		{name: "revision", want: "not contiguous", mutate: func(result *CompleteNetworkResolverSetupResult) {
			result.Operation.Revision++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.mutate(&result)
			if err := result.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompleteNetworkResolverSetupResult.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// stagedNetworkResolverSetupCompletionFixture stages one plan and returns its matching helper and native proof.
func stagedNetworkResolverSetupCompletionFixture(
	t *testing.T,
) (*networkResolverSetupFixture, OperationRecord, CompleteNetworkResolverSetupRequest) {
	t.Helper()
	fixture := newNetworkResolverSetupFixture(t)
	approval, err := fixture.journal.StageNetworkResolverSetup(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("stage network resolver setup completion fixture: %v", err)
	}
	observation := networkResolverSetupCompletionObservation(
		t,
		fixture.request.TargetOwnership.InstallationID,
		fixture.request.Policy,
	)
	observationFingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint resolver completion observation: %v", err)
	}
	targetFingerprint, err := fixture.request.TargetOwnership.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint resolver completion target: %v", err)
	}
	request := CompleteNetworkResolverSetupRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence: helper.ResolverMutationEvidence{
			Changed:                true,
			PolicyFingerprint:      fixture.request.TargetOwnership.NetworkPolicyFingerprint,
			OwnershipFingerprint:   targetFingerprint,
			ObservationFingerprint: observationFingerprint,
			Postcondition:          helper.ResolverPostconditionExact,
		},
		ObservedResolver: observation,
		At:               networkResolverSetupTime().Add(time.Minute),
	}
	return fixture, approval, request
}

// networkResolverSetupCompletionObservation returns one complete exact Linux resolver observation.
func networkResolverSetupCompletionObservation(
	t *testing.T,
	installationID string,
	policy networkpolicy.Policy,
) resolver.Observation {
	t.Helper()
	request, err := resolver.NewRequest(installationID, policy)
	if err != nil {
		t.Fatalf("construct resolver completion request: %v", err)
	}
	owner := request.OwnerMarker()
	return resolver.Observation{
		Request:  request,
		Complete: true,
		Rules: []resolver.RuleFact{{
			Mechanism:              request.Mechanism(),
			NativeID:               "harbor-resolver-owned",
			Namespace:              request.Suffix(),
			Servers:                []netip.AddrPort{request.Endpoint()},
			RouteOnly:              true,
			NativeExact:            true,
			NativeAttributesSHA256: strings.Repeat("a", 64),
			Owner:                  &owner,
		}},
	}
}

// restartNetworkResolverSetupCompletionStore reconstructs the aggregate store over the fixture's named database.
func restartNetworkResolverSetupCompletionStore(fixture *networkResolverSetupFixture) *Store {
	connections := fixture.store.mutations.connections
	return NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
	)
}

// assertNetworkResolverSetupCompletionState checks global sequence, lifecycle, plan, root stage, and ownership schema.
func assertNetworkResolverSetupCompletionState(
	t *testing.T,
	fixture *networkResolverSetupFixture,
	approval OperationRecord,
	wantSequence int64,
	wantTransitions int64,
	wantPlans int64,
	wantStage NetworkStage,
	wantOwnershipSchema uint32,
) {
	t.Helper()
	assertNetworkResolverSetupCounts(t, fixture, wantSequence, 1, wantTransitions, wantPlans)
	operation, err := fixture.journal.Operation(context.Background(), approval.Operation.ID)
	if err != nil {
		t.Fatalf("read resolver setup operation: %v", err)
	}
	if wantSequence == 4 {
		if operation.Revision != approval.Revision || operation.Operation.State != domain.OperationRequiresApproval {
			t.Fatalf("staged resolver operation after failure = %#v", operation)
		}
	} else if operation.Revision != 7 || operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("completed resolver operation = %#v", operation)
	}
	rows := networkDataPlaneActivationTestRows(t, fixture.database)
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("read resolver setup network = %#v, %v", network, err)
	}
	if network.Stage != wantStage {
		t.Fatalf("resolver setup network stage = %q, want %q", network.Stage, wantStage)
	}
	projection := networkDataPlaneActivationTestProjection(t, fixture.database)
	if projection.observation.Record.SchemaVersion != wantOwnershipSchema {
		t.Fatalf("resolver setup ownership schema = %d, want %d", projection.observation.Record.SchemaVersion, wantOwnershipSchema)
	}
}

// resolverPlanCountAfterMutation accounts for the one test that intentionally deletes its staged plan.
func resolverPlanCountAfterMutation(name string) int64 {
	if name == "missing plan" {
		return 0
	}
	return 1
}

// TestNetworkResolverSetupCompletionConflictErrorOmitsEvidence verifies diagnostics expose only the operation and fact group.
func TestNetworkResolverSetupCompletionConflictErrorOmitsEvidence(t *testing.T) {
	err := &NetworkResolverSetupCompletionConflictError{
		OperationID: "operation-resolver-completion",
		Difference:  "resolver proof",
	}
	if got := err.Error(); got != `network resolver setup operation "operation-resolver-completion" has different resolver proof` {
		t.Fatalf("NetworkResolverSetupCompletionConflictError.Error() = %q", got)
	}
}

// TestActivatePlannedNetworkResolverRejectsUnexpectedIdentityState directly covers impossible post-plan mutation inputs.
func TestActivatePlannedNetworkResolverRejectsUnexpectedIdentityState(t *testing.T) {
	t.Run("projection", func(t *testing.T) {
		fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
		activation, err := networkResolverSetupActivationRequest(request, ticketPlanFromFixture(t, fixture), fixture.request.ExpectedNetworkRevision)
		if err != nil {
			t.Fatalf("derive planned activation: %v", err)
		}
		projection := networkDataPlaneActivationTestProjection(t, fixture.database)
		target := networkDataPlaneActivationTestObservation(t, fixture.request.TargetOwnership)
		if err := fixture.database.Model(&models.MachineOwnershipProjection{}).
			Where("id = ?", projection.row.Id).
			Updates(map[string]any{
				"ownership_schema_version":   int(ownership.NetworkPolicySchemaVersion),
				"network_policy_fingerprint": fixture.request.TargetOwnership.NetworkPolicyFingerprint,
				"record_fingerprint":         target.Fingerprint,
			}).Error; err != nil {
			t.Fatalf("seed premature ownership upgrade: %v", err)
		}
		err = fixture.store.mutations.mutate(context.Background(), "test planned activation", func(tx *gorm.DB) error {
			_, activateErr := activatePlannedNetworkResolverInTransaction(tx, request.OperationID, activation)
			return activateErr
		})
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "identity-stage network retains schema-2 ownership") {
			t.Fatalf("premature projection error = %v, want corruption", err)
		}
	})
}

// ticketPlanFromFixture reconstructs the exact staged plan for focused internal mutation tests.
func ticketPlanFromFixture(t *testing.T, fixture *networkResolverSetupFixture) ticketissuer.ResolverPlan {
	t.Helper()
	plan, err := fixture.source.Resolve(
		context.Background(),
		ticketissuer.ResolverRequest{OperationID: fixture.request.Operation.ID},
	)
	if err != nil {
		t.Fatalf("resolve fixture ticket plan: %v", err)
	}
	return plan
}
