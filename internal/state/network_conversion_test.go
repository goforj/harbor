package state

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
)

// TestNetworkRecordFromModelsBuildsCanonicalPayloadFreeProjection verifies the conversion produces planner-ready identities without inventing upstreams.
func TestNetworkRecordFromModelsBuildsCanonicalPayloadFreeProjection(t *testing.T) {
	rows := validNetworkModelRows()
	record, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		t.Fatalf("networkRecordFromModels() error = %v", err)
	}
	if !initialized {
		t.Fatal("networkRecordFromModels() initialized = false")
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("NetworkRecord.Validate() error = %v", err)
	}
	if record.Stage != NetworkStageFull || record.Revision != 7 || record.Ownership.Generation != 3 {
		t.Fatalf("record authority = revision %d, ownership %#v", record.Revision, record.Ownership)
	}
	if candidates := addressStrings(record.Pool.Candidates()); !reflect.DeepEqual(candidates, []string{"127.77.0.10", "127.77.0.11", "127.77.0.12"}) {
		t.Fatalf("pool candidates = %v", candidates)
	}
	if got := networkLeaseKeys(record.Leases); !reflect.DeepEqual(got, []string{"project-alpha/primary", "project-alpha/secondary/metrics", "project-beta/primary"}) {
		t.Fatalf("lease order = %v", got)
	}
	if record.Leases[0].Ownership.Generation != 2 {
		t.Fatalf("active lease ownership generation = %d, want independent historical value 2", record.Leases[0].Ownership.Generation)
	}
	if len(record.Quarantines) != 0 || record.Quarantines == nil {
		t.Fatalf("quarantines = %#v, want initialized empty", record.Quarantines)
	}
	if got := endpointHosts(record.Reservations.Endpoints); !reflect.DeepEqual(got, []string{"alpha.test", "mysql.alpha.test", "mysql.beta.test"}) {
		t.Fatalf("endpoint order = %v", got)
	}
	if record.Reservations.Endpoints[0].Identity != nil || record.Reservations.Endpoints[1].Identity == nil {
		t.Fatalf("endpoint identity shapes = %#v", record.Reservations.Endpoints)
	}
	if record.Reservations.Endpoints[0].Generation != 99 {
		t.Fatalf("endpoint generation = %d, want independent value 99", record.Reservations.Endpoints[0].Generation)
	}
	if record.Reservations.SuppressedProjectIDs == nil || len(record.Reservations.SuppressedProjectIDs) != 0 {
		t.Fatalf("suppressed projects = %#v, want initialized empty", record.Reservations.SuppressedProjectIDs)
	}
}

// TestNetworkRecordFromModelsAcceptsIdentityFoundation verifies partial authority converts without inventing data-plane facts.
func TestNetworkRecordFromModelsAcceptsIdentityFoundation(t *testing.T) {
	rows := identityNetworkModelRows()
	record, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		t.Fatalf("networkRecordFromModels() identity error = %v", err)
	}
	if !initialized || record.Stage != NetworkStageIdentity {
		t.Fatalf("identity conversion = initialized %t, stage %q", initialized, record.Stage)
	}
	if record.Reservations.Listeners != (SharedListenerReservations{}) ||
		len(record.Reservations.Endpoints) != 0 || len(record.Leases) != 0 {
		t.Fatalf("identity conversion projected unproved authority: %#v", record)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("identity NetworkRecord.Validate() error = %v", err)
	}
}

// TestNetworkRecordFromModelsAcceptsResolverAuthority verifies policy-bound resolution remains non-publishable.
func TestNetworkRecordFromModelsAcceptsResolverAuthority(t *testing.T) {
	rows := resolverNetworkModelRows()
	record, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		t.Fatalf("networkRecordFromModels() resolver error = %v", err)
	}
	if !initialized || record.Stage != NetworkStageResolver {
		t.Fatalf("resolver conversion = initialized %t, stage %q", initialized, record.Stage)
	}
	if record.Reservations.Listeners != (SharedListenerReservations{}) ||
		len(record.Reservations.Endpoints) != 0 || len(record.Leases) != 0 {
		t.Fatalf("resolver conversion projected publishable authority: %#v", record)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("resolver NetworkRecord.Validate() error = %v", err)
	}
}

// TestNetworkRecordFromModelsRejectsResolverDataPlaneCorruption verifies the intermediate stage owns exactly three proofs and no routes.
func TestNetworkRecordFromModelsRejectsResolverDataPlaneCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "missing resolver", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence = rows.SetupEvidence[:2]
		}, want: "required component is missing"},
		{name: "low ports", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence = append(rows.SetupEvidence, models.NetworkSetupEvidence{
				Id: 24, NetworkStateId: 1, Component: "low_ports", Evidence: "verified low ports", Generation: 50, VerifiedAt: networkTestTime(),
			})
		}, want: "unsupported"},
		{name: "listener", mutate: func(rows *networkModelRows) {
			rows.Listeners = []models.NetworkSharedListener{validNetworkModelRows().Listeners[0]}
		}, want: "resolver-stage network must not contain listener reservations"},
		{name: "endpoint", mutate: func(rows *networkModelRows) {
			rows.Endpoints = []models.PublicEndpointLease{validNetworkModelRows().Endpoints[0]}
		}, want: "resolver-stage network must not contain endpoint reservations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := resolverNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkRecordFromModelsRejectsIdentityDataPlaneCorruption verifies persisted stage and child rows cannot disagree.
func TestNetworkRecordFromModelsRejectsIdentityDataPlaneCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "resolver proof", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence = append(rows.SetupEvidence, models.NetworkSetupEvidence{
				Id: 23, NetworkStateId: 1, Component: "resolver", Evidence: "verified resolver", Generation: 50, VerifiedAt: networkTestTime(),
			})
		}, want: "unsupported"},
		{name: "missing pool proof", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence = rows.SetupEvidence[:1]
		}, want: "required component is missing"},
		{name: "listener", mutate: func(rows *networkModelRows) {
			rows.Listeners = []models.NetworkSharedListener{validNetworkModelRows().Listeners[0]}
		}, want: "must not contain listener reservations"},
		{name: "endpoint", mutate: func(rows *networkModelRows) {
			rows.Endpoints = []models.PublicEndpointLease{validNetworkModelRows().Endpoints[0]}
		}, want: "must not contain endpoint reservations"},
		{name: "unknown stage", mutate: func(rows *networkModelRows) {
			rows.States[0].Stage = "partial"
		}, want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := identityNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkRecordFromModelsSupportsUninitializedState verifies first-run absence is distinct from corrupted orphan rows.
func TestNetworkRecordFromModelsSupportsUninitializedState(t *testing.T) {
	record, initialized, err := networkRecordFromModels(networkModelRows{
		Projects:      []models.Project{{Id: 1, ProjectId: "unrelated-project"}},
		ReleaseOwners: []models.Operation{{Id: "unrelated-operation"}},
	})
	if err != nil || initialized || !reflect.DeepEqual(record, NetworkRecord{}) {
		t.Fatalf("uninitialized conversion = record %#v, initialized %t, error %v", record, initialized, err)
	}

	rows := networkModelRows{Candidates: []models.NetworkPoolCandidate{{Id: 1}}}
	_, _, err = networkRecordFromModels(rows)
	assertNetworkConversionCorruption(t, err, "child rows exist")
}

// TestNetworkRecordFromModelsSuppressesStagedReleases verifies restart cannot republish a project whose unregister owns teardown.
func TestNetworkRecordFromModelsSuppressesStagedReleases(t *testing.T) {
	rows := validNetworkModelRows()
	rows.ReleaseOwners = []models.Operation{{
		Id:        "operation-release-alpha",
		Kind:      string(domain.OperationKindProjectUnregister),
		ProjectId: null.StringFrom("project-alpha"),
	}}
	rows.Releases = []models.NetworkProjectRelease{{
		Id:              71,
		NetworkStateId:  1,
		ProjectId:       null.StringFrom("project-alpha"),
		SourceProjectId: "project-alpha",
		OperationId:     "operation-release-alpha",
		State:           "releasing",
		BeginGeneration: 100,
		BeganAt:         networkTestTime().Add(4 * time.Minute),
	}}

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("release conversion initialized = %t, error %v", initialized, err)
	}
	if got := endpointHosts(record.Reservations.Endpoints); !reflect.DeepEqual(got, []string{"mysql.beta.test"}) {
		t.Fatalf("publishable endpoints = %v", got)
	}
	if got := record.Reservations.SuppressedProjectIDs; !reflect.DeepEqual(got, []domain.ProjectID{"project-alpha"}) {
		t.Fatalf("suppressed projects = %v", got)
	}
	if len(record.Leases) != 3 {
		t.Fatalf("planner leases = %d, want retained teardown inputs", len(record.Leases))
	}
}

