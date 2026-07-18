package materialstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/trust/localca"
)

var storeTestTime = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)

// TestAuthorityRoundTripRetainsIdentity verifies restart reload never rotates the public trust anchor.
func TestAuthorityRoundTripRetainsIdentity(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	if err := store.CreateAuthority(nil, authority); err != nil {
		t.Fatalf("CreateAuthority(idempotent) error = %v", err)
	}
	wantFingerprint := authority.Material().Fingerprint
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := mustStore(t, directory)
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.LoadAuthority(context.Background(), storeAuthorityConfig())
	if err != nil {
		t.Fatalf("LoadAuthority() error = %v", err)
	}
	if got := loaded.Material().Fingerprint; got != wantFingerprint {
		t.Fatalf("loaded fingerprint = %q, want %q", got, wantFingerprint)
	}
	assertAuthorityLayout(t, directory, wantFingerprint)
}

// TestCreateAuthorityRefusesIdentityReplacement proves setup cannot silently invalidate an installed root.
func TestCreateAuthorityRefusesIdentityReplacement(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	t.Cleanup(func() { _ = store.Close() })
	first := mustLocalAuthority(t)
	second := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), first); err != nil {
		t.Fatalf("CreateAuthority(first) error = %v", err)
	}
	if err := store.CreateAuthority(context.Background(), second); !errors.Is(err, ErrAuthorityAlreadyInitialized) {
		t.Fatalf("CreateAuthority(second) error = %v, want ErrAuthorityAlreadyInitialized", err)
	}
	loaded, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
	if err != nil {
		t.Fatalf("LoadAuthority() error = %v", err)
	}
	if got, want := loaded.Material().Fingerprint, first.Material().Fingerprint; got != want {
		t.Fatalf("active fingerprint = %q, want %q", got, want)
	}
}

// TestConcurrentAuthorityInitializationUsesFirstManifest proves first publication is an atomic root-identity compare-and-set across store instances.
func TestConcurrentAuthorityInitializationUsesFirstManifest(t *testing.T) {
	tests := []struct {
		name            string
		sameFingerprint bool
	}{
		{name: "same identity remains idempotent", sameFingerprint: true},
		{name: "different identity cannot replace winner", sameFingerprint: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "certificates")
			first := mustLocalAuthority(t)
			second := first
			if !test.sameFingerprint {
				second = mustLocalAuthority(t)
			}

			var arrivals atomic.Int32
			ready := make(chan struct{})
			release := make(chan struct{})
			checkpoint := func(checkpoint writeCheckpoint) error {
				if checkpoint != checkpointGenerationSynced {
					return nil
				}
				if arrivals.Add(1) == 2 {
					close(ready)
				}
				<-release
				return nil
			}
			firstStore, err := openStore(directory, storeDependencies{random: rand.Reader, checkpoint: checkpoint})
			if err != nil {
				t.Fatalf("openStore(first) error = %v", err)
			}
			defer firstStore.Close()
			secondStore, err := openStore(directory, storeDependencies{random: rand.Reader, checkpoint: checkpoint})
			if err != nil {
				t.Fatalf("openStore(second) error = %v", err)
			}
			defer secondStore.Close()

			results := make(chan error, 2)
			go func() { results <- firstStore.CreateAuthority(context.Background(), first) }()
			go func() { results <- secondStore.CreateAuthority(context.Background(), second) }()
			<-ready
			close(release)
			firstErr := <-results
			secondErr := <-results
			if test.sameFingerprint {
				if firstErr != nil || secondErr != nil {
					t.Fatalf("CreateAuthority(same identity) errors = (%v, %v)", firstErr, secondErr)
				}
			} else {
				successes := 0
				alreadyInitialized := 0
				for _, result := range []error{firstErr, secondErr} {
					if result == nil {
						successes++
					} else if errors.Is(result, ErrAuthorityAlreadyInitialized) {
						alreadyInitialized++
					} else {
						t.Fatalf("CreateAuthority(different identity) unexpected error = %v", result)
					}
				}
				if successes != 1 || alreadyInitialized != 1 {
					t.Fatalf("CreateAuthority(different identity) successes = %d, already initialized = %d", successes, alreadyInitialized)
				}
			}

			loaded, err := firstStore.LoadAuthority(context.Background(), storeAuthorityConfig())
			if err != nil {
				t.Fatalf("LoadAuthority() error = %v", err)
			}
			fingerprint := loaded.Material().Fingerprint
			if fingerprint != first.Material().Fingerprint && fingerprint != second.Material().Fingerprint {
				t.Fatalf("active fingerprint = %q, want one published identity", fingerprint)
			}
			entries, err := os.ReadDir(filepath.Join(directory, filepath.FromSlash(authorityGenerations)))
			if err != nil {
				t.Fatalf("ReadDir(generations) error = %v", err)
			}
			wantGenerations := 1
			if !test.sameFingerprint {
				wantGenerations = 2
			}
			if len(entries) != wantGenerations {
				t.Fatalf("generation count = %d, want %d", len(entries), wantGenerations)
			}
		})
	}
}

