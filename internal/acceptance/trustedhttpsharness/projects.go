package trustedhttpsharness

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const happyPathAppPort uint16 = 3000

const generatedAppReadyPath = "bin/.app.ready"

const maximumReadyMarkerBytes = 64 << 10

// ProjectSpec binds one generated GoForj project to its expected public Harbor identity.
type ProjectSpec struct {
	// Name is the project name retained by the generated checkout and OpenAPI document.
	Name string
	// Module is the unique Go module path supplied to GoForj.
	Module string
	// Domain is the stable literal HTTPS hostname projected by Harbor.
	Domain string
	// AppPort is the unchanged private App port shared by all three projects.
	AppPort uint16
}

// CheckoutBaseline binds an exact generated checkout root to its pre-Harbor filesystem state.
type CheckoutBaseline struct {
	// Root is the absolute generated checkout directory.
	Root string
	// Snapshot is the recursive no-follow state captured before Harbor registration.
	Snapshot goforjproject.Snapshot
	// ReadyMarker contains the pre-session bytes and permissions that GoForj dev rewrites.
	ReadyMarker []byte
	// ReadyMarkerMode is the exact pre-session permission set for ReadyMarker.
	ReadyMarkerMode fs.FileMode
}

// HappyPathProjects returns the single project set used by the native gate and every later platform adapter.
func HappyPathProjects() []ProjectSpec {
	return []ProjectSpec{
		{
			Name:    "Orders",
			Module:  "example.test/harbor/orders",
			Domain:  "orders.test",
			AppPort: happyPathAppPort,
		},
		{
			Name:    "Billing",
			Module:  "example.test/harbor/billing",
			Domain:  "billing.test",
			AppPort: happyPathAppPort,
		},
		{
			Name:    "Inventory",
			Module:  "example.test/harbor/inventory",
			Domain:  "inventory.test",
			AppPort: happyPathAppPort,
		},
	}
}

// RenderSpecs converts the fixed public identities into GoForj's unmodified generated-project contract.
func RenderSpecs(projects []ProjectSpec) ([]goforjproject.Spec, error) {
	if err := validateProjectSpecs(projects); err != nil {
		return nil, err
	}
	specifications := make([]goforjproject.Spec, 0, len(projects))
	for _, project := range projects {
		specifications = append(specifications, goforjproject.Spec{
			Name:   project.Name,
			Module: project.Module,
			Port:   project.AppPort,
		})
	}
	return specifications, nil
}

// ProbeEndpoints derives the exact system-HTTPS assertions from the generated project set.
func ProbeEndpoints(projects []ProjectSpec) ([]Endpoint, error) {
	if err := validateProjectSpecs(projects); err != nil {
		return nil, err
	}
	endpoints := make([]Endpoint, 0, len(projects))
	for _, project := range projects {
		endpoints = append(endpoints, Endpoint{
			Domain:       project.Domain,
			OpenAPITitle: project.Name,
		})
	}
	return endpoints, nil
}

// CaptureBaselines records every rendered checkout only after fixture generation is complete.
func CaptureBaselines(projects []goforjproject.Project) ([]CheckoutBaseline, error) {
	if len(projects) != 3 {
		return nil, fmt.Errorf("checkout baseline requires exactly three generated projects, got %d", len(projects))
	}
	baselines := make([]CheckoutBaseline, 0, len(projects))
	seen := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		if project.Root == "" || !filepath.IsAbs(project.Root) || filepath.Clean(project.Root) != project.Root {
			return nil, fmt.Errorf("generated checkout root %q must be absolute and clean", project.Root)
		}
		if _, exists := seen[project.Root]; exists {
			return nil, fmt.Errorf("generated checkout root %q is duplicated", project.Root)
		}
		seen[project.Root] = struct{}{}
		snapshot, err := goforjproject.CaptureSnapshot(project.Root)
		if err != nil {
			return nil, fmt.Errorf("capture generated checkout %q: %w", project.Name, err)
		}
		if err := validateGeneratedAppReady(snapshot); err != nil {
			return nil, fmt.Errorf("validate generated checkout %q build readiness: %w", project.Name, err)
		}
		readyMarker, readyMarkerMode, err := captureReadyMarker(project.Root)
		if err != nil {
			return nil, fmt.Errorf("capture generated checkout %q build readiness: %w", project.Name, err)
		}
		baselines = append(baselines, CheckoutBaseline{
			Root:            project.Root,
			Snapshot:        snapshot,
			ReadyMarker:     readyMarker,
			ReadyMarkerMode: readyMarkerMode,
		})
	}
	return baselines, nil
}

