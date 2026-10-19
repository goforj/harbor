package main

import (
	"errors"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/networksetupapproval"
)

// TestResumeNetworkSetupUsesTheRetainedPhase proves the native client selects approval or confirmation from daemon evidence.
func TestResumeNetworkSetupUsesTheRetainedPhase(t *testing.T) {
	for _, phase := range []string{"approval", "confirmation"} {
		t.Run(phase, func(t *testing.T) {
			app, client := connectedTestApp()
			setup := testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			client.snapshot.Operations = append(client.snapshot.Operations, setup.Operation)
			client.snapshot.NetworkSetup = &domain.NetworkSetupSnapshot{Configured: true, Pending: &domain.NetworkSetupRecovery{OperationID: setup.Operation.ID, IntentID: setup.Operation.IntentID, OperationRevision: 7, Kind: "expansion", Phase: phase}}
			confirmation := testNetworkSetupConfirmation(setup, 15, 16)
			confirmation.Repair = true
			confirmation.Expanded = true
			confirmation.ApprovalRevision = 7
			confirmation.Pool = "127.42.0.0/24"
			runner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}}
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			result, err := app.ResumeNetworkSetup()
			if err != nil {
				t.Fatal(err)
			}
			if result.Operation.ID != setup.Operation.ID || runner.requests[0].Resume != (phase == "confirmation") {
				t.Fatal("resume changed operation or selected another phase")
			}
		})
	}
	app, client := connectedTestApp()
	if _, err := app.ResumeNetworkSetup(); err == nil {
		t.Fatal("missing pending state accepted")
	}
	client.snapshotErr = errors.New("offline")
	if _, err := app.ResumeNetworkSetup(); err == nil {
		t.Fatal("offline resume accepted")
	}
}
