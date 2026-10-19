package domain

import "fmt"

// NetworkSetupRecovery identifies the exact pending operation without exposing helper capabilities or evidence.
type NetworkSetupRecovery struct {
	OperationID       OperationID `json:"operation_id"`
	IntentID          IntentID    `json:"intent_id"`
	OperationRevision Sequence    `json:"operation_revision"`
	Kind              string      `json:"kind"`
	Phase             string      `json:"phase"`
}

// Validate rejects ambiguous recovery actions and revisions outside the authoritative snapshot.
func (recovery NetworkSetupRecovery) Validate(sequence Sequence, operations []Operation) error {
	if err := recovery.OperationID.Validate(); err != nil {
		return err
	}
	if err := recovery.IntentID.Validate(); err != nil {
		return err
	}
	if recovery.OperationRevision == 0 || recovery.OperationRevision > MaximumSequence || recovery.OperationRevision > sequence {
		return fmt.Errorf("network setup recovery revision exceeds the current snapshot")
	}
	if recovery.Kind != "setup" && recovery.Kind != "repair" && recovery.Kind != "expansion" {
		return fmt.Errorf("network setup recovery kind is invalid")
	}
	if recovery.Phase != "approval" && recovery.Phase != "confirmation" {
		return fmt.Errorf("network setup recovery phase is invalid")
	}
	for _, operation := range operations {
		if operation.ID == recovery.OperationID && operation.IntentID == recovery.IntentID && operation.Kind == OperationKindNetworkSetup && operation.ProjectID == "" && operation.State == OperationRequiresApproval {
			return nil
		}
	}
	return fmt.Errorf("network setup recovery is not bound to a pending global approval")
}
