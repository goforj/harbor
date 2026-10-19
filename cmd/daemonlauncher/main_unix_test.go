//go:build darwin || linux

package main

import (
	"errors"
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
