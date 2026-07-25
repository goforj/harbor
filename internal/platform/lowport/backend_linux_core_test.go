package lowport

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// ubuntuNFTFakeNative exposes one bounded in-memory fixed-table boundary.
type ubuntuNFTFakeNative struct {
	state        ubuntuNFTSnapshot
	ensureCalls  int
	releaseCalls int
}

// snapshot returns the injected fixed-table facts.
func (native *ubuntuNFTFakeNative) snapshot(context.Context, Request) (ubuntuNFTSnapshot, error) {
	return native.state, nil
}

// ensure simulates one atomic exact table creation.
func (native *ubuntuNFTFakeNative) ensure(context.Context, Request) error {
	native.ensureCalls++
	native.state = ubuntuNFTSnapshot{
		TablePresent:     true,
		TableOwned:       true,
		TableExact:       true,
		TableFingerprint: strings.Repeat("a", canonicalFingerprintBytes),
		RulesPresent:     true,
		RulesOwned:       true,
		RulesExact:       true,
		RulesFingerprint: strings.Repeat("b", canonicalFingerprintBytes),
	}
	return nil
}

// release simulates one atomic fixed-table deletion.
func (native *ubuntuNFTFakeNative) release(context.Context, Request) error {
	native.releaseCalls++
	native.state = ubuntuNFTSnapshot{}
	return nil
}

// TestUbuntuNFTBackendOwnsOnlyItsExactFixedTable proves the portable ensure and release lifecycle.
func TestUbuntuNFTBackendOwnsOnlyItsExactFixedTable(t *testing.T) {
	request := ubuntuNFTTestRequest(t)
	native := &ubuntuNFTFakeNative{}
	adapter := newAdapter(newUbuntuNFTBackend(native))
	absent, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, fingerprintValidated(absent))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	state, err := ensured.After.State()
	if err != nil || state != StateExact || native.ensureCalls != 1 {
		t.Fatalf("ensure state/calls = %q/%d, error = %v", state, native.ensureCalls, err)
	}
	released, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(ensured.After))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	state, err = released.After.State()
	if err != nil || state != StateAbsent || native.releaseCalls != 1 {
		t.Fatalf("release state/calls = %q/%d, error = %v", state, native.releaseCalls, err)
	}
}

// TestUbuntuNFTBackendRejectsForeignAndDriftedTables proves cleanup cannot become a broad nftables repair.
func TestUbuntuNFTBackendRejectsForeignAndDriftedTables(t *testing.T) {
	request := ubuntuNFTTestRequest(t)
	for _, test := range []struct {
		name  string
		state ubuntuNFTSnapshot
	}{
		{
			name: "foreign",
			state: ubuntuNFTSnapshot{
				TablePresent:     true,
				TableFingerprint: strings.Repeat("a", canonicalFingerprintBytes),
				RulesPresent:     true,
				RulesFingerprint: strings.Repeat("b", canonicalFingerprintBytes),
			},
		},
		{
			name: "owned drift",
			state: ubuntuNFTSnapshot{
				TablePresent:     true,
				TableOwned:       true,
				TableExact:       true,
				TableFingerprint: strings.Repeat("a", canonicalFingerprintBytes),
				RulesPresent:     true,
				RulesOwned:       true,
				RulesFingerprint: strings.Repeat("b", canonicalFingerprintBytes),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &ubuntuNFTFakeNative{state: test.state}
			adapter := newAdapter(newUbuntuNFTBackend(native))
			observation, err := adapter.Observe(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprintValidated(observation)); err == nil {
				t.Fatal("ReleaseIfObserved() accepted unsafe table")
			}
			if native.releaseCalls != 0 {
				t.Fatalf("release calls = %d, want zero", native.releaseCalls)
			}
		})
	}
}

// TestUbuntuNFTOwnerCommentBindsUserAndPolicy pins the bounded table ownership namespace.
func TestUbuntuNFTOwnerCommentBindsUserAndPolicy(t *testing.T) {
	request := ubuntuNFTTestRequest(t)
	want := ubuntuNFTOwnerPrefix + "501|" + request.PolicyFingerprint()
	if got := ubuntuNFTOwnerComment(request); got != want || len(got) > 128 {
		t.Fatalf("ubuntuNFTOwnerComment() = %q", got)
	}
}

// ubuntuNFTTestRequest returns one complete Ubuntu redirected-listener authority.
func ubuntuNFTTestRequest(t *testing.T) Request {
	t.Helper()
	loopback := canonicalLocalhost
	policy, err := networkpolicy.New(
		strings.Repeat("a", 64),
		networkpolicy.UbuntuMechanisms(),
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 25000), Bind: netip.AddrPortFrom(loopback, 25000)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 80), Bind: netip.AddrPortFrom(loopback, 25001)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 443), Bind: netip.AddrPortFrom(loopback, 25002)},
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(testOwnership(fingerprint), policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
