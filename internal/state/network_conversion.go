package state

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
)

const (
	maximumNetworkEvidenceLength         = 16384
	maximumNetworkQuarantineReasonLength = 1024
)

// networkModelRows is the complete model input captured by one future Store.Network transaction.
type networkModelRows struct {
	States        []models.NetworkState
	Candidates    []models.NetworkPoolCandidate
	SetupEvidence []models.NetworkSetupEvidence
	Listeners     []models.NetworkSharedListener
	Leases        []models.LoopbackAddressLease
	Endpoints     []models.PublicEndpointLease
	Releases      []models.NetworkProjectRelease
	Projects      []models.Project
	ReleaseOwners []models.Operation
}

// networkReleaseOwner retains only the operation identity needed to prove teardown ownership.
type networkReleaseOwner struct {
	ProjectID domain.ProjectID
	Kind      domain.OperationKind
}

// networkReleaseState retains the suppression and completion facts needed during aggregate conversion.
type networkReleaseState struct {
	Completed   bool
	CompletedAt time.Time
}

// networkQuarantineState retains historical identity and timing facts needed to prove completed teardown.
type networkQuarantineState struct {
	Key           identity.LeaseKey
	ReleasedAt    time.Time
	QuarantinedAt time.Time
}

// networkRecordFromModels converts one consistent model set into safe identity and reservation projections.
func networkRecordFromModels(rows networkModelRows) (NetworkRecord, bool, error) {
	if len(rows.States) == 0 {
		if networkChildRowCount(rows) != 0 {
			return NetworkRecord{}, false, corruptStateError("network state", "1", fmt.Errorf("child rows exist without the singleton root"))
		}
		return NetworkRecord{}, false, nil
	}
	if len(rows.States) != 1 {
		return NetworkRecord{}, false, corruptStateError("network state", "1", fmt.Errorf("singleton contains %d rows, expected 1", len(rows.States)))
	}

	root, revision, ownership, prefix, err := networkRootFromModel(rows.States[0])
	if err != nil {
		return NetworkRecord{}, false, err
	}
	knownProjects, err := networkProjectsFromModels(rows.Projects)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	releaseOwners, err := networkReleaseOwnersFromModels(rows.ReleaseOwners)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	candidates, err := networkCandidatesFromModels(rows.Candidates, root.Id, prefix)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	pool, err := identity.NewPool(prefix, candidates)
	if err != nil {
		return NetworkRecord{}, false, corruptStateError("network state", "1", err)
	}
	if err := validateNetworkSetupEvidence(rows.SetupEvidence, root.Id, root.UpdatedAt); err != nil {
		return NetworkRecord{}, false, err
	}
	listeners, err := networkListenersFromModels(rows.Listeners, root.Id, root.UpdatedAt)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	leases, quarantines, activeByID, activeByProject, quarantineByProject, err := networkLeasesFromModels(
		rows.Leases,
		root.Id,
		ownership.InstallationID,
		pool,
		knownProjects,
		root.UpdatedAt,
	)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	releases, err := networkReleasesFromModels(rows.Releases, root.Id, knownProjects, releaseOwners, root.UpdatedAt)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	allEndpoints, err := networkEndpointsFromModels(rows.Endpoints, root.Id, knownProjects, activeByID, listeners, root.UpdatedAt)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	if err := validateNetworkEndpointPrimaryLeases(allEndpoints, leases); err != nil {
		return NetworkRecord{}, false, err
	}
	if err := validateCompletedNetworkReleases(releases, knownProjects, activeByProject, quarantineByProject, allEndpoints); err != nil {
		return NetworkRecord{}, false, err
	}
	if err := validateNetworkProjectPrimaryLeases(knownProjects, rows.Projects, releases, leases); err != nil {
		return NetworkRecord{}, false, err
	}

	suppressed := make([]domain.ProjectID, 0, len(releases))
	for projectID := range releases {
		suppressed = append(suppressed, projectID)
	}
	slices.Sort(suppressed)
	publishable := make([]EndpointReservation, 0, len(allEndpoints))
	for _, endpoint := range allEndpoints {
		if _, withheld := releases[endpoint.Key.ProjectID]; withheld {
			continue
		}
		publishable = append(publishable, endpoint)
	}

	record := NetworkRecord{
		Revision:    revision,
		CreatedAt:   root.CreatedAt,
		UpdatedAt:   root.UpdatedAt,
		Ownership:   ownership,
		Pool:        pool,
		Leases:      canonicalNetworkLeases(leases),
		Quarantines: canonicalNetworkQuarantines(quarantines),
		Reservations: DataPlaneReservations{
			Listeners:            listeners,
			Endpoints:            canonicalEndpointReservations(publishable),
			SuppressedProjectIDs: suppressed,
		},
	}
	if err := record.Validate(); err != nil {
		return NetworkRecord{}, false, corruptStateError("network state", "1", err)
	}
	return record, true, nil
}

