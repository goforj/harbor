package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/state"
)

const (
	// primaryLeasePersistenceAttempts bounds optimistic replanning when another durable writer wins the revision.
	primaryLeasePersistenceAttempts = 4
	// maximumPrimaryLeasePorts matches Harbor's bounded host-conflict socket vocabulary.
	maximumPrimaryLeasePorts = 128
	// maximumPrimaryLeaseProbeEvidenceBytes rejects unbounded injected observer output before it reaches a digest.
	maximumPrimaryLeaseProbeEvidenceBytes = 1024
	// primaryLeaseInitialGeneration starts per-address lease history independently from installation ownership generations.
	primaryLeaseInitialGeneration uint64 = 1
	// primaryLeaseEvidenceDomain prevents a digest from being reused as evidence for another Harbor operation.
	primaryLeaseEvidenceDomain = "goforj.harbor.project-primary-lease.v1\x00"
)

// projectPrimaryLeaseState is the optimistic durable surface needed to allocate one registered project identity.
type projectPrimaryLeaseState interface {
	Project(context.Context, domain.ProjectID) (state.ProjectRecord, error)
	Network(context.Context) (state.NetworkRecord, bool, error)
	ReplaceProjectNetwork(context.Context, state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error)
}

// projectPrimaryLeaseDiscoverer reads the one App port that must remain available on an allocated identity.
type projectPrimaryLeaseDiscoverer interface {
	DiscoverDefaultRuntimeAtAddress(context.Context, string, netip.Addr) (projectdiscovery.RuntimeTarget, error)
}

// projectPrimaryLeaseLoopbackObserver proves a pre-provisioned pool address still has Harbor's exact host shape.
type projectPrimaryLeaseLoopbackObserver interface {
	Observe(context.Context, netip.Addr) (loopback.Observation, error)
}

// projectPrimaryLeaseAdmission binds one durable identity to the exact runtime target checked before persistence.
type projectPrimaryLeaseAdmission struct {
	Lease            identity.Lease
	Target           projectdiscovery.RuntimeTarget
	Project          state.ProjectRecord
	NetworkUpdatedAt time.Time
}

// projectPrimaryLeaseRejection identifies a deterministic admission condition the user or host setup can correct.
type projectPrimaryLeaseRejection struct {
	cause   error
	problem domain.Problem
}

// Error preserves the concrete daemon-local admission diagnostic.
func (rejection *projectPrimaryLeaseRejection) Error() string {
	return rejection.cause.Error()
}

// Unwrap keeps the underlying condition available to diagnostic callers.
func (rejection *projectPrimaryLeaseRejection) Unwrap() error {
	return rejection.cause
}

// Problem returns the bounded client-facing failure associated with the rejection.
func (rejection *projectPrimaryLeaseRejection) Problem() domain.Problem {
	return rejection.problem
}

// newProjectPrimaryLeaseRejection keeps expected host or project conditions distinct from daemon health failures.
func newProjectPrimaryLeaseRejection(problem domain.Problem, cause error) error {
	return &projectPrimaryLeaseRejection{cause: cause, problem: problem}
}

// projectPrimaryLeaseObservation is the complete read-only admission result for one planned or retained identity.
type projectPrimaryLeaseObservation struct {
	Target              projectdiscovery.RuntimeTarget
	Loopback            loopback.Observation
	LoopbackFingerprint string
	Probe               identity.ProbeResult
	UnavailableAppPorts []uint16
}

// projectPrimaryLeaseCoordinator allocates from a verified pool without changing operating-system state.
type projectPrimaryLeaseCoordinator struct {
	state      projectPrimaryLeaseState
	discoverer projectPrimaryLeaseDiscoverer
	loopback   projectPrimaryLeaseLoopbackObserver
	ports      identity.HostProber
	planner    identity.Planner
	now        func() time.Time
}

// newProjectPrimaryLeaseCoordinator creates the independently observable allocation boundary used before process launch.
func newProjectPrimaryLeaseCoordinator(
	projectState projectPrimaryLeaseState,
	discoverer projectPrimaryLeaseDiscoverer,
	loopbackObserver projectPrimaryLeaseLoopbackObserver,
	portProber identity.HostProber,
	now func() time.Time,
) *projectPrimaryLeaseCoordinator {
	if nilDependency(projectState) || nilDependency(discoverer) || nilDependency(loopbackObserver) ||
		nilDependency(portProber) || nilDependency(now) {
		panic("reconcile.newProjectPrimaryLeaseCoordinator requires every dependency")
	}
	return &projectPrimaryLeaseCoordinator{
		state:      projectState,
		discoverer: discoverer,
		loopback:   loopbackObserver,
		ports:      portProber,
		planner:    identity.NewPlanner(),
		now:        now,
	}
}