// TestNetworkRecordFromModelsAcceptsCompletedReleaseBeforeProjectDeletion verifies the final network edge may precede atomic project removal.
func TestNetworkRecordFromModelsAcceptsCompletedReleaseBeforeProjectDeletion(t *testing.T) {
	rows := validNetworkModelRows()
	rows.Endpoints = rows.Endpoints[:1]
	rows.Leases = rows.Leases[:2]
	quarantineNetworkLease(&rows.Leases[1])
	rows.ReleaseOwners = []models.Operation{{
		Id:        "operation-release-alpha",
		Kind:      string(domain.OperationKindProjectUnregister),
		ProjectId: null.StringFrom("project-alpha"),
	}}
	completedAt := networkTestTime().Add(6 * time.Minute)
	rows.Releases = []models.NetworkProjectRelease{{
		Id:                   71,
		NetworkStateId:       1,
		SourceProjectId:      "project-alpha",
		OperationId:          "operation-release-alpha",
		State:                "completed",
		BeginGeneration:      100,
		BeganAt:              networkTestTime().Add(4 * time.Minute),
		CompletionGeneration: null.IntFrom(101),
		CompletedAt:          &completedAt,
		ReleaseEvidence:      null.StringFrom(" verified release evidence "),
		ReleaseSetDigest:     null.StringFrom("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
	}}

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("completed release initialized = %t, error %v", initialized, err)
	}
	if got := record.Reservations.SuppressedProjectIDs; !reflect.DeepEqual(got, []domain.ProjectID{"project-alpha"}) {
		t.Fatalf("completed release suppression = %v", got)
	}
	if len(record.Reservations.Endpoints) != 1 || record.Reservations.Endpoints[0].Key.ProjectID != "project-beta" {
		t.Fatalf("completed release endpoints = %#v", record.Reservations.Endpoints)
	}
}

// TestNetworkRootConversionRejectsCorruption covers every singleton field that SQLite constraints might no longer protect.
func TestNetworkRootConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "multiple roots", mutate: func(rows *networkModelRows) { rows.States = append(rows.States, rows.States[0]) }, want: "singleton contains 2 rows"},
		{name: "wrong ID", mutate: func(rows *networkModelRows) { rows.States[0].Id = 2 }, want: "singleton ID"},
		{name: "zero revision", mutate: func(rows *networkModelRows) { rows.States[0].Revision = 0 }, want: "revision"},
		{name: "overflow revision", mutate: func(rows *networkModelRows) { rows.States[0].Revision = int(domain.MaximumSequence) + 1 }, want: "revision"},
		{name: "zero creation", mutate: func(rows *networkModelRows) { rows.States[0].CreatedAt = time.Time{} }, want: "creation time"},
		{name: "zero update", mutate: func(rows *networkModelRows) { rows.States[0].UpdatedAt = time.Time{} }, want: "update time"},
		{name: "non UTC update", mutate: func(rows *networkModelRows) {
			rows.States[0].UpdatedAt = rows.States[0].UpdatedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "reversed time", mutate: func(rows *networkModelRows) { rows.States[0].UpdatedAt = rows.States[0].CreatedAt.Add(-time.Second) }, want: "precedes"},
		{name: "zero ownership generation", mutate: func(rows *networkModelRows) { rows.States[0].OwnershipGeneration = 0 }, want: "ownership generation"},
		{name: "invalid installation", mutate: func(rows *networkModelRows) { rows.States[0].InstallationId = "-unsafe" }, want: "installation ID"},
		{name: "wrong suffix", mutate: func(rows *networkModelRows) { rows.States[0].DnsSuffix = ".localhost" }, want: "DNS suffix"},
		{name: "invalid pool address", mutate: func(rows *networkModelRows) { rows.States[0].PoolNetwork = "invalid" }, want: "pool network"},
		{name: "mapped pool address", mutate: func(rows *networkModelRows) { rows.States[0].PoolNetwork = "::ffff:127.77.0.0" }, want: "canonical IPv4 form"},
		{name: "non loopback pool", mutate: func(rows *networkModelRows) { rows.States[0].PoolNetwork = "192.0.2.0" }, want: "IPv4 loopback"},
		{name: "short prefix", mutate: func(rows *networkModelRows) { rows.States[0].PoolPrefixLength = 7 }, want: "prefix length"},
		{name: "long prefix", mutate: func(rows *networkModelRows) { rows.States[0].PoolPrefixLength = 33 }, want: "prefix length"},
		{name: "noncanonical prefix", mutate: func(rows *networkModelRows) { rows.States[0].PoolNetwork = "127.77.0.1" }, want: "canonical prefix"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkCandidateConversionRejectsCorruption covers deterministic pool ordering and scope.
func TestNetworkCandidateConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "missing", mutate: func(rows *networkModelRows) { rows.Candidates = nil }, want: "at least one"},
		{name: "zero ID", mutate: func(rows *networkModelRows) { rows.Candidates[0].Id = 0 }, want: "database ID"},
		{name: "duplicate ID", mutate: func(rows *networkModelRows) { rows.Candidates[1].Id = rows.Candidates[0].Id }, want: "database ID is duplicated"},
		{name: "wrong root", mutate: func(rows *networkModelRows) { rows.Candidates[0].NetworkStateId = 2 }, want: "network state ID"},
		{name: "ordinal ceiling", mutate: func(rows *networkModelRows) {
			rows.Candidates = rows.Candidates[:1]
			rows.Candidates[0].Ordinal = 65536
		}, want: "exceeds 65535"},
		{name: "ordinal gap", mutate: func(rows *networkModelRows) { rows.Candidates[0].Ordinal = 4 }, want: "expected"},
		{name: "duplicate ordinal", mutate: func(rows *networkModelRows) { rows.Candidates[1].Ordinal = rows.Candidates[0].Ordinal }, want: "expected"},
		{name: "zero generation", mutate: func(rows *networkModelRows) { rows.Candidates[0].Generation = 0 }, want: "generation"},
		{name: "invalid address", mutate: func(rows *networkModelRows) { rows.Candidates[0].Address = "bad" }, want: "invalid"},
		{name: "mapped address", mutate: func(rows *networkModelRows) { rows.Candidates[0].Address = "::ffff:127.77.0.11" }, want: "canonical IPv4 form"},
		{name: "outside prefix", mutate: func(rows *networkModelRows) { rows.Candidates[0].Address = "127.78.0.10" }, want: "outside"},
		{name: "duplicate address", mutate: func(rows *networkModelRows) { rows.Candidates[1].Address = rows.Candidates[0].Address }, want: "duplicated"},
		{name: "ordinal numeric mismatch", mutate: func(rows *networkModelRows) {
			rows.Candidates[0].Address, rows.Candidates[1].Address = rows.Candidates[1].Address, rows.Candidates[0].Address
		}, want: "numeric address order"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkSetupEvidenceConversionRejectsCorruption covers the exact four-component proof contract.
func TestNetworkSetupEvidenceConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "missing component", mutate: func(rows *networkModelRows) { rows.SetupEvidence = rows.SetupEvidence[1:] }, want: "low_ports"},
		{name: "zero ID", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].Id = 0 }, want: "database ID"},
		{name: "duplicate ID", mutate: func(rows *networkModelRows) { rows.SetupEvidence[1].Id = rows.SetupEvidence[0].Id }, want: "database ID is duplicated"},
		{name: "wrong root", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].NetworkStateId = 2 }, want: "network state ID"},
		{name: "unsupported", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].Component = "trust" }, want: "unsupported"},
		{name: "duplicate component", mutate: func(rows *networkModelRows) { rows.SetupEvidence[1].Component = rows.SetupEvidence[0].Component }, want: "duplicated"},
		{name: "empty evidence", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].Evidence = " \n " }, want: "required"},
		{name: "oversized evidence", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence[0].Evidence = strings.Repeat("x", maximumNetworkEvidenceLength+1)
		}, want: "exceeds"},
		{name: "zero generation", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].Generation = 0 }, want: "generation"},
		{name: "zero verification", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].VerifiedAt = time.Time{} }, want: "verification time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkListenerConversionRejectsCorruption covers exact listener membership, mappings, and collisions.
func TestNetworkListenerConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "missing", mutate: func(rows *networkModelRows) { rows.Listeners = rows.Listeners[1:] }, want: "https"},
		{name: "zero ID", mutate: func(rows *networkModelRows) { rows.Listeners[0].Id = 0 }, want: "database ID"},
		{name: "duplicate ID", mutate: func(rows *networkModelRows) { rows.Listeners[1].Id = rows.Listeners[0].Id }, want: "database ID is duplicated"},
		{name: "wrong root", mutate: func(rows *networkModelRows) { rows.Listeners[0].NetworkStateId = 2 }, want: "network state ID"},
		{name: "unsupported kind", mutate: func(rows *networkModelRows) { rows.Listeners[0].Kind = "smtp" }, want: "unsupported"},
		{name: "duplicate kind", mutate: func(rows *networkModelRows) { rows.Listeners[1].Kind = rows.Listeners[0].Kind }, want: "duplicated"},
		{name: "bad advertised address", mutate: func(rows *networkModelRows) { rows.Listeners[0].AdvertisedAddress = "bad" }, want: "invalid"},
		{name: "bad bind port", mutate: func(rows *networkModelRows) { rows.Listeners[0].BindPort = 0 }, want: "between"},
		{name: "zero generation", mutate: func(rows *networkModelRows) { rows.Listeners[0].Generation = 0 }, want: "generation"},
		{name: "zero verification", mutate: func(rows *networkModelRows) { rows.Listeners[0].VerifiedAt = time.Time{} }, want: "verification"},
		{name: "unsupported mode", mutate: func(rows *networkModelRows) { rows.Listeners[0].Mode = "proxy" }, want: "mode"},
		{name: "direct mismatch", mutate: func(rows *networkModelRows) { networkListener(rows, "dns").Mode = "direct" }, want: "direct"},
		{name: "redirect same socket", mutate: func(rows *networkModelRows) {
			dns := networkListener(rows, "dns")
			dns.BindPort = dns.AdvertisedPort
		}, want: "distinct"},
		{name: "redirect address mismatch", mutate: func(rows *networkModelRows) { networkListener(rows, "dns").BindAddress = "127.0.0.2" }, want: "addresses must match"},
		{name: "HTTP wrong port", mutate: func(rows *networkModelRows) { networkListener(rows, "http").AdvertisedPort = 8080 }, want: "port 80"},
		{name: "HTTPS wrong port", mutate: func(rows *networkModelRows) {
			networkListener(rows, "https").AdvertisedPort = 8443
			networkListener(rows, "https").BindPort = 8443
		}, want: "port 443"},
		{name: "ingress address mismatch", mutate: func(rows *networkModelRows) {
			networkListener(rows, "https").AdvertisedAddress = "127.0.0.2"
			networkListener(rows, "https").BindAddress = "127.0.0.2"
		}, want: "share one ingress"},
		{name: "cross socket collision", mutate: func(rows *networkModelRows) { networkListener(rows, "dns").BindPort = 443 }, want: "collides"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkReferenceConversionRejectsCorruption covers project and release-operation lookup rows.
func TestNetworkReferenceConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "project zero ID", mutate: func(rows *networkModelRows) { rows.Projects[0].Id = 0 }, want: "database ID"},
		{name: "project duplicate ID", mutate: func(rows *networkModelRows) { rows.Projects[1].Id = rows.Projects[0].Id }, want: "database ID is duplicated"},
		{name: "project invalid ID", mutate: func(rows *networkModelRows) { rows.Projects[0].ProjectId = " bad " }, want: "surrounding whitespace"},
		{name: "project duplicate identity", mutate: func(rows *networkModelRows) { rows.Projects[1].ProjectId = rows.Projects[0].ProjectId }, want: "project ID is duplicated"},
		{name: "operation invalid ID", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners[0].Id = " bad " }), want: "operation ID"},
		{name: "operation duplicate", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners = append(rows.ReleaseOwners, rows.ReleaseOwners[0]) }), want: "operation ID is duplicated"},
		{name: "operation missing project", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners[0].ProjectId = null.String{} }), want: "identify a project"},
		{name: "operation invalid project", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners[0].ProjectId = null.StringFrom(" bad ") }), want: "project ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkLeaseConversionRejectsActiveCorruption covers persisted active lease identity, ownership, and evidence branches.
func TestNetworkLeaseConversionRejectsActiveCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "zero ID", mutate: func(rows *networkModelRows) { rows.Leases[0].Id = 0 }, want: "database ID"},
		{name: "duplicate ID", mutate: func(rows *networkModelRows) { rows.Leases[1].Id = rows.Leases[0].Id }, want: "database ID is duplicated"},
		{name: "wrong root", mutate: func(rows *networkModelRows) { rows.Leases[0].NetworkStateId = 2 }, want: "network state ID"},
		{name: "invalid address", mutate: func(rows *networkModelRows) { rows.Leases[0].Address = "bad" }, want: "invalid"},
		{name: "address outside pool", mutate: func(rows *networkModelRows) { rows.Leases[0].Address = "127.77.0.13" }, want: "not a pool candidate"},
		{name: "duplicate address", mutate: func(rows *networkModelRows) { rows.Leases[1].Address = rows.Leases[0].Address }, want: "address is duplicated"},
		{name: "invalid source project", mutate: func(rows *networkModelRows) { rows.Leases[0].SourceProjectId = " bad " }, want: "project ID"},
		{name: "primary secondary ID", mutate: func(rows *networkModelRows) { rows.Leases[0].SecondaryId = "extra" }, want: "primary lease"},
		{name: "secondary missing ID", mutate: func(rows *networkModelRows) { rows.Leases[2].SecondaryId = "" }, want: "secondary ID is required"},
		{name: "unsupported kind", mutate: func(rows *networkModelRows) { rows.Leases[0].Kind = "shared" }, want: "kind"},
		{name: "zero lease generation", mutate: func(rows *networkModelRows) { rows.Leases[0].LeaseGeneration = 0 }, want: "lease generation"},
		{name: "zero ownership generation", mutate: func(rows *networkModelRows) { rows.Leases[0].OwnershipGeneration = 0 }, want: "ownership generation"},
		{name: "invalid ownership installation", mutate: func(rows *networkModelRows) { rows.Leases[0].OwnershipInstallationId = "-bad" }, want: "installation ID"},
		{name: "empty ensure evidence", mutate: func(rows *networkModelRows) { rows.Leases[0].EnsureEvidence = " \n " }, want: "ensure evidence is required"},
		{name: "oversized ensure evidence", mutate: func(rows *networkModelRows) {
			rows.Leases[0].EnsureEvidence = strings.Repeat("x", maximumNetworkEvidenceLength+1)
		}, want: "ensure evidence exceeds"},
		{name: "zero lease time", mutate: func(rows *networkModelRows) { rows.Leases[0].LeasedAt = time.Time{} }, want: "lease time"},
		{name: "non UTC lease time", mutate: func(rows *networkModelRows) {
			rows.Leases[0].LeasedAt = rows.Leases[0].LeasedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "missing active project reference", mutate: func(rows *networkModelRows) { rows.Leases[0].ProjectId = null.String{} }, want: "retain its source"},
		{name: "mismatched active project reference", mutate: func(rows *networkModelRows) { rows.Leases[0].ProjectId = null.StringFrom("project-alpha") }, want: "retain its source"},
		{name: "unknown active project", mutate: func(rows *networkModelRows) {
			rows.Leases[0].ProjectId = null.StringFrom("project-gamma")
			rows.Leases[0].SourceProjectId = "project-gamma"
		}, want: "project \"project-gamma\" is missing"},
		{name: "foreign active installation", mutate: func(rows *networkModelRows) { rows.Leases[0].OwnershipInstallationId = "installation-b" }, want: "belongs to installation"},
		{name: "active release fields", mutate: func(rows *networkModelRows) { rows.Leases[0].ReleaseGeneration = null.IntFrom(81) }, want: "contains release"},
		{name: "duplicate active key", mutate: func(rows *networkModelRows) {
			rows.Leases[2].Kind = "primary"
			rows.Leases[2].SecondaryId = ""
		}, want: "active lease key is duplicated"},
		{name: "unsupported state", mutate: func(rows *networkModelRows) { rows.Leases[0].State = "released" }, want: "state \"released\" is unsupported"},
		{name: "secondary without primary", mutate: func(rows *networkModelRows) {
			rows.Leases = append(rows.Leases[:1], rows.Leases[2])
			rows.Endpoints = rows.Endpoints[:1]
		}, want: "requires a primary network lease"},
		{name: "HTTP endpoint without primary", mutate: func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:1]
			rows.Endpoints = rows.Endpoints[:2]
		}, want: "endpoint requires a primary lease"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkLeaseConversionBuildsQuarantine verifies historical ownership remains valid and reuse time never expires a read-side block.
func TestNetworkLeaseConversionBuildsQuarantine(t *testing.T) {
	rows := validNetworkModelRows()
	quarantineNetworkLease(&rows.Leases[2])
	rows.Leases[2].OwnershipInstallationId = "installation-retired"
	rows.Leases[2].OwnershipGeneration = 91

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("quarantine conversion initialized = %t, error %v", initialized, err)
	}
	if len(record.Quarantines) != 1 || record.Quarantines[0].Address.String() != "127.77.0.12" {
		t.Fatalf("quarantines = %#v", record.Quarantines)
	}
	if len(record.Leases) != 2 {
		t.Fatalf("active leases = %d, want 2", len(record.Leases))
	}
}

// TestNetworkLeaseConversionRejectsQuarantineCorruption covers every nullable proof, timestamp, and reason field.
func TestNetworkLeaseConversionRejectsQuarantineCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.LoopbackAddressLease)
		want   string
	}{
		{name: "active project retained", mutate: func(row *models.LoopbackAddressLease) { row.ProjectId = null.StringFrom(row.SourceProjectId) }, want: "clear its active project"},
		{name: "missing release generation", mutate: func(row *models.LoopbackAddressLease) { row.ReleaseGeneration = null.Int{} }, want: "release generation"},
		{name: "stale release generation", mutate: func(row *models.LoopbackAddressLease) {
			row.ReleaseGeneration = null.IntFrom(int64(row.LeaseGeneration))
		}, want: "greater than"},
		{name: "missing release evidence", mutate: func(row *models.LoopbackAddressLease) { row.ReleaseEvidence = null.String{} }, want: "release evidence is required"},
		{name: "empty release evidence", mutate: func(row *models.LoopbackAddressLease) { row.ReleaseEvidence = null.StringFrom(" \t ") }, want: "release evidence is required"},
		{name: "oversized release evidence", mutate: func(row *models.LoopbackAddressLease) {
			row.ReleaseEvidence = null.StringFrom(strings.Repeat("x", maximumNetworkEvidenceLength+1))
		}, want: "release evidence exceeds"},
		{name: "missing release time", mutate: func(row *models.LoopbackAddressLease) { row.ReleasedAt = nil }, want: "times are required"},
		{name: "missing quarantine time", mutate: func(row *models.LoopbackAddressLease) { row.QuarantinedAt = nil }, want: "times are required"},
		{name: "missing reuse time", mutate: func(row *models.LoopbackAddressLease) { row.ReuseAfter = nil }, want: "times are required"},
		{name: "zero release time", mutate: func(row *models.LoopbackAddressLease) { *row.ReleasedAt = time.Time{} }, want: "release time"},
		{name: "zero reuse time", mutate: func(row *models.LoopbackAddressLease) { *row.ReuseAfter = time.Time{} }, want: "reuse time"},
		{name: "non UTC quarantine time", mutate: func(row *models.LoopbackAddressLease) {
			*row.QuarantinedAt = row.QuarantinedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "release before lease", mutate: func(row *models.LoopbackAddressLease) { *row.ReleasedAt = row.LeasedAt.Add(-time.Second) }, want: "precedes lease"},
		{name: "quarantine before release", mutate: func(row *models.LoopbackAddressLease) { *row.QuarantinedAt = row.ReleasedAt.Add(-time.Second) }, want: "precedes release"},
		{name: "reuse before quarantine", mutate: func(row *models.LoopbackAddressLease) { *row.ReuseAfter = row.QuarantinedAt.Add(-time.Second) }, want: "precedes quarantine"},
		{name: "missing reason", mutate: func(row *models.LoopbackAddressLease) { row.QuarantineReason = null.String{} }, want: "reason is required"},
		{name: "empty reason", mutate: func(row *models.LoopbackAddressLease) { row.QuarantineReason = null.StringFrom(" \n ") }, want: "reason is required"},
		{name: "padded reason", mutate: func(row *models.LoopbackAddressLease) { row.QuarantineReason = null.StringFrom(" released ") }, want: "surrounding whitespace"},
		{name: "oversized reason", mutate: func(row *models.LoopbackAddressLease) {
			row.QuarantineReason = null.StringFrom(strings.Repeat("x", maximumNetworkQuarantineReasonLength+1))
		}, want: "reason exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			quarantineNetworkLease(&rows.Leases[2])
			test.mutate(&rows.Leases[2])
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkReleaseConversionRejectsCorruption covers teardown ownership and releasing/completed lifecycle shapes.
func TestNetworkReleaseConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "zero ID", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].Id = 0 }), want: "database ID"},
		{name: "duplicate ID", mutate: addReleaseMutation(func(rows *networkModelRows) {
			duplicate := rows.Releases[0]
			rows.Releases = append(rows.Releases, duplicate)
		}), want: "database ID is duplicated"},
		{name: "wrong root", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].NetworkStateId = 2 }), want: "network state ID"},
		{name: "invalid source project", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].SourceProjectId = " bad " }), want: "project ID"},
		{name: "duplicate source project", mutate: addReleaseMutation(func(rows *networkModelRows) {
			duplicate := rows.Releases[0]
			duplicate.Id = 72
			duplicate.OperationId = "operation-release-beta"
			rows.Releases = append(rows.Releases, duplicate)
		}), want: "source project is duplicated"},
		{name: "invalid operation ID", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].OperationId = " bad " }), want: "operation ID"},
		{name: "duplicate operation", mutate: addReleaseMutation(func(rows *networkModelRows) {
			duplicate := rows.Releases[0]
			duplicate.Id = 72
			duplicate.ProjectId = null.StringFrom("project-beta")
			duplicate.SourceProjectId = "project-beta"
			rows.Releases = append(rows.Releases, duplicate)
		}), want: "operation is duplicated"},
		{name: "missing operation", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].OperationId = "operation-missing" }), want: "is missing"},
		{name: "wrong operation kind", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners[0].Kind = "project.register" }), want: "does not own unregister"},
		{name: "wrong operation project", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.ReleaseOwners[0].ProjectId = null.StringFrom("project-beta") }), want: "does not own unregister"},
		{name: "zero begin generation", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].BeginGeneration = 0 }), want: "begin generation"},
		{name: "zero begin time", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].BeganAt = time.Time{} }), want: "begin time"},
		{name: "releasing missing project reference", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].ProjectId = null.String{} }), want: "retain its source"},
		{name: "releasing mismatched project reference", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].ProjectId = null.StringFrom("project-beta") }), want: "retain its source"},
		{name: "releasing missing project", mutate: addReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ProjectId = null.StringFrom("project-gamma")
			rows.Releases[0].SourceProjectId = "project-gamma"
			rows.ReleaseOwners[0].ProjectId = null.StringFrom("project-gamma")
		}), want: "project \"project-gamma\" is missing"},
		{name: "releasing completion fields", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].CompletionGeneration = null.IntFrom(101) }), want: "contains completion fields"},
		{name: "releasing release digest", mutate: addReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseSetDigest = null.StringFrom("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		}), want: "contains completion fields"},
		{name: "unsupported state", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].State = "cancelled" }), want: "unsupported"},
		{name: "completed project retained", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].ProjectId = null.StringFrom("project-alpha") }), want: "clear its active project"},
		{name: "completed generation missing", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].CompletionGeneration = null.Int{} }), want: "completion generation"},
		{name: "completed generation stale", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].CompletionGeneration = null.IntFrom(100) }), want: "greater than"},
		{name: "completed time missing", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].CompletedAt = nil }), want: "completion time is required"},
		{name: "completed time zero", mutate: completedReleaseMutation(func(rows *networkModelRows) { *rows.Releases[0].CompletedAt = time.Time{} }), want: "completion time"},
		{name: "completed before begin", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			*rows.Releases[0].CompletedAt = rows.Releases[0].BeganAt.Add(-time.Second)
		}), want: "precedes begin"},
		{name: "completed evidence missing", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].ReleaseEvidence = null.String{} }), want: "release evidence is required"},
		{name: "completed evidence empty", mutate: completedReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].ReleaseEvidence = null.StringFrom(" \n ") }), want: "release evidence is required"},
		{name: "completed evidence oversized", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseEvidence = null.StringFrom(strings.Repeat("x", maximumNetworkEvidenceLength+1))
		}), want: "release evidence exceeds"},
		{name: "completed release digest missing", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseSetDigest = null.String{}
		}), want: "release set digest is required"},
		{name: "completed release digest short", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseSetDigest = null.StringFrom("abcd")
		}), want: "64 lowercase hexadecimal"},
		{name: "completed release digest uppercase", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseSetDigest = null.StringFrom("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeF")
		}), want: "64 lowercase hexadecimal"},
		{name: "completed release digest nonhex", mutate: completedReleaseMutation(func(rows *networkModelRows) {
			rows.Releases[0].ReleaseSetDigest = null.StringFrom("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg")
		}), want: "64 lowercase hexadecimal"},
		{name: "completed active lease retained", mutate: completedReleaseMutation(func(*networkModelRows) {}), want: "retains active address leases"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestValidateCompletedNetworkReleasesRejectsRetainedEndpoint reaches the endpoint-only completion guard independently of stronger lease invariants.
func TestValidateCompletedNetworkReleasesRejectsRetainedEndpoint(t *testing.T) {
	err := validateCompletedNetworkReleases(
		map[domain.ProjectID]networkReleaseState{"project-alpha": {Completed: true, CompletedAt: networkTestTime().Add(6 * time.Minute)}},
		map[domain.ProjectID]struct{}{},
		map[domain.ProjectID]int{},
		map[domain.ProjectID][]networkQuarantineState{},
		[]EndpointReservation{{Key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: "app"}}},
	)
	assertNetworkConversionCorruption(t, err, "retains public endpoints")
}

