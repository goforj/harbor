package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSourceDevelopmentRunsDaemonHandoffBeforeWatchers verifies the checked-in development graph cannot start Harbor watchers behind an independently running daemon.
func TestSourceDevelopmentRunsDaemonHandoffBeforeWatchers(t *testing.T) {
	configurationPath := filepath.Join("..", "..", ".goforj.yml")
	configuration, err := os.ReadFile(configurationPath)
	if err != nil {
		t.Fatalf("read %s: %v", configurationPath, err)
	}

	const handoffTask = "- name: Handoff Harbor daemon\n      cmd: go run ./cmd/devhandoff"
	if !strings.Contains(string(configuration), handoffTask) {
		t.Fatalf("%s does not configure the Harbor daemon handoff task", configurationPath)
	}
}