// newSystemProjectPrimaryLeaseCoordinator selects production read-only host adapters for a pre-provisioned identity pool.
func newSystemProjectPrimaryLeaseCoordinator(
	projectState *state.Store,
	discoverer projectPrimaryLeaseDiscoverer,
) *projectPrimaryLeaseCoordinator {
	if projectState == nil {
		panic("reconcile.newSystemProjectPrimaryLeaseCoordinator requires non-nil state")
	}
	return newProjectPrimaryLeaseCoordinator(
		projectState,
		discoverer,
		loopback.New(),
		identity.NewSystemHost(),
		time.Now,
	)
}

// Ensure returns an existing primary unchanged or durably admits one newly observed pool candidate.
func (coordinator *projectPrimaryLeaseCoordinator) Ensure(
	ctx context.Context,
	projectID domain.ProjectID,
) (projectPrimaryLeaseAdmission, error) {
	if coordinator == nil {
		panic("reconcile.projectPrimaryLeaseCoordinator.Ensure requires a non-nil receiver")
	}
	if err := projectID.Validate(); err != nil {
		return projectPrimaryLeaseAdmission{}, err
	}
	ctx = normalizeLifecycleContext(ctx)
	if err := ctx.Err(); err != nil {
		return projectPrimaryLeaseAdmission{}, err
	}

	var lastConflict error
	for attempt := 0; attempt < primaryLeasePersistenceAttempts; attempt++ {
		admission, err := coordinator.ensureAtCurrentRevision(ctx, projectID)
		if err == nil {
			return admission, nil
		}
		if !primaryLeaseRevisionConflict(err) {
			return projectPrimaryLeaseAdmission{}, err
		}
		lastConflict = err
	}
	return projectPrimaryLeaseAdmission{}, fmt.Errorf(
		"allocate primary network lease for project %q did not converge after %d revisions: %w",
		projectID,
		primaryLeasePersistenceAttempts,
		lastConflict,
	)
}

// ensureAtCurrentRevision joins one project and network snapshot through ReplaceProjectNetwork's exact revisions.
func (coordinator *projectPrimaryLeaseCoordinator) ensureAtCurrentRevision(
	ctx context.Context,
	projectID domain.ProjectID,
) (projectPrimaryLeaseAdmission, error) {
	project, err := coordinator.state.Project(ctx, projectID)
	if err != nil {
		return projectPrimaryLeaseAdmission{}, fmt.Errorf("read project before primary lease allocation: %w", err)
	}
	network, initialized, err := coordinator.state.Network(ctx)
	if err != nil {
		return projectPrimaryLeaseAdmission{}, fmt.Errorf("read network before primary lease allocation: %w", err)
	}
	if !initialized {
		cause := fmt.Errorf("allocate primary network lease for project %q: network identity is not initialized", projectID)
		return projectPrimaryLeaseAdmission{}, newProjectPrimaryLeaseRejection(domain.Problem{
			Code:      "project.network.setup_required",
			Message:   "Harbor networking is not initialized. Complete network setup and try again.",
			Retryable: true,
		}, cause)
	}
	if err := network.Validate(); err != nil {
		return projectPrimaryLeaseAdmission{}, fmt.Errorf("allocate primary network lease for project %q: invalid network authority: %w", projectID, err)
	}
	key, err := identity.NewPrimaryKey(projectID)
	if err != nil {
		return projectPrimaryLeaseAdmission{}, err
	}
	if existing, found := primaryLeaseForKey(network.Leases, key); found {
		observation, observeErr := coordinator.observePrimaryLease(ctx, project.Project.Path, network.Pool, existing)
		if observeErr != nil {
			return projectPrimaryLeaseAdmission{}, fmt.Errorf("admit retained primary lease for project %q: %w", projectID, observeErr)
		}
		if observation.Loopback.State != loopback.StateExact {
			cause := fmt.Errorf(
				"admit retained primary lease for project %q: assigned address %s has host state %q",
				projectID,
				existing.Address,
				observation.Loopback.State,
			)
			return projectPrimaryLeaseAdmission{}, newProjectPrimaryLeaseRejection(domain.Problem{
				Code:      "project.network.identity_unavailable",
				Message:   fmt.Sprintf("The assigned Harbor address %s is not configured exactly on this machine. Repair Harbor networking and try again.", existing.Address),
				Retryable: true,
			}, cause)
		}
		if len(observation.UnavailableAppPorts) != 0 {
			port := observation.UnavailableAppPorts[0]
			cause := fmt.Errorf(
				"admit retained primary lease for project %q: App port %d is unavailable on %s",
				projectID,
				port,
				existing.Address,
			)
			return projectPrimaryLeaseAdmission{}, newProjectPrimaryLeaseRejection(domain.Problem{
				Code:      "project.network.port_unavailable",
				Message:   fmt.Sprintf("Port %d is already in use on Harbor address %s. Stop the conflicting listener and try again.", port, existing.Address),
				Retryable: true,
			}, cause)
		}
		return projectPrimaryLeaseAdmission{
			Lease:            existing,
			Target:           observation.Target,
			Project:          project,
			NetworkUpdatedAt: network.UpdatedAt,
		}, nil
	}

	return coordinator.allocateAtCurrentRevision(ctx, project, network, key)
}

