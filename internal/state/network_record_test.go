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

// TestNetworkStageValidate covers the complete durable lifecycle vocabulary.
func TestNetworkStageValidate(t *testing.T) {
	for _, test := range []struct {
		stage   NetworkStage
		wantErr bool
	}{
		{stage: NetworkStageIdentity},
		{stage: NetworkStageResolver},
		{stage: NetworkStageFull},
		{stage: "", wantErr: true},
		{stage: "partial", wantErr: true},
	} {
		err := test.stage.Validate()
		if (err != nil) != test.wantErr {
			t.Fatalf("NetworkStage(%q).Validate() error = %v, wantErr %t", test.stage, err, test.wantErr)
		}
	}
}

// TestNetworkRecordValidateSeparatesIdentityFromDataPlaneAuthority verifies the lifecycle stage gates listeners and public endpoints.
func TestNetworkRecordValidateSeparatesIdentityFromDataPlaneAuthority(t *testing.T) {
	identityRecord := recordTestNetworkRecord()
	identityRecord.Stage = NetworkStageIdentity
	identityRecord.Reservations.Listeners = SharedListenerReservations{}
	identityRecord.Reservations.Endpoints = []EndpointReservation{}
	if err := identityRecord.Validate(); err != nil {
		t.Fatalf("identity NetworkRecord.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*NetworkRecord)
		want   string
	}{
		{name: "listener", mutate: func(record *NetworkRecord) {
			record.Reservations.Listeners.DNS = recordTestListeners().DNS
		}, want: "must not contain listener reservations"},
		{name: "endpoint", mutate: func(record *NetworkRecord) {
			record.Reservations.Endpoints = []EndpointReservation{recordTestHTTPEndpoint("alpha.test", "project-alpha", "web")}
		}, want: "must not contain endpoint reservations"},
		{name: "unknown stage", mutate: func(record *NetworkRecord) {
			record.Stage = "partial"
		}, want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := identityRecord
			candidate.Reservations.Endpoints = []EndpointReservation{}
			test.mutate(&candidate)
			assertRecordValidationError(t, candidate.Validate(), test.want)
		})
	}
}

// TestNetworkRecordValidateKeepsResolverStageNonPublishable verifies resolver proof alone cannot expose listeners or endpoints.
func TestNetworkRecordValidateKeepsResolverStageNonPublishable(t *testing.T) {
	record := recordTestNetworkRecord()
	record.Stage = NetworkStageResolver
	record.Reservations.Listeners = SharedListenerReservations{}
	record.Reservations.Endpoints = []EndpointReservation{}
	if err := record.Validate(); err != nil {
		t.Fatalf("resolver NetworkRecord.Validate() error = %v", err)
	}

	record.Reservations.Listeners = recordTestListeners()
	assertRecordValidationError(t, record.Validate(), "resolver-stage network must not contain listener reservations")
	record.Reservations.Listeners = SharedListenerReservations{}
	record.Reservations.Endpoints = []EndpointReservation{recordTestHTTPEndpoint("alpha.test", "project-alpha", "web")}
	assertRecordValidationError(t, record.Validate(), "resolver-stage network must not contain endpoint reservations")
}

