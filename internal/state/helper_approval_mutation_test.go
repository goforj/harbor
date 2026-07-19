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
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const helperApprovalMutationTestMigrationName = "2026_07_19_001556_create_helper_approval_plans"

// helperApprovalMutationFixture owns one releasing project and its running unregister operation.
type helperApprovalMutationFixture struct {
	store    *Store
	database *gorm.DB
	journal  *OperationJournal
	running  OperationRecord
	begin    BeginProjectNetworkReleaseRequest
	release  ProjectNetworkReleaseMutationResult
	at       time.Time
}

// TestStoreStagesAndResumesExactProjectNetworkReleaseApproval proves the sole writable plan path derives the complete unregister release set.
func TestStoreStagesAndResumesExactProjectNetworkReleaseApproval(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	staged := mustStageProjectNetworkReleaseApproval(t, fixture)
	if staged.Operation.Operation.State != domain.OperationRequiresApproval || staged.Operation.Revision <= fixture.running.Revision {
		t.Fatalf("staged operation = %#v", staged.Operation)
	}
	if err := staged.Validate(); err != nil {
		t.Fatalf("ProjectNetworkReleaseApprovalResult.Validate() error = %v", err)
	}
	if len(staged.Plans) != len(fixture.release.Release.ActiveLeases) {
		t.Fatalf("staged plans = %d, want %d", len(staged.Plans), len(fixture.release.Release.ActiveLeases))
	}
	for index, plan := range staged.Plans {
		if plan.Intent.Mutation != helper.OperationReleaseLoopbackIdentity ||
			plan.Intent.LeaseState != ticketissuer.LeaseActive ||
			len(plan.Intent.Requirements) != 0 ||
			plan.Intent.Lease != fixture.release.Release.ActiveLeases[index].Lease {
			t.Fatalf("staged plan %d = %#v, want exact release %#v", index, plan, fixture.release.Release.ActiveLeases[index])
		}
	}
	assertHelperApprovalMutationCounts(t, fixture.database, int64(len(staged.Plans)), 0)

	resumed, err := fixture.store.ResumeProjectNetworkReleaseApproval(
		context.Background(),
		ResumeProjectNetworkReleaseApprovalRequest{
			OperationID:               staged.Operation.Operation.ID,
			ExpectedOperationRevision: staged.Operation.Revision,
			Phase:                     "host releases verified",
			At:                        fixture.at.Add(time.Second),
		},
	)
	if err != nil {
		t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v", err)
	}
	if resumed.Operation.State != domain.OperationRunning || resumed.Revision <= staged.Operation.Revision {
		t.Fatalf("resumed operation = %#v", resumed)
	}
	assertHelperApprovalMutationCounts(t, fixture.database, 0, 0)

	recovery, found, err := fixture.store.ProjectNetworkRelease(context.Background(), fixture.begin.OperationID)
	if err != nil || !found || !reflect.DeepEqual(recovery.ActiveLeases, fixture.release.Release.ActiveLeases) {
		t.Fatalf("ProjectNetworkRelease() after resume = %#v, %t, %v", recovery, found, err)
	}
	completion := networkReleaseTestCompleteRequest(fixture.begin, recovery)
	completion.ExpectedNetworkRevision = fixture.release.Record.Revision
	completion.ExpectedOperationRevision = resumed.Revision
	completed, err := fixture.store.CompleteProjectNetworkRelease(context.Background(), completion)
	if err != nil {
		t.Fatalf("CompleteProjectNetworkRelease() after approval resume error = %v", err)
	}
	if completed.Release.State != ProjectNetworkReleaseCompleted {
		t.Fatalf("completed release = %#v", completed.Release)
	}
}

// TestOperationJournalGenericTransitionCannotBypassReleasePlanResumption proves the composite foreign key protects exact plan retirement.
func TestOperationJournalGenericTransitionCannotBypassReleasePlanResumption(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	staged := mustStageProjectNetworkReleaseApproval(t, fixture)

	if _, err := fixture.journal.Transition(
		context.Background(),
		staged.Operation.Operation.ID,
		staged.Operation.Revision,
		domain.OperationRunning,
		"unsafe generic resume",
		fixture.at.Add(time.Second),
		nil,
	); err == nil {
		t.Fatal("generic Transition() unexpectedly advanced a planned operation")
	}
	assertHelperApprovalMutationOperation(t, fixture.journal, staged.Operation)
	assertHelperApprovalMutationCounts(t, fixture.database, int64(len(staged.Plans)), 0)
}

