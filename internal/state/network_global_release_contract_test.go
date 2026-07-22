package state

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/trust/certroot"
	"github.com/goforj/harbor/internal/trust/localca"
)

// TestGlobalNetworkReleaseAuthorityAcceptsBothTrustDispositions verifies full-stage teardown accepts either safe trust outcome.
func TestGlobalNetworkReleaseAuthorityAcceptsBothTrustDispositions(t *testing.T) {
	for _, disposition := range []GlobalNetworkReleaseTrustDisposition{
		GlobalNetworkReleaseTrustOwned,
		GlobalNetworkReleaseTrustPreexistingUnowned,
	} {
		t.Run(string(disposition), func(t *testing.T) {
			authority := validGlobalNetworkReleaseAuthority(t)
			authority.TrustDisposition = disposition
			if err := authority.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestGlobalNetworkReleaseAuthorityRejectsEveryIndependentInvalidBranch verifies immutable release authority fails closed.
func TestGlobalNetworkReleaseAuthorityRejectsEveryIndependentInvalidBranch(t *testing.T) {
	for name, mutate := range map[string]func(*GlobalNetworkReleaseAuthority){
		"stage": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Projection.Stage = NetworkStageResolver
		},
		"listener": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Projection.Listeners.HTTP.Bind = authority.Projection.Listeners.HTTPS.Bind
		},
		"policy listener": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Policy.HTTP.Bind = netip.MustParseAddrPort("127.0.0.1:18081")
			refreshGlobalNetworkReleasePolicyOwnership(t, authority)
		},
		"ownership policy": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = strings.Repeat("b", 64)
			refreshGlobalNetworkReleaseOwnership(t, authority)
		},
		"root policy": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Policy.AuthorityFingerprint = strings.Repeat("f", 64)
			refreshGlobalNetworkReleasePolicyOwnership(t, authority)
		},
		"missing root": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Root.CertificatePEM = nil
		},
		"expected ownership": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ExpectedOwnershipFingerprint = strings.Repeat("e", 64)
		},
		"private root": func(authority *GlobalNetworkReleaseAuthority) {
			authority.Root.CertificatePEM = append(authority.Root.CertificatePEM, []byte("-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----\n")...)
		},
		"disposition": func(authority *GlobalNetworkReleaseAuthority) {
			authority.TrustDisposition = "unknown"
		},
		"low-port digest": func(authority *GlobalNetworkReleaseAuthority) {
			authority.LowPortObservationFingerprint = strings.Repeat("A", 64)
		},
		"resolver digest": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ResolverObservationFingerprint = "short"
		},
		"trust digest": func(authority *GlobalNetworkReleaseAuthority) {
			authority.TrustObservationFingerprint = strings.Repeat("g", 64)
		},
		"targets missing": func(authority *GlobalNetworkReleaseAuthority) {
			authority.LoopbackTargets = authority.LoopbackTargets[:7]
		},
		"targets unordered": func(authority *GlobalNetworkReleaseAuthority) {
			authority.LoopbackTargets[0], authority.LoopbackTargets[1] = authority.LoopbackTargets[1], authority.LoopbackTargets[0]
		},
		"target outside pool": func(authority *GlobalNetworkReleaseAuthority) {
			authority.LoopbackTargets[0].Address = netip.MustParseAddr("127.77.0.16")
		},
		"target digest": func(authority *GlobalNetworkReleaseAuthority) {
			authority.LoopbackTargets[0].ObservationFingerprint = "bad"
		},
		"project revisions uninitialized": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ProjectRevisions = nil
		},
		"project revisions unordered": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ProjectRevisions = []NetworkProjectRevision{{ProjectID: "project-z", Revision: 2}, {ProjectID: "project-a", Revision: 1}}
		},
		"project revision duplicate": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ProjectRevisions = []NetworkProjectRevision{{ProjectID: "project-a", Revision: 1}, {ProjectID: "project-b", Revision: 1}}
		},
		"project revision invalid": func(authority *GlobalNetworkReleaseAuthority) {
			authority.ProjectRevisions = []NetworkProjectRevision{{ProjectID: "project-a", Revision: 0}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			authority := validGlobalNetworkReleaseAuthority(t)
			mutate(&authority)
			if err := authority.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid authority")
			}
		})
	}
}

// TestGlobalNetworkReleaseAuthorityPinsEveryPoolAddress verifies teardown covers the subnet and broadcast identities too.
func TestGlobalNetworkReleaseAuthorityPinsEveryPoolAddress(t *testing.T) {
	authority := validGlobalNetworkReleaseAuthority(t)
	if len(authority.LoopbackTargets) != 8 {
		t.Fatalf("LoopbackTargets length = %d, want 8", len(authority.LoopbackTargets))
	}
	want := netip.MustParseAddr("127.77.0.8")
	for index, target := range authority.LoopbackTargets {
		if target.Address != want {
			t.Fatalf("LoopbackTargets[%d].Address = %s, want %s", index, target.Address, want)
		}
		want = want.Next()
	}
}

// TestGlobalNetworkReleaseAuthorityCloneIsIndependent verifies retained authority cannot be mutated through its source.
func TestGlobalNetworkReleaseAuthorityCloneIsIndependent(t *testing.T) {
	authority := validGlobalNetworkReleaseAuthority(t)
	authority.ProjectRevisions = []NetworkProjectRevision{{ProjectID: "project-a", Revision: 1}}
	clone := authority.Clone()

	authority.Root.CertificatePEM[0] ^= 0xff
	authority.LoopbackTargets[0].ObservationFingerprint = "bad"
	authority.ProjectRevisions[0].Revision = 0

	if err := clone.Validate(); err != nil {
		t.Fatalf("cloned authority changed through source mutation: %v", err)
	}
}

