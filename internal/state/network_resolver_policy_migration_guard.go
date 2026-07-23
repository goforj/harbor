package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkResolverPolicyMigrationActiveError reports a lifecycle mutation blocked by legacy resolver retirement.
type NetworkResolverPolicyMigrationActiveError struct {
	OperationID domain.OperationID
	State       domain.OperationState
	Action      string
}

// Error describes the blocked action and migration operation that owns the network boundary.
func (err *NetworkResolverPolicyMigrationActiveError) Error() string {
	return fmt.Sprintf(
		"cannot %s while network resolver policy migration operation %q is %q",
		err.Action,
		err.OperationID,
		err.State,
	)
}

// rejectNetworkResolverPolicyMigrationEnqueue prevents new runtime routes while resolver retirement awaits approval.
func rejectNetworkResolverPolicyMigrationEnqueue(tx *gorm.DB, operation domain.Operation) error {
	switch operation.Kind {
	case domain.OperationKindProjectStart, domain.OperationKindProjectRestart:
	default:
		return nil
	}
	active, found, err := findActiveNetworkResolverPolicyMigrationOperation(tx)
	if err != nil || !found {
		return err
	}
	return &NetworkResolverPolicyMigrationActiveError{
		OperationID: active.Operation.ID,
		State:       active.Operation.State,
		Action:      "enqueue project runtime operation",
	}
}

// requireNoActiveNetworkResolverPolicyMigrationMutation fences direct project network replacement behind resolver retirement.
func requireNoActiveNetworkResolverPolicyMigrationMutation(tx *gorm.DB, action string) error {
	active, found, err := findActiveNetworkResolverPolicyMigrationOperation(tx)
	if err != nil || !found {
		return err
	}
	return &NetworkResolverPolicyMigrationActiveError{
		OperationID: active.Operation.ID,
		State:       active.Operation.State,
		Action:      action,
	}
}

// requireNoActiveProjectLifecycleOperation prevents migration staging from crossing an already-enqueued project transition.
func requireNoActiveProjectLifecycleOperation(tx *gorm.DB) error {
	var rows []models.Operation
	if err := tx.
		Where(
			"project_id IS NOT NULL AND kind IN ? AND state IN ?",
			[]domain.OperationKind{
				domain.OperationKindProjectStart,
				domain.OperationKindProjectStop,
				domain.OperationKindProjectRestart,
				domain.OperationKindProjectUnregister,
			},
			[]domain.OperationState{
				domain.OperationQueued,
				domain.OperationRunning,
				domain.OperationRequiresApproval,
			},
		).
		Order("revision ASC").
		Limit(1).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read active project lifecycle operation: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"network resolver policy migration requires project lifecycle to be idle; operation %q is %q",
		record.Operation.ID,
		record.Operation.State,
	)
}