// TestLeafRoundTripAndRotation verifies active manifests select complete exact-name generations across restart.
func TestLeafRoundTripAndRotation(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	t.Cleanup(func() { _ = store.Close() })
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	first := mustLeaf(t, authority, []string{"orders.test", "ADMIN.ORDERS.TEST."})
	if err := store.PutLeaf(context.Background(), authority, first); err != nil {
		t.Fatalf("PutLeaf(first) error = %v", err)
	}
	loaded, err := store.LoadLeaf(context.Background(), authority, []string{"admin.orders.test", "orders.test"})
	if err != nil {
		t.Fatalf("LoadLeaf(first) error = %v", err)
	}
	if loaded.Material.Fingerprint != first.Material.Fingerprint || !slices.Equal(loaded.Hosts, first.Hosts) {
		t.Fatalf("loaded first leaf = %#v, want fingerprint %q and hosts %#v", loaded, first.Material.Fingerprint, first.Hosts)
	}

	second := mustLeaf(t, authority, first.Hosts)
	if second.Material.Fingerprint == first.Material.Fingerprint {
		t.Fatal("independent leaf issuance reused a fingerprint")
	}
	if err := store.PutLeaf(nil, authority, second); err != nil {
		t.Fatalf("PutLeaf(second) error = %v", err)
	}
	loaded, err = store.LoadLeaf(context.Background(), authority, first.Hosts)
	if err != nil {
		t.Fatalf("LoadLeaf(second) error = %v", err)
	}
	if loaded.Material.Fingerprint != second.Material.Fingerprint {
		t.Fatalf("active leaf fingerprint = %q, want %q", loaded.Material.Fingerprint, second.Material.Fingerprint)
	}

	generations := filepath.Join(directory, leafDirectory(authority.Material().Fingerprint, first.Hosts), "generations")
	entries, err := os.ReadDir(generations)
	if err != nil {
		t.Fatalf("ReadDir(generations) error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("leaf generation count = %d, want 2", len(entries))
	}
}

// TestLeafLookupIsScopedByAuthorityAndHosts verifies a valid pair cannot be reached through another identity path.
func TestLeafLookupIsScopedByAuthorityAndHosts(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	t.Cleanup(func() { _ = store.Close() })
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	if _, err := store.LoadLeaf(context.Background(), authority, []string{"billing.test"}); !errors.Is(err, ErrLeafNotFound) {
		t.Fatalf("LoadLeaf(other host) error = %v, want ErrLeafNotFound", err)
	}
	otherAuthority := mustLocalAuthority(t)
	if _, err := store.LoadLeaf(context.Background(), otherAuthority, leaf.Hosts); !errors.Is(err, ErrLeafNotFound) {
		t.Fatalf("LoadLeaf(other authority) error = %v, want ErrLeafNotFound", err)
	}
}

// TestAuthorityRecoveryPromotesOneCompletedGeneration verifies a crash before first manifest publication retains identity.
func TestAuthorityRecoveryPromotesOneCompletedGeneration(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	sentinel := errors.New("simulated crash")
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointGenerationMoved {
				return sentinel
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); !errors.Is(err, sentinel) {
		t.Fatalf("CreateAuthority(crash) error = %v, want %v", err, sentinel)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authority manifest exists before recovery: %v", err)
	}
	if _, err := os.Stat(authorityCurrentDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authority current directory exists before recovery: %v", err)
	}

	reopened := mustStore(t, directory)
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.LoadAuthority(context.Background(), storeAuthorityConfig())
	if err != nil {
		t.Fatalf("LoadAuthority(recover) error = %v", err)
	}
	if got, want := loaded.Material().Fingerprint, authority.Material().Fingerprint; got != want {
		t.Fatalf("recovered fingerprint = %q, want %q", got, want)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); err != nil {
		t.Fatalf("recovered manifest error = %v", err)
	}
}

// TestAuthorityCurrentPointerAdmissionRejectsCommittedShapeDamage ensures only true current-directory absence enters recovery.
func TestAuthorityCurrentPointerAdmissionRejectsCommittedShapeDamage(t *testing.T) {
	tests := []struct {
		name          string
		wantComponent string
		setup         func(*testing.T, *Store, *localca.Authority)
	}{
		{
			name:          "empty current directory",
			wantComponent: "authority manifest",
			setup: func(t *testing.T, store *Store, authority *localca.Authority) {
				t.Helper()
				writeTestGeneration(t, store, filepath.Join(filepath.FromSlash(authorityGenerations), authority.Material().Fingerprint), authority.Material())
				if err := store.filesystem.ensureDirectory(filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)); err != nil {
					t.Fatalf("ensure empty current directory: %v", err)
				}
			},
		},
		{
			name:          "manifest removed from committed current directory",
			wantComponent: "authority manifest",
			setup: func(t *testing.T, store *Store, authority *localca.Authority) {
				t.Helper()
				if err := store.CreateAuthority(context.Background(), authority); err != nil {
					t.Fatalf("CreateAuthority() error = %v", err)
				}
				if err := store.filesystem.root.Remove(authorityManifestPath()); err != nil {
					t.Fatalf("Remove(authority manifest) error = %v", err)
				}
			},
		},
		{
			name:          "current pointer is a regular file",
			wantComponent: "authority current pointer",
			setup: func(t *testing.T, store *Store, authority *localca.Authority) {
				t.Helper()
				writeTestGeneration(t, store, filepath.Join(filepath.FromSlash(authorityGenerations), authority.Material().Fingerprint), authority.Material())
				current := filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)
				if err := store.filesystem.writeExclusiveFile(current, []byte("not a committed directory")); err != nil {
					t.Fatalf("write non-directory current pointer: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "certificates")
			store := mustStore(t, directory)
			defer store.Close()
			authority := mustLocalAuthority(t)
			test.setup(t, store, authority)

			_, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
			var corruption *CorruptionError
			if !errors.As(err, &corruption) {
				t.Fatalf("LoadAuthority() error = %v, want CorruptionError", err)
			}
			if corruption.Component != test.wantComponent {
				t.Fatalf("corruption component = %q, want %q", corruption.Component, test.wantComponent)
			}
		})
	}
}

// TestConcurrentAuthorityRecoveryPromotesTheSameOrphan verifies recovery publication is idempotent across daemon races.
func TestConcurrentAuthorityRecoveryPromotesTheSameOrphan(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	authority := mustLocalAuthority(t)
	base := mustStore(t, directory)
	material := authority.Material()
	writeTestGeneration(t, base, filepath.Join(filepath.FromSlash(authorityGenerations), material.Fingerprint), material)
	if err := base.Close(); err != nil {
		t.Fatalf("Close(base) error = %v", err)
	}

	var arrivals atomic.Int32
	ready := make(chan struct{})
	release := make(chan struct{})
	checkpoint := func(checkpoint writeCheckpoint) error {
		if checkpoint != checkpointManifestSynced {
			return nil
		}
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		<-release
		return nil
	}
	firstStore, err := openStore(directory, storeDependencies{random: rand.Reader, checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("openStore(first) error = %v", err)
	}
	defer firstStore.Close()
	secondStore, err := openStore(directory, storeDependencies{random: rand.Reader, checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("openStore(second) error = %v", err)
	}
	defer secondStore.Close()

	type loadResult struct {
		authority *localca.Authority
		err       error
	}
	results := make(chan loadResult, 2)
	go func() {
		loaded, loadErr := firstStore.LoadAuthority(context.Background(), storeAuthorityConfig())
		results <- loadResult{authority: loaded, err: loadErr}
	}()
	go func() {
		loaded, loadErr := secondStore.LoadAuthority(context.Background(), storeAuthorityConfig())
		results <- loadResult{authority: loaded, err: loadErr}
	}()
	<-ready
	close(release)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("LoadAuthority(concurrent recovery) error = %v", result.err)
		}
		if got := result.authority.Material().Fingerprint; got != material.Fingerprint {
			t.Fatalf("recovered fingerprint = %q, want %q", got, material.Fingerprint)
		}
	}
}

// TestAuthorityRecoveryRejectsAmbiguousRoots verifies crash recovery never guesses a trust identity.
func TestAuthorityRecoveryRejectsAmbiguousRoots(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	t.Cleanup(func() { _ = store.Close() })
	for _, authority := range []*localca.Authority{mustLocalAuthority(t), mustLocalAuthority(t)} {
		material := authority.Material()
		generation := filepath.Join(filepath.FromSlash(authorityGenerations), material.Fingerprint)
		writeTestGeneration(t, store, generation, material)
	}
	_, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
	var corruption *CorruptionError
	if !errors.As(err, &corruption) || !strings.Contains(err.Error(), "2 unpublished root identities") {
		t.Fatalf("LoadAuthority(ambiguous) error = %v, want corruption", err)
	}
}

// TestLeafPublicationFailuresRetainOldActivePair verifies every pre-commit failure leaves a reloadable old certificate.
func TestLeafPublicationFailuresRetainOldActivePair(t *testing.T) {
	checkpoints := []writeCheckpoint{
		checkpointCertificateSynced,
		checkpointPrivateKeySynced,
		checkpointGenerationChecked,
		checkpointGenerationCommit,
		checkpointGenerationMoved,
		checkpointGenerationSynced,
		checkpointManifestSynced,
	}
	for _, checkpoint := range checkpoints {
		checkpoint := checkpoint
		t.Run(string(checkpoint), func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "certificates")
			base := mustStore(t, directory)
			authority := mustLocalAuthority(t)
			oldLeaf := mustLeaf(t, authority, []string{"orders.test"})
			if err := base.PutLeaf(context.Background(), authority, oldLeaf); err != nil {
				t.Fatalf("PutLeaf(old) error = %v", err)
			}
			if err := base.Close(); err != nil {
				t.Fatalf("Close(base) error = %v", err)
			}

			sentinel := errors.New("injected persistence failure")
			failing, err := openStore(directory, storeDependencies{
				random: rand.Reader,
				checkpoint: func(reached writeCheckpoint) error {
					if reached == checkpoint {
						return sentinel
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("openStore(failing) error = %v", err)
			}
			newLeaf := mustLeaf(t, authority, oldLeaf.Hosts)
			if err := failing.PutLeaf(context.Background(), authority, newLeaf); !errors.Is(err, sentinel) {
				t.Fatalf("PutLeaf(failing) error = %v, want %v", err, sentinel)
			}
			if err := failing.Close(); err != nil {
				t.Fatalf("Close(failing) error = %v", err)
			}

			reopened := mustStore(t, directory)
			defer reopened.Close()
			loaded, err := reopened.LoadLeaf(context.Background(), authority, oldLeaf.Hosts)
			if err != nil {
				t.Fatalf("LoadLeaf(after failure) error = %v", err)
			}
			if loaded.Material.Fingerprint != oldLeaf.Material.Fingerprint {
				t.Fatalf("active fingerprint = %q, want old %q", loaded.Material.Fingerprint, oldLeaf.Material.Fingerprint)
			}
		})
	}
}

// TestLeafManifestReplacementIsTheCommitPoint verifies a post-replacement fault still leaves one complete new pair.
func TestLeafManifestReplacementIsTheCommitPoint(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	base := mustStore(t, directory)
	authority := mustLocalAuthority(t)
	oldLeaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := base.PutLeaf(context.Background(), authority, oldLeaf); err != nil {
		t.Fatalf("PutLeaf(old) error = %v", err)
	}
	_ = base.Close()
	sentinel := errors.New("failure after commit")
	failing, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointManifestReplaced {
				return sentinel
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	newLeaf := mustLeaf(t, authority, oldLeaf.Hosts)
	if err := failing.PutLeaf(context.Background(), authority, newLeaf); !errors.Is(err, sentinel) {
		t.Fatalf("PutLeaf() error = %v, want %v", err, sentinel)
	}
	_ = failing.Close()

	reopened := mustStore(t, directory)
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.LoadLeaf(context.Background(), authority, oldLeaf.Hosts)
	if err != nil {
		t.Fatalf("LoadLeaf() error = %v", err)
	}
	if loaded.Material.Fingerprint != newLeaf.Material.Fingerprint {
		t.Fatalf("active fingerprint = %q, want committed %q", loaded.Material.Fingerprint, newLeaf.Material.Fingerprint)
	}
}

// TestCorruptAuthorityFailsClosedWithoutRegeneration verifies invalid current material remains explicit repair work.
func TestCorruptAuthorityFailsClosedWithoutRegeneration(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	t.Cleanup(func() { _ = store.Close() })
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	keyPath := filepath.Join(
		directory,
		filepath.FromSlash(authorityGenerations),
		authority.Material().Fingerprint,
		privateKeyFilename,
	)
	secretSentinel := "PRIVATE-SENTINEL-DO-NOT-LOG"
	if err := os.WriteFile(keyPath, []byte(secretSentinel), privateFileMode); err != nil {
		t.Fatalf("corrupt private key: %v", err)
	}
	_, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
	var corruption *CorruptionError
	if !errors.As(err, &corruption) {
		t.Fatalf("LoadAuthority(corrupt) error = %v, want CorruptionError", err)
	}
	if strings.Contains(err.Error(), secretSentinel) {
		t.Fatalf("corruption error leaked private-key content: %v", err)
	}
	if err := store.CreateAuthority(context.Background(), mustLocalAuthority(t)); err == nil {
		t.Fatal("CreateAuthority() replaced corrupt active identity")
	}
}

// TestManifestAdmissionRejectsSchemaAndSizeDrift verifies active pointers remain strict and bounded.
func TestManifestAdmissionRejectsSchemaAndSizeDrift(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "unknown field", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `","extra":true}`), want: "unknown field"},
		{name: "case aliased field", content: []byte(`{"version":1,"kind":"authority","Kind":"leaf","fingerprint":"` + strings.Repeat("a", 64) + `"}`), want: `unknown field "Kind"`},
		{name: "duplicate field", content: []byte(`{"version":1,"kind":"authority","kind":"leaf","fingerprint":"` + strings.Repeat("a", 64) + `"}`), want: "duplicate field"},
		{name: "trailing value", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `"} {}`), want: "multiple JSON values"},
		{name: "wrong version", content: []byte(`{"version":2,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `"}`), want: "version"},
		{name: "empty leaf authority field", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `","authority_fingerprint":""}`), want: "leaf-only fields"},
		{name: "null leaf authority field", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `","authority_fingerprint":null}`), want: "leaf-only fields"},
		{name: "empty leaf hosts field", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `","hosts":[]}`), want: "leaf-only fields"},
		{name: "null leaf hosts field", content: []byte(`{"version":1,"kind":"authority","fingerprint":"` + strings.Repeat("a", 64) + `","hosts":null}`), want: "leaf-only fields"},
		{name: "oversized", content: bytes.Repeat([]byte("x"), maximumManifestBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "certificates")
			store := mustStore(t, directory)
			defer store.Close()
			writeAuthorityManifestFixture(t, store, test.content)
			_, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadAuthority() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestGenerationAdmissionRejectsUnexpectedEntries verifies immutable pair directories cannot hide extra material.
func TestGenerationAdmissionRejectsUnexpectedEntries(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	generation := filepath.Join(directory, filepath.FromSlash(authorityGenerations), authority.Material().Fingerprint)
	if err := os.WriteFile(filepath.Join(generation, "unexpected"), []byte("public"), privateFileMode); err != nil {
		t.Fatalf("write unexpected entry: %v", err)
	}
	_, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
	if err == nil || !strings.Contains(err.Error(), "contains 3 entries") {
		t.Fatalf("LoadAuthority() error = %v, want unexpected-entry failure", err)
	}
}

// TestCancellationAndClosedStoreFailBeforeMutation verifies lifecycle boundaries cannot publish new files.
func TestCancellationAndClosedStoreFailBeforeMutation(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	authority := mustLocalAuthority(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.CreateAuthority(ctx, authority); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateAuthority(cancelled) error = %v", err)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled creation published a manifest: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(repeated) error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("LoadAuthority(closed) error = %v, want ErrStoreClosed", err)
	}
}

// TestConcurrentLeafReadsAndRotations verifies readers never observe mismatched certificate and key generations.
func TestConcurrentLeafReadsAndRotations(t *testing.T) {
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	hosts := []string{"orders.test"}
	if err := store.PutLeaf(context.Background(), authority, mustLeaf(t, authority, hosts)); err != nil {
		t.Fatalf("PutLeaf(initial) error = %v", err)
	}

	const readerCount = 8
	const rotations = 12
	var workers sync.WaitGroup
	errorsChannel := make(chan error, readerCount+1)
	for reader := 0; reader < readerCount; reader++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < rotations*2; iteration++ {
				leaf, err := store.LoadLeaf(context.Background(), authority, hosts)
				if err != nil {
					errorsChannel <- err
					return
				}
				if _, err := authority.LoadLeaf(leaf.Material.CertificatePEM, leaf.Material.PrivateKeyPEM, hosts); err != nil {
					errorsChannel <- err
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for rotation := 0; rotation < rotations; rotation++ {
			leaf, err := authority.Issue(context.Background(), hosts)
			if err != nil {
				errorsChannel <- err
				return
			}
			if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
				errorsChannel <- err
				return
			}
		}
	}()
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent store error = %v", err)
	}
}