// TestStageGlobalNetworkReleaseRequestRejectsOperationDrift verifies only a fresh queued global release can use the authority.
func TestStageGlobalNetworkReleaseRequestRejectsOperationDrift(t *testing.T) {
	base := validStageGlobalNetworkReleaseRequest(t)
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	equalBoundary := validStageGlobalNetworkReleaseRequest(t)
	equalBoundary.Operation.RequestedAt = equalBoundary.Authority.Projection.NetworkUpdatedAt
	if err := equalBoundary.Validate(); err != nil {
		t.Fatalf("Validate() rejected equal network update boundary: %v", err)
	}
	for name, mutate := range map[string]func(*StageGlobalNetworkReleaseRequest){
		"kind": func(request *StageGlobalNetworkReleaseRequest) {
			request.Operation.Kind = domain.OperationKindNetworkSetup
		},
		"project scope": func(request *StageGlobalNetworkReleaseRequest) {
			request.Operation.ProjectID = "project-a"
		},
		"state": func(request *StageGlobalNetworkReleaseRequest) {
			request.Operation.State = domain.OperationRunning
			startedAt := request.Operation.RequestedAt
			request.Operation.StartedAt = &startedAt
		},
		"phase": func(request *StageGlobalNetworkReleaseRequest) {
			request.Operation.Phase = "staging"
		},
		"time": func(request *StageGlobalNetworkReleaseRequest) {
			request.Operation.RequestedAt = request.Authority.Projection.NetworkUpdatedAt.Add(-time.Nanosecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := validStageGlobalNetworkReleaseRequest(t)
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid staged release")
			}
		})
	}
}

// validStageGlobalNetworkReleaseRequest returns a complete queued release request for contract tests.
func validStageGlobalNetworkReleaseRequest(t *testing.T) StageGlobalNetworkReleaseRequest {
	t.Helper()
	authority := validGlobalNetworkReleaseAuthority(t)
	operation, err := domain.NewOperation(
		"operation-network-release",
		"intent-network-release",
		domain.OperationKindNetworkRelease,
		"",
		authority.Projection.NetworkUpdatedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return StageGlobalNetworkReleaseRequest{Operation: operation, Authority: authority}
}

// validGlobalNetworkReleaseAuthority derives a current root-bound full-stage release snapshot.
func validGlobalNetworkReleaseAuthority(t *testing.T) GlobalNetworkReleaseAuthority {
	t.Helper()
	fixture, _ := newFullNetworkDataPlaneSetupProjectionFixture(t)
	projection, err := fixture.source.Resolve(context.Background(), fixture.request.Policy)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	authority, err := localca.New(localca.Config{Now: func() time.Time { return projection.NetworkUpdatedAt }})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	root := certroot.Root{CertificatePEM: material.CertificatePEM, Fingerprint: material.Fingerprint, NotBefore: material.NotBefore, NotAfter: material.NotAfter}
	policy := fixture.request.Policy
	policy.AuthorityFingerprint = root.Fingerprint
	projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint, err = policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() error = %v", err)
	}
	recordFingerprint, err := projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	projection.ConfirmedOwnership.Fingerprint = recordFingerprint
	pool, err := networkSetupIdentityPool(projection.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		t.Fatalf("networkSetupIdentityPool() error = %v", err)
	}
	targets := make([]GlobalNetworkReleaseLoopbackTarget, 0, pool.Capacity())
	for _, address := range pool.Candidates() {
		targets = append(targets, GlobalNetworkReleaseLoopbackTarget{Address: address, ObservationFingerprint: strings.Repeat("a", 64)})
	}
	return GlobalNetworkReleaseAuthority{
		Projection:                     projection,
		Policy:                         policy,
		Root:                           root,
		ExpectedOwnershipFingerprint:   projection.ConfirmedOwnership.Fingerprint,
		TrustDisposition:               GlobalNetworkReleaseTrustOwned,
		LowPortObservationFingerprint:  strings.Repeat("b", 64),
		ResolverObservationFingerprint: strings.Repeat("c", 64),
		TrustObservationFingerprint:    strings.Repeat("d", 64),
		LoopbackTargets:                targets,
		ProjectRevisions:               []NetworkProjectRevision{},
	}
}

// refreshGlobalNetworkReleaseOwnership restores the observation fingerprint after a test changes its record.
func refreshGlobalNetworkReleaseOwnership(t *testing.T, authority *GlobalNetworkReleaseAuthority) {
	t.Helper()
	fingerprint, err := authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	authority.Projection.ConfirmedOwnership.Fingerprint = fingerprint
}

// refreshGlobalNetworkReleasePolicyOwnership restores both policy and ownership fingerprints after a policy change.
func refreshGlobalNetworkReleasePolicyOwnership(t *testing.T, authority *GlobalNetworkReleaseAuthority) {
	t.Helper()
	policyFingerprint, err := authority.Policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() error = %v", err)
	}
	authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = policyFingerprint
	refreshGlobalNetworkReleaseOwnership(t, authority)
	authority.ExpectedOwnershipFingerprint = authority.Projection.ConfirmedOwnership.Fingerprint
}
