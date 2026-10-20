package main

import (
	"testing"
	"time"

	"encoding/json"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"strings"
)

// TestSelectionRefusesIncompleteFixtureAuthority keeps the high tool limited to one fixed action and native identity.
func TestSelectionRefusesIncompleteFixtureAuthority(t *testing.T) {
	selected := selection{"setup", "0123456789abcdef0123456789abcdef", 123, 456}
	if err := selected.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*selection){
		func(s *selection) { s.Action = "arbitrary" },
		func(s *selection) { s.InstallationID = "foreign" },
		func(s *selection) { s.DaemonPID = 0 },
		func(s *selection) { s.DaemonPID = uint(uint64(1) << 32) },
		func(s *selection) { s.DaemonBirth = 0 },
	} {
		copy := selected
		mutate(&copy)
		if err := copy.validate(); err == nil {
			t.Fatalf("accepted incomplete authority: %+v", copy)
		}
	}
	selected.Action = "release"
	if err := selected.validate(); err != nil {
		t.Fatal(err)
	}
	if err := selected.intent("pool").Validate(); err != nil {
		t.Fatal(err)
	}
	if selected.intent("pool") == selected.intent("resolver") {
		t.Fatal("fixture phases share an intent")
	}
}

// fixtureOperation creates one valid exact protocol operation without any machine or daemon effects.
func fixtureOperation(t *testing.T) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation("operation-fixture", "intent-fixture", domain.OperationKindNetworkSetup, "", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

// TestOperationFenceRefusesCrossedSelections rejects another intent, kind, project or sequence before ticket preparation.
func TestOperationFenceRefusesCrossedSelections(t *testing.T) {
	operation := fixtureOperation(t)
	if _, err := fence(operation, 3, "intent-fixture", domain.OperationKindNetworkSetup); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		intent   domain.IntentID
		kind     domain.OperationKind
		revision domain.Sequence
	}{
		{"intent-other", domain.OperationKindNetworkSetup, 3},
		{"intent-fixture", domain.OperationKindNetworkRelease, 3},
		{"intent-fixture", domain.OperationKindNetworkSetup, 0},
		{"intent-fixture", domain.OperationKindNetworkSetup, domain.MaximumSequence + 1},
	} {
		if _, err := fence(operation, test.revision, test.intent, test.kind); err == nil {
			t.Fatalf("accepted crossed selection: %+v", test)
		}
	}
	operation.ID = ""
	if _, err := fence(operation, 3, "intent-fixture", domain.OperationKindNetworkSetup); err == nil {
		t.Fatal("accepted malformed operation")
	}
	operation = fixtureOperation(t)
	operation.ProjectID = "project-foreign"
	if _, err := fence(operation, 3, "intent-fixture", domain.OperationKindNetworkSetup); err == nil {
		t.Fatal("accepted project-scoped authority")
	}
}

// TestOperationFenceRequiresExactProgress refuses stale or unrelated confirmations.
func TestOperationFenceRequiresExactProgress(t *testing.T) {
	operation := fixtureOperation(t)
	selected, err := fence(operation, 3, "intent-fixture", domain.OperationKindNetworkSetup)
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.advance(operation, 4); err != nil {
		t.Fatal(err)
	}
	if err := selected.advance(operation, 3); err == nil {
		t.Fatal("accepted stale confirmation")
	}
	operation.ID = "operation-other"
	if err := selected.advance(operation, 4); err == nil {
		t.Fatal("accepted another operation")
	}
	operation = fixtureOperation(t)
	operation.IntentID = "intent-other"
	if err := selected.advance(operation, 4); err == nil {
		t.Fatal("accepted another intent")
	}
}

// TestPlaneCheckpointPreservesWindowsDirectCompletion accepts the product's direct-listener terminal state without requesting a low-port helper.
func TestPlaneCheckpointPreservesWindowsDirectCompletion(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		state    domain.OperationState
		phase    string
		complete bool
		valid    bool
	}{
		{domain.OperationSucceeded, "completed", true, true},
		{domain.OperationRequiresApproval, "awaiting low-port approval", false, true},
		{domain.OperationRequiresApproval, "awaiting trust approval", false, false},
		{domain.OperationSucceeded, "unexpected", false, false},
	} {
		operation, err := domain.NewOperation("operation-plane", "intent-plane", domain.OperationKindNetworkDataPlaneSetup, "", now)
		if err != nil {
			t.Fatal(err)
		}
		operation, err = operation.Transition(domain.OperationRunning, "preparing", now.Add(time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		operation, err = operation.Transition(test.state, test.phase, now.Add(2*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		complete, err := planeCheckpoint(control.NetworkDataPlaneSetupOperation{Operation: operation, Revision: 3})
		if (err == nil) != test.valid || (err == nil && complete != test.complete) {
			t.Fatalf("checkpoint %s/%s: complete=%t err=%v", test.state, test.phase, complete, err)
		}
	}
	if _, err := planeCheckpoint(control.NetworkDataPlaneSetupOperation{}); err == nil {
		t.Fatal("accepted malformed plane checkpoint")
	}
}

// TestNativeBirthReceiptRetainsDecimalIdentity keeps creation time exact across JSON consumers with limited numeric precision.
func TestNativeBirthReceiptRetainsDecimalIdentity(t *testing.T) {
	data, err := json.Marshal(selection{"setup", "0123456789abcdef0123456789abcdef", 123, 134369590329171600})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"daemon_birth":"134369590329171600"`) {
		t.Fatalf("native birth lost exact string encoding: %s", data)
	}
}