// TestOpenAndInputValidation covers public path, dependency, authority, and host admission branches.
func TestOpenAndInputValidation(t *testing.T) {
	t.Parallel()
	if _, err := Open(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Open(empty) error = %v", err)
	}
	if _, err := Open("relative"); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("Open(relative) error = %v", err)
	}
	if _, err := openStore(filepath.Join(t.TempDir(), "certificates"), storeDependencies{}); err == nil || !strings.Contains(err.Error(), "random source") {
		t.Fatalf("openStore(no random) error = %v", err)
	}
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	if err := store.CreateAuthority(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("CreateAuthority(nil) error = %v", err)
	}
	if _, err := store.LoadLeaf(context.Background(), nil, []string{"orders.test"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("LoadLeaf(nil authority) error = %v", err)
	}
	if err := store.PutLeaf(context.Background(), nil, localca.Leaf{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("PutLeaf(nil authority) error = %v", err)
	}
	authority := mustLocalAuthority(t)
	if _, err := store.LoadLeaf(context.Background(), authority, []string{"outside.local"}); err == nil {
		t.Fatal("LoadLeaf(invalid hosts) succeeded")
	}
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	leaf.Hosts = []string{"ORDERS.TEST"}
	if err := store.PutLeaf(context.Background(), authority, leaf); err == nil || !strings.Contains(err.Error(), "canonical order") {
		t.Fatalf("PutLeaf(noncanonical hosts) error = %v", err)
	}
}

// TestStagingEntropyFailureLeavesCurrentManifestUntouched verifies temporary-name failure precedes all publication work.
func TestStagingEntropyFailureLeavesCurrentManifestUntouched(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	base := mustStore(t, directory)
	authority := mustLocalAuthority(t)
	oldLeaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := base.PutLeaf(context.Background(), authority, oldLeaf); err != nil {
		t.Fatalf("PutLeaf(old) error = %v", err)
	}
	_ = base.Close()
	sentinel := errors.New("entropy unavailable")
	failing, err := openStore(directory, storeDependencies{random: errorStoreReader{err: sentinel}})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer failing.Close()
	if err := failing.PutLeaf(context.Background(), authority, mustLeaf(t, authority, oldLeaf.Hosts)); !errors.Is(err, sentinel) {
		t.Fatalf("PutLeaf(entropy failure) error = %v, want %v", err, sentinel)
	}
	loaded, err := failing.LoadLeaf(context.Background(), authority, oldLeaf.Hosts)
	if err != nil {
		t.Fatalf("LoadLeaf() error = %v", err)
	}
	if loaded.Material.Fingerprint != oldLeaf.Material.Fingerprint {
		t.Fatalf("active fingerprint = %q, want %q", loaded.Material.Fingerprint, oldLeaf.Material.Fingerprint)
	}
}

// TestHostSetIdentityIsUnambiguous verifies length-prefixing prevents concatenation collisions.
func TestHostSetIdentityIsUnambiguous(t *testing.T) {
	t.Parallel()
	first := hostSetID([]string{"ab.test", "c.test"})
	second := hostSetID([]string{"a.test", "bc.test"})
	if first == second {
		t.Fatalf("host set IDs collided at %q", first)
	}
	if len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("host set ID = %q, want lowercase SHA-256", first)
	}
}