// validateNetworkProjectPrimaryLeases distinguishes newly registered stopped projects from projects that already claim runtime lifecycle.
func validateNetworkProjectPrimaryLeases(
	projects map[domain.ProjectID]struct{},
	projectRows []models.Project,
	releases map[domain.ProjectID]networkReleaseState,
	leases []identity.Lease,
) error {
	states := make(map[domain.ProjectID]domain.ProjectState, len(projectRows))
	for _, row := range projectRows {
		states[domain.ProjectID(row.ProjectId)] = domain.ProjectState(row.State)
	}
	primaries := make(map[domain.ProjectID]struct{}, len(leases))
	for _, lease := range leases {
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primaries[lease.Key.ProjectID] = struct{}{}
		}
	}
	projectIDs := make([]domain.ProjectID, 0, len(projects))
	for projectID := range projects {
		projectIDs = append(projectIDs, projectID)
	}
	slices.Sort(projectIDs)
	for _, projectID := range projectIDs {
		if release, exists := releases[projectID]; exists && release.Completed {
			continue
		}
		if _, exists := primaries[projectID]; exists {
			continue
		}
		if states[projectID] == domain.ProjectStopped {
			continue
		}
		return corruptStateError("project", string(projectID), fmt.Errorf("registered project requires a primary network lease"))
	}
	return nil
}

// validateNetworkEndpointPrimaryLeases keeps every durable route anchored to a project identity, including routes suppressed during teardown.
func validateNetworkEndpointPrimaryLeases(endpoints []EndpointReservation, leases []identity.Lease) error {
	primaries := make(map[domain.ProjectID]struct{}, len(leases))
	for _, lease := range leases {
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primaries[lease.Key.ProjectID] = struct{}{}
		}
	}
	for _, endpoint := range endpoints {
		if _, exists := primaries[endpoint.Key.ProjectID]; exists {
			continue
		}
		key := string(endpoint.Key.ProjectID) + "/" + endpoint.Key.EndpointID
		return corruptStateError("public endpoint lease", key, fmt.Errorf("endpoint requires a primary lease for project %q", endpoint.Key.ProjectID))
	}
	return nil
}

// networkChildRowCount counts only rows owned by the optional network singleton.
func networkChildRowCount(rows networkModelRows) int {
	return len(rows.Candidates) +
		len(rows.SetupEvidence) +
		len(rows.Listeners) +
		len(rows.Leases) +
		len(rows.Endpoints) +
		len(rows.Releases)
}

// networkRootFromModel validates the singleton's durable identity without comparing unrelated child generations.
func networkRootFromModel(row models.NetworkState) (models.NetworkState, domain.Sequence, identity.Ownership, netip.Prefix, error) {
	key := strconv.Itoa(row.Id)
	if row.Id != networkStateSingletonID {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("singleton ID must be 1"))
	}
	if row.Revision <= 0 || domain.Sequence(row.Revision) > domain.MaximumSequence {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("revision must be positive and within the cross-client ordering range"))
	}
	if err := validateStoredTime("network creation time", row.CreatedAt); err != nil {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, err)
	}
	if err := validateStoredTime("network update time", row.UpdatedAt); err != nil {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, err)
	}
	if row.UpdatedAt.Before(row.CreatedAt) {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("update time precedes creation time"))
	}
	generation, err := positiveNetworkGeneration("ownership generation", row.OwnershipGeneration)
	if err != nil {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, err)
	}
	ownership, err := identity.NewOwnership(identity.InstallationID(row.InstallationId), generation)
	if err != nil {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, err)
	}
	if row.DnsSuffix != ".test" {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("DNS suffix must be .test"))
	}
	address, err := parseCanonicalNetworkAddress("pool network", row.PoolNetwork)
	if err != nil {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, err)
	}
	if row.PoolPrefixLength < 8 || row.PoolPrefixLength > 32 {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("pool prefix length must be between 8 and 32"))
	}
	prefix := netip.PrefixFrom(address, row.PoolPrefixLength)
	if prefix != prefix.Masked() {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("pool network must be the canonical prefix address"))
	}
	if !prefix.Addr().IsLoopback() {
		return models.NetworkState{}, 0, identity.Ownership{}, netip.Prefix{}, corruptStateError("network state", key, fmt.Errorf("pool prefix must be contained by IPv4 loopback"))
	}
	return row, domain.Sequence(row.Revision), ownership, prefix, nil
}