// TestStoreProjectNetworkReleaseApprovalRejectsMissingOrWrongOwners proves only the staged unregister marker can authorize plans.
func TestStoreProjectNetworkReleaseApprovalRejectsMissingOrWrongOwners(t *testing.T) {
	t.Run("missing release marker", func(t *testing.T) {
		store, databaseConnection, _, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
		applyHelperApprovalMutationTestMigration(t, databaseConnection)
		_, err := store.StageProjectNetworkReleaseApproval(context.Background(), StageProjectNetworkReleaseApprovalRequest{
			OperationID:               running.Operation.ID,
			ExpectedOperationRevision: running.Revision,
			Phase:                     "waiting for release approval",
			At:                        begin.At.Add(time.Second),
		})
		var missing *ProjectNetworkReleaseNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want ProjectNetworkReleaseNotFoundError", err)
		}
		assertHelperApprovalMutationCounts(t, databaseConnection, 0, 0)
	})

	t.Run("wrong operation kind", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		if err := fixture.database.Exec(
			"UPDATE operations SET kind = 'project.refresh' WHERE id = ?",
			fixture.running.Operation.ID,
		).Error; err != nil {
			t.Fatalf("corrupt operation kind: %v", err)
		}
		_, err := fixture.store.StageProjectNetworkReleaseApproval(
			context.Background(),
			helperApprovalMutationStageRequest(fixture),
		)
		if err == nil || !strings.Contains(err.Error(), "operation kind") {
			t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want operation kind", err)
		}
		assertHelperApprovalMutationCounts(t, fixture.database, 0, 0)
	})
}

// TestStoreProjectNetworkReleaseApprovalRejectsInvalidLifecycle proves staging and resumption cannot skip their operation edges.
func TestStoreProjectNetworkReleaseApprovalRejectsInvalidLifecycle(t *testing.T) {
	t.Run("invalid requests", func(t *testing.T) {
		var store *Store
		if _, err := store.StageProjectNetworkReleaseApproval(context.Background(), StageProjectNetworkReleaseApprovalRequest{}); err == nil {
			t.Fatal("StageProjectNetworkReleaseApproval() accepted an invalid request")
		}
		if _, err := store.ResumeProjectNetworkReleaseApproval(context.Background(), ResumeProjectNetworkReleaseApprovalRequest{}); err == nil {
			t.Fatal("ResumeProjectNetworkReleaseApproval() accepted an invalid request")
		}
	})

	t.Run("missing operation", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		request := helperApprovalMutationStageRequest(fixture)
		request.OperationID = "operation-missing"
		request.ExpectedOperationRevision = 1
		_, err := fixture.store.StageProjectNetworkReleaseApproval(context.Background(), request)
		var missing *OperationNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want OperationNotFoundError", err)
		}
	})

	t.Run("stage twice", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		staged := mustStageProjectNetworkReleaseApproval(t, fixture)
		request := helperApprovalMutationStageRequest(fixture)
		request.ExpectedOperationRevision = staged.Operation.Revision
		request.At = fixture.at.Add(time.Second)
		_, err := fixture.store.StageProjectNetworkReleaseApproval(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "require a running operation") {
			t.Fatalf("second StageProjectNetworkReleaseApproval() error = %v", err)
		}
	})

	t.Run("resume running", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		_, err := fixture.store.ResumeProjectNetworkReleaseApproval(
			context.Background(),
			ResumeProjectNetworkReleaseApprovalRequest{
				OperationID:               fixture.running.Operation.ID,
				ExpectedOperationRevision: fixture.running.Revision,
				Phase:                     "premature resume",
				At:                        fixture.at,
			},
		)
		if err == nil || !strings.Contains(err.Error(), "only from requires-approval") {
			t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v", err)
		}
	})

	t.Run("resume without plans", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		approval, err := fixture.journal.Transition(
			context.Background(),
			fixture.running.Operation.ID,
			fixture.running.Revision,
			domain.OperationRequiresApproval,
			"approval without authority",
			fixture.at,
			nil,
		)
		if err != nil {
			t.Fatalf("seed approval operation: %v", err)
		}
		_, err = fixture.store.ResumeProjectNetworkReleaseApproval(
			context.Background(),
			ResumeProjectNetworkReleaseApprovalRequest{
				OperationID:               approval.Operation.ID,
				ExpectedOperationRevision: approval.Revision,
				Phase:                     "missing plan resume",
				At:                        fixture.at.Add(time.Second),
			},
		)
		if err == nil || !strings.Contains(err.Error(), "has no helper approval plans") {
			t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v", err)
		}
	})

	t.Run("unscoped operation", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		if err := fixture.database.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
			t.Fatalf("disable check constraints for corruption fixture: %v", err)
		}
		if err := fixture.database.Exec("UPDATE operations SET project_id = '' WHERE id = ?", fixture.running.Operation.ID).Error; err != nil {
			t.Fatalf("clear operation project: %v", err)
		}
		_, err := fixture.store.StageProjectNetworkReleaseApproval(context.Background(), helperApprovalMutationStageRequest(fixture))
		if err == nil || !strings.Contains(err.Error(), "must identify a project") {
			t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want project scope", err)
		}
	})

	t.Run("invalid staging transition", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		request := helperApprovalMutationStageRequest(fixture)
		request.At = fixture.running.Operation.RequestedAt.Add(-time.Second)
		_, err := fixture.store.StageProjectNetworkReleaseApproval(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "must not precede") {
			t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want transition-time failure", err)
		}
		assertHelperApprovalMutationOperation(t, fixture.journal, fixture.running)
		assertHelperApprovalMutationCounts(t, fixture.database, 0, 0)
	})
}