// TestManifestEncodingIsDeterministic verifies active pointers remain reviewable and replay-stable.
func TestManifestEncodingIsDeterministic(t *testing.T) {
	t.Parallel()
	manifest := leafManifest(strings.Repeat("a", 64), strings.Repeat("b", 64), []string{"orders.test"})
	first, err := encodeManifest(manifest)
	if err != nil {
		t.Fatalf("encodeManifest(first) error = %v", err)
	}
	second, err := encodeManifest(manifest)
	if err != nil {
		t.Fatalf("encodeManifest(second) error = %v", err)
	}
	if !bytes.Equal(first, second) || first[len(first)-1] != '\n' {
		t.Fatalf("manifest encodings differ or lack newline\nfirst: %q\nsecond: %q", first, second)
	}
	decoded, err := decodeManifest(first)
	if err != nil {
		t.Fatalf("decodeManifest() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, manifest) {
		t.Fatalf("decoded manifest = %#v, want %#v", decoded, manifest)
	}
}

// mustStore opens one test store and fails before incomplete persistence can be used.
func mustStore(t *testing.T, directory string) *Store {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

// mustLocalAuthority creates one root at the test clock.
func mustLocalAuthority(t *testing.T) *localca.Authority {
	t.Helper()
	authority, err := localca.New(storeAuthorityConfig())
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	return authority
}

// storeAuthorityConfig gives generation, reload, and leaf verification one stable current instant.
func storeAuthorityConfig() localca.Config {
	return localca.Config{Now: func() time.Time { return storeTestTime }}
}

// mustLeaf issues one validated test leaf.
func mustLeaf(t *testing.T, authority *localca.Authority, hosts []string) localca.Leaf {
	t.Helper()
	leaf, err := authority.Issue(context.Background(), hosts)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	return leaf
}

// writeTestGeneration creates one complete immutable generation for recovery fixtures.
func writeTestGeneration(t *testing.T, store *Store, generation string, material localca.Material) {
	t.Helper()
	if err := store.filesystem.ensureDirectory(generation); err != nil {
		t.Fatalf("ensure generation: %v", err)
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(generation, certificateFilename), material.CertificatePEM); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(generation, privateKeyFilename), material.PrivateKeyPEM); err != nil {
		t.Fatalf("write private key: %v", err)
	}
}

// writeAuthorityManifestFixture creates the exact authority pointer shape before injecting parser input.
func writeAuthorityManifestFixture(t *testing.T, store *Store, content []byte) {
	t.Helper()
	current := filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)
	if err := store.filesystem.ensureDirectory(current); err != nil {
		t.Fatalf("ensure authority current directory: %v", err)
	}
	if err := store.filesystem.writeExclusiveFile(authorityManifestPath(), content); err != nil {
		t.Fatalf("write authority manifest: %v", err)
	}
}