// networkCandidatesFromModels validates persisted ordinal order before identity.NewPool applies its own address ordering.
func networkCandidatesFromModels(rows []models.NetworkPoolCandidate, stateID int, prefix netip.Prefix) ([]netip.Addr, error) {
	if len(rows) == 0 {
		return nil, corruptStateError("network pool candidate", "none", fmt.Errorf("at least one candidate is required"))
	}
	ordered := slices.Clone(rows)
	slices.SortFunc(ordered, func(left models.NetworkPoolCandidate, right models.NetworkPoolCandidate) int {
		if left.Ordinal != right.Ordinal {
			return cmp.Compare(left.Ordinal, right.Ordinal)
		}
		if left.Address != right.Address {
			return strings.Compare(left.Address, right.Address)
		}
		return cmp.Compare(left.Id, right.Id)
	})

	result := make([]netip.Addr, 0, len(ordered))
	ids := make(map[int]struct{}, len(ordered))
	addresses := make(map[netip.Addr]struct{}, len(ordered))
	for index, row := range ordered {
		key := durableKey(row.Address, row.Id)
		if row.Id <= 0 {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := ids[row.Id]; duplicate {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("database ID is duplicated"))
		}
		ids[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		if row.Ordinal > 65535 {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("ordinal exceeds 65535"))
		}
		if row.Ordinal != index+1 {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("ordinal is %d, expected %d", row.Ordinal, index+1))
		}
		if _, err := positiveNetworkGeneration("candidate generation", row.Generation); err != nil {
			return nil, corruptStateError("network pool candidate", key, err)
		}
		address, err := parseCanonicalNetworkAddress("candidate address", row.Address)
		if err != nil {
			return nil, corruptStateError("network pool candidate", key, err)
		}
		if !prefix.Contains(address) {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("address %s is outside %s", address, prefix))
		}
		if _, duplicate := addresses[address]; duplicate {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("address is duplicated"))
		}
		if index > 0 && result[index-1].Compare(address) >= 0 {
			return nil, corruptStateError("network pool candidate", key, fmt.Errorf("ordinal order must match numeric address order"))
		}
		addresses[address] = struct{}{}
		result = append(result, address)
	}
	return result, nil
}

// validateNetworkSetupEvidence requires one proof for each elevated network component.
func validateNetworkSetupEvidence(rows []models.NetworkSetupEvidence, stateID int, updatedAt time.Time) error {
	requiredOrder := []string{"machine_ownership", "loopback_pool", "resolver", "low_ports"}
	required := make(map[string]struct{}, len(requiredOrder))
	for _, component := range requiredOrder {
		required[component] = struct{}{}
	}
	seenIDs := make(map[int]struct{}, len(rows))
	seenComponents := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.Component, row.Id)
		if row.Id <= 0 {
			return corruptStateError("network setup evidence", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return corruptStateError("network setup evidence", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return corruptStateError("network setup evidence", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		if _, supported := required[row.Component]; !supported {
			return corruptStateError("network setup evidence", key, fmt.Errorf("component %q is unsupported", row.Component))
		}
		if _, duplicate := seenComponents[row.Component]; duplicate {
			return corruptStateError("network setup evidence", key, fmt.Errorf("component is duplicated"))
		}
		seenComponents[row.Component] = struct{}{}
		if err := validateNetworkEvidence("setup evidence", row.Evidence); err != nil {
			return corruptStateError("network setup evidence", key, err)
		}
		if _, err := positiveNetworkGeneration("setup evidence generation", row.Generation); err != nil {
			return corruptStateError("network setup evidence", key, err)
		}
		if err := validateNetworkFactTime("network setup verification time", row.VerifiedAt, updatedAt); err != nil {
			return corruptStateError("network setup evidence", key, err)
		}
	}
	for _, component := range requiredOrder {
		if _, exists := seenComponents[component]; !exists {
			return corruptStateError("network setup evidence", component, fmt.Errorf("required component is missing"))
		}
	}
	return nil
}

// networkListenersFromModels converts exactly one DNS, HTTP, and HTTPS reservation.
func networkListenersFromModels(rows []models.NetworkSharedListener, stateID int, updatedAt time.Time) (SharedListenerReservations, error) {
	byKind := make(map[string]ListenerReservation, len(rows))
	seenIDs := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.Kind, row.Id)
		if row.Id <= 0 {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		switch row.Kind {
		case "dns", "http", "https":
		default:
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, fmt.Errorf("kind %q is unsupported", row.Kind))
		}
		if _, duplicate := byKind[row.Kind]; duplicate {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, fmt.Errorf("kind is duplicated"))
		}
		advertised, err := networkAddressPortFromModel("advertised listener", row.AdvertisedAddress, row.AdvertisedPort)
		if err != nil {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, err)
		}
		bind, err := networkAddressPortFromModel("bind listener", row.BindAddress, row.BindPort)
		if err != nil {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, err)
		}
		generation, err := positiveNetworkGeneration("listener generation", row.Generation)
		if err != nil {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, err)
		}
		if err := validateNetworkFactTime("network listener verification time", row.VerifiedAt, updatedAt); err != nil {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, err)
		}
		reservation := ListenerReservation{
			Mode:       ListenerMode(row.Mode),
			Advertised: advertised,
			Bind:       bind,
			Generation: generation,
			VerifiedAt: row.VerifiedAt,
		}
		if err := reservation.Validate(); err != nil {
			return SharedListenerReservations{}, corruptStateError("network shared listener", key, err)
		}
		byKind[row.Kind] = reservation
	}
	for _, kind := range []string{"dns", "http", "https"} {
		if _, exists := byKind[kind]; !exists {
			return SharedListenerReservations{}, corruptStateError("network shared listener", kind, fmt.Errorf("required listener is missing"))
		}
	}
	result := SharedListenerReservations{DNS: byKind["dns"], HTTP: byKind["http"], HTTPS: byKind["https"]}
	if err := result.Validate(); err != nil {
		return SharedListenerReservations{}, corruptStateError("network shared listener", "aggregate", err)
	}
	return result, nil
}

