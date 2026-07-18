package state

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestNetworkMutationPrimitiveContractsAcceptCanonicalValues verifies every reusable write fact accepts its durable shape.
func TestNetworkMutationPrimitiveContractsAcceptCanonicalValues(t *testing.T) {
	for _, component := range []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
		NetworkSetupComponentResolver,
		NetworkSetupComponentLowPorts,
	} {
		if err := component.Validate(); err != nil {
			t.Fatalf("NetworkSetupComponent(%q).Validate() error = %v", component, err)
		}
	}
	if err := (NetworkProjectRevision{ProjectID: "project-alpha", Revision: 3}).Validate(); err != nil {
		t.Fatalf("NetworkProjectRevision.Validate() error = %v", err)
	}
	if err := networkMutationTestSetupProof(NetworkSetupComponentResolver).Validate(); err != nil {
		t.Fatalf("NetworkSetupProof.Validate() error = %v", err)
	}
	if err := networkMutationTestEnsure("project-alpha", "", "127.77.0.10").Validate(); err != nil {
		t.Fatalf("NetworkLeaseEnsure.Validate() error = %v", err)
	}
	release := networkMutationTestRelease("project-alpha", "", "127.77.0.10")
	release.ReuseAfter = networkMutationTestTime().Add(24 * time.Hour)
	if err := release.Validate(); err != nil {
		t.Fatalf("NetworkLeaseRelease.Validate() error = %v", err)
	}
}

