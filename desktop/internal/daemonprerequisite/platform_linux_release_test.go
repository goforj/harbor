//go:build linux && release

package daemonprerequisite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// TestLinuxEnsurerStartsTheAdmittedUserUnit proves reconnect never enables linger or starts an unmanaged child.
func TestLinuxEnsurerStartsTheAdmittedUserUnit(t *testing.T) {
	var calls []string
	ensurer := &linuxEnsurer{dependencies: linuxEnsurerDependencies{
		userID: func() int { return 1000 }, inspect: func() error { return nil },
		run: func(_ context.Context, arguments ...string) ([]byte, error) {
			calls = append(calls, arguments[0])
			if arguments[0] == "show" {
				return []byte("FragmentPath=" + daemonservice.LinuxUnitPath + "\nDropInPaths=\nLoadState=loaded\n"), nil
			}
			return nil, nil
		},
	}}
	for range 2 {
		if err := ensurer.Ensure(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"daemon-reload", "show", "start", "daemon-reload", "show", "start"}) {
		t.Fatal(calls)
	}
}

// TestAdmitLinuxUnitRejectsShadowsAndIncompleteObservations prevents a user override from being treated as the package.
func TestAdmitLinuxUnitRejectsShadowsAndIncompleteObservations(t *testing.T) {
	valid := "FragmentPath=" + daemonservice.LinuxUnitPath + "\nDropInPaths=\nLoadState=loaded\n"
	for _, output := range []string{
		"", strings.Replace(valid, daemonservice.LinuxUnitPath, "/home/user/.config/systemd/user/harbord.service", 1),
		strings.Replace(valid, "DropInPaths=", "DropInPaths=/foreign.conf", 1),
		strings.Replace(valid, "LoadState=loaded", "LoadState=not-found", 1),
		valid + "LoadState=loaded\n", strings.Replace(valid, "DropInPaths=\n", "", 1),
		valid + "Unknown=value\n", "malformed\n",
	} {
		if err := admitLinuxUnit([]byte(output)); err == nil {
			t.Fatalf("admitted %q", output)
		}
	}
}

// TestLinuxEnsurerStopsAtEveryFailedBoundary preserves an ambiguous unit instead of starting it.
func TestLinuxEnsurerStopsAtEveryFailedBoundary(t *testing.T) {
	for _, failure := range []string{"root", "inspect", "daemon-reload", "show", "foreign unit", "start", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			ensurer := &linuxEnsurer{dependencies: linuxEnsurerDependencies{
				userID: func() int {
					if failure == "root" {
						return 0
					}
					return 1000
				},
				inspect: func() error {
					if failure == "inspect" {
						return errors.New("missing package")
					}
					return nil
				},
				run: func(_ context.Context, arguments ...string) ([]byte, error) {
					if arguments[0] == failure {
						return nil, errors.New("unavailable")
					}
					if arguments[0] == "show" {
						if failure == "foreign unit" {
							return []byte("FragmentPath=/foreign"), nil
						}
						return []byte("FragmentPath=" + daemonservice.LinuxUnitPath + "\nDropInPaths=\nLoadState=loaded\n"), nil
					}
					if arguments[0] == "start" && failure != "start" {
						t.Fatal("start after failed admission")
					}
					return nil, nil
				},
			}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "cancelled" {
				cancel()
			}
			if err := ensurer.Ensure(ctx); err == nil {
				t.Fatal("missing failure")
			}
		})
	}
}