// networkProjectsFromModels builds the exact referential set supplied by the read transaction.
func networkProjectsFromModels(rows []models.Project) (map[domain.ProjectID]struct{}, error) {
	projects := make(map[domain.ProjectID]struct{}, len(rows))
	ids := make(map[int]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.ProjectId, row.Id)
		if row.Id <= 0 {
			return nil, corruptStateError("project", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := ids[row.Id]; duplicate {
			return nil, corruptStateError("project", key, fmt.Errorf("database ID is duplicated"))
		}
		ids[row.Id] = struct{}{}
		projectID := domain.ProjectID(row.ProjectId)
		if err := projectID.Validate(); err != nil {
			return nil, corruptStateError("project", key, err)
		}
		if _, duplicate := projects[projectID]; duplicate {
			return nil, corruptStateError("project", key, fmt.Errorf("project ID is duplicated"))
		}
		projects[projectID] = struct{}{}
	}
	return projects, nil
}

// networkReleaseOwnersFromModels retains operation ownership without requiring unrelated presentation columns.
func networkReleaseOwnersFromModels(rows []models.Operation) (map[domain.OperationID]networkReleaseOwner, error) {
	owners := make(map[domain.OperationID]networkReleaseOwner, len(rows))
	for _, row := range rows {
		operationID := domain.OperationID(row.Id)
		if err := operationID.Validate(); err != nil {
			return nil, corruptStateError("operation", row.Id, err)
		}
		if _, duplicate := owners[operationID]; duplicate {
			return nil, corruptStateError("operation", row.Id, fmt.Errorf("operation ID is duplicated"))
		}
		if !row.ProjectId.Valid {
			return nil, corruptStateError("operation", row.Id, fmt.Errorf("network release owner must identify a project"))
		}
		projectID := domain.ProjectID(row.ProjectId.String)
		if err := projectID.Validate(); err != nil {
			return nil, corruptStateError("operation", row.Id, err)
		}
		owners[operationID] = networkReleaseOwner{ProjectID: projectID, Kind: domain.OperationKind(row.Kind)}
	}
	return owners, nil
}

// networkLeasesFromModels converts active rows for planning and released rows into unconditional reuse blocks.
func networkLeasesFromModels(
	rows []models.LoopbackAddressLease,
	stateID int,
	installationID identity.InstallationID,
	pool identity.Pool,
	knownProjects map[domain.ProjectID]struct{},
	updatedAt time.Time,
) ([]identity.Lease, []identity.Quarantine, map[int]identity.Lease, map[domain.ProjectID]int, map[domain.ProjectID][]networkQuarantineState, error) {
	leases := make([]identity.Lease, 0, len(rows))
	quarantines := make([]identity.Quarantine, 0, len(rows))
	activeByID := make(map[int]identity.Lease, len(rows))
	activeByProject := make(map[domain.ProjectID]int, len(rows))
	quarantineByProject := make(map[domain.ProjectID][]networkQuarantineState, len(rows))
	seenIDs := make(map[int]struct{}, len(rows))
	seenAddresses := make(map[netip.Addr]struct{}, len(rows))
	seenKeys := make(map[identity.LeaseKey]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.Address, row.Id)
		if row.Id <= 0 {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		address, err := parseCanonicalNetworkAddress("lease address", row.Address)
		if err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		if !pool.Contains(address) {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("address %s is not a pool candidate", address))
		}
		if _, duplicate := seenAddresses[address]; duplicate {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("address is duplicated"))
		}
		seenAddresses[address] = struct{}{}
		projectID := domain.ProjectID(row.SourceProjectId)
		if err := projectID.Validate(); err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		leaseKey, err := networkLeaseKeyFromModel(projectID, row.Kind, row.SecondaryId)
		if err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		if _, err := positiveNetworkGeneration("lease generation", row.LeaseGeneration); err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		ownershipGeneration, err := positiveNetworkGeneration("lease ownership generation", row.OwnershipGeneration)
		if err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		ownership, err := identity.NewOwnership(identity.InstallationID(row.OwnershipInstallationId), ownershipGeneration)
		if err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		if err := validateNetworkEvidence("lease ensure evidence", row.EnsureEvidence); err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}
		if err := validateNetworkFactTime("network lease time", row.LeasedAt, updatedAt); err != nil {
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
		}

		switch row.State {
		case "leased":
			if !row.ProjectId.Valid || row.ProjectId.String != row.SourceProjectId {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("active lease must retain its source project reference"))
			}
			if _, exists := knownProjects[projectID]; !exists {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("project %q is missing", projectID))
			}
			if ownership.InstallationID != installationID {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("active lease belongs to installation %q, expected %q", ownership.InstallationID, installationID))
			}
			if networkLeaseHasReleaseFields(row) {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("active lease contains release or quarantine fields"))
			}
			if _, duplicate := seenKeys[leaseKey]; duplicate {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("active lease key is duplicated"))
			}
			seenKeys[leaseKey] = struct{}{}
			lease := identity.Lease{Key: leaseKey, Address: address, Ownership: ownership}
			if err := lease.Validate(); err != nil {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
			}
			leases = append(leases, lease)
			activeByID[row.Id] = lease
			activeByProject[projectID]++
		case "quarantined":
			if row.ProjectId.Valid {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("quarantined lease must clear its active project reference"))
			}
			if err := validateQuarantinedNetworkLease(row, updatedAt); err != nil {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
			}
			quarantine := identity.Quarantine{Address: address, Reason: row.QuarantineReason.String}
			if err := quarantine.Validate(pool); err != nil {
				return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, err)
			}
			quarantines = append(quarantines, quarantine)
			quarantineByProject[projectID] = append(quarantineByProject[projectID], networkQuarantineState{
				Key:           leaseKey,
				ReleasedAt:    *row.ReleasedAt,
				QuarantinedAt: *row.QuarantinedAt,
			})
		default:
			return nil, nil, nil, nil, nil, corruptStateError("loopback address lease", key, fmt.Errorf("state %q is unsupported", row.State))
		}
	}
	return leases, quarantines, activeByID, activeByProject, quarantineByProject, nil
}

