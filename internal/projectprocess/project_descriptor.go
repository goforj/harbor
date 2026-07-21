package projectprocess

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/goforj/harbor/internal/goforj"
)

// ProjectDescriptorObservation is the validated descriptor identity reserved for one subsequent process launch.
type ProjectDescriptorObservation struct {
	Executable                   string
	TopologyDigest               string
	ResourcesSupported           bool
	Resources                    []goforj.Resource
	ServiceRequirementsSupported bool
	ServiceRequirements          []goforj.ServiceRequirement
}

// ObserveProjectDescriptor reads the static GoForj descriptor before a managed process is launched.
func (supervisor *Supervisor) ObserveProjectDescriptor(ctx context.Context, checkoutRoot string) (ProjectDescriptorObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ProjectDescriptorObservation{}, err
	}
	canonicalRoot, err := canonicalDirectory(checkoutRoot)
	if err != nil {
		return ProjectDescriptorObservation{}, fmt.Errorf("canonicalize descriptor checkout: %w", err)
	}
	executable, err := supervisor.acceptedGoForjExecutable("")
	if err != nil {
		return ProjectDescriptorObservation{}, err
	}
	observation, err := goforj.Observe(ctx, goforj.Query{
		Executable:  executable,
		Checkout:    canonicalRoot,
		Environment: append([]string(nil), supervisor.environment...),
	})
	if err != nil {
		return ProjectDescriptorObservation{}, fmt.Errorf("observe GoForj project descriptor: %w", err)
	}
	if observation.TopologyDigest == "" {
		return ProjectDescriptorObservation{}, fmt.Errorf("observe GoForj project descriptor: topology digest is empty")
	}
	resources := make([]goforj.Resource, len(observation.Resources))
	copy(resources, observation.Resources)
	serviceRequirements := cloneServiceRequirements(observation.ServiceRequirements)
	return ProjectDescriptorObservation{
		Executable:                   executable,
		TopologyDigest:               observation.TopologyDigest,
		ResourcesSupported:           observation.ResourcesSupported,
		Resources:                    resources,
		ServiceRequirementsSupported: observation.ServiceRequirementsSupported,
		ServiceRequirements:          serviceRequirements,
	}, nil
}

// cloneServiceRequirements prevents a caller from mutating descriptor-owned nested slices after observation.
func cloneServiceRequirements(source []goforj.ServiceRequirement) []goforj.ServiceRequirement {
	if len(source) == 0 {
		return []goforj.ServiceRequirement{}
	}
	clone := make([]goforj.ServiceRequirement, len(source))
	for index, requirement := range source {
		clone[index] = requirement
		clone[index].Consumers = append([]string(nil), requirement.Consumers...)
		clone[index].Endpoints = append([]goforj.ServiceEndpoint(nil), requirement.Endpoints...)
	}
	return clone
}

// acceptedGoForjExecutable resolves and verifies one canonical GoForj executable for all process boundaries.
func (supervisor *Supervisor) acceptedGoForjExecutable(requested string) (string, error) {
	executable := requested
	if executable == "" {
		var err error
		executable, err = exec.LookPath("forj")
		if err != nil {
			return "", incompatibleGoForjError("", fmt.Sprintf("resolve executable from PATH: %v", err))
		}
	} else if !filepath.IsAbs(executable) {
		return "", incompatibleGoForjError(executable, "descriptor-bound executable must be an absolute path")
	}
	executableIdentity, err := canonicalExecutable(executable)
	if err != nil {
		return "", incompatibleGoForjError(executable, fmt.Sprintf("canonicalize executable: %v", err))
	}
	if err := supervisor.verifyExecutable(executableIdentity); err != nil {
		return "", err
	}
	return executableIdentity, nil
}
