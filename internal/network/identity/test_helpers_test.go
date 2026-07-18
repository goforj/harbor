package identity

import (
	"net/netip"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// mustAddress parses an address used by an identity test.
func mustAddress(t *testing.T, value string) netip.Addr {
	t.Helper()
	address, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("parse address %q: %v", value, err)
	}
	return address
}

// mustPrefix parses a prefix used by an identity test.
func mustPrefix(t *testing.T, value string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		t.Fatalf("parse prefix %q: %v", value, err)
	}
	return prefix
}

// mustPool builds a validated test pool from address strings.
func mustPool(t *testing.T, values ...string) Pool {
	t.Helper()
	candidates := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		candidates = append(candidates, mustAddress(t, value))
	}
	pool, err := NewPool(mustPrefix(t, "127.77.0.0/24"), candidates)
	if err != nil {
		t.Fatalf("new identity pool: %v", err)
	}
	return pool
}

// mustOwnership builds the current test ownership marker.
func mustOwnership(t *testing.T, installationID string, generation uint64) Ownership {
	t.Helper()
	ownership, err := NewOwnership(InstallationID(installationID), generation)
	if err != nil {
		t.Fatalf("new ownership: %v", err)
	}
	return ownership
}

// mustPrimary builds a validated primary lease key.
func mustPrimary(t *testing.T, projectID string) LeaseKey {
	t.Helper()
	key, err := NewPrimaryKey(domain.ProjectID(projectID))
	if err != nil {
		t.Fatalf("new primary key: %v", err)
	}
	return key
}

// mustSecondary builds a validated secondary lease key.
func mustSecondary(t *testing.T, projectID string, secondaryID string) LeaseKey {
	t.Helper()
	key, err := NewSecondaryKey(domain.ProjectID(projectID), secondaryID)
	if err != nil {
		t.Fatalf("new secondary key: %v", err)
	}
	return key
}

// leaseAddresses returns the address strings from leases in their current order.
func leaseAddresses(leases []Lease) []string {
	addresses := make([]string, 0, len(leases))
	for _, lease := range leases {
		addresses = append(addresses, lease.Address.String())
	}
	return addresses
}

// leaseKeys returns stable rendered keys from leases in their current order.
func leaseKeys(leases []Lease) []string {
	keys := make([]string, 0, len(leases))
	for _, lease := range leases {
		keys = append(keys, formatKey(lease.Key))
	}
	return keys
}
