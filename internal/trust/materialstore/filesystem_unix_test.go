//go:build darwin || linux

package materialstore

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestUnixStoreCreatesOwnerOnlyTree verifies private modes exist from the root through immutable key files.
func TestUnixStoreCreatesOwnerOnlyTree(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}

	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(privateFileMode)
		if entry.IsDir() {
			want = privateDirectoryMode
		}
		if info.Mode().Perm() != want {
			return errors.New(path + " has mode " + info.Mode().Perm().String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("private tree validation: %v", err)
	}
}

// TestUnixStoreRejectsExistingPermissiveRoot verifies startup does not silently repair exposed key storage.
func TestUnixStoreRejectsExistingPermissiveRoot(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Open(permissive root) error = %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("root mode = %04o, want original 0755", info.Mode().Perm())
	}
}

// TestUnixStoreRejectsSymlinkRoot verifies an owner path cannot redirect private material outside its selected directory.
func TestUnixStoreRejectsSymlinkRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(target) error = %v", err)
	}
	link := filepath.Join(parent, "certificates")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := Open(link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Open(symlink root) error = %v", err)
	}
}

// TestUnixStoreRejectsSymlinkedManifest verifies final-component links cannot select foreign active state.
func TestUnixStoreRejectsSymlinkedManifest(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	external := filepath.Join(t.TempDir(), "foreign.json")
	if err := os.WriteFile(external, []byte("{}"), privateFileMode); err != nil {
		t.Fatalf("WriteFile(external) error = %v", err)
	}
	if err := store.filesystem.ensureDirectory(filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)); err != nil {
		t.Fatalf("ensure authority current directory: %v", err)
	}
	manifestPath := authorityManifestDiskPath(directory)
	if err := os.Symlink(external, manifestPath); err != nil {
		t.Fatalf("Symlink(manifest) error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); err == nil || !strings.Contains(err.Error(), "direct object") {
		t.Fatalf("LoadAuthority(symlink manifest) error = %v", err)
	}
}

// TestUnixStoreRejectsHardLinkedPrivateKey verifies immutable key bytes cannot retain an external mutation alias.
func TestUnixStoreRejectsHardLinkedPrivateKey(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	authority := mustLocalAuthority(t)
	if err := store.CreateAuthority(context.Background(), authority); err != nil {
		t.Fatalf("CreateAuthority() error = %v", err)
	}
	key := filepath.Join(directory, filepath.FromSlash(authorityGenerations), authority.Material().Fingerprint, privateKeyFilename)
	alias := filepath.Join(directory, "key-alias")
	if err := os.Link(key, alias); err != nil {
		t.Fatalf("Link() error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("LoadAuthority(hard-linked key) error = %v", err)
	}
}

// TestUnixStoreRejectsNonRegularManifest verifies FIFOs and devices never enter bounded read paths.
func TestUnixStoreRejectsNonRegularManifest(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	defer store.Close()
	if err := store.filesystem.ensureDirectory(filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)); err != nil {
		t.Fatalf("ensure authority current directory: %v", err)
	}
	manifestPath := authorityManifestDiskPath(directory)
	if err := os.Mkdir(manifestPath, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(manifest) error = %v", err)
	}
	if _, err := store.LoadAuthority(context.Background(), storeAuthorityConfig()); err == nil || !strings.Contains(err.Error(), "direct regular file") {
		t.Fatalf("LoadAuthority(directory manifest) error = %v", err)
	}
}

// TestUnixStoreRejectsPermissiveDescendant verifies existing child state is inspected rather than chmod-repaired.
func TestUnixStoreRejectsPermissiveDescendant(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "certificates")
	store := mustStore(t, directory)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	authorityPath := filepath.Join(directory, filepath.FromSlash(authorityDirectory))
	if err := os.Chmod(authorityPath, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("Open(permissive child) error = %v", err)
	}
	info, err := os.Stat(authorityPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("child mode = %04o, want original 0755", info.Mode().Perm())
	}
}

