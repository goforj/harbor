package state

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certroot"
)

// GlobalNetworkReleaseTrustDisposition identifies whether Harbor may remove the observed trust entry.
type GlobalNetworkReleaseTrustDisposition string

const (
	// GlobalNetworkReleaseTrustOwned permits removal of the exact Harbor-owned trust entry.
	GlobalNetworkReleaseTrustOwned GlobalNetworkReleaseTrustDisposition = "owned"
	// GlobalNetworkReleaseTrustPreexistingUnowned preserves a matching entry that Harbor did not install.
	GlobalNetworkReleaseTrustPreexistingUnowned GlobalNetworkReleaseTrustDisposition = "preexisting_unowned"
)

// Validate rejects trust dispositions that would make teardown ownership ambiguous.
func (disposition GlobalNetworkReleaseTrustDisposition) Validate() error {
	switch disposition {
	case GlobalNetworkReleaseTrustOwned, GlobalNetworkReleaseTrustPreexistingUnowned:
		return nil
	default:
		return fmt.Errorf("global network release trust disposition %q is unsupported", disposition)
	}
}

// GlobalNetworkReleaseLoopbackTarget binds one canonical pool identity to its observed release fingerprint.
type GlobalNetworkReleaseLoopbackTarget struct {
	Address                netip.Addr
	ObservationFingerprint string
}

// Validate rejects target facts outside the supplied canonical identity pool.
func (target GlobalNetworkReleaseLoopbackTarget) Validate(pool identity.Pool) error {
	if err := pool.Validate(); err != nil {
		return fmt.Errorf("global network release identity pool: %w", err)
	}
	if !target.Address.IsValid() || !target.Address.Is4() || !target.Address.IsLoopback() || target.Address != target.Address.Unmap() {
		return fmt.Errorf("global network release loopback target address must be canonical IPv4 loopback")
	}
	if !pool.Contains(target.Address) {
		return fmt.Errorf("global network release loopback target %s is outside the confirmed pool", target.Address)
	}
	return validateNetworkDataPlaneSetupDigest("loopback observation fingerprint", target.ObservationFingerprint)
}

// GlobalNetworkReleaseAuthority is the immutable full-stage authority required to remove global network state.
type GlobalNetworkReleaseAuthority struct {
	Projection                     NetworkDataPlaneSetupProjection
	Policy                         networkpolicy.Policy
	Root                           certroot.Root
	ExpectedOwnershipFingerprint   string
	TrustDisposition               GlobalNetworkReleaseTrustDisposition
	LowPortObservationFingerprint  string
	ResolverObservationFingerprint string
	TrustObservationFingerprint    string
	LoopbackTargets                []GlobalNetworkReleaseLoopbackTarget
	ProjectRevisions               []NetworkProjectRevision
}

// Clone returns a copy whose root and snapshot slices cannot be modified through the original authority.
func (authority GlobalNetworkReleaseAuthority) Clone() GlobalNetworkReleaseAuthority {
	clone := authority
	clone.Root = cloneGlobalNetworkReleaseRoot(authority.Root)
	clone.LoopbackTargets = slices.Clone(authority.LoopbackTargets)
	clone.ProjectRevisions = slices.Clone(authority.ProjectRevisions)
	return clone
}

