package ownership

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestStoreExpandPreservesAuthorityAndReplays proves bounded growth survives reopening without changing installation identity.
func TestStoreExpandPreservesAuthorityAndReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	source := testNetworkPolicyRecord()
	source.LoopbackPoolPrefix = "127.77.0.8/29"
	claimed, err := store.Claim(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	target := source
	target.LoopbackPoolPrefix = "127.77.0.0/24"
	first, err := store.Expand(t.Context(), source.LoopbackPoolPrefix, claimed.Fingerprint, target)
	if err != nil || first.Record != target {
		t.Fatalf("Expand() = %v, %v", first, err)
	}
	reopened := openTestStore(t, path)
	replay, err := reopened.Expand(t.Context(), source.LoopbackPoolPrefix, claimed.Fingerprint, target)
	if err != nil || replay != first {
		t.Fatalf("replay = %v, %v", replay, err)
	}
	if _, err := store.Expand(t.Context(), source.LoopbackPoolPrefix, "bad fingerprint", target); err == nil {
		t.Fatal("stale source accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Expand(ctx, source.LoopbackPoolPrefix, claimed.Fingerprint, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled expansion: %v", err)
	}
}

// TestValidatePoolExpansionRejectsAuthorityChanges proves growth cannot rotate keys, relocate, shrink, or claim another owner.
func TestValidatePoolExpansionRejectsAuthorityChanges(t *testing.T) {
	source := testRecord()
	source.LoopbackPoolPrefix = "127.77.0.8/29"
	target := source
	target.LoopbackPoolPrefix = "127.77.0.0/24"
	for _, mutate := range []func(*Record){func(r *Record) { r.Generation++ }, func(r *Record) { r.OwnerIdentity = "99" }, func(r *Record) { r.InstallationID = "other-installation" }, func(r *Record) { r.LoopbackPoolPrefix = "127.77.1.0/24" }, func(r *Record) { r.LoopbackPoolPrefix = "127.77.0.8/29" }, func(r *Record) { r.LoopbackPoolPrefix = "127.77.0.0/23" }, func(r *Record) { r.TicketVerifierKey = "invalid" }} {
		changed := target
		mutate(&changed)
		if err := ValidatePoolExpansion(source, changed); err == nil {
			t.Fatalf("authority drift accepted: %v", changed)
		}
	}
	if err := ValidatePoolExpansion(target, source); err == nil {
		t.Fatal("shrink accepted")
	}
}

// TestStoreExpandRejectsMissingOrCompetingClaim keeps all host authority untouched after a failed compare-and-swap.
func TestStoreExpandRejectsMissingOrCompetingClaim(t *testing.T) {
	source := testRecord()
	source.LoopbackPoolPrefix = "127.77.0.8/29"
	target := source
	target.LoopbackPoolPrefix = "127.77.0.0/24"
	fingerprint, _ := source.Fingerprint()
	store := openTestStore(t, filepath.Join(t.TempDir(), "owner.json"))
	if _, err := store.Expand(t.Context(), source.LoopbackPoolPrefix, fingerprint, target); err == nil {
		t.Fatal("missing source accepted")
	}
	other := source
	other.Generation++
	claimed, err := store.Claim(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Expand(t.Context(), source.LoopbackPoolPrefix, fingerprint, target); err == nil {
		t.Fatal("competing generation accepted")
	}
	after, err := store.Observe(t.Context())
	if err != nil || after != claimed {
		t.Fatal("failed expansion changed protected claim", err)
	}
}
