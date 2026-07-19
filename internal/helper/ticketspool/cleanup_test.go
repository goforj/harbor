package ticketspool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
)

// TestCleanupExpiredRemovesOnlyAuthenticatedExpiredReferences proves cleanup cannot consume current or untrusted entries.
func TestCleanupExpiredRemovesOnlyAuthenticatedExpiredReferences(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(21)
	_, otherPrivateKey := testKey(22)
	publisher, directory := newTestPublisher(t, now, nil)

	expiredReference := cleanupReference('a')
	wrongKeyReference := cleanupReference('b')
	currentReference := cleanupReference('c')
	malformedReference := cleanupReference('d')
	writeCleanupTicket(t, directory, expiredReference, privateKey, now.Add(-2*time.Minute), "expired-matching")
	writeCleanupTicket(t, directory, wrongKeyReference, otherPrivateKey, now.Add(-2*time.Minute), "expired-other-key")
	writeCleanupTicket(t, directory, currentReference, otherPrivateKey, now, "current-other-key")
	writePrivateFile(t, filepath.Join(directory, string(malformedReference)), []byte("not an envelope"))
	writePrivateFile(t, filepath.Join(directory, stagingPrefix+strings.Repeat("e", 64)+stagingSuffix), []byte("staging"))
	writePrivateFile(t, filepath.Join(directory, "operator-note"), []byte("preserve"))

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 10)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	want := CleanupResult{Scanned: 6, Removed: 1, Preserved: 5}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryMissing(t, directory, expiredReference)
	for _, reference := range []helper.TicketReference{wrongKeyReference, currentReference, malformedReference} {
		assertCleanupEntryExists(t, directory, string(reference))
	}
	assertCleanupEntryExists(t, directory, stagingPrefix+strings.Repeat("e", 64)+stagingSuffix)
	assertCleanupEntryExists(t, directory, "operator-note")
}

// TestCleanupExpiredTreatsTheExpiryInstantAsExpired covers the exact boundary shared with ticket redemption.
func TestCleanupExpiredTreatsTheExpiryInstantAsExpired(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(23)
	publisher, directory := newTestPublisher(t, now, nil)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-time.Minute), "boundary")

	result, err := publisher.CleanupExpired(nil, verifierKey, 1)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	want := CleanupResult{Scanned: 1, Removed: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryMissing(t, directory, reference)
}

// TestCleanupExpiredBoundsDirectoryWork verifies both truncation detection and the exact end-of-directory case.
func TestCleanupExpiredBoundsDirectoryWork(t *testing.T) {
	verifierKey, _ := testKey(24)
	tests := []struct {
		name         string
		entries      int
		limit        int
		limitReached bool
	}{
		{name: "more entries remain", entries: 3, limit: 2, limitReached: true},
		{name: "exact end", entries: 2, limit: 2, limitReached: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher, directory := newTestPublisher(t, testTime(), nil)
			for index := range test.entries {
				writePrivateFile(t, filepath.Join(directory, string(rune('a'+index))), []byte("preserve"))
			}

			result, err := publisher.CleanupExpired(context.Background(), verifierKey, test.limit)
			if err != nil {
				t.Fatalf("CleanupExpired() error = %v", err)
			}
			want := CleanupResult{Scanned: test.limit, Preserved: test.limit, LimitReached: test.limitReached}
			if result != want {
				t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
			}
		})
	}
}

// TestCleanupExpiredRejectsInvalidInputsBeforeMutation covers every public bound and cancellation guard.
func TestCleanupExpiredRejectsInvalidInputsBeforeMutation(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(25)
	publisher, directory := newTestPublisher(t, now, nil)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "still-present")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name  string
		ctx   context.Context
		key   ed25519.PublicKey
		limit int
	}{
		{name: "canceled", ctx: canceled, key: verifierKey, limit: 1},
		{name: "short key", ctx: context.Background(), key: ed25519.PublicKey("short"), limit: 1},
		{name: "zero limit", ctx: context.Background(), key: verifierKey, limit: 0},
		{name: "excessive limit", ctx: context.Background(), key: verifierKey, limit: MaximumCleanupEntries + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := publisher.CleanupExpired(test.ctx, test.key, test.limit)
			if err == nil || result != (CleanupResult{}) {
				t.Fatalf("CleanupExpired() result = %#v, %v, want empty result and error", result, err)
			}
			assertCleanupEntryExists(t, directory, string(reference))
		})
	}
}

