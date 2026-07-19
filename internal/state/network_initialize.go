package state

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// NetworkInitializationConflictError reports an initialization retry whose durable host facts differ from the request.
type NetworkInitializationConflictError struct {
	ActualRevision domain.Sequence
	Difference     string
}

// Error describes the initialized revision and the non-secret fact group that differs.
func (err *NetworkInitializationConflictError) Error() string {
	return fmt.Sprintf(
		"network is already initialized at revision %d with different %s",
		err.ActualRevision,
		err.Difference,
	)
}

// NetworkRevisionConflictError reports a network mutation attempted against an obsolete aggregate revision.
type NetworkRevisionConflictError struct {
	Expected domain.Sequence
	Actual   domain.Sequence
}

// Error describes the network-scoped optimistic concurrency mismatch.
func (err *NetworkRevisionConflictError) Error() string {
	return fmt.Sprintf(
		"network revision is %d, not expected revision %d",
		err.Actual,
		err.Expected,
	)
}

// NetworkProjectSetConflictError reports that initialization was planned against a different registered project set.
type NetworkProjectSetConflictError struct {
	Expected []domain.ProjectID
	Actual   []domain.ProjectID
}

// Error describes the expected and durable project identities in canonical order.
func (err *NetworkProjectSetConflictError) Error() string {
	return fmt.Sprintf("network project set is %v, not expected set %v", err.Actual, err.Expected)
}

// ProjectRevisionConflictError reports that a network mutation observed a project revision other than the planned one.
type ProjectRevisionConflictError struct {
	ProjectID domain.ProjectID
	Expected  domain.Sequence
	Actual    domain.Sequence
}

// Error describes the project-scoped optimistic concurrency mismatch.
func (err *ProjectRevisionConflictError) Error() string {
	return fmt.Sprintf(
		"project %q revision is %d, not expected revision %d",
		err.ProjectID,
		err.Actual,
		err.Expected,
	)
}

// InitializeNetwork commits the first durable network aggregate after its host postconditions have been verified.
func (store *Store) InitializeNetwork(
	ctx context.Context,
	request InitializeNetworkRequest,
) (NetworkMutationResult, error) {
	if err := request.Validate(); err != nil {
		return NetworkMutationResult{}, err
	}
	request = cloneInitializeNetworkRequest(request)
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkMutationResult{}, err
	}

	var result NetworkMutationResult
	err := store.mutations.mutate(ctx, "network initialization", func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("network persistence schema is not installed")
		}

		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		current, initialized, err := networkRecordFromModels(rows)
		if err != nil {
			return err
		}
		if _, err := validateRetainedSequenceBounds(tx); err != nil {
			return err
		}
		if err := validateNetworkInitializationProjects(tx, request.ExpectedProjects, initialized); err != nil {
			return err
		}

		if initialized {
			if difference := networkInitializationDifference(rows, request); difference != "" {
				return &NetworkInitializationConflictError{
					ActualRevision: current.Revision,
					Difference:     difference,
				}
			}
			result = NetworkMutationResult{Record: current, Replayed: true}
			return result.Validate()
		}

		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		if err := insertNetworkInitialization(tx, request, sequence); err != nil {
			return err
		}

		persistedRows, err := readNetworkModelRows(tx)
		if err != nil {
			return fmt.Errorf("read initialized network state: %w", err)
		}
		persisted, exists, err := networkRecordFromModels(persistedRows)
		if err != nil {
			return err
		}
		if !exists {
			return corruptStateError("network state", "1", fmt.Errorf("initialized aggregate is missing after insert"))
		}
		if persisted.Revision != sequence {
			return corruptStateError(
				"network state",
				"1",
				fmt.Errorf("readback revision is %d, expected %d", persisted.Revision, sequence),
			)
		}
		if difference := networkInitializationDifference(persistedRows, request); difference != "" {
			return corruptStateError("network state", "1", fmt.Errorf("readback differs in %s", difference))
		}
		expected := networkInitializationProjection(request, sequence)
		if err := validateNetworkInitializationProjection(persisted, expected); err != nil {
			return err
		}
		if err := validateNetworkSequenceExclusivity(tx, sequence); err != nil {
			return err
		}
		result = NetworkMutationResult{Record: persisted, Replayed: false}
		return result.Validate()
	})
	if err != nil {
		return NetworkMutationResult{}, fmt.Errorf("initialize network state: %w", err)
	}
	return result, nil
}

