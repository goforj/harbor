// Package productproof verifies evidence emitted only by Harbor's protected product-worker workflows.
package productproof

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	// DockerProjectEvidenceSchemaVersion identifies the generated Docker-project lifecycle evidence schema.
	DockerProjectEvidenceSchemaVersion = 2
	maximumEvidenceBytes               = 1 << 20
	dockerProjectCapability            = "docker_project_lifecycle"
	dockerProjectCleanupCapability     = "docker_project_lifecycle_cleanup"
	dockerProjectScope                 = "product_end_to_end"
	minimumDockerEngineMajor           = 28
	maximumEngineVersionBytes          = 128
)

var requiredDockerProjectAssertions = []string{
	"docker.projects.generated",
	"docker.projects.isolated",
	"docker.adapter.read_only",
	"docker.logs.available",
	"docker.events.refresh",
	"docker.projects.stop_peer_survival",
	"docker.projects.restart",
}

// RuntimeEvidence identifies the exact product worker that executed a native lifecycle proof.
type RuntimeEvidence struct {
	GOOS               string `json:"goos"`
	GOARCH             string `json:"goarch"`
	Commit             string `json:"commit"`
	RunnerName         string `json:"runner_name"`
	RunnerImage        string `json:"runner_image"`
	RunnerImageVersion string `json:"runner_image_version"`
}

// DependencyEvidence binds the native proof to a pinned dependency identity without retaining paths or credentials.
type DependencyEvidence struct {
	GoForjVersion string `json:"goforj_version"`
	GoForjDigest  string `json:"goforj_digest"`
	EngineKind    string `json:"engine_kind"`
	EngineVersion string `json:"engine_version"`
}

// AssertionEvidence records one non-skipped product invariant.
type AssertionEvidence struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// ProjectEvidence records one generated project and its exact native ports and admitted container identities.
type ProjectEvidence struct {
	ID           string   `json:"id"`
	Address      string   `json:"address"`
	AppPort      uint16   `json:"app_port"`
	ServicePort  uint16   `json:"service_port"`
	ContainerIDs []string `json:"container_ids"`
}

// EventRefreshEvidence records the before-and-after identities that prove one host event refreshed a project projection.
type EventRefreshEvidence struct {
	TargetProjectID    string                     `json:"target_project_id"`
	TargetServiceID    string                     `json:"target_service_id"`
	BeforeRevision     uint64                     `json:"before_revision"`
	AfterRevision      uint64                     `json:"after_revision"`
	BeforeContainerIDs []string                   `json:"before_container_ids"`
	AfterContainerIDs  []string                   `json:"after_container_ids"`
	Peers              []EventRefreshPeerEvidence `json:"peers"`
}

// EventRefreshPeerEvidence records one neighboring project's identities across an unrelated target replacement.
type EventRefreshPeerEvidence struct {
	ProjectID          string   `json:"project_id"`
	BeforeContainerIDs []string `json:"before_container_ids"`
	AfterContainerIDs  []string `json:"after_container_ids"`
}

// DockerProjectEvidence is the bounded native product result for three generated GoForj projects.
type DockerProjectEvidence struct {
	SchemaVersion   int                  `json:"schema_version"`
	Capability      string               `json:"capability"`
	Scope           string               `json:"scope"`
	Runtime         RuntimeEvidence      `json:"runtime"`
	Dependencies    DependencyEvidence   `json:"dependencies"`
	Projects        []ProjectEvidence    `json:"projects"`
	EventRefresh    EventRefreshEvidence `json:"event_refresh"`
	Assertions      []AssertionEvidence  `json:"assertions"`
	ArtifactDigests []string             `json:"artifact_digests"`
}

// DockerCleanupEvidence proves the product worker removed only the exact namespace it created.
type DockerCleanupEvidence struct {
	SchemaVersion   int                 `json:"schema_version"`
	Capability      string              `json:"capability"`
	Scope           string              `json:"scope"`
	Runtime         RuntimeEvidence     `json:"runtime"`
	ProjectIDs      []string            `json:"project_ids"`
	Assertions      []AssertionEvidence `json:"assertions"`
	ArtifactDigests []string            `json:"artifact_digests"`
}

// DockerProjectRequirement identifies the native evidence required by one protected workflow gate.
type DockerProjectRequirement struct {
	Commit      string
	Platforms   []string
	AppPort     uint16
	ServicePort uint16
}

