//go:build platformmutation

package loopback_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platformproof"
)

// platformMutationCleanupTimeout bounds recovery independently from a cancelled proof.
const platformMutationCleanupTimeout = 30 * time.Second

// TestPlatformLoopbackAdapterLifecycle proves Harbor's production adapter owns the complete same-port identity lifecycle.
func TestPlatformLoopbackAdapterLifecycle(t *testing.T) {
	addresses := platformMutationAddresses(t)
	port := platformMutationPort(t)
	adapter := loopback.New()
	initial := platformMutationAbsentFingerprints(t, adapter, addresses)

	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		if err := cleanupPlatformMutationAddresses(adapter, addresses); err != nil {
			t.Errorf("cleanup production loopback identities: %v", err)
		}
	})

	for _, address := range addresses {
		change, err := adapter.EnsureIfObserved(t.Context(), address, initial[address])
		if err != nil {
			t.Fatalf("EnsureIfObserved(%s) error = %v", address, err)
		}
		if !change.Attempted || !change.Changed || change.Indeterminate || change.Before.State != loopback.StateAbsent || change.After.State != loopback.StateExact {
			t.Fatalf("EnsureIfObserved(%s) change = %#v", address, change)
		}

		exactFingerprint, err := change.After.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint ensured identity %s: %v", address, err)
		}
		replayed, err := adapter.EnsureIfObserved(t.Context(), address, exactFingerprint)
		if err != nil {
			t.Fatalf("idempotent EnsureIfObserved(%s) error = %v", address, err)
		}
		if replayed.Attempted || replayed.Changed || replayed.Indeterminate || replayed.After.State != loopback.StateExact {
			t.Fatalf("idempotent EnsureIfObserved(%s) change = %#v", address, replayed)
		}
	}

	proof, err := platformproof.ProveProjectIdentities(t.Context(), platformproof.ProjectIdentityRequest{
		Addresses: addresses,
		Port:      port,
	})
	if err != nil {
		t.Fatalf("prove adapter-created project identities: %v", err)
	}
	if len(proof.Identities) != len(addresses) {
		t.Fatalf("project identity proof count = %d, want %d", len(proof.Identities), len(addresses))
	}

	if err := cleanupPlatformMutationAddresses(adapter, addresses); err != nil {
		t.Fatalf("release production loopback identities: %v", err)
	}
	cleaned = true

	for _, address := range addresses {
		observation, err := adapter.Observe(t.Context(), address)
		if err != nil {
			t.Fatalf("Observe(%s) after release error = %v", address, err)
		}
		if observation.State != loopback.StateAbsent {
			t.Fatalf("Observe(%s) after release state = %q", address, observation.State)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint released identity %s: %v", address, err)
		}
		replayed, err := adapter.ReleaseIfObserved(t.Context(), address, fingerprint)
		if err != nil {
			t.Fatalf("idempotent ReleaseIfObserved(%s) error = %v", address, err)
		}
		if replayed.Attempted || replayed.Changed || replayed.Indeterminate || replayed.After.State != loopback.StateAbsent {
			t.Fatalf("idempotent ReleaseIfObserved(%s) change = %#v", address, replayed)
		}
	}

	if _, err := platformproof.ProveIdentitiesAbsent(platformproof.ProjectIdentityRequest{Addresses: addresses}); err != nil {
		t.Fatalf("prove adapter cleanup: %v", err)
	}
}

// platformMutationAddresses reads the exact workflow identities without selecting a convenient fallback outside CI.
func platformMutationAddresses(t *testing.T) []netip.Addr {
	t.Helper()
	value := os.Getenv("HARBOR_PROOF_ADDRESSES")
	if value == "" {
		t.Fatal("HARBOR_PROOF_ADDRESSES is required for a platform mutation proof")
	}
	addresses, err := platformproof.ParseAddresses(value)
	if err != nil {
		t.Fatalf("parse platform mutation addresses: %v", err)
	}
	return addresses
}

// platformMutationPort reads the unchanged native port shared by both workflow identities.
func platformMutationPort(t *testing.T) uint16 {
	t.Helper()
	value := os.Getenv("HARBOR_PROOF_PORT")
	if value == "" {
		t.Fatal("HARBOR_PROOF_PORT is required for a platform mutation proof")
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		t.Fatalf("parse platform mutation port %q", value)
	}
	return uint16(port)
}

// platformMutationAbsentFingerprints refuses to adopt or remove any address that existed before this proof.
func platformMutationAbsentFingerprints(
	t *testing.T,
	adapter *loopback.Adapter,
	addresses []netip.Addr,
) map[netip.Addr]string {
	t.Helper()
	fingerprints := make(map[netip.Addr]string, len(addresses))
	for _, address := range addresses {
		observation, err := adapter.Observe(t.Context(), address)
		if err != nil {
			t.Fatalf("Observe(%s) before ensure error = %v", address, err)
		}
		if observation.State != loopback.StateAbsent {
			t.Fatalf("proof identity %s existed before Harbor allocation with state %q", address, observation.State)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint absent identity %s: %v", address, err)
		}
		fingerprints[address] = fingerprint
	}
	return fingerprints
}

// cleanupPlatformMutationAddresses conditionally releases only exact assignments created from this proof's absent precondition.
func cleanupPlatformMutationAddresses(adapter *loopback.Adapter, addresses []netip.Addr) error {
	ctx, cancel := context.WithTimeout(context.Background(), platformMutationCleanupTimeout)
	defer cancel()

	var cleanupErrors []error
	for index := len(addresses) - 1; index >= 0; index-- {
		address := addresses[index]
		observation, err := adapter.Observe(ctx, address)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("observe %s: %w", address, err))
			continue
		}
		switch observation.State {
		case loopback.StateAbsent:
			continue
		case loopback.StateExact:
		default:
			cleanupErrors = append(cleanupErrors, fmt.Errorf("refuse to release %s in state %q", address, observation.State))
			continue
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("fingerprint %s: %w", address, err))
			continue
		}
		change, err := adapter.ReleaseIfObserved(ctx, address, fingerprint)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("release %s: %w", address, err))
			continue
		}
		if !change.Attempted || !change.Changed || change.Indeterminate || change.Before.State != loopback.StateExact || change.After.State != loopback.StateAbsent {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("release %s returned unexpected change %#v", address, change))
		}
	}
	return errors.Join(cleanupErrors...)
}
