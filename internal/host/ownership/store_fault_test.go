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

// TestOwnershipErrorsDescribeComparedEvidence keeps operator diagnostics tied to the exact rejected state.
func TestOwnershipErrorsDescribeComparedEvidence(t *testing.T) {
	requested := testRecord()
	existing := Observation{Exists: true, Record: requested, Fingerprint: strings.Repeat("a", sha256DigestHexLength)}
	conflict := &ConflictError{Requested: requested, Existing: existing}
	for _, want := range []string{ErrConflict.Error(), requested.InstallationID, requested.OwnerIdentity, existing.Fingerprint} {
		if got := conflict.Error(); !strings.Contains(got, want) {
			t.Fatalf("ConflictError.Error() = %q, want substring %q", got, want)
		}
	}
	mismatch := &FingerprintMismatchError{Expected: strings.Repeat("b", sha256DigestHexLength), Actual: existing}
	for _, want := range []string{ErrStaleFingerprint.Error(), mismatch.Expected, existing.Fingerprint} {
		if got := mismatch.Error(); !strings.Contains(got, want) {
			t.Fatalf("FingerprintMismatchError.Error() = %q, want substring %q", got, want)
		}
	}
}

// TestCloseOnceConsumesFailingClose prevents deferred cleanup from closing a possibly recycled descriptor.
func TestCloseOnceConsumesFailingClose(t *testing.T) {
	cause := errors.New("close failed")
	closed := false
	calls := 0
	closeFile := func() error {
		calls++
		return cause
	}
	if err := closeOnce(&closed, closeFile); !errors.Is(err, cause) {
		t.Fatalf("closeOnce() error = %v, want cause", err)
	}
	if err := closeOnce(&closed, closeFile); err != nil {
		t.Fatalf("closeOnce() repeated error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

// TestStoreClaimCleansTemporaryFilesAfterPrePublishFailures covers each fallible temporary-file stage.
func TestStoreClaimCleansTemporaryFilesAfterPrePublishFailures(t *testing.T) {
	cause := errors.New("injected claim failure")
	tests := []struct {
		name   string
		mutate func(*Store)
		want   string
	}{
		{
			name: "random name",
			mutate: func(store *Store) {
				store.operations.randomRead = func([]byte) (int, error) { return 0, cause }
			},
			want: "temporary name",
		},
		{
			name: "exclusive create",
			mutate: func(store *Store) {
				store.operations.createFile = func(*os.Root, string, string) (*os.File, error) { return nil, cause }
			},
			want: "create temporary",
		},
		{
			name: "name exhaustion",
			mutate: func(store *Store) {
				store.operations.createFile = func(*os.Root, string, string) (*os.File, error) { return nil, fs.ErrExist }
			},
			want: "exhausted unique names",
		},
		{
			name: "write",
			mutate: func(store *Store) {
				store.operations.writeTemporary = func(io.Writer, []byte) error { return cause }
			},
			want: "write temporary",
		},
		{
			name: "sync",
			mutate: func(store *Store) {
				store.operations.syncTemporary = func(*os.File) error { return cause }
			},
			want: "sync temporary",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			if _, err := store.Observe(context.Background()); err != nil {
				t.Fatalf("Store.Observe() setup error = %v", err)
			}
			test.mutate(store)
			_, err := store.Claim(context.Background(), testRecord())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Claim() error = %v, want substring %q", err, test.want)
			}
			assertOnlyOwnershipLock(t, path)
		})
	}
}

// TestStoreClaimReportsTemporaryCleanupFailure retains both the primary cause and failed cleanup evidence.
func TestStoreClaimReportsTemporaryCleanupFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Observe(context.Background()); err != nil {
		t.Fatalf("Store.Observe() setup error = %v", err)
	}
	writeCause := errors.New("injected write failure")
	removeCause := errors.New("injected remove failure")
	remove := store.operations.removeEntry
	store.operations.writeTemporary = func(io.Writer, []byte) error { return writeCause }
	store.operations.removeEntry = func(*os.Root, string) error { return removeCause }
	_, err := store.Claim(context.Background(), testRecord())
	store.operations.removeEntry = remove
	if !errors.Is(err, writeCause) || !errors.Is(err, removeCause) {
		t.Fatalf("Store.Claim() error = %v, want write and cleanup causes", err)
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatalf("os.ReadDir() error = %v", readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			if removeErr := os.Remove(filepath.Join(filepath.Dir(path), entry.Name())); removeErr != nil {
				t.Fatalf("os.Remove(stale temporary) error = %v", removeErr)
			}
		}
	}
}