// TestCleanupExpiredRejectsClosedPublisher keeps cleanup on the same terminal retained-handle lifecycle as publication.
func TestCleanupExpiredRejectsClosedPublisher(t *testing.T) {
	verifierKey, _ := testKey(26)
	publisher, _ := newTestPublisher(t, testTime(), nil)
	if err := publisher.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1); err == nil || result != (CleanupResult{}) {
		t.Fatalf("CleanupExpired() result = %#v, %v, want empty result and closed error", result, err)
	}
}

// TestCleanupExpiredPreservesEntryWhenRemovalFails proves a failed destructive boundary never reports success.
func TestCleanupExpiredPreservesEntryWhenRemovalFails(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(27)
	injected := errors.New("injected removal failure")
	dependencies := testDependencies(now)
	dependencies.files.remove = func(*os.Root, string) error { return injected }
	publisher, directory := newTestPublisher(t, now, &dependencies)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "remove-failure")

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if !errors.Is(err, injected) {
		t.Fatalf("CleanupExpired() error = %v, want injected failure", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// TestCleanupExpiredSurfacesReopenFailures distinguishes filesystem faults from ordinary preserved content.
func TestCleanupExpiredSurfacesReopenFailures(t *testing.T) {
	verifierKey, _ := testKey(34)
	injected := errors.New("injected reopen failure")
	dependencies := testDependencies(testTime())
	dependencies.files.reopen = func(*os.Root, *os.File, string, string) (*os.File, error) {
		return nil, injected
	}
	publisher, directory := newTestPublisher(t, testTime(), &dependencies)
	reference := cleanupReference('a')
	writePrivateFile(t, filepath.Join(directory, string(reference)), []byte("preserve"))

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if !errors.Is(err, injected) {
		t.Fatalf("CleanupExpired() error = %v, want injected failure", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// TestCleanupExpiredToleratesConcurrentReferenceConsumption covers a redeemer winning before cleanup opens the name.
func TestCleanupExpiredToleratesConcurrentReferenceConsumption(t *testing.T) {
	verifierKey, _ := testKey(35)
	dependencies := testDependencies(testTime())
	baseReopen := dependencies.files.reopen
	dependencies.files.reopen = func(root *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
		if err := root.Remove(name); err != nil {
			return nil, err
		}
		return baseReopen(root, directory, directoryPath, name)
	}
	publisher, directory := newTestPublisher(t, testTime(), &dependencies)
	reference := cleanupReference('a')
	writePrivateFile(t, filepath.Join(directory, string(reference)), []byte("claimed concurrently"))

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryMissing(t, directory, reference)
}

// TestCleanupExpiredReportsDurabilityUncertainty proves removal success remains visible when its directory barrier fails.
func TestCleanupExpiredReportsDurabilityUncertainty(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(28)
	injected := errors.New("injected directory sync failure")
	dependencies := testDependencies(now)
	dependencies.files.syncDirectory = func(*os.File) error { return injected }
	publisher, directory := newTestPublisher(t, now, &dependencies)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "sync-failure")

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if !errors.Is(err, ErrCleanupDurabilityUncertain) || !errors.Is(err, injected) {
		t.Fatalf("CleanupExpired() error = %v, want durability and injected failures", err)
	}
	want := CleanupResult{Scanned: 1, Removed: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryMissing(t, directory, reference)
}

// TestCleanupExpiredRejectsUnsafeEntriesWithoutDeletion keeps security drift observable and non-destructive.
func TestCleanupExpiredRejectsUnsafeEntriesWithoutDeletion(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(30)
	publisher, directory := newTestPublisher(t, now, nil)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "unsafe")
	makeStagedFileUnsafe(t, publisher.root, string(reference))

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("CleanupExpired() error = %v, want ErrUnsafePath", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// TestCleanupExpiredPreservesInvalidSizedEntries distinguishes malformed content from a filesystem fault.
func TestCleanupExpiredPreservesInvalidSizedEntries(t *testing.T) {
	verifierKey, _ := testKey(30)
	publisher, directory := newTestPublisher(t, testTime(), nil)
	reference := cleanupReference('a')
	writePrivateFile(t, filepath.Join(directory, string(reference)), nil)

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// TestCleanupExpiredSynchronizesBeforeReturningMidScanCancellation covers partial progress under cancellation.
func TestCleanupExpiredSynchronizesBeforeReturningMidScanCancellation(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(31)
	ctx, cancel := context.WithCancel(context.Background())
	dependencies := testDependencies(now)
	baseRemove := dependencies.files.remove
	dependencies.files.remove = func(root *os.Root, name string) error {
		err := baseRemove(root, name)
		cancel()
		return err
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	writeCleanupTicket(t, directory, cleanupReference('a'), privateKey, now.Add(-2*time.Minute), "cancel-a")
	writeCleanupTicket(t, directory, cleanupReference('b'), privateKey, now.Add(-2*time.Minute), "cancel-b")

	result, err := publisher.CleanupExpired(ctx, verifierKey, 2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CleanupExpired() error = %v, want cancellation", err)
	}
	want := CleanupResult{Scanned: 1, Removed: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("pending entries after cancellation = %#v, %v, want one preserved ticket", entries, readErr)
	}
}

// TestCleanupExpiredRejectsRootDriftAfterAuthentication proves deletion rechecks the directory boundary at the last safe point.
func TestCleanupExpiredRejectsRootDriftAfterAuthentication(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(32)
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	var restore func()
	dependencies.files.reopen = func(root *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
		file, err := baseReopen(root, directory, directoryPath, name)
		if err == nil && restore == nil {
			restore = makeOpenedTestDirectoryUnsafe(t, directoryPath)
		}
		return file, err
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	reference := cleanupReference('a')
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "root-drift")

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	if restore != nil {
		restore()
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("CleanupExpired() error = %v, want ErrUnsafePath", err)
	}
	want := CleanupResult{Scanned: 1, Preserved: 1}
	if result != want {
		t.Fatalf("CleanupExpired() result = %#v, want %#v", result, want)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// TestCleanupExpiredSurfacesCleanupCursorCloseFailure keeps retained-handle failures observable before mutation.
func TestCleanupExpiredSurfacesCleanupCursorCloseFailure(t *testing.T) {
	verifierKey, _ := testKey(33)
	injected := errors.New("injected cleanup cursor close failure")
	publisher, _ := newTestPublisher(t, testTime(), nil)
	baseClose := publisher.dependencies.files.closeFile
	firstDirectoryClose := true
	publisher.dependencies.files.closeFile = func(file *os.File) error {
		info, _ := file.Stat()
		err := baseClose(file)
		if firstDirectoryClose && info != nil && info.IsDir() {
			firstDirectoryClose = false
			return errors.Join(err, injected)
		}
		return err
	}

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 1)
	publisher.dependencies.files.closeFile = baseClose
	if !errors.Is(err, injected) || result != (CleanupResult{}) {
		t.Fatalf("CleanupExpired() result = %#v, %v, want empty result and injected failure", result, err)
	}
}

// TestCleanupEntriesRejectsUnsafeCursor validates the independently opened scan handle itself.
func TestCleanupEntriesRejectsUnsafeCursor(t *testing.T) {
	publisher, directory := newTestPublisher(t, testTime(), nil)
	restore := makeOpenedTestDirectoryUnsafe(t, directory)
	_, _, err := publisher.cleanupEntries(1)
	restore()
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("cleanupEntries() error = %v, want ErrUnsafePath", err)
	}
}

// TestReadCleanupCandidateRejectsUnstableFiles covers bounded-read failures without interpreting their bytes.
func TestReadCleanupCandidateRejectsUnstableFiles(t *testing.T) {
	publisher, directory := newTestPublisher(t, testTime(), nil)

	t.Run("closed before seek", func(t *testing.T) {
		name := string(cleanupReference('a'))
		writePrivateFile(t, filepath.Join(directory, name), []byte("content"))
		file, info := openCleanupFixture(t, publisher, name)
		if err := file.Close(); err != nil {
			t.Fatalf("close cleanup fixture: %v", err)
		}
		if _, _, err := readCleanupCandidate(file, info); err == nil {
			t.Fatal("readCleanupCandidate() accepted a closed file")
		}
	})

	t.Run("stream exceeds bound", func(t *testing.T) {
		largeName := string(cleanupReference('b'))
		smallName := string(cleanupReference('c'))
		writePrivateFile(t, filepath.Join(directory, largeName), bytes.Repeat([]byte{'x'}, ticketauth.MaxEnvelopeBytes+1))
		writePrivateFile(t, filepath.Join(directory, smallName), []byte("x"))
		large, _ := openCleanupFixture(t, publisher, largeName)
		defer func() { _ = large.Close() }()
		small, smallInfo := openCleanupFixture(t, publisher, smallName)
		if err := small.Close(); err != nil {
			t.Fatalf("close size fixture: %v", err)
		}
		if _, eligible, err := readCleanupCandidate(large, smallInfo); err != nil || eligible {
			t.Fatalf("readCleanupCandidate() result = eligible %t, %v, want preserved oversized stream", eligible, err)
		}
	})

	t.Run("security changes after open", func(t *testing.T) {
		name := string(cleanupReference('d'))
		writePrivateFile(t, filepath.Join(directory, name), []byte("content"))
		file, info := openCleanupFixture(t, publisher, name)
		defer func() { _ = file.Close() }()
		makeStagedFileUnsafe(t, publisher.root, name)
		if _, eligible, err := readCleanupCandidate(file, info); !errors.Is(err, ErrUnsafePath) || eligible {
			t.Fatalf("readCleanupCandidate() result = eligible %t, %v, want preserved ErrUnsafePath", eligible, err)
		}
	})

	t.Run("identity differs", func(t *testing.T) {
		firstName := string(cleanupReference('e'))
		secondName := string(cleanupReference('f'))
		writePrivateFile(t, filepath.Join(directory, firstName), []byte("same"))
		writePrivateFile(t, filepath.Join(directory, secondName), []byte("same"))
		first, _ := openCleanupFixture(t, publisher, firstName)
		defer func() { _ = first.Close() }()
		second, secondInfo := openCleanupFixture(t, publisher, secondName)
		if err := second.Close(); err != nil {
			t.Fatalf("close identity fixture: %v", err)
		}
		if _, eligible, err := readCleanupCandidate(first, secondInfo); err != nil || eligible {
			t.Fatalf("readCleanupCandidate() result = eligible %t, %v, want preserved changed identity", eligible, err)
		}
	})
}

// TestCleanupExpiredPreservesAReplacedName proves authentication of one inode cannot delete a later name occupant.
func TestCleanupExpiredPreservesAReplacedName(t *testing.T) {
	now := testTime()
	verifierKey, privateKey := testKey(29)
	reference := cleanupReference('a')
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	replaced := false
	var directory string
	dependencies.files.reopen = func(root *os.Root, openedDirectory *os.File, directoryPath string, name string) (*os.File, error) {
		file, err := baseReopen(root, openedDirectory, directoryPath, name)
		if err != nil || name != string(reference) || replaced {
			return file, err
		}
		replaced = true
		if err := os.Rename(filepath.Join(directory, name), filepath.Join(directory, "moved-expired")); err != nil {
			_ = file.Close()
			return nil, err
		}
		writeCleanupTicket(t, directory, reference, privateKey, now, "replacement-current")
		return file, nil
	}
	publisher, openedPath := newTestPublisher(t, now, &dependencies)
	directory = openedPath
	writeCleanupTicket(t, directory, reference, privateKey, now.Add(-2*time.Minute), "original-expired")

	result, err := publisher.CleanupExpired(context.Background(), verifierKey, 3)
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	if result.Removed != 0 || result.Scanned != 1 || result.Preserved != 1 {
		t.Fatalf("CleanupExpired() result = %#v, want the scanned replacement preserved", result)
	}
	assertCleanupEntryExists(t, directory, string(reference))
}

// cleanupReference returns one canonical deterministic final-reference name.
func cleanupReference(marker byte) helper.TicketReference {
	return helper.TicketReference(strings.Repeat(string(marker), 64))
}

// writeCleanupTicket writes one canonical signed envelope at the exact reference path used by cleanup.
func writeCleanupTicket(t *testing.T, directory string, reference helper.TicketReference, privateKey ed25519.PrivateKey, signingTime time.Time, nonce string) {
	t.Helper()
	ticket := testTicket(signingTime, nonce)
	envelope, err := ticketauth.Sign(ticket, privateKey, signingTime)
	if err != nil {
		t.Fatalf("sign cleanup fixture: %v", err)
	}
	content, err := ticketauth.Encode(envelope)
	if err != nil {
		t.Fatalf("encode cleanup fixture: %v", err)
	}
	writePrivateFile(t, filepath.Join(directory, string(reference)), content)
}

// assertCleanupEntryExists verifies cleanup preserved one exact pending name.
func assertCleanupEntryExists(t *testing.T, directory string, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(directory, name)); err != nil {
		t.Fatalf("expected cleanup entry %q to exist: %v", name, err)
	}
}

// assertCleanupEntryMissing verifies cleanup removed one exact pending name.
func assertCleanupEntryMissing(t *testing.T, directory string, reference helper.TicketReference) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(directory, string(reference))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected cleanup reference %q to be absent, stat error = %v", reference, err)
	}
}

// openCleanupFixture reopens one private fixture through the production no-follow boundary.
func openCleanupFixture(t *testing.T, publisher *Publisher, name string) (*os.File, os.FileInfo) {
	t.Helper()
	file, err := publisher.dependencies.files.reopen(publisher.root, publisher.directory, publisher.path, name)
	if err != nil {
		t.Fatalf("open cleanup fixture %q: %v", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("stat cleanup fixture %q: %v", name, err)
	}
	return file, info
}