// TestNetworkProjectPrimaryLeaseInvariantCoversProjectsWithoutEndpoints verifies topology is not inferred only from public routes.
func TestNetworkProjectPrimaryLeaseInvariantCoversProjectsWithoutEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
	}{
		{name: "active project", mutate: func(rows *networkModelRows) {
			rows.Projects = append(rows.Projects, models.Project{Id: 63, ProjectId: "project-gamma", State: string(domain.ProjectReady)})
		}},
		{name: "releasing project", mutate: addReleaseMutation(func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:1]
			rows.Endpoints = rows.Endpoints[:1]
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, "requires a primary network lease")
		})
	}
}

// TestNetworkProjectPrimaryLeaseInvariantAllowsStoppedPendingProject verifies network reads tolerate registration before reconciliation.
func TestNetworkProjectPrimaryLeaseInvariantAllowsStoppedPendingProject(t *testing.T) {
	rows := validNetworkModelRows()
	rows.Projects = append(rows.Projects, models.Project{
		Id:        63,
		ProjectId: "project-gamma",
		State:     string(domain.ProjectStopped),
	})

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("pending project conversion = initialized %t, error %v", initialized, err)
	}
	if got := networkLeaseKeys(record.Leases); !reflect.DeepEqual(got, []string{"project-alpha/primary", "project-alpha/secondary/metrics", "project-beta/primary"}) {
		t.Fatalf("pending project conversion leases = %v", got)
	}
	if got := endpointHosts(record.Reservations.Endpoints); !reflect.DeepEqual(got, []string{"alpha.test", "mysql.alpha.test", "mysql.beta.test"}) {
		t.Fatalf("pending project conversion endpoints = %v", got)
	}
}

// TestNetworkCompletedReleaseAllowsDeletedProjectWithoutQuarantine verifies expired reuse may remove the last historical lease after project deletion.
func TestNetworkCompletedReleaseAllowsDeletedProjectWithoutQuarantine(t *testing.T) {
	rows := validNetworkModelRows()
	completedReleaseMutation(func(rows *networkModelRows) {
		rows.Projects = rows.Projects[:1]
		rows.Leases = rows.Leases[:1]
		rows.Endpoints = rows.Endpoints[:1]
	})(&rows)

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("completed release after project deletion initialized = %t, error %v", initialized, err)
	}
	if got := record.Reservations.SuppressedProjectIDs; !reflect.DeepEqual(got, []domain.ProjectID{"project-alpha"}) {
		t.Fatalf("suppressed projects = %v", got)
	}
}

// TestNetworkCompletedReleaseSurvivesConsumedLeaseRow proves a tombstone remains readable after its original address row is reused by another project.
func TestNetworkCompletedReleaseSurvivesConsumedLeaseRow(t *testing.T) {
	rows := validNetworkModelRows()
	completedReleaseMutation(func(rows *networkModelRows) {
		betaProject := rows.Projects[0]
		betaLease := rows.Leases[0]
		betaEndpoint := rows.Endpoints[0]
		reusedPrimary := activeNetworkLeaseModel(
			rows.Leases[1].Id,
			"project-gamma",
			"primary",
			"",
			rows.Leases[1].Address,
			3,
		)
		rows.Projects = []models.Project{betaProject, {Id: 63, ProjectId: "project-gamma"}}
		rows.Leases = []models.LoopbackAddressLease{betaLease, reusedPrimary}
		rows.Endpoints = []models.PublicEndpointLease{betaEndpoint}
	})(&rows)
	digest := rows.Releases[0].ReleaseSetDigest.String

	record, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("completed release after lease reuse initialized = %t, error %v", initialized, err)
	}
	if got := networkLeaseKeys(record.Leases); !reflect.DeepEqual(got, []string{"project-beta/primary", "project-gamma/primary"}) {
		t.Fatalf("leases after tombstone reuse = %v", got)
	}
	if got := record.Reservations.SuppressedProjectIDs; !reflect.DeepEqual(got, []domain.ProjectID{"project-alpha"}) {
		t.Fatalf("suppressed projects after tombstone reuse = %v", got)
	}
	if rows.Releases[0].ReleaseSetDigest.String != digest || digest == "" {
		t.Fatal("completed tombstone lost its release-set replay identity")
	}
}

