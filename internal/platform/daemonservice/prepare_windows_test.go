//go:build windows

package daemonservice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/runtimepath"
	"github.com/goforj/harbor/internal/platform/windowsfile"
	"golang.org/x/sys/windows"
)

// TestWindowsPreparationHoldsAndReleasesSingleton proves the callback owns the exact production byte-range guard.
func TestWindowsPreparationHoldsAndReleasesSingleton(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := filepath.Join(runtimeFixtureRoot(t), "runtime")
		want := errors.New("migration failure")
		err := prepareDatabaseAt(root, func() error {
			if err := prepareDatabaseAt(root, func() error { t.Fatal("concurrent migration ran"); return nil }); !errors.Is(err, ErrDaemonActive) {
				t.Fatalf("nested preparation = %v", err)
			}
			if fail {
				return want
			}
			return nil
		})
		if fail && !errors.Is(err, want) {
			t.Fatalf("migration error = %v", err)
		}
		if !fail && err != nil {
			t.Fatal(err)
		}
		if err := prepareDatabaseAt(root, func() error { return nil }); err != nil {
			t.Fatalf("singleton not released: %v", err)
		}
	}
}

// TestWindowsPreparationPreservesExistingDaemon uses the daemon's native byte offset, length and lock flags independently.
func TestWindowsPreparationPreservesExistingDaemon(t *testing.T) {
	root := filepath.Join(runtimeFixtureRoot(t), "runtime")
	if err := prepareDatabaseAt(root, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(root, runtimepath.DaemonLockFilename), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), 0x1|0x2, 0, 1, 0, overlapped); err != nil {
		t.Fatal(err)
	}
	defer windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	if err := prepareDatabaseAt(root, func() error { t.Fatal("migration ran beside daemon"); return nil }); !errors.Is(err, ErrDaemonActive) {
		t.Fatalf("existing daemon = %v", err)
	}
}

// TestWindowsPreparationRefusesUnsafeRuntime preserves redirected or foreign singleton objects without migration.
func TestWindowsPreparationRefusesUnsafeRuntime(t *testing.T) {
	for _, shape := range []string{"runtime reparse", "parent reparse", "lock reparse", "lock hardlink", "lock directory", "runtime acl", "lock acl", "runtime owner", "lock owner", "relative"} {
		t.Run(shape, func(t *testing.T) {
			parent := runtimeFixtureRoot(t)
			root := filepath.Join(parent, "runtime")
			if err := prepareDatabaseAt(root, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(root, runtimepath.DaemonLockFilename)
			switch shape {
			case "runtime reparse":
				if err := os.Rename(root, root+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root+".original", root); err != nil {
					t.Skipf("account cannot create directory reparse: %v", err)
				}
			case "parent reparse":
				alias := filepath.Join(parent, "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Skipf("account cannot create parent reparse: %v", err)
				}
				root = filepath.Join(alias, "child")
			case "lock reparse":
				if err := os.Rename(lock, lock+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(lock+".original", lock); err != nil {
					t.Skipf("account cannot create file reparse: %v", err)
				}
			case "lock hardlink":
				if err := os.Link(lock, lock+".alias"); err != nil {
					t.Fatal(err)
				}
			case "lock directory":
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(lock, 0700); err != nil {
					t.Fatal(err)
				}
			case "runtime acl", "lock acl", "runtime owner", "lock owner":
				path := root
				if shape == "lock acl" || shape == "lock owner" {
					path = lock
				}
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				owner := user.User.Sid.String()
				if shape == "runtime owner" || shape == "lock owner" {
					owner = "BA"
				}
				sddl := "O:" + owner + "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)"
				if shape == "runtime acl" || shape == "lock acl" {
					sddl += "(A;;GR;;;BU)"
				}
				setRuntimeFixtureSecurity(t, path, sddl)
			case "relative":
				root = "relative"
			}
			if err := prepareDatabaseAt(root, func() error { t.Fatal("unsafe runtime reached migration"); return nil }); err == nil {
				t.Fatal("unsafe runtime admitted")
			}
		})
	}
}

// setRuntimeFixtureSecurity changes only an owned temporary fixture to exercise native admission refusal.
func setRuntimeFixtureSecurity(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
	if err := errors.Join(err, windows.CloseHandle(handle)); err != nil {
		t.Fatal(err)
	}
}

// runtimeFixtureRoot removes Windows short-name aliases from only the temporary test directory.
func runtimeFixtureRoot(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	final, err := windowsfile.FinalPath(windows.Handle(file.Fd()))
	if err := errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(final, `\\?\`)
}