// TestUnixStoreWritesThroughRetainedRootAfterPathSwaps proves mutable root and ancestor names never redirect post-open writes.
func TestUnixStoreWritesThroughRetainedRootAfterPathSwaps(t *testing.T) {
	tests := []struct {
		name string
		swap func(*testing.T, string, string) string
	}{
		{
			name: "root",
			swap: func(t *testing.T, workspace, directory string) string {
				t.Helper()
				original := filepath.Join(workspace, "original-certificates")
				if err := os.Rename(directory, original); err != nil {
					t.Fatalf("Rename(root) error = %v", err)
				}
				if err := os.Mkdir(directory, privateDirectoryMode); err != nil {
					t.Fatalf("Mkdir(replacement root) error = %v", err)
				}
				return original
			},
		},
		{
			name: "ancestor",
			swap: func(t *testing.T, workspace, directory string) string {
				t.Helper()
				ancestor := filepath.Dir(directory)
				originalAncestor := filepath.Join(workspace, "original-harbor")
				if err := os.Rename(ancestor, originalAncestor); err != nil {
					t.Fatalf("Rename(ancestor) error = %v", err)
				}
				if err := os.Mkdir(ancestor, privateDirectoryMode); err != nil {
					t.Fatalf("Mkdir(replacement ancestor) error = %v", err)
				}
				if err := os.Mkdir(directory, privateDirectoryMode); err != nil {
					t.Fatalf("Mkdir(replacement root) error = %v", err)
				}
				return filepath.Join(originalAncestor, filepath.Base(directory))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			ancestor := filepath.Join(workspace, "harbor")
			if err := os.Mkdir(ancestor, privateDirectoryMode); err != nil {
				t.Fatalf("Mkdir(ancestor) error = %v", err)
			}
			directory := filepath.Join(ancestor, "certificates")
			store := mustStore(t, directory)
			defer store.Close()
			original := test.swap(t, workspace, directory)

			authority := mustLocalAuthority(t)
			if err := store.CreateAuthority(context.Background(), authority); err != nil {
				t.Fatalf("CreateAuthority() error = %v", err)
			}
			if _, err := os.Stat(authorityManifestDiskPath(original)); err != nil {
				t.Fatalf("original-tree manifest error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, storeVersionDirectory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement tree was touched: %v", err)
			}
		})
	}
}

// TestUnixStoreSyncsEachNewDirectoryBeforeItsParent instruments the durability order for fixed and dynamic hierarchy creation.
func TestUnixStoreSyncsEachNewDirectoryBeforeItsParent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "certificates")
	var synced []string
	store, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		syncDirectory: func(path string, file *os.File) error {
			synced = append(synced, path)
			return platformSyncDirectory(file)
		},
	})
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	defer store.Close()
	wantLayout := []string{
		filepath.FromSlash(storeVersionDirectory), ".",
		filepath.FromSlash(authorityDirectory), filepath.FromSlash(storeVersionDirectory),
		filepath.FromSlash(authorityGenerations), filepath.FromSlash(authorityDirectory),
		filepath.FromSlash(leavesDirectory), filepath.FromSlash(storeVersionDirectory),
	}
	if !slices.Equal(synced, wantLayout) {
		t.Fatalf("layout sync order = %#v, want %#v", synced, wantLayout)
	}

	authority := mustLocalAuthority(t)
	leaf := mustLeaf(t, authority, []string{"orders.test"})
	if err := store.PutLeaf(context.Background(), authority, leaf); err != nil {
		t.Fatalf("PutLeaf() error = %v", err)
	}
	leafPath := leafDirectory(authority.Material().Fingerprint, leaf.Hosts)
	assertSyncPair(t, synced, filepath.Join(filepath.FromSlash(leavesDirectory), authority.Material().Fingerprint), filepath.FromSlash(leavesDirectory))
	assertSyncPair(t, synced, leafPath, filepath.Dir(leafPath))
	assertSyncPair(t, synced, filepath.Join(leafPath, "generations"), leafPath)
}

