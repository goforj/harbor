package harbordruntime

import (
	"context"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// TestResolverDesiredStateOnlyOwnsDNS keeps resolver completion independent from the later HTTPS and low-port gates.
func TestResolverDesiredStateOnlyOwnsDNS(t *testing.T) {
	runtimeState := resolverStageControllerRuntimeState()
	desired, err := resolverDesiredState(runtimeState, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("resolverDesiredState() error = %v", err)
	}
	if desired.ListenerPlan().DNS == (netip.AddrPort{}) {
		t.Fatal("resolver desired state omitted the authoritative DNS listener")
	}
	if desired.ListenerPlan().HTTP != (netip.AddrPort{}) || desired.ListenerPlan().HTTPS != (netip.AddrPort{}) {
		t.Fatalf("resolver desired state claimed web listeners: %#v", desired.ListenerPlan())
	}
	if len(desired.DNSRecords()) != 0 || len(desired.HTTPRoutes()) != 0 || len(desired.NativeRoutes()) != 0 {
		t.Fatalf("resolver desired state published routes: DNS %v, HTTP %v, native %v", desired.DNSRecords(), desired.HTTPRoutes(), desired.NativeRoutes())
	}
}

// TestControllerStartsResolverGenerationOnRestart proves a daemon restart does not lose a completed resolver listener.
func TestControllerStartsResolverGenerationOnRestart(t *testing.T) {
	runtimeState := resolverStageControllerRuntimeState()
	source := &testRuntimeStateSource{
		snapshot:           runtimeState.Snapshot,
		network:            runtimeState.Network,
		networkInitialized: true,
	}
	runtime := newTestDataPlane(nil)
	var captured dataplane.Config
	dependencies := testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, runtime)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		captured = config
		setActivationTestSnapshot(runtime, config.Desired)
		return runtime, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if captured.Desired.ListenerPlan().DNS == (netip.AddrPort{}) || captured.Desired.ListenerPlan().HTTP != (netip.AddrPort{}) {
		t.Fatalf("restart resolver listener plan = %#v", captured.Desired.ListenerPlan())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerActivateResolverPublishesDNSWithoutClaimingIngress proves setup confirmation replaces the empty generation.
func TestControllerActivateResolverPublishesDNSWithoutClaimingIngress(t *testing.T) {
	identity := resolverStageControllerRuntimeState()
	identity.Network.Stage = state.NetworkStageIdentity
	identity.Network.Reservations.Listeners = state.SharedListenerReservations{}
	identity.Network.Reservations.Endpoints = []state.EndpointReservation{}
	source := &testRuntimeStateSource{
		snapshot:           identity.Snapshot,
		network:            identity.Network,
		networkInitialized: true,
	}
	initial := newTestDataPlane(nil)
	replacement := newTestDataPlane(nil)
	var constructions atomic.Int64
	dependencies := testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, initial)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	dependencies.newDataPlane = func(config dataplane.Config) (dataPlane, error) {
		if constructions.Add(1) == 1 {
			if !config.Desired.Empty() {
				t.Fatalf("initial listener plan = %#v, want empty", config.Desired.ListenerPlan())
			}
			return initial, nil
		}
		if config.Desired.ListenerPlan().DNS == (netip.AddrPort{}) || config.Desired.ListenerPlan().HTTP != (netip.AddrPort{}) {
			t.Fatalf("resolver replacement listener plan = %#v", config.Desired.ListenerPlan())
		}
		setActivationTestSnapshot(replacement, config.Desired)
		return replacement, nil
	}
	controller := newFakeController(t, source, dependencies)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	resolverState := resolverStageControllerRuntimeState()
	setActivationTestRuntimeState(source, resolverState)
	if err := controller.ActivateResolver(context.Background(), resolverState.Network.Revision); err != nil {
		t.Fatalf("ActivateResolver() error = %v", err)
	}
	if initial.closes.Load() != 1 || replacement.starts.Load() != 1 || replacement.closes.Load() != 0 {
		t.Fatalf("resolver activation lifecycle = initial closes %d, replacement starts %d/closes %d", initial.closes.Load(), replacement.starts.Load(), replacement.closes.Load())
	}
	snapshot, err := controller.NetworkSnapshot()
	if err != nil {
		t.Fatalf("NetworkSnapshot() error = %v", err)
	}
	if snapshot.State != dataplane.StateReady || !snapshot.DNS.Configured || snapshot.Ingress.Configured {
		t.Fatalf("NetworkSnapshot() = %#v, want ready DNS-only generation", snapshot)
	}
	if err := controller.ActivateResolver(context.Background(), resolverState.Network.Revision); err != nil {
		t.Fatalf("ActivateResolver() replay error = %v", err)
	}
	if constructions.Load() != 2 {
		t.Fatalf("resolver replay reconstructed generation: constructions = %d", constructions.Load())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// resolverStageControllerRuntimeState returns the durable shape after resolver approval and before full ingress setup.
func resolverStageControllerRuntimeState() state.RuntimeState {
	runtimeState := initializedControllerRuntimeState()
	runtimeState.Network.Stage = state.NetworkStageResolver
	runtimeState.Network.Reservations.Listeners = state.SharedListenerReservations{}
	runtimeState.Network.Reservations.Endpoints = []state.EndpointReservation{}
	return runtimeState
}
