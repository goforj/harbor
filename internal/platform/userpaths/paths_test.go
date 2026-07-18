package userpaths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveHomeDirectoryReturnsAnAbsoluteCleanPath verifies paths cannot inherit working-directory semantics.
func TestResolveHomeDirectoryReturnsAnAbsoluteCleanPath(t *testing.T) {
	want := filepath.Clean(filepath.Join(string(filepath.Separator), "users", "harbor"))
	got, err := resolveHomeDirectory(func() (string, error) {
		return filepath.Join(want, "documents", ".."), nil
	})
	if err != nil {
		t.Fatalf("resolve home directory: %v", err)
	}
	if got != want {
		t.Fatalf("home directory = %q, want %q", got, want)
	}
}

// TestResolveHomeDirectoryRejectsEmptyPath verifies missing user identity cannot place state relative to the daemon.
func TestResolveHomeDirectoryRejectsEmptyPath(t *testing.T) {
	_, err := resolveHomeDirectory(func() (string, error) {
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("error = %v, want empty-path error", err)
	}
}

// TestResolveHomeDirectoryRejectsRelativePath verifies malformed environments cannot redirect state into a checkout.
func TestResolveHomeDirectoryRejectsRelativePath(t *testing.T) {
	_, err := resolveHomeDirectory(func() (string, error) {
		return filepath.Join("users", "harbor"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "is not absolute") {
		t.Fatalf("error = %v, want relative-path error", err)
	}
}

// TestResolveHomeDirectoryPreservesLookupError verifies callers retain the operating system's failure evidence.
func TestResolveHomeDirectoryPreservesLookupError(t *testing.T) {
	want := errors.New("home unavailable")
	_, err := resolveHomeDirectory(func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

// TestDatabasePathLivesUnderDataDirectory verifies the database cannot drift from Harbor's durable state root.
func TestDatabasePathLivesUnderDataDirectory(t *testing.T) {
	directory, err := DataDirectory()
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}

	path, err := DatabasePath()
	if err != nil {
		t.Fatalf("resolve database path: %v", err)
	}
	if want := filepath.Join(directory, databaseFilename); path != want {
		t.Fatalf("database path = %q, want %q", path, want)
	}
}

// TestCertificateDirectoryLivesUnderDataDirectory verifies private material cannot drift from Harbor's durable state root.
func TestCertificateDirectoryLivesUnderDataDirectory(t *testing.T) {
	directory, err := DataDirectory()
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}

	path, err := CertificateDirectory()
	if err != nil {
		t.Fatalf("resolve certificate directory: %v", err)
	}
	if want := filepath.Join(directory, certificateDirectory); path != want {
		t.Fatalf("certificate directory = %q, want %q", path, want)
	}
}
