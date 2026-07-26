//go:build darwin || linux

package releasebundle

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

const installedTestSequence = uint64(73)

// TestVerifyInstalledDarwinDevelopmentReleaseAdmitsTheExactSealedInstallation proves package admission binds selection, symlinks, and executables.
func TestVerifyInstalledDarwinDevelopmentReleaseAdmitsTheExactSealedInstallation(t *testing.T) {
	root, userID, groupID := installedTestFixture(t)
	manifest, err := verifyInstalledDarwinDevelopmentRelease(root, userID, groupID)
	if err != nil {
		t.Fatalf("verifyInstalledDarwinDevelopmentRelease() error = %v", err)
	}
	if manifest.ReleaseSequence != installedTestSequence {
		t.Fatalf("verified release sequence = %d, want %d", manifest.ReleaseSequence, installedTestSequence)
	}
}

// TestVerifyInstalledDarwinDevelopmentReleasePreservesAMismatchedComponent proves uninstall admission fails before foreign content can be removed.
func TestVerifyInstalledDarwinDevelopmentReleasePreservesAMismatchedComponent(t *testing.T) {
	root, userID, groupID := installedTestFixture(t)
	daemon := installedPath(
		root,
		"/Library/Application Support/GoForj/Harbor/releases/73/bin/harbord",
	)
	if err := os.WriteFile(daemon, []byte("foreign daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInstalledDarwinDevelopmentRelease(root, userID, groupID); err == nil {
		t.Fatal("verifyInstalledDarwinDevelopmentRelease() error = nil")
	}
	if content, err := os.ReadFile(daemon); err != nil || string(content) != "foreign daemon" {
		t.Fatalf("mismatched component changed: content=%q error=%v", content, err)
	}
}

// TestVerifyInstalledDarwinDevelopmentReleaseRejectsRedirectedSelection proves current cannot escape the selected immutable release.
func TestVerifyInstalledDarwinDevelopmentReleaseRejectsRedirectedSelection(t *testing.T) {
	root, userID, groupID := installedTestFixture(t)
	current := installedPath(root, "/Library/Application Support/GoForj/Harbor/current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/74", current); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyInstalledDarwinDevelopmentRelease(root, userID, groupID); err == nil {
		t.Fatal("verifyInstalledDarwinDevelopmentRelease() error = nil")
	}
}

// installedTestFixture materializes the package's fixed layout beneath an owner-controlled root.
func installedTestFixture(t *testing.T) (string, uint32, uint32) {
	t.Helper()
	root := t.TempDir()
	for _, plan := range darwinComponentPlans(installedTestSequence) {
		source := plan.source
		if source == "" {
			source = plan.component.Destination
		}
		path := installedPath(root, source)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(plan.component.Role), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := SealDarwinDevelopmentPayload(root, DarwinConfig{
		Version:         "0.1.0-dev.73",
		ReleaseSequence: installedTestSequence,
		SourceRevision:  "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, plan := range darwinComponentPlans(installedTestSequence) {
		if plan.source == "" {
			continue
		}
		source := installedPath(root, plan.source)
		destination := installedPath(root, plan.component.Destination)
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		mode, err := strconv.ParseUint(manifest.Components[index].Mode, 8, 32)
		if err != nil {
			t.Fatal(err)
		}
		nativeFileMode := os.FileMode(mode & 0o777)
		if mode&0o4000 != 0 {
			nativeFileMode |= os.ModeSetuid
		}
		if err := os.WriteFile(destination, content, nativeFileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(destination, nativeFileMode); err != nil {
			t.Fatal(err)
		}
	}
	machineRoot := installedPath(root, "/Library/Application Support/GoForj/Harbor")
	if err := os.Symlink(filepath.Join("releases", strconv.FormatUint(installedTestSequence, 10)), filepath.Join(machineRoot, "current")); err != nil {
		t.Fatal(err)
	}
	cli := installedPath(root, "/usr/local/bin/harbor")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/Library/Application Support/GoForj/Harbor/current/bin/harbor", cli); err != nil {
		t.Fatal(err)
	}
	information, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("native test root ownership is unavailable")
	}
	return root, status.Uid, status.Gid
}
