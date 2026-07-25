package trustedhttpsharness

import (
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

var generatedDerivedOutputPaths = map[string]struct{}{
	"bin/app":                              {},
	"bin/.forj-build-cache/app.target/app": {},
	"bin/.app.ready":                       {},
}

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
		if err := validateGeneratedDerivedOutputs(snapshot); err != nil {
			return nil, fmt.Errorf("validate generated checkout %q build outputs: %w", project.Name, err)
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

// RestoreReadyMarkers restores GoForj's rewritten readiness markers for callers that need exact diagnostic snapshots.
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

// captureReadyMarker records the bounded direct readiness artifact for exact diagnostic restoration.
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

// validateGeneratedDerivedOutputs requires every output whose later content refresh GoForj owns.
func validateGeneratedDerivedOutputs(snapshot goforjproject.Snapshot) error {
	entries := make(map[string]goforjproject.SnapshotEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	for path := range generatedDerivedOutputPaths {
		entry, found := entries[path]
		if !found {
			return fmt.Errorf("%s is missing", path)
		}
		if entry.Type != goforjproject.SnapshotEntryRegularFile {
			return fmt.Errorf("%s is %s, want regular_file", path, entry.Type)
		}
	}
	return nil
}

// VerifyBaselines proves Harbor cleanup left every checkout unchanged apart from
// GoForj's regular derived outputs, whose content it refreshes during each dev session.
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

// VerifyBaselinesExact proves every checkout exactly matches its pre-Harbor baseline.
// It remains available for diagnostic assertions; lifecycle cleanup uses VerifyBaselines.
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

// diffCheckoutBaseline retains exact checkout comparison except for content
// refreshes to the pre-existing direct regular outputs that GoForj owns.
func diffCheckoutBaseline(baseline goforjproject.Snapshot, current goforjproject.Snapshot) string {
	return normalizeGeneratedDerivedOutputContent(baseline, current).Diff(current)
}

// normalizeGeneratedDerivedOutputContent removes a hash only when the same
// permitted derived output remains a regular file with its original mode.
func normalizeGeneratedDerivedOutputContent(baseline goforjproject.Snapshot, current goforjproject.Snapshot) goforjproject.Snapshot {
	normalized := baseline
	normalized.Entries = append([]goforjproject.SnapshotEntry(nil), baseline.Entries...)
	for index := range normalized.Entries {
		entry := normalized.Entries[index]
		if _, permitted := generatedDerivedOutputPaths[entry.Path]; !permitted || entry.Type != goforjproject.SnapshotEntryRegularFile {
			continue
		}
		currentEntry, found := snapshotEntryAt(current, entry.Path)
		if found && currentEntry.Type == goforjproject.SnapshotEntryRegularFile && currentEntry.Permissions == entry.Permissions {
			normalized.Entries[index].SHA256 = currentEntry.SHA256
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