// TestStoreClaimCloseFailureClosesTemporaryOnce pins the error path that previously attempted a second close.
func TestStoreClaimCloseFailureClosesTemporaryOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Observe(context.Background()); err != nil {
		t.Fatalf("Store.Observe() setup error = %v", err)
	}
	cause := errors.New("injected close failure")
	calls := 0
	store.operations.closeTemporary = func(file *os.File) error {
		calls++
		return errors.Join(file.Close(), cause)
	}
	_, err := store.Claim(context.Background(), testRecord())
	if !errors.Is(err, cause) {
		t.Fatalf("Store.Claim() error = %v, want close cause", err)
	}
	if calls != 1 {
		t.Fatalf("temporary close calls = %d, want 1", calls)
	}
	assertOnlyOwnershipLock(t, path)
}

// TestStoreClaimPublishFailuresDistinguishAppliedTransition proves only a committed name change requires reconciliation.
func TestStoreClaimPublishFailuresDistinguishAppliedTransition(t *testing.T) {
	cause := errors.New("injected publish failure")
	t.Run("not applied", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Observe(context.Background()); err != nil {
			t.Fatalf("Store.Observe() setup error = %v", err)
		}
		store.operations.renameNoReplace = func(*os.Root, string, string, string) (bool, error) {
			return false, cause
		}
		_, err := store.Claim(context.Background(), testRecord())
		if !errors.Is(err, cause) || errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("Store.Claim() error = %v, want ordinary publish failure", err)
		}
		assertOnlyOwnershipLock(t, path)
	})

	t.Run("applied barrier failure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Observe(context.Background()); err != nil {
			t.Fatalf("Store.Observe() setup error = %v", err)
		}
		rename := store.operations.renameNoReplace
		store.operations.renameNoReplace = func(root *os.Root, directory string, source string, destination string) (bool, error) {
			applied, err := rename(root, directory, source, destination)
			return applied, errors.Join(err, cause)
		}
		_, err := store.Claim(context.Background(), testRecord())
		if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("Store.Claim() error = %v, want applied-transition uncertainty", err)
		}
		observed, observeErr := store.Observe(context.Background())
		if observeErr != nil || !observed.Exists {
			t.Fatalf("Store.Observe() = %#v, %v, want committed claim", observed, observeErr)
		}
	})

	t.Run("post-publish validation failure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Observe(context.Background()); err != nil {
			t.Fatalf("Store.Observe() setup error = %v", err)
		}
		rename := store.operations.renameNoReplace
		store.operations.renameNoReplace = func(root *os.Root, directory string, source string, destination string) (bool, error) {
			applied, err := rename(root, directory, source, destination)
			if err == nil {
				err = root.Remove(destination)
			}
			return applied, err
		}
		_, err := store.Claim(context.Background(), testRecord())
		if !errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("Store.Claim() error = %v, want post-publish uncertainty", err)
		}
	})
}

