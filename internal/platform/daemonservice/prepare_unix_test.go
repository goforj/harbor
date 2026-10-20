//go:build darwin || linux

package daemonservice

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

// TestPrepareDatabaseHoldsAndReleasesTheDaemonSingleton covers successful preparation and migration failure.
func TestPrepareDatabaseHoldsAndReleasesTheDaemonSingleton(t *testing.T) {
	for _, failMigration := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "migration failure"}[failMigration], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "runtime")
			want := errors.New("migration failed")
			err := prepareDatabaseAt(root, func() error {
				file, err := os.OpenFile(filepath.Join(root, runtimepath.DaemonLockFilename), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
					t.Fatal("migration does not own the daemon singleton")
				}
				if failMigration {
					return want
				}
				return nil
			})
			if failMigration && !errors.Is(err, want) {
				t.Fatalf("migration error = %v", err)
			}
			if !failMigration && err != nil {
				t.Fatal(err)
			}
			if err := prepareDatabaseAt(root, func() error { return nil }); err != nil {
				t.Fatal("singleton not released:", err)
			}
		})
	}
}

// TestPrepareDatabasePreservesAnExistingDaemon prevents source-development and installed writers from overlapping.
func TestPrepareDatabasePreservesAnExistingDaemon(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(root, runtimepath.DaemonLockFilename), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	err = prepareDatabaseAt(root, func() error { t.Fatal("migration ran beside an existing daemon"); return nil })
	if !errors.Is(err, ErrDaemonActive) {
		t.Fatalf("contended guard = %v", err)
	}
}

// TestPrepareDatabaseRejectsUnsafeRuntimeShapes verifies no migration follows a redirected or shared singleton.
func TestPrepareDatabaseRejectsUnsafeRuntimeShapes(t *testing.T) {
	for _, shape := range []string{"directory symlink", "directory mode", "lock symlink", "lock mode", "lock hardlink", "lock directory"} {
		t.Run(shape, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "runtime")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(root, runtimepath.DaemonLockFilename)
			switch shape {
			case "directory symlink":
				if err := os.Rename(root, root+".foreign"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root+".foreign", root); err != nil {
					t.Fatal(err)
				}
			case "directory mode":
				if err := os.Chmod(root, 0o755); err != nil {
					t.Fatal(err)
				}
			case "lock symlink":
				if err := os.Symlink("foreign", lock); err != nil {
					t.Fatal(err)
				}
			case "lock directory":
				if err := os.Mkdir(lock, 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				mode := os.FileMode(0o600)
				if shape == "lock mode" {
					mode = 0o644
				}
				if err := os.WriteFile(lock, []byte("preserve"), mode); err != nil {
					t.Fatal(err)
				}
				if shape == "lock hardlink" {
					if err := os.Link(lock, lock+".foreign"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := prepareDatabaseAt(root, func() error { t.Fatal("unsafe singleton reached migration"); return nil }); err == nil {
				t.Fatal("unsafe singleton admitted")
			}
		})
	}
}