// TestListenerModeValidate covers the complete durable listener-mode vocabulary.
func TestListenerModeValidate(t *testing.T) {
	tests := []struct {
		name    string
		mode    ListenerMode
		wantErr bool
	}{
		{name: "direct", mode: ListenerModeDirect},
		{name: "redirect", mode: ListenerModeRedirect},
		{name: "empty", mode: "", wantErr: true},
		{name: "unsupported", mode: "proxy", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.mode.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("ListenerMode.Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

// TestEndpointProtocolValidate covers the complete payload-free endpoint vocabulary.
func TestEndpointProtocolValidate(t *testing.T) {
	tests := []struct {
		name     string
		protocol EndpointProtocol
		wantErr  bool
	}{
		{name: "HTTP", protocol: EndpointProtocolHTTP},
		{name: "TCP", protocol: EndpointProtocolTCP},
		{name: "empty", protocol: "", wantErr: true},
		{name: "UDP", protocol: "udp", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.protocol.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("EndpointProtocol.Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

// TestListenerReservationValidate accepts direct and redirected ownership proofs.
func TestListenerReservationValidate(t *testing.T) {
	direct := recordTestListener(ListenerModeDirect, "127.0.0.1:443", "127.0.0.1:443")
	redirect := recordTestListener(ListenerModeRedirect, "127.0.0.1:443", "127.0.0.1:18443")
	for name, reservation := range map[string]ListenerReservation{"direct": direct, "redirect": redirect} {
		t.Run(name, func(t *testing.T) {
			if err := reservation.Validate(); err != nil {
				t.Fatalf("ListenerReservation.Validate() error = %v", err)
			}
		})
	}
}

// TestListenerReservationValidateRejectsInvalidState covers socket, mode, generation, and evidence boundaries.
func TestListenerReservationValidateRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ListenerReservation)
		want   string
	}{
		{name: "unsupported mode", mutate: func(value *ListenerReservation) { value.Mode = "proxy" }, want: "unsupported"},
		{name: "invalid advertised socket", mutate: func(value *ListenerReservation) { value.Advertised = netip.AddrPort{} }, want: "advertised listener"},
		{name: "zero advertised port", mutate: func(value *ListenerReservation) { value.Advertised = netip.MustParseAddrPort("127.0.0.1:0") }, want: "nonzero port"},
		{name: "non-loopback advertised address", mutate: func(value *ListenerReservation) { value.Advertised = netip.MustParseAddrPort("192.0.2.1:443") }, want: "IPv4 loopback"},
		{name: "IPv6 advertised address", mutate: func(value *ListenerReservation) { value.Advertised = netip.MustParseAddrPort("[::1]:443") }, want: "IPv4 loopback"},
		{name: "mapped advertised address", mutate: func(value *ListenerReservation) {
			value.Advertised = netip.AddrPortFrom(netip.MustParseAddr("::ffff:127.0.0.1"), 443)
		}, want: "canonical IPv4"},
		{name: "invalid bind socket", mutate: func(value *ListenerReservation) { value.Bind = netip.AddrPort{} }, want: "bind listener"},
		{name: "different addresses", mutate: func(value *ListenerReservation) { value.Bind = netip.MustParseAddrPort("127.0.0.2:443") }, want: "addresses must match"},
		{name: "direct socket differs", mutate: func(value *ListenerReservation) { value.Bind = netip.MustParseAddrPort("127.0.0.1:18443") }, want: "advertise its bind socket"},
		{name: "redirect socket identical", mutate: func(value *ListenerReservation) {
			value.Mode = ListenerModeRedirect
		}, want: "distinct bind port"},
		{name: "zero generation", mutate: func(value *ListenerReservation) { value.Generation = 0 }, want: "generation must be positive"},
		{name: "generation overflow", mutate: func(value *ListenerReservation) { value.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "zero verification time", mutate: func(value *ListenerReservation) { value.VerifiedAt = time.Time{} }, want: "must not be zero"},
		{name: "non-UTC verification time", mutate: func(value *ListenerReservation) {
			value.VerifiedAt = value.VerifiedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservation := recordTestListener(ListenerModeDirect, "127.0.0.1:443", "127.0.0.1:443")
			test.mutate(&reservation)
			assertRecordValidationError(t, reservation.Validate(), test.want)
		})
	}
}

// TestSharedListenerReservationsValidate accepts direct and redirected socket plans.
func TestSharedListenerReservationsValidate(t *testing.T) {
	fixtures := map[string]SharedListenerReservations{
		"direct":   recordTestListeners(),
		"redirect": recordTestRedirectedListeners(),
	}
	for name, reservations := range fixtures {
		t.Run(name, func(t *testing.T) {
			if err := reservations.Validate(); err != nil {
				t.Fatalf("SharedListenerReservations.Validate() error = %v", err)
			}
		})
	}
}

// TestSharedListenerReservationsValidateRejectsInvalidState covers ingress pairing and cross-owner collisions.
func TestSharedListenerReservationsValidateRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name   string
		base   func() SharedListenerReservations
		mutate func(*SharedListenerReservations)
		want   string
	}{
		{name: "invalid DNS reservation", base: recordTestListeners, mutate: func(value *SharedListenerReservations) { value.DNS.Generation = 0 }, want: "DNS listener"},
		{name: "different ingress addresses", base: recordTestListeners, mutate: func(value *SharedListenerReservations) {
			value.HTTPS = recordTestListener(ListenerModeDirect, "127.0.0.2:443", "127.0.0.2:443")
		}, want: "share one ingress address"},
		{name: "wrong HTTP port", base: recordTestListeners, mutate: func(value *SharedListenerReservations) {
			value.HTTP = recordTestListener(ListenerModeDirect, "127.0.0.1:8080", "127.0.0.1:8080")
		}, want: "advertise port 80"},
		{name: "wrong HTTPS port", base: recordTestListeners, mutate: func(value *SharedListenerReservations) {
			value.HTTPS = recordTestListener(ListenerModeDirect, "127.0.0.1:8443", "127.0.0.1:8443")
		}, want: "advertise port 443"},
		{name: "advertised collision", base: recordTestListeners, mutate: func(value *SharedListenerReservations) {
			value.DNS = recordTestListener(ListenerModeDirect, "127.0.0.1:80", "127.0.0.1:80")
		}, want: "collides"},
		{name: "bind to advertised collision", base: recordTestRedirectedListeners, mutate: func(value *SharedListenerReservations) {
			value.DNS.Bind = value.HTTP.Advertised
		}, want: "collides"},
		{name: "bind collision", base: recordTestRedirectedListeners, mutate: func(value *SharedListenerReservations) {
			value.DNS.Bind = value.HTTP.Bind
		}, want: "collides"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservations := test.base()
			test.mutate(&reservations)
			assertRecordValidationError(t, reservations.Validate(), test.want)
		})
	}
}

// TestEndpointReservationKeyValidate covers the composite endpoint identity grammar and bounds.
func TestEndpointReservationKeyValidate(t *testing.T) {
	valid := []EndpointReservationKey{
		{ProjectID: "project-alpha", EndpointID: "api"},
		{ProjectID: "project-alpha", EndpointID: "API.v1_internal:443-native"},
	}
	for _, key := range valid {
		if err := key.Validate(); err != nil {
			t.Errorf("EndpointReservationKey.Validate(%#v) error = %v", key, err)
		}
	}

	tests := []struct {
		name string
		key  EndpointReservationKey
		want string
	}{
		{name: "empty project", key: EndpointReservationKey{EndpointID: "api"}, want: "project ID"},
		{name: "empty endpoint", key: EndpointReservationKey{ProjectID: "project-alpha"}, want: "ID is required"},
		{name: "oversized endpoint", key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: strings.Repeat("a", maximumNetworkEndpointIDLength+1)}, want: "exceeds"},
		{name: "surrounding whitespace", key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: " api"}, want: "surrounding whitespace"},
		{name: "embedded whitespace", key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: "api v1"}, want: "unsupported character"},
		{name: "slash", key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: "api/v1"}, want: "unsupported character"},
		{name: "non-ASCII", key: EndpointReservationKey{ProjectID: "project-alpha", EndpointID: "café"}, want: "unsupported character"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertRecordValidationError(t, test.key.Validate(), test.want)
		})
	}
}