// networkLeaseKeyFromModel preserves primary and secondary identity semantics exactly.
func networkLeaseKeyFromModel(projectID domain.ProjectID, kind string, secondaryID string) (identity.LeaseKey, error) {
	switch kind {
	case string(identity.LeaseKindPrimary):
		if secondaryID != "" {
			return identity.LeaseKey{}, fmt.Errorf("primary lease must not contain a secondary ID")
		}
		return identity.NewPrimaryKey(projectID)
	case string(identity.LeaseKindSecondary):
		return identity.NewSecondaryKey(projectID, secondaryID)
	default:
		return identity.LeaseKey{}, fmt.Errorf("lease kind %q is unsupported", kind)
	}
}

// networkLeaseHasReleaseFields detects weakened-schema rows that combine active and released shapes.
func networkLeaseHasReleaseFields(row models.LoopbackAddressLease) bool {
	return row.ReleaseGeneration.Valid ||
		row.ReleaseEvidence.Valid ||
		row.ReleasedAt != nil ||
		row.QuarantinedAt != nil ||
		row.ReuseAfter != nil ||
		row.QuarantineReason.Valid
}

// validateQuarantinedNetworkLease validates the complete historical release proof without comparing it to the current installation.
func validateQuarantinedNetworkLease(row models.LoopbackAddressLease, updatedAt time.Time) error {
	if !row.ReleaseGeneration.Valid || row.ReleaseGeneration.Int64 <= int64(row.LeaseGeneration) {
		return fmt.Errorf("release generation must be present and greater than the lease generation")
	}
	if !row.ReleaseEvidence.Valid {
		return fmt.Errorf("release evidence is required")
	}
	if err := validateNetworkEvidence("lease release evidence", row.ReleaseEvidence.String); err != nil {
		return err
	}
	if row.ReleasedAt == nil || row.QuarantinedAt == nil || row.ReuseAfter == nil {
		return fmt.Errorf("release, quarantine, and reuse times are required")
	}
	for _, candidate := range []struct {
		name  string
		value *time.Time
	}{
		{name: "network lease release time", value: row.ReleasedAt},
		{name: "network lease quarantine time", value: row.QuarantinedAt},
	} {
		if err := validateNetworkFactTime(candidate.name, *candidate.value, updatedAt); err != nil {
			return err
		}
	}
	if err := validateStoredTime("network lease reuse time", *row.ReuseAfter); err != nil {
		return err
	}
	if row.ReleasedAt.Before(row.LeasedAt) {
		return fmt.Errorf("release time precedes lease time")
	}
	if row.QuarantinedAt.Before(*row.ReleasedAt) {
		return fmt.Errorf("quarantine time precedes release time")
	}
	if row.ReuseAfter.Before(*row.QuarantinedAt) {
		return fmt.Errorf("reuse time precedes quarantine time")
	}
	if !row.QuarantineReason.Valid {
		return fmt.Errorf("quarantine reason is required")
	}
	if err := validateBoundedNetworkText("quarantine reason", row.QuarantineReason.String, maximumNetworkQuarantineReasonLength); err != nil {
		return err
	}
	return nil
}