// TestStorePostCommitLockCleanupFailuresRequireReconciliation covers errors after the transaction boundary commits.
func TestStorePostCommitLockCleanupFailuresRequireReconciliation(t *testing.T) {
	cause := errors.New("injected lock cleanup failure")
	for _, phase := range []string{"release", "close"} {
		phase := phase
		t.Run("claim "+phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			if _, err := store.Observe(context.Background()); err != nil {
				t.Fatalf("Store.Observe() setup error = %v", err)
			}
			restore := injectLockCleanupFailure(store, phase, cause)
			_, err := store.Claim(context.Background(), testRecord())
			restore()
			if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("Store.Claim() error = %v, want post-commit uncertainty", err)
			}
			observed, observeErr := store.Observe(context.Background())
			if observeErr != nil || !observed.Exists {
				t.Fatalf("Store.Observe() = %#v, %v, want committed claim", observed, observeErr)
			}
		})

		t.Run("release "+phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			claimed, err := store.Claim(context.Background(), testRecord())
			if err != nil {
				t.Fatalf("Store.Claim() setup error = %v", err)
			}
			restore := injectLockCleanupFailure(store, phase, cause)
			err = store.Release(context.Background(), claimed.Fingerprint)
			restore()
			if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("Store.Release() error = %v, want post-commit uncertainty", err)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("os.Lstat(active) error = %v, want committed release", statErr)
			}
		})
	}
}

// TestStorePrecommitLockFailuresRemainOrdinary prevents reconciliation signaling before protected state changes.
func TestStorePrecommitLockFailuresRemainOrdinary(t *testing.T) {
	cause := errors.New("injected precommit lock failure")
	t.Run("acquire", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "owner.json")
		store := openTestStore(t, path)
		if _, err := store.Observe(context.Background()); err != nil {
			t.Fatalf("Store.Observe() setup error = %v", err)
		}
		store.operations.acquireLock = func(context.Context, *os.File) error { return cause }
		_, err := store.Claim(context.Background(), testRecord())
		if !errors.Is(err, cause) || errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("Store.Claim() error = %v, want ordinary acquisition failure", err)
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("os.Lstat(active) error = %v, want no claim", statErr)
		}
	})

	for _, phase := range []string{"release", "close"} {
		phase := phase
		t.Run("conflict "+phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			original, err := store.Claim(context.Background(), testRecord())
			if err != nil {
				t.Fatalf("Store.Claim() setup error = %v", err)
			}
			restore := injectLockCleanupFailure(store, phase, cause)
			conflicting := testRecord()
			conflicting.InstallationID = "other-installation"
			_, err = store.Claim(context.Background(), conflicting)
			restore()
			if !errors.Is(err, cause) || !errors.Is(err, ErrConflict) || errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("Store.Claim() error = %v, want ordinary conflict plus cleanup failure", err)
			}
			observed, observeErr := store.Observe(context.Background())
			if observeErr != nil || observed != original {
				t.Fatalf("Store.Observe() = %#v, %v, want original %#v", observed, observeErr, original)
			}
		})
	}
}