// WriteDockerProjectEvidence writes one exclusive lifecycle and cleanup manifest into an empty direct evidence directory.
func WriteDockerProjectEvidence(directory string, lifecycle DockerProjectEvidence, cleanup DockerCleanupEvidence) error {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return fmt.Errorf("evidence directory %q must be an absolute clean path", directory)
	}
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect evidence directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("evidence directory %q is not a direct directory", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read evidence directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("evidence directory %q is not empty", directory)
	}
	if err := writeEvidenceFile(directory, "docker-projects.json", lifecycle); err != nil {
		return err
	}
	if err := writeEvidenceFile(directory, "docker-cleanup.json", cleanup); err != nil {
		return err
	}
	return nil
}

// writeEvidenceFile serializes one fixed manifest without following an existing artifact path.
func writeEvidenceFile(directory, name string, evidence any) (writeErr error) {
	contents, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evidence %q: %w", name, err)
	}
	contents = append(contents, '\n')
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create evidence %q: %w", name, err)
	}
	defer func() {
		writeErr = errors.Join(writeErr, file.Close())
	}()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write evidence %q: %w", name, err)
	}
	return nil
}

// VerifyDockerProjectEvidenceDirectory verifies exactly one lifecycle and cleanup manifest for every required platform.
func VerifyDockerProjectEvidenceDirectory(root string, requirement DockerProjectRequirement) error {
	if root == "" {
		return errors.New("evidence root is required")
	}
	if len(requirement.Platforms) == 0 {
		return errors.New("at least one required platform is required")
	}
	if requirement.AppPort == 0 || requirement.ServicePort == 0 {
		return errors.New("required app and service ports must be non-zero")
	}
	lifecycles, cleanups, err := collectDockerProjectEvidence(root)
	if err != nil {
		return err
	}
	for _, platform := range requirement.Platforms {
		lifecycle, exists := lifecycles[platform]
		if !exists {
			return fmt.Errorf("missing Docker project lifecycle evidence for %s", platform)
		}
		cleanup, exists := cleanups[platform]
		if !exists {
			return fmt.Errorf("missing Docker project cleanup evidence for %s", platform)
		}
		if err := verifyDockerProjectLifecycle(lifecycle, requirement, platform); err != nil {
			return err
		}
		if err := verifyDockerProjectCleanup(cleanup, lifecycle, requirement, platform); err != nil {
			return err
		}
	}
	if len(lifecycles) != len(requirement.Platforms) || len(cleanups) != len(requirement.Platforms) {
		return fmt.Errorf("evidence contains unexpected platform results: %d lifecycles and %d cleanups", len(lifecycles), len(cleanups))
	}
	return nil
}

// collectDockerProjectEvidence admits only the two fixed manifests expected from every product worker.
func collectDockerProjectEvidence(root string) (map[string]DockerProjectEvidence, map[string]DockerCleanupEvidence, error) {
	lifecycles := make(map[string]DockerProjectEvidence)
	cleanups := make(map[string]DockerCleanupEvidence)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case "docker-projects.json":
			var evidence DockerProjectEvidence
			if err := decodeEvidence(path, &evidence); err != nil {
				return err
			}
			if evidence.Runtime.GOOS == "" {
				return fmt.Errorf("Docker project lifecycle evidence %s has no platform", path)
			}
			if _, exists := lifecycles[evidence.Runtime.GOOS]; exists {
				return fmt.Errorf("duplicate Docker project lifecycle evidence for %s", evidence.Runtime.GOOS)
			}
			lifecycles[evidence.Runtime.GOOS] = evidence
		case "docker-cleanup.json":
			var evidence DockerCleanupEvidence
			if err := decodeEvidence(path, &evidence); err != nil {
				return err
			}
			if evidence.Runtime.GOOS == "" {
				return fmt.Errorf("Docker project cleanup evidence %s has no platform", path)
			}
			if _, exists := cleanups[evidence.Runtime.GOOS]; exists {
				return fmt.Errorf("duplicate Docker project cleanup evidence for %s", evidence.Runtime.GOOS)
			}
			cleanups[evidence.Runtime.GOOS] = evidence
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("collect Docker project evidence: %w", err)
	}
	return lifecycles, cleanups, nil
}

// verifyDockerProjectLifecycle refuses to infer product success from an artifact's presence.
func verifyDockerProjectLifecycle(evidence DockerProjectEvidence, requirement DockerProjectRequirement, platform string) error {
	if evidence.SchemaVersion != DockerProjectEvidenceSchemaVersion || evidence.Capability != dockerProjectCapability || evidence.Scope != dockerProjectScope {
		return fmt.Errorf("%s Docker project lifecycle evidence has unsupported schema or capability", platform)
	}
	if err := verifyRuntime(evidence.Runtime, requirement.Commit, platform); err != nil {
		return err
	}
	if err := verifyDependencies(evidence.Dependencies, platform); err != nil {
		return err
	}
	if err := verifyProjects(evidence.Projects, requirement, platform); err != nil {
		return err
	}
	if err := verifyEventRefresh(evidence.EventRefresh, evidence.Projects, platform); err != nil {
		return err
	}
	if err := verifyAssertions(evidence.Assertions, requiredDockerProjectAssertions, platform); err != nil {
		return err
	}
	return verifyDigests(evidence.ArtifactDigests, platform)
}

