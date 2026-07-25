package productproof

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

const (
	// TrustedHTTPSLifecycleEvidenceSchemaVersion identifies the native lifecycle summary schema.
	TrustedHTTPSLifecycleEvidenceSchemaVersion = 1
	maximumTrustedHTTPSLogBytes                = 256 << 10
)

var (
	requiredTrustedHTTPSChecks = []string{
		"full network setup completed",
		"three generated projects reached distinct trusted HTTPS routes",
		"literal system DNS and default trust returned three distinct OpenAPI identities",
		"project processes and per-user daemon resources were removed",
	}
	trustedHTTPSEvidenceFiles = []string{
		"daemon-generation-1-hard-kill.log",
		"daemon-generation-2-restart.log",
		"daemon-generation-3-startup-recovery.log",
		"summary.json",
	}
)

// TrustedHTTPSLifecycleSummary is the bounded non-secret result emitted by the native behavioral test.
type TrustedHTTPSLifecycleSummary struct {
	SchemaVersion   int      `json:"schema_version"`
	OperatingSystem string   `json:"operating_system"`
	Checks          []string `json:"checks"`
}

// TrustedHTTPSLifecycleRequirement identifies the native platform required by one product-worker gate.
type TrustedHTTPSLifecycleRequirement struct {
	Platform string
}

// VerifyTrustedHTTPSLifecycleEvidenceDirectory verifies one exact native trusted-HTTPS lifecycle artifact.
func VerifyTrustedHTTPSLifecycleEvidenceDirectory(root string, requirement TrustedHTTPSLifecycleRequirement) error {
	if root == "" {
		return errors.New("trusted HTTPS evidence root is required")
	}
	if requirement.Platform != "darwin" && requirement.Platform != "linux" && requirement.Platform != "windows" {
		return fmt.Errorf("trusted HTTPS evidence platform %q is not implemented", requirement.Platform)
	}

	observed := make([]string, 0, len(trustedHTTPSEvidenceFiles))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("trusted HTTPS evidence contains symlink %s", path)
		}
		if entry.IsDir() {
			if path != root {
				return fmt.Errorf("trusted HTTPS evidence contains nested directory %s", path)
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve trusted HTTPS evidence file: %w", err)
		}
		if !slices.Contains(trustedHTTPSEvidenceFiles, relative) {
			return fmt.Errorf("trusted HTTPS evidence contains unexpected file %s", path)
		}
		observed = append(observed, relative)
		return nil
	})
	if err != nil {
		return fmt.Errorf("collect trusted HTTPS evidence: %w", err)
	}
	slices.Sort(observed)
	if !slices.Equal(observed, trustedHTTPSEvidenceFiles) {
		return fmt.Errorf("trusted HTTPS evidence files = %v, want %v", observed, trustedHTTPSEvidenceFiles)
	}

	var summary TrustedHTTPSLifecycleSummary
	if err := decodeEvidence(filepath.Join(root, "summary.json"), &summary); err != nil {
		return err
	}
	if summary.SchemaVersion != TrustedHTTPSLifecycleEvidenceSchemaVersion {
		return fmt.Errorf("trusted HTTPS evidence has unsupported schema version %d", summary.SchemaVersion)
	}
	if summary.OperatingSystem != requirement.Platform {
		return fmt.Errorf("trusted HTTPS evidence reports platform %q instead of %q", summary.OperatingSystem, requirement.Platform)
	}
	if !slices.Equal(summary.Checks, requiredTrustedHTTPSChecks) {
		return fmt.Errorf("trusted HTTPS evidence checks = %v, want %v", summary.Checks, requiredTrustedHTTPSChecks)
	}

	for _, name := range trustedHTTPSEvidenceFiles[:len(trustedHTTPSEvidenceFiles)-1] {
		path := filepath.Join(root, name)
		information, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect trusted HTTPS log %s: %w", name, err)
		}
		if !information.Mode().IsRegular() || information.Size() > maximumTrustedHTTPSLogBytes {
			return fmt.Errorf("trusted HTTPS log %s has unsupported type or size", name)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read trusted HTTPS log %s: %w", name, err)
		}
		if bytes.Contains(contents, []byte("WARNING: DATA RACE")) {
			return fmt.Errorf("trusted HTTPS log %s contains a Go data race report", name)
		}
	}
	return nil
}
