package main

import (
	"strings"
	"testing"
)

// TestParseArgumentsAcceptsExactBrokerBoundary verifies the ticket remains outside argv while descriptor ownership stays explicit.
func TestParseArgumentsAcceptsExactBrokerBoundary(t *testing.T) {
	parsed, err := parseArguments([]string{configFlag, "/tmp/harbor-broker.json", stdoutFDFlag, "3", stderrFDFlag, "4"})
	if err != nil {
		t.Fatalf("parseArguments() error = %v", err)
	}
	if parsed.configPath != "/tmp/harbor-broker.json" || parsed.stdoutFD != 3 || parsed.stderrFD != 4 {
		t.Fatalf("parsed arguments = %#v", parsed)
	}
}

// TestParseArgumentsRejectsAmbiguousBrokerBoundary covers order, descriptor, and duplicate-stream failures.
func TestParseArgumentsRejectsAmbiguousBrokerBoundary(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: nil, want: "requires"},
		{name: "reordered", args: []string{stdoutFDFlag, "3", configFlag, "/tmp/config", stderrFDFlag, "4"}, want: "requires"},
		{name: "relative descriptor", args: []string{configFlag, "/tmp/config", stdoutFDFlag, "+3", stderrFDFlag, "4"}, want: "canonical integer"},
		{name: "standard input", args: []string{configFlag, "/tmp/config", stdoutFDFlag, "2", stderrFDFlag, "4"}, want: "greater than 2"},
		{name: "same descriptor", args: []string{configFlag, "/tmp/config", stdoutFDFlag, "3", stderrFDFlag, "3"}, want: "must differ"},
		{name: "ticket in argv", args: []string{configFlag, "/tmp/config", stdoutFDFlag, "3", stderrFDFlag, "4", "ticket"}, want: "requires"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseArguments(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseArguments() error = %v, want %q", err, test.want)
			}
		})
	}
}