// TestStoreProjectNetworkReleaseApprovalRejectsStaleRevisions proves staging and resumption retain optimistic operation ownership.
func TestStoreProjectNetworkReleaseApprovalRejectsStaleRevisions(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	request := helperApprovalMutationStageRequest(fixture)
	request.ExpectedOperationRevision--
	_, err := fixture.store.StageProjectNetworkReleaseApproval(context.Background(), request)
	assertHelperApprovalMutationStale(t, err, fixture.running.Operation.ID, request.ExpectedOperationRevision, fixture.running.Revision)
	assertHelperApprovalMutationCounts(t, fixture.database, 0, 0)

	staged := mustStageProjectNetworkReleaseApproval(t, fixture)
	_, err = fixture.store.ResumeProjectNetworkReleaseApproval(
		context.Background(),
		ResumeProjectNetworkReleaseApprovalRequest{
			OperationID:               staged.Operation.Operation.ID,
			ExpectedOperationRevision: fixture.running.Revision,
			Phase:                     "stale release resume",
			At:                        fixture.at.Add(time.Second),
		},
	)
	assertHelperApprovalMutationStale(t, err, staged.Operation.Operation.ID, fixture.running.Revision, staged.Operation.Revision)
	assertHelperApprovalMutationCounts(t, fixture.database, int64(len(staged.Plans)), 0)
}

// TestStoreProjectNetworkReleaseApprovalStageRollsBackInsertionFailure proves approval state cannot survive without every exact plan.
func TestStoreProjectNetworkReleaseApprovalStageRollsBackInsertionFailure(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	cause := errors.New("approval plan insert failed")
	callback := "harbor:test_helper_approval_plan_failure"
	if err := fixture.database.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "helper_approval_plans" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatalf("register plan failure callback: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Create().Remove(callback) })

	_, err := fixture.store.StageProjectNetworkReleaseApproval(
		context.Background(),
		helperApprovalMutationStageRequest(fixture),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("StageProjectNetworkReleaseApproval() error = %v, want sentinel", err)
	}
	assertHelperApprovalMutationOperation(t, fixture.journal, fixture.running)
	assertHelperApprovalMutationCounts(t, fixture.database, 0, 0)
}

// TestStoreProjectNetworkReleaseApprovalResumeRejectsChangedPlanSet proves restart recovery cannot retire a subset or altered effect.
func TestStoreProjectNetworkReleaseApprovalResumeRejectsChangedPlanSet(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB)
		want   string
	}{
		{name: "subset", mutate: func(t *testing.T, databaseConnection *gorm.DB) {
			if err := databaseConnection.Exec(
				"DELETE FROM helper_approval_plans WHERE id = (SELECT max(id) FROM helper_approval_plans)",
			).Error; err != nil {
				t.Fatalf("remove approval plan: %v", err)
			}
		}, want: "differ from the exact project release set"},
		{name: "mutation", mutate: func(t *testing.T, databaseConnection *gorm.DB) {
			if err := databaseConnection.Exec(
				"UPDATE helper_approval_plans SET mutation = 'ensure_loopback_identity' WHERE id = (SELECT min(id) FROM helper_approval_plans)",
			).Error; err != nil {
				t.Fatalf("alter approval mutation: %v", err)
			}
		}, want: "differ from the exact project release set"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalMutationFixture(t)
			staged := mustStageProjectNetworkReleaseApproval(t, fixture)
			test.mutate(t, fixture.database)
			_, err := fixture.store.ResumeProjectNetworkReleaseApproval(
				context.Background(),
				ResumeProjectNetworkReleaseApprovalRequest{
					OperationID:               staged.Operation.Operation.ID,
					ExpectedOperationRevision: staged.Operation.Revision,
					Phase:                     "changed plan resume",
					At:                        fixture.at.Add(time.Second),
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v, want containing %q", err, test.want)
			}
			assertHelperApprovalMutationOperation(t, fixture.journal, staged.Operation)
		})
	}
}

