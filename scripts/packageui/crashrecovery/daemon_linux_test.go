//go:build linux

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/control"
)

// TestLinuxDaemonBirth rejects incomplete native records and handles process names with parentheses.
func TestLinuxDaemonBirth(t *testing.T) {
	fields := append([]string{"S"}, strings.Fields(strings.Repeat("0 ", 18))...)
	fields = append(fields, "4321")
	valid := "42 (harbor ) daemon) " + strings.Join(fields, " ")
	for _, test := range []struct{ name, record, want string }{
		{"valid", valid, "4321"},
		{"missing close", "42 daemon S 0", ""},
		{"missing fields", "42 (daemon) S 0", ""},
		{"zero birth", strings.TrimSuffix(valid, "4321") + "0", ""},
		{"invalid birth", strings.TrimSuffix(valid, "4321") + "bad", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := linuxDaemonBirth([]byte(test.record))
			if got != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("birth = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

// TestLinuxObservationRejectsUnauthenticatedPeer proves absent identity cannot reach signaling authority.
func TestLinuxObservationRejectsUnauthenticatedPeer(t *testing.T) {
	if _, err := observeInstalledDaemon(context.Background(), control.DaemonPeer{}); err == nil {
		t.Fatal("empty authenticated peer accepted")
	}
}