// TestNetworkMutationPrimitiveContractsRejectCorruption covers value-local persistence bounds before aggregate validation.
func TestNetworkMutationPrimitiveContractsRejectCorruption(t *testing.T) {
	t.Run("project revision", func(t *testing.T) {
		tests := []struct {
			name  string
			value NetworkProjectRevision
			want  string
		}{
			{name: "project", value: NetworkProjectRevision{ProjectID: " bad ", Revision: 1}, want: "project ID"},
			{name: "zero revision", value: NetworkProjectRevision{ProjectID: "project-alpha"}, want: "expected project revision must be positive"},
			{name: "overflow revision", value: NetworkProjectRevision{ProjectID: "project-alpha", Revision: domain.MaximumSequence + 1}, want: "cross-client ordering"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				assertNetworkMutationValidationError(t, test.value.Validate(), test.want)
			})
		}
	})

	t.Run("setup proof", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*NetworkSetupProof)
			want   string
		}{
			{name: "component", mutate: func(proof *NetworkSetupProof) { proof.Component = "trust" }, want: "unsupported"},
			{name: "generation", mutate: func(proof *NetworkSetupProof) { proof.Generation = 0 }, want: "generation must be positive"},
			{name: "generation overflow", mutate: func(proof *NetworkSetupProof) { proof.Generation = recordTestOverflowUint() }, want: "database range"},
			{name: "evidence", mutate: func(proof *NetworkSetupProof) { proof.Evidence = " \n " }, want: "evidence is required"},
			{name: "evidence overflow", mutate: func(proof *NetworkSetupProof) { proof.Evidence = strings.Repeat("x", maximumNetworkEvidenceLength+1) }, want: "evidence exceeds"},
			{name: "time", mutate: func(proof *NetworkSetupProof) { proof.VerifiedAt = time.Time{} }, want: "verification time"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				proof := networkMutationTestSetupProof(NetworkSetupComponentResolver)
				test.mutate(&proof)
				assertNetworkMutationValidationError(t, proof.Validate(), test.want)
			})
		}
	})

	t.Run("ensure", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*NetworkLeaseEnsure)
			want   string
		}{
			{name: "lease key", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Lease.Key.ProjectID = " bad " }, want: "project ID"},
			{name: "lease address", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Lease.Address = netip.MustParseAddr("192.0.2.1") }, want: "IPv4 loopback"},
			{name: "mapped address", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Lease.Address = netip.MustParseAddr("::ffff:127.77.0.10") }, want: "canonical IPv4 form"},
			{name: "ownership", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Lease.Ownership.InstallationID = "-bad" }, want: "installation ID"},
			{name: "ownership overflow", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Lease.Ownership.Generation = recordTestOverflowUint() }, want: "database range"},
			{name: "generation", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Generation = 0 }, want: "generation must be positive"},
			{name: "generation overflow", mutate: func(ensure *NetworkLeaseEnsure) { ensure.Generation = recordTestOverflowUint() }, want: "database range"},
			{name: "evidence", mutate: func(ensure *NetworkLeaseEnsure) { ensure.EnsureEvidence = " \t " }, want: "evidence is required"},
			{name: "time", mutate: func(ensure *NetworkLeaseEnsure) { ensure.LeasedAt = time.Time{} }, want: "ensure time"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				ensure := networkMutationTestEnsure("project-alpha", "", "127.77.0.10")
				test.mutate(&ensure)
				assertNetworkMutationValidationError(t, ensure.Validate(), test.want)
			})
		}
	})

	t.Run("release", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*NetworkLeaseRelease)
			want   string
		}{
			{name: "lease", mutate: func(release *NetworkLeaseRelease) { release.Lease.Key.ProjectID = " bad " }, want: "project ID"},
			{name: "generation", mutate: func(release *NetworkLeaseRelease) { release.ReleaseGeneration = 0 }, want: "generation must be positive"},
			{name: "generation overflow", mutate: func(release *NetworkLeaseRelease) { release.ReleaseGeneration = recordTestOverflowUint() }, want: "database range"},
			{name: "evidence", mutate: func(release *NetworkLeaseRelease) { release.ReleaseEvidence = " \n " }, want: "evidence is required"},
			{name: "release time", mutate: func(release *NetworkLeaseRelease) { release.ReleasedAt = time.Time{} }, want: "release time"},
			{name: "quarantine time", mutate: func(release *NetworkLeaseRelease) { release.QuarantinedAt = time.Time{} }, want: "quarantine time"},
			{name: "reuse time", mutate: func(release *NetworkLeaseRelease) { release.ReuseAfter = time.Time{} }, want: "reuse time"},
			{name: "quarantine before release", mutate: func(release *NetworkLeaseRelease) { release.QuarantinedAt = release.ReleasedAt.Add(-time.Second) }, want: "must not precede release"},
			{name: "reuse before quarantine", mutate: func(release *NetworkLeaseRelease) { release.ReuseAfter = release.QuarantinedAt.Add(-time.Second) }, want: "must not precede quarantine"},
			{name: "reason", mutate: func(release *NetworkLeaseRelease) { release.QuarantineReason = "" }, want: "reason is required"},
			{name: "padded reason", mutate: func(release *NetworkLeaseRelease) { release.QuarantineReason = " released " }, want: "surrounding whitespace"},
			{name: "reason overflow", mutate: func(release *NetworkLeaseRelease) {
				release.QuarantineReason = strings.Repeat("x", maximumNetworkQuarantineReasonLength+1)
			}, want: "reason exceeds"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				release := networkMutationTestRelease("project-alpha", "", "127.77.0.10")
				test.mutate(&release)
				assertNetworkMutationValidationError(t, release.Validate(), test.want)
			})
		}
	})
}

// TestInitializeNetworkRequestAcceptsCanonicalPlans covers populated and deliberately empty first-run topologies.
func TestInitializeNetworkRequestAcceptsCanonicalPlans(t *testing.T) {
	request := networkMutationTestInitializeRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("InitializeNetworkRequest.Validate() error = %v", err)
	}
	for _, ensure := range request.Ensures {
		if ensure.Lease.Ownership.Generation == request.Ownership.Generation {
			t.Fatalf("lease ownership generation = root generation %d; fixture must prove independent host-effect generations", request.Ownership.Generation)
		}
	}

	empty := networkMutationTestInitializeRequest()
	empty.ExpectedProjects = []NetworkProjectRevision{}
	empty.Ensures = []NetworkLeaseEnsure{}
	empty.Endpoints = []EndpointReservation{}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty InitializeNetworkRequest.Validate() error = %v", err)
	}
}