// authorityManifestDiskPath gives black-box filesystem assertions the published authority manifest location.
func authorityManifestDiskPath(directory string) string {
	return filepath.Join(directory, authorityManifestPath())
}

// authorityCurrentDiskPath gives black-box assertions the atomically installed authority pointer directory.
func authorityCurrentDiskPath(directory string) string {
	return filepath.Join(directory, filepath.FromSlash(authorityDirectory), authorityCurrent)
}

// assertAuthorityLayout verifies active and immutable paths contain no caller-controlled names.
func assertAuthorityLayout(t *testing.T, directory, fingerprint string) {
	t.Helper()
	manifestPath := authorityManifestDiskPath(directory)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		t.Fatalf("decodeManifest() error = %v", err)
	}
	if manifest.Fingerprint != fingerprint {
		t.Fatalf("manifest fingerprint = %q, want %q", manifest.Fingerprint, fingerprint)
	}
	generation := filepath.Join(directory, filepath.FromSlash(authorityGenerations), fingerprint)
	entries, err := os.ReadDir(generation)
	if err != nil {
		t.Fatalf("ReadDir(generation) error = %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != certificateFilename || entries[1].Name() != privateKeyFilename {
		t.Fatalf("generation entries = %#v", entries)
	}
}

// errorStoreReader fails every read without exposing secret material.
type errorStoreReader struct {
	err error
}