// TestNetworkCompletedReleaseRequiresRetainedPrimaryQuarantine verifies an extant project cannot lose its teardown handoff evidence.
func TestNetworkCompletedReleaseRequiresRetainedPrimaryQuarantine(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "missing quarantine", mutate: func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:1]
			rows.Endpoints = rows.Endpoints[:1]
		}, want: "requires its source primary quarantine"},
		{name: "only secondary quarantine", mutate: func(rows *networkModelRows) {
			rows.Leases = append(rows.Leases[:1], rows.Leases[2])
			quarantineNetworkLease(&rows.Leases[1])
			rows.Endpoints = rows.Endpoints[:1]
		}, want: "requires its source primary quarantine"},
		{name: "release time after completion", mutate: func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:2]
			quarantineNetworkLease(&rows.Leases[1])
			releasedAt := networkTestTime().Add(7 * time.Minute)
			quarantinedAt := networkTestTime().Add(8 * time.Minute)
			reuseAfter := networkTestTime().Add(9 * time.Minute)
			rows.Leases[1].ReleasedAt = &releasedAt
			rows.Leases[1].QuarantinedAt = &quarantinedAt
			rows.Leases[1].ReuseAfter = &reuseAfter
			rows.Endpoints = rows.Endpoints[:1]
		}, want: "release time postdates"},
		{name: "quarantine time after completion", mutate: func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:2]
			quarantineNetworkLease(&rows.Leases[1])
			releasedAt := networkTestTime().Add(5 * time.Minute)
			quarantinedAt := networkTestTime().Add(7 * time.Minute)
			reuseAfter := networkTestTime().Add(8 * time.Minute)
			rows.Leases[1].ReleasedAt = &releasedAt
			rows.Leases[1].QuarantinedAt = &quarantinedAt
			rows.Leases[1].ReuseAfter = &reuseAfter
			rows.Endpoints = rows.Endpoints[:1]
		}, want: "quarantine time postdates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			completedReleaseMutation(test.mutate)(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkChildFactTimesCannotPostdateRoot covers every persisted fact timestamp owned by the aggregate revision.
func TestNetworkChildFactTimesCannotPostdateRoot(t *testing.T) {
	afterRoot := networkTestTime().Add(11 * time.Minute)
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "setup verification", mutate: func(rows *networkModelRows) { rows.SetupEvidence[0].VerifiedAt = afterRoot }, want: "setup verification time"},
		{name: "listener verification", mutate: func(rows *networkModelRows) { rows.Listeners[0].VerifiedAt = afterRoot }, want: "listener verification time"},
		{name: "lease", mutate: func(rows *networkModelRows) { rows.Leases[0].LeasedAt = afterRoot }, want: "network lease time"},
		{name: "quarantine release", mutate: func(rows *networkModelRows) {
			quarantineNetworkLease(&rows.Leases[2])
			releasedAt := afterRoot
			quarantinedAt := afterRoot.Add(time.Minute)
			reuseAfter := afterRoot.Add(2 * time.Minute)
			rows.Leases[2].ReleasedAt = &releasedAt
			rows.Leases[2].QuarantinedAt = &quarantinedAt
			rows.Leases[2].ReuseAfter = &reuseAfter
		}, want: "lease release time"},
		{name: "quarantine", mutate: func(rows *networkModelRows) {
			quarantineNetworkLease(&rows.Leases[2])
			releasedAt := networkTestTime().Add(9 * time.Minute)
			quarantinedAt := afterRoot
			reuseAfter := afterRoot.Add(time.Minute)
			rows.Leases[2].ReleasedAt = &releasedAt
			rows.Leases[2].QuarantinedAt = &quarantinedAt
			rows.Leases[2].ReuseAfter = &reuseAfter
		}, want: "lease quarantine time"},
		{name: "endpoint creation", mutate: func(rows *networkModelRows) {
			rows.Endpoints[0].CreatedAt = afterRoot
			rows.Endpoints[0].UpdatedAt = afterRoot.Add(time.Minute)
		}, want: "endpoint creation time"},
		{name: "endpoint update", mutate: func(rows *networkModelRows) { rows.Endpoints[0].UpdatedAt = afterRoot }, want: "endpoint update time"},
		{name: "release begin", mutate: addReleaseMutation(func(rows *networkModelRows) { rows.Releases[0].BeganAt = afterRoot }), want: "release begin time"},
		{name: "release completion", mutate: completedReleaseMutation(func(rows *networkModelRows) { *rows.Releases[0].CompletedAt = afterRoot }), want: "release completion time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
			if !strings.Contains(err.Error(), "after the network state update time") {
				t.Fatalf("error = %v, want root update bound", err)
			}
		})
	}
}

// TestNetworkQuarantineReuseTimeMayPostdateRoot verifies the future safety window is intent rather than a claimed historical fact.
func TestNetworkQuarantineReuseTimeMayPostdateRoot(t *testing.T) {
	rows := validNetworkModelRows()
	quarantineNetworkLease(&rows.Leases[2])
	reuseAfter := networkTestTime().Add(24 * time.Hour)
	rows.Leases[2].ReuseAfter = &reuseAfter

	_, initialized, err := networkRecordFromModels(rows)
	if err != nil || !initialized {
		t.Fatalf("future reuse conversion initialized = %t, error %v", initialized, err)
	}
}

