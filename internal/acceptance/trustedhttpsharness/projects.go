package trustedhttpsharness

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const happyPathAppPort uint16 = 3000

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
}

// HappyPathProjects returns the single project set used by the native gate and every later platform adapter.
func HappyPathProjects() []ProjectSpec {
	return []ProjectSpec{
		{Name: "Orders", Module: "example.test/harbor/orders", Domain: "orders.test", AppPort: happyPathAppPort},
		{Name: "Billing", Module: "example.test/harbor/billing", Domain: "billing.test", AppPort: happyPathAppPort},
		{Name: "Inventory", Module: "example.test/harbor/inventory", Domain: "inventory.test", AppPort: happyPathAppPort},
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
		endpoints = append(endpoints, Endpoint{Domain: project.Domain, OpenAPITitle: project.Name})
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
		baselines = append(baselines, CheckoutBaseline{Root: project.Root, Snapshot: snapshot})
	}
	return baselines, nil
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
		if difference := baseline.Snapshot.Diff(current); difference != "" {
			verificationErr = errors.Join(verificationErr, fmt.Errorf("checkout %q changed:\n%s", baseline.Root, difference))
		}
	}
	return verificationErr
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
