package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStoreObserveClaimReplayAndRelease proves the complete safe lifecycle and stable durable encoding.
func TestStoreObserveClaimReplayAndRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)

	missing, err := store.Observe(nil)
	if err != nil {
		t.Fatalf("Store.Observe() missing error = %v", err)
	}
	if missing != (Observation{}) {
		t.Fatalf("Store.Observe() missing = %#v, want zero observation", missing)
	}

	record := testRecord()
	claimed, err := store.Claim(nil, record)
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	if !claimed.Exists || claimed.Record != record || len(claimed.Fingerprint) != sha256DigestHexLength {
		t.Fatalf("Store.Claim() = %#v", claimed)
	}
	replayed, err := store.Claim(nil, record)
	if err != nil {
		t.Fatalf("Store.Claim() replay error = %v", err)
	}
	if replayed != claimed {
		t.Fatalf("Store.Claim() replay = %#v, want %#v", replayed, claimed)
	}
	observed, err := store.Observe(nil)
	if err != nil || observed != claimed {
		t.Fatalf("Store.Observe() = %#v, %v, want %#v", observed, err, claimed)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	want, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(content) != string(want) {
		t.Fatalf("stored content = %q, want %q", content, want)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != "owner.json" || entries[1].Name() != "owner.json.lock" {
		t.Fatalf("store entries = %#v, want only record and lock", entries)
	}

	if err := store.Release(nil, claimed.Fingerprint); err != nil {
		t.Fatalf("Store.Release() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat() after release error = %v, want not exist", err)
	}
	entries, err = os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.ReadDir() after release error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "owner.json.lock" {
		t.Fatalf("store entries after release = %#v, want only lock", entries)
	}
	if err := store.Release(nil, claimed.Fingerprint); !errors.Is(err, ErrNotClaimed) {
		t.Fatalf("Store.Release() repeated error = %v, want ErrNotClaimed", err)
	}
}

// TestStoreClaimAndObserveNetworkPolicyRecord proves strict storage round-trips the canonical schema-2 field.
func TestStoreClaimAndObserveNetworkPolicyRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	record := testNetworkPolicyRecord()

	claimed, err := store.Claim(context.Background(), record)
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}
	wantFingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	want := Observation{Exists: true, Record: record, Fingerprint: wantFingerprint}
	if claimed != want {
		t.Fatalf("Store.Claim() = %#v, want %#v", claimed, want)
	}
	replayed, err := store.Claim(context.Background(), record)
	if err != nil || replayed != want {
		t.Fatalf("Store.Claim() replay = %#v, %v, want %#v", replayed, err, want)
	}

	observed, err := store.Observe(context.Background())
	if err != nil || observed != want {
		t.Fatalf("Store.Observe() = %#v, %v, want %#v", observed, err, want)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if string(content) != string(canonical) {
		t.Fatalf("stored content = %q, want %q", content, canonical)
	}
}

// TestPlatformRenameNoReplace proves publication is one no-replace rename with no hard-link or temporary-name residue.
func TestPlatformRenameNoReplace(t *testing.T) {
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

	source, err := createPlatformFile(root, directory, "source")
	if err != nil {
		t.Fatalf("createPlatformFile() source error = %v", err)
	}
	if err := writeAll(source, []byte("source")); err != nil {
		t.Fatalf("writeAll() source error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("source.Close() error = %v", err)
	}
	applied, err := platformRenameNoReplace(root, directory, "source", "destination")
	if err != nil || !applied {
		t.Fatalf("platformRenameNoReplace() = %t, %v, want applied", applied, err)
	}
	if _, err := root.Lstat("source"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Root.Lstat(source) error = %v, want not exist", err)
	}
	destination, err := root.Open("destination")
	if err != nil {
		t.Fatalf("Root.Open(destination) error = %v", err)
	}
	if err := validateOpenedEntry(root, "destination", destination); err != nil {
		t.Fatalf("validateOpenedEntry(destination) error = %v", err)
	}
	content, readErr := io.ReadAll(destination)
	closeErr := destination.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(content) != "source" {
		t.Fatalf("destination content = %q, want source", content)
	}

	conflicting, err := createPlatformFile(root, directory, "conflicting")
	if err != nil {
		t.Fatalf("createPlatformFile() conflicting error = %v", err)
	}
	if err := writeAll(conflicting, []byte("conflicting")); err != nil {
		t.Fatalf("writeAll() conflicting error = %v", err)
	}
	if err := conflicting.Close(); err != nil {
		t.Fatalf("conflicting.Close() error = %v", err)
	}
	applied, err = platformRenameNoReplace(root, directory, "conflicting", "destination")
	if applied || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("platformRenameNoReplace() conflict = %t, %v, want not applied and fs.ErrExist", applied, err)
	}
	for name, want := range map[string]string{"conflicting": "conflicting", "destination": "source"} {
		content, err := root.ReadFile(name)
		if err != nil {
			t.Fatalf("Root.ReadFile(%q) error = %v", name, err)
		}
		if string(content) != want {
			t.Errorf("Root.ReadFile(%q) = %q, want %q", name, content, want)
		}
	}
}

