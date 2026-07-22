//go:build darwin

package trust

import (
	"context"
	"encoding/pem"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// darwinTrustCoreFakeNative keeps the Security.framework boundary replaceable for core tests.
type darwinTrustCoreFakeNative struct {
	entries []darwinTrustEntry
	owned   bool
}

// snapshot returns an independent copy of the fake current-user trust entries.
func (native *darwinTrustCoreFakeNative) snapshot(context.Context) ([]darwinTrustEntry, error) {
	return append([]darwinTrustEntry(nil), native.entries...), nil
}

// ensure leaves the fake store unchanged because core tests exercise observation mapping only.
func (native *darwinTrustCoreFakeNative) ensure(context.Context, Request) error { return nil }

// release leaves the fake store unchanged because core tests exercise observation mapping only.
func (native *darwinTrustCoreFakeNative) release(context.Context, Request) error { return nil }

// ownerExists returns the configured ownership marker result.
func (native *darwinTrustCoreFakeNative) ownerExists(context.Context, Request) (bool, error) {
	return native.owned, nil
}

// TestDarwinTrustBackendMapsCertificateFacts proves native DER and exactness become bounded CAS facts with ownership.
func TestDarwinTrustBackendMapsCertificateFacts(t *testing.T) {
	root := trustTestRoot(t)
	request, err := NewRequestForRequester("installation-darwin", "501", networkpolicy.DarwinCurrentUserTrust, root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	if block == nil {
		t.Fatal("test root did not contain a PEM block")
	}
	native := &darwinTrustCoreFakeNative{
		entries: []darwinTrustEntry{{CertificateDER: block.Bytes, NativeExact: true}},
		owned:   true,
	}
	observation, err := newDarwinTrustBackend(native).observe(context.Background(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if len(observation.Entries) != 1 || observation.Entries[0].CertificateFingerprint != request.AuthorityFingerprint() {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.Entries[0].Owner == nil || observation.Entries[0].Owner.RequesterIdentity != "501" || !observation.Entries[0].NativeExact {
		t.Fatalf("observation entry = %#v", observation.Entries[0])
	}
}

// TestDarwinRootDERConvertsCanonicalPEM keeps Security.framework from receiving PEM armor instead of certificate DER.
func TestDarwinRootDERConvertsCanonicalPEM(t *testing.T) {
	root := trustTestRoot(t)
	der, err := darwinRootDER(root.CertificatePEM)
	if err != nil {
		t.Fatalf("darwinRootDER() error = %v", err)
	}
	block, rest := pem.Decode(root.CertificatePEM)
	if block == nil || len(rest) != 0 || string(der) != string(block.Bytes) {
		t.Fatalf("darwinRootDER() = %x, want %x", der, block.Bytes)
	}
	if _, err := darwinRootDER(append(append([]byte(nil), root.CertificatePEM...), '\n')); err == nil {
		t.Fatal("darwinRootDER() accepted trailing data")
	}
}