// allocateAtCurrentRevision rejects unsafe candidates through the planner until one exact identity and port set is proved.
func (coordinator *projectPrimaryLeaseCoordinator) allocateAtCurrentRevision(
	ctx context.Context,
	project state.ProjectRecord,
	network state.NetworkRecord,
	key identity.LeaseKey,
) (projectPrimaryLeaseAdmission, error) {
	requirements := primaryLeaseRequirements(network.Leases, key)
	conflicts := make([]identity.Conflict, 0, network.Pool.Capacity())
	for {
		plan, err := coordinator.planner.Plan(identity.Input{
			Pool:         network.Pool,
			Ownership:    network.Ownership,
			Requirements: requirements,
			Existing:     network.Leases,
			Quarantines:  network.Quarantines,
			Conflicts:    conflicts,
		})
		if err != nil {
			cause := fmt.Errorf("plan primary network lease for project %q: %w", project.Project.ID, err)
			var exhausted *identity.ExhaustionError
			if errors.As(err, &exhausted) {
				return projectPrimaryLeaseAdmission{}, newProjectPrimaryLeaseRejection(domain.Problem{
					Code:      "project.network.capacity_exhausted",
					Message:   "No Harbor loopback address is available for this project. Repair or expand Harbor networking, stop conflicting listeners, and try again.",
					Retryable: true,
				}, cause)
			}
			return projectPrimaryLeaseAdmission{}, cause
		}
		lease, err := primaryLeaseAllocation(plan, key)
		if err != nil {
			return projectPrimaryLeaseAdmission{}, fmt.Errorf("plan primary network lease for project %q: %w", project.Project.ID, err)
		}

		observation, err := coordinator.observePrimaryLease(ctx, project.Project.Path, network.Pool, lease)
		if err != nil {
			return projectPrimaryLeaseAdmission{}, err
		}
		if observation.Loopback.State != loopback.StateExact {
			conflicts = append(conflicts, identity.Conflict{
				Address: lease.Address,
				Kind:    identity.ConflictKindAddress,
				Detail:  "pre-provisioned identity state is " + string(observation.Loopback.State),
			})
			continue
		}
		if len(observation.UnavailableAppPorts) != 0 {
			for _, port := range observation.UnavailableAppPorts {
				conflicts = append(conflicts, identity.Conflict{
					Address: lease.Address,
					Kind:    identity.ConflictKindListener,
					Port:    port,
					Detail:  "required project listener is unavailable",
				})
			}
			continue
		}

		return coordinator.persistPrimaryLease(ctx, project, network, lease, observation)
	}
}

// observePrimaryLease binds one exact default-App target to the address and port observations used for admission.
//
// Native service ports remain endpoint-reconciliation work. This slice admits only the default App listener that
// Harbor launches and probes for readiness.
func (coordinator *projectPrimaryLeaseCoordinator) observePrimaryLease(
	ctx context.Context,
	checkoutRoot string,
	pool identity.Pool,
	lease identity.Lease,
) (projectPrimaryLeaseObservation, error) {
	target, err := coordinator.discoverer.DiscoverDefaultRuntimeAtAddress(ctx, checkoutRoot, lease.Address)
	if err != nil {
		cause := fmt.Errorf("discover primary runtime at %s: %w", lease.Address, err)
		var invalid *projectdiscovery.InvalidProjectError
		if errors.As(err, &invalid) {
			return projectPrimaryLeaseObservation{}, newProjectPrimaryLeaseRejection(
				lifecycleProblem("project.runtime.invalid", cause),
				cause,
			)
		}
		return projectPrimaryLeaseObservation{}, cause
	}
	assignment, fingerprint, err := coordinator.observePreProvisionedIdentity(ctx, lease.Address)
	if err != nil {
		return projectPrimaryLeaseObservation{}, err
	}
	result := projectPrimaryLeaseObservation{
		Target:              target,
		Loopback:            assignment,
		LoopbackFingerprint: fingerprint,
	}
	if assignment.State != loopback.StateExact {
		return result, nil
	}
	probe, unavailable, err := coordinator.probePrimaryPorts(ctx, pool, lease.Address, []uint16{target.Port})
	if err != nil {
		return projectPrimaryLeaseObservation{}, err
	}
	result.Probe = probe
	result.UnavailableAppPorts = unavailable
	return result, nil
}