// TestNetworkEndpointConversionRejectsCorruption covers endpoint identity, protocol, foreign-key, socket, host, and timestamp branches.
func TestNetworkEndpointConversionRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkModelRows)
		want   string
	}{
		{name: "zero ID", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Id = 0 }, want: "database ID"},
		{name: "duplicate ID", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Id = rows.Endpoints[0].Id }, want: "database ID is duplicated"},
		{name: "wrong root", mutate: func(rows *networkModelRows) { rows.Endpoints[0].NetworkStateId = 2 }, want: "network state ID"},
		{name: "invalid project ID", mutate: func(rows *networkModelRows) { rows.Endpoints[0].ProjectId = " bad " }, want: "project ID"},
		{name: "missing endpoint ID", mutate: func(rows *networkModelRows) { rows.Endpoints[0].EndpointId = "" }, want: "endpoint ID is required"},
		{name: "padded endpoint ID", mutate: func(rows *networkModelRows) { rows.Endpoints[0].EndpointId = " mysql " }, want: "surrounding whitespace"},
		{name: "oversized endpoint ID", mutate: func(rows *networkModelRows) {
			rows.Endpoints[0].EndpointId = strings.Repeat("x", maximumNetworkEndpointIDLength+1)
		}, want: "exceeds"},
		{name: "unsupported endpoint ID character", mutate: func(rows *networkModelRows) { rows.Endpoints[0].EndpointId = "mysql/socket" }, want: "unsupported character"},
		{name: "missing project", mutate: func(rows *networkModelRows) { rows.Endpoints[0].ProjectId = "project-gamma" }, want: "project \"project-gamma\" is missing"},
		{name: "duplicate project endpoint", mutate: func(rows *networkModelRows) {
			rows.Endpoints[1].ProjectId = rows.Endpoints[0].ProjectId
			rows.Endpoints[1].EndpointId = rows.Endpoints[0].EndpointId
		}, want: "project-scoped endpoint ID is duplicated"},
		{name: "duplicate hostname", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Hostname = rows.Endpoints[0].Hostname }, want: "hostname"},
		{name: "invalid public address", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Address = "bad" }, want: "invalid"},
		{name: "non loopback public address", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Address = "192.0.2.1" }, want: "IPv4 loopback"},
		{name: "bad public port", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Port = 0 }, want: "between 1 and 65535"},
		{name: "zero generation", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Generation = 0 }, want: "endpoint generation"},
		{name: "zero creation time", mutate: func(rows *networkModelRows) { rows.Endpoints[0].CreatedAt = time.Time{} }, want: "creation time"},
		{name: "zero update time", mutate: func(rows *networkModelRows) { rows.Endpoints[0].UpdatedAt = time.Time{} }, want: "update time"},
		{name: "non UTC update time", mutate: func(rows *networkModelRows) {
			rows.Endpoints[0].UpdatedAt = rows.Endpoints[0].UpdatedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "update before creation", mutate: func(rows *networkModelRows) {
			rows.Endpoints[0].UpdatedAt = rows.Endpoints[0].CreatedAt.Add(-time.Second)
		}, want: "precedes creation"},
		{name: "HTTP address lease", mutate: func(rows *networkModelRows) { rows.Endpoints[1].LoopbackAddressLeaseId = null.IntFrom(41) }, want: "HTTP endpoint must not reference"},
		{name: "HTTP wrong socket", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Port = 80 }, want: "advertised HTTPS socket"},
		{name: "TCP missing lease", mutate: func(rows *networkModelRows) { rows.Endpoints[0].LoopbackAddressLeaseId = null.Int{} }, want: "must reference an active"},
		{name: "TCP zero lease", mutate: func(rows *networkModelRows) { rows.Endpoints[0].LoopbackAddressLeaseId = null.IntFrom(0) }, want: "must reference an active"},
		{name: "TCP unknown lease", mutate: func(rows *networkModelRows) { rows.Endpoints[0].LoopbackAddressLeaseId = null.IntFrom(999) }, want: "missing or not active"},
		{name: "TCP foreign project lease", mutate: func(rows *networkModelRows) { rows.Endpoints[2].LoopbackAddressLeaseId = null.IntFrom(43) }, want: "does not own project"},
		{name: "TCP wrong lease address", mutate: func(rows *networkModelRows) { rows.Endpoints[2].Address = "127.77.0.11" }, want: "does not own project"},
		{name: "duplicate native socket", mutate: func(rows *networkModelRows) {
			rows.Endpoints[2].ProjectId = "project-beta"
			rows.Endpoints[2].EndpointId = "mysql-copy"
			rows.Endpoints[2].Address = "127.77.0.11"
			rows.Endpoints[2].LoopbackAddressLeaseId = null.IntFrom(43)
		}, want: "native socket"},
		{name: "unsupported protocol", mutate: func(rows *networkModelRows) { rows.Endpoints[0].Protocol = "udp" }, want: "protocol \"udp\" is unsupported"},
		{name: "uppercase host", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Hostname = "Alpha.test" }, want: "must be lowercase"},
		{name: "host outside test zone", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Hostname = "alpha.localhost" }, want: "outside the .test zone"},
		{name: "oversized host label", mutate: func(rows *networkModelRows) { rows.Endpoints[1].Hostname = strings.Repeat("a", 64) + ".test" }, want: "between 1 and 63 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			test.mutate(&rows)
			_, _, err := networkRecordFromModels(rows)
			assertNetworkConversionCorruption(t, err, test.want)
		})
	}
}

// TestNetworkEndpointConversionRejectsSharedSocketCollision covers native routing against every shared listener socket.
func TestNetworkEndpointConversionRejectsSharedSocketCollision(t *testing.T) {
	rows := validNetworkModelRows()
	listeners, err := networkListenersFromModels(rows.Listeners, 1, rows.States[0].UpdatedAt)
	if err != nil {
		t.Fatalf("networkListenersFromModels() error = %v", err)
	}
	key, err := identity.NewPrimaryKey("project-alpha")
	if err != nil {
		t.Fatalf("identity.NewPrimaryKey() error = %v", err)
	}
	lease := identity.Lease{
		Key:       key,
		Address:   netip.MustParseAddr("127.0.0.1"),
		Ownership: identity.Ownership{InstallationID: "installation-a", Generation: 1},
	}
	endpoint := tcpNetworkEndpointModel(1, "project-alpha", "database", "database.alpha.test", "127.0.0.1", 443, 1)
	_, err = networkEndpointsFromModels(
		[]models.PublicEndpointLease{endpoint},
		1,
		map[domain.ProjectID]struct{}{"project-alpha": {}},
		map[int]identity.Lease{1: lease},
		listeners,
		rows.States[0].UpdatedAt,
	)
	assertNetworkConversionCorruption(t, err, "collides with HTTPS listener")
}

// identityNetworkModelRows returns one identity-only aggregate with stopped projects and no data-plane children.
func identityNetworkModelRows() networkModelRows {
	rows := validNetworkModelRows()
	rows.States[0].Stage = string(NetworkStageIdentity)
	proofs := make([]models.NetworkSetupEvidence, 0, 2)
	for _, proof := range rows.SetupEvidence {
		if proof.Component == string(NetworkSetupComponentMachineOwnership) ||
			proof.Component == string(NetworkSetupComponentLoopbackPool) {
			proofs = append(proofs, proof)
		}
	}
	rows.SetupEvidence = proofs
	rows.Listeners = []models.NetworkSharedListener{}
	rows.Leases = []models.LoopbackAddressLease{}
	rows.Endpoints = []models.PublicEndpointLease{}
	rows.Releases = []models.NetworkProjectRelease{}
	for index := range rows.Projects {
		rows.Projects[index].State = string(domain.ProjectStopped)
	}
	return rows
}

// resolverNetworkModelRows returns one resolver-authorized aggregate without publishable listener or endpoint authority.
func resolverNetworkModelRows() networkModelRows {
	rows := identityNetworkModelRows()
	rows.States[0].Stage = string(NetworkStageResolver)
	rows.SetupEvidence = append(rows.SetupEvidence, models.NetworkSetupEvidence{
		Id:             23,
		NetworkStateId: 1,
		Component:      string(NetworkSetupComponentResolver),
		Evidence:       "verified resolver",
		Generation:     50,
		VerifiedAt:     networkTestTime(),
	})
	return rows
}