// TestStoreProjectNetworkReleaseApprovalResumeRollsBackInvalidTransition proves plan deletion and operation resumption remain atomic.
func TestStoreProjectNetworkReleaseApprovalResumeRollsBackInvalidTransition(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	staged := mustStageProjectNetworkReleaseApproval(t, fixture)
	_, err := fixture.store.ResumeProjectNetworkReleaseApproval(
		context.Background(),
		ResumeProjectNetworkReleaseApprovalRequest{
			OperationID:               staged.Operation.Operation.ID,
			ExpectedOperationRevision: staged.Operation.Revision,
			Phase:                     "resume too early",
			At:                        staged.Operation.Operation.RequestedAt.Add(-time.Second),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "must not precede") {
		t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v, want transition-time failure", err)
	}
	assertHelperApprovalMutationOperation(t, fixture.journal, staged.Operation)
	assertHelperApprovalMutationCounts(t, fixture.database, int64(len(staged.Plans)), 0)
}

// TestStoreProjectNetworkReleaseApprovalResumeRollsBackDeleteAnomalies proves durable authority survives both database errors and unexpected row counts.
func TestStoreProjectNetworkReleaseApprovalResumeRollsBackDeleteAnomalies(t *testing.T) {
	tests := []struct {
		name     string
		callback func(*gorm.DB)
		want     string
	}{
		{name: "delete error", callback: func(tx *gorm.DB) {
			tx.AddError(errors.New("approval plan delete failed"))
		}, want: "approval plan delete failed"},
		{name: "row count", callback: func(tx *gorm.DB) {
			tx.RowsAffected = 0
		}, want: "delete affected 0 rows"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalMutationFixture(t)
			staged := mustStageProjectNetworkReleaseApproval(t, fixture)
			callbackName := "harbor:test_helper_approval_delete_" + strings.ReplaceAll(test.name, " ", "_")
			if test.name == "delete error" {
				if err := fixture.database.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
					if tx.Statement.Table == "helper_approval_plans" {
						test.callback(tx)
					}
				}); err != nil {
					t.Fatalf("register delete error callback: %v", err)
				}
			} else {
				if err := fixture.database.Callback().Delete().After("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
					if tx.Statement.Table == "helper_approval_plans" {
						test.callback(tx)
					}
				}); err != nil {
					t.Fatalf("register row-count callback: %v", err)
				}
			}
			t.Cleanup(func() { _ = fixture.database.Callback().Delete().Remove(callbackName) })

			_, err := fixture.store.ResumeProjectNetworkReleaseApproval(
				context.Background(),
				ResumeProjectNetworkReleaseApprovalRequest{
					OperationID:               staged.Operation.Operation.ID,
					ExpectedOperationRevision: staged.Operation.Revision,
					Phase:                     "host releases verified",
					At:                        fixture.at.Add(time.Second),
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResumeProjectNetworkReleaseApproval() error = %v, want containing %q", err, test.want)
			}
			assertHelperApprovalMutationOperation(t, fixture.journal, staged.Operation)
			assertHelperApprovalMutationCounts(t, fixture.database, int64(len(staged.Plans)), 0)
		})
	}
}

