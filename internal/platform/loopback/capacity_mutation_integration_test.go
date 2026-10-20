//go:build platformmutation

package loopback_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/poolpolicy"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platformproof"
)

// TestPlatformLoopbackCapacityLifecycle proves bounded native growth, retry, 100 listeners, and cleanup on isolated runners.
func TestPlatformLoopbackCapacityLifecycle(t *testing.T) {
	value := os.Getenv("HARBOR_CAPACITY_PROOF_POOL")
	if value == "" {
		t.Skip("HARBOR_CAPACITY_PROOF_POOL explicitly selects an isolated native proof range")
	}
	addresses, err := capacityProofAddresses(value)
	if err != nil {
		t.Fatal(err)
	}
	adapter := loopback.New()
	initial := platformMutationAbsentFingerprints(t, adapter, addresses)
	created := map[netip.Addr]string{}
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		if err := cleanupCapacityProof(adapter, addresses, created); err != nil {
			t.Errorf("cleanup capacity proof: %v", err)
		}
	})
	// A retained legacy range and a partial first attempt must replay without changing their exact assignments.
	first := addresses[8:40]
	for _, address := range first {
		change, err := adapter.EnsureIfObserved(t.Context(), address, initial[address])
		if err != nil {
			observation, encodeErr := json.Marshal(change.After)
			t.Fatalf("partial assignment %s: %v; cause=%v; observation=%s; encode=%v", address, err, errors.Unwrap(err), observation, encodeErr)
		}
		if !change.Changed || change.Indeterminate || change.After.State != loopback.StateExact {
			t.Fatal("partial assignment was not verified")
		}
		fingerprint, err := change.After.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		created[address] = fingerprint
	}
	for _, address := range addresses {
		expected := initial[address]
		previous, retained := created[address]
		if retained {
			expected = previous
		}
		change, err := adapter.EnsureIfObserved(t.Context(), address, expected)
		if err != nil {
			observation, encodeErr := json.Marshal(change.After)
			t.Fatalf("resume assignment %s: %v; cause=%v; observation=%s; encode=%v", address, err, errors.Unwrap(err), observation, encodeErr)
		}
		if change.Indeterminate || change.After.State != loopback.StateExact {
			t.Fatal("resumed assignment was not verified")
		}
		fingerprint, err := change.After.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if retained && (change.Attempted || change.Changed || fingerprint != previous) {
			t.Fatal("resume changed a retained assignment")
		}
		created[address] = fingerprint
	}
	proof, err := platformproof.ProveProjectIdentities(t.Context(), platformproof.ProjectIdentityRequest{Addresses: addresses[:100], Port: platformMutationPort(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Identities) != 100 {
		t.Fatal("native proof did not serve 100 simultaneous identities")
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("100-identity native socket evidence: %s", encoded)
	if err := cleanupCapacityProof(adapter, addresses, created); err != nil {
		t.Fatal(err)
	}
	if _, err := platformproof.ProveIdentitiesAbsent(platformproof.ProjectIdentityRequest{Addresses: addresses}); err != nil {
		t.Fatal(err)
	}
	cleaned = true
}

// capacityProofAddresses refuses implicit, partial, relocated, or unbounded host mutation ranges.
func capacityProofAddresses(value string) ([]netip.Addr, error) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.Bits() != poolpolicy.PrefixBits || prefix.String() != value {
		return nil, fmt.Errorf("capacity proof requires an explicit canonical IPv4 loopback /24")
	}
	return poolpolicy.Addresses(prefix)
}

// cleanupCapacityProof removes only exact native fingerprints created during this proof.
func cleanupCapacityProof(adapter *loopback.Adapter, addresses []netip.Addr, created map[netip.Addr]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var failures []error
	for index := len(addresses) - 1; index >= 0; index-- {
		address := addresses[index]
		fingerprint, owned := created[address]
		if !owned {
			continue
		}
		change, err := adapter.ReleaseIfObserved(ctx, address, fingerprint)
		if err != nil {
			failures = append(failures, fmt.Errorf("release %s: %w", address, err))
			continue
		}
		if change.Indeterminate || change.After.State != loopback.StateAbsent {
			failures = append(failures, fmt.Errorf("release %s did not verify absence", address))
			continue
		}
		delete(created, address)
	}
	return errors.Join(failures...)
}

// TestCapacityProofAddressesRejectsUnsafeRanges keeps native mutation opt-in and strictly bounded.
func TestCapacityProofAddressesRejectsUnsafeRanges(t *testing.T) {
	for _, value := range []string{"", "127.77.253.8/29", "127.77.253.1/24", "127.0.0.0/8", "192.0.2.0/24", "::1/128", " 127.77.253.0/24"} {
		if _, err := capacityProofAddresses(value); err == nil {
			t.Fatalf("unsafe capacity proof range accepted: %q", value)
		}
	}
	addresses, err := capacityProofAddresses("127.77.253.0/24")
	if err != nil || len(addresses) != 256 || addresses[255].String() != "127.77.253.255" {
		t.Fatal("incomplete proof range", err)
	}
}
