package domain

import "testing"

// TestNetworkSetupRecoveryValidation checks every input boundary before recovery metadata reaches a client.
func TestNetworkSetupRecoveryValidation(t *testing.T) {
	operation := Operation{ID: "operation-recovery", IntentID: "intent-recovery", Kind: OperationKindNetworkSetup, State: OperationRequiresApproval}
	valid := NetworkSetupRecovery{OperationID: operation.ID, IntentID: operation.IntentID, OperationRevision: 7, Kind: "expansion", Phase: "confirmation"}
	if err := valid.Validate(9, []Operation{operation}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NetworkSetupRecovery){func(v *NetworkSetupRecovery) { v.OperationID = "bad id" }, func(v *NetworkSetupRecovery) { v.IntentID = "bad id" }, func(v *NetworkSetupRecovery) { v.OperationRevision = 0 }, func(v *NetworkSetupRecovery) { v.OperationRevision = 10 }, func(v *NetworkSetupRecovery) { v.OperationRevision = MaximumSequence + 1 }, func(v *NetworkSetupRecovery) { v.Kind = "unknown" }, func(v *NetworkSetupRecovery) { v.Phase = "unknown" }} {
		candidate := valid
		mutate(&candidate)
		if candidate.Validate(9, []Operation{operation}) == nil {
			t.Fatal("invalid recovery accepted")
		}
	}
	if valid.Validate(9, nil) == nil {
		t.Fatal("missing approval accepted")
	}
	operation.State = OperationSucceeded
	if valid.Validate(9, []Operation{operation}) == nil {
		t.Fatal("completed operation advertised as pending")
	}
}
