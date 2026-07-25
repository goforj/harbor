package projectprocess

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestDevelopmentArtifactPathIsOpaque verifies domain identities cannot become filesystem path components.
func TestDevelopmentArtifactPathIsOpaque(t *testing.T) {
	directory := t.TempDir()
	path, name, err := developmentArtifactPath(directory, "project-one", "session-one")
	if err != nil {
		t.Fatalf("developmentArtifactPath() error = %v", err)
	}
	if filepath.Dir(path) != filepath.Join(directory, developmentArtifactDirectoryName) {
		t.Fatalf("developmentArtifactPath() parent = %q", filepath.Dir(path))
	}
	if filepath.Base(path) != name || len(name) != 64 || strings.Contains(name, "project") || strings.Contains(name, "session") {
		t.Fatalf("developmentArtifactPath() opaque name = %q", name)
	}
}

// TestDevelopmentArtifactPathRejectsUnsafeInputs verifies lifecycle cleanup never derives authority from malformed paths or identities.
func TestDevelopmentArtifactPathRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		projectID string
		sessionID string
	}{
		{name: "relative directory", directory: "relative", projectID: "project-one", sessionID: "session-one"},
		{name: "unclean directory", directory: t.TempDir() + string(os.PathSeparator) + "..", projectID: "project-one", sessionID: "session-one"},
		{name: "invalid project", directory: t.TempDir(), projectID: "", sessionID: "session-one"},
		{name: "invalid session", directory: t.TempDir(), projectID: "project-one", sessionID: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := developmentArtifactPath(test.directory, domainProjectID(test.projectID), domainSessionID(test.sessionID)); err == nil {
				t.Fatal("developmentArtifactPath() error = nil")
			}
		})
	}
}

// TestDevelopmentArtifactRootRemovalIsSessionScoped verifies cleanup cannot remove a neighboring session tree.
func TestDevelopmentArtifactRootRemovalIsSessionScoped(t *testing.T) {
	directory := t.TempDir()
	first, err := prepareDevelopmentArtifactRoot(directory, "project-one", "session-one")
	if err != nil {
		t.Fatalf("prepare first development artifact root: %v", err)
	}
	second, err := prepareDevelopmentArtifactRoot(directory, "project-two", "session-two")
	if err != nil {
		_ = first.remove()
		t.Fatalf("prepare second development artifact root: %v", err)
	}
	t.Cleanup(func() {
		_ = first.remove()
		_ = second.remove()
	})
	firstFile := filepath.Join(first.path, "bin", "app")
	if err := os.MkdirAll(filepath.Dir(firstFile), 0o700); err != nil {
		t.Fatalf("create first artifact directory: %v", err)
	}
	if err := os.WriteFile(firstFile, []byte("first"), 0o600); err != nil {
		t.Fatalf("write first artifact: %v", err)
	}
	secondFile := filepath.Join(second.path, "bin", "app")
	if err := os.MkdirAll(filepath.Dir(secondFile), 0o700); err != nil {
		t.Fatalf("create second artifact directory: %v", err)
	}
	if err := os.WriteFile(secondFile, []byte("second"), 0o600); err != nil {
		t.Fatalf("write second artifact: %v", err)
	}
	if err := first.remove(); err != nil {
		t.Fatalf("remove first development artifact root: %v", err)
	}
	if _, err := os.Stat(first.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first development artifact root error = %v, want not exist", err)
	}
	if contents, err := os.ReadFile(secondFile); err != nil || string(contents) != "second" {
		t.Fatalf("neighboring development artifact = %q, %v", contents, err)
	}
}

// domainProjectID converts table input without obscuring validation intent.
func domainProjectID(value string) domain.ProjectID {
	return domain.ProjectID(value)
}

// domainSessionID converts table input without obscuring validation intent.
func domainSessionID(value string) domain.SessionID {
	return domain.SessionID(value)
}
