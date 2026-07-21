package harbordruntime

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// TestNormalizeManagedEndpointPublicationsMapsSelectedComposeEndpoints proves native service facts become exact fenced planner inputs.
func TestNormalizeManagedEndpointPublicationsMapsSelectedComposeEndpoints(t *testing.T) {
	input := managedPublicationObservationFixture()
	input.ServicePorts = append(input.ServicePorts, ManagedServicePortObservation{
		ServiceID: "unknown",
		Observation: projectprocess.ServicePortObservation{Supported: true, Available: true, Ports: []projectprocess.ServicePort{{
			Address: "127.0.0.1", Private: 1234, Public: 43210, Protocol: "tcp", Replica: 1,
		}}},
	})
	publications, err := NormalizeManagedEndpointPublications(input)
	if err != nil {
		t.Fatalf("NormalizeManagedEndpointPublications() error = %v", err)
	}
	want := []ManagedEndpointPublication{
		managedEndpointPublication(input.Fence, "service:endpoint.cache.primary.tcp", 1, netip.MustParseAddrPort("127.0.0.1:43107")),
		managedEndpointPublication(input.Fence, "service:endpoint.database.primary.tcp", 2, netip.MustParseAddrPort("127.0.0.1:43106")),
	}
	if !reflect.DeepEqual(publications, want) {
		t.Fatalf("publications = %#v, want %#v", publications, want)
	}
}

// TestValidateManagedEndpointPublicationsCompleteDistinguishesWithdrawalFromReadiness proves a barrier cannot acknowledge a partial host observation.
func TestValidateManagedEndpointPublicationsCompleteDistinguishesWithdrawalFromReadiness(t *testing.T) {
	input := managedPublicationObservationFixture()
	publications, err := NormalizeManagedEndpointPublications(input)
	if err != nil {
		t.Fatalf("NormalizeManagedEndpointPublications() error = %v", err)
	}
	if err := ValidateManagedEndpointPublicationsComplete(input, publications); err != nil {
		t.Fatalf("ValidateManagedEndpointPublicationsComplete(complete) error = %v", err)
	}
	input.ServicePorts = input.ServicePorts[:1]
	withdrawn, err := NormalizeManagedEndpointPublications(input)
	if err != nil {
		t.Fatalf("NormalizeManagedEndpointPublications(incomplete) error = %v", err)
	}
	if err := ValidateManagedEndpointPublicationsComplete(input, withdrawn); !errors.Is(err, ErrManagedPublicationsIncomplete) {
		t.Fatalf("ValidateManagedEndpointPublicationsComplete(incomplete) error = %v, want incomplete sentinel", err)
	}
	input.Requirements = nil
	input.ServicePorts = []ManagedServicePortObservation{}
	if err := ValidateManagedEndpointPublicationsComplete(input, []ManagedEndpointPublication{}); err != nil {
		t.Fatalf("ValidateManagedEndpointPublicationsComplete(no endpoints) error = %v", err)
	}
}

// TestNormalizeManagedEndpointPublicationsIgnoresUnselectedAndUnknownFacts keeps Harbor from inventing authority for non-host intent.
func TestNormalizeManagedEndpointPublicationsIgnoresUnselectedAndUnknownFacts(t *testing.T) {
	input := managedPublicationObservationFixture()
	input.Requirements = []goforj.ServiceRequirement{
		managedPublicationRequirement("requirement.database.primary", "mysql", "endpoint.database.primary.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerCompose),
		managedPublicationRequirement("requirement.cache.private", "redis", "endpoint.cache.private.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityPrivate, goforj.ServiceRequirementOwnerCompose),
		managedPublicationRequirement("requirement.mail.external", "mail", "endpoint.mail.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerExternal),
		managedPublicationRequirement("requirement.metrics.available", "metrics", "endpoint.metrics.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerAvailable),
		managedPublicationRequirement("requirement.dashboard.http", "dashboard", "endpoint.dashboard.http", goforj.ServiceEndpointProtocolHTTP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerCompose),
	}
	input.Reservations = []state.EndpointReservation{managedPublicationReservation("service:endpoint.database.primary.tcp", "mysql.orders.test", "127.77.0.10:3306", 2)}
	input.ServicePorts = []ManagedServicePortObservation{input.ServicePorts[0]}
	publications, err := NormalizeManagedEndpointPublications(input)
	if err != nil {
		t.Fatalf("NormalizeManagedEndpointPublications() error = %v", err)
	}
	want := []ManagedEndpointPublication{managedEndpointPublication(input.Fence, "service:endpoint.database.primary.tcp", 2, netip.MustParseAddrPort("127.0.0.1:43106"))}
	if !reflect.DeepEqual(publications, want) {
		t.Fatalf("publications = %#v, want %#v", publications, want)
	}
}

// TestNormalizeManagedEndpointPublicationsWithdrawsOnIncompleteObservation proves a partial read never preserves a stale publication.
func TestNormalizeManagedEndpointPublicationsWithdrawsOnIncompleteObservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ManagedPublicationObservationInput)
	}{
		{name: "missing service", mutate: func(input *ManagedPublicationObservationInput) { input.ServicePorts = input.ServicePorts[:1] }},
		{name: "unsupported service", mutate: func(input *ManagedPublicationObservationInput) { input.ServicePorts[0].Observation.Supported = false }},
		{name: "unavailable service", mutate: func(input *ManagedPublicationObservationInput) { input.ServicePorts[0].Observation.Available = false }},
		{name: "wrong native port", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports[0].Private = 3307
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := managedPublicationObservationFixture()
			test.mutate(&input)
			publications, err := NormalizeManagedEndpointPublications(input)
			if err != nil {
				t.Fatalf("NormalizeManagedEndpointPublications() error = %v", err)
			}
			if publications == nil || len(publications) != 0 {
				t.Fatalf("publications = %#v, want initialized empty replacement", publications)
			}
		})
	}
}

