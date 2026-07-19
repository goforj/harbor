package state

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
)

const maximumNetworkPoolCandidateCount = 65535

// NetworkProjectRevision pins one project aggregate considered by an initial network plan.
type NetworkProjectRevision struct {
	ProjectID domain.ProjectID
	Revision  domain.Sequence
}

// Validate rejects project expectations that cannot participate in optimistic concurrency.
func (expectation NetworkProjectRevision) Validate() error {
	if err := expectation.ProjectID.Validate(); err != nil {
		return err
	}
	_, err := sequenceToModelInt("expected project revision", expectation.Revision, false)
	return err
}

// NetworkSetupComponent identifies one machine-scoped host integration proved before initialization commits.
type NetworkSetupComponent string

const (
	// NetworkSetupComponentMachineOwnership proves Harbor owns the selected machine-level integration namespace.
	NetworkSetupComponentMachineOwnership NetworkSetupComponent = "machine_ownership"
	// NetworkSetupComponentLoopbackPool proves the selected loopback pool boundary and candidates are reserved for Harbor.
	NetworkSetupComponentLoopbackPool NetworkSetupComponent = "loopback_pool"
	// NetworkSetupComponentResolver proves the .test resolver path reaches Harbor.
	NetworkSetupComponentResolver NetworkSetupComponent = "resolver"
	// NetworkSetupComponentLowPorts proves the selected DNS and ingress bind mechanism is installed.
	NetworkSetupComponentLowPorts NetworkSetupComponent = "low_ports"
)

// Validate rejects setup components outside Harbor's fixed host ownership boundary.
func (component NetworkSetupComponent) Validate() error {
	switch component {
	case NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
		NetworkSetupComponentResolver,
		NetworkSetupComponentLowPorts:
		return nil
	default:
		return fmt.Errorf("network setup component %q is unsupported", component)
	}
}

// NetworkSetupProof is the bounded, sanitized postcondition persisted for one installed host component.
type NetworkSetupProof struct {
	Component  NetworkSetupComponent
	Evidence   string
	Generation uint64
	VerifiedAt time.Time
}

// Validate rejects setup proof metadata that cannot be persisted without exposing mutation authority.
func (proof NetworkSetupProof) Validate() error {
	if err := proof.Component.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network setup proof generation", proof.Generation, false); err != nil {
		return err
	}
	if err := validateNetworkEvidence("network setup proof evidence", proof.Evidence); err != nil {
		return err
	}
	return validateStoredTime("network setup proof verification time", proof.VerifiedAt)
}

// NetworkLeaseEnsure records one completed host ensure without carrying a raw mutation ticket or helper response.
type NetworkLeaseEnsure struct {
	Lease          identity.Lease
	Generation     uint64
	EnsureEvidence string
	LeasedAt       time.Time
}

// Validate rejects ensure facts that cannot become one canonical active lease row.
func (ensure NetworkLeaseEnsure) Validate() error {
	if err := validateNetworkMutationLease("network lease ensure", ensure.Lease); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network lease generation", ensure.Generation, false); err != nil {
		return err
	}
	if err := validateNetworkEvidence("network lease ensure evidence", ensure.EnsureEvidence); err != nil {
		return err
	}
	return validateStoredTime("network lease ensure time", ensure.LeasedAt)
}

// NetworkLeaseRelease records one completed host release and its bounded quarantine policy.
type NetworkLeaseRelease struct {
	Lease             identity.Lease
	ReleaseGeneration uint64
	ReleaseEvidence   string
	ReleasedAt        time.Time
	QuarantinedAt     time.Time
	ReuseAfter        time.Time
	QuarantineReason  string
}

// Validate rejects release facts that cannot become one complete quarantined lease row.
func (release NetworkLeaseRelease) Validate() error {
	if err := validateNetworkMutationLease("network lease release", release.Lease); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network lease release generation", release.ReleaseGeneration, false); err != nil {
		return err
	}
	if err := validateNetworkEvidence("network lease release evidence", release.ReleaseEvidence); err != nil {
		return err
	}
	for _, candidate := range []struct {
		name  string
		value time.Time
	}{
		{name: "network lease release time", value: release.ReleasedAt},
		{name: "network lease quarantine time", value: release.QuarantinedAt},
		{name: "network lease reuse time", value: release.ReuseAfter},
	} {
		if err := validateStoredTime(candidate.name, candidate.value); err != nil {
			return err
		}
	}
	if release.QuarantinedAt.Before(release.ReleasedAt) {
		return fmt.Errorf("network lease quarantine time must not precede release time")
	}
	if release.ReuseAfter.Before(release.QuarantinedAt) {
		return fmt.Errorf("network lease reuse time must not precede quarantine time")
	}
	return validateBoundedNetworkText(
		"network lease quarantine reason",
		release.QuarantineReason,
		maximumNetworkQuarantineReasonLength,
	)
}