// TestReplaceProjectNetworkRequestAcceptsReallocationAndNoop verifies delta semantics preserve retained hidden evidence.
func TestReplaceProjectNetworkRequestAcceptsReallocationAndNoop(t *testing.T) {
	request := networkMutationTestReplaceRequest()
	if err := request.Validate(); err != nil {
		t.Fatalf("ReplaceProjectNetworkRequest.Validate() error = %v", err)
	}
	if request.Ensures[0].Lease.Key != request.Releases[0].Lease.Key || request.Ensures[0].Lease.Address == request.Releases[0].Lease.Address {
		t.Fatalf("reallocation fixture = ensure %#v, release %#v", request.Ensures[0].Lease, request.Releases[0].Lease)
	}

	noop := networkMutationTestReplaceRequest()
	noop.Ensures = []NetworkLeaseEnsure{}
	noop.Releases = []NetworkLeaseRelease{}
	noop.Endpoints = []EndpointReservation{}
	if err := noop.Validate(); err != nil {
		t.Fatalf("no-op ReplaceProjectNetworkRequest.Validate() error = %v", err)
	}
}

// TestInitializeNetworkRequestRejectsCorruption covers optimistic project scope, exact host facts, and aggregate topology.
func TestInitializeNetworkRequestRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InitializeNetworkRequest)
		want   string
	}{
		{name: "network revision", mutate: func(request *InitializeNetworkRequest) { request.ExpectedNetworkRevision = 1 }, want: "must be zero"},
		{name: "network revision overflow", mutate: func(request *InitializeNetworkRequest) { request.ExpectedNetworkRevision = domain.MaximumSequence + 1 }, want: "cross-client ordering"},
		{name: "time", mutate: func(request *InitializeNetworkRequest) { request.At = time.Time{} }, want: "initialization time"},
		{name: "non UTC time", mutate: func(request *InitializeNetworkRequest) { request.At = request.At.In(time.FixedZone("offset", 3600)) }, want: "use UTC"},
		{name: "nil projects", mutate: func(request *InitializeNetworkRequest) { request.ExpectedProjects = nil }, want: "expected network projects must be initialized"},
		{name: "invalid project", mutate: func(request *InitializeNetworkRequest) { request.ExpectedProjects[0].ProjectID = " bad " }, want: "project ID"},
		{name: "zero project revision", mutate: func(request *InitializeNetworkRequest) { request.ExpectedProjects[0].Revision = 0 }, want: "project revision must be positive"},
		{name: "project order", mutate: func(request *InitializeNetworkRequest) {
			request.ExpectedProjects[0], request.ExpectedProjects[1] = request.ExpectedProjects[1], request.ExpectedProjects[0]
		}, want: "unique and ordered"},
		{name: "duplicate project", mutate: func(request *InitializeNetworkRequest) { request.ExpectedProjects[1] = request.ExpectedProjects[0] }, want: "unique and ordered"},
		{name: "duplicate project revision", mutate: func(request *InitializeNetworkRequest) {
			request.ExpectedProjects[1].Revision = request.ExpectedProjects[0].Revision
		}, want: "revision 5 is shared"},
		{name: "ownership", mutate: func(request *InitializeNetworkRequest) { request.Ownership.InstallationID = "-bad" }, want: "installation ID"},
		{name: "ownership overflow", mutate: func(request *InitializeNetworkRequest) { request.Ownership.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "pool", mutate: func(request *InitializeNetworkRequest) { request.Pool = identity.Pool{} }, want: "prefix is invalid"},
		{name: "pool capacity", mutate: func(request *InitializeNetworkRequest) { request.Pool = networkMutationTestOversizedPool(t) }, want: "maximum is 65535"},
		{name: "pool generation", mutate: func(request *InitializeNetworkRequest) { request.PoolGeneration = 0 }, want: "pool generation must be positive"},
		{name: "pool generation overflow", mutate: func(request *InitializeNetworkRequest) { request.PoolGeneration = recordTestOverflowUint() }, want: "database range"},
		{name: "nil setup", mutate: func(request *InitializeNetworkRequest) { request.Setup = nil }, want: "setup proofs must be initialized"},
		{name: "setup count", mutate: func(request *InitializeNetworkRequest) { request.Setup = request.Setup[:3] }, want: "expected 4"},
		{name: "setup order", mutate: func(request *InitializeNetworkRequest) {
			request.Setup[0], request.Setup[1] = request.Setup[1], request.Setup[0]
		}, want: "expected \"machine_ownership\""},
		{name: "setup proof", mutate: func(request *InitializeNetworkRequest) { request.Setup[0].Generation = 0 }, want: "proof generation"},
		{name: "future setup proof", mutate: func(request *InitializeNetworkRequest) { request.Setup[0].VerifiedAt = request.At.Add(time.Second) }, want: "after the network mutation time"},
		{name: "listeners", mutate: func(request *InitializeNetworkRequest) { request.Listeners.DNS.Mode = "proxy" }, want: "mode"},
		{name: "future listener", mutate: func(request *InitializeNetworkRequest) {
			request.Listeners.DNS.VerifiedAt = request.At.Add(time.Second)
		}, want: "after the network mutation time"},
		{name: "nil ensures", mutate: func(request *InitializeNetworkRequest) { request.Ensures = nil }, want: "lease ensures must be initialized"},
		{name: "invalid ensure", mutate: func(request *InitializeNetworkRequest) { request.Ensures[0].Generation = 0 }, want: "lease ensure 0"},
		{name: "future ensure", mutate: func(request *InitializeNetworkRequest) { request.Ensures[0].LeasedAt = request.At.Add(time.Second) }, want: "after the network mutation time"},
		{name: "ensure order", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[0], request.Ensures[1] = request.Ensures[1], request.Ensures[0]
		}, want: "unique and ordered"},
		{name: "duplicate ensure address", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[1].Lease.Address = request.Ensures[0].Lease.Address
		}, want: "address"},
		{name: "nil endpoints", mutate: func(request *InitializeNetworkRequest) { request.Endpoints = nil }, want: "endpoints must be initialized"},
		{name: "invalid endpoint", mutate: func(request *InitializeNetworkRequest) { request.Endpoints[0].Host = "Alpha.test" }, want: "lowercase"},
		{name: "endpoint order", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints[0], request.Endpoints[1] = request.Endpoints[1], request.Endpoints[0]
		}, want: "unique and ordered"},
		{name: "duplicate endpoint key", mutate: func(request *InitializeNetworkRequest) { request.Endpoints[1].Key = request.Endpoints[0].Key }, want: "key"},
		{name: "duplicate endpoint host", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints[1].Host = request.Endpoints[0].Host
			request.Endpoints[1].Key.EndpointID = "z-web"
		}, want: "host"},
		{name: "duplicate native socket", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints = append(request.Endpoints, recordTestTCPEndpoint("postgres.alpha.test", "project-alpha", "postgres", "127.77.0.10:3306"))
		}, want: "socket"},
		{name: "unexpected lease project", mutate: func(request *InitializeNetworkRequest) { request.Ensures[2].Lease.Key.ProjectID = "project-gamma" }, want: "unexpected project"},
		{name: "unexpected endpoint project", mutate: func(request *InitializeNetworkRequest) { request.Endpoints[1].Key.ProjectID = "project-gamma" }, want: "unexpected project"},
		{name: "project missing primary", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures = request.Ensures[:2]
			request.Endpoints = append(request.Endpoints[:1], request.Endpoints[2:]...)
		}, want: "requires an initial primary"},
		{name: "lease installation mismatch", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[0].Lease.Ownership.InstallationID = "other-installation"
		}, want: "initialized installation"},
		{name: "lease outside pool", mutate: func(request *InitializeNetworkRequest) {
			request.Pool = recordTestPool("127.77.0.0/24", "127.77.0.11", "127.77.0.12")
		}, want: "not a pool candidate"},
		{name: "listener uses pool candidate", mutate: func(request *InitializeNetworkRequest) {
			request.Pool = recordTestPool("127.0.0.0/8", "127.0.0.1", "127.77.0.10", "127.77.0.11", "127.77.0.12")
		}, want: "also a project pool candidate"},
		{name: "HTTP endpoint wrong listener", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.1:80")
		}, want: "advertised HTTPS socket"},
		{name: "TCP endpoint missing lease", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints[2].Identity = recordTestLeaseKeyPointer("project-alpha", "unknown")
		}, want: "unknown network lease"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := networkMutationTestInitializeRequest()
			test.mutate(&request)
			assertNetworkMutationValidationError(t, request.Validate(), test.want)
		})
	}
}

