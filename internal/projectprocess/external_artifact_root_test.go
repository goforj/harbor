package projectprocess

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/userpaths"
)

// prepareExternalArtifactRootTestDataDirectory isolates the platform data root so artifact tests never touch user state.
func prepareExternalArtifactRootTestDataDirectory(t *testing.T) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "Harbor Application Support")
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_DATA_HOME", directory)
	case "windows":
		t.Setenv("LOCALAPPDATA", directory)
	default:
		t.Setenv("HOME", directory)
	}
}

// externalArtifactRootPathForTest returns the exact internally-derived capability path for one test session.
func externalArtifactRootPathForTest(t *testing.T, projectID domain.ProjectID, sessionID domain.SessionID) string {
	t.Helper()
	path, _, _, err := expectedExternalArtifactRoot(projectID, sessionID)
	if err != nil {
		t.Fatalf("expectedExternalArtifactRoot() error = %v", err)
	}
	return path
}

// TestStartRejectsExternalArtifactRootsOutsideTheDerivedSessionCapability prevents arbitrary cleanup targets.
func TestStartRejectsExternalArtifactRootsOutsideTheDerivedSessionCapability(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	checkout := t.TempDir()
	projectID := domain.ProjectID("project-artifact-capability")
	sessionID := domain.SessionID("session-artifact-capability")
	wrongDigest := externalArtifactRootPathForTest(t, projectID, "other-session")
	for _, artifactRoot := range []string{
		filepath.Join(t.TempDir(), "outside"),
		wrongDigest,
	} {
		t.Run(filepath.Base(artifactRoot), func(t *testing.T) {
			_, err := newTestSupervisor(Options{}).Start(t.Context(), StartRequest{
				ProjectID:            projectID,
				SessionID:            sessionID,
				CheckoutRoot:         checkout,
				ExternalArtifactRoot: artifactRoot,
				EnvironmentOverrides: projectProcessTestEnvironment(),
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Start() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestExternalArtifactRootRejectsSymlinkedIntermediate keeps the data-directory walk descriptor-bounded.
func TestExternalArtifactRootRejectsSymlinkedIntermediate(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	path := externalArtifactRootPathForTest(t, "project-artifact-link", "session-artifact-link")
	_, base, _, err := expectedExternalArtifactRoot("project-artifact-link", "session-artifact-link")
	if err != nil {
		t.Fatalf("expectedExternalArtifactRoot() error = %v", err)
	}
	sentinel := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatalf("create artifact base parent: %v", err)
	}
	if err := os.Symlink(sentinel, base); err != nil {
		t.Fatalf("link artifact base: %v", err)
	}
	_, err = prepareExternalArtifactRoot("project-artifact-link", "session-artifact-link", path)
	if err == nil {
		t.Fatal("prepareExternalArtifactRoot() error = nil, want symlink rejection")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("symlink target changed: %v", err)
	}
}

// TestExternalArtifactRootCleanupRejectsReplacement preserves a sentinel when the admitted root name changes to a link.
func TestExternalArtifactRootCleanupRejectsReplacement(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	projectID := domain.ProjectID("project-artifact-replacement")
	sessionID := domain.SessionID("session-artifact-replacement")
	path := externalArtifactRootPathForTest(t, projectID, sessionID)
	capability, err := prepareExternalArtifactRoot(projectID, sessionID, path)
	if err != nil {
		t.Fatalf("prepareExternalArtifactRoot() error = %v", err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatalf("move artifact root: %v", err)
	}
	if err := os.Symlink(sentinel, path); err != nil {
		t.Fatalf("replace artifact root with link: %v", err)
	}
	if err := removeExternalArtifactRoot(capability); err == nil {
		t.Fatal("removeExternalArtifactRoot() error = nil, want changed-root rejection")
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("sentinel = %q, %v, want preserved", contents, err)
	}
}

// TestExternalArtifactRootCleanupRetainsInBaseReplacement proves the final parent removal cannot recursively delete a swapped directory.
func TestExternalArtifactRootCleanupRetainsInBaseReplacement(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	projectID := domain.ProjectID("project-artifact-in-base-replacement")
	sessionID := domain.SessionID("session-artifact-in-base-replacement")
	path := externalArtifactRootPathForTest(t, projectID, sessionID)
	capability, err := prepareExternalArtifactRoot(projectID, sessionID, path)
	if err != nil {
		t.Fatalf("prepareExternalArtifactRoot() error = %v", err)
	}
	afterContentsRemoved := func() {
		if err := os.Rename(path, path+"-retired"); err != nil {
			t.Errorf("move admitted artifact root: %v", err)
			return
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Errorf("create replacement artifact root: %v", err)
			return
		}
		if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("keep"), 0o600); err != nil {
			t.Errorf("write replacement sentinel: %v", err)
		}
	}
	if err := removeExternalArtifactRootWithHook(capability, afterContentsRemoved); err == nil {
		t.Fatal("removeExternalArtifactRoot() error = nil, want changed-root rejection")
	}
	contents, err := os.ReadFile(filepath.Join(path, "sentinel"))
	if err != nil || string(contents) != "keep" {
		t.Fatalf("replacement sentinel = %q, %v, want preserved", contents, err)
	}
}

// TestExternalArtifactRootSupportsDataDirectorySpaces verifies platform paths such as macOS Application Support remain direct capabilities.
func TestExternalArtifactRootSupportsDataDirectorySpaces(t *testing.T) {
	prepareExternalArtifactRootTestDataDirectory(t)
	projectID := domain.ProjectID("project-artifact-spaces")
	sessionID := domain.SessionID("session-artifact-spaces")
	path := externalArtifactRootPathForTest(t, projectID, sessionID)
	dataDirectory, err := userpaths.DataDirectory()
	if err != nil {
		t.Fatalf("DataDirectory() error = %v", err)
	}
	if !strings.Contains(dataDirectory, "Harbor Application Support") {
		t.Fatalf("DataDirectory() = %q, want test path with spaces", dataDirectory)
	}
	capability, err := prepareExternalArtifactRoot(projectID, sessionID, path)
	if err != nil {
		t.Fatalf("prepareExternalArtifactRoot() error = %v", err)
	}
	if err := removeExternalArtifactRoot(capability); err != nil {
		t.Fatalf("removeExternalArtifactRoot() error = %v", err)
	}
}