// TestUnixStoreSurfacesParentDirectorySyncFailure proves a newly linked hierarchy is never reported ready without parent durability.
func TestUnixStoreSurfacesParentDirectorySyncFailure(t *testing.T) {
	sentinel := errors.New("parent sync failed")
	directory := filepath.Join(t.TempDir(), "certificates")
	_, err := openStore(directory, storeDependencies{
		random: rand.Reader,
		syncDirectory: func(path string, file *os.File) error {
			if path == "." {
				return sentinel
			}
			return platformSyncDirectory(file)
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("openStore(sync failure) error = %v, want %v", err, sentinel)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatalf("Open(retry) error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close(retry) error = %v", err)
	}
}

// TestUnixRootPreparationDurablyCreatesNestedMissingParents verifies constructor-created ancestors use the same child-then-parent protocol.
func TestUnixRootPreparationDurablyCreatesNestedMissingParents(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "harbor")
	second := filepath.Join(first, "state")
	directory := filepath.Join(second, "certificates")
	var synced []string
	err := preparePlatformRootWithSync(directory, func(path string, file *os.File) error {
		synced = append(synced, path)
		return platformSyncDirectory(file)
	})
	if err != nil {
		t.Fatalf("preparePlatformRootWithSync() error = %v", err)
	}
	want := []string{base, filepath.Dir(base), first, base, second, first, directory, second}
	if !slices.Equal(synced, want) {
		t.Fatalf("root hierarchy sync order = %#v, want %#v", synced, want)
	}
	for _, path := range []string{first, second, directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if info.Mode().Perm() != privateDirectoryMode {
			t.Fatalf("mode(%q) = %04o, want %04o", path, info.Mode().Perm(), privateDirectoryMode)
		}
	}
}

// TestUnixRootPreparationRetriesFailedNestedParentLinks proves each discovered prefix is re-synced before deeper creation resumes.
func TestUnixRootPreparationRetriesFailedNestedParentLinks(t *testing.T) {
	tests := []struct {
		name      string
		deep      bool
		wantRetry func(string, string, string, string) []string
	}{
		{
			name: "first missing ancestor",
			wantRetry: func(base, first, second, directory string) []string {
				return []string{first, base, second, first, directory, second}
			},
		},
		{
			name: "deep missing ancestor",
			deep: true,
			wantRetry: func(_ string, first, second, directory string) []string {
				return []string{second, first, directory, second}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			first := filepath.Join(base, "harbor")
			second := filepath.Join(first, "state")
			directory := filepath.Join(second, "certificates")
			failedChild := first
			failedParent := base
			if test.deep {
				failedChild = second
				failedParent = first
			}
			sentinel := errors.New("nested parent sync failed")
			previous := ""
			err := preparePlatformRootWithSync(directory, func(path string, file *os.File) error {
				if previous == failedChild && path == failedParent {
					return sentinel
				}
				previous = path
				return platformSyncDirectory(file)
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("preparePlatformRootWithSync(failure) error = %v, want %v", err, sentinel)
			}

			var retried []string
			err = preparePlatformRootWithSync(directory, func(path string, file *os.File) error {
				retried = append(retried, path)
				return platformSyncDirectory(file)
			})
			if err != nil {
				t.Fatalf("preparePlatformRootWithSync(retry) error = %v", err)
			}
			want := test.wantRetry(base, first, second, directory)
			if !slices.Equal(retried, want) {
				t.Fatalf("retry sync order = %#v, want %#v", retried, want)
			}
			if _, err := os.Stat(directory); err != nil {
				t.Fatalf("Stat(certificate root) error = %v", err)
			}
		})
	}
}

// assertSyncPair requires child metadata to flush immediately before the parent that links it.
func assertSyncPair(t *testing.T, synced []string, child, parent string) {
	t.Helper()
	for index := 0; index+1 < len(synced); index++ {
		if synced[index] == child && synced[index+1] == parent {
			return
		}
	}
	t.Fatalf("sync order %#v omits adjacent pair %q then %q", synced, child, parent)
}