// validNetworkModelRows returns one complete aggregate with deliberately noncanonical input slice order.
func validNetworkModelRows() networkModelRows {
	at := networkTestTime()
	return networkModelRows{
		States: []models.NetworkState{{
			Id:                  1,
			Stage:               string(NetworkStageFull),
			InstallationId:      "installation-a",
			OwnershipGeneration: 3,
			PoolNetwork:         "127.77.0.0",
			PoolPrefixLength:    24,
			DnsSuffix:           ".test",
			CreatedAt:           at,
			UpdatedAt:           at.Add(10 * time.Minute),
			Revision:            7,
		}},
		Candidates: []models.NetworkPoolCandidate{
			{Id: 12, NetworkStateId: 1, Ordinal: 2, Address: "127.77.0.11", Generation: 40},
			{Id: 11, NetworkStateId: 1, Ordinal: 1, Address: "127.77.0.10", Generation: 40},
			{Id: 13, NetworkStateId: 1, Ordinal: 3, Address: "127.77.0.12", Generation: 40},
		},
		SetupEvidence: []models.NetworkSetupEvidence{
			{Id: 24, NetworkStateId: 1, Component: "low_ports", Evidence: " verified low ports ", Generation: 50, VerifiedAt: at},
			{Id: 21, NetworkStateId: 1, Component: "machine_ownership", Evidence: "verified owner", Generation: 50, VerifiedAt: at},
			{Id: 23, NetworkStateId: 1, Component: "resolver", Evidence: "verified resolver", Generation: 50, VerifiedAt: at},
			{Id: 22, NetworkStateId: 1, Component: "loopback_pool", Evidence: "verified pool", Generation: 50, VerifiedAt: at},
		},
		Listeners: []models.NetworkSharedListener{
			{Id: 33, NetworkStateId: 1, Kind: "https", Mode: "direct", AdvertisedAddress: "127.0.0.1", AdvertisedPort: 443, BindAddress: "127.0.0.1", BindPort: 443, Generation: 60, VerifiedAt: at},
			{Id: 31, NetworkStateId: 1, Kind: "dns", Mode: "redirect", AdvertisedAddress: "127.0.0.1", AdvertisedPort: 53, BindAddress: "127.0.0.1", BindPort: 10053, Generation: 60, VerifiedAt: at},
			{Id: 32, NetworkStateId: 1, Kind: "http", Mode: "redirect", AdvertisedAddress: "127.0.0.1", AdvertisedPort: 80, BindAddress: "127.0.0.1", BindPort: 10080, Generation: 60, VerifiedAt: at},
		},
		Leases: []models.LoopbackAddressLease{
			activeNetworkLeaseModel(43, "project-beta", "primary", "", "127.77.0.11", 2),
			activeNetworkLeaseModel(41, "project-alpha", "primary", "", "127.77.0.10", 2),
			activeNetworkLeaseModel(42, "project-alpha", "secondary", "metrics", "127.77.0.12", 2),
		},
		Endpoints: []models.PublicEndpointLease{
			tcpNetworkEndpointModel(53, "project-beta", "mysql", "mysql.beta.test", "127.77.0.11", 3306, 43),
			httpNetworkEndpointModel(51, "project-alpha", "app", "alpha.test"),
			tcpNetworkEndpointModel(52, "project-alpha", "mysql", "mysql.alpha.test", "127.77.0.10", 3306, 41),
		},
		Projects: []models.Project{
			{Id: 62, ProjectId: "project-beta"},
			{Id: 61, ProjectId: "project-alpha"},
		},
	}
}

// activeNetworkLeaseModel creates one valid active lease fixture.
func activeNetworkLeaseModel(id int, projectID string, kind string, secondaryID string, address string, ownershipGeneration int) models.LoopbackAddressLease {
	return models.LoopbackAddressLease{
		Id:                      id,
		NetworkStateId:          1,
		ProjectId:               null.StringFrom(projectID),
		SourceProjectId:         projectID,
		Kind:                    kind,
		SecondaryId:             secondaryID,
		Address:                 address,
		State:                   "leased",
		LeaseGeneration:         80,
		OwnershipInstallationId: "installation-a",
		OwnershipGeneration:     ownershipGeneration,
		EnsureEvidence:          " verified ensure evidence ",
		LeasedAt:                networkTestTime().Add(time.Minute),
	}
}

// httpNetworkEndpointModel creates one valid shared-ingress reservation fixture.
func httpNetworkEndpointModel(id int, projectID string, endpointID string, host string) models.PublicEndpointLease {
	return models.PublicEndpointLease{
		Id:             id,
		NetworkStateId: 1,
		ProjectId:      projectID,
		EndpointId:     endpointID,
		Protocol:       "http",
		Hostname:       host,
		Address:        "127.0.0.1",
		Port:           443,
		Generation:     99,
		CreatedAt:      networkTestTime().Add(2 * time.Minute),
		UpdatedAt:      networkTestTime().Add(3 * time.Minute),
	}
}

// tcpNetworkEndpointModel creates one valid native reservation fixture.
func tcpNetworkEndpointModel(id int, projectID string, endpointID string, host string, address string, port int, leaseID int64) models.PublicEndpointLease {
	row := httpNetworkEndpointModel(id, projectID, endpointID, host)
	row.Protocol = "tcp"
	row.Address = address
	row.Port = port
	row.LoopbackAddressLeaseId = null.IntFrom(leaseID)
	return row
}

// networkTestTime returns the UTC base shared by persistence fixtures.
func networkTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}

// networkListener returns the mutable listener fixture with the requested kind.
func networkListener(rows *networkModelRows, kind string) *models.NetworkSharedListener {
	for index := range rows.Listeners {
		if rows.Listeners[index].Kind == kind {
			return &rows.Listeners[index]
		}
	}
	panic("missing listener fixture " + kind)
}

// addReleaseMutation prepares a valid release before applying one targeted corruption.
func addReleaseMutation(mutate func(*networkModelRows)) func(*networkModelRows) {
	return func(rows *networkModelRows) {
		rows.ReleaseOwners = []models.Operation{{
			Id:        "operation-release-alpha",
			Kind:      string(domain.OperationKindProjectUnregister),
			ProjectId: null.StringFrom("project-alpha"),
		}}
		rows.Releases = []models.NetworkProjectRelease{{
			Id:              71,
			NetworkStateId:  1,
			ProjectId:       null.StringFrom("project-alpha"),
			SourceProjectId: "project-alpha",
			OperationId:     "operation-release-alpha",
			State:           "releasing",
			BeginGeneration: 100,
			BeganAt:         networkTestTime().Add(4 * time.Minute),
		}}
		mutate(rows)
	}
}

// completedReleaseMutation prepares one complete teardown proof while retaining resources unless the targeted mutation removes them.
func completedReleaseMutation(mutate func(*networkModelRows)) func(*networkModelRows) {
	return addReleaseMutation(func(rows *networkModelRows) {
		completedAt := networkTestTime().Add(6 * time.Minute)
		rows.Releases[0].State = "completed"
		rows.Releases[0].ProjectId = null.String{}
		rows.Releases[0].CompletionGeneration = null.IntFrom(101)
		rows.Releases[0].CompletedAt = &completedAt
		rows.Releases[0].ReleaseEvidence = null.StringFrom(" verified release evidence ")
		rows.Releases[0].ReleaseSetDigest = null.StringFrom("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		mutate(rows)
	})
}

// quarantineNetworkLease changes one active fixture into a complete historical reuse block.
func quarantineNetworkLease(row *models.LoopbackAddressLease) {
	releasedAt := networkTestTime().Add(2 * time.Minute)
	quarantinedAt := networkTestTime().Add(3 * time.Minute)
	reuseAfter := networkTestTime().Add(4 * time.Minute)
	row.ProjectId = null.String{}
	row.State = "quarantined"
	row.ReleaseGeneration = null.IntFrom(81)
	row.ReleaseEvidence = null.StringFrom(" verified release evidence ")
	row.ReleasedAt = &releasedAt
	row.QuarantinedAt = &quarantinedAt
	row.ReuseAfter = &reuseAfter
	row.QuarantineReason = null.StringFrom("released during project teardown")
}

// assertNetworkConversionCorruption requires the common typed boundary and a useful cause fragment.
func assertNetworkConversionCorruption(t *testing.T, err error, want string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error = %v, want CorruptStateError", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}

// addressStrings renders canonical addresses for readable assertions.
func addressStrings(addresses []netip.Addr) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, address.String())
	}
	return result
}

// networkLeaseKeys renders stable keys without exposing identity package internals.
func networkLeaseKeys(leases []identity.Lease) []string {
	result := make([]string, 0, len(leases))
	for _, lease := range leases {
		value := string(lease.Key.ProjectID) + "/" + string(lease.Key.Kind())
		if lease.Key.SecondaryID != "" {
			value += "/" + lease.Key.SecondaryID
		}
		result = append(result, value)
	}
	return result
}

// endpointHosts renders canonical public names for readable assertions.
func endpointHosts(endpoints []EndpointReservation) []string {
	result := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		result = append(result, endpoint.Host)
	}
	return result
}
