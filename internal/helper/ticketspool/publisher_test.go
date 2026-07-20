package ticketspool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// TestPublisherPublishPersistsCanonicalEnvelope proves the happy path is private, durable, and independently verifiable.
func TestPublisherPublishPersistsCanonicalEnvelope(t *testing.T) {
	now := testTime()
	publicKey, privateKey := testKey(1)
	ticket := testTicket(now, "nonce-one")
	publisher, directory := newTestPublisher(t, now, nil)

	reference, err := publisher.Publish(nil, ticket, privateKey)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if err := reference.Validate(); err != nil {
		t.Fatalf("published reference %q is invalid: %v", reference, err)
	}
	content := readPublished(t, directory, reference)
	envelope, err := ticketauth.Decode(content)
	if err != nil {
		t.Fatalf("decode published envelope: %v", err)
	}
	verified, err := envelope.Verify(publicKey, now)
	if err != nil {
		t.Fatalf("verify published envelope: %v", err)
	}
	if !reflect.DeepEqual(verified, ticket) {
		t.Fatalf("verified ticket = %#v, want %#v", verified, ticket)
	}
	assertOnlyFinalFiles(t, directory, 1)
	assertPrivateRegularFile(t, filepath.Join(directory, string(reference)))
}

// TestOpenRejectsUnsafeDirectories proves publication never creates or repairs its installer-owned boundary.
func TestOpenRejectsUnsafeDirectories(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := open(missing, testDependencies(testTime())); err == nil {
		t.Fatal("open() created or accepted a missing directory")
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory stat error = %v, want not exist", err)
	}

	permissive := filepath.Join(root, "permissive")
	makeTestDirectoryUnsafe(t, permissive)
	if _, err := open(permissive, testDependencies(testTime())); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open() permissive error = %v, want ErrUnsafePath", err)
	}
	assertTestDirectoryUnsafe(t, permissive)

	private := filepath.Join(root, "private")
	prepareTestDirectory(t, private)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Logf("skip unavailable symlink fixture: %v", err)
		return
	}
	if _, err := open(alias, testDependencies(testTime())); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open() symlink error = %v, want ErrUnsafePath", err)
	}
}

// TestOpenRejectsInvalidConfiguration covers non-filesystem inputs before any directory handle is retained.
func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	if _, err := open("relative", testDependencies(testTime())); err == nil {
		t.Fatal("open() accepted a relative path")
	}
	if _, err := open(filepath.Join(t.TempDir(), "missing"), publisherDependencies{}); err == nil {
		t.Fatal("open() accepted incomplete dependencies")
	}
	path := filepath.Join(t.TempDir(), "file")
	writePrivateFile(t, path, []byte("not a directory"))
	if _, err := open(path, testDependencies(testTime())); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open() regular file error = %v, want ErrUnsafePath", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := open(missing, testDependencies(testTime())); !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("open() missing path error = %v, want ordinary filesystem absence", err)
	}
}

// TestOpenDefaultNeverProvisions verifies the production entry point remains a read-only opener when installation is absent.
func TestOpenDefaultNeverProvisions(t *testing.T) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return
	}
	_, beforeErr := os.Lstat(paths.PendingDirectory)
	publisher, err := OpenDefault()
	if err == nil {
		if err := publisher.Close(); err != nil {
			t.Fatalf("close default publisher: %v", err)
		}
	}
	if errors.Is(beforeErr, fs.ErrNotExist) && (!errors.Is(err, ErrNotInstalled) || errors.Is(err, fs.ErrNotExist)) {
		t.Fatalf("OpenDefault() missing path error = %v, want only ErrNotInstalled", err)
	}
	_, afterErr := os.Lstat(paths.PendingDirectory)
	if errors.Is(beforeErr, fs.ErrNotExist) && !errors.Is(afterErr, fs.ErrNotExist) {
		t.Fatalf("OpenDefault() provisioned absent path %q", paths.PendingDirectory)
	}
}

// TestPublisherCloseIsIdempotentAndTerminal keeps a closed retained handle from silently reopening by path.
func TestPublisherCloseIsIdempotentAndTerminal(t *testing.T) {
	publisher, _ := newTestPublisher(t, testTime(), nil)
	if err := publisher.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := publisher.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	_, privateKey := testKey(2)
	if _, err := publisher.Publish(context.Background(), testTicket(testTime(), "closed"), privateKey); err == nil {
		t.Fatal("Publish() accepted a closed publisher")
	}
}

