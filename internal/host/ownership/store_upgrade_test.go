package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreUpgradeAtomicallyBindsNetworkPolicy proves the canonical schema transition and its idempotent replay.
func TestStoreUpgradeAtomicallyBindsNetworkPolicy(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	source := testRecord()
	claimed, err := store.Claim(context.Background(), source)
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	target := testNetworkPolicyRecord()

	upgraded, err := store.Upgrade(nil, claimed.Fingerprint, target)
	if err != nil {
		t.Fatalf("Store.Upgrade() error = %v", err)
	}
	wantFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() target error = %v", err)
	}
	want := Observation{Exists: true, Record: target, Fingerprint: wantFingerprint}
	if upgraded != want {
		t.Fatalf("Store.Upgrade() = %#v, want %#v", upgraded, want)
	}
	replayed, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil || replayed != want {
		t.Fatalf("Store.Upgrade() replay = %#v, %v, want %#v", replayed, err, want)
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

// TestStoreUpgradeRejectsMissingAndStaleSources preserves absent and concurrently changed ownership state.
func TestStoreUpgradeRejectsMissingAndStaleSources(t *testing.T) {
	t.Parallel()
	target := testNetworkPolicyRecord()
	source := target
	source.SchemaVersion = IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	expected, err := source.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() source error = %v", err)
	}

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Upgrade(context.Background(), expected, target); !errors.Is(err, ErrNotClaimed) {
			t.Fatalf("Store.Upgrade() error = %v, want ErrNotClaimed", err)
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
		_, err = store.Upgrade(context.Background(), expected, target)
		if !errors.Is(err, ErrStaleFingerprint) {
			t.Fatalf("Store.Upgrade() error = %v, want ErrStaleFingerprint", err)
		}
		var mismatch *FingerprintMismatchError
		if !errors.As(err, &mismatch) || mismatch.Expected != expected || mismatch.Actual != claimed {
			t.Fatalf("Store.Upgrade() mismatch = %#v, want expected %q and actual %#v", mismatch, expected, claimed)
		}
		observed, observeErr := store.Observe(context.Background())
		if observeErr != nil || observed != claimed {
			t.Fatalf("Store.Observe() = %#v, %v, want %#v", observed, observeErr, claimed)
		}
		assertOwnershipEntries(t, path, true)
	})
}

// TestCompareUpgradeSourceRejectsNonexactFingerprintMatches prevents a digest match from bypassing record equality.
func TestCompareUpgradeSourceRejectsNonexactFingerprintMatches(t *testing.T) {
	t.Parallel()
	source := testRecord()
	existingRecord := source
	existingRecord.Generation++
	expected := strings.Repeat("a", sha256DigestHexLength)
	existing := Observation{Exists: true, Record: existingRecord, Fingerprint: expected}
	err := compareUpgradeSource(existing, expected, source)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("compareUpgradeSource() error = %v, want ErrConflict", err)
	}
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Requested != source || conflict.Existing != existing {
		t.Fatalf("compareUpgradeSource() conflict = %#v, want source %#v and existing %#v", conflict, source, existing)
	}
}

// TestStoreUpgradeValidatesTheExactSourceBeforeIO prevents schema or immutable-field drift from touching storage.
func TestStoreUpgradeValidatesTheExactSourceBeforeIO(t *testing.T) {
	t.Parallel()
	source := testRecord()
	expected, err := source.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() source error = %v", err)
	}
	target := testNetworkPolicyRecord()

	tests := []struct {
		name     string
		expected string
		target   Record
		want     string
	}{
		{name: "malformed expected fingerprint", expected: "bad", target: target, want: "64 lowercase hexadecimal"},
		{name: "schema-1 target", expected: expected, target: source, want: "target schema version"},
	}
	invalidTarget := target
	invalidTarget.NetworkPolicyFingerprint = "bad"
	tests = append(tests, struct {
		name     string
		expected string
		target   Record
		want     string
	}{name: "invalid target", expected: expected, target: invalidTarget, want: "network policy fingerprint"})
	for _, mutation := range []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "installation", mutate: func(record *Record) { record.InstallationID = "other-installation" }},
		{name: "owner", mutate: func(record *Record) { record.OwnerIdentity = "502" }},
		{name: "generation", mutate: func(record *Record) { record.Generation++ }},
		{name: "pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "127.45.0.0/24" }},
		{name: "verifier key", mutate: func(record *Record) { record.TicketVerifierKey = testVerifierKey(77) }},
	} {
		changed := target
		mutation.mutate(&changed)
		tests = append(tests, struct {
			name     string
			expected string
			target   Record
			want     string
		}{name: "changed " + mutation.name, expected: expected, target: changed, want: "does not match target-derived source"})
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			_, err := store.Upgrade(context.Background(), test.expected, test.target)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Upgrade() error = %v, want substring %q", err, test.want)
			}
			assertOwnershipDirectoryEmpty(t, path)
		})
	}

	t.Run("canceled context wins", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.Upgrade(ctx, "bad", Record{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Store.Upgrade() error = %v, want context.Canceled", err)
		}
		assertOwnershipDirectoryEmpty(t, path)
	})
}

// TestPlatformRenameReplace proves overwrite publication is atomic at one handle-relative directory entry.
func TestPlatformRenameReplace(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("Root.Close() error = %v", err)
		}
	})
	for name, content := range map[string]string{"source": "new", "destination": "old"} {
		file, err := createPlatformFile(root, directory, name)
		if err != nil {
			t.Fatalf("createPlatformFile(%q) error = %v", name, err)
		}
		writeErr := writeAll(file, []byte(content))
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			t.Fatalf("stage %q: %v", name, err)
		}
	}

	applied, err := platformRenameReplace(root, directory, "source", "destination")
	if err != nil || !applied {
		t.Fatalf("platformRenameReplace() = %t, %v, want applied", applied, err)
	}
	if _, err := root.Lstat("source"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Root.Lstat(source) error = %v, want not exist", err)
	}
	destination, err := root.Open("destination")
	if err != nil {
		t.Fatalf("Root.Open(destination) error = %v", err)
	}
	content, readErr := io.ReadAll(destination)
	closeErr := destination.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("destination content = %q, want new", content)
	}
	if applied, err := platformRenameReplace(root, directory, "missing", "destination"); applied || err == nil {
		t.Fatalf("platformRenameReplace(missing) = %t, %v, want failure without transition", applied, err)
	}
	content, err = root.ReadFile("destination")
	if err != nil || string(content) != "new" {
		t.Fatalf("Root.ReadFile(destination) = %q, %v, want preserved new content", content, err)
	}
}

// assertOwnershipEntries rejects temporary and retired residue around the expected active and lock names.
func assertOwnershipEntries(t *testing.T, path string, active bool) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	want := map[string]bool{filepath.Base(path) + ".lock": true}
	if active {
		want[filepath.Base(path)] = true
	}
	if len(entries) != len(want) {
		t.Fatalf("store entries = %#v, want names %#v", entries, want)
	}
	for _, entry := range entries {
		if !want[entry.Name()] {
			t.Fatalf("unexpected store entry %q, want names %#v", entry.Name(), want)
		}
	}
}

// assertOwnershipDirectoryEmpty proves validation stopped before even the coordination file was opened.
func assertOwnershipDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store entries = %#v, want empty directory", entries)
	}
}