// cloneInitializeNetworkRequest isolates the queued transaction from caller-owned slice and pointer mutation.
func cloneInitializeNetworkRequest(request InitializeNetworkRequest) InitializeNetworkRequest {
	request.ExpectedProjects = slices.Clone(request.ExpectedProjects)
	request.Setup = slices.Clone(request.Setup)
	request.Ensures = slices.Clone(request.Ensures)
	request.Endpoints = canonicalEndpointReservations(request.Endpoints)
	request.At = canonicalNetworkMutationTime(request.At)
	for index := range request.Setup {
		request.Setup[index].VerifiedAt = canonicalNetworkMutationTime(request.Setup[index].VerifiedAt)
	}
	for index := range request.Ensures {
		request.Ensures[index].LeasedAt = canonicalNetworkMutationTime(request.Ensures[index].LeasedAt)
	}
	request.Listeners.DNS.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.DNS.VerifiedAt)
	request.Listeners.HTTP.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.HTTP.VerifiedAt)
	request.Listeners.HTTPS.VerifiedAt = canonicalNetworkMutationTime(request.Listeners.HTTPS.VerifiedAt)
	return request
}

// canonicalNetworkMutationTime removes monotonic process metadata that SQLite cannot preserve.
func canonicalNetworkMutationTime(value time.Time) time.Time {
	return value.UTC().Round(0)
}

// validateNetworkInitializationProjects proves the exact project set and every project's global sequence ownership.
func validateNetworkInitializationProjects(
	tx *gorm.DB,
	expectations []NetworkProjectRevision,
	allowAdvancedRevisions bool,
) error {
	var rows []models.Project
	if err := tx.Order("project_id ASC").Order("id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("read network initialization projects: %w", err)
	}

	expectedIDs := make([]domain.ProjectID, 0, len(expectations))
	expectedRevisions := make(map[domain.ProjectID]domain.Sequence, len(expectations))
	for _, expectation := range expectations {
		expectedIDs = append(expectedIDs, expectation.ProjectID)
		expectedRevisions[expectation.ProjectID] = expectation.Revision
	}
	actualIDs := make([]domain.ProjectID, 0, len(rows))
	seenIDs := make(map[int]struct{}, len(rows))
	seenProjects := make(map[domain.ProjectID]struct{}, len(rows))
	for _, row := range rows {
		key := durableKey(row.ProjectId, row.Id)
		if row.Id <= 0 {
			return corruptStateError("project", key, fmt.Errorf("database ID must be positive"))
		}
		if _, duplicate := seenIDs[row.Id]; duplicate {
			return corruptStateError("project", key, fmt.Errorf("database ID is duplicated"))
		}
		seenIDs[row.Id] = struct{}{}
		projectID := domain.ProjectID(row.ProjectId)
		if err := projectID.Validate(); err != nil {
			return corruptStateError("project", key, err)
		}
		if _, duplicate := seenProjects[projectID]; duplicate {
			return corruptStateError("project", key, fmt.Errorf("project ID is duplicated"))
		}
		seenProjects[projectID] = struct{}{}
		actualRevision, err := sequenceToModelInt("project revision", domain.Sequence(row.Revision), false)
		if err != nil || actualRevision != row.Revision {
			if err == nil {
				err = fmt.Errorf("project revision cannot be represented")
			}
			return corruptStateError("project", key, err)
		}
		actualIDs = append(actualIDs, projectID)
	}
	if !slices.Equal(actualIDs, expectedIDs) {
		return &NetworkProjectSetConflictError{
			Expected: slices.Clone(expectedIDs),
			Actual:   slices.Clone(actualIDs),
		}
	}

	for _, row := range rows {
		projectID := domain.ProjectID(row.ProjectId)
		actual := domain.Sequence(row.Revision)
		expected := expectedRevisions[projectID]
		if actual != expected && (!allowAdvancedRevisions || actual < expected) {
			return &ProjectRevisionConflictError{
				ProjectID: projectID,
				Expected:  expected,
				Actual:    actual,
			}
		}
		if err := validateProjectMutationSequenceOwner(tx, row); err != nil {
			return err
		}
	}
	return nil
}

