package harbordruntime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
)

// TestControllerActivateNetworkReplacesEmptyGenerationWithoutStoppingAuthority proves first-run setup becomes live in-process.
func TestControllerActivateNetworkReplacesEmptyGenerationWithoutStoppingAuthority(t *testing.T) {
	full := initializedControllerRuntimeState()
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	authority := &testCertificateAuthority{root: validTestRoot()}
	initial := newTestDataPlane(nil)
	replacement := newTestDataPlane(nil)
	var constructions atomic.Int64
	dependencies := testControllerDependencies(material, authority, initial)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		call := constructions.Add(1)
		if call == 1 {
			if config.Desired.ListenerPlan() != (dataplane.ListenerPlan{}) {
				t.Fatalf("initial listener plan = %#v, want empty", config.Desired.ListenerPlan())
			}
			return initial, nil
		}
		if call != 2 {
			t.Fatalf("data-plane construction call = %d, want at most 2", call)
		}
		setActivationTestSnapshot(replacement, config.Desired)
		return replacement, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	setActivationTestRuntimeState(source, full)
	if err := controller.ActivateNetwork(context.Background(), full.Network.Revision); err != nil {
		t.Fatalf("ActivateNetwork() error = %v", err)
	}
	if initial.closes.Load() != 1 || replacement.starts.Load() != 1 || replacement.closes.Load() != 0 {
		t.Fatalf(
			"activation lifecycle = initial closes %d, replacement starts %d/closes %d",
			initial.closes.Load(),
			replacement.starts.Load(),
			replacement.closes.Load(),
		)
	}
	select {
	case <-controller.Done():
		t.Fatalf("retired empty generation stopped controller: %v", controller.Err())
	default:
	}
	snapshot, err := controller.NetworkSnapshot()
	if err != nil || snapshot.State != dataplane.StateReady || !snapshot.DNS.Configured || !snapshot.Ingress.Configured {
		t.Fatalf("NetworkSnapshot() = %#v, %v; want ready full generation", snapshot, err)
	}

	if err := controller.ActivateNetwork(context.Background(), full.Network.Revision); err != nil {
		t.Fatalf("ActivateNetwork() replay error = %v", err)
	}
	if constructions.Load() != 2 || replacement.starts.Load() != 1 {
		t.Fatalf("activation replay reconstructed runtime: constructions %d, starts %d", constructions.Load(), replacement.starts.Load())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if replacement.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("terminal cleanup = replacement %d, material %d", replacement.closes.Load(), material.closes.Load())
	}
}