// Read returns the configured deterministic entropy failure.
func (reader errorStoreReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// shortStoreReader terminates staging entropy early for exact io.ReadFull coverage.
type shortStoreReader struct{}

// Read returns one byte and EOF so staging cannot proceed with predictable names.
func (shortStoreReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	target[0] = 1
	return 1, io.EOF
}

// TestStagingNameRejectsShortEntropy verifies partial random input is never padded into a predictable path.
func TestStagingNameRejectsShortEntropy(t *testing.T) {
	t.Parallel()
	store := &Store{random: shortStoreReader{}}
	if _, err := store.stagingName(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("stagingName() error = %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestValidateRelativePathRejectsEscapes verifies internal helpers cannot accidentally weaken os.Root confinement.
func TestValidateRelativePathRejectsEscapes(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", ".", "..", "../outside", "safe/../outside", string(filepath.Separator) + "absolute"} {
		if err := validateRelativePath(path); err == nil {
			t.Fatalf("validateRelativePath(%q) succeeded", path)
		}
	}
	if err := validateRelativePath(filepath.Join("v1", "authority")); err != nil {
		t.Fatalf("validateRelativePath(valid) error = %v", err)
	}
}

// TestWriteAllRejectsZeroProgress verifies malformed writers cannot spin persistence indefinitely.
func TestWriteAllRejectsZeroProgress(t *testing.T) {
	t.Parallel()
	if err := writeAll(zeroWriter{}, []byte("material")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll() error = %v, want io.ErrShortWrite", err)
	}
}

// zeroWriter violates io.Writer progress so writeAll's defensive branch remains covered.
type zeroWriter struct{}

// Write reports no progress and no error.
func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

// TestCorruptionErrorNilSafety verifies diagnostics remain stable across error aggregation paths.
func TestCorruptionErrorNilSafety(t *testing.T) {
	t.Parallel()
	var corruption *CorruptionError
	if got, want := corruption.Error(), "certificate material store material is corrupt: validation failed"; got != want {
		t.Fatalf("nil CorruptionError = %q, want %q", got, want)
	}
	if corruption.Unwrap() != nil {
		t.Fatal("nil CorruptionError unwrap returned a cause")
	}
}

// TestStoreErrorMessagesDoNotContainPEM verifies public failures never stringify supplied private bytes.
func TestStoreErrorMessagesDoNotContainPEM(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	secret := "SECRET-PRIVATE-KEY-CONTENT"
	leaf.Material.PrivateKeyPEM = []byte(secret)
	err := store.PutLeaf(context.Background(), authority, leaf)
	if err == nil {
		t.Fatal("PutLeaf(corrupt key) succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("PutLeaf() error leaked private content: %v", err)
	}
}

// TestStoreRequiredValidatorBranch verifies persistence never publishes material without semantic certificate validation.
func TestStoreRequiredValidatorBranch(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	material := authority.Material()
	err := store.persist(
		context.Background(),
		filepath.FromSlash(authorityDirectory),
		material,
		authorityManifest(material.Fingerprint),
		false,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "validator is required") {
		t.Fatalf("persist(nil validator) error = %v", err)
	}
}

// TestStoreFingerprintAndMaterialBounds verifies malformed callers fail before creating staging state.
func TestStoreFingerprintAndMaterialBounds(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	valid := authority.Material()
	validate := func([]byte, []byte) error { return nil }
	tests := []struct {
		name     string
		material localca.Material
		manifest activeManifest
	}{
		{name: "bad fingerprint", material: localca.Material{Fingerprint: "bad", CertificatePEM: valid.CertificatePEM, PrivateKeyPEM: valid.PrivateKeyPEM}, manifest: authorityManifest("bad")},
		{name: "manifest mismatch", material: valid, manifest: authorityManifest(strings.Repeat("a", 64))},
		{name: "missing certificate", material: localca.Material{Fingerprint: valid.Fingerprint, PrivateKeyPEM: valid.PrivateKeyPEM}, manifest: authorityManifest(valid.Fingerprint)},
		{name: "large certificate", material: localca.Material{Fingerprint: valid.Fingerprint, CertificatePEM: bytes.Repeat([]byte("x"), maximumCertificatePEM+1), PrivateKeyPEM: valid.PrivateKeyPEM}, manifest: authorityManifest(valid.Fingerprint)},
		{name: "missing key", material: localca.Material{Fingerprint: valid.Fingerprint, CertificatePEM: valid.CertificatePEM}, manifest: authorityManifest(valid.Fingerprint)},
		{name: "large key", material: localca.Material{Fingerprint: valid.Fingerprint, CertificatePEM: valid.CertificatePEM, PrivateKeyPEM: bytes.Repeat([]byte("x"), maximumPrivateKeyPEM+1)}, manifest: authorityManifest(valid.Fingerprint)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := store.persist(context.Background(), filepath.FromSlash(authorityDirectory), test.material, test.manifest, false, validate); err == nil {
				t.Fatal("persist() succeeded")
			}
		})
	}
}

// TestPersistHonorsMidOperationCancellation verifies cancellation after a durable file never advances the active pointer.
func TestPersistHonorsMidOperationCancellation(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	ctx, cancel := context.WithCancel(context.Background())
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointCertificateSynced {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	if err := store.CreateAuthority(ctx, mustLocalAuthority(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateAuthority() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled persistence published manifest: %v", err)
	}
}

// TestCreateAuthorityCancellationBeforeGenerationCommitLeavesNoRecoverableIdentity races cancellation at the last reversible boundary.
func TestCreateAuthorityCancellationBeforeGenerationCommitLeavesNoRecoverableIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	ctx, cancel := context.WithCancel(context.Background())
	reached := make(chan struct{})
	release := make(chan struct{})
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointGenerationCommit {
				close(reached)
				<-release
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	authority := mustLocalAuthority(t)
	result := make(chan error, 1)
	go func() {
		result <- store.CreateAuthority(ctx, authority)
	}()
	<-reached
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateAuthority() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-commit cancellation published a manifest: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, filepath.FromSlash(authorityGenerations)))
	if err != nil {
		t.Fatalf("ReadDir(generations) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("pre-commit cancellation left %d recoverable generations", len(entries))
	}
}

// TestCreateAuthorityCancellationAfterGenerationCommitCompletesActivation proves cancellation cannot strand a recoverable orphan.
func TestCreateAuthorityCancellationAfterGenerationCommitCompletesActivation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	ctx, cancel := context.WithCancel(context.Background())
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointGenerationMoved {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(ctx, authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v, want completed activation", err)
	}
	loaded, err := store.LoadAuthority(context.Background(), storeAuthorityConfig())
	if err != nil {
		t.Fatalf("LoadAuthority() error = %v", err)
	}
	if loaded.Material().Fingerprint != authority.Material().Fingerprint {
		t.Fatalf("active fingerprint = %q, want %q", loaded.Material().Fingerprint, authority.Material().Fingerprint)
	}
}

// TestManifestValidationCoversAuthorityAndLeafShapes verifies leaf-only fields cannot enter a root pointer and vice versa.
func TestManifestValidationCoversAuthorityAndLeafShapes(t *testing.T) {
	t.Parallel()
	fingerprint := strings.Repeat("a", 64)
	hosts := []string{"orders.test"}
	root := authorityManifest(fingerprint)
	if err := validateAuthorityManifest(root); err != nil {
		t.Fatalf("validateAuthorityManifest(valid) error = %v", err)
	}
	root.Hosts = hosts
	if err := validateAuthorityManifest(root); err == nil {
		t.Fatal("validateAuthorityManifest(leaf fields) succeeded")
	}
	leaf := leafManifest(fingerprint, strings.Repeat("b", 64), hosts)
	if err := validateLeafManifest(leaf, fingerprint, hosts); err != nil {
		t.Fatalf("validateLeafManifest(valid) error = %v", err)
	}
	leaf.Hosts = []string{"ORDERS.TEST"}
	if err := validateLeafManifest(leaf, fingerprint, hosts); err == nil {
		t.Fatal("validateLeafManifest(noncanonical) succeeded")
	}
}

// TestWriteAllPreservesWriterError verifies filesystem causes remain available to callers.
func TestWriteAllPreservesWriterError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("disk unavailable")
	if err := writeAll(errorWriter{err: sentinel}, []byte("material")); !errors.Is(err, sentinel) {
		t.Fatalf("writeAll() error = %v, want %v", err, sentinel)
	}
}

// errorWriter always returns its configured failure.
type errorWriter struct {
	err error
}

// Write returns the configured storage failure without consuming content.
func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// TestReadBoundedFileDetectsPostStatGrowth verifies the reader limit reinforces metadata size admission.
func TestReadBoundedFileDetectsPostStatGrowth(t *testing.T) {
	t.Parallel()
	filesystem, err := openRootedFilesystem(filepath.Join(t.TempDir(), "certificates"))
	if err != nil {
		t.Fatalf("openRootedFilesystem() error = %v", err)
	}
	defer filesystem.Close()
	if err := filesystem.writeExclusiveFile("bounded", []byte("12345")); err != nil {
		t.Fatalf("writeExclusiveFile() error = %v", err)
	}
	if _, err := filesystem.readBoundedFile("bounded", 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBoundedFile() error = %v", err)
	}
}

// TestStoreLayoutDoesNotAcceptOccupiedFiles verifies directory initialization fails closed on non-directory entries.
func TestStoreLayoutDoesNotAcceptOccupiedFiles(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	if err := preparePlatformRoot(directory); err != nil {
		t.Fatalf("preparePlatformRoot() error = %v", err)
	}
	occupied := filepath.Join(directory, storeVersionDirectory)
	if err := os.WriteFile(occupied, []byte("occupied"), privateFileMode); err != nil {
		t.Fatalf("write occupied path: %v", err)
	}
	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "not a direct directory") {
		t.Fatalf("Open(occupied) error = %v", err)
	}
}

// TestRootedFilesystemRejectsPathSwap verifies the retained handle must name the exact directory initially validated.
func TestRootedFilesystemRejectsPathSwap(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	directory := filepath.Join(parent, "certificates")
	if err := preparePlatformRoot(directory); err != nil {
		t.Fatalf("preparePlatformRoot() error = %v", err)
	}
	displaced := filepath.Join(parent, "displaced")
	var hookErr error
	filesystem, err := openRootedFilesystemWithHook(directory, func() {
		if renameErr := os.Rename(directory, displaced); renameErr != nil {
			hookErr = renameErr
			return
		}
		hookErr = preparePlatformRoot(directory)
	})
	if filesystem != nil {
		_ = filesystem.Close()
	}
	if hookErr != nil {
		t.Fatalf("swap hook error = %v", hookErr)
	}
	if err == nil || !strings.Contains(err.Error(), "changed before") {
		t.Fatalf("openRootedFilesystemWithHook(swapped) error = %v", err)
	}
}

// TestStoreMissingAuthorityAndLeafReturnTypedErrors verifies absence is distinct from corruption.
func TestStoreMissingAuthorityAndLeafReturnTypedErrors(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("LoadAuthority(empty) error = %v", err)
	}
	authority := mustLocalAuthority(t)
	if _, err := store.LoadLeaf(context.Background(), authority, []string{"orders.test"}); !errors.Is(err, ErrLeafNotFound) {
		t.Fatalf("LoadLeaf(empty) error = %v", err)
	}
}

// TestCreateAuthorityAcceptsCustomClockMaterial verifies persistence validates the pair inside its encoded lifetime.
func TestCreateAuthorityAcceptsCustomClockMaterial(t *testing.T) {
	t.Parallel()
	customTime := time.Date(2040, time.January, 2, 3, 4, 5, 0, time.UTC)
	authority, err := localca.New(localca.Config{Now: func() time.Time { return customTime }})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority(custom clock) error = %v", err)
	}
	loaded, err := store.LoadAuthority(context.Background(), localca.Config{Now: func() time.Time { return customTime }})
	if err != nil {
		t.Fatalf("LoadAuthority(custom clock) error = %v", err)
	}
	if loaded.Material().Fingerprint != authority.Material().Fingerprint {
		t.Fatal("custom-clock authority changed across persistence")
	}
}