// Validate rejects authority that cannot prove a complete, exact global release boundary.
func (authority GlobalNetworkReleaseAuthority) Validate() error {
	if authority.Projection.Stage != NetworkStageFull {
		return fmt.Errorf("global network release requires %q projection", NetworkStageFull)
	}
	if err := authority.Projection.Validate(); err != nil {
		return fmt.Errorf("global network release projection: %w", err)
	}
	if err := authority.Policy.Validate(); err != nil {
		return fmt.Errorf("global network release policy: %w", err)
	}
	if err := validateNetworkDataPlanePolicyListeners(authority.Policy, authority.Projection.Listeners); err != nil {
		return fmt.Errorf("global network release policy listeners: %w", err)
	}
	policyFingerprint, err := authority.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("global network release policy fingerprint: %w", err)
	}
	if policyFingerprint != authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint {
		return fmt.Errorf("global network release policy fingerprint does not match confirmed ownership")
	}
	if _, err := trust.NewRequestForRequester(
		authority.Projection.ConfirmedOwnership.Record.InstallationID,
		authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		authority.Policy.Mechanisms.Trust,
		cloneGlobalNetworkReleaseRoot(authority.Root),
	); err != nil {
		return fmt.Errorf("global network release public root: %w", err)
	}
	if authority.Root.Fingerprint != authority.Policy.AuthorityFingerprint {
		return fmt.Errorf("global network release public root fingerprint does not match policy authority")
	}
	if err := validateNetworkDataPlaneSetupDigest("expected ownership fingerprint", authority.ExpectedOwnershipFingerprint); err != nil {
		return err
	}
	if authority.ExpectedOwnershipFingerprint != authority.Projection.ConfirmedOwnership.Fingerprint {
		return fmt.Errorf("global network release expected ownership fingerprint does not match confirmed ownership")
	}
	if err := authority.TrustDisposition.Validate(); err != nil {
		return err
	}
	for _, candidate := range []struct {
		name  string
		value string
	}{
		{name: "low-port observation fingerprint", value: authority.LowPortObservationFingerprint},
		{name: "resolver observation fingerprint", value: authority.ResolverObservationFingerprint},
		{name: "trust observation fingerprint", value: authority.TrustObservationFingerprint},
	} {
		if err := validateNetworkDataPlaneSetupDigest(candidate.name, candidate.value); err != nil {
			return err
		}
	}
	pool, err := networkSetupIdentityPool(authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		return fmt.Errorf("global network release loopback pool: %w", err)
	}
	candidates := pool.Candidates()
	if len(authority.LoopbackTargets) != len(candidates) {
		return fmt.Errorf("global network release loopback targets must contain exactly %d entries", len(candidates))
	}
	for index, target := range authority.LoopbackTargets {
		if err := target.Validate(pool); err != nil {
			return fmt.Errorf("global network release loopback target %d: %w", index, err)
		}
		if target.Address != candidates[index] {
			return fmt.Errorf("global network release loopback targets must be unique and ordered by confirmed pool")
		}
	}
	if _, err := validateNetworkProjectRevisions(authority.ProjectRevisions); err != nil {
		return fmt.Errorf("global network release project revisions: %w", err)
	}
	return nil
}

// cloneGlobalNetworkReleaseRoot prevents a caller from mutating retained public certificate bytes.
func cloneGlobalNetworkReleaseRoot(root certroot.Root) certroot.Root {
	root.CertificatePEM = slices.Clone(root.CertificatePEM)
	return root
}

// StageGlobalNetworkReleaseRequest stages one queued global network.release operation with immutable release authority.
type StageGlobalNetworkReleaseRequest struct {
	Operation domain.Operation
	Authority GlobalNetworkReleaseAuthority
}

// Validate rejects staged operations that are not queued global releases authorized by the current full network.
func (request StageGlobalNetworkReleaseRequest) Validate() error {
	if err := request.Operation.Validate(); err != nil {
		return fmt.Errorf("global network release operation: %w", err)
	}
	if request.Operation.Kind != domain.OperationKindNetworkRelease {
		return fmt.Errorf("global network release operation kind must be %q", domain.OperationKindNetworkRelease)
	}
	if request.Operation.ProjectID != "" {
		return fmt.Errorf("global network release operation must be global")
	}
	if request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("global network release operation must be queued")
	}
	if request.Operation.Phase != string(domain.OperationQueued) {
		return fmt.Errorf("global network release queued phase must be %q", domain.OperationQueued)
	}
	if err := request.Authority.Validate(); err != nil {
		return fmt.Errorf("global network release authority: %w", err)
	}
	if request.Operation.RequestedAt.Before(request.Authority.Projection.NetworkUpdatedAt) {
		return fmt.Errorf("global network release operation requested time must not precede the network update time")
	}
	return nil
}