// insertNetworkInitialization writes the aggregate in parent-before-child and lease-before-endpoint order.
func insertNetworkInitialization(tx *gorm.DB, request InitializeNetworkRequest, sequence domain.Sequence) error {
	if err := insertNetworkInitializationFoundation(
		tx,
		NetworkStageFull,
		request.Ownership,
		request.Pool,
		request.PoolGeneration,
		request.Setup,
		request.At,
		sequence,
	); err != nil {
		return err
	}

	for _, listener := range networkInitializationListeners(request.Listeners) {
		row := models.NetworkSharedListener{
			NetworkStateId:    networkStateSingletonID,
			Kind:              listener.kind,
			Mode:              string(listener.reservation.Mode),
			AdvertisedAddress: listener.reservation.Advertised.Addr().String(),
			AdvertisedPort:    int(listener.reservation.Advertised.Port()),
			BindAddress:       listener.reservation.Bind.Addr().String(),
			BindPort:          int(listener.reservation.Bind.Port()),
			Generation:        int(listener.reservation.Generation),
			VerifiedAt:        listener.reservation.VerifiedAt,
		}
		if err := requireOneCreate(tx.Create(&row), "create network shared listener", listener.kind); err != nil {
			return err
		}
	}

	leaseIDs := make(map[identity.LeaseKey]int, len(request.Ensures))
	for _, ensure := range request.Ensures {
		row := models.LoopbackAddressLease{
			NetworkStateId:          networkStateSingletonID,
			ProjectId:               null.StringFrom(string(ensure.Lease.Key.ProjectID)),
			SourceProjectId:         string(ensure.Lease.Key.ProjectID),
			Kind:                    string(ensure.Lease.Key.Kind()),
			SecondaryId:             ensure.Lease.Key.SecondaryID,
			Address:                 ensure.Lease.Address.String(),
			State:                   "leased",
			LeaseGeneration:         int(ensure.Generation),
			OwnershipInstallationId: string(ensure.Lease.Ownership.InstallationID),
			OwnershipGeneration:     int(ensure.Lease.Ownership.Generation),
			EnsureEvidence:          ensure.EnsureEvidence,
			LeasedAt:                ensure.LeasedAt,
		}
		key := networkInitializationLeaseKey(ensure.Lease.Key)
		if err := requireOneCreate(tx.Create(&row), "create loopback address lease", key); err != nil {
			return err
		}
		if row.Id <= 0 {
			return corruptStateError("loopback address lease", key, fmt.Errorf("insert did not return a positive database ID"))
		}
		leaseIDs[ensure.Lease.Key] = row.Id
	}

	for _, endpoint := range request.Endpoints {
		row := models.PublicEndpointLease{
			NetworkStateId: networkStateSingletonID,
			ProjectId:      string(endpoint.Key.ProjectID),
			EndpointId:     endpoint.Key.EndpointID,
			Protocol:       string(endpoint.Protocol),
			Hostname:       endpoint.Host,
			Address:        endpoint.Public.Addr().String(),
			Port:           int(endpoint.Public.Port()),
			Generation:     int(endpoint.Generation),
			CreatedAt:      request.At,
			UpdatedAt:      request.At,
		}
		if endpoint.Identity != nil {
			leaseID, exists := leaseIDs[*endpoint.Identity]
			if !exists {
				return corruptStateError(
					"public endpoint lease",
					networkInitializationEndpointKey(endpoint.Key),
					fmt.Errorf("requested identity has no inserted address lease"),
				)
			}
			row.LoopbackAddressLeaseId = null.IntFrom(int64(leaseID))
		}
		if err := requireOneCreate(
			tx.Create(&row),
			"create public endpoint lease",
			networkInitializationEndpointKey(endpoint.Key),
		); err != nil {
			return err
		}
	}
	return nil
}

