package state

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
)

// ProjectNetworkReleaseState identifies the durable teardown boundary reached by one unregister operation.
type ProjectNetworkReleaseState string

const (
	// ProjectNetworkReleaseReleasing suppresses public routes while retaining exact host-release recovery facts.
	ProjectNetworkReleaseReleasing ProjectNetworkReleaseState = "releasing"
	// ProjectNetworkReleaseCompleted proves every public route and host identity has been withdrawn.
	ProjectNetworkReleaseCompleted ProjectNetworkReleaseState = "completed"
)

// Validate rejects states outside the two forward-only network teardown boundaries.
func (state ProjectNetworkReleaseState) Validate() error {
	switch state {
	case ProjectNetworkReleaseReleasing, ProjectNetworkReleaseCompleted:
		return nil
	default:
		return fmt.Errorf("project network release state %q is unsupported", state)
	}
}

// ProjectNetworkReleaseCompletion is the bounded proof that host teardown finished after staging.
type ProjectNetworkReleaseCompletion struct {
	Generation       uint64
	CompletedAt      time.Time
	Evidence         string
	ReleaseSetDigest string
}

// Validate rejects completion facts that cannot be persisted as durable recovery evidence.
func (completion ProjectNetworkReleaseCompletion) Validate() error {
	if _, err := unsignedToModelInt("project network release completion generation", completion.Generation, false); err != nil {
		return err
	}
	if err := validateStoredTime("project network release completion time", completion.CompletedAt); err != nil {
		return err
	}
	if err := validateNetworkEvidence("project network release completion evidence", completion.Evidence); err != nil {
		return err
	}
	return validateProjectNetworkReleaseSetDigest(completion.ReleaseSetDigest)
}

// ProjectNetworkReleaseRecord is the durable recovery projection for one unregister operation.
type ProjectNetworkReleaseRecord struct {
	ProjectID       domain.ProjectID
	OperationID     domain.OperationID
	State           ProjectNetworkReleaseState
	BeginGeneration uint64
	BeganAt         time.Time
	Completion      *ProjectNetworkReleaseCompletion
	ActiveLeases    []NetworkLeaseEnsure
	Endpoints       []EndpointReservation
}

// Validate rejects release records whose lifecycle or hidden recovery facts are contradictory.
func (record ProjectNetworkReleaseRecord) Validate() error {
	if err := record.ProjectID.Validate(); err != nil {
		return err
	}
	if err := record.OperationID.Validate(); err != nil {
		return err
	}
	if err := record.State.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("project network release begin generation", record.BeginGeneration, false); err != nil {
		return err
	}
	if err := validateStoredTime("project network release begin time", record.BeganAt); err != nil {
		return err
	}
	if record.ActiveLeases == nil {
		return fmt.Errorf("project network release active leases must be initialized")
	}
	if record.Endpoints == nil {
		return fmt.Errorf("project network release endpoints must be initialized")
	}

	if record.State == ProjectNetworkReleaseReleasing {
		if record.Completion != nil {
			return fmt.Errorf("releasing project network record must not contain completion facts")
		}
		if err := validateNetworkLeaseEnsures(record.ActiveLeases, record.ProjectID, record.BeganAt); err != nil {
			return err
		}
		if err := validateNetworkMutationEndpoints(record.Endpoints, record.ProjectID); err != nil {
			return err
		}
		return validateProjectNetworkReleaseTopology(record.ProjectID, record.ActiveLeases, record.Endpoints)
	}
	if record.Completion == nil {
		return fmt.Errorf("completed project network record requires completion facts")
	}
	if err := record.Completion.Validate(); err != nil {
		return err
	}
	if record.Completion.Generation <= record.BeginGeneration {
		return fmt.Errorf("project network release completion generation must exceed begin generation")
	}
	if record.Completion.CompletedAt.Before(record.BeganAt) {
		return fmt.Errorf("project network release completion time must not precede begin time")
	}
	if len(record.ActiveLeases) != 0 || len(record.Endpoints) != 0 {
		return fmt.Errorf("completed project network record must not retain active leases or endpoints")
	}
	return nil
}

// BeginProjectNetworkReleaseRequest stages route suppression for one running unregister operation.
type BeginProjectNetworkReleaseRequest struct {
	ProjectID                 domain.ProjectID
	OperationID               domain.OperationID
	ExpectedNetworkRevision   domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	ExpectedOperationRevision domain.Sequence
	BeginGeneration           uint64
	At                        time.Time
}

// Validate rejects stale-shaped begin requests before persistence can suppress public routes.
func (request BeginProjectNetworkReleaseRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateProjectNetworkReleaseRevisions(
		request.ExpectedNetworkRevision,
		request.ExpectedProjectRevision,
		request.ExpectedOperationRevision,
	); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("project network release begin generation", request.BeginGeneration, false); err != nil {
		return err
	}
	return validateStoredTime("project network release begin time", request.At)
}

