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
	"gorm.io/gorm"
)

// TestStageNetworkResolverPolicyMigrationStagesAndReplaysExactAuthority verifies the retirement boundary is idempotent.
func TestStageNetworkResolverPolicyMigrationStagesAndReplaysExactAuthority(t *testing.T) {
	fixture := newNetworkResolverPolicyMigrationFixture(t)
	staged, err := fixture.journal.StageNetworkResolverPolicyMigration(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("StageNetworkResolverPolicyMigration() error = %v", err)
	}
	if staged.Operation.State != domain.OperationRequiresApproval || staged.Operation.Phase != networkResolverPolicyMigrationApprovalPhase {
		t.Fatalf("staged operation = %#v, want approval checkpoint", staged)
	}
	replayed, err := fixture.journal.StageNetworkResolverPolicyMigration(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("replayed StageNetworkResolverPolicyMigration() error = %v", err)
	}
	if !reflect.DeepEqual(replayed, staged) {
		t.Fatalf("replayed operation = %#v, want %#v", replayed, staged)
	}
	plan, err := fixture.source.Resolve(context.Background(), ticketissuer.ResolverRequest{OperationID: staged.Operation.ID})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if plan.Purpose != ticketissuer.ResolverPlanPurposePolicyMigration || plan.Mutation != helper.OperationRetireResolver || plan.TargetOwnership != fixture.request.SourceOwnership.Record || plan.Policy != fixture.request.Policy {
		t.Fatalf("resolved policy migration plan = %#v", plan)
	}
}

// TestStageNetworkResolverPolicyMigrationRequestValidateRejectsNonCanonicalAuthority verifies every stage boundary is explicit before database access.
func TestStageNetworkResolverPolicyMigrationRequestValidateRejectsNonCanonicalAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*StageNetworkResolverPolicyMigrationRequest)
	}{
		{
			name: "operation kind",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Operation.Kind = domain.OperationKindNetworkResolverSetup
			},
		},
		{
			name: "operation global",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Operation.ProjectID = "project-policy-migration"
			},
		},
		{
			name: "operation state",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Operation.State = domain.OperationRunning
				started := request.Operation.RequestedAt
				request.Operation.StartedAt = &started
			},
		},
		{
			name: "operation phase",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Operation.Phase = "unexpected"
			},
		},
		{
			name: "network revision",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.ExpectedNetworkRevision = 0
			},
		},
		{
			name: "ownership absent",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.SourceOwnership.Exists = false
			},
		},
		{
			name: "ownership record",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.SourceOwnership.Record.OwnerIdentity = ""
			},
		},
		{
			name: "ownership fingerprint",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.SourceOwnership.Fingerprint = strings.Repeat("d", 64)
			},
		},
		{
			name: "ownership schema",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.SourceOwnership.Record.SchemaVersion = ownership.IdentitySchemaVersion
				request.SourceOwnership.Record.NetworkPolicyFingerprint = ""
				fingerprint, err := request.SourceOwnership.Record.Fingerprint()
				if err != nil {
					t.Fatalf("fingerprint schema-one ownership: %v", err)
				}
				request.SourceOwnership.Fingerprint = fingerprint
			},
		},
		{
			name: "legacy policy",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Policy.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
			},
		},
		{
			name: "policy ownership fingerprint",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.Policy.AuthorityFingerprint = strings.Repeat("e", 64)
			},
		},
		{
			name: "replacement fingerprint",
			mutate: func(request *StageNetworkResolverPolicyMigrationRequest) {
				request.ReplacementPolicyFingerprint = strings.Repeat("f", 64)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverPolicyMigrationFixture(t)
			request := fixture.request
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("StageNetworkResolverPolicyMigrationRequest.Validate() unexpectedly succeeded")
			}
		})
	}
}