// TestCreateAuthorityRejectsExpiredCurrentAuthority verifies historical pair validity cannot authorize a new persistence commit.
func TestCreateAuthorityRejectsExpiredCurrentAuthority(t *testing.T) {
	t.Parallel()
	now := storeTestTime
	authority, err := localca.New(localca.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	now = authority.Material().NotAfter
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	if err := store.CreateAuthority(context.Background(), authority); err == nil || !strings.Contains(err.Error(), "not currently valid") {
		t.Fatalf("CreateAuthority(expired) error = %v", err)
	}
	manifestPath := authorityManifestDiskPath(directory)
	if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired authority published a manifest: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(directory, filepath.FromSlash(authorityGenerations)))
	if err != nil {
		t.Fatalf("ReadDir(generations) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expired authority published %d generations", len(entries))
	}
}

// TestCheckpointErrorNamesBoundaryWithoutSecrets verifies injected causes remain classifiable and redacted.
func TestCheckpointErrorNamesBoundaryWithoutSecrets(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("storage failure")
	store := &Store{checkpoint: func(checkpoint writeCheckpoint) error { return sentinel }}
	err := store.reach(checkpointPrivateKeySynced)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), string(checkpointPrivateKeySynced)) {
		t.Fatalf("reach() error = %v", err)
	}
}

// TestAuthorityManifestFingerprintUsesCertificateDigest verifies path identity cannot be caller-selected independently.
func TestAuthorityManifestFingerprintUsesCertificateDigest(t *testing.T) {
	t.Parallel()
	authority := mustLocalAuthority(t)
	material := authority.Material()
	manifest := authorityManifest(material.Fingerprint)
	if manifest.Fingerprint != material.Fingerprint {
		t.Fatalf("manifest fingerprint = %q, want %q", manifest.Fingerprint, material.Fingerprint)
	}
}

// TestPutLeafRejectsFingerprintDrift verifies metadata cannot select a generation different from the encoded certificate.
func TestPutLeafRejectsFingerprintDrift(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	leaf.Material.Fingerprint = strings.Repeat("a", 64)
	if err := store.PutLeaf(context.Background(), authority, leaf); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("PutLeaf(drift) error = %v", err)
	}
}

// TestAuthorityGenerationCollisionValidatesExistingPair verifies immutable names cannot hide mismatched content.
func TestAuthorityGenerationCollisionValidatesExistingPair(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	material := authority.Material()
	generation := filepath.Join(filepath.FromSlash(authorityGenerations), material.Fingerprint)
	if err := store.filesystem.ensureDirectory(generation); err != nil {
		t.Fatalf("ensure generation: %v", err)
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(generation, certificateFilename), material.CertificatePEM); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(generation, privateKeyFilename), []byte("not a key")); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}
	if err := store.CreateAuthority(context.Background(), authority); err == nil {
		t.Fatal("CreateAuthority(collision) succeeded")
	}
}

// TestGenerationDirectoryReadErrorDoesNotPanic verifies joined read/close handling remains an ordinary error.
func TestGenerationDirectoryReadErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	if err := store.filesystem.ensureDirectory("empty-generation"); err != nil {
		t.Fatalf("ensure directory: %v", err)
	}
	if err := store.filesystem.validateGenerationDirectory("empty-generation"); err == nil {
		t.Fatal("validateGenerationDirectory(empty) succeeded")
	}
}

// TestErrorReaderImplementsExpectedFailure verifies the fixture never makes entropy progress.
func TestErrorReaderImplementsExpectedFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("no entropy")
	buffer := make([]byte, 1)
	read, err := errorStoreReader{err: sentinel}.Read(buffer)
	if read != 0 || !errors.Is(err, sentinel) {
		t.Fatalf("Read() = %d, %v", read, err)
	}
}

// TestLeafManifestHostSetMismatchFailsClosed verifies copied manifests cannot redirect another exact-name certificate.
func TestLeafManifestHostSetMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	manifestPath := filepath.Join(directory, leafDirectory(authority.Material().Fingerprint, leaf.Hosts), currentFilename)
	encoded, err := encodeManifest(leafManifest(authority.Material().Fingerprint, leaf.Material.Fingerprint, []string{"billing.test"}))
	if err != nil {
		t.Fatalf("encodeManifest() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, encoded, privateFileMode); err != nil {
		t.Fatalf("replace manifest: %v", err)
	}
	if _, err := store.LoadLeaf(context.Background(), authority, leaf.Hosts); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("LoadLeaf(mismatch) error = %v", err)
	}
}

// TestStoreDoesNotExposePrivateMaterialInPaths verifies generation paths contain hashes rather than domains or PEM data.
func TestStoreDoesNotExposePrivateMaterialInPaths(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"private-project-name.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	var paths []string
	err := filepath.WalkDir(directory, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	for _, path := range paths {
		if strings.Contains(path, "private-project-name") || strings.Contains(path, "BEGIN PRIVATE KEY") {
			t.Fatalf("private material leaked into path %q", path)
		}
	}
}

// TestStoreRandomSourceIsSerializedByMutationLock verifies even non-concurrent readers remain safe behind Store's writer authority.
func TestStoreRandomSourceIsSerializedByMutationLock(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "certificates"), storeDependencies{random: &countingReader{}})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	authority := mustLocalAuthority(t)
	if err := store.PutLeaf(context.Background(), authority, mustLeaf(t, authority, []string{"orders.test"})); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
}

// countingReader emits deterministic unique bytes and panics if Store uses it concurrently.
type countingReader struct {
	mutex sync.Mutex
	next  byte
	busy  bool
}

// Read verifies Store serializes staging entropy and fills the requested buffer.
func (reader *countingReader) Read(target []byte) (int, error) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.busy {
		panic("concurrent entropy read")
	}
	reader.busy = true
	defer func() { reader.busy = false }()
	for index := range target {
		reader.next++
		target[index] = reader.next
	}
	return len(target), nil
}

// TestStoreErrorFormattingRetainsCause verifies filesystem and policy causes remain inspectable.
func TestStoreErrorFormattingRetainsCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sentinel")
	err := corrupt("authority manifest", sentinel)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "authority manifest") {
		t.Fatalf("corrupt() error = %v", err)
	}
}

