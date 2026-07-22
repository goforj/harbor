package reconcile

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestNetworkDataPlaneSetupRequestValidation covers every caller-controlled identity and evidence branch.
func TestNetworkDataPlaneSetupRequestValidation(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	start := NetworkDataPlaneSetupStartRequest{
		OperationID:       "operation-data-plane",
		IntentID:          "intent-data-plane",
		RequesterIdentity: "501",
	}
	if err := start.Validate(); err != nil {
		t.Fatalf("NetworkDataPlaneSetupStartRequest.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*NetworkDataPlaneSetupStartRequest)
		want   string
	}{
		{name: "operation", mutate: func(value *NetworkDataPlaneSetupStartRequest) { value.OperationID = " bad " }, want: "operation ID"},
		{name: "intent", mutate: func(value *NetworkDataPlaneSetupStartRequest) { value.IntentID = " bad " }, want: "intent ID"},
		{name: "requester", mutate: func(value *NetworkDataPlaneSetupStartRequest) { value.RequesterIdentity = "" }, want: "requester identity"},
	} {
		t.Run("start/"+test.name, func(t *testing.T) {
			candidate := start
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	prepareTrust := NetworkDataPlaneSetupPrepareTrustRequest{
		OperationID:               start.OperationID,
		ExpectedOperationRevision: 5,
		RequesterIdentity:         start.RequesterIdentity,
	}
	prepareLowPorts := NetworkDataPlaneSetupPrepareLowPortsRequest(prepareTrust)
	if err := prepareTrust.Validate(); err != nil {
		t.Fatalf("NetworkDataPlaneSetupPrepareTrustRequest.Validate() error = %v", err)
	}
	if err := prepareLowPorts.Validate(); err != nil {
		t.Fatalf("NetworkDataPlaneSetupPrepareLowPortsRequest.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*NetworkDataPlaneSetupPrepareTrustRequest)
		want   string
	}{
		{name: "operation", mutate: func(value *NetworkDataPlaneSetupPrepareTrustRequest) { value.OperationID = "" }, want: "operation ID"},
		{name: "revision", mutate: func(value *NetworkDataPlaneSetupPrepareTrustRequest) { value.ExpectedOperationRevision = 0 }, want: "revision"},
		{name: "requester", mutate: func(value *NetworkDataPlaneSetupPrepareTrustRequest) { value.RequesterIdentity = "" }, want: "requester identity"},
	} {
		t.Run("prepare/"+test.name, func(t *testing.T) {
			candidate := prepareTrust
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("trust Validate() error = %v, want containing %q", err, test.want)
			}
			lowPortCandidate := NetworkDataPlaneSetupPrepareLowPortsRequest(candidate)
			if err := lowPortCandidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("low-port Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	confirmTrust := NetworkDataPlaneSetupConfirmTrustRequest{
		OperationID:               start.OperationID,
		ExpectedOperationRevision: 5,
		RequesterIdentity:         start.RequesterIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   fingerprint,
			Mechanism:              networkpolicy.DarwinCurrentUserTrust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionExact,
		},
	}
	if err := confirmTrust.Validate(); err != nil {
		t.Fatalf("NetworkDataPlaneSetupConfirmTrustRequest.Validate() error = %v", err)
	}
	administrator := confirmTrust
	administrator.TrustEvidence.Mechanism = networkpolicy.DarwinAdministratorTrust
	if err := administrator.Validate(); err != nil {
		t.Fatalf("administrator NetworkDataPlaneSetupConfirmTrustRequest.Validate() error = %v", err)
	}
	preexisting := confirmTrust
	preexisting.TrustEvidence.Postcondition = helper.TrustPostconditionPreexisting
	if err := preexisting.Validate(); err != nil {
		t.Fatalf("preexisting NetworkDataPlaneSetupConfirmTrustRequest.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*NetworkDataPlaneSetupConfirmTrustRequest)
		want   string
	}{
		{name: "operation", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) { value.OperationID = "" }, want: "operation ID"},
		{name: "revision", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) { value.ExpectedOperationRevision = 0 }, want: "revision"},
		{name: "requester", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) { value.RequesterIdentity = "" }, want: "requester identity"},
		{name: "authority", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) {
			value.TrustEvidence.AuthorityFingerprint = strings.Repeat("A", 64)
		}, want: "authority fingerprint"},
		{name: "unknown mechanism", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) { value.TrustEvidence.Mechanism = "unsupported" }, want: "mechanism"},
		{name: "mixed mechanism", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) {
			value.TrustEvidence.Mechanism = networkpolicy.TrustMechanism(string(networkpolicy.DarwinAdministratorTrust) + "," + string(networkpolicy.DarwinCurrentUserTrust))
		}, want: "mechanism"},
		{name: "observation", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) {
			value.TrustEvidence.ObservationFingerprint = "short"
		}, want: "observation fingerprint"},
		{name: "postcondition", mutate: func(value *NetworkDataPlaneSetupConfirmTrustRequest) {
			value.TrustEvidence.Postcondition = helper.TrustPostconditionOwnedAbsent
		}, want: "exact or identical preexisting"},
	} {
		t.Run("confirm-trust/"+test.name, func(t *testing.T) {
			candidate := confirmTrust
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	confirmLowPorts := NetworkDataPlaneSetupConfirmLowPortsRequest{
		OperationID:               start.OperationID,
		ExpectedOperationRevision: 7,
		RequesterIdentity:         start.RequesterIdentity,
		LowPortEvidence: helper.LowPortMutationEvidence{
			PolicyFingerprint:      fingerprint,
			OwnershipFingerprint:   fingerprint,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.LowPortPostconditionExact,
		},
	}
	if err := confirmLowPorts.Validate(); err != nil {
		t.Fatalf("NetworkDataPlaneSetupConfirmLowPortsRequest.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*NetworkDataPlaneSetupConfirmLowPortsRequest)
		want   string
	}{
		{name: "operation", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) { value.OperationID = "" }, want: "operation ID"},
		{name: "revision", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) { value.ExpectedOperationRevision = 0 }, want: "revision"},
		{name: "requester", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) { value.RequesterIdentity = "" }, want: "requester identity"},
		{name: "policy", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) {
			value.LowPortEvidence.PolicyFingerprint = "short"
		}, want: "policy fingerprint"},
		{name: "ownership", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) {
			value.LowPortEvidence.OwnershipFingerprint = strings.Repeat("A", 64)
		}, want: "ownership fingerprint"},
		{name: "observation", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) {
			value.LowPortEvidence.ObservationFingerprint = "short"
		}, want: "observation fingerprint"},
		{name: "postcondition", mutate: func(value *NetworkDataPlaneSetupConfirmLowPortsRequest) {
			value.LowPortEvidence.Postcondition = helper.LowPortPostconditionOwnedAbsent
		}, want: "exact paired listener"},
	} {
		t.Run("confirm-low-port/"+test.name, func(t *testing.T) {
			candidate := confirmLowPorts
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkDataPlaneSetupRequestSurfacesStayNarrow catches accidental transport authority expansion.
func TestNetworkDataPlaneSetupRequestSurfacesStayNarrow(t *testing.T) {
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupStartRequest{}, []string{"OperationID", "IntentID", "RequesterIdentity"})
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupPrepareTrustRequest{}, []string{"OperationID", "ExpectedOperationRevision", "RequesterIdentity"})
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupConfirmTrustRequest{}, []string{"OperationID", "ExpectedOperationRevision", "RequesterIdentity", "TrustEvidence"})
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupPrepareLowPortsRequest{}, []string{"OperationID", "ExpectedOperationRevision", "RequesterIdentity"})
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupConfirmLowPortsRequest{}, []string{"OperationID", "ExpectedOperationRevision", "RequesterIdentity", "LowPortEvidence"})
	assertNetworkDataPlaneSetupFields(t, NetworkDataPlaneSetupResult{}, []string{"Operation", "Network"})
}

// assertNetworkDataPlaneSetupFields compares one exported request or result surface in declaration order.
func assertNetworkDataPlaneSetupFields(t *testing.T, value any, want []string) {
	t.Helper()
	typeOf := reflect.TypeOf(value)
	got := make([]string, typeOf.NumField())
	for index := range got {
		got[index] = typeOf.Field(index).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields = %v, want %v", typeOf.Name(), got, want)
	}
}