// verifyDockerProjectCleanup binds exact cleanup ownership to the matching lifecycle record.
func verifyDockerProjectCleanup(cleanup DockerCleanupEvidence, lifecycle DockerProjectEvidence, requirement DockerProjectRequirement, platform string) error {
	if cleanup.SchemaVersion != DockerProjectEvidenceSchemaVersion || cleanup.Capability != dockerProjectCleanupCapability || cleanup.Scope != dockerProjectScope {
		return fmt.Errorf("%s Docker project cleanup evidence has unsupported schema or capability", platform)
	}
	if err := verifyRuntime(cleanup.Runtime, requirement.Commit, platform); err != nil {
		return err
	}
	expected := make([]string, 0, len(lifecycle.Projects))
	for _, project := range lifecycle.Projects {
		expected = append(expected, project.ID)
	}
	actual := slices.Clone(cleanup.ProjectIDs)
	slices.Sort(expected)
	slices.Sort(actual)
	if !slices.Equal(expected, actual) {
		return fmt.Errorf("%s Docker cleanup project IDs do not match lifecycle evidence", platform)
	}
	if err := verifyAssertions(cleanup.Assertions, []string{"docker.cleanup.exact"}, platform); err != nil {
		return err
	}
	return verifyDigests(cleanup.ArtifactDigests, platform)
}

// verifyRuntime ensures a product artifact cannot be replayed from another commit, platform, or unidentifiable worker.
func verifyRuntime(runtime RuntimeEvidence, commit, platform string) error {
	if runtime.GOOS != platform {
		return fmt.Errorf("%s evidence reports platform %s", platform, runtime.GOOS)
	}
	if runtime.GOARCH == "" || runtime.RunnerName == "" || runtime.RunnerImage == "" || runtime.RunnerImageVersion == "" {
		return fmt.Errorf("%s evidence is missing runner identity", platform)
	}
	if commit != "" && runtime.Commit != commit {
		return fmt.Errorf("%s evidence reports commit %q instead of %q", platform, runtime.Commit, commit)
	}
	return nil
}

// verifyDependencies requires pinned GoForj and Engine/Desktop facts before a generated project claim is admitted.
func verifyDependencies(dependencies DependencyEvidence, platform string) error {
	if dependencies.GoForjVersion == "" || dependencies.EngineKind == "" || dependencies.EngineVersion == "" {
		return fmt.Errorf("%s evidence has incomplete dependency identity", platform)
	}
	if dependencies.EngineKind != "docker-engine" && dependencies.EngineKind != "docker-desktop" {
		return fmt.Errorf("%s evidence has unsupported Docker engine kind %q", platform, dependencies.EngineKind)
	}
	if err := verifyDockerEngineVersion(dependencies.EngineVersion, platform); err != nil {
		return err
	}
	return verifyDigests([]string{dependencies.GoForjDigest}, platform)
}

// verifyDockerEngineVersion enforces the supported Engine 28-equivalent floor before accepting lifecycle evidence.
func verifyDockerEngineVersion(version, platform string) error {
	if len(version) == 0 || len(version) > maximumEngineVersionBytes {
		return fmt.Errorf("%s evidence has an invalid Docker engine version", platform)
	}
	normalized := strings.TrimPrefix(version, "v")
	parts := strings.Split(normalized, ".")
	if len(parts) < 3 {
		return fmt.Errorf("%s evidence has an invalid Docker engine version %q", platform, version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return fmt.Errorf("%s evidence has an invalid Docker engine version %q", platform, version)
	}
	for _, part := range parts[:3] {
		if part == "" {
			return fmt.Errorf("%s evidence has an invalid Docker engine version %q", platform, version)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return fmt.Errorf("%s evidence has an invalid Docker engine version %q", platform, version)
			}
		}
	}
	if major < minimumDockerEngineMajor {
		return fmt.Errorf("%s evidence uses Docker engine %q below the supported major version %d", platform, version, minimumDockerEngineMajor)
	}
	return nil
}

