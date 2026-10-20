//go:build windows

package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// fixtureChild records settlement before its receipt closes and checks migration's bounded context.
type fixtureChild struct {
	events     *[]string
	role       string
	code       uint32
	waitError  error
	closeError error
	migration  bool
}

// Wait records the exact child role and refuses an accidentally unbounded migration fixture.
func (child *fixtureChild) Wait(ctx context.Context) (uint32, error) {
	*child.events = append(*child.events, "wait:"+child.role)
	deadline, bounded := ctx.Deadline()
	if child.migration && (!bounded || time.Until(deadline) > time.Minute || time.Until(deadline) <= 0) {
		return 0, errors.New("migration context has no one-minute bound")
	}
	if !child.migration && bounded {
		return 0, errors.New("foreground lifetime was unexpectedly bounded")
	}
	return child.code, child.waitError
}

// Close records the retained child receipt being released after Wait.
func (child *fixtureChild) Close() error {
	*child.events = append(*child.events, "child-close:"+child.role)
	return child.closeError
}

// TestWindowsDaemonLauncherAdmission rejects worker authority and unsupported modes before installation or user-data access.
func TestWindowsDaemonLauncherAdmission(t *testing.T) {
	for _, arguments := range [][]string{{"--unknown"}, {"--installed-worker", "extra"}, {"migrate"}, {"--foreground"}} {
		operations := launcherOperations{}
		if _, err := runDaemonLauncher(arguments, operations); err == nil {
			t.Fatalf("admitted arguments %q", arguments)
		}
	}
	authority := errors.New("effective administrative authority")
	operations := launcherOperations{inspect: func() error { return authority }}
	if _, err := runDaemonLauncher([]string{"--installed-worker"}, operations); !errors.Is(err, authority) {
		t.Fatalf("worker authority error = %v", err)
	}
}

// TestWindowsDaemonLauncherLifecycle verifies singleton, migration, and exact child-lifetime ordering without native process fixtures.
func TestWindowsDaemonLauncherLifecycle(t *testing.T) {
	migrationFailure := errors.New("migration wait failed")
	for _, fixture := range []struct {
		name            string
		arguments       []string
		inspection      error
		preparation     error
		migrationCode   uint32
		migrationError  error
		childCloseError error
		wantCode        uint32
		wantError       string
		wantEvents      string
	}{
		{name: "task-demotion", wantCode: 17, wantEvents: "admit,start:daemon-launcher:--installed-worker,wait:daemon-launcher,child-close:daemon-launcher,installation-close"},
		{name: "worker-active", arguments: []string{"--installed-worker"}, preparation: daemonservice.ErrDaemonActive, wantEvents: "inspect,admit,prepare,installation-close"},
		{name: "worker-success", arguments: []string{"--installed-worker"}, wantCode: 17, wantEvents: "inspect,admit,prepare,start:daemon:migrate,wait:daemon,child-close:daemon,preparation-released,start:daemon:--foreground,wait:daemon,child-close:daemon,installation-close"},
		{name: "migration-exit", arguments: []string{"--installed-worker"}, migrationCode: 9, wantError: "migration exited with code 9", wantEvents: "inspect,admit,prepare,start:daemon:migrate,wait:daemon,child-close:daemon,preparation-released,installation-close"},
		{name: "migration-wait", arguments: []string{"--installed-worker"}, migrationError: migrationFailure, wantError: "migration wait failed", wantEvents: "inspect,admit,prepare,start:daemon:migrate,wait:daemon,child-close:daemon,preparation-released,installation-close"},
		{name: "migration-close", arguments: []string{"--installed-worker"}, childCloseError: errors.New("receipt close failed"), wantError: "receipt close failed", wantEvents: "inspect,admit,prepare,start:daemon:migrate,wait:daemon,child-close:daemon,preparation-released,installation-close"},
		{name: "verify-high", arguments: []string{"--verify-stopped"}, inspection: errors.New("high integrity"), wantCode: 17, wantEvents: "inspect,admit,start:daemon-launcher:--verify-stopped,wait:daemon-launcher,child-close:daemon-launcher,installation-close"},
		{name: "verify-stopped", arguments: []string{"--verify-stopped"}, wantEvents: "inspect,admit,prepare,preparation-released,installation-close"},
		{name: "verify-live", arguments: []string{"--verify-stopped"}, preparation: daemonservice.ErrDaemonActive, wantError: daemonservice.ErrDaemonActive.Error(), wantEvents: "inspect,admit,prepare,installation-close"},
		{name: "prepare-failure", arguments: []string{"--installed-worker"}, preparation: errors.New("foreign runtime ACL"), wantError: "foreign runtime ACL", wantEvents: "inspect,admit,prepare,installation-close"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var events []string
			operations := launcherOperations{
				inspect: func() error { events = append(events, "inspect"); return fixture.inspection },
				admit:   func() error { events = append(events, "admit"); return nil },
				close:   func() error { events = append(events, "installation-close"); return nil },
				prepare: func(migrate func() error) error {
					events = append(events, "prepare")
					if fixture.preparation != nil {
						return fixture.preparation
					}
					err := migrate()
					events = append(events, "preparation-released")
					return err
				},
				start: func(role string, arguments []string) (launchedChild, error) {
					events = append(events, "start:"+role+":"+strings.Join(arguments, ","))
					migration := role == "daemon" && reflect.DeepEqual(arguments, []string{"migrate"})
					child := &fixtureChild{events: &events, role: role, code: 17, migration: migration}
					if migration {
						child.code = fixture.migrationCode
						child.waitError = fixture.migrationError
						child.closeError = fixture.childCloseError
					}
					return child, nil
				},
			}
			code, err := runDaemonLauncher(fixture.arguments, operations)
			if fixture.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), fixture.wantError) {
					t.Fatalf("error=%v, expected %q", err, fixture.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if code != fixture.wantCode || strings.Join(events, ",") != fixture.wantEvents {
				t.Fatalf("code=%d, events=%v; expected code=%d, events=%s", code, events, fixture.wantCode, fixture.wantEvents)
			}
		})
	}
}

// TestWindowsDaemonLauncherFailureCleanup preserves failure causes and releases installation admission after launch refusal.
func TestWindowsDaemonLauncherFailureCleanup(t *testing.T) {
	failure := errors.New("native child authority rejected")
	closed := false
	operations := launcherOperations{
		admit: func() error { return nil },
		close: func() error { closed = true; return nil },
		start: func(string, []string) (launchedChild, error) { return nil, failure },
	}
	if _, err := runDaemonLauncher(nil, operations); !errors.Is(err, failure) || !closed {
		t.Fatalf("error=%v, closed=%v", err, closed)
	}
	operations.admit = func() error { return failure }
	operations.close = func() error { t.Fatal("closed admission which did not succeed"); return nil }
	if _, err := runDaemonLauncher(nil, operations); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

// TestWindowsDaemonWorkerUserAdmission rejects direct activation under a different account before migration.
func TestWindowsDaemonWorkerUserAdmission(t *testing.T) {
	user := "S-1-5-21-1-2-3-1001"
	if err := admitInstalledUser(user, user); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", "S-1-5-21-1-2-3-1002"} {
		if err := admitInstalledUser(selected, user); err == nil {
			t.Fatalf("admitted selected account %q", selected)
		}
	}
}
