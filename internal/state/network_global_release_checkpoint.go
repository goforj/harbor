package state

import (
	"fmt"
	"strconv"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// validateGlobalNetworkReleaseCheckpoint proves one release checkpoint is visible, phase-consistent, and its sole sequence owner.
func validateGlobalNetworkReleaseCheckpoint(
	tx *gorm.DB,
	plan GlobalNetworkReleasePlanRecord,
	highWater domain.Sequence,
) error {
	if err := validateVisibleSequence(
		highWater,
		plan.CheckpointRevision,
		"global network release checkpoint",
		nil,
	); err != nil {
		return err
	}
	history, err := requireExactGlobalNetworkReleaseHistory(tx, plan.Operation)
	if err != nil {
		return err
	}
	if err := validateOperationHistorySequenceOwners(tx, plan.Operation, history); err != nil {
		return err
	}
	if plan.Phase == GlobalNetworkReleasePlanPhaseRuntimeRelease {
		if plan.CheckpointRevision != plan.Operation.Revision {
			return corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"runtime checkpoint revision %d differs from operation revision %d",
					plan.CheckpointRevision,
					plan.Operation.Revision,
				),
			)
		}
		return nil
	}
	if plan.CheckpointRevision <= plan.Operation.Revision {
		return corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf(
				"advanced checkpoint revision %d must follow operation revision %d",
				plan.CheckpointRevision,
				plan.Operation.Revision,
			),
		)
	}
	return validateGlobalNetworkReleaseCheckpointSequenceOwner(tx, plan)
}

// requireExactGlobalNetworkReleaseHistory proves checkpointing cannot append or reinterpret lifecycle edges.
func requireExactGlobalNetworkReleaseHistory(
	tx *gorm.DB,
	operation OperationRecord,
) ([]OperationTransition, error) {
	history, err := operationHistoryInTransaction(tx, operation)
	if err != nil {
		return nil, err
	}
	if len(history) != 2 || operation.Revision != history[1].Sequence {
		return nil, corruptGlobalNetworkReleasePlan(
			operation.Operation.ID,
			fmt.Errorf("operation history does not contain exactly two staging edges"),
		)
	}
	states := [...]domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
	}
	phases := [...]string{
		string(domain.OperationQueued),
		globalNetworkReleaseRuntimeOperationPhase,
	}
	for index := range history {
		if history[index].State != states[index] ||
			history[index].Phase != phases[index] ||
			!history[index].OccurredAt.Equal(operation.Operation.RequestedAt) ||
			(index > 0 && history[index].Sequence != history[index-1].Sequence+1) {
			return nil, corruptGlobalNetworkReleasePlan(
				operation.Operation.ID,
				fmt.Errorf("operation history differs from fixed global release staging"),
			)
		}
	}
	return history, nil
}

// validateGlobalNetworkReleaseCheckpointSequenceOwner rejects reuse of an advanced checkpoint revision.
func validateGlobalNetworkReleaseCheckpointSequenceOwner(
	tx *gorm.DB,
	plan GlobalNetworkReleasePlanRecord,
) error {
	sequence := int(plan.CheckpointRevision)
	owner := "global network release checkpoint " + strconv.Quote(string(plan.Operation.Operation.ID))

	var projects []models.Project
	if err := tx.
		Select("id", "project_id").
		Where("revision = ?", sequence).
		Order("id ASC").
		Find(&projects).Error; err != nil {
		return fmt.Errorf("verify global release checkpoint project owners: %w", err)
	}
	if len(projects) != 0 {
		return sequenceOwnerCollision(
			sequence,
			owner,
			"project "+strconv.Quote(projects[0].ProjectId),
		)
	}

	var recents []models.RecentResource
	if err := tx.
		Select("id", "project_id", "resource_id").
		Where("sequence = ?", sequence).
		Order("id ASC").
		Find(&recents).Error; err != nil {
		return fmt.Errorf("verify global release checkpoint recent owners: %w", err)
	}
	if len(recents) != 0 {
		return sequenceOwnerCollision(
			sequence,
			owner,
			fmt.Sprintf(
				"recent resource %q/%q",
				recents[0].ProjectId,
				recents[0].ResourceId,
			),
		)
	}

	var operations []models.Operation
	if err := tx.
		Select("id").
		Where("revision = ?", sequence).
		Order("id ASC").
		Find(&operations).Error; err != nil {
		return fmt.Errorf("verify global release checkpoint operation owners: %w", err)
	}
	if len(operations) != 0 {
		return sequenceOwnerCollision(
			sequence,
			owner,
			"operation "+strconv.Quote(operations[0].Id),
		)
	}

	var transitions []models.OperationTransition
	if err := tx.
		Select("id", "operation_id", "ordinal").
		Where("sequence = ?", sequence).
		Order("id ASC").
		Find(&transitions).Error; err != nil {
		return fmt.Errorf("verify global release checkpoint transition owners: %w", err)
	}
	if len(transitions) != 0 {
		return sequenceOwnerCollision(
			sequence,
			owner,
			fmt.Sprintf(
				"operation transition %q ordinal %d",
				transitions[0].OperationId,
				transitions[0].Ordinal,
			),
		)
	}
	return validateNetworkSequenceCollision(tx, sequence, owner)
}
