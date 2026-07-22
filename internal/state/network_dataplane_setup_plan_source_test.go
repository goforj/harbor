package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
)

// networkDataPlanePlanSourceRoots is a stable public-root reader for source tests.
type networkDataPlanePlanSourceRoots struct{ root certificates.Root }

// PublicRoot returns a value copy so source results cannot borrow test-owned authority.
func (roots networkDataPlanePlanSourceRoots) PublicRoot() (certificates.Root, error) {
	return roots.root, nil
}

// TestNetworkDataPlanePlanSourcesResolveExactLifecycleAuthority covers both approval boundaries using production migrations.
func TestNetworkDataPlanePlanSourcesResolveExactLifecycleAuthority(t *testing.T) {
	fixture, trust, low := newNetworkDataPlanePlanSourceFixture(t)
	trustPlan, err := trust.Resolve(context.Background(), ticketissuer.TrustRequest{OperationID: fixture.stage.Operation.ID})
	if err != nil {
		t.Fatalf("trust Resolve(): %v", err)
	}
	if err := trustPlan.Validate(); err != nil {
		t.Fatalf("trust plan Validate(): %v", err)
	}
	if trustPlan.OperationID != fixture.stage.Operation.ID || trustPlan.OperationRevision == 0 || trustPlan.Mutation != helper.OperationEnsureTrust || trustPlan.TargetOwnership != fixture.stage.Projection.ConfirmedOwnership.Record || trustPlan.Policy != fixture.stage.Policy {
		t.Fatalf("trust plan = %#v, want exact current authority", trustPlan)
	}
	trustPlan.Root.CertificatePEM[0] ^= 1
	again, err := trust.Resolve(context.Background(), ticketissuer.TrustRequest{OperationID: fixture.stage.Operation.ID})
	if err != nil || again.Root.CertificatePEM[0] == trustPlan.Root.CertificatePEM[0] {
		t.Fatalf("trust result root isolation = %#v, %v", again, err)
	}

	advanced := advanceNetworkDataPlanePlanSourceTrust(t, fixture)
	lowPlan, err := low.Resolve(context.Background(), ticketissuer.LowPortRequest{OperationID: advanced.Operation.ID})
	if err != nil {
		t.Fatalf("low-port Resolve(): %v", err)
	}
	wantNative, err := lowport.NewRequest(fixture.stage.Projection.ConfirmedOwnership.Record, fixture.stage.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := lowPlan.Validate(); err != nil {
		t.Fatalf("low-port plan Validate(): %v", err)
	}
	if lowPlan.Operation.ID != advanced.Operation.ID || lowPlan.OperationRevision != advanced.Revision || lowPlan.Mutation != helper.OperationEnsureLowPorts || lowPlan.TargetOwnership != fixture.stage.Projection.ConfirmedOwnership.Record || lowPlan.Policy != fixture.stage.Policy || lowPlan.NativeRequest != wantNative {
		t.Fatalf("low-port plan = %#v, want exact committed receipt", lowPlan)
	}
}

// TestNetworkDataPlanePlanSourcesFailClosedOnBoundaryAndReceiptDrift exercises selected-operation, receipt, and current-projection rejection.
func TestNetworkDataPlanePlanSourcesFailClosedOnBoundaryAndReceiptDrift(t *testing.T) {
	tests := []struct {
		name, sql, want string
		low             bool
	}{
		{"trust requested operation mismatch", "UPDATE operations SET kind = 'host.setup'", "selected operation", false},
		{"trust scope drift", "UPDATE operations SET project_id = 'project-x'", "must not identify a project", false},
		{"trust revision drift", "UPDATE operations SET revision = revision + 1", "revision does not match", false},
		{"trust history drift", "UPDATE operation_transitions SET phase = 'bad' WHERE sequence = 3", "transition", false},
		{"low receipt revision", "UPDATE network_data_plane_setup_plans SET operation_revision = operation_revision + 1", "plan revision", true},
		{"low receipt corruption", "UPDATE network_data_plane_setup_plans SET authority_payload = '{}'", "authority payload digest", true},
		{"low projection drift", "UPDATE network_state SET revision = revision + 2", "reuses revision", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, trust, low := newNetworkDataPlanePlanSourceFixture(t)
			if test.low {
				advanceNetworkDataPlanePlanSourceTrust(t, fixture)
			}
			mustProjectStoreReadExec(t, fixture.database, test.sql)
			var err error
			if test.low {
				_, err = low.Resolve(context.Background(), ticketissuer.LowPortRequest{OperationID: fixture.stage.Operation.ID})
			} else {
				_, err = trust.Resolve(context.Background(), ticketissuer.TrustRequest{OperationID: fixture.stage.Operation.ID})
			}
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want corruption containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkDataPlanePlanSourcesRejectInvalidCancelledAndMissingDependencies proves constructors and reads fail closed.
func TestNetworkDataPlanePlanSourcesRejectInvalidCancelledAndMissingDependencies(t *testing.T) {
	fixture, trust, low := newNetworkDataPlanePlanSourceFixture(t)
	if _, err := trust.Resolve(context.Background(), ticketissuer.TrustRequest{}); err == nil {
		t.Fatal("trust accepted empty request")
	}
	if _, err := low.Resolve(context.Background(), ticketissuer.LowPortRequest{}); err == nil {
		t.Fatal("low-port accepted empty request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := trust.Resolve(ctx, ticketissuer.TrustRequest{OperationID: fixture.stage.Operation.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("trust cancelled error = %v", err)
	}
	if _, err := low.Resolve(ctx, ticketissuer.LowPortRequest{OperationID: fixture.stage.Operation.ID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("low-port cancelled error = %v", err)
	}
	for _, call := range []func(){func() {
		_ = NewNetworkDataPlaneTrustPlanSource(nil, networkDataPlanePlanSourceRoots{}, networkplan.PlatformMacOS)
	}, func() {
		_ = NewNetworkDataPlaneTrustPlanSource(fixture.state.state.store.networkState, nil, networkplan.PlatformMacOS)
	}, func() { _ = NewNetworkDataPlaneLowPortPlanSource(nil) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("constructor did not panic")
				}
			}()
			call()
		}()
	}
}

// TestActiveNetworkDataPlaneSetupOperationClassifiesOnlySoleActiveRecoverableBoundaries covers recovery selection and terminal exclusion.
func TestActiveNetworkDataPlaneSetupOperationClassifiesOnlySoleActiveRecoverableBoundaries(t *testing.T) {
	fixture, _, _ := newNetworkDataPlanePlanSourceFixture(t)
	active, found, err := fixture.journal.ActiveNetworkDataPlaneSetupOperation(context.Background())
	if err != nil || !found || active.Phase != NetworkDataPlaneSetupPhaseTrustApproval {
		t.Fatalf("trust active = %#v, %t, %v", active, found, err)
	}
	advanced := advanceNetworkDataPlanePlanSourceTrust(t, fixture)
	active, found, err = fixture.journal.ActiveNetworkDataPlaneSetupOperation(context.Background())
	if err != nil || !found || active.Operation.Revision != advanced.Revision || active.Phase != NetworkDataPlaneSetupPhaseLowPortApproval {
		t.Fatalf("low-port active = %#v, %t, %v", active, found, err)
	}
	mustProjectStoreReadExec(t, fixture.database, "UPDATE operations SET state = ?, phase = ?", domain.OperationRunning, networkDataPlaneSetupActivationPhase)
	// The forged activation has no matching lifecycle history and must be rejected rather than resumed.
	if _, _, err := fixture.journal.ActiveNetworkDataPlaneSetupOperation(context.Background()); err == nil {
		t.Fatal("forged activation was accepted")
	}
	mustProjectStoreReadExec(t, fixture.database, "UPDATE operations SET state = ?, phase = ?", domain.OperationSucceeded, networkDataPlaneSetupCompletedPhase)
	if _, found, err := fixture.journal.ActiveNetworkDataPlaneSetupOperation(context.Background()); err != nil || found {
		t.Fatalf("terminal active = %t, %v", found, err)
	}
}

// newNetworkDataPlanePlanSourceFixture stages one root-bound resolver authority and returns both sources.
func newNetworkDataPlanePlanSourceFixture(t *testing.T) (*networkDataPlaneSetupCompleteFixture, *NetworkDataPlaneTrustPlanSource, *NetworkDataPlaneLowPortPlanSource) {
	t.Helper()
	fixture := newNetworkDataPlaneSetupCompleteFixture(t)
	authority, err := localca.New(localca.Config{Now: func() time.Time { return fixture.stage.Projection.NetworkUpdatedAt }})
	if err != nil {
		t.Fatal(err)
	}
	material := authority.Material()
	root := certificates.Root{CertificatePEM: material.CertificatePEM, Fingerprint: material.Fingerprint, NotBefore: material.NotBefore, NotAfter: material.NotAfter}
	network, found, err := fixture.state.state.store.Network(context.Background())
	if err != nil || !found {
		t.Fatalf("read current network: found %t, error %v", found, err)
	}
	fixture.stage.Policy, err = networkplan.Build(networkplan.Request{Platform: networkplan.PlatformMacOS, InstallationID: network.Ownership.InstallationID, Pool: network.Pool, AuthorityFingerprint: root.Fingerprint})
	if err != nil {
		t.Fatalf("build current policy: %v", err)
	}
	fingerprint, err := fixture.stage.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fixture.stage.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = fingerprint
	setNetworkDataPlaneSetupStageOwnershipFingerprint(t, &fixture.stage)
	mustProjectStoreReadExec(t, fixture.database, "UPDATE machine_ownership_projections SET network_policy_fingerprint = ?, record_fingerprint = ? WHERE id = 1", fingerprint, fixture.stage.Projection.ConfirmedOwnership.Fingerprint)
	staged, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.stage)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	_ = staged
	return fixture, NewNetworkDataPlaneTrustPlanSource(fixture.state.state.store.networkState, networkDataPlanePlanSourceRoots{root: root}, networkplan.PlatformMacOS), NewNetworkDataPlaneLowPortPlanSource(fixture.state.state.store.networkState)
}

// advanceNetworkDataPlanePlanSourceTrust writes the exact post-trust receipt consumed by the low-port source.
func advanceNetworkDataPlanePlanSourceTrust(t *testing.T, fixture *networkDataPlaneSetupCompleteFixture) OperationRecord {
	t.Helper()
	digest, err := NetworkDataPlaneSetupEvidenceDigest(struct{ Evidence string }{"trust"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := fixture.journal.Operation(context.Background(), fixture.stage.Operation.ID)
	if err != nil {
		t.Fatalf("read staged operation: %v", err)
	}
	record, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: fixture.stage.Operation.ID, ExpectedOperationRevision: current.Revision, RequesterIdentity: fixture.owner(), Projection: fixture.stage.Projection, Policy: fixture.stage.Policy, TrustEvidenceDigest: digest, TrustVerifiedAt: fixture.stage.Projection.NetworkUpdatedAt.Add(time.Minute)})
	if err != nil {
		t.Fatalf("advance trust: %v", err)
	}
	return record
}