// TestProjectNetworkReleaseApprovalIntentDerivationRejectsIncompleteAuthority covers schema, initialization, marker, and revision ownership boundaries.
func TestProjectNetworkReleaseApprovalIntentDerivationRejectsIncompleteAuthority(t *testing.T) {
	t.Run("schema missing", func(t *testing.T) {
		_, databaseConnection := newOperationJournalTestHarness(t)
		operation := OperationRecord{Operation: domain.Operation{Kind: domain.OperationKindProjectUnregister}}
		if _, err := projectNetworkReleaseApprovalIntentsInTransaction(databaseConnection, operation); err == nil || !strings.Contains(err.Error(), "schema is not installed") {
			t.Fatalf("projectNetworkReleaseApprovalIntentsInTransaction() error = %v, want missing schema", err)
		}
		if _, err := prepareHelperApprovalPlans(databaseConnection, operation, nil); err == nil || !strings.Contains(err.Error(), "schema is not installed") {
			t.Fatalf("prepareHelperApprovalPlans() error = %v, want missing schema", err)
		}
	})

	t.Run("network uninitialized", func(t *testing.T) {
		_, databaseConnection := newNetworkInitializeTestHarness(t, true)
		operation := OperationRecord{Operation: domain.Operation{Kind: domain.OperationKindProjectUnregister}}
		if _, err := projectNetworkReleaseApprovalIntentsInTransaction(databaseConnection, operation); err == nil {
			t.Fatal("projectNetworkReleaseApprovalIntentsInTransaction() accepted an uninitialized network")
		}
		if _, err := prepareHelperApprovalPlans(databaseConnection, operation, nil); err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("prepareHelperApprovalPlans() error = %v, want uninitialized network", err)
		}
	})

	t.Run("operation revision", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		operation := fixture.running
		operation.Revision++
		if _, err := projectNetworkReleaseApprovalIntentsInTransaction(fixture.database, operation); err == nil || !strings.Contains(err.Error(), "owner differs") {
			t.Fatalf("projectNetworkReleaseApprovalIntentsInTransaction() error = %v, want owner mismatch", err)
		}
	})

	t.Run("completed marker", func(t *testing.T) {
		fixture := newHelperApprovalMutationFixture(t)
		if err := fixture.database.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
			t.Fatalf("disable check constraints for corruption fixture: %v", err)
		}
		if err := fixture.database.Exec(
			"UPDATE network_project_releases SET state = ? WHERE operation_id = ?",
			ProjectNetworkReleaseCompleted,
			fixture.running.Operation.ID,
		).Error; err != nil {
			t.Fatalf("complete release marker without evidence: %v", err)
		}
		if _, err := projectNetworkReleaseApprovalIntentsInTransaction(fixture.database, fixture.running); err == nil || !strings.Contains(err.Error(), "must clear its active project reference") {
			t.Fatalf("projectNetworkReleaseApprovalIntentsInTransaction() error = %v, want marker conflict", err)
		}
	})
}

// TestRequireRetiredHelperApprovalPlansDetectsSurvivingAuthority covers the post-delete assertion independently from the mutation transaction.
func TestRequireRetiredHelperApprovalPlansDetectsSurvivingAuthority(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	staged := mustStageProjectNetworkReleaseApproval(t, fixture)
	planIDs, err := helperApprovalPlanIDsInTransaction(fixture.database, staged.Operation.Operation.ID, staged.Operation.Revision)
	if err != nil {
		t.Fatalf("helperApprovalPlanIDsInTransaction() error = %v", err)
	}
	if err := requireRetiredHelperApprovalPlans(fixture.database, staged.Operation, planIDs); err == nil || !strings.Contains(err.Error(), "survived retirement") {
		t.Fatalf("requireRetiredHelperApprovalPlans() error = %v, want surviving plans", err)
	}

	empty := newHelperApprovalMutationFixture(t)
	if err := requireRetiredHelperApprovalPlans(empty.database, empty.running, nil); err != nil {
		t.Fatalf("requireRetiredHelperApprovalPlans(empty) error = %v", err)
	}
}