// TestEndpointReservationValidate accepts HTTP and identity-backed TCP reservations.
func TestEndpointReservationValidate(t *testing.T) {
	for name, reservation := range map[string]EndpointReservation{
		"HTTP": recordTestHTTPEndpoint("alpha.test", "project-alpha", "web"),
		"TCP":  recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := reservation.Validate(); err != nil {
				t.Fatalf("EndpointReservation.Validate() error = %v", err)
			}
		})
	}
}

// TestEndpointReservationValidateRejectsInvalidState covers identity, DNS, socket, and generation boundaries.
func TestEndpointReservationValidateRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name   string
		base   func() EndpointReservation
		mutate func(*EndpointReservation)
		want   string
	}{
		{name: "invalid key", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Key.EndpointID = "" }, want: "ID is required"},
		{name: "invalid protocol", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Protocol = "udp" }, want: "unsupported"},
		{name: "invalid public socket", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Public = netip.AddrPort{} }, want: "public endpoint"},
		{name: "zero generation", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Generation = 0 }, want: "generation must be positive"},
		{name: "generation overflow", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "empty host", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Host = "" }, want: "name is required"},
		{name: "uppercase host", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Host = "Alpha.test" }, want: "lowercase"},
		{name: "foreign host", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Host = "alpha.example" }, want: "outside the .test zone"},
		{name: "oversized host label", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Host = strings.Repeat("a", 64) + ".test" }, want: "between 1 and 63"},
		{name: "HTTP identity", base: recordTestHTTPFixture, mutate: func(value *EndpointReservation) { value.Identity = recordTestLeaseKeyPointer("project-alpha", "") }, want: "must not reference"},
		{name: "TCP missing identity", base: recordTestTCPFixture, mutate: func(value *EndpointReservation) { value.Identity = nil }, want: "must reference"},
		{name: "TCP invalid identity", base: recordTestTCPFixture, mutate: func(value *EndpointReservation) { value.Identity = recordTestLeaseKeyPointer("", "") }, want: "project ID"},
		{name: "TCP identity project mismatch", base: recordTestTCPFixture, mutate: func(value *EndpointReservation) {
			value.Identity = recordTestLeaseKeyPointer("project-beta", "")
		}, want: "belongs to project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservation := test.base()
			test.mutate(&reservation)
			assertRecordValidationError(t, reservation.Validate(), test.want)
		})
	}
}

