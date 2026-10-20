//go:build darwin && cgo

package main

import (
	"context"
	"testing"

	"github.com/goforj/harbor/internal/control"
)

// TestDarwinJobScalars rejects duplicate and nested launchd identity substitutions.
func TestDarwinJobScalars(t *testing.T) {
	job := "gui/501/com.goforj.harbor.daemon"
	for _, test := range []struct {
		name, output, wantPID string
		invalid               bool
	}{
		{"direct", job + " = {\n pid = 42\n environment = {\n pid = 999\n }\n}", "42", false},
		{"nested only", job + " = {\n environment = {\n pid = 999\n }\n}", "", false},
		{"duplicate", job + " = {\n pid = 42\n pid = 43\n}", "", true},
		{"multiple arguments", job + " = {\n arguments = {\n /package/launcher\n --foreign\n }\n}", "", true},
		{"duplicate arguments", job + " = {\n arguments = {\n /package/launcher\n }\n arguments = {\n /foreign\n }\n}", "", true},
		{"foreign root", "system/foreign = {\n pid = 42\n}", "", true},
		{"unclosed", job + " = {\n pid = 42", "", true},
		{"trailing", job + " = {\n pid = 42\n}\n pid = 999", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := darwinJobScalars(test.output, job)
			if (err != nil) != test.invalid {
				t.Fatalf("parse error = %v; invalid = %v", err, test.invalid)
			}
			if err == nil && facts["pid"] != test.wantPID {
				t.Fatalf("pid = %q; want %q", facts["pid"], test.wantPID)
			}
		})
	}
	facts, err := darwinJobScalars(job+" = {\n arguments = {\n /Library/Application Support/GoForj/Harbor/daemon-launcher\n }\n}", job)
	if err != nil || facts["arguments"] != "/Library/Application Support/GoForj/Harbor/daemon-launcher" {
		t.Fatalf("exact installed arguments lost: %v, %v", facts, err)
	}
}

// TestDarwinObservationRejectsUnauthenticatedPeer proves absent identity cannot reach signaling authority.
func TestDarwinObservationRejectsUnauthenticatedPeer(t *testing.T) {
	if _, err := observeInstalledDaemon(context.Background(), control.DaemonPeer{}); err == nil {
		t.Fatal("empty authenticated peer accepted")
	}
}