// TestPublisherSigningFailuresLeaveNoFiles proves invalid authority never reaches the staging namespace.
func TestPublisherSigningFailuresLeaveNoFiles(t *testing.T) {
	now := testTime()
	tests := []struct {
		name       string
		mutate     func(*helper.Ticket)
		privateKey ed25519.PrivateKey
	}{
		{name: "invalid ticket", mutate: func(ticket *helper.Ticket) { ticket.ApprovedAddress = "192.0.2.1" }, privateKey: testPrivateKey(2)},
		{name: "invalid key", mutate: func(*helper.Ticket) {}, privateKey: ed25519.PrivateKey("short")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher, directory := newTestPublisher(t, now, nil)
			ticket := testTicket(now, test.name)
			test.mutate(&ticket)
			reference, err := publisher.Publish(context.Background(), ticket, test.privateKey)
			if reference != "" || err == nil {
				t.Fatalf("Publish() result = %q, %v, want signing failure", reference, err)
			}
			assertOnlyFinalFiles(t, directory, 0)
		})
	}
}

// TestPublisherCancellationCleansStaging proves cancellation before the commit attempt leaves no authority or residue.
func TestPublisherCancellationCleansStaging(t *testing.T) {
	now := testTime()
	ctx, cancel := context.WithCancel(context.Background())
	dependencies := testDependencies(now)
	baseWrite := dependencies.files.write
	dependencies.files.write = func(writer io.Writer, content []byte) error {
		if err := baseWrite(writer, content); err != nil {
			return err
		}
		cancel()
		return nil
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(3)
	reference, err := publisher.Publish(ctx, testTicket(now, "cancelled"), privateKey)
	if !errors.Is(err, context.Canceled) || reference != "" {
		t.Fatalf("Publish() result = %q, %v, want empty reference and cancellation", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherImmediateCancellationAvoidsEntropy proves an already canceled request stops before identity generation.
func TestPublisherImmediateCancellationAvoidsEntropy(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	called := false
	dependencies.referenceRandom = func([]byte) error {
		called = true
		return nil
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, privateKey := testKey(3)
	reference, err := publisher.Publish(ctx, testTicket(now, "immediate-cancel"), privateKey)
	if reference != "" || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Publish() result = %q, %v, entropy called %t", reference, err, called)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherRejectsRootPermissionDrift proves a retained handle does not make later boundary changes invisible.
func TestPublisherRejectsRootPermissionDrift(t *testing.T) {
	now := testTime()
	publisher, directory := newTestPublisher(t, now, nil)
	restore := makeOpenedTestDirectoryUnsafe(t, directory)
	_, privateKey := testKey(3)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "root-drift"), privateKey)
	if reference != "" || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Publish() result = %q, %v, want ErrUnsafePath", reference, err)
	}
	restore()
}

// TestPublisherValidateRootReportsClosedHandle covers retained-handle failure without reopening by absolute path.
func TestPublisherValidateRootReportsClosedHandle(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	publisher, err := open(directory, testDependencies(testTime()))
	if err != nil {
		t.Fatalf("open publisher: %v", err)
	}
	if err := publisher.directory.Close(); err != nil {
		t.Fatalf("close retained directory: %v", err)
	}
	if err := publisher.validateRoot(); err == nil {
		t.Fatal("validateRoot() accepted a closed directory handle")
	}
	if err := publisher.root.Close(); err != nil {
		t.Fatalf("close retained root: %v", err)
	}
}

// TestCreateStagingHonorsCancellation covers cancellation before a staging name or file is created.
func TestCreateStagingHonorsCancellation(t *testing.T) {
	publisher, directory := newTestPublisher(t, testTime(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := publisher.createStaging(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("createStaging() error = %v, want cancellation", err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherPrecommitFaultsLeaveNoResidue covers write, file-sync, and unapplied-rename failures.
func TestPublisherPrecommitFaultsLeaveNoResidue(t *testing.T) {
	injected := errors.New("injected storage failure")
	tests := []struct {
		name   string
		mutate func(*publisherDependencies)
	}{
		{name: "write", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.write = func(io.Writer, []byte) error { return injected }
		}},
		{name: "file sync", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.syncFile = func(*os.File) error { return injected }
		}},
		{name: "rename", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.rename = func(*os.Root, *os.File, *os.File, string, string) (bool, error) {
				return false, injected
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := testTime()
			dependencies := testDependencies(now)
			test.mutate(&dependencies)
			publisher, directory := newTestPublisher(t, now, &dependencies)
			_, privateKey := testKey(4)
			reference, err := publisher.Publish(context.Background(), testTicket(now, test.name), privateKey)
			if !errors.Is(err, injected) || reference != "" {
				t.Fatalf("Publish() result = %q, %v, want empty reference and injected failure", reference, err)
			}
			assertOnlyFinalFiles(t, directory, 0)
		})
	}
}

// TestPublisherAdditionalStorageFaults covers exclusive create, reopen, staging stat, and invalid rename outcomes.
func TestPublisherAdditionalStorageFaults(t *testing.T) {
	injected := errors.New("injected storage boundary failure")
	tests := []struct {
		name   string
		mutate func(*publisherDependencies)
	}{
		{name: "create", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.create = func(*os.Root, *os.File, string, string) (*os.File, error) { return nil, injected }
		}},
		{name: "staging stat", mutate: func(dependencies *publisherDependencies) {
			baseCreate := dependencies.files.create
			dependencies.files.create = func(root *os.Root, directory *os.File, path string, name string) (*os.File, error) {
				file, err := baseCreate(root, directory, path, name)
				if err != nil {
					return nil, err
				}
				if err := file.Close(); err != nil {
					return nil, err
				}
				return file, nil
			}
		}},
		{name: "reopen", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.reopen = func(*os.Root, *os.File, string, string) (*os.File, error) { return nil, injected }
		}},
		{name: "rename no result", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.rename = func(*os.Root, *os.File, *os.File, string, string) (bool, error) { return false, nil }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := testTime()
			dependencies := testDependencies(now)
			test.mutate(&dependencies)
			publisher, directory := newTestPublisher(t, now, &dependencies)
			_, privateKey := testKey(4)
			reference, err := publisher.Publish(context.Background(), testTicket(now, test.name), privateKey)
			if reference != "" || err == nil {
				t.Fatalf("Publish() result = %q, %v, want precommit failure", reference, err)
			}
			assertOnlyFinalFiles(t, directory, 0)
		})
	}
}

// TestPublisherCloseFaultsDistinguishCommitState proves the same close failure is ordinary before rename and uncertain after it.
func TestPublisherCloseFaultsDistinguishCommitState(t *testing.T) {
	injected := errors.New("injected close failure")
	tests := []struct {
		name          string
		failCall      uint32
		wantCommitted bool
	}{
		{name: "staging", failCall: 1},
		{name: "published", failCall: 2, wantCommitted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := testTime()
			dependencies := testDependencies(now)
			baseClose := dependencies.files.closeFile
			var calls atomic.Uint32
			dependencies.files.closeFile = func(file *os.File) error {
				closeErr := baseClose(file)
				if calls.Add(1) == test.failCall {
					return errors.Join(closeErr, injected)
				}
				return closeErr
			}
			publisher, directory := newTestPublisher(t, now, &dependencies)
			_, privateKey := testKey(4)
			reference, err := publisher.Publish(context.Background(), testTicket(now, test.name+"-close"), privateKey)
			if !errors.Is(err, injected) {
				t.Fatalf("Publish() error = %v, want injected close failure", err)
			}
			if test.wantCommitted {
				if reference == "" || !errors.Is(err, ErrDurabilityUncertain) {
					t.Fatalf("published close result = %q, %v, want uncertain reference", reference, err)
				}
				_ = readPublished(t, directory, reference)
				return
			}
			if reference != "" {
				t.Fatalf("staging close returned committed reference %q", reference)
			}
			assertOnlyFinalFiles(t, directory, 0)
		})
	}
}

// TestPublisherCleanupFailurePreservesBothCauses proves residue cannot conceal the operation that triggered cleanup.
func TestPublisherCleanupFailurePreservesBothCauses(t *testing.T) {
	writeFailure := errors.New("injected write failure")
	cleanupFailure := errors.New("injected cleanup failure")
	now := testTime()
	dependencies := testDependencies(now)
	dependencies.files.write = func(io.Writer, []byte) error { return writeFailure }
	baseRemove := dependencies.files.remove
	dependencies.files.remove = func(root *os.Root, name string) error {
		_ = baseRemove(root, name)
		return cleanupFailure
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(4)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "cleanup"), privateKey)
	if reference != "" || !errors.Is(err, writeFailure) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("Publish() result = %q, %v, want joined write and cleanup failures", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherCancellationAfterValidationCleansStaging covers the last cancellation boundary before rename.
func TestPublisherCancellationAfterValidationCleansStaging(t *testing.T) {
	now := testTime()
	ctx, cancel := context.WithCancel(context.Background())
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	dependencies.files.reopen = func(root *os.Root, directory *os.File, path string, name string) (*os.File, error) {
		file, err := baseReopen(root, directory, path, name)
		cancel()
		return file, err
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(4)
	reference, err := publisher.Publish(ctx, testTicket(now, "late-cancel"), privateKey)
	if reference != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() result = %q, %v, want cancellation", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherRejectsRootDriftBeforeCommit proves the second boundary check guards the exact rename point.
func TestPublisherRejectsRootDriftBeforeCommit(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	var restore func()
	dependencies.files.reopen = func(root *os.Root, directory *os.File, path string, name string) (*os.File, error) {
		file, err := baseReopen(root, directory, path, name)
		if err == nil {
			restore = makeOpenedTestDirectoryUnsafe(t, path)
		}
		return file, err
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(4)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "commit-root-drift"), privateKey)
	if restore != nil {
		restore()
	}
	if reference != "" || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Publish() result = %q, %v, want ErrUnsafePath", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherCancellationAfterCollisionStopsRetries proves cancellation is observed between immutable references.
func TestPublisherCancellationAfterCollisionStopsRetries(t *testing.T) {
	now := testTime()
	ctx, cancel := context.WithCancel(context.Background())
	dependencies := testDependencies(now)
	dependencies.files.rename = func(*os.Root, *os.File, *os.File, string, string) (bool, error) {
		cancel()
		return false, fs.ErrExist
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(4)
	reference, err := publisher.Publish(ctx, testTicket(now, "collision-cancel"), privateKey)
	if reference != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() result = %q, %v, want cancellation", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherAppliedDurabilityFailuresReturnReference proves callers can reconcile an already committed name.
func TestPublisherAppliedDurabilityFailuresReturnReference(t *testing.T) {
	injected := errors.New("injected durability failure")
	tests := []struct {
		name   string
		mutate func(*publisherDependencies)
	}{
		{name: "rename write through", mutate: func(dependencies *publisherDependencies) {
			baseRename := dependencies.files.rename
			dependencies.files.rename = func(root *os.Root, directory *os.File, source *os.File, oldName string, newName string) (bool, error) {
				applied, err := baseRename(root, directory, source, oldName, newName)
				if err != nil || !applied {
					return applied, err
				}
				return true, injected
			}
		}},
		{name: "directory sync", mutate: func(dependencies *publisherDependencies) {
			dependencies.files.syncDirectory = func(*os.File) error { return injected }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := testTime()
			dependencies := testDependencies(now)
			test.mutate(&dependencies)
			publisher, directory := newTestPublisher(t, now, &dependencies)
			_, privateKey := testKey(5)
			reference, err := publisher.Publish(context.Background(), testTicket(now, test.name), privateKey)
			if reference == "" || !errors.Is(err, ErrDurabilityUncertain) || !errors.Is(err, injected) {
				t.Fatalf("Publish() result = %q, %v, want reference and classifiable durability failure", reference, err)
			}
			_ = readPublished(t, directory, reference)
			assertOnlyFinalFiles(t, directory, 1)
		})
	}
}

// TestPublisherEntropyFailuresLeaveNoFiles proves identity generation fails closed before publication.
func TestPublisherEntropyFailuresLeaveNoFiles(t *testing.T) {
	injected := errors.New("entropy unavailable")
	tests := []struct {
		name   string
		mutate func(*publisherDependencies)
	}{
		{name: "reference", mutate: func(dependencies *publisherDependencies) {
			dependencies.referenceRandom = func([]byte) error { return injected }
		}},
		{name: "staging", mutate: func(dependencies *publisherDependencies) {
			dependencies.stagingRandom = func([]byte) error { return injected }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := testTime()
			dependencies := testDependencies(now)
			test.mutate(&dependencies)
			publisher, directory := newTestPublisher(t, now, &dependencies)
			_, privateKey := testKey(6)
			reference, err := publisher.Publish(context.Background(), testTicket(now, test.name), privateKey)
			if reference != "" || !errors.Is(err, injected) {
				t.Fatalf("Publish() result = %q, %v, want empty reference and entropy failure", reference, err)
			}
			assertOnlyFinalFiles(t, directory, 0)
		})
	}
}

// TestPublisherRetriesReferenceCollisionWithoutOverwrite proves an existing reference remains immutable.
func TestPublisherRetriesReferenceCollisionWithoutOverwrite(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	var calls atomic.Uint32
	dependencies.referenceRandom = func(destination []byte) error {
		marker := byte('a')
		if calls.Add(1) > 1 {
			marker = byte('b')
		}
		fillBytes(destination, marker)
		return nil
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	firstReference := helper.TicketReference(strings.Repeat("61", referenceEntropyBytes))
	secondReference := helper.TicketReference(strings.Repeat("62", referenceEntropyBytes))
	original := []byte("immutable existing ticket")
	writePrivateFile(t, filepath.Join(directory, string(firstReference)), original)
	_, privateKey := testKey(7)

	reference, err := publisher.Publish(context.Background(), testTicket(now, "collision"), privateKey)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if reference != secondReference {
		t.Fatalf("Publish() reference = %q, want %q", reference, secondReference)
	}
	content, err := os.ReadFile(filepath.Join(directory, string(firstReference)))
	if err != nil {
		t.Fatalf("read existing reference: %v", err)
	}
	if !bytes.Equal(content, original) {
		t.Fatalf("existing reference changed to %q", content)
	}
	assertOnlyFinalFiles(t, directory, 2)
}

// TestPublisherCollisionExhaustionPreservesExistingFile proves bounded retries never weaken no-replace publication.
func TestPublisherCollisionExhaustionPreservesExistingFile(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	dependencies.referenceRandom = fillRandom('a')
	dependencies.stagingRandom = fillRandom('c')
	publisher, directory := newTestPublisher(t, now, &dependencies)
	reference := helper.TicketReference(strings.Repeat("61", referenceEntropyBytes))
	original := []byte("existing")
	writePrivateFile(t, filepath.Join(directory, string(reference)), original)
	_, privateKey := testKey(8)

	got, err := publisher.Publish(context.Background(), testTicket(now, "exhausted"), privateKey)
	if got != "" || !errors.Is(err, ErrCollisionExhausted) {
		t.Fatalf("Publish() result = %q, %v, want ErrCollisionExhausted", got, err)
	}
	content, readErr := os.ReadFile(filepath.Join(directory, string(reference)))
	if readErr != nil || !bytes.Equal(content, original) {
		t.Fatalf("existing content = %q, %v, want %q", content, readErr, original)
	}
	assertOnlyFinalFiles(t, directory, 1)
}

// TestPublisherStagingCollisionExhaustionLeavesExistingFile proves staging names are exclusive and bounded too.
func TestPublisherStagingCollisionExhaustionLeavesExistingFile(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	dependencies.stagingRandom = fillRandom('d')
	publisher, directory := newTestPublisher(t, now, &dependencies)
	stagingName := stagingPrefix + strings.Repeat("64", stagingEntropyBytes) + stagingSuffix
	original := []byte("foreign staging content")
	writePrivateFile(t, filepath.Join(directory, stagingName), original)
	_, privateKey := testKey(9)

	reference, err := publisher.Publish(context.Background(), testTicket(now, "staging-collision"), privateKey)
	if reference != "" || err == nil || !strings.Contains(err.Error(), "staging name collisions exhausted") {
		t.Fatalf("Publish() result = %q, %v, want staging collision exhaustion", reference, err)
	}
	content, readErr := os.ReadFile(filepath.Join(directory, stagingName))
	if readErr != nil || !bytes.Equal(content, original) {
		t.Fatalf("existing staging content = %q, %v, want %q", content, readErr, original)
	}
}

// TestPublisherRejectsMalformedStagingMetadata proves private bytes cannot be published after metadata drift.
func TestPublisherRejectsMalformedStagingMetadata(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	dependencies.files.reopen = func(root *os.Root, directory *os.File, path string, name string) (*os.File, error) {
		makeStagedFileUnsafe(t, root, name)
		return baseReopen(root, directory, path, name)
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(10)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "metadata"), privateKey)
	if reference != "" || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Publish() result = %q, %v, want ErrUnsafePath", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherRejectsChangedStagingContent proves synced bytes are compared again through a fresh no-follow handle.
func TestPublisherRejectsChangedStagingContent(t *testing.T) {
	now := testTime()
	dependencies := testDependencies(now)
	baseReopen := dependencies.files.reopen
	dependencies.files.reopen = func(root *os.Root, directory *os.File, path string, name string) (*os.File, error) {
		file, err := root.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		_, writeErr := file.WriteAt([]byte{'x'}, 0)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return nil, err
		}
		return baseReopen(root, directory, path, name)
	}
	publisher, directory := newTestPublisher(t, now, &dependencies)
	_, privateKey := testKey(10)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "content"), privateKey)
	if reference != "" || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Publish() result = %q, %v, want ErrUnsafePath", reference, err)
	}
	assertOnlyFinalFiles(t, directory, 0)
}

// TestPublisherRetainedRootSurvivesPathSwap proves later absolute-path replacement cannot redirect ticket authority.
func TestPublisherRetainedRootSurvivesPathSwap(t *testing.T) {
	now := testTime()
	publisher, directory := newTestPublisher(t, now, nil)
	parent := filepath.Dir(directory)
	moved := filepath.Join(parent, "moved-pending")
	if err := os.Rename(directory, moved); err != nil {
		if runtime.GOOS == "windows" {
			_, privateKey := testKey(11)
			reference, publishErr := publisher.Publish(context.Background(), testTicket(now, "path-swap"), privateKey)
			if publishErr != nil {
				t.Fatalf("Publish() after Windows root rename refusal: %v", publishErr)
			}
			_ = readPublished(t, directory, reference)
			assertOnlyFinalFiles(t, directory, 1)
			return
		}
		t.Fatalf("move opened pending directory: %v", err)
	}
	prepareTestDirectory(t, directory)
	_, privateKey := testKey(11)
	reference, err := publisher.Publish(context.Background(), testTicket(now, "path-swap"), privateKey)
	if err != nil {
		t.Fatalf("Publish() after path swap error = %v", err)
	}
	_ = readPublished(t, moved, reference)
	assertOnlyFinalFiles(t, moved, 1)
	assertOnlyFinalFiles(t, directory, 0)
}

// TestConcurrentPublishersProduceOneImmutableOutcome proves colliding writers cannot replace the winner.
func TestConcurrentPublishersProduceOneImmutableOutcome(t *testing.T) {
	now := testTime()
	root := t.TempDir()
	directory := filepath.Join(root, "pending")
	prepareTestDirectory(t, directory)
	dependencies := testDependencies(now)
	dependencies.referenceRandom = fillRandom('e')
	var stagingCounter atomic.Uint64
	dependencies.stagingRandom = func(destination []byte) error {
		for index := range destination {
			destination[index] = 0
		}
		binary.BigEndian.PutUint64(destination[len(destination)-8:], stagingCounter.Add(1))
		return nil
	}
	first, err := open(directory, dependencies)
	if err != nil {
		t.Fatalf("open first publisher: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := open(directory, dependencies)
	if err != nil {
		t.Fatalf("open second publisher: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	_, privateKey := testKey(12)
	tickets := []helper.Ticket{testTicket(now, "concurrent-one"), testTicket(now, "concurrent-two")}
	publishers := []*Publisher{first, second}
	type result struct {
		reference helper.TicketReference
		err       error
	}
	results := make(chan result, 2)
	var waitGroup sync.WaitGroup
	for index := range publishers {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			reference, err := publishers[index].Publish(context.Background(), tickets[index], privateKey)
			results <- result{reference: reference, err: err}
		}(index)
	}
	waitGroup.Wait()
	close(results)

	successes := 0
	failures := 0
	var winningReference helper.TicketReference
	for result := range results {
		if result.err == nil {
			successes++
			winningReference = result.reference
			continue
		}
		if result.reference != "" || !errors.Is(result.err, ErrCollisionExhausted) {
			t.Fatalf("losing result = %q, %v, want collision exhaustion", result.reference, result.err)
		}
		failures++
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent outcomes = %d successes/%d failures, want 1/1", successes, failures)
	}
	content := readPublished(t, directory, winningReference)
	envelope, err := ticketauth.Decode(content)
	if err != nil {
		t.Fatalf("decode winning envelope: %v", err)
	}
	verified, _, err := envelope.VerifyBootstrap(now)
	if err != nil {
		t.Fatalf("verify winning envelope: %v", err)
	}
	if !reflect.DeepEqual(verified, tickets[0]) && !reflect.DeepEqual(verified, tickets[1]) {
		t.Fatalf("winning ticket = %#v, want one complete submitted ticket", verified)
	}
	assertOnlyFinalFiles(t, directory, 1)
}

// TestWriteAllRejectsNoProgress prevents an incomplete envelope from being mistaken for a successful write.
func TestWriteAllRejectsNoProgress(t *testing.T) {
	if err := writeAll(zeroWriter{}, []byte("ticket")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll() error = %v, want io.ErrShortWrite", err)
	}
}

// TestWriteAllPreservesWriterFailure proves storage errors are returned without retrying partial authority blindly.
func TestWriteAllPreservesWriterFailure(t *testing.T) {
	injected := errors.New("injected writer failure")
	if err := writeAll(failingWriter{err: injected}, []byte("ticket")); !errors.Is(err, injected) {
		t.Fatalf("writeAll() error = %v, want injected failure", err)
	}
}

// TestStorageHelpersCoverNativeFailures exercises bounded helper outcomes that publication normally avoids.
func TestStorageHelpersCoverNativeFailures(t *testing.T) {
	if err := readRandom(make([]byte, referenceEntropyBytes)); err != nil {
		t.Fatalf("readRandom() error = %v", err)
	}
	if err := ignoreMissing(fs.ErrNotExist); err != nil {
		t.Fatalf("ignoreMissing() error = %v, want nil", err)
	}
	injected := errors.New("injected non-missing failure")
	if !errors.Is(ignoreMissing(injected), injected) {
		t.Fatal("ignoreMissing() discarded a non-missing failure")
	}

	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("open native helper root: %v", err)
	}
	defer func() { _ = root.Close() }()
	openedDirectory, err := root.Open(".")
	if err != nil {
		t.Fatalf("open native helper directory: %v", err)
	}
	defer func() { _ = openedDirectory.Close() }()
	if _, err := reopenPlatformFile(root, openedDirectory, directory, "missing"); err == nil {
		t.Fatal("reopenPlatformFile() accepted a missing child")
	}
}

// TestValidateStagedFileRejectsEveryObservableMismatch covers identity, size, and canonical-content checks directly.
func TestValidateStagedFileRejectsEveryObservableMismatch(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	firstPath := filepath.Join(directory, "first")
	secondPath := filepath.Join(directory, "second")
	writePrivateFile(t, firstPath, []byte("{}"))
	writePrivateFile(t, secondPath, []byte("{}"))
	first, err := os.Open(firstPath)
	if err != nil {
		t.Fatalf("open first fixture: %v", err)
	}
	firstInfo, err := first.Stat()
	if err != nil {
		_ = first.Close()
		t.Fatalf("stat first fixture: %v", err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		_ = first.Close()
		t.Fatalf("stat second fixture: %v", err)
	}
	if err := validateStagedFile(first, secondInfo, []byte("{}")); err == nil {
		t.Fatal("validateStagedFile() accepted a replaced file identity")
	}
	if err := validateStagedFile(first, firstInfo, []byte("longer")); err == nil {
		t.Fatal("validateStagedFile() accepted a size mismatch")
	}
	if err := validateStagedFile(first, firstInfo, []byte("{}")); err == nil || !strings.Contains(err.Error(), "decode staged") {
		t.Fatalf("validateStagedFile() malformed canonical error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first fixture: %v", err)
	}
	if err := validateStagedFile(first, firstInfo, []byte("{}")); err == nil {
		t.Fatal("validateStagedFile() accepted a closed handle")
	}

	oversizedPath := filepath.Join(directory, "oversized")
	oversized := bytes.Repeat([]byte{'x'}, ticketauth.MaxEnvelopeBytes+1)
	writePrivateFile(t, oversizedPath, oversized)
	oversizedFile, err := os.Open(oversizedPath)
	if err != nil {
		t.Fatalf("open oversized fixture: %v", err)
	}
	oversizedInfo, err := oversizedFile.Stat()
	if err != nil {
		_ = oversizedFile.Close()
		t.Fatalf("stat oversized fixture: %v", err)
	}
	if err := validateStagedFile(oversizedFile, oversizedInfo, oversized); err == nil {
		t.Fatal("validateStagedFile() accepted oversized content")
	}
	if err := oversizedFile.Close(); err != nil {
		t.Fatalf("close oversized fixture: %v", err)
	}

	now := testTime()
	_, privateKey := testKey(13)
	envelope, err := ticketauth.Sign(testTicket(now, "read-failure"), privateKey, now)
	if err != nil {
		t.Fatalf("sign read-failure fixture: %v", err)
	}
	canonical, err := ticketauth.Encode(envelope)
	if err != nil {
		t.Fatalf("encode read-failure fixture: %v", err)
	}
	writeOnlyPath := filepath.Join(directory, "write-only")
	writePrivateFile(t, writeOnlyPath, canonical)
	writeOnly, err := os.OpenFile(writeOnlyPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write-only fixture: %v", err)
	}
	writeOnlyInfo, err := writeOnly.Stat()
	if err != nil {
		_ = writeOnly.Close()
		t.Fatalf("stat write-only fixture: %v", err)
	}
	if err := validateStagedFile(writeOnly, writeOnlyInfo, canonical); err == nil || !strings.Contains(err.Error(), "read staged") {
		t.Fatalf("validateStagedFile() unreadable error = %v", err)
	}
	if err := writeOnly.Close(); err != nil {
		t.Fatalf("close write-only fixture: %v", err)
	}
}

// failingWriter returns one injected storage error.
type failingWriter struct {
	err error
}

// Write prevents writeAll from mistaking a failed transport for progress.
func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// zeroWriter reports no progress without a transport error.
type zeroWriter struct{}

// Write simulates a broken writer that would otherwise spin forever.
func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

// testClock supplies the trusted fixed time used by publisher tests.
type testClock struct {
	now time.Time
}

// Now returns one stable UTC publisher time.
func (clock testClock) Now() time.Time {
	return clock.now
}

// newTestPublisher opens one exact private directory with deterministic entropy unless overridden.
func newTestPublisher(t *testing.T, now time.Time, override *publisherDependencies) (*Publisher, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "pending")
	prepareTestDirectory(t, directory)
	dependencies := testDependencies(now)
	if override != nil {
		dependencies = *override
	}
	publisher, err := open(directory, dependencies)
	if err != nil {
		t.Fatalf("open test publisher: %v", err)
	}
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("close test publisher: %v", err)
		}
	})
	return publisher, directory
}

// testDependencies returns deterministic authority inputs layered over native filesystem operations.
func testDependencies(now time.Time) publisherDependencies {
	dependencies := defaultDependencies()
	dependencies.clock = testClock{now: now}
	dependencies.referenceRandom = fillRandom('r')
	dependencies.stagingRandom = fillRandom('s')
	return dependencies
}

// fillRandom returns one deterministic full-buffer entropy seam.
func fillRandom(marker byte) func([]byte) error {
	return func(destination []byte) error {
		fillBytes(destination, marker)
		return nil
	}
}

// fillBytes sets every generated byte to one test marker.
func fillBytes(destination []byte, marker byte) {
	for index := range destination {
		destination[index] = marker
	}
}

// testKey derives deterministic Ed25519 material without fixture files.
func testKey(marker byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	fillBytes(seed, marker)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey, _ := privateKey.Public().(ed25519.PublicKey)
	return publicKey, privateKey
}

// testPrivateKey returns only the deterministic private half used in table fixtures.
func testPrivateKey(marker byte) ed25519.PrivateKey {
	_, privateKey := testKey(marker)
	return privateKey
}

// testTicket returns one valid ticket whose nonce distinguishes concurrent content.
func testTicket(now time.Time, nonceMarker string) helper.Ticket {
	nonceMarker = strings.ReplaceAll(nonceMarker, " ", "-")
	return helper.Ticket{
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackIdentity,
		InstallationID:         "harbor-ticket-spool-test",
		RequesterIdentity:      "uid-1000",
		OwnershipGeneration:    1,
		OwnershipSchemaVersion: 1,
		ApprovedPool:           "127.77.0.0/24",
		ApprovedAddress:        "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("a", 64),
		},
		ExpectedPreAssignment: &helper.ExpectedPreAssignment{
			Fingerprint:  strings.Repeat("b", 64),
			Requirements: []helper.SocketRequirement{},
		},
		Nonce:     nonceMarker + strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testTime supplies the canonical trusted instant shared by publisher tests.
func testTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}

// readPublished reads one final reference without accepting staging-name substitutions.
func readPublished(t *testing.T, directory string, reference helper.TicketReference) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, string(reference)))
	if err != nil {
		t.Fatalf("read published reference %q: %v", reference, err)
	}
	return content
}

// assertOnlyFinalFiles checks that no staging residue remains after one operation.
func assertOnlyFinalFiles(t *testing.T, directory string, count int) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read pending directory: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("pending entries = %d, want %d: %#v", len(entries), count, entries)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingPrefix) {
			t.Fatalf("staging residue remains: %q", entry.Name())
		}
	}
}
