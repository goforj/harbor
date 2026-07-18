package userpaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	databaseFilename     = "harbor.db"
	certificateDirectory = "certificates"
)

// environmentLookup keeps path policy testable without mutating process-wide environment state.
type environmentLookup func(string) (string, bool)

// homeDirectoryLookup keeps platform lookup failures reproducible in unit tests.
type homeDirectoryLookup func() (string, error)

// DataDirectory returns Harbor's platform-standard per-user data directory.
func DataDirectory() (string, error) {
	return platformDataDirectory(os.LookupEnv, os.UserHomeDir)
}

// DatabasePath returns the path to Harbor's durable SQLite database.
func DatabasePath() (string, error) {
	directory, err := DataDirectory()
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, databaseFilename), nil
}

// CertificateDirectory returns the dedicated per-user directory for Harbor-owned certificate material.
func CertificateDirectory() (string, error) {
	directory, err := DataDirectory()
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, certificateDirectory), nil
}

// resolveHomeDirectory rejects ambiguous relative paths because daemon state must not depend on its working directory.
func resolveHomeDirectory(lookup homeDirectoryLookup) (string, error) {
	directory, err := lookup()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if directory == "" {
		return "", errors.New("resolve user home directory: path is empty")
	}
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("resolve user home directory: path %q is not absolute", directory)
	}

	return filepath.Clean(directory), nil
}