// insertNetworkInitializationFoundation writes the lifecycle root and identity authority shared by both initialization stages.
func insertNetworkInitializationFoundation(
	tx *gorm.DB,
	stage NetworkStage,
	ownership identity.Ownership,
	pool identity.Pool,
	poolGeneration uint64,
	setup []NetworkSetupProof,
	at time.Time,
	sequence domain.Sequence,
) error {
	root := models.NetworkState{
		Id:                  networkStateSingletonID,
		Stage:               string(stage),
		InstallationId:      string(ownership.InstallationID),
		OwnershipGeneration: int(ownership.Generation),
		PoolNetwork:         pool.Prefix().Addr().String(),
		PoolPrefixLength:    pool.Prefix().Bits(),
		DnsSuffix:           ".test",
		CreatedAt:           at,
		UpdatedAt:           at,
		Revision:            int(sequence),
	}
	if err := requireOneCreate(tx.Create(&root), "create network state", "1"); err != nil {
		return err
	}

	for index, address := range pool.Candidates() {
		row := models.NetworkPoolCandidate{
			NetworkStateId: networkStateSingletonID,
			Ordinal:        index + 1,
			Address:        address.String(),
			Generation:     int(poolGeneration),
		}
		if err := requireOneCreate(
			tx.Create(&row),
			"create network pool candidate",
			address.String(),
		); err != nil {
			return err
		}
	}

	for _, proof := range setup {
		row := models.NetworkSetupEvidence{
			NetworkStateId: networkStateSingletonID,
			Component:      string(proof.Component),
			Evidence:       proof.Evidence,
			Generation:     int(proof.Generation),
			VerifiedAt:     proof.VerifiedAt,
		}
		if err := requireOneCreate(
			tx.Create(&row),
			"create network setup evidence",
			string(proof.Component),
		); err != nil {
			return err
		}
	}

	return nil
}

// networkInitializationListener pairs the fixed durable kind with its validated reservation.
type networkInitializationListener struct {
	kind        string
	reservation ListenerReservation
}

// networkInitializationListeners returns the stable listener insertion and comparison order.
func networkInitializationListeners(listeners SharedListenerReservations) []networkInitializationListener {
	return []networkInitializationListener{
		{kind: "dns", reservation: listeners.DNS},
		{kind: "http", reservation: listeners.HTTP},
		{kind: "https", reservation: listeners.HTTPS},
	}
}

// networkInitializationDifference compares every durable initialization fact while excluding surrogate IDs and revision.
func networkInitializationDifference(rows networkModelRows, request InitializeNetworkRequest) string {
	if len(rows.States) != 1 {
		return "network root"
	}
	root := rows.States[0]
	if root.Stage != string(NetworkStageFull) {
		return "network stage"
	}
	if root.Id != networkStateSingletonID ||
		root.InstallationId != string(request.Ownership.InstallationID) ||
		root.OwnershipGeneration != int(request.Ownership.Generation) {
		return "network ownership"
	}
	if root.PoolNetwork != request.Pool.Prefix().Addr().String() ||
		root.PoolPrefixLength != request.Pool.Prefix().Bits() ||
		root.DnsSuffix != ".test" {
		return "network pool"
	}
	if !root.CreatedAt.Equal(request.At) || !root.UpdatedAt.Equal(request.At) {
		return "network root timestamps"
	}
	if len(rows.Releases) != 0 {
		return "project release state"
	}
	if difference := networkInitializationCandidateDifference(rows.Candidates, request.Pool, request.PoolGeneration); difference != "" {
		return difference
	}
	if difference := networkInitializationSetupDifference(rows.SetupEvidence, request.Setup); difference != "" {
		return difference
	}
	if difference := networkInitializationListenerDifference(rows.Listeners, request); difference != "" {
		return difference
	}
	leaseKeys, difference := networkInitializationLeaseDifference(rows.Leases, request)
	if difference != "" {
		return difference
	}
	return networkInitializationEndpointDifference(rows.Endpoints, request, leaseKeys)
}