// observePreProvisionedIdentity requires the exact platform assignment shape established during elevated setup.
func (coordinator *projectPrimaryLeaseCoordinator) observePreProvisionedIdentity(
	ctx context.Context,
	address netip.Addr,
) (loopback.Observation, string, error) {
	observation, err := coordinator.loopback.Observe(ctx, address)
	if err != nil {
		return loopback.Observation{}, "", fmt.Errorf("observe pre-provisioned primary identity %s: %w", address, err)
	}
	if observation.Address != address {
		return loopback.Observation{}, "", fmt.Errorf("primary identity observation address differs from %s", address)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return loopback.Observation{}, "", fmt.Errorf("validate primary identity observation %s: %w", address, err)
	}
	return observation, fingerprint, nil
}

// probePrimaryPorts validates the complete exact-address bind result and identifies definite listener conflicts.
func (coordinator *projectPrimaryLeaseCoordinator) probePrimaryPorts(
	ctx context.Context,
	pool identity.Pool,
	address netip.Addr,
	ports []uint16,
) (identity.ProbeResult, []uint16, error) {
	ports, err := canonicalPrimaryLeasePorts(ports)
	if err != nil {
		return identity.ProbeResult{}, nil, err
	}
	result, err := coordinator.ports.Probe(ctx, identity.ProbeRequest{Pool: pool, Address: address, Ports: ports})
	if err != nil {
		return identity.ProbeResult{}, nil, fmt.Errorf("probe primary identity %s: %w", address, err)
	}
	if result.Address != address {
		return identity.ProbeResult{}, nil, fmt.Errorf("primary identity probe address differs from %s", address)
	}
	if len(result.Ports) != len(ports) {
		return identity.ProbeResult{}, nil, fmt.Errorf("primary identity probe returned %d ports, expected %d", len(result.Ports), len(ports))
	}
	unavailable := make([]uint16, 0, len(ports))
	for index, expected := range ports {
		probe := result.Ports[index]
		if probe.Port != expected {
			return identity.ProbeResult{}, nil, fmt.Errorf("primary identity probe port %d is %d, expected %d", index, probe.Port, expected)
		}
		if strings.TrimSpace(probe.Evidence) == "" || len(probe.Evidence) > maximumPrimaryLeaseProbeEvidenceBytes {
			return identity.ProbeResult{}, nil, fmt.Errorf("primary identity probe evidence for port %d is empty or exceeds %d bytes", probe.Port, maximumPrimaryLeaseProbeEvidenceBytes)
		}
		if !probe.Available {
			unavailable = append(unavailable, probe.Port)
		}
	}
	return result, unavailable, nil
}

// persistPrimaryLease commits only the completed host facts for the newly selected project identity.
func (coordinator *projectPrimaryLeaseCoordinator) persistPrimaryLease(
	ctx context.Context,
	project state.ProjectRecord,
	network state.NetworkRecord,
	lease identity.Lease,
	observation projectPrimaryLeaseObservation,
) (projectPrimaryLeaseAdmission, error) {
	at := lifecycleTime(coordinator.now())
	if at.Before(project.Project.UpdatedAt) {
		at = project.Project.UpdatedAt
	}
	if at.Before(network.UpdatedAt) {
		at = network.UpdatedAt
	}
	request := state.ReplaceProjectNetworkRequest{
		ProjectID:               project.Project.ID,
		ExpectedNetworkRevision: network.Revision,
		ExpectedProjectRevision: project.Revision,
		Ensures: []state.NetworkLeaseEnsure{{
			Lease:          lease,
			Generation:     primaryLeaseInitialGeneration,
			EnsureEvidence: primaryLeaseEvidence(lease, observation.LoopbackFingerprint, observation.Probe),
			LeasedAt:       at,
		}},
		Releases:  []state.NetworkLeaseRelease{},
		Endpoints: primaryLeaseProjectEndpoints(network.Reservations.Endpoints, project.Project.ID),
		At:        at,
	}
	result, err := coordinator.state.ReplaceProjectNetwork(ctx, request)
	if err != nil {
		return projectPrimaryLeaseAdmission{}, err
	}
	if err := result.Validate(); err != nil {
		return projectPrimaryLeaseAdmission{}, fmt.Errorf("validate persisted primary lease for project %q: %w", project.Project.ID, err)
	}
	persisted, found := primaryLeaseForKey(result.Record.Leases, lease.Key)
	if !found || persisted != lease {
		return projectPrimaryLeaseAdmission{}, fmt.Errorf("persisted primary lease for project %q differs from its admitted identity", project.Project.ID)
	}
	return projectPrimaryLeaseAdmission{
		Lease:            persisted,
		Target:           observation.Target,
		Project:          project,
		NetworkUpdatedAt: result.Record.UpdatedAt,
	}, nil
}