// networkReleasesFromModels validates staged teardown ownership and builds the fail-closed suppression set.
func networkReleasesFromModels(
	rows []models.NetworkProjectRelease,
	stateID int,
	knownProjects map[domain.ProjectID]struct{},
	owners map[domain.OperationID]networkReleaseOwner,
	updatedAt time.Time,
) (map[domain.ProjectID]networkReleaseState, error) {
	releases := make(map[domain.ProjectID]networkReleaseState, len(rows))
	seenIDs := make(map[int]struct{}, len(rows))
	seenOperations := make(map[domain.OperationID]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.SourceProjectId, row.Id)
		if row.Id <= 0 {
			return nil, corruptStateError("network project release", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return nil, corruptStateError("network project release", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return nil, corruptStateError("network project release", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		projectID := domain.ProjectID(row.SourceProjectId)
		if err := projectID.Validate(); err != nil {
			return nil, corruptStateError("network project release", key, err)
		}
		if _, duplicate := releases[projectID]; duplicate {
			return nil, corruptStateError("network project release", key, fmt.Errorf("source project is duplicated"))
		}
		operationID := domain.OperationID(row.OperationId)
		if err := operationID.Validate(); err != nil {
			return nil, corruptStateError("network project release", key, err)
		}
		if _, duplicate := seenOperations[operationID]; duplicate {
			return nil, corruptStateError("network project release", key, fmt.Errorf("operation is duplicated"))
		}
		seenOperations[operationID] = struct{}{}
		owner, exists := owners[operationID]
		if !exists {
			return nil, corruptStateError("network project release", key, fmt.Errorf("operation %q is missing", operationID))
		}
		if owner.Kind != domain.OperationKindProjectUnregister || owner.ProjectID != projectID {
			return nil, corruptStateError("network project release", key, fmt.Errorf("operation %q does not own unregister for project %q", operationID, projectID))
		}
		if _, err := positiveNetworkGeneration("release begin generation", row.BeginGeneration); err != nil {
			return nil, corruptStateError("network project release", key, err)
		}
		if err := validateNetworkFactTime("network project release begin time", row.BeganAt, updatedAt); err != nil {
			return nil, corruptStateError("network project release", key, err)
		}

		switch row.State {
		case "releasing":
			if !row.ProjectId.Valid || row.ProjectId.String != row.SourceProjectId {
				return nil, corruptStateError("network project release", key, fmt.Errorf("releasing row must retain its source project reference"))
			}
			if _, exists := knownProjects[projectID]; !exists {
				return nil, corruptStateError("network project release", key, fmt.Errorf("project %q is missing", projectID))
			}
			if row.CompletionGeneration.Valid || row.CompletedAt != nil || row.ReleaseEvidence.Valid || row.ReleaseSetDigest.Valid {
				return nil, corruptStateError("network project release", key, fmt.Errorf("releasing row contains completion fields"))
			}
			releases[projectID] = networkReleaseState{}
		case "completed":
			if row.ProjectId.Valid {
				return nil, corruptStateError("network project release", key, fmt.Errorf("completed row must clear its active project reference"))
			}
			if !row.CompletionGeneration.Valid || row.CompletionGeneration.Int64 <= int64(row.BeginGeneration) {
				return nil, corruptStateError("network project release", key, fmt.Errorf("completion generation must be present and greater than begin generation"))
			}
			if row.CompletedAt == nil {
				return nil, corruptStateError("network project release", key, fmt.Errorf("completion time is required"))
			}
			if err := validateNetworkFactTime("network project release completion time", *row.CompletedAt, updatedAt); err != nil {
				return nil, corruptStateError("network project release", key, err)
			}
			if row.CompletedAt.Before(row.BeganAt) {
				return nil, corruptStateError("network project release", key, fmt.Errorf("completion time precedes begin time"))
			}
			if !row.ReleaseEvidence.Valid {
				return nil, corruptStateError("network project release", key, fmt.Errorf("release evidence is required"))
			}
			if err := validateNetworkEvidence("project release evidence", row.ReleaseEvidence.String); err != nil {
				return nil, corruptStateError("network project release", key, err)
			}
			if !row.ReleaseSetDigest.Valid {
				return nil, corruptStateError("network project release", key, fmt.Errorf("release set digest is required"))
			}
			if err := validateProjectNetworkReleaseSetDigest(row.ReleaseSetDigest.String); err != nil {
				return nil, corruptStateError("network project release", key, err)
			}
			releases[projectID] = networkReleaseState{Completed: true, CompletedAt: *row.CompletedAt}
		default:
			return nil, corruptStateError("network project release", key, fmt.Errorf("state %q is unsupported", row.State))
		}
	}
	return releases, nil
}

// networkEndpointsFromModels converts every durable reservation before release suppression is applied.
func networkEndpointsFromModels(
	rows []models.PublicEndpointLease,
	stateID int,
	knownProjects map[domain.ProjectID]struct{},
	activeByID map[int]identity.Lease,
	listeners SharedListenerReservations,
	updatedAt time.Time,
) ([]EndpointReservation, error) {
	reservations := make([]EndpointReservation, 0, len(rows))
	seenIDs := make(map[int]struct{}, len(rows))
	seenKeys := make(map[EndpointReservationKey]struct{}, len(rows))
	seenHosts := make(map[string]struct{}, len(rows))
	seenTCPSockets := make(map[netip.AddrPort]struct{}, len(rows))
	for _, row := range rows {
		key := scopedKey(row.ProjectId, row.EndpointId, row.Id)
		if row.Id <= 0 {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		if row.NetworkStateId != stateID {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, stateID))
		}
		projectID := domain.ProjectID(row.ProjectId)
		reservationKey := EndpointReservationKey{ProjectID: projectID, EndpointID: row.EndpointId}
		if err := reservationKey.Validate(); err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		if _, exists := knownProjects[projectID]; !exists {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("project %q is missing", projectID))
		}
		if _, duplicate := seenKeys[reservationKey]; duplicate {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("project-scoped endpoint ID is duplicated"))
		}
		seenKeys[reservationKey] = struct{}{}
		if _, duplicate := seenHosts[row.Hostname]; duplicate {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("hostname %q is duplicated", row.Hostname))
		}
		seenHosts[row.Hostname] = struct{}{}
		public, err := networkAddressPortFromModel("public endpoint", row.Address, row.Port)
		if err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		generation, err := positiveNetworkGeneration("endpoint generation", row.Generation)
		if err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		if err := validateNetworkFactTime("network endpoint creation time", row.CreatedAt, updatedAt); err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		if err := validateNetworkFactTime("network endpoint update time", row.UpdatedAt, updatedAt); err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		if row.UpdatedAt.Before(row.CreatedAt) {
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("update time precedes creation time"))
		}
		reservation := EndpointReservation{
			Key:        reservationKey,
			Protocol:   EndpointProtocol(row.Protocol),
			Host:       row.Hostname,
			Public:     public,
			Generation: generation,
		}
		switch reservation.Protocol {
		case EndpointProtocolHTTP:
			if row.LoopbackAddressLeaseId.Valid {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("HTTP endpoint must not reference an address lease"))
			}
			if public != listeners.HTTPS.Advertised {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("HTTP endpoint must use the advertised HTTPS socket"))
			}
		case EndpointProtocolTCP:
			if !row.LoopbackAddressLeaseId.Valid || row.LoopbackAddressLeaseId.Int64 <= 0 {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("TCP endpoint must reference an active address lease"))
			}
			leaseID := int(row.LoopbackAddressLeaseId.Int64)
			if int64(leaseID) != row.LoopbackAddressLeaseId.Int64 {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("address lease ID exceeds the supported database range"))
			}
			lease, exists := activeByID[leaseID]
			if !exists {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("address lease %d is missing or not active", leaseID))
			}
			if lease.Key.ProjectID != projectID || lease.Address != public.Addr() {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("address lease does not own project %q at %s", projectID, public.Addr()))
			}
			if _, duplicate := seenTCPSockets[public]; duplicate {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("native socket %s is duplicated", public))
			}
			seenTCPSockets[public] = struct{}{}
			if owner, collision := sharedSocketOwner(listeners, public); collision {
				return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("native socket %s collides with %s", public, owner))
			}
			identityKey := lease.Key
			reservation.Identity = &identityKey
		default:
			return nil, corruptStateError("public endpoint lease", key, fmt.Errorf("protocol %q is unsupported", row.Protocol))
		}
		if err := reservation.Validate(); err != nil {
			return nil, corruptStateError("public endpoint lease", key, err)
		}
		reservations = append(reservations, reservation)
	}
	return canonicalEndpointReservations(reservations), nil
}