// RestoreReadyMarkers restores GoForj's rewritten readiness markers before exact checkout verification.
func RestoreReadyMarkers(baselines []CheckoutBaseline) error {
	var restoreErr error
	for _, baseline := range baselines {
		filename := filepath.Join(baseline.Root, filepath.FromSlash(generatedAppReadyPath))
		information, err := os.Lstat(filename)
		if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("inspect readiness marker %q: %w", filename, err))
			continue
		}
		if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("readiness marker %q is not a direct regular file", filename))
			continue
		}
		if err := os.WriteFile(filename, baseline.ReadyMarker, baseline.ReadyMarkerMode); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore readiness marker %q: %w", filename, err))
			continue
		}
		if err := os.Chmod(filename, baseline.ReadyMarkerMode); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore readiness marker permissions %q: %w", filename, err))
		}
	}
	return restoreErr
}

// VerifyBaselines proves Harbor start, stop, and cleanup restored every checkout byte-for-byte.
func VerifyBaselines(baselines []CheckoutBaseline) error {
	if len(baselines) != 3 {
		return fmt.Errorf("checkout verification requires exactly three baselines, got %d", len(baselines))
	}
	seen := make(map[string]struct{}, len(baselines))
	var verificationErr error
	for _, baseline := range baselines {
		if baseline.Root == "" || !filepath.IsAbs(baseline.Root) || filepath.Clean(baseline.Root) != baseline.Root {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("baseline root %q is not absolute and clean", baseline.Root))
			continue
		}
		if _, exists := seen[baseline.Root]; exists {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("baseline root %q is duplicated", baseline.Root))
			continue
		}
		seen[baseline.Root] = struct{}{}
		current, err := goforjproject.CaptureSnapshot(baseline.Root)
		if err != nil {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("capture final checkout %q: %w", baseline.Root, err))
			continue
		}
		if difference := diffCheckoutBaseline(baseline.Snapshot, current); difference != "" {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("checkout %q changed:\n%s", baseline.Root, difference))
		}
	}
	return verificationErr
}

// VerifyBaselinesExact proves every checkout now exactly matches its pre-Harbor baseline.
func VerifyBaselinesExact(baselines []CheckoutBaseline) error {
	var verificationErr error
	for _, baseline := range baselines {
		current, err := goforjproject.CaptureSnapshot(baseline.Root)
		if err != nil {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("capture final checkout %q: %w", baseline.Root, err))
			continue
		}
		if difference := baseline.Snapshot.Diff(current); difference != "" {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("checkout %q changed:\n%s", baseline.Root, difference))
		}
	}
	return verificationErr
}

// captureReadyMarker records the bounded direct readiness artifact before Harbor can launch a runtime.
func captureReadyMarker(root string) ([]byte, fs.FileMode, error) {
	filename := filepath.Join(root, filepath.FromSlash(generatedAppReadyPath))
	information, err := os.Lstat(filename)
	if err != nil {
		return nil, 0, err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return nil, 0, errors.New("readiness marker is not a direct regular file")
	}
	if information.Size() > maximumReadyMarkerBytes {
		return nil, 0, fmt.Errorf("readiness marker exceeds %d-byte limit", maximumReadyMarkerBytes)
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, 0, err
	}
	if len(content) > maximumReadyMarkerBytes {
		return nil, 0, fmt.Errorf("readiness marker exceeds %d-byte limit", maximumReadyMarkerBytes)
	}
	return content, information.Mode().Perm(), nil
}

// validateGeneratedAppReady requires the ordinary generated build's readiness marker before Harbor starts.
func validateGeneratedAppReady(snapshot goforjproject.Snapshot) error {
	for _, entry := range snapshot.Entries {
		if entry.Path != generatedAppReadyPath {
			continue
		}
		if entry.Type != goforjproject.SnapshotEntryRegularFile {
			return fmt.Errorf("%s is %s, want regular_file", generatedAppReadyPath, entry.Type)
		}
		return nil
	}
	return fmt.Errorf("%s is missing", generatedAppReadyPath)
}