// TestPrepareHelperApprovalPlansRejectsConflictingLeaseAuthority covers the pending and active lease boundaries used by durable plan preparation.
func TestPrepareHelperApprovalPlansRejectsConflictingLeaseAuthority(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	intents, err := projectNetworkReleaseApprovalIntentsInTransaction(fixture.database, fixture.running)
	if err != nil {
		t.Fatalf("projectNetworkReleaseApprovalIntentsInTransaction() error = %v", err)
	}
	base := intents[0]
	freeAddress := helperApprovalMutationFreeCandidate(t, fixture.release.Record)

	tests := []struct {
		name   string
		mutate func(*HelperApprovalPlanIntent)
		want   string
	}{
		{name: "project", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Key.ProjectID = "project-other"
		}, want: "does not match operation project"},
		{name: "outside pool", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Address = netip.MustParseAddr("127.0.0.1")
		}, want: "not a network pool candidate"},
		{name: "pending active key", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Mutation = helper.OperationEnsureLoopbackIdentity
			intent.LeaseState = ticketissuer.LeasePending
			intent.Lease.Ownership = fixture.release.Record.Ownership
		}, want: "already active"},
		{name: "pending occupied address", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Mutation = helper.OperationEnsureLoopbackIdentity
			intent.LeaseState = ticketissuer.LeasePending
			intent.Lease.Key.SecondaryID = "unallocated"
			intent.Lease.Ownership = fixture.release.Record.Ownership
		}, want: "occupied by durable lease"},
		{name: "active missing key", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Key.SecondaryID = "unallocated"
			intent.Lease.Address = freeAddress
		}, want: "was not found"},
		{name: "active differs", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Address = freeAddress
		}, want: "differs from the exact durable lease"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := base
			test.mutate(&intent)
			if _, err := prepareHelperApprovalPlans(fixture.database, fixture.running, []HelperApprovalPlanIntent{intent}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareHelperApprovalPlans() error = %v, want containing %q", err, test.want)
			}
		})
	}

	pending := base
	pending.Mutation = helper.OperationEnsureLoopbackIdentity
	pending.LeaseState = ticketissuer.LeasePending
	pending.Lease.Key.SecondaryID = "unallocated"
	pending.Lease.Address = freeAddress
	pending.Lease.Ownership = fixture.release.Record.Ownership
	prepared, err := prepareHelperApprovalPlans(fixture.database, fixture.running, []HelperApprovalPlanIntent{pending})
	if err != nil {
		t.Fatalf("prepareHelperApprovalPlans(pending) error = %v", err)
	}
	if len(prepared) != 1 || prepared[0].LeaseID.Valid || !sameHelperApprovalIntent(prepared[0].Intent, pending) {
		t.Fatalf("prepared pending plan = %#v", prepared)
	}
	if err := requireHelperApprovalProject(nil, fixture.running.Operation.ProjectID); err == nil || !strings.Contains(err.Error(), "0 rows") {
		t.Fatalf("requireHelperApprovalProject() error = %v, want missing owner", err)
	}
}

// TestHelperApprovalPlanPersistenceRoundTripsSocketRequirements proves canonical pre-assignment observations survive the generic plan projection.
func TestHelperApprovalPlanPersistenceRoundTripsSocketRequirements(t *testing.T) {
	fixture := newHelperApprovalMutationFixture(t)
	approval, err := fixture.journal.Transition(
		context.Background(),
		fixture.running.Operation.ID,
		fixture.running.Revision,
		domain.OperationRequiresApproval,
		"waiting for candidate approval",
		fixture.at,
		nil,
	)
	if err != nil {
		t.Fatalf("seed approval operation: %v", err)
	}
	intent := HelperApprovalPlanIntent{
		Mutation: helper.OperationEnsureLoopbackIdentity,
		Lease: identity.Lease{
			Key: identity.LeaseKey{
				ProjectID:   approval.Operation.ProjectID,
				SecondaryID: "candidate",
			},
			Address:   helperApprovalMutationFreeCandidate(t, fixture.release.Record),
			Ownership: fixture.release.Record.Ownership,
		},
		LeaseState: ticketissuer.LeasePending,
		Requirements: []hostconflict.SocketRequirement{
			{Transport: hostconflict.TransportTCP4, Port: 443},
			{Transport: hostconflict.TransportUDP4, Port: 53},
		},
	}
	prepared, err := prepareHelperApprovalPlans(fixture.database, approval, []HelperApprovalPlanIntent{intent})
	if err != nil {
		t.Fatalf("prepareHelperApprovalPlans() error = %v", err)
	}
	if err := insertHelperApprovalPlans(fixture.database, approval, prepared); err != nil {
		t.Fatalf("insertHelperApprovalPlans() error = %v", err)
	}
	records, err := readHelperApprovalPlanRecordsInTransaction(fixture.database, approval.Operation.ID, approval.Revision)
	if err != nil {
		t.Fatalf("readHelperApprovalPlanRecordsInTransaction() error = %v", err)
	}
	if len(records) != 1 || !sameHelperApprovalIntent(records[0].Intent, intent) || records[0].leaseID.Valid {
		t.Fatalf("approval plan readback = %#v", records)
	}
	assertHelperApprovalMutationCounts(t, fixture.database, 1, 2)
}