// TestDataPlaneReservationsValidate accepts initialized empty and populated projections.
func TestDataPlaneReservationsValidate(t *testing.T) {
	fixtures := map[string]DataPlaneReservations{
		"empty": {
			Listeners:            recordTestListeners(),
			Endpoints:            []EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
		"populated": recordTestDataPlane(),
	}
	for name, reservations := range fixtures {
		t.Run(name, func(t *testing.T) {
			if err := reservations.Validate(); err != nil {
				t.Fatalf("DataPlaneReservations.Validate() error = %v", err)
			}
		})
	}
}

// TestDataPlaneReservationsValidateRejectsInvalidState covers initialization, canonical ordering, suppression, and public authority collisions.
func TestDataPlaneReservationsValidateRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DataPlaneReservations)
		want   string
	}{
		{name: "nil endpoints", mutate: func(value *DataPlaneReservations) { value.Endpoints = nil }, want: "endpoint reservations must be initialized"},
		{name: "nil suppressed projects", mutate: func(value *DataPlaneReservations) { value.SuppressedProjectIDs = nil }, want: "suppressed network projects must be initialized"},
		{name: "invalid listeners", mutate: func(value *DataPlaneReservations) { value.Listeners.DNS.Generation = 0 }, want: "DNS listener"},
		{name: "invalid suppressed project", mutate: func(value *DataPlaneReservations) { value.SuppressedProjectIDs = []domain.ProjectID{""} }, want: "project ID"},
		{name: "unordered suppressed projects", mutate: func(value *DataPlaneReservations) {
			value.SuppressedProjectIDs = []domain.ProjectID{"project-zulu", "project-alpha"}
		}, want: "unique and ordered"},
		{name: "duplicate suppressed project", mutate: func(value *DataPlaneReservations) {
			value.SuppressedProjectIDs = []domain.ProjectID{"project-alpha", "project-alpha"}
		}, want: "unique and ordered"},
		{name: "invalid endpoint", mutate: func(value *DataPlaneReservations) { value.Endpoints[0].Generation = 0 }, want: "reservation 0"},
		{name: "unordered endpoints", mutate: func(value *DataPlaneReservations) {
			value.Endpoints[0], value.Endpoints[1] = value.Endpoints[1], value.Endpoints[0]
		}, want: "unique and ordered"},
		{name: "identical endpoint", mutate: func(value *DataPlaneReservations) {
			value.Endpoints = append(value.Endpoints, value.Endpoints[1])
		}, want: "unique and ordered"},
		{name: "duplicate endpoint key", mutate: func(value *DataPlaneReservations) {
			duplicate := recordTestTCPEndpoint("redis.alpha.test", "project-alpha", "web", "127.77.0.11:6379")
			value.Endpoints = []EndpointReservation{value.Endpoints[0], value.Endpoints[1], duplicate}
		}, want: "duplicate network endpoint key"},
		{name: "duplicate endpoint host", mutate: func(value *DataPlaneReservations) {
			duplicate := recordTestTCPEndpoint("mysql.alpha.test", "project-beta", "mysql", "127.77.0.11:3306")
			value.Endpoints = []EndpointReservation{value.Endpoints[0], value.Endpoints[1], duplicate}
		}, want: "duplicate network endpoint host"},
		{name: "suppressed endpoint", mutate: func(value *DataPlaneReservations) {
			value.SuppressedProjectIDs = []domain.ProjectID{"project-alpha"}
		}, want: "belongs to suppressed project"},
		{name: "HTTP wrong shared socket", mutate: func(value *DataPlaneReservations) {
			value.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.1:80")
		}, want: "advertised HTTPS socket"},
		{name: "duplicate TCP socket", mutate: func(value *DataPlaneReservations) {
			duplicate := recordTestTCPEndpoint("redis.beta.test", "project-beta", "redis", "127.77.0.10:3306")
			value.Endpoints = []EndpointReservation{value.Endpoints[0], value.Endpoints[1], duplicate}
		}, want: "duplicate native network socket"},
		{name: "TCP advertised listener collision", mutate: func(value *DataPlaneReservations) {
			value.Endpoints[1] = recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.0.0.1:80")
		}, want: "collides with HTTP listener"},
		{name: "TCP listener bind collision", mutate: func(value *DataPlaneReservations) {
			value.Listeners = recordTestRedirectedListeners()
			value.Endpoints[1] = recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.0.0.1:18443")
		}, want: "collides with HTTPS listener"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservations := recordTestDataPlane()
			test.mutate(&reservations)
			assertRecordValidationError(t, reservations.Validate(), test.want)
		})
	}
}

// TestNetworkRecordValidate accepts a canonical durable aggregate with independent causal generations.
func TestNetworkRecordValidate(t *testing.T) {
	record := recordTestNetworkRecord()
	if err := record.Validate(); err != nil {
		t.Fatalf("NetworkRecord.Validate() error = %v", err)
	}
	if record.Ownership.Generation == record.Leases[0].Ownership.Generation || record.Ownership.Generation == record.Reservations.Endpoints[0].Generation {
		t.Fatal("fixture must prove child generations remain independent from root ownership")
	}
}

