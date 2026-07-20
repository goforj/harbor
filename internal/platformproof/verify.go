package platformproof

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	maximumEvidenceBytes   = 1 << 20
	sha256DigestByteLength = 32
	sha256DigestHexLength  = sha256DigestByteLength * 2
)

var requiredProjectIdentityAssertions = []string{
	"network.loopback.explicit_assignment",
	"network.loopback.distinct_identities",
	"network.loopback.same_native_port",
	"network.loopback.distinct_payloads",
	"network.loopback.duplicate_rejected",
}

// EvidenceRequirement defines the exact platform-proof evidence needed by a workflow gate.
type EvidenceRequirement struct {
	Commit                string
	Platforms             []string
	Port                  uint16
	RequireRunnerIdentity bool
}

// VerifyEvidenceDirectory proves that every required platform emitted matching proof and cleanup evidence.
func VerifyEvidenceDirectory(root string, requirement EvidenceRequirement) error {
	if root == "" {
		return errors.New("evidence root is required")
	}
	if len(requirement.Platforms) == 0 {
		return errors.New("at least one required platform is required")
	}
	if requirement.Port == 0 {
		return errors.New("required native port must be non-zero")
	}

	proofs, cleanups, err := collectEvidence(root)
	if err != nil {
		return err
	}
	for _, platform := range requirement.Platforms {
		proof, exists := proofs[platform]
		if !exists {
			return fmt.Errorf("missing project identity evidence for %s", platform)
		}
		cleanup, exists := cleanups[platform]
		if !exists {
			return fmt.Errorf("missing cleanup evidence for %s", platform)
		}
		if err := verifyProjectIdentityEvidence(proof, requirement, platform); err != nil {
			return err
		}
		if err := verifyCleanupEvidence(cleanup, proof, requirement, platform); err != nil {
			return err
		}
	}
	if len(proofs) != len(requirement.Platforms) || len(cleanups) != len(requirement.Platforms) {
		return fmt.Errorf("evidence contains unexpected platform results: %d proofs and %d cleanups", len(proofs), len(cleanups))
	}
	return nil
}

// collectEvidence reads only the two versioned capability documents emitted by each platform job.
func collectEvidence(root string) (map[string]ProjectIdentityEvidence, map[string]CleanupEvidence, error) {
	proofs := make(map[string]ProjectIdentityEvidence)
	cleanups := make(map[string]CleanupEvidence)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case "project-identity.json":
			var evidence ProjectIdentityEvidence
			if err := decodeEvidence(path, &evidence); err != nil {
				return err
			}
			if evidence.Runtime.GOOS == "" {
				return fmt.Errorf("project identity evidence %s has no platform", path)
			}
			if _, exists := proofs[evidence.Runtime.GOOS]; exists {
				return fmt.Errorf("duplicate project identity evidence for %s", evidence.Runtime.GOOS)
			}
			proofs[evidence.Runtime.GOOS] = evidence
		case "cleanup.json":
			var evidence CleanupEvidence
			if err := decodeEvidence(path, &evidence); err != nil {
				return err
			}
			if evidence.Runtime.GOOS == "" {
				return fmt.Errorf("cleanup evidence %s has no platform", path)
			}
			if _, exists := cleanups[evidence.Runtime.GOOS]; exists {
				return fmt.Errorf("duplicate cleanup evidence for %s", evidence.Runtime.GOOS)
			}
			cleanups[evidence.Runtime.GOOS] = evidence
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("collect platform evidence: %w", err)
	}
	return proofs, cleanups, nil
}

// decodeEvidence bounds file size so a malformed artifact cannot exhaust the verifier.
func decodeEvidence(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open evidence %s: %w", path, err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maximumEvidenceBytes+1))
	if err != nil {
		return fmt.Errorf("read evidence %s: %w", path, err)
	}
	if len(content) > maximumEvidenceBytes {
		return fmt.Errorf("decode evidence %s: evidence exceeds one mebibyte", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode evidence %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode evidence %s: multiple JSON documents", path)
		}
		return fmt.Errorf("decode evidence %s: trailing content: %w", path, err)
	}
	return nil
}