// InitializeNetworkIdentityRequest commits the machine ownership and loopback pool foundation without claiming data-plane authority.
type InitializeNetworkIdentityRequest struct {
	ExpectedNetworkRevision domain.Sequence
	Ownership               identity.Ownership
	Pool                    identity.Pool
	PoolGeneration          uint64
	Setup                   []NetworkSetupProof
	At                      time.Time
}

// Validate rejects stale-shaped or overprivileged identity initialization plans before storage authority is entered.
func (request InitializeNetworkIdentityRequest) Validate() error {
	if _, err := sequenceToModelInt("expected network revision", request.ExpectedNetworkRevision, true); err != nil {
		return err
	}
	if request.ExpectedNetworkRevision != 0 {
		return fmt.Errorf("initial network revision must be zero")
	}
	if err := validateStoredTime("network identity initialization time", request.At); err != nil {
		return err
	}
	if err := request.Ownership.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network ownership generation", request.Ownership.Generation, false); err != nil {
		return err
	}
	if err := request.Pool.Validate(); err != nil {
		return err
	}
	if request.Pool.Capacity() > maximumNetworkPoolCandidateCount {
		return fmt.Errorf("network pool contains %d candidates, maximum is %d", request.Pool.Capacity(), maximumNetworkPoolCandidateCount)
	}
	if _, err := unsignedToModelInt("network pool generation", request.PoolGeneration, false); err != nil {
		return err
	}
	if err := validateNetworkIdentitySetupProofs(request.Setup, request.At); err != nil {
		return err
	}

	candidate := NetworkRecord{
		Stage:       NetworkStageIdentity,
		Revision:    1,
		CreatedAt:   request.At,
		UpdatedAt:   request.At,
		Ownership:   request.Ownership,
		Pool:        request.Pool,
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: DataPlaneReservations{
			Endpoints:            []EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("initial network identity aggregate: %w", err)
	}
	return nil
}

// InitializeNetworkRequest commits the first full durable network aggregate after every supplied host fact is observed.
type InitializeNetworkRequest struct {
	ExpectedNetworkRevision domain.Sequence
	ExpectedProjects        []NetworkProjectRevision
	Ownership               identity.Ownership
	Pool                    identity.Pool
	PoolGeneration          uint64
	Setup                   []NetworkSetupProof
	Listeners               SharedListenerReservations
	Ensures                 []NetworkLeaseEnsure
	Endpoints               []EndpointReservation
	At                      time.Time
}