// TestReadHelperApprovalRequirementsRejectsCorruptRows proves durable socket observations fail closed before ticket authority is reconstructed.
func TestReadHelperApprovalRequirementsRejectsCorruptRows(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		port      int
		want      string
	}{
		{name: "port", transport: string(hostconflict.TransportTCP4), port: 0, want: "port is outside"},
		{name: "transport", transport: "sctp4", port: 443, want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalMutationFixture(t)
			staged := mustStageProjectNetworkReleaseApproval(t, fixture)
			var planID int
			if err := fixture.database.Table("helper_approval_plans").Select("min(id)").Scan(&planID).Error; err != nil {
				t.Fatalf("read approval plan ID: %v", err)
			}
			if err := fixture.database.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
				t.Fatalf("disable check constraints for corruption fixture: %v", err)
			}
			if err := fixture.database.Exec(
				"INSERT INTO helper_approval_plan_socket_requirements (helper_approval_plan_id, transport, port) VALUES (?, ?, ?)",
				planID,
				test.transport,
				test.port,
			).Error; err != nil {
				t.Fatalf("seed corrupt socket requirement: %v", err)
			}
			if _, err := readHelperApprovalRequirementsByPlan(fixture.database, []int{planID}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readHelperApprovalRequirementsByPlan() error = %v, want containing %q", err, test.want)
			}
			assertHelperApprovalMutationOperation(t, fixture.journal, staged.Operation)
		})
	}

	empty, err := readHelperApprovalRequirementsByPlan(newHelperApprovalMutationFixture(t).database, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("readHelperApprovalRequirementsByPlan(nil) = %#v, %v", empty, err)
	}
}

// TestHelperApprovalPersistencePropagatesReadFailures proves storage outages cannot be mistaken for absent or retired approval authority.
func TestHelperApprovalPersistencePropagatesReadFailures(t *testing.T) {
	cause := errors.New("approval persistence read failed")
	tests := []struct {
		name  string
		table string
		read  func(*helperApprovalMutationFixture) error
	}{
		{name: "plans", table: "helper_approval_plans", read: func(fixture *helperApprovalMutationFixture) error {
			_, err := readHelperApprovalPlanRecordsInTransaction(fixture.database, fixture.running.Operation.ID, fixture.running.Revision)
			return err
		}},
		{name: "requirements", table: "helper_approval_plan_socket_requirements", read: func(fixture *helperApprovalMutationFixture) error {
			_, err := readHelperApprovalRequirementsByPlan(fixture.database, []int{1})
			return err
		}},
		{name: "identities", table: "helper_approval_plans", read: func(fixture *helperApprovalMutationFixture) error {
			_, err := helperApprovalPlanIDsInTransaction(fixture.database, fixture.running.Operation.ID, fixture.running.Revision)
			return err
		}},
		{name: "retired plans", table: "helper_approval_plans", read: func(fixture *helperApprovalMutationFixture) error {
			return requireRetiredHelperApprovalPlans(fixture.database, fixture.running, nil)
		}},
		{name: "retired requirements", table: "helper_approval_plan_socket_requirements", read: func(fixture *helperApprovalMutationFixture) error {
			return requireRetiredHelperApprovalPlans(fixture.database, fixture.running, []int{1})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHelperApprovalMutationFixture(t)
			callbackName := "harbor:test_helper_approval_read_" + strings.ReplaceAll(test.name, " ", "_")
			if err := fixture.database.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table == test.table {
					tx.AddError(cause)
				}
			}); err != nil {
				t.Fatalf("register query failure callback: %v", err)
			}
			t.Cleanup(func() { _ = fixture.database.Callback().Query().Remove(callbackName) })
			if err := test.read(&fixture); !errors.Is(err, cause) {
				t.Fatalf("approval persistence read error = %v, want sentinel", err)
			}
		})
	}
}