// TestNetworkRecordValidateRejectsRootState covers revision, timestamps, ownership, pool, slice, and shared-address boundaries.
func TestNetworkRecordValidateRejectsRootState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NetworkRecord)
		want   string
	}{
		{name: "zero revision", mutate: func(value *NetworkRecord) { value.Revision = 0 }, want: "revision must be positive"},
		{name: "revision overflow", mutate: func(value *NetworkRecord) { value.Revision = domain.MaximumSequence + 1 }, want: "cross-client ordering range"},
		{name: "zero creation time", mutate: func(value *NetworkRecord) { value.CreatedAt = time.Time{} }, want: "creation time"},
		{name: "non-UTC creation time", mutate: func(value *NetworkRecord) {
			value.CreatedAt = value.CreatedAt.In(time.FixedZone("offset", -3600))
		}, want: "use UTC"},
		{name: "zero update time", mutate: func(value *NetworkRecord) { value.UpdatedAt = time.Time{} }, want: "update time"},
		{name: "update before creation", mutate: func(value *NetworkRecord) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) }, want: "must not precede"},
		{name: "invalid ownership", mutate: func(value *NetworkRecord) { value.Ownership.InstallationID = "-invalid" }, want: "installation ID"},
		{name: "ownership generation overflow", mutate: func(value *NetworkRecord) { value.Ownership.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "invalid pool", mutate: func(value *NetworkRecord) { value.Pool = identity.Pool{} }, want: "prefix is invalid"},
		{name: "nil leases", mutate: func(value *NetworkRecord) { value.Leases = nil }, want: "leases must be initialized"},
		{name: "nil quarantines", mutate: func(value *NetworkRecord) { value.Quarantines = nil }, want: "quarantines must be initialized"},
		{name: "invalid reservations", mutate: func(value *NetworkRecord) { value.Reservations.Endpoints = nil }, want: "endpoint reservations must be initialized"},
		{name: "shared listener in pool", mutate: func(value *NetworkRecord) {
			value.Pool = recordTestPool("127.0.0.0/8", "127.0.0.1", "127.77.0.10", "127.77.0.11", "127.77.0.12")
		}, want: "also a project pool candidate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordTestNetworkRecord()
			test.mutate(&record)
			assertRecordValidationError(t, record.Validate(), test.want)
		})
	}
}

// TestNetworkRecordValidateRejectsLeaseState covers canonical identity ownership, ordering, uniqueness, and primary requirements.
func TestNetworkRecordValidateRejectsLeaseState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NetworkRecord)
		want   string
	}{
		{name: "invalid lease", mutate: func(value *NetworkRecord) { value.Leases[0].Address = netip.MustParseAddr("192.0.2.1") }, want: "not IPv4 loopback"},
		{name: "mapped lease address", mutate: func(value *NetworkRecord) { value.Leases[0].Address = netip.MustParseAddr("::ffff:127.77.0.10") }, want: "canonical IPv4"},
		{name: "lease generation overflow", mutate: func(value *NetworkRecord) { value.Leases[0].Ownership.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "different installation", mutate: func(value *NetworkRecord) { value.Leases[0].Ownership.InstallationID = "other-installation" }, want: "belongs to installation"},
		{name: "lease outside pool", mutate: func(value *NetworkRecord) { value.Leases[0].Address = netip.MustParseAddr("127.78.0.10") }, want: "not a pool candidate"},
		{name: "unordered leases", mutate: func(value *NetworkRecord) {
			value.Leases = []identity.Lease{
				recordTestLease("project-beta", "", "127.77.0.11", 3),
				recordTestLease("project-alpha", "", "127.77.0.10", 3),
			}
		}, want: "unique and ordered"},
		{name: "duplicate lease key", mutate: func(value *NetworkRecord) {
			value.Leases = []identity.Lease{
				recordTestLease("project-alpha", "", "127.77.0.10", 3),
				recordTestLease("project-alpha", "", "127.77.0.11", 3),
			}
		}, want: "unique and ordered"},
		{name: "duplicate lease address", mutate: func(value *NetworkRecord) {
			value.Leases = []identity.Lease{
				recordTestLease("project-alpha", "", "127.77.0.10", 3),
				recordTestLease("project-beta", "", "127.77.0.10", 3),
			}
		}, want: "duplicate network lease address"},
		{name: "secondary without primary", mutate: func(value *NetworkRecord) {
			value.Leases = []identity.Lease{recordTestLease("project-alpha", "metrics", "127.77.0.11", 3)}
		}, want: "requires a primary lease"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordTestNetworkRecord()
			test.mutate(&record)
			assertRecordValidationError(t, record.Validate(), test.want)
		})
	}
}