// networkInitializationCandidateDifference compares the hidden pool generation and deterministic ordinals.
func networkInitializationCandidateDifference(rows []models.NetworkPoolCandidate, pool identity.Pool, generation uint64) string {
	candidates := pool.Candidates()
	if len(rows) != len(candidates) {
		return "network pool candidates"
	}
	for index, row := range rows {
		if row.NetworkStateId != networkStateSingletonID ||
			row.Ordinal != index+1 ||
			row.Address != candidates[index].String() ||
			row.Generation != int(generation) {
			return "network pool candidates"
		}
	}
	return ""
}

// networkInitializationSetupDifference compares every sanitized setup proof without exposing evidence in diagnostics.
func networkInitializationSetupDifference(rows []models.NetworkSetupEvidence, setup []NetworkSetupProof) string {
	if len(rows) != len(setup) {
		return "network setup proofs"
	}
	byComponent := make(map[string]models.NetworkSetupEvidence, len(rows))
	for _, row := range rows {
		byComponent[row.Component] = row
	}
	for _, proof := range setup {
		row, exists := byComponent[string(proof.Component)]
		if !exists ||
			row.NetworkStateId != networkStateSingletonID ||
			row.Evidence != proof.Evidence ||
			row.Generation != int(proof.Generation) ||
			!row.VerifiedAt.Equal(proof.VerifiedAt) {
			return "network setup proofs"
		}
	}
	return ""
}

// networkInitializationListenerDifference compares all shared advertised and bind postconditions.
func networkInitializationListenerDifference(rows []models.NetworkSharedListener, request InitializeNetworkRequest) string {
	listeners := networkInitializationListeners(request.Listeners)
	if len(rows) != len(listeners) {
		return "network listeners"
	}
	byKind := make(map[string]models.NetworkSharedListener, len(rows))
	for _, row := range rows {
		byKind[row.Kind] = row
	}
	for _, listener := range listeners {
		row, exists := byKind[listener.kind]
		reservation := listener.reservation
		if !exists ||
			row.NetworkStateId != networkStateSingletonID ||
			row.Mode != string(reservation.Mode) ||
			row.AdvertisedAddress != reservation.Advertised.Addr().String() ||
			row.AdvertisedPort != int(reservation.Advertised.Port()) ||
			row.BindAddress != reservation.Bind.Addr().String() ||
			row.BindPort != int(reservation.Bind.Port()) ||
			row.Generation != int(reservation.Generation) ||
			!row.VerifiedAt.Equal(reservation.VerifiedAt) {
			return "network listeners"
		}
	}
	return ""
}

// networkInitializationLeaseDifference compares active lease authority, generations, evidence, and observation times.
func networkInitializationLeaseDifference(
	rows []models.LoopbackAddressLease,
	request InitializeNetworkRequest,
) (map[int]identity.LeaseKey, string) {
	if len(rows) != len(request.Ensures) {
		return nil, "network lease ensures"
	}
	byKey := make(map[identity.LeaseKey]models.LoopbackAddressLease, len(rows))
	keysByID := make(map[int]identity.LeaseKey, len(rows))
	for _, row := range rows {
		key := identity.LeaseKey{ProjectID: domain.ProjectID(row.SourceProjectId), SecondaryID: row.SecondaryId}
		byKey[key] = row
		keysByID[row.Id] = key
	}
	for _, ensure := range request.Ensures {
		row, exists := byKey[ensure.Lease.Key]
		if !exists ||
			row.NetworkStateId != networkStateSingletonID ||
			!row.ProjectId.Valid || row.ProjectId.String != string(ensure.Lease.Key.ProjectID) ||
			row.SourceProjectId != string(ensure.Lease.Key.ProjectID) ||
			row.Kind != string(ensure.Lease.Key.Kind()) ||
			row.SecondaryId != ensure.Lease.Key.SecondaryID ||
			row.Address != ensure.Lease.Address.String() ||
			row.State != "leased" ||
			row.LeaseGeneration != int(ensure.Generation) ||
			row.OwnershipInstallationId != string(ensure.Lease.Ownership.InstallationID) ||
			row.OwnershipGeneration != int(ensure.Lease.Ownership.Generation) ||
			row.EnsureEvidence != ensure.EnsureEvidence ||
			!row.LeasedAt.Equal(ensure.LeasedAt) ||
			row.ReleaseGeneration.Valid || row.ReleaseEvidence.Valid ||
			row.ReleasedAt != nil || row.QuarantinedAt != nil || row.ReuseAfter != nil || row.QuarantineReason.Valid {
			return nil, "network lease ensures"
		}
	}
	return keysByID, ""
}