// primaryLeaseRequirements preserves every current logical identity while adding the missing project primary.
func primaryLeaseRequirements(existing []identity.Lease, required identity.LeaseKey) []identity.LeaseKey {
	requirements := make([]identity.LeaseKey, 0, len(existing)+1)
	for _, lease := range existing {
		requirements = append(requirements, lease.Key)
	}
	requirements = append(requirements, required)
	return requirements
}

// primaryLeaseAllocation requires the planner to allocate exactly the missing primary and no other logical identity.
func primaryLeaseAllocation(plan identity.Plan, key identity.LeaseKey) (identity.Lease, error) {
	if len(plan.Allocated) != 1 || plan.Allocated[0].Key != key {
		return identity.Lease{}, fmt.Errorf("planner allocated %d identities instead of the requested primary", len(plan.Allocated))
	}
	return plan.Allocated[0], nil
}

// primaryLeaseForKey returns the exact durable primary without treating an address as logical identity.
func primaryLeaseForKey(leases []identity.Lease, key identity.LeaseKey) (identity.Lease, bool) {
	for _, lease := range leases {
		if lease.Key == key {
			return lease, true
		}
	}
	return identity.Lease{}, false
}

// primaryLeaseProjectEndpoints preserves the complete existing reservation delta for one full-stage project.
func primaryLeaseProjectEndpoints(
	endpoints []state.EndpointReservation,
	projectID domain.ProjectID,
) []state.EndpointReservation {
	result := make([]state.EndpointReservation, 0)
	for _, endpoint := range endpoints {
		if endpoint.Key.ProjectID == projectID {
			result = append(result, endpoint)
		}
	}
	return result
}

// canonicalPrimaryLeasePorts bounds and orders the exact native ports considered by one admission.
func canonicalPrimaryLeasePorts(ports []uint16) ([]uint16, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("primary identity requires at least one port")
	}
	if len(ports) > maximumPrimaryLeasePorts {
		return nil, fmt.Errorf("primary identity ports exceed limit %d", maximumPrimaryLeasePorts)
	}
	canonical := slices.Clone(ports)
	slices.Sort(canonical)
	for index, port := range canonical {
		if port == 0 {
			return nil, fmt.Errorf("primary identity port zero is unsupported")
		}
		if index > 0 && canonical[index-1] == port {
			return nil, fmt.Errorf("primary identity port %d is duplicated", port)
		}
	}
	return canonical, nil
}

// primaryLeaseEvidence binds the logical lease to independently validated assignment and port observations.
func primaryLeaseEvidence(lease identity.Lease, loopbackFingerprint string, probe identity.ProbeResult) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(primaryLeaseEvidenceDomain))
	_, _ = digest.Write([]byte(lease.Key.ProjectID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(lease.Key.SecondaryID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(lease.Address.String()))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(lease.Ownership.InstallationID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(strconv.FormatUint(lease.Ownership.Generation, 10)))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(loopbackFingerprint))
	_, _ = digest.Write([]byte{0})
	for _, port := range probe.Ports {
		_, _ = digest.Write([]byte(strconv.FormatUint(uint64(port.Port), 10)))
		_, _ = digest.Write([]byte{0})
		if port.Available {
			_, _ = digest.Write([]byte{1})
		} else {
			_, _ = digest.Write([]byte{0})
		}
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(port.Evidence))
		_, _ = digest.Write([]byte{0})
	}
	return "project-primary-lease-sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// primaryLeaseRevisionConflict identifies only the optimistic state races that a fresh plan can safely retry.
func primaryLeaseRevisionConflict(err error) bool {
	var networkConflict *state.NetworkRevisionConflictError
	var projectConflict *state.ProjectRevisionConflictError
	return errors.As(err, &networkConflict) || errors.As(err, &projectConflict)
}