// verifyProjectIdentityEvidence validates the exact same-port proof rather than trusting file presence.
func verifyProjectIdentityEvidence(evidence ProjectIdentityEvidence, requirement EvidenceRequirement, platform string) error {
	if evidence.SchemaVersion != EvidenceSchemaVersion || evidence.Capability != "project_loopback_identity" || evidence.Scope != hostNetworkAPISmokeScope {
		return fmt.Errorf("%s project identity evidence has unsupported schema or capability", platform)
	}
	if err := verifyRuntimeEvidence(evidence.Runtime, requirement, platform); err != nil {
		return err
	}
	if evidence.Port != requirement.Port {
		return fmt.Errorf("%s proved port %d instead of required port %d", platform, evidence.Port, requirement.Port)
	}
	if len(evidence.Identities) != 3 {
		return fmt.Errorf("%s did not prove three distinct identities", platform)
	}
	addresses := make(map[netip.Addr]struct{}, len(evidence.Identities))
	digests := make(map[string]struct{}, len(evidence.Identities))
	interfaceName := ""
	interfaceIndex := 0
	for index, identity := range evidence.Identities {
		address, digest, err := verifyIdentityEvidence(identity, evidence.Port, platform)
		if err != nil {
			return err
		}
		if _, exists := addresses[address]; exists {
			return fmt.Errorf("%s did not prove three distinct identities", platform)
		}
		addresses[address] = struct{}{}
		if _, exists := digests[digest]; exists {
			return fmt.Errorf("%s identities reported the same payload digest", platform)
		}
		digests[digest] = struct{}{}
		if index == 0 {
			interfaceName = identity.InterfaceName
			interfaceIndex = identity.InterfaceIndex
			continue
		}
		if identity.InterfaceName != interfaceName || identity.InterfaceIndex != interfaceIndex {
			return fmt.Errorf("%s identities were not observed on the same provisioned loopback interface", platform)
		}
	}
	if err := verifyHostedLoopbackInterfaceName(interfaceName, platform); err != nil {
		return err
	}
	return verifyAssertions(evidence.Assertions, requiredProjectIdentityAssertions, platform)
}

// verifyHostedLoopbackInterfaceName binds hosted smoke evidence to each workflow's provisioned interface.
func verifyHostedLoopbackInterfaceName(name string, platform string) error {
	valid := false
	switch platform {
	case "linux":
		valid = name == "lo"
	case "darwin":
		valid = name == "lo0"
	case "windows":
		valid = strings.Contains(strings.ToLower(name), "loopback")
	}
	if !valid {
		return fmt.Errorf("%s identity evidence reports unexpected hosted loopback interface %q", platform, name)
	}
	return nil
}

// verifyIdentityEvidence independently validates the address, endpoint, assignment, and payload digest shape.
func verifyIdentityEvidence(identity IdentityEvidence, port uint16, platform string) (netip.Addr, string, error) {
	address, err := netip.ParseAddr(identity.Address)
	if err != nil || !address.Is4() || !address.IsLoopback() || address.String() != identity.Address {
		return netip.Addr{}, "", fmt.Errorf("%s identity address %q is not a canonical IPv4 loopback", platform, identity.Address)
	}
	expectedEndpoint := net.JoinHostPort(address.String(), strconv.Itoa(int(port)))
	if identity.Endpoint != expectedEndpoint {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s reports endpoint %q instead of %q", platform, address, identity.Endpoint, expectedEndpoint)
	}
	if identity.InterfaceName == "" || strings.TrimSpace(identity.InterfaceName) != identity.InterfaceName || identity.InterfaceIndex <= 0 {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s has incomplete interface identity", platform, address)
	}
	if !identity.InterfaceLoopback {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s was not observed on a loopback interface", platform, address)
	}
	if identity.PrefixLength != 32 {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s reports prefix length %d instead of 32", platform, address, identity.PrefixLength)
	}
	if len(identity.PayloadDigest) != sha256DigestHexLength || strings.ToLower(identity.PayloadDigest) != identity.PayloadDigest {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s has a non-canonical SHA-256 payload digest", platform, address)
	}
	digest, err := hex.DecodeString(identity.PayloadDigest)
	if err != nil || len(digest) != sha256DigestByteLength {
		return netip.Addr{}, "", fmt.Errorf("%s identity %s has an invalid SHA-256 payload digest", platform, address)
	}
	return address, identity.PayloadDigest, nil
}