// TestNetworkRecordValidateRejectsQuarantineState covers durable reason bounds, address canonicalization, ordering, and exclusivity.
func TestNetworkRecordValidateRejectsQuarantineState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NetworkRecord)
		want   string
	}{
		{name: "outside pool", mutate: func(value *NetworkRecord) { value.Quarantines[0].Address = netip.MustParseAddr("127.78.0.12") }, want: "not a pool candidate"},
		{name: "mapped address", mutate: func(value *NetworkRecord) { value.Quarantines[0].Address = netip.MustParseAddr("::ffff:127.77.0.12") }, want: "canonical IPv4"},
		{name: "empty reason", mutate: func(value *NetworkRecord) { value.Quarantines[0].Reason = " \n " }, want: "reason is required"},
		{name: "padded reason", mutate: func(value *NetworkRecord) { value.Quarantines[0].Reason = " retained " }, want: "surrounding whitespace"},
		{name: "oversized reason", mutate: func(value *NetworkRecord) {
			value.Quarantines[0].Reason = strings.Repeat("x", maximumNetworkQuarantineReasonLength+1)
		}, want: "exceeds"},
		{name: "unordered quarantines", mutate: func(value *NetworkRecord) {
			value.Quarantines = []identity.Quarantine{
				{Address: netip.MustParseAddr("127.77.0.12"), Reason: "released"},
				{Address: netip.MustParseAddr("127.77.0.11"), Reason: "released"},
			}
		}, want: "unique and ordered"},
		{name: "duplicate quarantine", mutate: func(value *NetworkRecord) {
			value.Quarantines = append(value.Quarantines, value.Quarantines[0])
		}, want: "unique and ordered"},
		{name: "leased and quarantined", mutate: func(value *NetworkRecord) {
			value.Quarantines[0].Address = netip.MustParseAddr("127.77.0.10")
		}, want: "both leased and quarantined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordTestNetworkRecord()
			test.mutate(&record)
			assertRecordValidationError(t, record.Validate(), test.want)
		})
	}
}

// TestNetworkRecordValidateRejectsEndpointLeaseMismatch covers project-primary and exact TCP identity joins.
func TestNetworkRecordValidateRejectsEndpointLeaseMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NetworkRecord)
		want   string
	}{
		{name: "endpoint without project primary", mutate: func(value *NetworkRecord) {
			value.Leases = []identity.Lease{recordTestLease("project-beta", "", "127.77.0.11", 3)}
			value.Quarantines = []identity.Quarantine{{Address: netip.MustParseAddr("127.77.0.12"), Reason: "released"}}
		}, want: "requires a primary lease"},
		{name: "unknown TCP lease", mutate: func(value *NetworkRecord) {
			value.Reservations.Endpoints[1].Identity = recordTestLeaseKeyPointer("project-alpha", "mysql")
		}, want: "unknown network lease"},
		{name: "TCP address differs from lease", mutate: func(value *NetworkRecord) {
			value.Reservations.Endpoints[1].Public = netip.MustParseAddrPort("127.77.0.11:3306")
		}, want: "does not match lease address"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordTestNetworkRecord()
			test.mutate(&record)
			assertRecordValidationError(t, record.Validate(), test.want)
		})
	}
}

// TestNetworkRecordCanonicalOrderingHelpers verifies all durable projections use deterministic order without mutating their inputs.
func TestNetworkRecordCanonicalOrderingHelpers(t *testing.T) {
	leases := []identity.Lease{
		recordTestLease("project-beta", "", "127.77.0.12", 3),
		recordTestLease("project-alpha", "metrics", "127.77.0.11", 3),
		recordTestLease("project-alpha", "", "127.77.0.10", 3),
	}
	orderedLeases := canonicalNetworkLeases(leases)
	wantLeaseKeys := []identity.LeaseKey{
		{ProjectID: "project-alpha"},
		{ProjectID: "project-alpha", SecondaryID: "metrics"},
		{ProjectID: "project-beta"},
	}
	gotLeaseKeys := make([]identity.LeaseKey, 0, len(orderedLeases))
	for _, lease := range orderedLeases {
		gotLeaseKeys = append(gotLeaseKeys, lease.Key)
	}
	if !reflect.DeepEqual(gotLeaseKeys, wantLeaseKeys) {
		t.Errorf("canonicalNetworkLeases() keys = %#v, want %#v", gotLeaseKeys, wantLeaseKeys)
	}
	if leases[0].Key.ProjectID != "project-beta" {
		t.Fatal("canonicalNetworkLeases() mutated its input")
	}
	if !networkLeaseLess(orderedLeases[0], orderedLeases[1]) || networkLeaseLess(orderedLeases[1], orderedLeases[0]) {
		t.Fatal("networkLeaseLess() does not preserve primary-before-secondary ordering")
	}
	duplicateLeases := canonicalNetworkLeases([]identity.Lease{leases[0], leases[0]})
	if len(duplicateLeases) != 2 || duplicateLeases[0] != duplicateLeases[1] {
		t.Fatalf("canonicalNetworkLeases() duplicate result = %#v", duplicateLeases)
	}

	quarantines := []identity.Quarantine{
		{Address: netip.MustParseAddr("127.77.0.12"), Reason: "second"},
		{Address: netip.MustParseAddr("127.77.0.11"), Reason: "first"},
	}
	orderedQuarantines := canonicalNetworkQuarantines(quarantines)
	if got := []netip.Addr{orderedQuarantines[0].Address, orderedQuarantines[1].Address}; !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("127.77.0.11"), netip.MustParseAddr("127.77.0.12")}) {
		t.Errorf("canonicalNetworkQuarantines() addresses = %v", got)
	}
	if quarantines[0].Address != netip.MustParseAddr("127.77.0.12") {
		t.Fatal("canonicalNetworkQuarantines() mutated its input")
	}

	endpoints := []EndpointReservation{
		recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306"),
		recordTestHTTPEndpoint("alpha.test", "project-alpha", "web"),
	}
	orderedEndpoints := canonicalEndpointReservations(endpoints)
	if got := []string{orderedEndpoints[0].Host, orderedEndpoints[1].Host}; !reflect.DeepEqual(got, []string{"alpha.test", "mysql.alpha.test"}) {
		t.Errorf("canonicalEndpointReservations() hosts = %v", got)
	}
	if endpoints[0].Host != "mysql.alpha.test" {
		t.Fatal("canonicalEndpointReservations() mutated its input slice")
	}
	if !endpointReservationLess(orderedEndpoints[0], orderedEndpoints[1]) || endpointReservationLess(orderedEndpoints[1], orderedEndpoints[0]) {
		t.Fatal("endpointReservationLess() does not preserve host-first ordering")
	}
	duplicateEndpoints := canonicalEndpointReservations([]EndpointReservation{endpoints[0], endpoints[0]})
	if len(duplicateEndpoints) != 2 || duplicateEndpoints[0].Host != duplicateEndpoints[1].Host {
		t.Fatalf("canonicalEndpointReservations() duplicate result = %#v", duplicateEndpoints)
	}
}

