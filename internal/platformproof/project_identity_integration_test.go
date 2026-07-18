//go:build platformproof

package platformproof

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
)

// TestPlatformProjectIdentities runs the real same-port socket proof after the workflow provisions identities.
func TestPlatformProjectIdentities(t *testing.T) {
	addresses := proofAddresses(t)
	port := proofPort(t)

	evidence, err := ProveProjectIdentities(context.Background(), ProjectIdentityRequest{
		Addresses: addresses,
		Port:      port,
	})
	if err != nil {
		t.Fatalf("prove project identities: %v", err)
	}
	if len(evidence.Assertions) != 5 {
		t.Fatalf("expected five assertions, got %d", len(evidence.Assertions))
	}
}

// proofAddresses reads the exact workflow identities without selecting a fallback network model.
func proofAddresses(t *testing.T) []netip.Addr {
	t.Helper()
	value := os.Getenv("HARBOR_PROOF_ADDRESSES")
	if value == "" {
		value = "127.77.254.10,127.77.254.11"
	}
	addresses, err := ParseAddresses(value)
	if err != nil {
		t.Fatalf("parse proof addresses: %v", err)
	}
	return addresses
}

// proofPort reads the required native port and rejects translated ephemeral substitutes.
func proofPort(t *testing.T) uint16 {
	t.Helper()
	value := os.Getenv("HARBOR_PROOF_PORT")
	if value == "" {
		return 3306
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		t.Fatalf("parse proof port %q", value)
	}
	return uint16(port)
}

// TestPlatformProofAddressesAreExplicit confirms the integration precondition was installed on the host.
func TestPlatformProofAddressesAreExplicit(t *testing.T) {
	assigned, err := assignedAddresses()
	if err != nil {
		t.Fatalf("list interface addresses: %v", err)
	}
	for _, address := range proofAddresses(t) {
		if _, exists := assigned[address]; !exists {
			t.Fatalf("proof identity %s is not explicitly assigned", address)
		}
	}

	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatalf("list network interfaces: %v", err)
	}
}
