//go:build darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestPrepareDaemonLaunchMigratesOnlyImplicitServiceStartup preserves explicit maintenance and failure visibility.
func TestPrepareDaemonLaunchMigratesOnlyImplicitServiceStartup(t *testing.T) {
	for _, mode := range []string{"implicit", "explicit", "failed migration"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var arguments []string
			if mode == "explicit" {
				arguments = []string{"migrate"}
			}
			got, err := prepareDaemonLaunch("/installed/harbord", arguments, func(string) error {
				calls++
				if mode == "failed migration" {
					return errors.New("migration failed")
				}
				return nil
			})
			if mode == "failed migration" {
				if err == nil || got != nil {
					t.Fatal("migration failure did not stop launch")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if mode == "explicit" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("migrations = %d, want %d", calls, wantCalls)
			}
			if !reflect.DeepEqual(got, daemonArguments("/installed/harbord", arguments)) {
				t.Fatal(got)
			}
		})
	}
}

// TestDaemonArgumentsStartsTheLaunchAgentInForeground keeps the canonical no-argument service from exiting after help.
func TestDaemonArgumentsStartsTheLaunchAgentInForeground(t *testing.T) {
	want := []string{"/installed/harbord", "--foreground"}
	for _, arguments := range [][]string{nil, {"--foreground"}} {
		if got := daemonArguments("/installed/harbord", arguments); !reflect.DeepEqual(got, want) {
			t.Fatalf("daemonArguments(%q) = %q, want %q", arguments, got, want)
		}
	}
}

// TestDaemonArgumentsPreservesInstallerSelectedDaemonOperations keeps migration and lifecycle commands usable through the stable launcher.
func TestDaemonArgumentsPreservesInstallerSelectedDaemonOperations(t *testing.T) {
	got := daemonArguments("/installed/harbord", []string{"migrate", "--example"})
	want := []string{"/installed/harbord", "migrate", "--example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("daemonArguments() = %q, want %q", got, want)
	}
}

// TestResolveInstalledDaemonAtAdmitsOnlyOneSelectedRelease pins launcher execution beneath the release root.
func TestResolveInstalledDaemonAtAdmitsOnlyOneSelectedRelease(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "releases", "42")
	if err := os.MkdirAll(filepath.Join(release, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(release, "bin", "harbord")
	if err := os.WriteFile(daemon, []byte("daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join("releases", "42"), current); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInstalledDaemonAt(root, current)
	if err != nil {
		t.Fatalf("resolveInstalledDaemonAt() error = %v", err)
	}
	if got != daemon {
		t.Fatalf("resolveInstalledDaemonAt() = %q, want %q", got, daemon)
	}
}

// TestResolveInstalledDaemonAtRejectsASelectionOutsideTheReleaseRoot prevents path indirection from becoming execution authority.
func TestResolveInstalledDaemonAtRejectsASelectionOutsideTheReleaseRoot(t *testing.T) {
	root := t.TempDir()
	foreign := t.TempDir()
	current := filepath.Join(root, "current")
	if err := os.Symlink(foreign, current); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveInstalledDaemonAt(root, current); err == nil {
		t.Fatal("resolveInstalledDaemonAt() error = nil")
	}
}
