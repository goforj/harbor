package projectprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/projectruntime"
)

// TestStartupProgressRejectsMixedSessionsAndMalformedRecords protects the diagnostic transport independently of lifecycle state.
func TestStartupProgressRejectsMixedSessionsAndMalformedRecords(t *testing.T) {
	valid := projectruntime.StartupEvent{SchemaVersion: 1, Session: "session", Sequence: 1, Phase: "task", Label: "Run Docker Compose", Status: "started"}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, input string
		invalid     bool
		count       int
	}{
		{"valid", string(encoded) + "\n", false, 1},
		{"partial", string(encoded) + "\n{\"schema_version\":", false, 1},
		{"wrong-session", strings.Replace(string(encoded), `"session":"session"`, `"session":"other"`, 1) + "\n", true, 0},
		{"wrong-sequence", strings.Replace(string(encoded), `"sequence":1`, `"sequence":2`, 1) + "\n", true, 0},
		{"unknown-field", strings.TrimSuffix(string(encoded), "}") + `,"secret":"never admit"}` + "\n", true, 0},
		{"garbage", "not JSON\n", true, 0},
		{"two-values", string(encoded) + " {}\n", true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			progress := decodeStartupProgress([]byte(test.input), "session")
			if progress.Invalid != test.invalid || len(progress.Events) != test.count {
				t.Fatalf("unexpected progress: %#v", progress)
			}
		})
	}
}

// TestStartupProgressFilesAreExclusiveBoundedAndSessionScoped proves telemetry cannot overwrite another session or follow a link.
func TestStartupProgressFilesAreExclusiveBoundedAndSessionScoped(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &Supervisor{outputSpoolDirectory: root}
	path, err := prepareStartupProgress(root, "project", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStartupProgress(root, "project", "session"); err == nil {
		t.Fatal("existing stream overwritten")
	}
	if progress := supervisor.ReadStartupProgress("project", "other-session"); progress != nil {
		t.Fatal("read another session's progress")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maximumStartupProgressBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if progress := supervisor.ReadStartupProgress("project", "session"); progress == nil || !progress.Invalid {
		t.Fatal("oversized progress admitted")
	}
	if err := removeStartupProgress(root, "project", "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stream survived cleanup: %v", err)
	}
}

// TestStartupProgressEnvironmentDoesNotLeakAnotherSession verifies child launches ignore ambient telemetry destinations.
func TestStartupProgressEnvironmentDoesNotLeakAnotherSession(t *testing.T) {
	got := withoutStartupProgressEnvironment([]string{"PATH=/bin", "FORJ_DEV_EVENTS_FILE=/other", "FORJ_DEV_EVENTS_SESSION=other"})
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("ambient diagnostic environment leaked: %#v", got)
	}
}
