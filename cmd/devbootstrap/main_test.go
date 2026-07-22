//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/devbootstrap"
)

// TestParseArgumentsRequiresExplicitInputs proves no UID, GID, or helper source is inferred from ambient state.
func TestParseArgumentsRequiresExplicitInputs(t *testing.T) {
	helper := filepath.Join(string(filepath.Separator), "build", "harbor-helper")
	configuration, err := parseArguments([]string{"--group-id", "0", "--helper", helper, "--user-id", "501"})
	if err != nil {
		t.Fatalf("parseArguments() error = %v", err)
	}
	if configuration.HelperSource != helper || configuration.UserID != 501 || configuration.GroupID != 0 {
		t.Fatalf("parseArguments() = %#v", configuration)
	}
}

// TestParseArgumentsRejectsMissingMalformedAndPositionalInputs covers the complete narrow command grammar.
func TestParseArgumentsRejectsMissingMalformedAndPositionalInputs(t *testing.T) {
	helper := filepath.Join(string(filepath.Separator), "build", "harbor-helper")
	valid := []string{"--helper", helper, "--user-id", "501", "--group-id", "20"}
	tests := []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "missing helper", arguments: []string{"--user-id", "501", "--group-id", "20"}, want: "--helper is required"},
		{name: "missing user", arguments: []string{"--helper", helper, "--group-id", "20"}, want: "--user-id is required"},
		{name: "missing group", arguments: []string{"--helper", helper, "--user-id", "501"}, want: "--group-id is required"},
		{name: "empty helper", arguments: []string{"--helper", "", "--user-id", "501", "--group-id", "20"}, want: "must not be empty"},
		{name: "negative user", arguments: []string{"--helper", helper, "--user-id", "-1", "--group-id", "20"}, want: "valid Unix ID"},
		{name: "signed user", arguments: []string{"--helper", helper, "--user-id", "+501", "--group-id", "20"}, want: "valid Unix ID"},
		{name: "leading-zero group", arguments: []string{"--helper", helper, "--user-id", "501", "--group-id", "020"}, want: "canonical decimal"},
		{name: "large group", arguments: []string{"--helper", helper, "--user-id", "501", "--group-id", "4294967296"}, want: "valid Unix ID"},
		{name: "reserved user", arguments: []string{"--helper", helper, "--user-id", "4294967295", "--group-id", "20"}, want: "reserved by chown"},
		{name: "reserved group", arguments: []string{"--helper", helper, "--user-id", "501", "--group-id", "4294967295"}, want: "reserved by chown"},
		{name: "unknown flag", arguments: append(append([]string(nil), valid...), "--destination", "/tmp/helper"), want: "flag provided but not defined"},
		{name: "duplicate helper", arguments: append(append([]string(nil), valid...), "--helper", helper), want: "specified only once"},
		{name: "positional", arguments: append(append([]string(nil), valid...), "repair"), want: "unexpected positional"},
	}
	if runtime.GOOS != "darwin" {
		tests = append(tests, struct {
			name      string
			arguments []string
			want      string
		}{name: "linux relay", arguments: append(append([]string(nil), valid...), "--launchd-relay", helper), want: "supported only on darwin"})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseArguments(test.arguments)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseArguments() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestRunCommandReportsOneConciseOutcome verifies success and failure formatting without launching a subprocess.
func TestRunCommandReportsOneConciseOutcome(t *testing.T) {
	helper := filepath.Join(string(filepath.Separator), "build", "harbor-helper")
	arguments := []string{"--helper", helper, "--user-id", "501", "--group-id", "20"}
	t.Run("success", func(t *testing.T) {
		var output bytes.Buffer
		var diagnostics bytes.Buffer
		called := false
		status := runCommand(arguments, &output, &diagnostics, func(configuration devbootstrap.Config) error {
			called = true
			return nil
		})
		if status != 0 || !called || output.String() != "Harbor development bootstrap complete.\n" || diagnostics.Len() != 0 {
			t.Fatalf("runCommand() = status %d, called %t, output %q, diagnostics %q", status, called, output.String(), diagnostics.String())
		}
	})
	t.Run("bootstrap failure", func(t *testing.T) {
		var output bytes.Buffer
		var diagnostics bytes.Buffer
		cause := errors.New("failed")
		status := runCommand(arguments, &output, &diagnostics, func(devbootstrap.Config) error { return cause })
		if status != 1 || output.Len() != 0 || !strings.Contains(diagnostics.String(), cause.Error()) {
			t.Fatalf("runCommand() = status %d, output %q, diagnostics %q", status, output.String(), diagnostics.String())
		}
	})
}
