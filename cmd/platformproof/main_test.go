package main

import (
	"context"
	"testing"
)

// TestRunRejectsUnknownCommands ensures the proof binary cannot become a generic host command runner.
func TestRunRejectsUnknownCommands(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{nil, {"unknown"}, {"project-identity", "extra"}} {
		if err := run(context.Background(), arguments); err == nil {
			t.Fatalf("expected arguments %v to fail", arguments)
		}
	}
}

// TestParseOptionsRejectsInvalidPorts keeps translated or overflowing ports out of proof evidence.
func TestParseOptionsRejectsInvalidPorts(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"--port", "0"}, {"--port", "65536"}} {
		if _, err := parseOptions("test", arguments, true); err == nil {
			t.Fatalf("expected arguments %v to fail", arguments)
		}
	}
}

// TestVerifyEvidenceRequiresRoot rejects an evidence gate with no artifact location.
func TestVerifyEvidenceRequiresRoot(t *testing.T) {
	t.Parallel()

	if err := verifyEvidence([]string{"--port", "3306"}); err == nil {
		t.Fatal("expected missing evidence root to fail")
	}
}

// TestVerifyDockerProjectEvidenceRequiresValidInputs keeps the protected product gate from accepting translated or incomplete requirements.
func TestVerifyDockerProjectEvidenceRequiresValidInputs(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"--app-port", "3000", "--service-port", "3306"},
		{"--root", "evidence", "--app-port", "0"},
		{"--root", "evidence", "--service-port", "65536"},
	} {
		if err := verifyDockerProjectEvidence(arguments); err == nil {
			t.Fatalf("expected arguments %v to fail", arguments)
		}
	}
}

// TestSplitNonEmpty normalizes workflow platform lists without inventing requirements.
func TestSplitNonEmpty(t *testing.T) {
	t.Parallel()

	platforms := splitNonEmpty(" linux, darwin,windows, ")
	if len(platforms) != 3 || platforms[1] != "darwin" {
		t.Fatalf("unexpected platforms: %v", platforms)
	}
}