// TestStageNetworkResolverPolicyMigrationRejectsAuthorityDriftWithoutWrites verifies staging observes before it mutates.
func TestStageNetworkResolverPolicyMigrationRejectsAuthorityDriftWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*networkResolverPolicyMigrationFixture)
	}{
		{
			name: "revision",
			mutate: func(fixture *networkResolverPolicyMigrationFixture) {
				fixture.request.ExpectedNetworkRevision++
			},
		},
		{name: "projection", mutate: func(fixture *networkResolverPolicyMigrationFixture) {
			fixture.request.SourceOwnership.Fingerprint = strings.Repeat("d", 64)
		}},
		{name: "proof", mutate: func(fixture *networkResolverPolicyMigrationFixture) {
			if err := fixture.database.Where("component = ?", "resolver").Delete(&models.NetworkSetupEvidence{}).Error; err != nil {
				t.Fatalf("delete resolver proof: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverPolicyMigrationFixture(t)
			test.mutate(fixture)
			if _, err := fixture.journal.StageNetworkResolverPolicyMigration(context.Background(), fixture.request); err == nil {
				t.Fatal("StageNetworkResolverPolicyMigration() unexpectedly succeeded")
			}
			var count int64
			if err := fixture.database.Model(&models.Operation{}).Count(&count).Error; err != nil {
				t.Fatalf("count operations: %v", err)
			}
			if count != 1 {
				t.Fatalf("operations after rejected drift = %d, want 1", count)
			}
		})
	}
}

// networkResolverPolicyMigrationFixture provides one completed legacy resolver stage for retirement staging.
type networkResolverPolicyMigrationFixture struct {
	database *gorm.DB
	journal  *OperationJournal
	source   *NetworkResolverPolicyMigrationPlanSource
	request  StageNetworkResolverPolicyMigrationRequest
}

// newNetworkResolverPolicyMigrationFixture completes a legacy macOS resolver setup before staging retirement.
func newNetworkResolverPolicyMigrationFixture(t *testing.T) *networkResolverPolicyMigrationFixture {
	t.Helper()
	setup := newNetworkResolverSetupFixture(t)
	if err := setup.database.AutoMigrate(&models.NetworkResolverPolicyMigrationPlan{}); err != nil {
		t.Fatalf("create migration plan test table: %v", err)
	}
	policy := networkResolverSetupMacOSPolicy(t, strings.Repeat("c", 64))
	policy.Mechanisms = networkpolicy.LegacyMacOSMechanisms()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint legacy policy: %v", err)
	}
	setup.request.Policy = policy
	setup.request.TargetOwnership = networkDataPlaneActivationTestOwnership(t, policy).Record
	setNetworkResolverSetupSourceFingerprint(t, &setup.request)
	approval, err := setup.journal.StageNetworkResolverSetup(context.Background(), setup.request)
	if err != nil {
		t.Fatalf("stage legacy resolver setup: %v", err)
	}
	observation := networkResolverSetupCompletionObservation(t, setup.request.TargetOwnership.InstallationID, policy)
	observationFingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint resolver observation: %v", err)
	}
	targetFingerprint, err := setup.request.TargetOwnership.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint target ownership: %v", err)
	}
	_, err = setup.store.CompleteNetworkResolverSetup(context.Background(), CompleteNetworkResolverSetupRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence: helper.ResolverMutationEvidence{
			Changed:                true,
			PolicyFingerprint:      fingerprint,
			OwnershipFingerprint:   targetFingerprint,
			ObservationFingerprint: observationFingerprint,
			Postcondition:          helper.ResolverPostconditionExact,
		},
		ObservedResolver: observation,
		At:               networkResolverSetupTime().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("complete legacy resolver setup: %v", err)
	}
	projection, _, err := readMachineOwnershipProjectionInTransaction(setup.database)
	if err != nil {
		t.Fatalf("read completed projection: %v", err)
	}
	operation, err := domain.NewOperation("operation-policy-migration", "intent-policy-migration", domain.OperationKindNetworkResolverPolicyMigration, "", networkResolverSetupTime().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("create migration operation: %v", err)
	}
	replacement := policy
	replacement.Mechanisms.Trust = networkpolicy.DarwinAdministratorTrust
	replacementFingerprint, err := replacement.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint replacement policy: %v", err)
	}
	var root models.NetworkState
	if err := setup.database.First(&root, networkStateSingletonID).Error; err != nil {
		t.Fatalf("read resolver-stage network root: %v", err)
	}
	return &networkResolverPolicyMigrationFixture{
		database: setup.database,
		journal:  setup.journal,
		source:   NewNetworkResolverPolicyMigrationPlanSource(models.NewNetworkResolverPolicyMigrationPlanRepo(setup.store.mutations.connections)),
		request: StageNetworkResolverPolicyMigrationRequest{
			Operation:                    operation,
			ExpectedNetworkRevision:      domain.Sequence(root.Revision),
			SourceOwnership:              projection,
			Policy:                       policy,
			ReplacementPolicyFingerprint: replacementFingerprint,
		},
	}
}