// validateCompletedNetworkReleases rejects teardown completion that still owns publishable resources.
func validateCompletedNetworkReleases(
	releases map[domain.ProjectID]networkReleaseState,
	knownProjects map[domain.ProjectID]struct{},
	activeByProject map[domain.ProjectID]int,
	quarantineByProject map[domain.ProjectID][]networkQuarantineState,
	endpoints []EndpointReservation,
) error {
	endpointCount := make(map[domain.ProjectID]int, len(releases))
	for _, endpoint := range endpoints {
		endpointCount[endpoint.Key.ProjectID]++
	}
	projectIDs := make([]domain.ProjectID, 0, len(releases))
	for projectID := range releases {
		projectIDs = append(projectIDs, projectID)
	}
	slices.Sort(projectIDs)
	for _, projectID := range projectIDs {
		release := releases[projectID]
		if !release.Completed {
			continue
		}
		if activeByProject[projectID] != 0 {
			return corruptStateError("network project release", string(projectID), fmt.Errorf("completed release retains active address leases"))
		}
		if endpointCount[projectID] != 0 {
			return corruptStateError("network project release", string(projectID), fmt.Errorf("completed release retains public endpoints"))
		}
		primaryQuarantine := false
		for _, quarantine := range quarantineByProject[projectID] {
			if quarantine.ReleasedAt.After(release.CompletedAt) {
				return corruptStateError("network project release", string(projectID), fmt.Errorf("source quarantine release time postdates project release completion"))
			}
			if quarantine.QuarantinedAt.After(release.CompletedAt) {
				return corruptStateError("network project release", string(projectID), fmt.Errorf("source quarantine time postdates project release completion"))
			}
			if quarantine.Key.Kind() == identity.LeaseKindPrimary {
				primaryQuarantine = true
			}
		}
		if _, projectExists := knownProjects[projectID]; projectExists && !primaryQuarantine {
			return corruptStateError("network project release", string(projectID), fmt.Errorf("completed release with retained project requires its source primary quarantine"))
		}
	}
	return nil
}