// Validate rejects stale-shaped or noncanonical initialization plans before storage authority is entered.
func (request InitializeNetworkRequest) Validate() error {
	if _, err := sequenceToModelInt("expected network revision", request.ExpectedNetworkRevision, true); err != nil {
		return err
	}
	if request.ExpectedNetworkRevision != 0 {
		return fmt.Errorf("initial network revision must be zero")
	}
	if err := validateStoredTime("network initialization time", request.At); err != nil {
		return err
	}
	expectedProjects, err := validateNetworkProjectRevisions(request.ExpectedProjects)
	if err != nil {
		return err
	}
	if err := request.Ownership.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("network ownership generation", request.Ownership.Generation, false); err != nil {
		return err
	}
	if err := request.Pool.Validate(); err != nil {
		return err
	}
	if request.Pool.Capacity() > maximumNetworkPoolCandidateCount {
		return fmt.Errorf("network pool contains %d candidates, maximum is %d", request.Pool.Capacity(), maximumNetworkPoolCandidateCount)
	}
	if _, err := unsignedToModelInt("network pool generation", request.PoolGeneration, false); err != nil {
		return err
	}
	if err := validateNetworkSetupProofs(request.Setup, request.At); err != nil {
		return err
	}
	if err := request.Listeners.Validate(); err != nil {
		return err
	}
	for _, listener := range []ListenerReservation{request.Listeners.DNS, request.Listeners.HTTP, request.Listeners.HTTPS} {
		if err := validateNetworkMutationFactTime("network listener verification time", listener.VerifiedAt, request.At); err != nil {
			return err
		}
	}
	if request.Ensures == nil {
		return fmt.Errorf("initial network lease ensures must be initialized")
	}
	if err := validateNetworkLeaseEnsures(request.Ensures, "", request.At); err != nil {
		return err
	}
	if request.Endpoints == nil {
		return fmt.Errorf("initial network endpoints must be initialized")
	}
	if err := validateNetworkMutationEndpoints(request.Endpoints, ""); err != nil {
		return err
	}
	if err := validateInitializeNetworkProjectTopology(expectedProjects, request.Ensures, request.Endpoints); err != nil {
		return err
	}

	leases := make([]identity.Lease, 0, len(request.Ensures))
	for _, ensure := range request.Ensures {
		if ensure.Lease.Ownership.InstallationID != request.Ownership.InstallationID {
			return fmt.Errorf("initial network lease for project %q does not use the initialized installation", ensure.Lease.Key.ProjectID)
		}
		if !request.Pool.Contains(ensure.Lease.Address) {
			return fmt.Errorf("initial network lease address %s is not a pool candidate", ensure.Lease.Address)
		}
		leases = append(leases, ensure.Lease)
	}
	candidate := NetworkRecord{
		Stage:       NetworkStageFull,
		Revision:    1,
		CreatedAt:   request.At,
		UpdatedAt:   request.At,
		Ownership:   request.Ownership,
		Pool:        request.Pool,
		Leases:      leases,
		Quarantines: []identity.Quarantine{},
		Reservations: DataPlaneReservations{
			Listeners:            request.Listeners,
			Endpoints:            request.Endpoints,
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("initial network aggregate: %w", err)
	}
	return nil
}

// ReplaceProjectNetworkRequest applies completed lease effects and replaces one project's durable public reservations.
type ReplaceProjectNetworkRequest struct {
	ProjectID               domain.ProjectID
	ExpectedNetworkRevision domain.Sequence
	ExpectedProjectRevision domain.Sequence
	Ensures                 []NetworkLeaseEnsure
	Releases                []NetworkLeaseRelease
	Endpoints               []EndpointReservation
	At                      time.Time
}

// Validate rejects incomplete deltas and stale-shaped replacement preconditions before storage authority is entered.
func (request ReplaceProjectNetworkRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected network revision", request.ExpectedNetworkRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if request.ExpectedNetworkRevision == request.ExpectedProjectRevision {
		return fmt.Errorf("network state and project %q cannot share expected revision %d", request.ProjectID, request.ExpectedNetworkRevision)
	}
	if err := validateStoredTime("project network replacement time", request.At); err != nil {
		return err
	}
	if request.Ensures == nil {
		return fmt.Errorf("project network lease ensures must be initialized")
	}
	if err := validateNetworkLeaseEnsures(request.Ensures, request.ProjectID, request.At); err != nil {
		return err
	}
	if request.Releases == nil {
		return fmt.Errorf("project network lease releases must be initialized")
	}
	if err := validateNetworkLeaseReleases(request.Releases, request.ProjectID, request.At); err != nil {
		return err
	}
	if request.Endpoints == nil {
		return fmt.Errorf("project network endpoints must be initialized")
	}
	if err := validateNetworkMutationEndpoints(request.Endpoints, request.ProjectID); err != nil {
		return err
	}

	ensureAddresses := make(map[netip.Addr]identity.LeaseKey, len(request.Ensures))
	ensureKeys := make(map[identity.LeaseKey]netip.Addr, len(request.Ensures))
	for _, ensure := range request.Ensures {
		ensureAddresses[ensure.Lease.Address] = ensure.Lease.Key
		ensureKeys[ensure.Lease.Key] = ensure.Lease.Address
	}
	for _, release := range request.Releases {
		if address, reallocates := ensureKeys[release.Lease.Key]; reallocates && address == release.Lease.Address {
			return fmt.Errorf("network lease key for project %q cannot be released and ensured at the same address", release.Lease.Key.ProjectID)
		}
		if key, overlaps := ensureAddresses[release.Lease.Address]; overlaps {
			return fmt.Errorf("network lease address %s is both released and ensured for %q", release.Lease.Address, key.ProjectID)
		}
	}
	return nil
}

// NetworkMutationResult returns the safe aggregate and whether storage avoided a write because durable state already satisfied the requested semantics.
type NetworkMutationResult struct {
	// Record is the complete public aggregate read from durable state.
	Record NetworkRecord
	// Replayed reports semantic equality, not proof that this exact call previously committed.
	Replayed bool
}

// Validate rejects mutation results that could not have been read back from durable state.
func (result NetworkMutationResult) Validate() error {
	if err := result.Record.Validate(); err != nil {
		return fmt.Errorf("network mutation result: %w", err)
	}
	return nil
}

