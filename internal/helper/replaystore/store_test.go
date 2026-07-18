package replaystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// fixedClock returns one deterministic instant for durable replay tests.
type fixedClock struct {
	now time.Time
}

// Now returns the configured canonical test instant.
func (clock fixedClock) Now() time.Time {
	return clock.now
}

// TestStoreConsumePersistsReplayAcrossReopen proves process exit cannot make one nonce useful again.
func TestStoreConsumePersistsReplayAcrossReopen(t *testing.T) {
	directory := replayTestDirectory(t)
	clock := fixedClock{now: replayTestTime()}
	claim := replayTestClaim(clock.now)
	store := openReplayTestStore(t, directory, clock)
	if err := store.Consume(context.Background(), claim); err != nil {
		t.Fatalf("consume first claim: %v", err)
	}
	if err := store.Consume(context.Background(), claim); !errors.Is(err, helper.ErrReplay) {
		t.Fatalf("consume replay error = %v, want ErrReplay", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened := openReplayTestStore(t, directory, clock)
	defer reopened.Close()
	if err := reopened.Consume(context.Background(), claim); !errors.Is(err, helper.ErrReplay) {
		t.Fatalf("consume replay after reopen error = %v, want ErrReplay", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read replay directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != replayRecordName(claim.Key) {
		t.Fatalf("replay entries = %#v", entries)
	}
}

// TestStoreConsumeSerializesConcurrentClaims permits exactly one mutation admission for a nonce.
func TestStoreConsumeSerializesConcurrentClaims(t *testing.T) {
	clock := fixedClock{now: replayTestTime()}
	store := openReplayTestStore(t, replayTestDirectory(t), clock)
	defer store.Close()
	claim := replayTestClaim(clock.now)
	const callers = 16
	start := make(chan struct{})
	results := make(chan error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			<-start
			results <- store.Consume(context.Background(), claim)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	replays := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, helper.ErrReplay):
			replays++
		default:
			t.Fatalf("concurrent consume error = %v", err)
		}
	}
	if successes != 1 || replays != callers-1 {
		t.Fatalf("successes/replays = %d/%d, want 1/%d", successes, replays, callers-1)
	}
}

// TestStoreConsumeValidatesBeforeStorage proves invalid or canceled authority leaves no durable claim.
func TestStoreConsumeValidatesBeforeStorage(t *testing.T) {
	clock := fixedClock{now: replayTestTime()}
	directory := replayTestDirectory(t)
	store := openReplayTestStore(t, directory, clock)
	defer store.Close()
	invalid := replayTestClaim(clock.now)
	invalid.Key.Nonce = "short"
	if err := store.Consume(context.Background(), invalid); err == nil {
		t.Fatal("Consume() accepted an invalid claim")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Consume(canceled, replayTestClaim(clock.now)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume() cancellation error = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read replay directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid claims created entries %#v", entries)
	}
}

// TestStoreConsumeFailsClosedForCorruptOrUnsafeTombstones prevents path state from becoming mutation permission.
func TestStoreConsumeFailsClosedForCorruptOrUnsafeTombstones(t *testing.T) {
	clock := fixedClock{now: replayTestTime()}
	claim := replayTestClaim(clock.now)
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string, string)
	}{
		{name: "corrupt", prepare: func(t *testing.T, directory string, name string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(directory, name), []byte("not-json"), 0o600); err != nil {
				t.Fatalf("write corrupt tombstone: %v", err)
			}
		}},
		{name: "different record", prepare: func(t *testing.T, directory string, name string) {
			t.Helper()
			different := replayRecordFromClaim(claim)
			different.Nonce = strings.Repeat("z", 32)
			content, err := encodeReplayRecord(different)
			if err != nil {
				t.Fatalf("encode different tombstone: %v", err)
			}
			if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
				t.Fatalf("write different tombstone: %v", err)
			}
		}},
		{name: "symlink", prepare: func(t *testing.T, directory string, name string) {
			t.Helper()
			target := filepath.Join(directory, "target")
			if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
				t.Fatalf("write symlink target: %v", err)
			}
			if err := os.Symlink(target, filepath.Join(directory, name)); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := replayTestDirectory(t)
			name := replayRecordName(claim.Key)
			test.prepare(t, directory, name)
			store := openReplayTestStore(t, directory, clock)
			defer store.Close()
			err := store.Consume(context.Background(), claim)
			if !errors.Is(err, helper.ErrReplayProtectionUnavailable) || errors.Is(err, helper.ErrReplay) {
				t.Fatalf("Consume() error = %v, want replay protection unavailable", err)
			}
		})
	}
}

// TestOpenRejectsUnsafeDirectoriesAndClosedUse covers fixed-path and lifecycle failures.
func TestOpenRejectsUnsafeDirectoriesAndClosedUse(t *testing.T) {
	clock := fixedClock{now: replayTestTime()}
	if _, err := open("relative", clock); err == nil {
		t.Fatal("open() accepted a relative directory")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err == nil {
		if _, openErr := open(link, clock); openErr == nil {
			t.Fatal("open() accepted a symlink directory")
		}
	}

	directory := replayTestDirectory(t)
	store := openReplayTestStore(t, directory, clock)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store twice: %v", err)
	}
	if err := store.Consume(context.Background(), replayTestClaim(clock.now)); !errors.Is(err, helper.ErrReplayProtectionUnavailable) {
		t.Fatalf("Consume() after close error = %v", err)
	}
}

// TestReplayRecordEncodingIsCanonical proves durable bytes reject aliases and trailing values.
func TestReplayRecordEncodingIsCanonical(t *testing.T) {
	record := replayRecordFromClaim(replayTestClaim(replayTestTime()))
	content, err := encodeReplayRecord(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	decoded, err := decodeReplayRecord(content)
	if err != nil || decoded != record {
		t.Fatalf("decode canonical record = %#v, %v", decoded, err)
	}
	for _, invalid := range [][]byte{
		append([]byte(" "), content...),
		append(append([]byte(nil), content...), []byte("{}")...),
		[]byte(`{"version":1,"version":1}`),
	} {
		if _, err := decodeReplayRecord(invalid); err == nil {
			t.Fatalf("decodeReplayRecord(%q) accepted noncanonical bytes", invalid)
		}
	}
}

// replayTestDirectory creates the private root required by the current platform contract.
func replayTestDirectory(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "replays")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create replay directory: %v", err)
	}
	return directory
}

// openReplayTestStore opens one fixture and registers best-effort cleanup for failed assertions.
func openReplayTestStore(t *testing.T, directory string, clock helper.Clock) *Store {
	t.Helper()
	store, err := open(directory, clock)
	if err != nil {
		t.Fatalf("open replay store: %v", err)
	}
	return store
}

// replayTestClaim returns one valid claim with a canonical future UTC expiry.
func replayTestClaim(now time.Time) helper.ReplayClaim {
	return helper.ReplayClaim{
		Key: helper.ReplayKey{
			InstallationID:      "harbor-test-installation",
			OwnershipGeneration: 1,
			Nonce:               strings.Repeat("n", 32),
		},
		ExpiresAt: now.Add(time.Minute),
	}
}

// replayTestTime returns the stable UTC instant shared by replay fixtures.
func replayTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}
