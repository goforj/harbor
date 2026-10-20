// Package main runs only the preauthorized disposable Windows networking fixture.
package main

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/goforj/harbor/internal/domain"
)

var installationIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// selection binds the fixture to an admitted installation and one exact retained daemon instance.
type selection struct {
	Action         string `json:"action"`
	InstallationID string `json:"installation_id"`
	DaemonPID      uint   `json:"daemon_pid"`
	DaemonBirth    uint64 `json:"daemon_birth"`
}

// validate rejects arbitrary actions and incomplete native identity before connecting or requesting authority.
func (selected selection) validate() error {
	if selected.Action != "setup" && selected.Action != "release" {
		return errors.New("only setup or release fixture actions are allowed")
	}
	if !installationIDPattern.MatchString(selected.InstallationID) || selected.DaemonPID == 0 || uint64(selected.DaemonPID) > uint64(^uint32(0)) || selected.DaemonBirth == 0 {
		return errors.New("fixture requires exact installation and daemon identity")
	}
	return nil
}

// intent creates a fixed installation-bound intent rather than accepting caller-selected operation authority.
func (selected selection) intent(phase string) domain.IntentID {
	return domain.IntentID("intent-winui-" + selected.InstallationID + "-" + phase)
}

// operationFence records only public correlation fields, never helper tickets or invocation nonces.
type operationFence struct {
	ID                 domain.OperationID   `json:"id"`
	Intent             domain.IntentID      `json:"intent"`
	Kind               domain.OperationKind `json:"kind"`
	Revision           domain.Sequence      `json:"revision"`
	Phase              string               `json:"phase"`
	CheckpointRevision domain.Sequence      `json:"checkpoint_revision,omitempty"`
}

// fence rejects crossed or malformed operation responses before a production executor may prepare a ticket.
func fence(operation domain.Operation, revision domain.Sequence, intent domain.IntentID, kind domain.OperationKind) (operationFence, error) {
	if err := operation.Validate(); err != nil {
		return operationFence{}, err
	}
	if operation.IntentID != intent || operation.Kind != kind || operation.ProjectID != "" || revision == 0 || revision > domain.MaximumSequence {
		return operationFence{}, errors.New("fixture operation crossed its exact intent, kind or revision")
	}
	return operationFence{ID: operation.ID, Intent: intent, Kind: kind, Revision: revision, Phase: operation.Phase}, nil
}

// advance admits only the same operation after its selected revision has progressed.
func (selected operationFence) advance(operation domain.Operation, revision domain.Sequence) error {
	next, err := fence(operation, revision, selected.Intent, selected.Kind)
	if err != nil {
		return err
	}
	if next.ID != selected.ID || next.Revision <= selected.Revision {
		return fmt.Errorf("fixture confirmation did not advance selected operation %s", selected.ID)
	}
	return nil
}