// verifyProjects requires exactly three distinct loopback identities and independently owned admitted containers.
func verifyProjects(projects []ProjectEvidence, requirement DockerProjectRequirement, platform string) error {
	if len(projects) != 3 {
		return fmt.Errorf("%s did not prove three generated projects", platform)
	}
	projectIDs := make(map[string]struct{}, len(projects))
	addresses := make(map[netip.Addr]struct{}, len(projects))
	containers := make(map[string]struct{})
	for _, project := range projects {
		if project.ID == "" || strings.TrimSpace(project.ID) != project.ID {
			return fmt.Errorf("%s evidence has an invalid project ID", platform)
		}
		if _, exists := projectIDs[project.ID]; exists {
			return fmt.Errorf("%s evidence has duplicate project ID %q", platform, project.ID)
		}
		projectIDs[project.ID] = struct{}{}
		address, err := netip.ParseAddr(project.Address)
		if err != nil || !address.Is4() || !address.IsLoopback() || address.String() != project.Address {
			return fmt.Errorf("%s evidence has non-canonical project loopback address %q", platform, project.Address)
		}
		if _, exists := addresses[address]; exists {
			return fmt.Errorf("%s evidence has duplicate project loopback address %s", platform, address)
		}
		addresses[address] = struct{}{}
		if project.AppPort != requirement.AppPort || project.ServicePort != requirement.ServicePort {
			return fmt.Errorf("%s project %q ports are %d/%d instead of required %d/%d", platform, project.ID, project.AppPort, project.ServicePort, requirement.AppPort, requirement.ServicePort)
		}
		if len(project.ContainerIDs) == 0 {
			return fmt.Errorf("%s project %q has no admitted containers", platform, project.ID)
		}
		for _, container := range project.ContainerIDs {
			if container == "" || strings.TrimSpace(container) != container {
				return fmt.Errorf("%s project %q has an invalid container ID", platform, project.ID)
			}
			if _, exists := containers[container]; exists {
				return fmt.Errorf("%s evidence admits container %q to multiple projects", platform, container)
			}
			containers[container] = struct{}{}
		}
	}
	return nil
}

// verifyEventRefresh requires a replacement target, advancing projection revision, and unchanged peer ownership.
func verifyEventRefresh(refresh EventRefreshEvidence, projects []ProjectEvidence, platform string) error {
	projectIDs := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		projectIDs[project.ID] = struct{}{}
	}
	if refresh.TargetProjectID == "" || strings.TrimSpace(refresh.TargetProjectID) != refresh.TargetProjectID {
		return fmt.Errorf("%s event-refresh evidence has an invalid target project ID", platform)
	}
	if _, exists := projectIDs[refresh.TargetProjectID]; !exists {
		return fmt.Errorf("%s event-refresh evidence names unknown target project %q", platform, refresh.TargetProjectID)
	}
	if refresh.TargetServiceID == "" || strings.TrimSpace(refresh.TargetServiceID) != refresh.TargetServiceID {
		return fmt.Errorf("%s event-refresh evidence has an invalid target service ID", platform)
	}
	if refresh.BeforeRevision == 0 || refresh.AfterRevision <= refresh.BeforeRevision {
		return fmt.Errorf("%s event-refresh project revision did not advance from %d to %d", platform, refresh.BeforeRevision, refresh.AfterRevision)
	}
	before, err := normalizeEventRefreshContainerIDs(refresh.BeforeContainerIDs, "target before", platform)
	if err != nil {
		return err
	}
	after, err := normalizeEventRefreshContainerIDs(refresh.AfterContainerIDs, "target after", platform)
	if err != nil {
		return err
	}
	if containsSharedContainer(before, after) {
		return fmt.Errorf("%s event-refresh target retained a shared container identity across replacement", platform)
	}
	if len(refresh.Peers) != len(projects)-1 {
		return fmt.Errorf("%s event-refresh evidence has %d peers, want %d", platform, len(refresh.Peers), len(projects)-1)
	}
	peerIDs := make(map[string]struct{}, len(refresh.Peers))
	peerBefore := make([][]string, 0, len(refresh.Peers))
	peerAfter := make([][]string, 0, len(refresh.Peers))
	for _, peer := range refresh.Peers {
		if peer.ProjectID == "" || strings.TrimSpace(peer.ProjectID) != peer.ProjectID {
			return fmt.Errorf("%s event-refresh evidence has an invalid peer project ID", platform)
		}
		if peer.ProjectID == refresh.TargetProjectID {
			return fmt.Errorf("%s event-refresh evidence lists target project %q as a peer", platform, peer.ProjectID)
		}
		if _, exists := projectIDs[peer.ProjectID]; !exists {
			return fmt.Errorf("%s event-refresh evidence names unknown peer project %q", platform, peer.ProjectID)
		}
		if _, exists := peerIDs[peer.ProjectID]; exists {
			return fmt.Errorf("%s event-refresh evidence duplicates peer project %q", platform, peer.ProjectID)
		}
		peerIDs[peer.ProjectID] = struct{}{}
		beforeIDs, beforeErr := normalizeEventRefreshContainerIDs(peer.BeforeContainerIDs, "peer before", platform)
		if beforeErr != nil {
			return beforeErr
		}
		afterIDs, afterErr := normalizeEventRefreshContainerIDs(peer.AfterContainerIDs, "peer after", platform)
		if afterErr != nil {
			return afterErr
		}
		if !slices.Equal(beforeIDs, afterIDs) {
			return fmt.Errorf("%s event-refresh peer project %q changed container identities", platform, peer.ProjectID)
		}
		peerBefore = append(peerBefore, beforeIDs)
		peerAfter = append(peerAfter, afterIDs)
	}
	if len(peerIDs) != len(projects)-1 {
		return fmt.Errorf("%s event-refresh evidence does not cover every peer project", platform)
	}
	if err := verifyEventRefreshSnapshotOwnership(before, peerBefore, "before", platform); err != nil {
		return err
	}
	return verifyEventRefreshSnapshotOwnership(after, peerAfter, "after", platform)
}