// TestStoreCancellationAfterPlatformLockStopsBeforeProtectedState proves the final deadline check precedes callbacks.
func TestStoreCancellationAfterPlatformLockStopsBeforeProtectedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Observe(context.Background()); err != nil {
		t.Fatalf("Store.Observe() setup error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	acquire := store.operations.acquireLock
	store.operations.acquireLock = func(ctx context.Context, file *os.File) error {
		if err := acquire(ctx, file); err != nil {
			return err
		}
		cancel()
		return nil
	}
	called := false
	err := store.withLock(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("Store.withLock() = %v, called = %t, want cancellation before callback", err, called)
	}
}

// TestStoreLockCreationFailuresRemainOutsideTransactions covers collision, creation, and created-handle validation errors.
func TestStoreLockCreationFailuresRemainOutsideTransactions(t *testing.T) {
	cause := errors.New("injected lock creation failure")
	tests := []struct {
		name   string
		create func(*os.Root, string, string) (*os.File, error)
		want   string
	}{
		{
			name: "collision exhaustion",
			create: func(*os.Root, string, string) (*os.File, error) {
				return nil, fs.ErrExist
			},
			want: "did not settle",
		},
		{
			name: "creation failure",
			create: func(*os.Root, string, string) (*os.File, error) {
				return nil, cause
			},
			want: "create machine ownership lock",
		},
		{
			name: "unprotected created handle",
			create: func(root *os.Root, _ string, name string) (*os.File, error) {
				file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, privateFileMode)
				if err != nil {
					return nil, err
				}
				if err := file.Chmod(0o640); err != nil {
					return nil, errors.Join(err, file.Close(), root.Remove(name))
				}
				return file, nil
			},
			want: "not protected",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			store.operations.createFile = test.create
			_, err := store.Observe(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Observe() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestStoreOperationsFailAfterRetainedRootCloses covers native path-inspection failures without path races.
func TestStoreOperationsFailAfterRetainedRootCloses(t *testing.T) {
	directory := t.TempDir()
	prepareTestStoreDirectory(t, directory)
	store, err := NewStore(filepath.Join(directory, "owner.json"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.root.Close(); err != nil {
		t.Fatalf("Root.Close() error = %v", err)
	}
	store.closed = true
	if _, err := store.openLockFile(); err == nil || !strings.Contains(err.Error(), "inspect machine ownership lock") {
		t.Fatalf("Store.openLockFile() error = %v, want retained-root failure", err)
	}
	if err := validateExistingEntry(store.root, "owner.json"); err == nil || !strings.Contains(err.Error(), "inspect machine ownership path") {
		t.Fatalf("validateExistingEntry() error = %v, want retained-root failure", err)
	}
}

// TestStoreReleaseRejectsCorruptObservedState prevents malformed bytes from reaching destructive comparison.
func TestStoreReleaseRejectsCorruptObservedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	prepareTestStoreDirectory(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("{"), privateFileMode); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	store := openTestStore(t, path)
	err := store.Release(context.Background(), strings.Repeat("a", sha256DigestHexLength))
	if !errors.Is(err, ErrCorruptRecord) || errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Release() error = %v, want ordinary corrupt-record rejection", err)
	}
}

// TestStoreClaimRejectsInvalidRequestedStateBeforeLocking keeps validation failures free of disk effects.
func TestStoreClaimRejectsInvalidRequestedStateBeforeLocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	record := testRecord()
	record.Generation = 0
	if _, err := store.Claim(context.Background(), record); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("Store.Claim() error = %v, want invalid-generation failure", err)
	}
	for _, candidate := range []string{path, path + ".lock"} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("os.Lstat(%q) error = %v, want no disk effect", candidate, err)
		}
	}
}

// TestStoreClaimReportsCorruptConcurrentWinner preserves both collision and untrusted observed-state evidence.
func TestStoreClaimReportsCorruptConcurrentWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Observe(context.Background()); err != nil {
		t.Fatalf("Store.Observe() setup error = %v", err)
	}
	store.operations.renameNoReplace = func(root *os.Root, directory string, _ string, destination string) (bool, error) {
		file, err := createPlatformFile(root, directory, destination)
		if err != nil {
			return false, err
		}
		return false, errors.Join(fs.ErrExist, writeAll(file, []byte("{")), file.Sync(), file.Close())
	}
	_, err := store.Claim(context.Background(), testRecord())
	if !errors.Is(err, fs.ErrExist) || !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("Store.Claim() error = %v, want collision and corrupt record", err)
	}
}

// ownershipWriterFunc exposes narrow write outcomes without constructing an unrelated filesystem fixture.
type ownershipWriterFunc func([]byte) (int, error)

// Write delegates one test write to the configured outcome.
func (writer ownershipWriterFunc) Write(content []byte) (int, error) {
	return writer(content)
}

// TestWriteAllRejectsWriterErrorsAndNoProgress covers corruption-sensitive short-write behavior.
func TestWriteAllRejectsWriterErrorsAndNoProgress(t *testing.T) {
	cause := errors.New("injected writer failure")
	if err := writeAll(ownershipWriterFunc(func([]byte) (int, error) { return 0, cause }), []byte("record")); !errors.Is(err, cause) {
		t.Fatalf("writeAll(error) = %v, want cause", err)
	}
	if err := writeAll(ownershipWriterFunc(func([]byte) (int, error) { return 0, nil }), []byte("record")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll(no progress) = %v, want io.ErrShortWrite", err)
	}
}