// TestReplaceProjectNetworkRequestRejectsCorruption covers project-scoped optimistic deltas and completed host facts.
func TestReplaceProjectNetworkRequestRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReplaceProjectNetworkRequest)
		want   string
	}{
		{name: "project", mutate: func(request *ReplaceProjectNetworkRequest) { request.ProjectID = " bad " }, want: "project ID"},
		{name: "network revision", mutate: func(request *ReplaceProjectNetworkRequest) { request.ExpectedNetworkRevision = 0 }, want: "network revision must be positive"},
		{name: "network revision overflow", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.ExpectedNetworkRevision = domain.MaximumSequence + 1
		}, want: "cross-client ordering"},
		{name: "project revision", mutate: func(request *ReplaceProjectNetworkRequest) { request.ExpectedProjectRevision = 0 }, want: "project revision must be positive"},
		{name: "project revision overflow", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.ExpectedProjectRevision = domain.MaximumSequence + 1
		}, want: "cross-client ordering"},
		{name: "shared owner revision", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.ExpectedProjectRevision = request.ExpectedNetworkRevision
		}, want: "cannot share expected revision"},
		{name: "time", mutate: func(request *ReplaceProjectNetworkRequest) { request.At = time.Time{} }, want: "replacement time"},
		{name: "non UTC time", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.At = request.At.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "nil ensures", mutate: func(request *ReplaceProjectNetworkRequest) { request.Ensures = nil }, want: "lease ensures must be initialized"},
		{name: "invalid ensure", mutate: func(request *ReplaceProjectNetworkRequest) { request.Ensures[0].Generation = 0 }, want: "lease ensure 0"},
		{name: "ensure project", mutate: func(request *ReplaceProjectNetworkRequest) { request.Ensures[0].Lease.Key.ProjectID = "project-beta" }, want: "belongs to project"},
		{name: "future ensure", mutate: func(request *ReplaceProjectNetworkRequest) { request.Ensures[0].LeasedAt = request.At.Add(time.Second) }, want: "after the network mutation time"},
		{name: "ensure order", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Ensures = append(request.Ensures, networkMutationTestEnsure("project-alpha", "metrics", "127.77.0.11"))
			request.Ensures[0], request.Ensures[1] = request.Ensures[1], request.Ensures[0]
		}, want: "unique and ordered"},
		{name: "duplicate ensure address", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Ensures = append(request.Ensures, networkMutationTestEnsure("project-alpha", "metrics", "127.77.0.12"))
		}, want: "address"},
		{name: "nil releases", mutate: func(request *ReplaceProjectNetworkRequest) { request.Releases = nil }, want: "lease releases must be initialized"},
		{name: "invalid release", mutate: func(request *ReplaceProjectNetworkRequest) { request.Releases[0].ReleaseGeneration = 0 }, want: "lease release 0"},
		{name: "release project", mutate: func(request *ReplaceProjectNetworkRequest) { request.Releases[0].Lease.Key.ProjectID = "project-beta" }, want: "belongs to project"},
		{name: "future release", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Releases[0].ReleasedAt = request.At.Add(time.Second)
			request.Releases[0].QuarantinedAt = request.At.Add(2 * time.Second)
			request.Releases[0].ReuseAfter = request.At.Add(time.Hour)
		}, want: "after the network mutation time"},
		{name: "future quarantine", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Releases[0].ReleasedAt = request.At
			request.Releases[0].QuarantinedAt = request.At.Add(time.Second)
			request.Releases[0].ReuseAfter = request.At.Add(time.Hour)
		}, want: "after the network mutation time"},
		{name: "release order", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Releases = append(request.Releases, networkMutationTestRelease("project-alpha", "metrics", "127.77.0.11"))
			request.Releases[0], request.Releases[1] = request.Releases[1], request.Releases[0]
		}, want: "unique and ordered"},
		{name: "duplicate release address", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Releases = append(request.Releases, networkMutationTestRelease("project-alpha", "metrics", "127.77.0.10"))
		}, want: "address"},
		{name: "nil endpoints", mutate: func(request *ReplaceProjectNetworkRequest) { request.Endpoints = nil }, want: "endpoints must be initialized"},
		{name: "invalid endpoint", mutate: func(request *ReplaceProjectNetworkRequest) { request.Endpoints[0].Host = "Alpha.test" }, want: "lowercase"},
		{name: "endpoint project", mutate: func(request *ReplaceProjectNetworkRequest) { request.Endpoints[0].Key.ProjectID = "project-beta" }, want: "belongs to project"},
		{name: "endpoint order", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Endpoints[0], request.Endpoints[1] = request.Endpoints[1], request.Endpoints[0]
		}, want: "unique and ordered"},
		{name: "duplicate endpoint key", mutate: func(request *ReplaceProjectNetworkRequest) { request.Endpoints[1].Key = request.Endpoints[0].Key }, want: "key"},
		{name: "duplicate endpoint host", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Endpoints[1].Host = request.Endpoints[0].Host
			request.Endpoints[1].Key.EndpointID = "z-web"
		}, want: "host"},
		{name: "duplicate native socket", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Endpoints = append(request.Endpoints, recordTestTCPEndpoint("postgres.alpha.test", "project-alpha", "postgres", "127.77.0.12:3306"))
		}, want: "socket"},
		{name: "same key and address released and ensured", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Ensures[0].Lease.Address = request.Releases[0].Lease.Address
		}, want: "same address"},
		{name: "same address released and ensured under distinct keys", mutate: func(request *ReplaceProjectNetworkRequest) {
			request.Ensures[0] = networkMutationTestEnsure("project-alpha", "metrics", "127.77.0.10")
		}, want: "both released and ensured"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := networkMutationTestReplaceRequest()
			test.mutate(&request)
			assertNetworkMutationValidationError(t, request.Validate(), test.want)
		})
	}
}