// normalizeEventRefreshContainerIDs validates and sorts one bounded container identity set for set comparisons.
func normalizeEventRefreshContainerIDs(ids []string, scope, platform string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s event-refresh evidence has no %s container IDs", platform, scope)
	}
	normalized := slices.Clone(ids)
	for _, id := range normalized {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("%s event-refresh evidence has an invalid %s container ID", platform, scope)
		}
	}
	slices.Sort(normalized)
	for index := 1; index < len(normalized); index++ {
		if normalized[index] == normalized[index-1] {
			return nil, fmt.Errorf("%s event-refresh evidence duplicates a %s container ID", platform, scope)
		}
	}
	return normalized, nil
}

// containsSharedContainer reports whether a replacement retained any identity from the pre-event target set.
func containsSharedContainer(before, after []string) bool {
	seen := make(map[string]struct{}, len(before))
	for _, id := range before {
		seen[id] = struct{}{}
	}
	for _, id := range after {
		if _, exists := seen[id]; exists {
			return true
		}
	}
	return false
}

// verifyEventRefreshSnapshotOwnership rejects identities shared by target and peers at either event snapshot.
func verifyEventRefreshSnapshotOwnership(target []string, peers [][]string, snapshot, platform string) error {
	owners := make(map[string]string, len(target))
	for _, id := range target {
		owners[id] = "target"
	}
	for peerIndex, peer := range peers {
		for _, id := range peer {
			if owner, exists := owners[id]; exists {
				return fmt.Errorf("%s event-refresh snapshot %s shares container %q between %s and peer %d", platform, snapshot, id, owner, peerIndex)
			}
			owners[id] = fmt.Sprintf("peer %d", peerIndex)
		}
	}
	return nil
}

// verifyAssertions rejects skipped, failed, duplicate, missing, and unrecognized product assertions.
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
	for _, requiredID := range required {
		if _, exists := seen[requiredID]; !exists {
			return fmt.Errorf("%s is missing assertion %s", platform, requiredID)
		}
	}
	return nil
}

// verifyDigests admits only canonical SHA-256 digests for bounded uploaded artifacts and dependencies.
func verifyDigests(digests []string, platform string) error {
	if len(digests) == 0 {
		return fmt.Errorf("%s evidence has no artifact digests", platform)
	}
	seen := make(map[string]struct{}, len(digests))
	for _, digest := range digests {
		if len(digest) != 64 || strings.ToLower(digest) != digest {
			return fmt.Errorf("%s evidence has a non-canonical artifact digest", platform)
		}
		for _, character := range digest {
			if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
				return fmt.Errorf("%s evidence has an invalid artifact digest", platform)
			}
		}
		if _, exists := seen[digest]; exists {
			return fmt.Errorf("%s evidence has duplicate artifact digest", platform)
		}
		seen[digest] = struct{}{}
	}
	return nil
}

// decodeEvidence parses one bounded strict JSON document without accepting concatenated or extension payloads.
func decodeEvidence(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open evidence %s: %w", path, err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximumEvidenceBytes+1))
	if err != nil {
		return fmt.Errorf("read evidence %s: %w", path, err)
	}
	if len(contents) > maximumEvidenceBytes {
		return fmt.Errorf("decode evidence %s: evidence exceeds one mebibyte", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
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
