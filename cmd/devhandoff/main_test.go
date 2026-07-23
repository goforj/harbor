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
	const sourceMarker = "env:\n          HARBOR_SOURCE_DEVELOPMENT_HANDOFF: \"1\""
	if !strings.Contains(string(configuration), handoffTask) || !strings.Contains(string(configuration), sourceMarker) {
		t.Fatalf("%s does not configure the Harbor daemon handoff task and source-development marker", configurationPath)
	}
}