// TestNetworkMutationResultKeepsReplayResponsesPayloadSafe verifies applied and replayed writes share one read projection.
func TestNetworkMutationResultKeepsReplayResponsesPayloadSafe(t *testing.T) {
	for _, replayed := range []bool{false, true} {
		result := NetworkMutationResult{Record: recordTestNetworkRecord(), Replayed: replayed}
		if err := result.Validate(); err != nil {
			t.Fatalf("NetworkMutationResult{Replayed: %t}.Validate() error = %v", replayed, err)
		}
	}

	invalid := NetworkMutationResult{Record: recordTestNetworkRecord()}
	invalid.Record.Revision = 0
	assertNetworkMutationValidationError(t, invalid.Validate(), "network mutation result")

	resultType := reflect.TypeOf(NetworkMutationResult{})
	gotFields := make([]string, 0, resultType.NumField())
	for index := 0; index < resultType.NumField(); index++ {
		gotFields = append(gotFields, resultType.Field(index).Name)
	}
	wantFields := []string{"Record", "Replayed"}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("NetworkMutationResult fields = %v, want payload-safe surface %v", gotFields, wantFields)
	}
}

// networkMutationTestSetupProof returns one sanitized machine-scoped setup fact.
func networkMutationTestSetupProof(component NetworkSetupComponent) NetworkSetupProof {
	return NetworkSetupProof{
		Component:  component,
		Evidence:   " verified postcondition ",
		Generation: 13,
		VerifiedAt: networkMutationTestTime().Add(-9 * time.Minute),
	}
}