// diffCheckoutBaseline retains exact checkout comparison except for the content
// timestamp GoForj rewrites in its own readiness marker on every dev session.
func diffCheckoutBaseline(baseline goforjproject.Snapshot, current goforjproject.Snapshot) string {
	if !readyMarkerMatches(baseline, current) {
		return baseline.Diff(current)
	}
	return normalizeReadyMarkerContent(baseline).Diff(normalizeReadyMarkerContent(current))
}

// readyMarkerMatches permits only a regular marker with its original permissions and existence.
func readyMarkerMatches(baseline goforjproject.Snapshot, current goforjproject.Snapshot) bool {
	baselineEntry, baselineFound := snapshotEntryAt(baseline, generatedAppReadyPath)
	currentEntry, currentFound := snapshotEntryAt(current, generatedAppReadyPath)
	return baselineFound && currentFound &&
		baselineEntry.Type == goforjproject.SnapshotEntryRegularFile &&
		currentEntry.Type == goforjproject.SnapshotEntryRegularFile &&
		baselineEntry.Permissions == currentEntry.Permissions
}

// normalizeReadyMarkerContent removes only the expected readiness timestamp from an otherwise exact snapshot.
func normalizeReadyMarkerContent(snapshot goforjproject.Snapshot) goforjproject.Snapshot {
	normalized := snapshot
	normalized.Entries = append([]goforjproject.SnapshotEntry(nil), snapshot.Entries...)
	for index := range normalized.Entries {
		if normalized.Entries[index].Path == generatedAppReadyPath {
			normalized.Entries[index].SHA256 = [sha256.Size]byte{}
		}
	}
	return normalized
}

// snapshotEntryAt returns the direct snapshot entry at one exact checkout-relative path.
func snapshotEntryAt(snapshot goforjproject.Snapshot, path string) (goforjproject.SnapshotEntry, bool) {
	for _, entry := range snapshot.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return goforjproject.SnapshotEntry{}, false
}

// validateProjectSpecs keeps fixture, DNS, certificate, SNI, and OpenAPI identities on one exact three-project contract.
func validateProjectSpecs(projects []ProjectSpec) error {
	if len(projects) != 3 {
		return fmt.Errorf("trusted HTTPS happy path requires exactly three projects, got %d", len(projects))
	}
	names := make(map[string]struct{}, len(projects))
	modules := make(map[string]struct{}, len(projects))
	domains := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		if project.Name == "" || strings.TrimSpace(project.Name) != project.Name {
			return errors.New("trusted HTTPS project name must be nonempty and canonical")
		}
		if project.Module == "" || strings.TrimSpace(project.Module) != project.Module {
			return fmt.Errorf("trusted HTTPS project %q has an invalid module", project.Name)
		}
		if !validProjectTestDomain(project.Domain) {
			return fmt.Errorf("trusted HTTPS project %q has invalid domain %q", project.Name, project.Domain)
		}
		if project.Domain != strings.ToLower(project.Name)+".test" {
			return fmt.Errorf("trusted HTTPS project %q domain %q does not match its generated project identity", project.Name, project.Domain)
		}
		if project.AppPort != happyPathAppPort {
			return fmt.Errorf("trusted HTTPS project %q App port is %d, want shared unchanged port %d", project.Name, project.AppPort, happyPathAppPort)
		}
		if _, exists := names[project.Name]; exists {
			return fmt.Errorf("trusted HTTPS project name %q is duplicated", project.Name)
		}
		if _, exists := modules[project.Module]; exists {
			return fmt.Errorf("trusted HTTPS project module %q is duplicated", project.Module)
		}
		if _, exists := domains[project.Domain]; exists {
			return fmt.Errorf("trusted HTTPS project domain %q is duplicated", project.Domain)
		}
		names[project.Name] = struct{}{}
		modules[project.Module] = struct{}{}
		domains[project.Domain] = struct{}{}
	}
	return nil
}