// TestDecodeRecordRejectsTypedFieldMismatch ensures shape validation cannot let incompatible JSON types normalize.
func TestDecodeRecordRejectsTypedFieldMismatch(t *testing.T) {
	content := []byte(`{"schema_version":"one","installation_id":"harbor-installation","owner_identity":"501","generation":7,"loopback_pool_prefix":"127.44.0.0/24","ticket_verifier_key":"AQID"}`)
	if _, err := decodeRecord(content); err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("decodeRecord() error = %v, want typed decode failure", err)
	}
}

// TestStoreClaimHandlesNoReplaceRace covers identical and conflicting state that appears during publication.
func TestStoreClaimHandlesNoReplaceRace(t *testing.T) {
	for _, identical := range []bool{true, false} {
		name := "conflicting"
		if identical {
			name = "identical"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			if _, err := store.Observe(context.Background()); err != nil {
				t.Fatalf("Store.Observe() setup error = %v", err)
			}
			requested := testRecord()
			stored := requested
			if !identical {
				stored.InstallationID = "other-installation"
			}
			store.operations.renameNoReplace = func(root *os.Root, directory string, source string, destination string) (bool, error) {
				content, err := json.Marshal(stored)
				if err != nil {
					return false, err
				}
				file, err := createPlatformFile(root, directory, destination)
				if err != nil {
					return false, err
				}
				err = errors.Join(writeAll(file, content), file.Sync(), file.Close())
				return false, errors.Join(fs.ErrExist, err)
			}
			observed, err := store.Claim(context.Background(), requested)
			if identical {
				if err != nil || observed.Record != requested {
					t.Fatalf("Store.Claim() identical race = %#v, %v", observed, err)
				}
				return
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Store.Claim() conflicting race error = %v, want ErrConflict", err)
			}
		})
	}
}

// TestStoreConcurrentIdenticalClaimRequiresConfirmation covers a race winner whose durable entry cannot be confirmed.
func TestStoreConcurrentIdenticalClaimRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Observe(context.Background()); err != nil {
		t.Fatalf("Store.Observe() setup error = %v", err)
	}
	record := testRecord()
	store.operations.renameNoReplace = func(root *os.Root, directory string, _ string, destination string) (bool, error) {
		content, err := json.Marshal(record)
		if err != nil {
			return false, err
		}
		file, err := createPlatformFile(root, directory, destination)
		if err != nil {
			return false, err
		}
		return false, errors.Join(fs.ErrExist, writeAll(file, content), file.Sync(), file.Close())
	}
	cause := errors.New("injected concurrent confirmation failure")
	store.operations.confirmEntry = func(*os.Root, string, string) error { return cause }
	_, err := store.Claim(context.Background(), record)
	if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Claim() error = %v, want concurrent confirmation uncertainty", err)
	}
}

// TestStoreClaimReplayRequiresDurabilityConfirmation prevents existing bytes alone from granting authority.
func TestStoreClaimReplayRequiresDurabilityConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	if _, err := store.Claim(context.Background(), testRecord()); err != nil {
		t.Fatalf("Store.Claim() setup error = %v", err)
	}
	cause := errors.New("injected confirmation failure")
	store.operations.confirmEntry = func(*os.Root, string, string) error { return cause }
	_, err := store.Claim(context.Background(), testRecord())
	if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Claim() replay error = %v, want durability uncertainty", err)
	}
}