// TestNormalizeManagedEndpointPublicationsRejectsMalformedMatchingFacts keeps unsafe runtime evidence out of the registry.
func TestNormalizeManagedEndpointPublicationsRejectsMalformedMatchingFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ManagedPublicationObservationInput)
		want   string
	}{
		{name: "wrong protocol", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports[0].Protocol = "udp"
		}, want: "not TCP"},
		{name: "non-loopback address", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports[0].Address = "192.0.2.10"
		}, want: "not canonical IPv4 loopback"},
		{name: "IPv6 address", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports[0].Address = "::1"
		}, want: "not canonical IPv4 loopback"},
		{name: "zero public port", mutate: func(input *ManagedPublicationObservationInput) { input.ServicePorts[0].Observation.Ports[0].Public = 0 }, want: "no public host port"},
		{name: "low public port", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports[0].Public = 1023
		}, want: "high port"},
		{name: "duplicate replica", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts[0].Observation.Ports = append(input.ServicePorts[0].Observation.Ports, input.ServicePorts[0].Observation.Ports[0])
		}, want: "matching replicas"},
		{name: "nil port set", mutate: func(input *ManagedPublicationObservationInput) { input.ServicePorts[0].Observation.Ports = nil }, want: "nil port observation"},
		{name: "malformed peer despite incomplete set", mutate: func(input *ManagedPublicationObservationInput) {
			input.ServicePorts = input.ServicePorts[:1]
			input.ServicePorts[0].Observation.Ports[0].Address = "0.0.0.0"
		}, want: "not canonical IPv4 loopback"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := managedPublicationObservationFixture()
			test.mutate(&input)
			if _, err := NormalizeManagedEndpointPublications(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NormalizeManagedEndpointPublications() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNormalizeManagedEndpointPublicationsRejectsReservationDrift proves publication facts cannot claim another project or protocol.
func TestNormalizeManagedEndpointPublicationsRejectsReservationDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ManagedPublicationObservationInput)
		want   string
	}{
		{name: "missing reservation", mutate: func(input *ManagedPublicationObservationInput) { input.Reservations = input.Reservations[:1] }, want: "no exact durable reservation"},
		{name: "foreign reservation", mutate: func(input *ManagedPublicationObservationInput) {
			foreign := managedPublicationReservation("service:endpoint.database.primary.tcp", "mysql.other.test", "127.77.0.11:3306", 2)
			foreign.Key.ProjectID = "other"
			foreign.Identity.ProjectID = "other"
			input.Reservations = []state.EndpointReservation{foreign, input.Reservations[1]}
		}, want: "no exact durable reservation"},
		{name: "HTTP reservation", mutate: func(input *ManagedPublicationObservationInput) {
			input.Reservations[1].Protocol = state.EndpointProtocolHTTP
			input.Reservations[1].Identity = nil
		}, want: "not TCP"},
		{name: "duplicate reservation", mutate: func(input *ManagedPublicationObservationInput) {
			input.Reservations = append(input.Reservations, input.Reservations[0])
		}, want: "duplicated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := managedPublicationObservationFixture()
			test.mutate(&input)
			if _, err := NormalizeManagedEndpointPublications(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NormalizeManagedEndpointPublications() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestManagedPublicationRegistryPreservesGoodObservationAcrossRejectedRefresh proves callers can safely leave the prior replacement intact.
func TestManagedPublicationRegistryPreservesGoodObservationAcrossRejectedRefresh(t *testing.T) {
	input := managedPublicationObservationFixture()
	registry := NewManagedPublicationRegistry()
	fence, err := registry.Open(managedPublicationRegistrySession())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	input.Fence = fence
	good, err := NormalizeManagedEndpointPublications(input)
	if err != nil {
		t.Fatalf("Normalize(good) error = %v", err)
	}
	if err := registry.Replace(fence, good); err != nil {
		t.Fatalf("Replace(good) error = %v", err)
	}
	bad := input
	bad.ServicePorts = append([]ManagedServicePortObservation(nil), input.ServicePorts...)
	bad.ServicePorts[0].Observation.Ports = append([]projectprocess.ServicePort(nil), input.ServicePorts[0].Observation.Ports...)
	bad.ServicePorts[0].Observation.Ports[0].Address = "0.0.0.0"
	if _, err := NormalizeManagedEndpointPublications(bad); err == nil {
		t.Fatal("Normalize(bad) error = nil")
	}
	retained, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot(retained) error = %v", err)
	}
	if !reflect.DeepEqual(retained, good) {
		t.Fatalf("retained = %#v, want %#v", retained, good)
	}
	withdrawnInput := input
	withdrawnInput.ServicePorts = withdrawnInput.ServicePorts[:1]
	withdrawn, err := NormalizeManagedEndpointPublications(withdrawnInput)
	if err != nil {
		t.Fatalf("Normalize(withdrawn) error = %v", err)
	}
	if err := registry.Replace(fence, withdrawn); err != nil {
		t.Fatalf("Replace(withdrawn) error = %v", err)
	}
	final, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot(final) error = %v", err)
	}
	if final == nil || len(final) != 0 {
		t.Fatalf("final = %#v, want initialized empty replacement", final)
	}
}

// managedPublicationObservationFixture creates selected MySQL and Redis requirements with exact host reservations.
func managedPublicationObservationFixture() ManagedPublicationObservationInput {
	fence := managedPublicationTestFence()
	return ManagedPublicationObservationInput{
		Fence: fence,
		Requirements: []goforj.ServiceRequirement{
			managedPublicationRequirement("requirement.database.primary", "mysql", "endpoint.database.primary.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerCompose),
			managedPublicationRequirement("requirement.cache.primary", "redis", "endpoint.cache.primary.tcp", goforj.ServiceEndpointProtocolTCP, goforj.ServiceEndpointVisibilityHost, goforj.ServiceRequirementOwnerCompose),
		},
		ServicePorts: []ManagedServicePortObservation{
			{ServiceID: "mysql", Observation: projectprocess.ServicePortObservation{Supported: true, Available: true, Ports: []projectprocess.ServicePort{{Address: "127.0.0.1", Private: 3306, Public: 43106, Protocol: "tcp", Replica: 1}}}},
			{ServiceID: "redis", Observation: projectprocess.ServicePortObservation{Supported: true, Available: true, Ports: []projectprocess.ServicePort{{Address: "127.0.0.1", Private: 6379, Public: 43107, Protocol: "TCP", Replica: 1}}}},
		},
		Reservations: []state.EndpointReservation{
			managedPublicationReservation("service:endpoint.cache.primary.tcp", "redis.orders.test", "127.77.0.10:6379", 1),
			managedPublicationReservation("service:endpoint.database.primary.tcp", "mysql.orders.test", "127.77.0.10:3306", 2),
		},
	}
}

// managedPublicationRequirement creates one descriptor requirement with one endpoint for focused normalizer tests.
func managedPublicationRequirement(requirementID, serviceKey, endpointID string, protocol goforj.ServiceEndpointProtocol, visibility goforj.ServiceEndpointVisibility, owner goforj.ServiceRequirementOwner) goforj.ServiceRequirement {
	nativePort := 3306
	if strings.Contains(serviceKey, "redis") {
		nativePort = 6379
	}
	return goforj.ServiceRequirement{
		ID: requirementID, ServiceKey: serviceKey, Kind: "database", Driver: serviceKey, Owner: owner,
		Lifecycle: goforj.ServiceRequirementLifecycleProject, Consumers: []string{"app"},
		Endpoints: []goforj.ServiceEndpoint{{ID: endpointID, Protocol: protocol, NativePort: nativePort, Visibility: visibility}},
	}
}