// CompleteProjectNetworkReleaseRequest commits completed helper effects and the final host teardown proof.
type CompleteProjectNetworkReleaseRequest struct {
	ProjectID                 domain.ProjectID
	OperationID               domain.OperationID
	ExpectedNetworkRevision   domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	ExpectedOperationRevision domain.Sequence
	ExpectedBeginGeneration   uint64
	CompletionGeneration      uint64
	Releases                  []NetworkLeaseRelease
	ReleaseEvidence           string
	At                        time.Time
}

// Validate rejects incomplete completion requests before persistence can clear project ownership.
func (request CompleteProjectNetworkReleaseRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateProjectNetworkReleaseRevisions(
		request.ExpectedNetworkRevision,
		request.ExpectedProjectRevision,
		request.ExpectedOperationRevision,
	); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected project network release begin generation", request.ExpectedBeginGeneration, false); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("project network release completion generation", request.CompletionGeneration, false); err != nil {
		return err
	}
	if request.CompletionGeneration <= request.ExpectedBeginGeneration {
		return fmt.Errorf("project network release completion generation must exceed expected begin generation")
	}
	if err := validateStoredTime("project network release completion time", request.At); err != nil {
		return err
	}
	if err := validateNetworkEvidence("project network release completion evidence", request.ReleaseEvidence); err != nil {
		return err
	}
	if request.Releases == nil {
		return fmt.Errorf("project network lease releases must be initialized")
	}
	if err := validateNetworkLeaseReleases(request.Releases, request.ProjectID, request.At); err != nil {
		return err
	}
	for _, release := range request.Releases {
		if release.Lease.Key.Kind() == identity.LeaseKindPrimary {
			return nil
		}
	}
	return fmt.Errorf("project network release requires the project's primary lease")
}

// ProjectNetworkReleaseMutationResult returns the safe network and recovery projections from one mutation instant.
type ProjectNetworkReleaseMutationResult struct {
	// Record is the complete public aggregate read from durable state.
	Record NetworkRecord
	// Release is the exact recovery projection owned by the unregister operation.
	Release ProjectNetworkReleaseRecord
	// Replayed reports semantic equality, not proof that this exact call previously committed.
	Replayed bool
}

// Validate rejects results that do not expose one suppressed release boundary consistently.
func (result ProjectNetworkReleaseMutationResult) Validate() error {
	if err := result.Record.Validate(); err != nil {
		return fmt.Errorf("project network release mutation result: %w", err)
	}
	if err := result.Release.Validate(); err != nil {
		return fmt.Errorf("project network release mutation result: %w", err)
	}
	_, suppressed := slices.BinarySearch(result.Record.Reservations.SuppressedProjectIDs, result.Release.ProjectID)
	if !suppressed {
		return fmt.Errorf("project network release mutation result does not suppress project %q", result.Release.ProjectID)
	}
	if result.Release.BeganAt.After(result.Record.UpdatedAt) {
		return fmt.Errorf("project network release mutation result begins after the network revision")
	}
	if result.Release.Completion != nil && result.Release.Completion.CompletedAt.After(result.Record.UpdatedAt) {
		return fmt.Errorf("project network release mutation result completes after the network revision")
	}
	targetLeases := make(map[identity.LeaseKey]identity.Lease, len(result.Release.ActiveLeases))
	for _, lease := range result.Record.Leases {
		if lease.Key.ProjectID == result.Release.ProjectID {
			targetLeases[lease.Key] = lease
		}
	}
	if result.Release.State == ProjectNetworkReleaseReleasing {
		if err := validateProjectNetworkReleaseVisibility(
			result.Record.Reservations.Listeners,
			result.Record.Reservations.Endpoints,
			result.Release.Endpoints,
		); err != nil {
			return err
		}
		if len(targetLeases) != len(result.Release.ActiveLeases) {
			return fmt.Errorf("releasing project network release mutation result has inconsistent active leases")
		}
		for _, ensure := range result.Release.ActiveLeases {
			if targetLeases[ensure.Lease.Key] != ensure.Lease {
				return fmt.Errorf("releasing project network release mutation result has inconsistent active leases")
			}
		}
	}
	if result.Release.State == ProjectNetworkReleaseCompleted {
		if len(targetLeases) != 0 {
			return fmt.Errorf("completed project network release mutation result retains an active project lease")
		}
	}
	return nil
}

// ProjectNetworkReleaseConflictError reports durable release facts that differ from one semantic retry.
type ProjectNetworkReleaseConflictError struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	Difference  string
}

// Error describes the non-secret release fact that differs from durable state.
func (err *ProjectNetworkReleaseConflictError) Error() string {
	return fmt.Sprintf(
		"project %q network release operation %q conflicts in %s",
		err.ProjectID,
		err.OperationID,
		err.Difference,
	)
}

