//go:build darwin

package trust

import (
	"context"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// darwinTrustCoreFakeNative keeps the Security.framework boundary replaceable for core tests.
type darwinTrustCoreFakeNative struct {
	entries      []darwinTrustEntry
	owned        bool
	releaseCalls int
	releaseErr   error
}

// snapshot returns an independent copy of the fake current-user trust entries.
func (native *darwinTrustCoreFakeNative) snapshot(context.Context) ([]darwinTrustEntry, error) {
	return append([]darwinTrustEntry(nil), native.entries...), nil
}

// ensure leaves the fake store unchanged because core tests exercise observation mapping only.
func (native *darwinTrustCoreFakeNative) ensure(context.Context, Request) error { return nil }

// release records whether Darwin-specific admission reached the native effect boundary.
func (native *darwinTrustCoreFakeNative) release(context.Context, Request) error {
	native.releaseCalls++
	return native.releaseErr
}

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
		entries: []darwinTrustEntry{
			{
				CertificateDER: block.Bytes,
				NativeExact:    true,
			},
		},
		owned: true,
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

// TestDarwinTrustBackendReleasesOnlyExactOwnedObservation prevents drift or competing facts from reaching certificate-only native removal.
func TestDarwinTrustBackendReleasesOnlyExactOwnedObservation(t *testing.T) {
	request := trustTestRequest(t, networkpolicy.DarwinCurrentUserTrust)
	exact := trustExactEntry(request, "owned")
	drifted := cloneEntry(exact)
	drifted.NativeExact = false
	drifted.NativeAttributesSHA256 = darwinTrustAttributesFingerprint(false)
	foreign := cloneEntry(exact)
	foreign.NativeID = "foreign"
	foreign.Owner = nil
	second := cloneEntry(exact)
	second.NativeID = "owned-second"
	for _, test := range []struct {
		name       string
		complete   bool
		entries    []Entry
		wantNative bool
	}{
		{
			name:       "exact owned",
			complete:   true,
			entries:    []Entry{exact},
			wantNative: true,
		},
		{
			name:     "owned drifted",
			complete: true,
			entries:  []Entry{drifted},
		},
		{
			name:     "competing identical entry",
			complete: true,
			entries: []Entry{
				exact,
				foreign,
			},
		},
		{
			name:     "ambiguous ownership",
			complete: true,
			entries: []Entry{
				exact,
				second,
			},
		},
		{
			name:    "incomplete observation",
			entries: []Entry{exact},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			native := &darwinTrustCoreFakeNative{}
			before := Observation{
				Request:  request,
				Complete: test.complete,
				Entries:  test.entries,
			}
			err := newDarwinTrustBackend(native).release(t.Context(), request, before)
			if test.wantNative {
				if err != nil || native.releaseCalls != 1 {
					t.Fatalf("release() error = %v, native calls = %d", err, native.releaseCalls)
				}
				return
			}
			if !errors.Is(err, errNativeMutationConflict) || native.releaseCalls != 0 {
				t.Fatalf("release() error = %v, native calls = %d", err, native.releaseCalls)
			}
		})
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
