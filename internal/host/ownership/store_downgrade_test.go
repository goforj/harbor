package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreDowngradeAtomicallyRemovesNetworkPolicy proves the inverse canonical transition and its idempotent replay.
func TestStoreDowngradeAtomicallyRemovesNetworkPolicy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	source := testNetworkPolicyRecord()
	claimed, err := store.Claim(context.Background(), source)
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	target := source
	target.SchemaVersion = IdentitySchemaVersion
	target.NetworkPolicyFingerprint = ""

	downgraded, err := store.Downgrade(nil, claimed.Fingerprint, source)
	if err != nil {
		t.Fatalf("Store.Downgrade() error = %v", err)
	}
	wantFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() target error = %v", err)
	}
	want := Observation{Exists: true, Record: target, Fingerprint: wantFingerprint}
	if downgraded != want {
		t.Fatalf("Store.Downgrade() = %#v, want %#v", downgraded, want)
	}
	replayed, err := store.Downgrade(context.Background(), claimed.Fingerprint, source)
	if err != nil || replayed != want {
		t.Fatalf("Store.Downgrade() replay = %#v, %v, want %#v", replayed, err, want)
	}
	observed, err := store.Observe(context.Background())
	if err != nil || observed != want {
		t.Fatalf("Store.Observe() = %#v, %v, want %#v", observed, err, want)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("json.Marshal() target error = %v", err)
	}
	if string(content) != string(canonical) {
		t.Fatalf("stored content = %q, want %q", content, canonical)
	}
	assertOwnershipEntries(t, path, true)
}

// TestStoreDowngradeRejectsMissingAndStaleSources preserves absent and concurrently changed ownership state.
func TestStoreDowngradeRejectsMissingAndStaleSources(t *testing.T) {
	t.Parallel()
	source := testNetworkPolicyRecord()
	expected, err := source.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() source error = %v", err)
	}

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Downgrade(context.Background(), expected, source); !errors.Is(err, ErrNotClaimed) {
			t.Fatalf("Store.Downgrade() error = %v, want ErrNotClaimed", err)
		}
		assertOwnershipEntries(t, path, false)
	})

	t.Run("stale", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		concurrent := source
		concurrent.Generation++
		claimed, err := store.Claim(context.Background(), concurrent)
		if err != nil {
			t.Fatalf("Store.Claim() error = %v", err)
		}
		_, err = store.Downgrade(context.Background(), expected, source)
		if !errors.Is(err, ErrStaleFingerprint) {
			t.Fatalf("Store.Downgrade() error = %v, want ErrStaleFingerprint", err)
		}
		var mismatch *FingerprintMismatchError
		if !errors.As(err, &mismatch) || mismatch.Expected != expected || mismatch.Actual != claimed {
			t.Fatalf("Store.Downgrade() mismatch = %#v, want expected %q and actual %#v", mismatch, expected, claimed)
		}
		observed, observeErr := store.Observe(context.Background())
		if observeErr != nil || observed != claimed {
			t.Fatalf("Store.Observe() = %#v, %v, want %#v", observed, observeErr, claimed)
		}
		assertOwnershipEntries(t, path, true)
	})
}

// TestCompareDowngradeSourceRejectsNonexactFingerprintMatches prevents a digest match from bypassing record equality.
func TestCompareDowngradeSourceRejectsNonexactFingerprintMatches(t *testing.T) {
	t.Parallel()
	source := testNetworkPolicyRecord()
	existingRecord := source
	existingRecord.Generation++
	expected := strings.Repeat("a", sha256DigestHexLength)
	existing := Observation{Exists: true, Record: existingRecord, Fingerprint: expected}
	err := compareDowngradeSource(existing, expected, source)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("compareDowngradeSource() error = %v, want ErrConflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Requested != source || conflict.Existing != existing {
		t.Fatalf("compareDowngradeSource() conflict = %#v, want source %#v and existing %#v", conflict, source, existing)
	}
}

// TestStoreDowngradeValidatesTheExactSourceBeforeIO prevents malformed or drifted sources from touching storage.
func TestStoreDowngradeValidatesTheExactSourceBeforeIO(t *testing.T) {
	t.Parallel()
	source := testNetworkPolicyRecord()
	expected, err := source.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() source error = %v", err)
	}

	tests := []struct {
		name     string
		expected string
		source   Record
		want     string
	}{
		{name: "malformed expected fingerprint", expected: "bad", source: source, want: "64 lowercase hexadecimal"},
		{name: "schema-1 source", expected: expected, source: testRecord(), want: "source schema version"},
	}
	invalidSource := source
	invalidSource.NetworkPolicyFingerprint = "bad"
	tests = append(tests, struct {
		name     string
		expected string
		source   Record
		want     string
	}{name: "invalid source", expected: expected, source: invalidSource, want: "network policy fingerprint"})
	changed := source
	changed.Generation++
	tests = append(tests, struct {
		name     string
		expected string
		source   Record
		want     string
	}{name: "changed source", expected: expected, source: changed, want: "does not match source fingerprint"})

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			_, err := store.Downgrade(context.Background(), test.expected, test.source)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Downgrade() error = %v, want substring %q", err, test.want)
			}
			assertOwnershipDirectoryEmpty(t, path)
		})
	}

	t.Run("canceled context wins without storage or lock IO", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		store.operations.createFile = func(*os.Root, string, string) (*os.File, error) {
			t.Fatal("Store.Downgrade() attempted storage IO after cancellation")
			return nil, nil
		}
		store.operations.acquireLock = func(context.Context, *os.File) error {
			t.Fatal("Store.Downgrade() attempted lock IO after cancellation")
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.Downgrade(ctx, "bad", Record{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Store.Downgrade() error = %v, want context.Canceled", err)
		}
		assertOwnershipDirectoryEmpty(t, path)
	})
}