// networkMutationTestEnsure returns one completed lease ensure fact.
func networkMutationTestEnsure(projectID domain.ProjectID, secondaryID string, address string) NetworkLeaseEnsure {
	generation := uint64(3)
	if secondaryID != "" {
		generation = 4
	}
	if projectID == "project-beta" {
		generation = 5
	}
	return NetworkLeaseEnsure{
		Lease:          recordTestLease(projectID, secondaryID, address, generation),
		Generation:     20,
		EnsureEvidence: " verified ensure ",
		LeasedAt:       networkMutationTestTime().Add(-8 * time.Minute),
	}
}

// networkMutationTestRelease returns one completed lease release and quarantine fact.
func networkMutationTestRelease(projectID domain.ProjectID, secondaryID string, address string) NetworkLeaseRelease {
	return NetworkLeaseRelease{
		Lease:             recordTestLease(projectID, secondaryID, address, 3),
		ReleaseGeneration: 21,
		ReleaseEvidence:   " verified release ",
		ReleasedAt:        networkMutationTestTime().Add(-7 * time.Minute),
		QuarantinedAt:     networkMutationTestTime().Add(-6 * time.Minute),
		ReuseAfter:        networkMutationTestTime().Add(20 * time.Minute),
		QuarantineReason:  "verified release pending reuse",
	}
}