// TestControllerActivateNetworkRollsBackFailedCandidate keeps the empty control generation retryable.
func TestControllerActivateNetworkRollsBackFailedCandidate(t *testing.T) {
	full := initializedControllerRuntimeState()
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	initial := newTestDataPlane(nil)
	replacement := newTestDataPlane(nil)
	replacement.startErr = errors.New("listener acquisition failed")
	var constructions atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, initial)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	dependencies.newDataPlane = func(dataplane.Config) (dataPlane, error) {
		if constructions.Add(1) == 1 {
			return initial, nil
		}
		return replacement, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	setActivationTestRuntimeState(source, full)

	err := controller.ActivateNetwork(context.Background(), full.Network.Revision)
	if err == nil || !strings.Contains(err.Error(), "listener acquisition failed") {
		t.Fatalf("ActivateNetwork() error = %v, want candidate failure", err)
	}
	if initial.closes.Load() != 0 || replacement.closes.Load() != 1 {
		t.Fatalf("rollback closes = initial %d, replacement %d", initial.closes.Load(), replacement.closes.Load())
	}
	select {
	case <-controller.Done():
		t.Fatalf("candidate failure stopped retryable controller: %v", controller.Err())
	default:
	}
	snapshot, err := controller.NetworkSnapshot()
	if err != nil || snapshot.State != dataplane.StateReady || snapshot.DNS.Configured || snapshot.Ingress.Configured {
		t.Fatalf("NetworkSnapshot() after rollback = %#v, %v; want ready empty generation", snapshot, err)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerActivateNetworkRequiresExactFullRevision prevents stale approval from publishing another topology.
func TestControllerActivateNetworkRequiresExactFullRevision(t *testing.T) {
	full := initializedControllerRuntimeState()
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	initial := newTestDataPlane(nil)
	dependencies := testControllerDependencies(
		&testMaterialStore{},
		&testCertificateAuthority{root: validTestRoot()},
		initial,
	)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	setActivationTestRuntimeState(source, full)

	for _, test := range []struct {
		name     string
		revision uint64
		mutate   func(*testRuntimeStateSource)
		want     string
	}{
		{name: "zero", revision: 0, mutate: func(*testRuntimeStateSource) {}, want: "between 1"},
		{name: "stale", revision: uint64(full.Network.Revision - 1), mutate: func(*testRuntimeStateSource) {}, want: "expected 20"},
		{name: "resolver stage", revision: uint64(full.Network.Revision), mutate: func(source *testRuntimeStateSource) {
			source.network.Stage = state.NetworkStageResolver
			source.network.Leases = []identity.Lease{}
			source.network.Reservations.Listeners = state.SharedListenerReservations{}
			source.network.Reservations.Endpoints = []state.EndpointReservation{}
		}, want: "not at full stage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setActivationTestRuntimeState(source, full)
			test.mutate(source)
			err := controller.ActivateNetwork(context.Background(), domain.Sequence(test.revision))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ActivateNetwork() error = %v, want containing %q", err, test.want)
			}
			if initial.closes.Load() != 0 {
				t.Fatalf("invalid activation retired current runtime %d times", initial.closes.Load())
			}
		})
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerActivateNetworkMonitorsReplacementFailure proves supervision follows the published generation.
func TestControllerActivateNetworkMonitorsReplacementFailure(t *testing.T) {
	full := initializedControllerRuntimeState()
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	material := &testMaterialStore{}
	initial := newTestDataPlane(nil)
	replacement := newTestDataPlane(nil)
	var constructions atomic.Int64
	dependencies := testControllerDependencies(material, &testCertificateAuthority{root: validTestRoot()}, initial)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		if constructions.Add(1) == 1 {
			return initial, nil
		}
		setActivationTestSnapshot(replacement, config.Desired)
		return replacement, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	setActivationTestRuntimeState(source, full)
	if err := controller.ActivateNetwork(context.Background(), full.Network.Revision); err != nil {
		t.Fatalf("ActivateNetwork() error = %v", err)
	}

	replacement.fail(errors.New("replacement listener failed"))
	waitControllerSignal(t, controller.Done(), "replacement failure cleanup")
	if !errors.Is(controller.Err(), ErrRuntimeStoppedUnexpectedly) || !strings.Contains(controller.Err().Error(), "replacement listener failed") {
		t.Fatalf("Err() = %v, want replacement terminal failure", controller.Err())
	}
	if replacement.closes.Load() != 1 || material.closes.Load() != 1 {
		t.Fatalf("failure cleanup = replacement %d, material %d", replacement.closes.Load(), material.closes.Load())
	}
}

// setActivationTestRuntimeState replaces the fake source with one complete durable instant.
func setActivationTestRuntimeState(source *testRuntimeStateSource, runtimeState state.RuntimeState) {
	source.snapshot = runtimeState.Snapshot
	source.network = runtimeState.Network
	source.networkInitialized = runtimeState.NetworkInitialized
}

// setActivationTestSnapshot mirrors listener ownership for one fake candidate generation.
func setActivationTestSnapshot(runtime *testDataPlane, desired dataplane.DesiredState) {
	listeners := desired.ListenerPlan()
	runtime.snapshot.DNS = dataplane.DNSStatus{
		Configured: listeners.DNS.IsValid(),
		Address:    listeners.DNS,
		Running:    listeners.DNS.IsValid(),
		Records:    len(desired.DNSRecords()),
	}
	runtime.snapshot.Ingress = dataplane.IngressStatus{
		Configured:   listeners.HTTP.IsValid(),
		HTTPAddress:  listeners.HTTP,
		HTTPSAddress: listeners.HTTPS,
		Running:      listeners.HTTP.IsValid(),
		Routes:       len(desired.HTTPRoutes()),
	}
}
