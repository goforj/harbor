package state

import (
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"gorm.io/gorm"
)

// readNetworkSetupRecovery projects only current plan authority and independently retained confirmation evidence.
func readNetworkSetupRecovery(tx *gorm.DB, snapshot domain.Snapshot) (*domain.NetworkSetupRecovery, error) {
	for _, operation := range snapshot.Operations {
		if operation.Kind != domain.OperationKindNetworkSetup || operation.State != domain.OperationRequiresApproval {
			continue
		}
		present, err := networkSetupConfirmationTableExists(tx)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, nil
		}
		plan, err := resolveNetworkSetupPoolPlan(tx, operation.ID)
		if err != nil {
			return nil, fmt.Errorf("read pending network setup: %w", err)
		}
		recovery := &domain.NetworkSetupRecovery{OperationID: operation.ID, IntentID: operation.IntentID, OperationRevision: plan.OperationRevision, Kind: "setup", Phase: "approval"}
		if plan.Mode == ticketissuer.PoolModeRepair {
			recovery.Kind = "repair"
		}
		if plan.PreviousPool != "" {
			recovery.Kind = "expansion"
		}
		if _, err := networkSetupConfirmationForPlan(tx, plan); err == nil {
			recovery.Phase = "confirmation"
		} else if !errors.Is(err, ErrNetworkSetupConfirmationUnavailable) {
			return nil, err
		}
		if err := recovery.Validate(snapshot.Sequence, snapshot.Operations); err != nil {
			return nil, err
		}
		return recovery, nil
	}
	return nil, nil
}