// validateNetworkProjectRevisions requires one canonical snapshot entry for every expected project.
func validateNetworkProjectRevisions(expectations []NetworkProjectRevision) (map[domain.ProjectID]domain.Sequence, error) {
	if expectations == nil {
		return nil, fmt.Errorf("expected network projects must be initialized")
	}
	result := make(map[domain.ProjectID]domain.Sequence, len(expectations))
	revisions := make(map[domain.Sequence]domain.ProjectID, len(expectations))
	for index, expectation := range expectations {
		if err := expectation.Validate(); err != nil {
			return nil, fmt.Errorf("expected network project %d: %w", index, err)
		}
		if index > 0 && expectations[index-1].ProjectID >= expectation.ProjectID {
			return nil, fmt.Errorf("expected network projects must be unique and ordered")
		}
		if owner, duplicate := revisions[expectation.Revision]; duplicate {
			return nil, fmt.Errorf(
				"expected network project revision %d is shared by %q and %q",
				expectation.Revision,
				owner,
				expectation.ProjectID,
			)
		}
		result[expectation.ProjectID] = expectation.Revision
		revisions[expectation.Revision] = expectation.ProjectID
	}
	return result, nil
}

// validateNetworkIdentitySetupProofs requires only the ownership facts that identity allocation can safely consume.
func validateNetworkIdentitySetupProofs(proofs []NetworkSetupProof, at time.Time) error {
	return validateNetworkSetupProofSet(proofs, []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
	}, at)
}

// validateNetworkSetupProofs requires the full host setup vocabulary in its canonical dependency order.
func validateNetworkSetupProofs(proofs []NetworkSetupProof, at time.Time) error {
	return validateNetworkSetupProofSet(proofs, []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
		NetworkSetupComponentResolver,
		NetworkSetupComponentLowPorts,
	}, at)
}

// validateNetworkSetupProofSet requires one exact, ordered proof for every authority granted by a lifecycle stage.
func validateNetworkSetupProofSet(proofs []NetworkSetupProof, expected []NetworkSetupComponent, at time.Time) error {
	if proofs == nil {
		return fmt.Errorf("network setup proofs must be initialized")
	}
	if len(proofs) != len(expected) {
		return fmt.Errorf("network setup proofs contain %d components, expected %d", len(proofs), len(expected))
	}
	for index, proof := range proofs {
		if proof.Component != expected[index] {
			return fmt.Errorf("network setup proof %d is %q, expected %q", index, proof.Component, expected[index])
		}
		if err := proof.Validate(); err != nil {
			return fmt.Errorf("network setup proof %q: %w", proof.Component, err)
		}
		if err := validateNetworkMutationFactTime("network setup proof verification time", proof.VerifiedAt, at); err != nil {
			return err
		}
	}
	return nil
}

// validateNetworkLeaseEnsures requires deterministic, nonaliasing host ensure facts for one optional project scope.
func validateNetworkLeaseEnsures(ensures []NetworkLeaseEnsure, projectID domain.ProjectID, at time.Time) error {
	addresses := make(map[netip.Addr]struct{}, len(ensures))
	for index, ensure := range ensures {
		if err := ensure.Validate(); err != nil {
			return fmt.Errorf("network lease ensure %d: %w", index, err)
		}
		if projectID != "" && ensure.Lease.Key.ProjectID != projectID {
			return fmt.Errorf("network lease ensure belongs to project %q, not %q", ensure.Lease.Key.ProjectID, projectID)
		}
		if index > 0 && !networkLeaseLess(ensures[index-1].Lease, ensure.Lease) {
			return fmt.Errorf("network lease ensures must be unique and ordered")
		}
		if _, duplicate := addresses[ensure.Lease.Address]; duplicate {
			return fmt.Errorf("network lease ensure address %s is duplicated", ensure.Lease.Address)
		}
		addresses[ensure.Lease.Address] = struct{}{}
		if err := validateNetworkMutationFactTime("network lease ensure time", ensure.LeasedAt, at); err != nil {
			return err
		}
	}
	return nil
}

// validateNetworkLeaseReleases requires deterministic, nonaliasing host release facts for one project.
func validateNetworkLeaseReleases(releases []NetworkLeaseRelease, projectID domain.ProjectID, at time.Time) error {
	addresses := make(map[netip.Addr]struct{}, len(releases))
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return fmt.Errorf("network lease release %d: %w", index, err)
		}
		if release.Lease.Key.ProjectID != projectID {
			return fmt.Errorf("network lease release belongs to project %q, not %q", release.Lease.Key.ProjectID, projectID)
		}
		if index > 0 && !networkLeaseLess(releases[index-1].Lease, release.Lease) {
			return fmt.Errorf("network lease releases must be unique and ordered")
		}
		if _, duplicate := addresses[release.Lease.Address]; duplicate {
			return fmt.Errorf("network lease release address %s is duplicated", release.Lease.Address)
		}
		addresses[release.Lease.Address] = struct{}{}
		if err := validateNetworkMutationFactTime("network lease release time", release.ReleasedAt, at); err != nil {
			return err
		}
		if err := validateNetworkMutationFactTime("network lease quarantine time", release.QuarantinedAt, at); err != nil {
			return err
		}
	}
	return nil
}