// ProjectNetworkReleaseIncompleteError reports unregister completion attempted before host teardown completed.
type ProjectNetworkReleaseIncompleteError struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	State       ProjectNetworkReleaseState
}

// Error describes the release state that still prevents project deletion.
func (err *ProjectNetworkReleaseIncompleteError) Error() string {
	return fmt.Sprintf(
		"project %q network release operation %q is %q, not completed",
		err.ProjectID,
		err.OperationID,
		err.State,
	)
}

// ProjectNetworkReleaseNotFoundError reports an initialized project whose unregister operation never staged network teardown.
type ProjectNetworkReleaseNotFoundError struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
}

// Error describes the missing release boundary required before project deletion.
func (err *ProjectNetworkReleaseNotFoundError) Error() string {
	return fmt.Sprintf(
		"project %q network release operation %q was not started",
		err.ProjectID,
		err.OperationID,
	)
}

// ProjectNetworkReleaseActiveError reports a project mutation frozen behind durable network teardown.
type ProjectNetworkReleaseActiveError struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	State       ProjectNetworkReleaseState
	Action      string
}

// Error describes the blocked action and the non-secret release boundary that owns the project.
func (err *ProjectNetworkReleaseActiveError) Error() string {
	return fmt.Sprintf(
		"project %q cannot %s while network release operation %q is %q",
		err.ProjectID,
		err.Action,
		err.OperationID,
		err.State,
	)
}

// validateProjectNetworkReleaseRevisions keeps all three global owners pairwise distinct.
func validateProjectNetworkReleaseRevisions(network domain.Sequence, project domain.Sequence, operation domain.Sequence) error {
	for _, candidate := range []struct {
		name  string
		value domain.Sequence
	}{
		{name: "expected network revision", value: network},
		{name: "expected project revision", value: project},
		{name: "expected operation revision", value: operation},
	} {
		if _, err := sequenceToModelInt(candidate.name, candidate.value, false); err != nil {
			return err
		}
	}
	if network == project || network == operation || project == operation {
		return fmt.Errorf("expected network, project, and operation revisions must be pairwise distinct")
	}
	return nil
}

// validateProjectNetworkReleaseTopology binds every staged TCP endpoint to one exact retained project lease.
func validateProjectNetworkReleaseTopology(
	projectID domain.ProjectID,
	leases []NetworkLeaseEnsure,
	endpoints []EndpointReservation,
) error {
	byKey := make(map[identity.LeaseKey]identity.Lease, len(leases))
	primary := false
	for _, ensure := range leases {
		byKey[ensure.Lease.Key] = ensure.Lease
		if ensure.Lease.Key.Kind() == identity.LeaseKindPrimary {
			primary = true
		}
	}
	if !primary {
		return fmt.Errorf("project network release for %q requires its primary lease", projectID)
	}
	for _, endpoint := range endpoints {
		if endpoint.Protocol != EndpointProtocolTCP {
			continue
		}
		lease, exists := byKey[*endpoint.Identity]
		if !exists {
			return fmt.Errorf("project network release endpoint %q references an unknown active lease", endpoint.Host)
		}
		if lease.Address != endpoint.Public.Addr() {
			return fmt.Errorf("project network release endpoint %q does not use its active lease address", endpoint.Host)
		}
	}
	return nil
}

// validateProjectNetworkReleaseVisibility proves hidden recovery routes cannot alias the public projection.
func validateProjectNetworkReleaseVisibility(
	listeners SharedListenerReservations,
	visible []EndpointReservation,
	hidden []EndpointReservation,
) error {
	visibleHosts := make(map[string]struct{}, len(visible))
	visibleTCPSockets := make(map[netip.AddrPort]struct{}, len(visible))
	for _, endpoint := range visible {
		visibleHosts[endpoint.Host] = struct{}{}
		if endpoint.Protocol == EndpointProtocolTCP {
			visibleTCPSockets[endpoint.Public] = struct{}{}
		}
	}
	for _, endpoint := range hidden {
		if endpoint.Protocol == EndpointProtocolHTTP && endpoint.Public != listeners.HTTPS.Advertised {
			return fmt.Errorf("hidden HTTP endpoint %q does not use the advertised HTTPS socket", endpoint.Host)
		}
		if _, collision := visibleHosts[endpoint.Host]; collision {
			return fmt.Errorf("hidden endpoint host %q collides with the public projection", endpoint.Host)
		}
		if endpoint.Protocol == EndpointProtocolTCP {
			if _, collision := visibleTCPSockets[endpoint.Public]; collision {
				return fmt.Errorf("hidden native endpoint socket %s collides with the public projection", endpoint.Public)
			}
			if owner, collision := sharedSocketOwner(listeners, endpoint.Public); collision {
				return fmt.Errorf("hidden native endpoint %q socket %s collides with %s", endpoint.Host, endpoint.Public, owner)
			}
		}
	}
	return nil
}