// TestDurabilityUncertainPreservesCause proves callers can distinguish an applied transition from an ordinary failure.
func TestDurabilityUncertainPreservesCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("storage barrier failed")
	err := durabilityUncertain("publish machine ownership record", "/protected/owner.json", cause)
	if !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("durabilityUncertain() error = %v, want ErrDurabilityUncertain", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("durabilityUncertain() error = %v, want original cause", err)
	}
}

// TestStoreClaimPreservesEveryDifferingDimension verifies no foreign or stale claim can be adopted as a replay.
func TestStoreClaimPreservesEveryDifferingDimension(t *testing.T) {
	t.Parallel()
	mutations := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "installation", mutate: func(record *Record) { record.InstallationID = "other-installation" }},
		{name: "owner", mutate: func(record *Record) { record.OwnerIdentity = "502" }},
		{name: "generation", mutate: func(record *Record) { record.Generation++ }},
		{name: "pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "127.45.0.0/24" }},
		{name: "verifier key", mutate: func(record *Record) { record.TicketVerifierKey = testVerifierKey(77) }},
	}
	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			original, err := store.Claim(context.Background(), testRecord())
			if err != nil {
				t.Fatalf("Store.Claim() original error = %v", err)
			}
			requested := testRecord()
			mutation.mutate(&requested)
			_, err = store.Claim(context.Background(), requested)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Store.Claim() conflict error = %v, want ErrConflict", err)
			}
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.Existing != original || conflict.Requested != requested {
				t.Fatalf("Store.Claim() conflict = %#v, want existing %#v and requested %#v", conflict, original, requested)
			}
			after, observeErr := store.Observe(context.Background())
			if observeErr != nil || after != original {
				t.Fatalf("Store.Observe() after conflict = %#v, %v, want %#v", after, observeErr, original)
			}
		})
	}
}

// TestStoreReleaseRejectsStaleAndMalformedFingerprints proves cleanup is an exact compare-and-swap.
func TestStoreReleaseRejectsStaleAndMalformedFingerprints(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	claimed, err := store.Claim(context.Background(), testRecord())
	if err != nil {
		t.Fatalf("Store.Claim() error = %v", err)
	}

	for _, fingerprint := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		if err := store.Release(context.Background(), fingerprint); err == nil || errors.Is(err, ErrStaleFingerprint) {
			t.Errorf("Store.Release(%q) error = %v, want validation error", fingerprint, err)
		}
	}
	stale := strings.Repeat("0", sha256DigestHexLength)
	if stale == claimed.Fingerprint {
		stale = strings.Repeat("1", sha256DigestHexLength)
	}
	err = store.Release(context.Background(), stale)
	if !errors.Is(err, ErrStaleFingerprint) {
		t.Fatalf("Store.Release() stale error = %v, want ErrStaleFingerprint", err)
	}
	var mismatch *FingerprintMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != stale || mismatch.Actual != claimed {
		t.Fatalf("Store.Release() mismatch = %#v, want expected %q and actual %#v", mismatch, stale, claimed)
	}
	if observed, observeErr := store.Observe(context.Background()); observeErr != nil || observed != claimed {
		t.Fatalf("Store.Observe() after stale release = %#v, %v, want %#v", observed, observeErr, claimed)
	}
}