// newHelperApprovalMutationFixture creates production network release and approval schema for one exact unregister owner.
func newHelperApprovalMutationFixture(t *testing.T) helperApprovalMutationFixture {
	t.Helper()
	store, databaseConnection, journal, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
	release, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("BeginProjectNetworkRelease() error = %v", err)
	}
	applyHelperApprovalMutationTestMigration(t, databaseConnection)
	return helperApprovalMutationFixture{
		store:    store,
		database: databaseConnection,
		journal:  journal,
		running:  running,
		begin:    begin,
		release:  release,
		at:       begin.At.Add(time.Second),
	}
}

// applyHelperApprovalMutationTestMigration applies the embedded production approval schema after its network prerequisites.
func applyHelperApprovalMutationTestMigration(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() != helperApprovalMutationTestMigrationName ||
			migration.App() != "harbord" ||
			migration.Connection() != "default" ||
			(migration.Driver() != "" && migration.Driver() != "sqlite") {
			continue
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply embedded helper approval migration: %v", err)
		}
		return
	}
	t.Fatalf("embedded helper approval migration %q was not registered", helperApprovalMutationTestMigrationName)
}

// helperApprovalMutationStageRequest returns valid metadata while the Store derives every release intent itself.
func helperApprovalMutationStageRequest(fixture helperApprovalMutationFixture) StageProjectNetworkReleaseApprovalRequest {
	return StageProjectNetworkReleaseApprovalRequest{
		OperationID:               fixture.running.Operation.ID,
		ExpectedOperationRevision: fixture.running.Revision,
		Phase:                     "waiting for host release approval",
		At:                        fixture.at,
	}
}

// mustStageProjectNetworkReleaseApproval stages the fixture release or fails before lifecycle assertions.
func mustStageProjectNetworkReleaseApproval(
	t *testing.T,
	fixture helperApprovalMutationFixture,
) ProjectNetworkReleaseApprovalResult {
	t.Helper()
	result, err := fixture.store.StageProjectNetworkReleaseApproval(
		context.Background(),
		helperApprovalMutationStageRequest(fixture),
	)
	if err != nil {
		t.Fatalf("StageProjectNetworkReleaseApproval() error = %v", err)
	}
	return result
}

// assertHelperApprovalMutationCounts verifies plan and cascaded requirement cardinality directly.
func assertHelperApprovalMutationCounts(t *testing.T, databaseConnection *gorm.DB, plans int64, requirements int64) {
	t.Helper()
	for _, expectation := range []struct {
		table string
		want  int64
	}{
		{table: "helper_approval_plans", want: plans},
		{table: "helper_approval_plan_socket_requirements", want: requirements},
	} {
		var count int64
		if err := databaseConnection.Table(expectation.table).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", expectation.table, err)
		}
		if count != expectation.want {
			t.Fatalf("%s count = %d, want %d", expectation.table, count, expectation.want)
		}
	}
}

// assertHelperApprovalMutationOperation requires exact lifecycle and revision readback.
func assertHelperApprovalMutationOperation(t *testing.T, journal *OperationJournal, want OperationRecord) {
	t.Helper()
	got, err := journal.Operation(context.Background(), want.Operation.ID)
	if err != nil {
		t.Fatalf("Operation() error = %v", err)
	}
	if got.Revision != want.Revision || got.Operation.State != want.Operation.State || got.Operation.Phase != want.Operation.Phase {
		t.Fatalf("Operation() = %#v, want %#v", got, want)
	}
}

// assertHelperApprovalMutationStale requires the stable optimistic-concurrency error fields.
func assertHelperApprovalMutationStale(
	t *testing.T,
	err error,
	operationID domain.OperationID,
	expected domain.Sequence,
	actual domain.Sequence,
) {
	t.Helper()
	var stale *StaleRevisionError
	if !errors.As(err, &stale) {
		t.Fatalf("error = %v, want StaleRevisionError", err)
	}
	if stale.OperationID != operationID || stale.Expected != expected || stale.Actual != actual {
		t.Fatalf("stale revision = %#v", stale)
	}
}

// helperApprovalMutationFreeCandidate returns one pool address not retained by a durable active lease.
func helperApprovalMutationFreeCandidate(t *testing.T, network NetworkRecord) netip.Addr {
	t.Helper()
	occupied := make(map[netip.Addr]struct{}, len(network.Leases))
	for _, lease := range network.Leases {
		occupied[lease.Address] = struct{}{}
	}
	for _, candidate := range network.Pool.Candidates() {
		if _, exists := occupied[candidate]; !exists {
			return candidate
		}
	}
	t.Fatal("network fixture has no free candidate")
	return netip.Addr{}
}