// TestStoreReleaseFailuresDistinguishAppliedTransition covers random, collision, rename, barrier, and cleanup outcomes.
func TestStoreReleaseFailuresDistinguishAppliedTransition(t *testing.T) {
	cause := errors.New("injected release failure")
	tests := []struct {
		name      string
		mutate    func(*Store)
		uncertain bool
		missing   bool
		want      string
	}{
		{
			name: "random name",
			mutate: func(store *Store) {
				store.operations.randomRead = func([]byte) (int, error) { return 0, cause }
			},
			want: "create retired",
		},
		{
			name: "collision exhaustion",
			mutate: func(store *Store) {
				store.operations.renameNoReplace = func(*os.Root, string, string, string) (bool, error) { return false, fs.ErrExist }
			},
			want: "exhausted unique names",
		},
		{
			name: "rename not applied",
			mutate: func(store *Store) {
				store.operations.renameNoReplace = func(*os.Root, string, string, string) (bool, error) { return false, cause }
			},
			want: "retire machine ownership",
		},
		{
			name: "applied barrier failure",
			mutate: func(store *Store) {
				rename := store.operations.renameNoReplace
				store.operations.renameNoReplace = func(root *os.Root, directory string, source string, destination string) (bool, error) {
					applied, err := rename(root, directory, source, destination)
					return applied, errors.Join(err, cause)
				}
			},
			uncertain: true,
			missing:   true,
			want:      "retire machine ownership",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			claimed, err := store.Claim(context.Background(), testRecord())
			if err != nil {
				t.Fatalf("Store.Claim() setup error = %v", err)
			}
			test.mutate(store)
			err = store.Release(context.Background(), claimed.Fingerprint)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Store.Release() error = %v, want substring %q", err, test.want)
			}
			if errors.Is(err, ErrDurabilityUncertain) != test.uncertain {
				t.Fatalf("Store.Release() uncertainty = %t, want %t: %v", errors.Is(err, ErrDurabilityUncertain), test.uncertain, err)
			}
			_, statErr := os.Lstat(path)
			if errors.Is(statErr, os.ErrNotExist) != test.missing {
				t.Fatalf("os.Lstat(active) error = %v, missing = %t", statErr, test.missing)
			}
		})
	}
}

// TestStoreReleaseCleanupIsBestEffort proves a committed release is never converted into a destructive retry.
func TestStoreReleaseCleanupIsBestEffort(t *testing.T) {
	for _, removeErr := range []error{errors.New("cleanup failed"), fs.ErrNotExist} {
		name := removeErr.Error()
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			claimed, err := store.Claim(context.Background(), testRecord())
			if err != nil {
				t.Fatalf("Store.Claim() setup error = %v", err)
			}
			confirmations := 0
			store.operations.removeEntry = func(*os.Root, string) error { return removeErr }
			store.operations.confirmCleanup = func(*os.Root) error {
				confirmations++
				return errors.New("ignored confirmation failure")
			}
			if err := store.Release(context.Background(), claimed.Fingerprint); err != nil {
				t.Fatalf("Store.Release() error = %v", err)
			}
			wantConfirmations := 0
			if errors.Is(removeErr, fs.ErrNotExist) {
				wantConfirmations = 1
			}
			if confirmations != wantConfirmations {
				t.Fatalf("cleanup confirmations = %d, want %d", confirmations, wantConfirmations)
			}
		})
	}
}

// assertOnlyOwnershipLock ensures failed publication cannot leave authority or temporary files behind.
func assertOnlyOwnershipLock(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path)+".lock" {
		t.Fatalf("store entries = %#v, want only ownership lock", entries)
	}
}

// injectLockCleanupFailure performs the real cleanup before adding a deterministic error classification probe.
func injectLockCleanupFailure(store *Store, phase string, cause error) func() {
	if phase == "release" {
		original := store.operations.releaseLock
		store.operations.releaseLock = func(file *os.File) error {
			return errors.Join(original(file), cause)
		}
		return func() { store.operations.releaseLock = original }
	}
	original := store.operations.closeLock
	store.operations.closeLock = func(file *os.File) error {
		return errors.Join(original(file), cause)
	}
	return func() { store.operations.closeLock = original }
}