// networkMutationTestTime returns the root timestamp for stable mutation fixtures.
func networkMutationTestTime() time.Time {
	return recordTestTime().Add(10 * time.Minute)
}

// networkMutationTestInitializeRequest returns a complete initial network plan for two projects.
func networkMutationTestInitializeRequest() InitializeNetworkRequest {
	return InitializeNetworkRequest{
		ExpectedNetworkRevision: 0,
		ExpectedProjects: []NetworkProjectRevision{
			{ProjectID: "project-alpha", Revision: 5},
			{ProjectID: "project-beta", Revision: 6},
		},
		Ownership:      identity.Ownership{InstallationID: "harbor-installation", Generation: 9},
		Pool:           recordTestPool("127.77.0.0/24", "127.77.0.10", "127.77.0.11", "127.77.0.12"),
		PoolGeneration: 14,
		Setup: []NetworkSetupProof{
			networkMutationTestSetupProof(NetworkSetupComponentMachineOwnership),
			networkMutationTestSetupProof(NetworkSetupComponentLoopbackPool),
			networkMutationTestSetupProof(NetworkSetupComponentResolver),
			networkMutationTestSetupProof(NetworkSetupComponentLowPorts),
		},
		Listeners: recordTestListeners(),
		Ensures: []NetworkLeaseEnsure{
			networkMutationTestEnsure("project-alpha", "", "127.77.0.10"),
			networkMutationTestEnsure("project-alpha", "metrics", "127.77.0.12"),
			networkMutationTestEnsure("project-beta", "", "127.77.0.11"),
		},
		Endpoints: []EndpointReservation{
			recordTestHTTPEndpoint("alpha.test", "project-alpha", "web"),
			recordTestHTTPEndpoint("beta.test", "project-beta", "web"),
			recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306"),
		},
		At: networkMutationTestTime(),
	}
}

// networkMutationTestReplaceRequest returns one project reallocation with replacement public reservations.
func networkMutationTestReplaceRequest() ReplaceProjectNetworkRequest {
	return ReplaceProjectNetworkRequest{
		ProjectID:               "project-alpha",
		ExpectedNetworkRevision: 21,
		ExpectedProjectRevision: 5,
		Ensures: []NetworkLeaseEnsure{
			networkMutationTestEnsure("project-alpha", "", "127.77.0.12"),
		},
		Releases: []NetworkLeaseRelease{
			networkMutationTestRelease("project-alpha", "", "127.77.0.10"),
		},
		Endpoints: []EndpointReservation{
			recordTestHTTPEndpoint("alpha.test", "project-alpha", "web"),
			recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.12:3306"),
		},
		At: networkMutationTestTime(),
	}
}

// networkMutationTestOversizedPool returns one valid pool that exceeds the durable candidate row bound.
func networkMutationTestOversizedPool(t *testing.T) identity.Pool {
	t.Helper()
	addresses := make([]netip.Addr, maximumNetworkPoolCandidateCount+1)
	for index := range addresses {
		addresses[index] = netip.AddrFrom4([4]byte{127, 1, byte(index >> 8), byte(index)})
	}
	pool, err := identity.NewPool(netip.MustParsePrefix("127.0.0.0/8"), addresses)
	if err != nil {
		t.Fatalf("identity.NewPool() error = %v", err)
	}
	return pool
}

// assertNetworkMutationValidationError requires the expected validation boundary and diagnostic fragment.
func assertNetworkMutationValidationError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("validation error = nil, want containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("validation error = %q, want containing %q", err, want)
	}
}