// TestCanonicalEndpointReservationsDeepCopiesIdentity prevents callers from sharing mutable TCP join keys.
func TestCanonicalEndpointReservationsDeepCopiesIdentity(t *testing.T) {
	input := []EndpointReservation{recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306")}
	result := canonicalEndpointReservations(input)
	if result[0].Identity == input[0].Identity {
		t.Fatal("canonicalEndpointReservations() retained the input identity pointer")
	}
	input[0].Identity.SecondaryID = "changed-input"
	if result[0].Identity.SecondaryID != "" {
		t.Fatalf("result identity changed with input: %#v", result[0].Identity)
	}
	result[0].Identity.ProjectID = "changed-result"
	if input[0].Identity.ProjectID != "project-alpha" {
		t.Fatalf("input identity changed with result: %#v", input[0].Identity)
	}
}

// TestSharedSocketOwnerMatchesAdvertisedAndBindSockets verifies collision diagnostics cover both ownership surfaces.
func TestSharedSocketOwnerMatchesAdvertisedAndBindSockets(t *testing.T) {
	listeners := recordTestRedirectedListeners()
	tests := []struct {
		name     string
		socket   netip.AddrPort
		wantName string
		want     bool
	}{
		{name: "advertised", socket: listeners.HTTP.Advertised, wantName: "HTTP listener", want: true},
		{name: "bind", socket: listeners.HTTPS.Bind, wantName: "HTTPS listener", want: true},
		{name: "unowned", socket: netip.MustParseAddrPort("127.0.0.1:9999")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, found := sharedSocketOwner(listeners, test.socket)
			if name != test.wantName || found != test.want {
				t.Fatalf("sharedSocketOwner() = %q, %t, want %q, %t", name, found, test.wantName, test.want)
			}
		})
	}
}

// recordTestTime returns a stable UTC persistence timestamp.
func recordTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}

// recordTestOverflowUint returns the smallest unsigned value that cannot fit a generated model integer.
func recordTestOverflowUint() uint64 {
	return uint64(^uint(0)>>1) + 1
}

// recordTestListener builds a complete listener fixture from exact sockets.
func recordTestListener(mode ListenerMode, advertised string, bind string) ListenerReservation {
	return ListenerReservation{
		Mode:       mode,
		Advertised: netip.MustParseAddrPort(advertised),
		Bind:       netip.MustParseAddrPort(bind),
		Generation: 7,
		VerifiedAt: recordTestTime(),
	}
}

// recordTestListeners returns a collision-free direct listener plan.
func recordTestListeners() SharedListenerReservations {
	return SharedListenerReservations{
		DNS:   recordTestListener(ListenerModeDirect, "127.0.0.1:53", "127.0.0.1:53"),
		HTTP:  recordTestListener(ListenerModeDirect, "127.0.0.1:80", "127.0.0.1:80"),
		HTTPS: recordTestListener(ListenerModeDirect, "127.0.0.1:443", "127.0.0.1:443"),
	}
}

