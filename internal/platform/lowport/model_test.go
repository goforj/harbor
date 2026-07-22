package lowport

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
)

// TestNewRequestBindsSchemaTwoOwnershipToCanonicalDarwinPolicy rejects every caller-controlled authority substitution.
func TestNewRequestBindsSchemaTwoOwnershipToCanonicalDarwinPolicy(t *testing.T) {
	policy := testPolicy(t)
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	record := testOwnership(fingerprint)
	request, err := NewRequest(record, policy)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if request.OwnerUID() != 501 || request.HTTPUpstream() != policy.HTTP.Bind || request.HTTPSUpstream() != policy.HTTPS.Bind {
		t.Fatalf("NewRequest() = %#v", request)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ownership.Record, *networkpolicy.Policy)
	}{
		{"schema", func(r *ownership.Record, _ *networkpolicy.Policy) {
			r.SchemaVersion = ownership.IdentitySchemaVersion
			r.NetworkPolicyFingerprint = ""
		}},
		{"owner", func(r *ownership.Record, _ *networkpolicy.Policy) { r.OwnerIdentity = "S-1-5-18" }},
		{"root", func(r *ownership.Record, _ *networkpolicy.Policy) { r.OwnerIdentity = "0" }},
		{"fingerprint", func(r *ownership.Record, _ *networkpolicy.Policy) {
			r.NetworkPolicyFingerprint = strings.Repeat("b", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := record
			candidatePolicy := policy
			test.mutate(&candidate, &candidatePolicy)
			if _, err := NewRequest(candidate, candidatePolicy); err == nil {
				t.Fatal("NewRequest() accepted invalid authority")
			}
		})
	}
}

// testPolicy returns one canonical Darwin low-port policy.
func testPolicy(t *testing.T) networkpolicy.Policy {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	policy, err := networkpolicy.New(strings.Repeat("a", 64), networkpolicy.MacOSMechanisms(), networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 25000), Bind: netip.AddrPortFrom(loopback, 25000)}, networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 80), Bind: netip.AddrPortFrom(loopback, 25001)}, networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 443), Bind: netip.AddrPortFrom(loopback, 25002)})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// testOwnership returns one valid schema-2 ownership fixture.
func testOwnership(fingerprint string) ownership.Record {
	return ownership.Record{SchemaVersion: ownership.NetworkPolicySchemaVersion, InstallationID: "harbor-test-installation", OwnerIdentity: "501", Generation: 1, LoopbackPoolPrefix: "127.77.0.0/24", NetworkPolicyFingerprint: fingerprint, TicketVerifierKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
}
