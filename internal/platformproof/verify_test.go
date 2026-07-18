package platformproof

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyEvidenceDirectory accepts one complete proof and cleanup pair per required platform.
func TestVerifyEvidenceDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	platforms := []string{"linux", "darwin", "windows"}
	for _, platform := range platforms {
		writeEvidenceFixture(t, root, validProjectIdentityFixture(platform), validCleanupFixture(platform))
	}
	if err := VerifyEvidenceDirectory(root, EvidenceRequirement{Commit: "abc123", Platforms: platforms, Port: 3306, RequireRunnerIdentity: true}); err != nil {
		t.Fatalf("verify evidence: %v", err)
	}
}

// TestVerifyEvidenceDirectoryRejectsIncompleteEvidence exercises the final gate's fail-closed behavior.
func TestVerifyEvidenceDirectoryRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProjectIdentityEvidence, *CleanupEvidence)
		want   string
	}{
		{name: "wrong commit", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Runtime.Commit = "other" }, want: "instead of"},
		{name: "translated port", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Port = 13306 }, want: "instead of required port"},
		{name: "failed assertion", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Assertions[0].Passed = false }, want: "failed assertion"},
		{name: "missing assertion", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Assertions = proof.Assertions[1:] }, want: "missing assertion"},
		{name: "mismatched cleanup", mutate: func(_ *ProjectIdentityEvidence, cleanup *CleanupEvidence) { cleanup.Addresses[1] = "127.77.254.12" }, want: "do not match"},
		{name: "missing runner identity", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Runtime.RunnerName = "" }, want: "runner image identity"},
		{name: "unsupported scope", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Scope = "product_end_to_end" }, want: "unsupported"},
		{name: "non-loopback address", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].Address = "192.0.2.10" }, want: "canonical IPv4 loopback"},
		{name: "IPv6 address", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].Address = "::1" }, want: "canonical IPv4 loopback"},
		{name: "non-canonical address", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].Address = "127.077.254.10"
		}, want: "canonical IPv4 loopback"},
		{name: "duplicate address", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[1].Address = proof.Identities[0].Address
			proof.Identities[1].Endpoint = proof.Identities[0].Endpoint
		}, want: "two distinct identities"},
		{name: "wrong endpoint", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].Endpoint = "127.77.254.10:13306"
		}, want: "reports endpoint"},
		{name: "missing interface name", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].InterfaceName = "" }, want: "incomplete interface identity"},
		{name: "non-canonical interface name", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].InterfaceName = " lo" }, want: "incomplete interface identity"},
		{name: "missing interface index", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].InterfaceIndex = 0 }, want: "incomplete interface identity"},
		{name: "non-loopback interface", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].InterfaceLoopback = false
		}, want: "not observed on a loopback"},
		{name: "mismatched interface", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[1].InterfaceIndex = 2
		}, want: "same provisioned loopback interface"},
		{name: "unexpected hosted interface", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].InterfaceName = "loopback0"
			proof.Identities[1].InterfaceName = "loopback0"
		}, want: "unexpected hosted loopback interface"},
		{name: "broad interface prefix", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].PrefixLength = 8 }, want: "instead of 32"},
		{name: "short payload digest", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Identities[0].PayloadDigest = "abc" }, want: "non-canonical SHA-256"},
		{name: "uppercase payload digest", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].PayloadDigest = strings.Repeat("A", 64)
		}, want: "non-canonical SHA-256"},
		{name: "non-hex payload digest", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[0].PayloadDigest = strings.Repeat("g", 64)
		}, want: "invalid SHA-256"},
		{name: "duplicate payload digest", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Identities[1].PayloadDigest = proof.Identities[0].PayloadDigest
		}, want: "same payload digest"},
		{name: "blank assertion detail", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Assertions[0].Detail = "" }, want: "incomplete assertion evidence"},
		{name: "duplicate assertion", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) { proof.Assertions[1] = proof.Assertions[0] }, want: "duplicated assertion"},
		{name: "unexpected assertion", mutate: func(proof *ProjectIdentityEvidence, _ *CleanupEvidence) {
			proof.Assertions[0].ID = "network.loopback.unexpected"
		}, want: "unexpected assertion"},
		{name: "unsupported cleanup scope", mutate: func(_ *ProjectIdentityEvidence, cleanup *CleanupEvidence) { cleanup.Scope = "product_end_to_end" }, want: "cleanup evidence has unsupported"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			proof := validProjectIdentityFixture("linux")
			cleanup := validCleanupFixture("linux")
			test.mutate(&proof, &cleanup)
			writeEvidenceFixture(t, root, proof, cleanup)
			err := VerifyEvidenceDirectory(root, EvidenceRequirement{Commit: "abc123", Platforms: []string{"linux"}, Port: 3306, RequireRunnerIdentity: true})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

// TestVerifyHostedLoopbackInterfaceName recognizes only interfaces provisioned by the hosted workflow.
func TestVerifyHostedLoopbackInterfaceName(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		platform string
		name     string
	}{
		{platform: "linux", name: "lo"},
		{platform: "darwin", name: "lo0"},
		{platform: "windows", name: "Loopback Pseudo-Interface 1"},
	} {
		if err := verifyHostedLoopbackInterfaceName(test.name, test.platform); err != nil {
			t.Fatalf("verify %s interface: %v", test.platform, err)
		}
	}
	if err := verifyHostedLoopbackInterfaceName("lo", "plan9"); err == nil {
		t.Fatal("expected unsupported hosted platform to fail")
	}
}