// networkInitializationEndpointDifference compares public reservations and their exact lease identity joins.
func networkInitializationEndpointDifference(
	rows []models.PublicEndpointLease,
	request InitializeNetworkRequest,
	leaseKeys map[int]identity.LeaseKey,
) string {
	if len(rows) != len(request.Endpoints) {
		return "network endpoints"
	}
	byKey := make(map[EndpointReservationKey]models.PublicEndpointLease, len(rows))
	for _, row := range rows {
		key := EndpointReservationKey{ProjectID: domain.ProjectID(row.ProjectId), EndpointID: row.EndpointId}
		byKey[key] = row
	}
	for _, endpoint := range request.Endpoints {
		row, exists := byKey[endpoint.Key]
		if !exists ||
			row.NetworkStateId != networkStateSingletonID ||
			row.Protocol != string(endpoint.Protocol) ||
			row.Hostname != endpoint.Host ||
			row.Address != endpoint.Public.Addr().String() ||
			row.Port != int(endpoint.Public.Port()) ||
			row.Generation != int(endpoint.Generation) ||
			!row.CreatedAt.Equal(request.At) ||
			!row.UpdatedAt.Equal(request.At) {
			return "network endpoints"
		}
		if endpoint.Identity == nil {
			if row.LoopbackAddressLeaseId.Valid {
				return "network endpoint identities"
			}
			continue
		}
		if !row.LoopbackAddressLeaseId.Valid || row.LoopbackAddressLeaseId.Int64 <= 0 {
			return "network endpoint identities"
		}
		leaseID := int(row.LoopbackAddressLeaseId.Int64)
		if int64(leaseID) != row.LoopbackAddressLeaseId.Int64 || leaseKeys[leaseID] != *endpoint.Identity {
			return "network endpoint identities"
		}
	}
	return ""
}

// networkInitializationProjection builds the payload-safe record expected from a successful readback.
func networkInitializationProjection(request InitializeNetworkRequest, sequence domain.Sequence) NetworkRecord {
	leases := make([]identity.Lease, 0, len(request.Ensures))
	for _, ensure := range request.Ensures {
		leases = append(leases, ensure.Lease)
	}
	return NetworkRecord{
		Stage:       NetworkStageFull,
		Revision:    sequence,
		CreatedAt:   request.At,
		UpdatedAt:   request.At,
		Ownership:   request.Ownership,
		Pool:        request.Pool,
		Leases:      canonicalNetworkLeases(leases),
		Quarantines: []identity.Quarantine{},
		Reservations: DataPlaneReservations{
			Listeners:            request.Listeners,
			Endpoints:            canonicalEndpointReservations(request.Endpoints),
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
}

// validateNetworkInitializationProjection keeps the public readback equal to the host plan without comparing hidden authority.
func validateNetworkInitializationProjection(persisted NetworkRecord, expected NetworkRecord) error {
	if !reflect.DeepEqual(persisted, expected) {
		return corruptStateError("network state", "1", fmt.Errorf("readback aggregate differs from the requested projection"))
	}
	return nil
}

// networkInitializationLeaseKey formats a project-scoped lease key without exposing host evidence.
func networkInitializationLeaseKey(key identity.LeaseKey) string {
	if key.SecondaryID == "" {
		return string(key.ProjectID) + "/primary"
	}
	return string(key.ProjectID) + "/secondary/" + key.SecondaryID
}

// networkInitializationEndpointKey formats one durable endpoint's natural key.
func networkInitializationEndpointKey(key EndpointReservationKey) string {
	return string(key.ProjectID) + "/" + key.EndpointID
}