// validateNetworkMutationEndpoints requires deterministic project-scoped reservations without needing current shared listeners.
func validateNetworkMutationEndpoints(endpoints []EndpointReservation, projectID domain.ProjectID) error {
	keys := make(map[EndpointReservationKey]struct{}, len(endpoints))
	hosts := make(map[string]struct{}, len(endpoints))
	tcpSockets := make(map[netip.AddrPort]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		if err := endpoint.Validate(); err != nil {
			return fmt.Errorf("network endpoint %d: %w", index, err)
		}
		if projectID != "" && endpoint.Key.ProjectID != projectID {
			return fmt.Errorf("network endpoint belongs to project %q, not %q", endpoint.Key.ProjectID, projectID)
		}
		if index > 0 && !endpointReservationLess(endpoints[index-1], endpoint) {
			return fmt.Errorf("network endpoints must be unique and ordered")
		}
		if _, duplicate := keys[endpoint.Key]; duplicate {
			return fmt.Errorf("network endpoint key %q/%q is duplicated", endpoint.Key.ProjectID, endpoint.Key.EndpointID)
		}
		keys[endpoint.Key] = struct{}{}
		if _, duplicate := hosts[endpoint.Host]; duplicate {
			return fmt.Errorf("network endpoint host %q is duplicated", endpoint.Host)
		}
		hosts[endpoint.Host] = struct{}{}
		if endpoint.Protocol != EndpointProtocolTCP {
			continue
		}
		if _, duplicate := tcpSockets[endpoint.Public]; duplicate {
			return fmt.Errorf("native network endpoint socket %s is duplicated", endpoint.Public)
		}
		tcpSockets[endpoint.Public] = struct{}{}
	}
	return nil
}

// validateInitializeNetworkProjectTopology binds every initial lease and endpoint to the exact optimistic project snapshot.
func validateInitializeNetworkProjectTopology(
	expectedProjects map[domain.ProjectID]domain.Sequence,
	ensures []NetworkLeaseEnsure,
	endpoints []EndpointReservation,
) error {
	primaries := make(map[domain.ProjectID]struct{}, len(ensures))
	for _, ensure := range ensures {
		if _, exists := expectedProjects[ensure.Lease.Key.ProjectID]; !exists {
			return fmt.Errorf("initial network lease belongs to unexpected project %q", ensure.Lease.Key.ProjectID)
		}
		if ensure.Lease.Key.Kind() == identity.LeaseKindPrimary {
			primaries[ensure.Lease.Key.ProjectID] = struct{}{}
		}
	}
	for _, endpoint := range endpoints {
		if _, exists := expectedProjects[endpoint.Key.ProjectID]; !exists {
			return fmt.Errorf("initial network endpoint belongs to unexpected project %q", endpoint.Key.ProjectID)
		}
	}
	projectIDs := make([]domain.ProjectID, 0, len(expectedProjects))
	for projectID := range expectedProjects {
		projectIDs = append(projectIDs, projectID)
	}
	slices.Sort(projectIDs)
	for _, projectID := range projectIDs {
		if _, exists := primaries[projectID]; !exists {
			return fmt.Errorf("expected project %q requires an initial primary network lease", projectID)
		}
	}
	return nil
}

// validateNetworkMutationFactTime keeps persisted facts at or before the root timestamp that will commit them.
func validateNetworkMutationFactTime(name string, value time.Time, at time.Time) error {
	if value.After(at) {
		return fmt.Errorf("%s must not be after the network mutation time", name)
	}
	return nil
}

// validateNetworkMutationLease rejects address aliases and counters that generated models cannot preserve.
func validateNetworkMutationLease(name string, lease identity.Lease) error {
	if err := lease.Validate(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if lease.Address != lease.Address.Unmap() {
		return fmt.Errorf("%s address %s must use canonical IPv4 form", name, lease.Address)
	}
	if _, err := unsignedToModelInt(name+" ownership generation", lease.Ownership.Generation, false); err != nil {
		return err
	}
	return nil
}