// recordTestRedirectedListeners returns a collision-free redirected listener plan.
func recordTestRedirectedListeners() SharedListenerReservations {
	return SharedListenerReservations{
		DNS:   recordTestListener(ListenerModeRedirect, "127.0.0.1:53", "127.0.0.1:1053"),
		HTTP:  recordTestListener(ListenerModeRedirect, "127.0.0.1:80", "127.0.0.1:18080"),
		HTTPS: recordTestListener(ListenerModeRedirect, "127.0.0.1:443", "127.0.0.1:18443"),
	}
}

// recordTestLeaseKeyPointer returns an independently addressable identity join key.
func recordTestLeaseKeyPointer(projectID domain.ProjectID, secondaryID string) *identity.LeaseKey {
	return &identity.LeaseKey{ProjectID: projectID, SecondaryID: secondaryID}
}

// recordTestHTTPEndpoint builds a reservation on the shared HTTPS listener.
func recordTestHTTPEndpoint(host string, projectID domain.ProjectID, endpointID string) EndpointReservation {
	return EndpointReservation{
		Key:        EndpointReservationKey{ProjectID: projectID, EndpointID: endpointID},
		Protocol:   EndpointProtocolHTTP,
		Host:       host,
		Public:     netip.MustParseAddrPort("127.0.0.1:443"),
		Generation: 11,
	}
}

// recordTestTCPEndpoint builds a reservation joined to the project's primary identity.
func recordTestTCPEndpoint(host string, projectID domain.ProjectID, endpointID string, public string) EndpointReservation {
	return EndpointReservation{
		Key:        EndpointReservationKey{ProjectID: projectID, EndpointID: endpointID},
		Protocol:   EndpointProtocolTCP,
		Host:       host,
		Public:     netip.MustParseAddrPort(public),
		Identity:   recordTestLeaseKeyPointer(projectID, ""),
		Generation: 12,
	}
}

// recordTestHTTPFixture adapts the HTTP fixture for mutation tables.
func recordTestHTTPFixture() EndpointReservation {
	return recordTestHTTPEndpoint("alpha.test", "project-alpha", "web")
}

// recordTestTCPFixture adapts the TCP fixture for mutation tables.
func recordTestTCPFixture() EndpointReservation {
	return recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306")
}

// recordTestDataPlane returns an ordered HTTP and TCP projection.
func recordTestDataPlane() DataPlaneReservations {
	return DataPlaneReservations{
		Listeners: recordTestListeners(),
		Endpoints: []EndpointReservation{
			recordTestHTTPEndpoint("alpha.test", "project-alpha", "web"),
			recordTestTCPEndpoint("mysql.alpha.test", "project-alpha", "mysql", "127.77.0.10:3306"),
		},
		SuppressedProjectIDs: []domain.ProjectID{},
	}
}

// recordTestPool constructs a valid deterministic identity pool.
func recordTestPool(prefix string, candidates ...string) identity.Pool {
	addresses := make([]netip.Addr, 0, len(candidates))
	for _, candidate := range candidates {
		addresses = append(addresses, netip.MustParseAddr(candidate))
	}
	pool, err := identity.NewPool(netip.MustParsePrefix(prefix), addresses)
	if err != nil {
		panic(err)
	}
	return pool
}

// recordTestLease builds one canonical lease under the fixture installation.
func recordTestLease(projectID domain.ProjectID, secondaryID string, address string, generation uint64) identity.Lease {
	return identity.Lease{
		Key:       identity.LeaseKey{ProjectID: projectID, SecondaryID: secondaryID},
		Address:   netip.MustParseAddr(address),
		Ownership: identity.Ownership{InstallationID: "harbor-installation", Generation: generation},
	}
}

// recordTestNetworkRecord returns a complete canonical aggregate.
func recordTestNetworkRecord() NetworkRecord {
	return NetworkRecord{
		Stage:     NetworkStageFull,
		Revision:  21,
		CreatedAt: recordTestTime(),
		UpdatedAt: recordTestTime().Add(time.Minute),
		Ownership: identity.Ownership{InstallationID: "harbor-installation", Generation: 9},
		Pool:      recordTestPool("127.77.0.0/24", "127.77.0.10", "127.77.0.11", "127.77.0.12"),
		Leases: []identity.Lease{
			recordTestLease("project-alpha", "", "127.77.0.10", 3),
		},
		Quarantines: []identity.Quarantine{
			{Address: netip.MustParseAddr("127.77.0.12"), Reason: "verified release pending reuse"},
		},
		Reservations: recordTestDataPlane(),
	}
}

// assertRecordValidationError requires the expected validation boundary and diagnostic fragment.
func assertRecordValidationError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("validation error = nil, want containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("validation error = %q, want containing %q", err, want)
	}
}
