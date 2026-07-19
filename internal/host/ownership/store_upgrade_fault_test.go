package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreUpgradePrecommitFailuresPreserveTheSchema1Claim covers every staged-write boundary and the replace call.
func TestStoreUpgradePrecommitFailuresPreserveTheSchema1Claim(t *testing.T) {
	cause := errors.New("injected upgrade failure")
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
		{
			name: "close",
			mutate: func(store *Store) {
				store.operations.closeTemporary = func(file *os.File) error {
					return errors.Join(file.Close(), cause)
				}
			},
			want: "close temporary",
		},
		{
			name: "replace",
			mutate: func(store *Store) {
				store.operations.renameReplace = func(*os.Root, string, string, string) (bool, error) {
					return false, cause
				}
			},
			want: "replace machine ownership record",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			source, target, claimed := claimUpgradeSource(t, store)
			test.mutate(store)

			_, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
			if err == nil || !strings.Contains(err.Error(), test.want) || !errors.Is(err, cause) {
				t.Fatalf("Store.Upgrade() error = %v, want cause and substring %q", err, test.want)
			}
			if errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("Store.Upgrade() error = %v, want ordinary precommit failure", err)
			}
			observed, observeErr := store.Observe(context.Background())
			if observeErr != nil || observed != (Observation{Exists: true, Record: source, Fingerprint: claimed.Fingerprint}) {
				t.Fatalf("Store.Observe() = %#v, %v, want preserved source %#v", observed, observeErr, source)
			}
			assertOwnershipEntries(t, path, true)
		})
	}
}

// TestStoreUpgradeAppliedBarrierFailureConvergesOnReplay proves the new state is authoritative only after confirmation.
func TestStoreUpgradeAppliedBarrierFailureConvergesOnReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	_, target, claimed := claimUpgradeSource(t, store)
	cause := errors.New("injected replacement barrier failure")
	rename := store.operations.renameReplace
	store.operations.renameReplace = func(root *os.Root, directory string, source string, destination string) (bool, error) {
		applied, err := rename(root, directory, source, destination)
		return applied, errors.Join(err, cause)
	}

	_, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	store.operations.renameReplace = rename
	if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Upgrade() error = %v, want applied-transition uncertainty", err)
	}
	committed, observeErr := store.Observe(context.Background())
	if observeErr != nil || committed.Record != target {
		t.Fatalf("Store.Observe() = %#v, %v, want committed target %#v", committed, observeErr, target)
	}
	replayed, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil || replayed != committed {
		t.Fatalf("Store.Upgrade() retry = %#v, %v, want %#v", replayed, err, committed)
	}
	assertOwnershipEntries(t, path, true)
}

// TestStoreUpgradePostcommitVerificationFailureConvergesFromOldState covers rollback-shaped uncertain storage.
func TestStoreUpgradePostcommitVerificationFailureConvergesFromOldState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	source, target, claimed := claimUpgradeSource(t, store)
	canonicalSource, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal() source error = %v", err)
	}
	rename := store.operations.renameReplace
	store.operations.renameReplace = func(root *os.Root, directory string, staged string, destination string) (bool, error) {
		applied, err := rename(root, directory, staged, destination)
		if err != nil {
			return applied, err
		}
		active, err := root.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return applied, err
		}
		writeErr := writeAll(active, canonicalSource)
		syncErr := active.Sync()
		closeErr := active.Close()
		return applied, errors.Join(writeErr, syncErr, closeErr)
	}

	_, err = store.Upgrade(context.Background(), claimed.Fingerprint, target)
	store.operations.renameReplace = rename
	if !errors.Is(err, ErrDurabilityUncertain) || !strings.Contains(err.Error(), "verify upgraded") {
		t.Fatalf("Store.Upgrade() error = %v, want postcommit verification uncertainty", err)
	}
	rolledBack, observeErr := store.Observe(context.Background())
	if observeErr != nil || rolledBack.Record != source || rolledBack.Fingerprint != claimed.Fingerprint {
		t.Fatalf("Store.Observe() = %#v, %v, want rollback-shaped source %#v", rolledBack, observeErr, source)
	}
	upgraded, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil || upgraded.Record != target {
		t.Fatalf("Store.Upgrade() retry = %#v, %v, want target %#v", upgraded, err, target)
	}
	assertOwnershipEntries(t, path, true)
}

// TestStoreUpgradePostcommitLockFailuresRequireReconciliation classifies errors after the atomic name boundary.
func TestStoreUpgradePostcommitLockFailuresRequireReconciliation(t *testing.T) {
	cause := errors.New("injected upgrade lock cleanup failure")
	for _, phase := range []string{"release", "close"} {
		phase := phase
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.json")
			store := openTestStore(t, path)
			_, target, claimed := claimUpgradeSource(t, store)
			restore := injectLockCleanupFailure(store, phase, cause)

			_, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
			restore()
			if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("Store.Upgrade() error = %v, want postcommit lock uncertainty", err)
			}
			observed, observeErr := store.Observe(context.Background())
			if observeErr != nil || observed.Record != target {
				t.Fatalf("Store.Observe() = %#v, %v, want target %#v", observed, observeErr, target)
			}
			assertOwnershipEntries(t, path, true)
		})
	}
}

// TestStoreUpgradeReplayRequiresDurabilityConfirmation prevents target bytes alone from granting authority.
func TestStoreUpgradeReplayRequiresDurabilityConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.json")
	store := openTestStore(t, path)
	_, target, claimed := claimUpgradeSource(t, store)
	upgraded, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil {
		t.Fatalf("Store.Upgrade() setup error = %v", err)
	}
	cause := errors.New("injected replay confirmation failure")
	confirm := store.operations.confirmEntry
	store.operations.confirmEntry = func(*os.Root, string, string) error { return cause }

	_, err = store.Upgrade(context.Background(), claimed.Fingerprint, target)
	store.operations.confirmEntry = confirm
	if !errors.Is(err, cause) || !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("Store.Upgrade() replay error = %v, want confirmation uncertainty", err)
	}
	replayed, err := store.Upgrade(context.Background(), claimed.Fingerprint, target)
	if err != nil || replayed != upgraded {
		t.Fatalf("Store.Upgrade() confirmed replay = %#v, %v, want %#v", replayed, err, upgraded)
	}
	assertOwnershipEntries(t, path, true)
}

// claimUpgradeSource creates the exact schema-1 record derived from the shared schema-2 target.
func claimUpgradeSource(t *testing.T, store *Store) (Record, Record, Observation) {
	t.Helper()
	target := testNetworkPolicyRecord()
	source := target
	source.SchemaVersion = IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	claimed, err := store.Claim(context.Background(), source)
	if err != nil {
		t.Fatalf("Store.Claim() source error = %v", err)
	}
	return source, target, claimed
}