// networkAddressPortFromModel parses one exact durable IPv4 loopback socket.
func networkAddressPortFromModel(name string, addressValue string, portValue int) (netip.AddrPort, error) {
	address, err := parseCanonicalNetworkAddress(name+" address", addressValue)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if portValue <= 0 || portValue > 65535 {
		return netip.AddrPort{}, fmt.Errorf("%s port must be between 1 and 65535", name)
	}
	return netip.AddrPortFrom(address, uint16(portValue)), nil
}

// parseCanonicalNetworkAddress rejects textual aliases before they can evade socket or lease equality.
func parseCanonicalNetworkAddress(name string, value string) (netip.Addr, error) {
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s %q is invalid: %w", name, value, err)
	}
	address = address.Unmap()
	if !address.Is4() || !address.IsLoopback() {
		return netip.Addr{}, fmt.Errorf("%s %q must use IPv4 loopback", name, value)
	}
	if address.String() != value {
		return netip.Addr{}, fmt.Errorf("%s %q must use canonical IPv4 form %q", name, value, address)
	}
	return address, nil
}

// positiveNetworkGeneration converts an independent causal generation without treating it as a Harbor sequence owner.
func positiveNetworkGeneration(name string, value int) (uint64, error) {
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return uint64(value), nil
}

// validateNetworkFactTime rejects child facts that claim to occur after the root revision containing them.
func validateNetworkFactTime(name string, value time.Time, updatedAt time.Time) error {
	if err := validateStoredTime(name, value); err != nil {
		return err
	}
	if value.After(updatedAt) {
		return fmt.Errorf("%s must not be after the network state update time", name)
	}
	return nil
}

// validateNetworkEvidence applies the durable evidence byte and whitespace bounds shared by setup and teardown rows.
func validateNetworkEvidence(name string, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximumNetworkEvidenceLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumNetworkEvidenceLength)
	}
	return nil
}

// validateBoundedNetworkText rejects empty, padded, or oversized durable proof text.
func validateBoundedNetworkText(name string, value string, maximum int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", name, maximum)
	}
	return nil
}