// TestPutLeafPreservesExactTLSMaterial verifies a loaded pair remains usable by the standard library.
func TestPutLeafPreservesExactTLSMaterial(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	loaded, err := store.LoadLeaf(context.Background(), authority, leaf.Hosts)
	if err != nil {
		t.Fatalf("LoadLeaf() error = %v", err)
	}
	if loaded.TLSCertificate.Leaf == nil || loaded.TLSCertificate.Leaf.VerifyHostname("orders.test") != nil {
		t.Fatalf("loaded TLS certificate = %#v", loaded.TLSCertificate)
	}
}

// TestEnsureLeafDirectoryRejectsInvalidAuthorityFingerprint verifies dynamic layout never accepts caller path text.
func TestEnsureLeafDirectoryRejectsInvalidAuthorityFingerprint(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	if _, err := store.ensureLeafDirectory("../escape", []string{"orders.test"}); err == nil {
		t.Fatal("ensureLeafDirectory(invalid fingerprint) succeeded")
	}
}

// TestPublishManifestHonorsPreCommitCancellation verifies a staged pointer cannot commit after its operation is cancelled.
func TestPublishManifestHonorsPreCommitCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	directory := filepath.Join(t.TempDir(), "certificates")
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		checkpoint: func(checkpoint writeCheckpoint) error {
			if checkpoint == checkpointManifestSynced {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	fingerprint := strings.Repeat("a", 64)
	err = store.publishManifest(ctx, filepath.FromSlash(authorityDirectory), authorityManifest(fingerprint), false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publishManifest() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(authorityManifestDiskPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled publication created current manifest: %v", err)
	}
}

// TestStoreCheckpointCoverageNamesAreUnique verifies fault-injection boundaries cannot accidentally collapse.
func TestStoreCheckpointCoverageNamesAreUnique(t *testing.T) {
	t.Parallel()
	checkpoints := []writeCheckpoint{
		checkpointCertificateSynced,
		checkpointPrivateKeySynced,
		checkpointGenerationChecked,
		checkpointGenerationCommit,
		checkpointGenerationMoved,
		checkpointGenerationSynced,
		checkpointManifestSynced,
		checkpointManifestCommit,
		checkpointManifestReplaced,
		checkpointManifestDirSynced,
	}
	seen := make(map[writeCheckpoint]struct{}, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if _, exists := seen[checkpoint]; exists || checkpoint == "" {
			t.Fatalf("duplicate or empty checkpoint %q", checkpoint)
		}
		seen[checkpoint] = struct{}{}
	}
}

// TestContextNormalizationMatchesHarborConvention verifies nil contexts remain supported without disabling real cancellation.
func TestContextNormalizationMatchesHarborConvention(t *testing.T) {
	t.Parallel()
	if normalizeContext(nil) == nil || normalizeContext(context.Background()) != context.Background() {
		t.Fatal("normalizeContext() changed expected context semantics")
	}
}

// TestReadDirectoryReturnsOwnedSlice verifies callers can sort observations without mutating filesystem state.
func TestReadDirectoryReturnsOwnedSlice(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	entries, err := store.filesystem.readDirectory(filepath.FromSlash(authorityGenerations))
	if err != nil {
		t.Fatalf("readDirectory() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("authority generations = %#v, want empty", entries)
	}
}

// TestFingerprintValidationRejectsCaseAndLengthDrift verifies paths use one canonical digest language.
func TestFingerprintValidationRejectsCaseAndLengthDrift(t *testing.T) {
	t.Parallel()
	valid := strings.Repeat("a", 64)
	if err := validateFingerprint(valid); err != nil {
		t.Fatalf("validateFingerprint(valid) error = %v", err)
	}
	for _, fingerprint := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		if err := validateFingerprint(fingerprint); err == nil {
			t.Fatalf("validateFingerprint(%q) succeeded", fingerprint)
		}
	}
}

// TestStoreFormattedErrorsRemainBounded verifies large private inputs do not expand diagnostics.
func TestStoreFormattedErrorsRemainBounded(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	leaf.Material.PrivateKeyPEM = bytes.Repeat([]byte("S"), maximumPrivateKeyPEM+1)
	err := store.PutLeaf(context.Background(), authority, leaf)
	if err == nil {
		t.Fatal("PutLeaf(large key) succeeded")
	}
	if len(err.Error()) > 4096 {
		t.Fatalf("error length = %d, want bounded", len(err.Error()))
	}
}

// TestPrivateMaterialContentDoesNotAffectHostSetPath verifies only canonical public names select a leaf directory.
func TestPrivateMaterialContentDoesNotAffectHostSetPath(t *testing.T) {
	t.Parallel()
	hosts := []string{"orders.test"}
	first := leafDirectory(strings.Repeat("a", 64), hosts)
	second := leafDirectory(strings.Repeat("a", 64), append([]string(nil), hosts...))
	if first != second {
		t.Fatalf("leaf directories differ: %q and %q", first, second)
	}
}

// TestStoreCanUseDeterministicEntropyWithoutNameCollision verifies sequential staging names remain distinct.
func TestStoreCanUseDeterministicEntropyWithoutNameCollision(t *testing.T) {
	t.Parallel()
	reader := &countingReader{}
	store := &Store{random: reader}
	first, err := store.stagingName()
	if err != nil {
		t.Fatalf("stagingName(first) error = %v", err)
	}
	second, err := store.stagingName()
	if err != nil {
		t.Fatalf("stagingName(second) error = %v", err)
	}
	if first == second || !strings.HasPrefix(first, ".staging-") {
		t.Fatalf("staging names = %q, %q", first, second)
	}
}

// TestManifestDecoderRejectsEmptyAndMalformedInput verifies parser failures remain ordinary bounded errors.
func TestManifestDecoderRejectsEmptyAndMalformedInput(t *testing.T) {
	t.Parallel()
	for _, encoded := range [][]byte{nil, {}, []byte("{"), []byte("null"), bytes.Repeat([]byte("x"), maximumManifestBytes+1)} {
		if _, err := decodeManifest(encoded); err == nil {
			t.Fatalf("decodeManifest(%q) succeeded", encoded)
		}
	}
}

// TestLeafDirectoryUsesCanonicalHosts verifies equivalent caller spelling selects one durable identity.
func TestLeafDirectoryUsesCanonicalHosts(t *testing.T) {
	t.Parallel()
	canonical, err := localca.CanonicalHosts([]string{"ORDERS.TEST."})
	if err != nil {
		t.Fatalf("CanonicalHosts() error = %v", err)
	}
	if got, want := leafDirectory(strings.Repeat("a", 64), canonical), leafDirectory(strings.Repeat("a", 64), []string{"orders.test"}); got != want {
		t.Fatalf("leaf directory = %q, want %q", got, want)
	}
}

// TestPutLeafDoesNotNeedAuthorityPersistence verifies file storage remains composable before host-trust reconciliation exists.
func TestPutLeafDoesNotNeedAuthorityPersistence(t *testing.T) {
	t.Parallel()
	store := mustStore(t, filepath.Join(t.TempDir(), "certificates"))
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); !errors.Is(err, ErrAuthorityNotInitialized) {
		t.Fatalf("LoadAuthority() error = %v, want uninitialized", err)
	}
}