// verifyCleanupEvidence binds cleanup to the exact identities and commit proved by the same platform.
func verifyCleanupEvidence(cleanup CleanupEvidence, proof ProjectIdentityEvidence, requirement EvidenceRequirement, platform string) error {
	if cleanup.SchemaVersion != EvidenceSchemaVersion || cleanup.Capability != "project_loopback_identity_cleanup" || cleanup.Scope != hostNetworkAPISmokeScope {
		return fmt.Errorf("%s cleanup evidence has unsupported schema or capability", platform)
	}
	if err := verifyRuntimeEvidence(cleanup.Runtime, requirement, platform); err != nil {
		return err
	}
	expectedAddresses := make([]string, 0, len(proof.Identities))
	for _, identity := range proof.Identities {
		expectedAddresses = append(expectedAddresses, identity.Address)
	}
	slices.Sort(expectedAddresses)
	actualAddresses := slices.Clone(cleanup.Addresses)
	slices.Sort(actualAddresses)
	if !slices.Equal(expectedAddresses, actualAddresses) {
		return fmt.Errorf("%s cleanup addresses do not match the proved identities", platform)
	}
	return verifyAssertions(cleanup.Assertions, []string{"network.loopback.explicit_cleanup"}, platform)
}

// verifyRuntimeEvidence prevents artifacts from another commit or platform from satisfying the gate.
func verifyRuntimeEvidence(runtime RuntimeEvidence, requirement EvidenceRequirement, platform string) error {
	if runtime.GOOS != platform {
		return fmt.Errorf("%s evidence reports platform %s", platform, runtime.GOOS)
	}
	if requirement.Commit != "" && runtime.Commit != requirement.Commit {
		return fmt.Errorf("%s evidence reports commit %q instead of %q", platform, runtime.Commit, requirement.Commit)
	}
	if runtime.GOARCH == "" {
		return fmt.Errorf("%s evidence has no architecture", platform)
	}
	if requirement.RequireRunnerIdentity && (runtime.RunnerName == "" || runtime.RunnerImage == "" || runtime.RunnerImageVersion == "") {
		return fmt.Errorf("%s evidence is missing runner image identity", platform)
	}
	return nil
}

// verifyAssertions rejects skipped, failed, duplicated, missing, and unexpected assertion IDs.
func verifyAssertions(assertions []AssertionEvidence, required []string, platform string) error {
	seen := make(map[string]struct{}, len(assertions))
	for _, assertion := range assertions {
		if assertion.ID == "" || strings.TrimSpace(assertion.Detail) == "" {
			return fmt.Errorf("%s reported incomplete assertion evidence", platform)
		}
		if _, exists := seen[assertion.ID]; exists {
			return fmt.Errorf("%s duplicated assertion %s", platform, assertion.ID)
		}
		seen[assertion.ID] = struct{}{}
		if !assertion.Passed {
			return fmt.Errorf("%s failed assertion %s", platform, assertion.ID)
		}
		if !slices.Contains(required, assertion.ID) {
			return fmt.Errorf("%s reported unexpected assertion %s", platform, assertion.ID)
		}
	}
	for _, assertionID := range required {
		if _, exists := seen[assertionID]; !exists {
			return fmt.Errorf("%s is missing assertion %s", platform, assertionID)
		}
	}
	return nil
}