// TestStoreRejectsMalformedAndOversizedStorage exercises bounded strict decoding before a claim can be replayed.
func TestStoreRejectsMalformedAndOversizedStorage(t *testing.T) {
	t.Parallel()
	valid, err := json.Marshal(testRecord())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	validNetworkPolicy, err := json.Marshal(testNetworkPolicyRecord())
	if err != nil {
		t.Fatalf("json.Marshal() network policy record error = %v", err)
	}
	networkPolicyField := `,"network_policy_fingerprint":"` + strings.Repeat("a", 64) + `"`
	duplicateNetworkPolicy := append([]byte(nil), validNetworkPolicy[:len(validNetworkPolicy)-1]...)
	duplicateNetworkPolicy = append(duplicateNetworkPolicy, []byte(networkPolicyField+"}")...)
	withoutNetworkPolicy := strings.Replace(string(validNetworkPolicy), networkPolicyField, "", 1)
	noncanonicalNetworkPolicy := []byte(strings.TrimSuffix(withoutNetworkPolicy, "}") + networkPolicyField + "}")
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "empty", content: nil, want: "EOF"},
		{name: "array", content: []byte(`[]`), want: "top-level value"},
		{name: "syntax", content: []byte(`{"schema_version":`), want: "EOF"},
		{name: "unknown", content: append(valid[:len(valid)-1], []byte(`,"extra":true}`)...), want: "unknown field"},
		{name: "case variant", content: append(valid[:len(valid)-1], []byte(`,"Schema_Version":1}`)...), want: "unknown field"},
		{name: "duplicate", content: append(valid[:len(valid)-1], []byte(`,"schema_version":1}`)...), want: "duplicate field"},
		{name: "duplicate network policy fingerprint", content: duplicateNetworkPolicy, want: "duplicate field"},
		{name: "trailing", content: append(valid, []byte(` {}`)...), want: "trailing JSON"},
		{name: "trailing newline", content: append(valid, '\n'), want: "not canonically encoded"},
		{name: "leading whitespace", content: append([]byte(" "), valid...), want: "not canonically encoded"},
		{name: "explicit empty network policy fingerprint", content: append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"network_policy_fingerprint":""}`)...), want: "not canonically encoded"},
		{name: "noncanonical network policy field order", content: noncanonicalNetworkPolicy, want: "not canonically encoded"},
		{name: "invalid record", content: []byte(`{"schema_version":1,"installation_id":"harbor-installation","owner_identity":"501","generation":0,"loopback_pool_prefix":"127.44.0.0/24","ticket_verifier_key":"AQID"}`), want: "generation"},
		{name: "oversized", content: []byte(strings.Repeat("x", int(MaximumRecordBytes)+1)), want: "exceeds"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "owner.json")
			if err := os.WriteFile(path, test.content, privateFileMode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			store := openTestStore(t, path)
			_, err := store.Observe(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Observe() error = %v, want substring %q", err, test.want)
			}
			_, claimErr := store.Claim(context.Background(), testRecord())
			if claimErr == nil {
				t.Fatal("Store.Claim() error = nil for untrusted existing storage")
			}
		})
	}
}

// TestStoreConcurrentClaimsHaveOneImmutableWinner proves separate Store values in one process share serialization.
func TestStoreConcurrentClaimsHaveOneImmutableWinner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	const contenders = 32
	stores := make([]*Store, contenders)
	for index := range stores {
		stores[index] = openTestStore(t, path)
	}

	type result struct {
		record Record
		value  Observation
		err    error
	}
	results := make(chan result, contenders)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index, store := range stores {
		group.Add(1)
		go func(index int, store *Store) {
			defer group.Done()
			<-start
			record := testRecord()
			record.InstallationID = fmt.Sprintf("harbor-installation-%02d", index)
			value, err := store.Claim(context.Background(), record)
			results <- result{record: record, value: value, err: err}
		}(index, store)
	}
	close(start)
	group.Wait()
	close(results)

	winners := 0
	var winner Observation
	for result := range results {
		if result.err == nil {
			winners++
			winner = result.value
			if result.value.Record != result.record {
				t.Errorf("winning observation = %#v, want record %#v", result.value, result.record)
			}
			continue
		}
		if !errors.Is(result.err, ErrConflict) {
			t.Errorf("Store.Claim() loser error = %v, want ErrConflict", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("winning claims = %d, want 1", winners)
	}
	observed, err := stores[0].Observe(context.Background())
	if err != nil || observed != winner {
		t.Fatalf("Store.Observe() winner = %#v, %v, want %#v", observed, err, winner)
	}
}

// TestNewStoreRejectsUnsafePaths verifies callers cannot redirect the fixed machine-global record boundary.
func TestNewStoreRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	if _, err := NewStore("relative-owner.json"); err == nil {
		t.Fatal("NewStore() relative path error = nil")
	}
	directory := t.TempDir()
	unclean := filepath.Join(directory, "nested") + string(filepath.Separator) + ".." + string(filepath.Separator) + "owner.json"
	if _, err := NewStore(unclean); err == nil {
		t.Fatal("NewStore() unclean path error = nil")
	}

	t.Run("parent symlink", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("os.Symlink() unavailable: %v", err)
		}
		if _, err := NewStore(filepath.Join(link, "owner.json")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NewStore() parent symlink error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("record symlink", func(t *testing.T) {
		directory := t.TempDir()
		prepareTestStoreDirectory(t, directory)
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, []byte("foreign"), privateFileMode); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		path := filepath.Join(directory, "owner.json")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("os.Symlink() unavailable: %v", err)
		}
		if _, err := NewStore(path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NewStore() record symlink error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		directory := t.TempDir()
		prepareTestStoreDirectory(t, directory)
		target := filepath.Join(directory, "target.lock")
		if err := os.WriteFile(target, nil, privateFileMode); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		path := filepath.Join(directory, "owner.json")
		if err := os.Symlink(target, path+".lock"); err != nil {
			t.Skipf("os.Symlink() unavailable: %v", err)
		}
		if _, err := NewStore(path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NewStore() lock symlink error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("record directory", func(t *testing.T) {
		directory := t.TempDir()
		prepareTestStoreDirectory(t, directory)
		path := filepath.Join(directory, "owner.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		if _, err := NewStore(path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NewStore() record directory error = %v, want ErrUnsafePath", err)
		}
	})
}

// TestStoreRejectsRecordPathReplacedAfterOpening proves every operation revalidates the retained fixed path.
func TestStoreRejectsRecordPathReplacedAfterOpening(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "owner.json")
	store := openTestStore(t, path)
	target := filepath.Join(directory, "foreign.json")
	if err := os.WriteFile(target, []byte("foreign"), privateFileMode); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("os.Symlink() unavailable: %v", err)
	}
	if _, err := store.Observe(context.Background()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Store.Observe() replaced path error = %v, want ErrUnsafePath", err)
	}
	if _, err := store.Claim(context.Background(), testRecord()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Store.Claim() replaced path error = %v, want ErrUnsafePath", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "foreign" {
		t.Fatalf("foreign target = %q, %v, want unchanged", content, err)
	}
}

// TestStoreCloseIsIdempotentAndFinal ensures a released directory handle cannot be reused accidentally.
func TestStoreCloseIsIdempotentAndFinal(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	prepareTestStoreDirectory(t, filepath.Dir(path))
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if store.Path() != path {
		t.Fatalf("Store.Path() = %q, want %q", store.Path(), path)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Store.Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Store.Close() repeated error = %v", err)
	}
	if _, err := store.Observe(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Store.Observe() after close error = %v, want closed", err)
	}
}

// TestStoreCancellationBeforeIOLeavesNoCoordinationOrOwnershipFiles proves an expired helper deadline causes no disk effects.
func TestStoreCancellationBeforeIOLeavesNoCoordinationOrOwnershipFiles(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name string
		run  func(context.Context, *Store) error
	}{
		{name: "observe", run: func(ctx context.Context, store *Store) error { _, err := store.Observe(ctx); return err }},
		{name: "claim", run: func(ctx context.Context, store *Store) error { _, err := store.Claim(ctx, testRecord()); return err }},
		{name: "release", run: func(ctx context.Context, store *Store) error {
			return store.Release(ctx, strings.Repeat("a", sha256DigestHexLength))
		}},
	}
	for _, operation := range operations {
		operation := operation
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := operation.run(ctx, store); !errors.Is(err, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", err)
			}
			for _, candidate := range []string{path, path + ".lock"} {
				if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("os.Lstat(%q) error = %v, want not exist", candidate, err)
				}
			}
		})
	}
}

// TestStorePathContentionHonorsDeadline proves local waiters stop before filesystem access when the transaction is busy.
func TestStorePathContentionHonorsDeadline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if err := store.pathLock.acquire(context.Background()); err != nil {
		t.Fatalf("processPathLock.acquire() error = %v", err)
	}
	defer store.pathLock.release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := store.Claim(ctx, testRecord())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Store.Claim() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Store.Claim() cancellation took %s", elapsed)
	}
	for _, candidate := range []string{path, path + ".lock"} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("os.Lstat(%q) error = %v, want not exist", candidate, err)
		}
	}
}

// TestStorePlatformLockContentionHonorsDeadline proves cross-process-style file lock contention is bounded too.
func TestStorePlatformLockContentionHonorsDeadline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	held, err := store.openLockFile()
	if err != nil {
		t.Fatalf("Store.openLockFile() error = %v", err)
	}
	if err := acquirePlatformLock(context.Background(), held); err != nil {
		t.Fatalf("acquirePlatformLock() error = %v", err)
	}
	defer func() {
		if err := releasePlatformLock(held); err != nil {
			t.Errorf("releasePlatformLock() error = %v", err)
		}
		if err := held.Close(); err != nil {
			t.Errorf("lock.Close() error = %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = store.Observe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Store.Observe() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Store.Observe() cancellation took %s", elapsed)
	}
}

// TestStoreCrossProcessLockContentionHonorsDeadline proves the lock file coordinates independent helper processes.
func TestStoreCrossProcessLockContentionHonorsDeadline(t *testing.T) {
	const helperPathEnvironment = "HARBOR_OWNERSHIP_LOCK_TEST_PATH"
	if path := os.Getenv(helperPathEnvironment); path != "" {
		store := openTestStore(t, path)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := store.Observe(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("child Store.Observe() error = %v, want context.DeadlineExceeded", err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	held, err := store.openLockFile()
	if err != nil {
		t.Fatalf("Store.openLockFile() error = %v", err)
	}
	if err := acquirePlatformLock(context.Background(), held); err != nil {
		t.Fatalf("acquirePlatformLock() error = %v", err)
	}
	defer func() {
		if err := releasePlatformLock(held); err != nil {
			t.Errorf("releasePlatformLock() error = %v", err)
		}
		if err := held.Close(); err != nil {
			t.Errorf("lock.Close() error = %v", err)
		}
	}()

	command := exec.Command(os.Args[0], "-test.run=^TestStoreCrossProcessLockContentionHonorsDeadline$")
	command.Env = append(os.Environ(), helperPathEnvironment+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock probe error = %v\n%s", err, output)
	}
}

// openTestStore creates a store and registers its retained directory handle for cleanup.
func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	prepareTestStorePath(t, path)
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore(%q) error = %v", path, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return store
}

// prepareTestStorePath gives portable fixtures the same protected boundary an installer supplies in production.
func prepareTestStorePath(t *testing.T, path string) {
	t.Helper()
	prepareTestStoreDirectory(t, filepath.Dir(path))
	for _, candidate := range []string{path, path + ".lock"} {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
			continue
		}
		if err != nil {
			t.Fatalf("os.Lstat(%q) error = %v", candidate, err)
		}
		file, err := os.OpenFile(candidate, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("os.OpenFile(%q) error = %v", candidate, err)
		}
		secureErr := securePlatformFile(file, false)
		closeErr := file.Close()
		if err := errors.Join(secureErr, closeErr); err != nil {
			t.Fatalf("secure test store file %q: %v", candidate, err)
		}
	}
}

// prepareTestStoreDirectory applies the exact platform boundary required before NewStore can inspect a fixture.
func prepareTestStoreDirectory(t *testing.T, directory string) {
	t.Helper()
	file, err := os.Open(directory)
	if err != nil {
		t.Fatalf("os.Open(%q) error = %v", directory, err)
	}
	secureErr := securePlatformFile(file, true)
	closeErr := file.Close()
	if err := errors.Join(secureErr, closeErr); err != nil {
		t.Fatalf("secure test store directory %q: %v", directory, err)
	}
}