// TestDecodeEvidenceRejectsUntrustedDocuments proves artifact parsing is bounded, strict, and single-document.
func TestDecodeEvidenceRejectsUntrustedDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "unknown field", content: []byte(`{"schema_version":1,"unknown":true}`), want: "unknown field"},
		{name: "multiple documents", content: []byte(`{} {}`), want: "multiple JSON documents"},
		{name: "trailing content", content: []byte(`{} !`), want: "trailing content"},
		{name: "oversized", content: bytes.Repeat([]byte(" "), maximumEvidenceBytes+1), want: "exceeds one mebibyte"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "evidence.json")
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatalf("write evidence: %v", err)
			}
			var evidence ProjectIdentityEvidence
			err := decodeEvidence(path, &evidence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

// validProjectIdentityFixture builds a complete proof document for verifier tests.
func validProjectIdentityFixture(platform string) ProjectIdentityEvidence {
	interfaceName := hostedInterfaceFixtureName(platform)
	return ProjectIdentityEvidence{
		SchemaVersion: EvidenceSchemaVersion,
		Capability:    "project_loopback_identity",
		Scope:         hostNetworkAPISmokeScope,
		Runtime: RuntimeEvidence{
			GOOS:               platform,
			GOARCH:             "amd64",
			Commit:             "abc123",
			RunnerName:         "runner",
			RunnerImage:        "image",
			RunnerImageVersion: "version",
		},
		Port: 3306,
		Identities: []IdentityEvidence{
			{Address: "127.77.254.10", Endpoint: "127.77.254.10:3306", PayloadDigest: strings.Repeat("a", 64), InterfaceName: interfaceName, InterfaceIndex: 1, InterfaceLoopback: true, PrefixLength: 32},
			{Address: "127.77.254.11", Endpoint: "127.77.254.11:3306", PayloadDigest: strings.Repeat("b", 64), InterfaceName: interfaceName, InterfaceIndex: 1, InterfaceLoopback: true, PrefixLength: 32},
		},
		Assertions: []AssertionEvidence{
			{ID: "network.loopback.explicit_assignment", Passed: true, Detail: "assigned"},
			{ID: "network.loopback.distinct_identities", Passed: true, Detail: "distinct"},
			{ID: "network.loopback.same_native_port", Passed: true, Detail: "same port"},
			{ID: "network.loopback.distinct_payloads", Passed: true, Detail: "distinct payloads"},
			{ID: "network.loopback.duplicate_rejected", Passed: true, Detail: "duplicate rejected"},
		},
	}
}

// hostedInterfaceFixtureName returns the stable interface provisioned for each hosted test platform.
func hostedInterfaceFixtureName(platform string) string {
	switch platform {
	case "linux":
		return "lo"
	case "darwin":
		return "lo0"
	case "windows":
		return "Loopback Pseudo-Interface 1"
	default:
		return "unsupported"
	}
}

// validCleanupFixture builds cleanup evidence bound to the verifier's project identities.
func validCleanupFixture(platform string) CleanupEvidence {
	return CleanupEvidence{
		SchemaVersion: EvidenceSchemaVersion,
		Capability:    "project_loopback_identity_cleanup",
		Scope:         hostNetworkAPISmokeScope,
		Runtime: RuntimeEvidence{
			GOOS:               platform,
			GOARCH:             "amd64",
			Commit:             "abc123",
			RunnerName:         "runner",
			RunnerImage:        "image",
			RunnerImageVersion: "version",
		},
		Addresses:  []string{"127.77.254.10", "127.77.254.11"},
		Assertions: []AssertionEvidence{{ID: "network.loopback.explicit_cleanup", Passed: true, Detail: "absent"}},
	}
}

// writeEvidenceFixture writes the same artifact names consumed by the workflow verifier.
func writeEvidenceFixture(t *testing.T, root string, proof ProjectIdentityEvidence, cleanup CleanupEvidence) {
	t.Helper()
	directory := filepath.Join(root, proof.Runtime.GOOS)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	for name, value := range map[string]any{"project-identity.json": proof, "cleanup.json": cleanup} {
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
