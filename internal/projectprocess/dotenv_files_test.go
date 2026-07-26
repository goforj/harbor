package projectprocess

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestListDotenvFilesConfinesReadsToRegularRootDotenvFiles verifies the editor cannot become a checkout browser.
func TestListDotenvFilesConfinesReadsToRegularRootDotenvFiles(t *testing.T) {
	root := t.TempDir()
	for name, contents := range map[string]string{".env": "ROOT=yes", ".env.local": "LOCAL=yes", ".envrc": "ignored", ".harbor-dotenv-temp": "ignored"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".env.directory"), 0o700); err != nil {
		t.Fatalf("make dotenv directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, ".env.link")); err != nil {
		t.Fatalf("make dotenv symlink: %v", err)
	}
	files, err := ListDotenvFiles(root)
	if err != nil {
		t.Fatalf("ListDotenvFiles() error = %v", err)
	}
	if got := []string{files[0].Name, files[1].Name}; !reflect.DeepEqual(got, []string{".env", ".env.local"}) {
		t.Fatalf("dotenv files = %#v", got)
	}
}

// TestEnvironmentWithoutDotenvKeepsOnlyLauncherOwnedValues prevents one managed application's config from entering another.
func TestEnvironmentWithoutDotenvKeepsOnlyLauncherOwnedValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_DRIVER=sqlite\nAPP_NAME=harbor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("DB_QUERY_LOGGING=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := Environment{
		"PATH=/tools",
		"DB_DRIVER=sqlite",
		"APP_NAME=harbor",
		"DB_QUERY_LOGGING=1",
		"UNRELATED=preserved",
	}
	if runtime.GOOS == "windows" {
		environment[1] = "db_driver=sqlite"
	}

	got, err := EnvironmentWithoutDotenv(environment, root)
	if err != nil {
		t.Fatalf("EnvironmentWithoutDotenv() error = %v", err)
	}
	joined := strings.Join(got, "\x00")
	for _, removed := range []string{"DB_DRIVER=", "db_driver=", "APP_NAME=", "DB_QUERY_LOGGING="} {
		if strings.Contains(joined, removed) {
			t.Fatalf("environment retained dotenv value %q: %#v", removed, got)
		}
	}
	for _, preserved := range []string{"PATH=/tools", "UNRELATED=preserved"} {
		if !strings.Contains(joined, preserved) {
			t.Fatalf("environment removed launcher value %q: %#v", preserved, got)
		}
	}
}

// TestEnvironmentWithoutDotenvRejectsMalformedFiles prevents partial isolation from launching a contaminated child.
func TestEnvironmentWithoutDotenvRejectsMalformedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("INVALID='unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnvironmentWithoutDotenv(Environment{"PATH=/tools"}, root); err == nil {
		t.Fatal("EnvironmentWithoutDotenv() accepted malformed dotenv content")
	}
}

// TestSaveDotenvFileRequiresDisplayedRevisionAndPreservesMode verifies external edits cannot be silently overwritten.
func TestSaveDotenvFileRequiresDisplayedRevisionAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte("ORIGINAL=yes\n"), 0o640); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	files, err := ListDotenvFiles(root)
	if err != nil {
		t.Fatalf("ListDotenvFiles() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("EXTERNAL=yes\n"), 0o640); err != nil {
		t.Fatalf("external write dotenv: %v", err)
	}
	if _, err := SaveDotenvFile(root, ".env", "HARBOR=yes\n", files[0].Revision); err == nil {
		t.Fatal("SaveDotenvFile() succeeded after external modification")
	}
	current, err := ListDotenvFiles(root)
	if err != nil || current[0].Contents != "EXTERNAL=yes\n" {
		t.Fatalf("dotenv after rejected save = %#v, %v", current, err)
	}
	saved, err := SaveDotenvFile(root, ".env", "HARBOR=yes\n", current[0].Revision)
	if err != nil || saved.Contents != "HARBOR=yes\n" {
		t.Fatalf("SaveDotenvFile() = %#v, %v", saved, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved dotenv: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("saved mode = %o, want 0640", info.Mode().Perm())
	}
}

// TestSaveDotenvFileRejectsUnsafeNamesAndSymlinks verifies writes remain direct root dotenv files.
func TestSaveDotenvFileRejectsUnsafeNamesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if _, err := SaveDotenvFile(root, "../.env", "x", ""); err == nil {
		t.Fatal("SaveDotenvFile() accepted traversal")
	}
	if _, err := SaveDotenvFile(root, ".env.missing", "x", ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SaveDotenvFile() missing file error = %v, want not exist", err)
	}
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".env.local")); err != nil {
		t.Fatalf("make symlink: %v", err)
	}
	_, err := SaveDotenvFile(root, ".env.local", "x", "")
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SaveDotenvFile() error = %v, want symlink rejection", err)
	}
}
