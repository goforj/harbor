//go:build windows

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/harbor/internal/control"
	"golang.org/x/sys/windows"
)

// TestAdmitDaemonIdentity requires agreement between native, authenticated peer and admitted installed identities.
func TestAdmitDaemonIdentity(t *testing.T) {
	identity := daemonIdentity{PID: 42, UserID: "S-1-5-21-1-2-3-1001", Executable: `C:\Program Files\GoForj\Harbor\releases\1\harbord.exe`, Birth: "12345"}
	peer := control.DaemonPeer{}
	peer.Transport.ProcessID = identity.PID
	peer.Transport.UserID = identity.UserID
	for _, test := range []struct {
		name   string
		change func(*daemonIdentity, *control.DaemonPeer, *string, *string, *string)
	}{
		{"zero PID", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.PID = 0 }},
		{"peer PID changed", func(_ *daemonIdentity, p *control.DaemonPeer, _, _, _ *string) { p.Transport.ProcessID++ }},
		{"peer SID changed", func(_ *daemonIdentity, p *control.DaemonPeer, _, _, _ *string) { p.Transport.UserID = "foreign" }},
		{"owner SID changed", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.UserID = "foreign" }},
		{"selected SID changed", func(_ *daemonIdentity, _ *control.DaemonPeer, _, s, _ *string) { *s = "foreign" }},
		{"controller SID changed", func(_ *daemonIdentity, _ *control.DaemonPeer, _, _, c *string) { *c = "foreign" }},
		{"missing selected SID", func(_ *daemonIdentity, _ *control.DaemonPeer, _, s, _ *string) { *s = "" }},
		{"foreign executable", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.Executable = `C:\temp\harbord.exe` }},
		{"missing executable", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.Executable = "" }},
		{"missing installed image", func(_ *daemonIdentity, _ *control.DaemonPeer, image, _, _ *string) { *image = "" }},
		{"missing birth", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.Birth = "" }},
		{"invalid birth", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.Birth = "abc" }},
		{"zero birth", func(i *daemonIdentity, _ *control.DaemonPeer, _, _, _ *string) { i.Birth = "0" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed, authenticated := identity, peer
			image, selected, current := identity.Executable, identity.UserID, identity.UserID
			test.change(&observed, &authenticated, &image, &selected, &current)
			if err := admitDaemonIdentity(observed, authenticated, image, selected, current); err == nil {
				t.Fatal("foreign or incomplete native identity was admitted")
			}
		})
	}
	if err := admitDaemonIdentity(identity, peer, identity.Executable, identity.UserID, identity.UserID); err != nil {
		t.Fatal(err)
	}
	if err := admitDaemonIdentity(identity, peer, `c:\PROGRAM FILES\GOFORJ\HARBOR\RELEASES\1\HARBORD.EXE`, identity.UserID, identity.UserID); err != nil {
		t.Fatalf("Windows case-insensitive image comparison: %v", err)
	}
}

// TestNativeDaemonIdentityRefusesInvalidHandle prevents unavailable native facts from authorizing a crash.
func TestNativeDaemonIdentityRefusesInvalidHandle(t *testing.T) {
	if _, err := nativeDaemonIdentity(0, 42); err == nil {
		t.Fatal("invalid process handle was admitted")
	}
}

// TestNativeDaemonIdentityRefusesHandlePIDMismatch binds PID evidence to the retained process object.
func TestNativeDaemonIdentityRefusesHandlePIDMismatch(t *testing.T) {
	if _, err := nativeDaemonIdentity(windows.CurrentProcess(), windows.GetCurrentProcessId()+1); err == nil {
		t.Fatal("a PID unrelated to the retained native process was admitted")
	}
}

// TestCanceledWindowsDaemonActions ensures cancellation prevents installation observation, start and termination.
func TestCanceledWindowsDaemonActions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeInstalledDaemon(ctx, control.DaemonPeer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("observe after cancellation: %v", err)
	}
	if err := crashInstalledDaemon(ctx, daemonIdentity{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("crash after cancellation: %v", err)
	}
	if err := ensureInstalledDaemon(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("start after cancellation: %v", err)
	}
}
