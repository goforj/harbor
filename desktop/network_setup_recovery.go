package main

import (
	"fmt"

	"github.com/goforj/harbor/internal/control"
)

// ResumeNetworkSetup continues only the daemon's retained operation and its verified current phase.
func (a *App) ResumeNetworkSetup() (control.NetworkSetupOperation, error) {
	ctx, client, release, err := a.leaseCurrentConnection()
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	defer release()
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate pending Harbor setup: %w", err)
	}
	if snapshot.NetworkSetup == nil || snapshot.NetworkSetup.Pending == nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("Harbor has no pending network setup to resume")
	}
	pending := snapshot.NetworkSetup.Pending
	var setup control.NetworkSetupOperation
	for _, operation := range snapshot.Operations {
		if operation.ID == pending.OperationID {
			setup = control.NetworkSetupOperation{Operation: operation, Revision: pending.OperationRevision}
			break
		}
	}
	if err := setup.Validate(); err != nil {
		return control.NetworkSetupOperation{}, err
	}
	result, err := a.runNetworkSetupApproval(ctx, client, setup, pending.Phase == "confirmation")
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	if pending.Kind == "setup" {
		if err := a.completeNetworkResolverSetup(ctx, client); err != nil {
			return control.NetworkSetupOperation{}, err
		}
		if err := a.completeNetworkDataPlaneSetup(ctx, client); err != nil {
			return control.NetworkSetupOperation{}, err
		}
	}
	return result, nil
}
