package helper

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/trust/localca"
)

// TestExpectedTrustObservationValidate covers the trust compare-and-swap digest boundary.
func TestExpectedTrustObservationValidate(t *testing.T) {
	for _, test := range []struct {
		name        string
		fingerprint string
		wantError   bool
	}{
		{name: "canonical", fingerprint: testFingerprint()},
		{name: "short", fingerprint: "bad", wantError: true},
		{name: "uppercase", fingerprint: strings.Repeat("A", fingerprintLength), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (ExpectedTrustObservation{Fingerprint: test.fingerprint}).Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("ExpectedTrustObservation.Validate() error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}

// TestTrustRootValidateKeepsPrivateMaterialOutsideTickets covers the bounded public CA wire shape.
func TestTrustRootValidateKeepsPrivateMaterialOutsideTickets(t *testing.T) {
	root := testHelperTrustRoot(t)
	if err := root.Validate(); err != nil {
		t.Fatalf("TrustRoot.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*TrustRoot)
	}{
		{name: "missing certificate", mutate: func(value *TrustRoot) { value.CertificatePEM = nil }},
		{name: "private key material", mutate: func(value *TrustRoot) { value.CertificatePEM = append(value.CertificatePEM, []byte("PRIVATE KEY")...) }},
		{name: "invalid fingerprint", mutate: func(value *TrustRoot) { value.Fingerprint = "bad" }},
		{name: "invalid validity", mutate: func(value *TrustRoot) { value.NotAfter = value.NotBefore }},
		{name: "non-UTC validity", mutate: func(value *TrustRoot) { value.NotAfter = value.NotAfter.In(time.FixedZone("offset", 3600)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := root
			candidate.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("TrustRoot.Validate() accepted invalid public CA material")
			}
		})
	}
}

// TestTicketValidateTrustAuthority covers both trust operations and rejects cross-domain authority fields.
func TestTicketValidateTrustAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{OperationEnsureTrust, OperationReleaseTrust} {
		t.Run(string(operation), func(t *testing.T) {
			ticket := validTestTrustTicket(t, now, operation)
			if err := ticket.Validate(now); err != nil {
				t.Fatalf("Ticket.Validate() valid trust ticket error = %v", err)
			}
			for _, test := range []struct {
				name   string
				mutate func(*Ticket)
			}{
				{name: "identity ownership", mutate: func(value *Ticket) {
					value.OwnershipSchemaVersion = identityOwnershipSchemaVersion
					value.NetworkPolicyFingerprint = ""
				}},
				{name: "missing policy", mutate: func(value *Ticket) { value.NetworkPolicy = nil }},
				{name: "policy mismatch", mutate: func(value *Ticket) { value.NetworkPolicyFingerprint = strings.Repeat("f", fingerprintLength) }},
				{name: "missing root", mutate: func(value *Ticket) { value.TrustRoot = nil }},
				{name: "root mismatch", mutate: func(value *Ticket) { value.TrustRoot.Fingerprint = strings.Repeat("f", fingerprintLength) }},
				{name: "missing observation", mutate: func(value *Ticket) { value.ExpectedTrustObservation = nil }},
				{name: "invalid observation", mutate: func(value *Ticket) { value.ExpectedTrustObservation.Fingerprint = "bad" }},
				{name: "loopback authority", mutate: func(value *Ticket) { value.ApprovedAddress = "127.77.0.10" }},
				{name: "resolver authority", mutate: func(value *Ticket) {
					value.ExpectedResolverObservation = &ExpectedResolverObservation{Fingerprint: testFingerprint()}
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					candidate := validTestTrustTicket(t, now, operation)
					test.mutate(&candidate)
					if err := candidate.Validate(now); err == nil {
						t.Fatal("Ticket.Validate() accepted invalid trust authority")
					}
				})
			}
		})
	}
}

// TestDispatcherDispatchTrustOperations verifies trust handlers receive only redeemed tickets and return correlated evidence.
func TestDispatcherDispatchTrustOperations(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{OperationEnsureTrust, OperationReleaseTrust} {
		t.Run(string(operation), func(t *testing.T) {
			ticket := validTestTrustTicket(t, now, operation)
			postcondition := TrustPostconditionExact
			if operation == OperationReleaseTrust {
				postcondition = TrustPostconditionOwnedAbsent
			}
			root := ticket.TrustRoot
			handler := &testTrustHandler{evidence: TrustMutationEvidence{
				Changed:                true,
				AuthorityFingerprint:   root.Fingerprint,
				Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
				ObservationFingerprint: strings.Repeat("d", fingerprintLength),
				Postcondition:          postcondition,
			}}
			reference := testTicketReference()
			dispatcher := NewDispatcherWithResolverAndTrust(
				newTestTicketRedeemer(reference, ticket),
				newTestClock(now),
				newTestReplayGuard(),
				UnavailableLoopbackIdentityHandler{},
				UnavailableResolverHandler{},
				handler,
			)

			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if !response.OK || response.Result == nil || response.Result.Operation != operation ||
				response.Result.TrustEvidence == nil || *response.Result.TrustEvidence != handler.evidence ||
				response.Result.Evidence != (MutationEvidence{}) || response.Result.PoolEvidence != nil || response.Result.ResolverEvidence != nil {
				t.Fatalf("Dispatch() response = %#v", response)
			}
			if handler.calls != 1 || handler.operation != operation {
				t.Fatalf("trust handler calls/operation = %d/%q", handler.calls, handler.operation)
			}
		})
	}
}

// TestDispatcherTrustEvidenceFailsClosed rejects unavailable handlers and authority mismatches.
func TestDispatcherTrustEvidenceFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestTrustTicket(t, now, OperationEnsureTrust)
	reference := testTicketReference()
	newDispatcher := func(handler TrustHandler) *Dispatcher {
		return NewDispatcherWithResolverAndTrust(
			newTestTicketRedeemer(reference, ticket),
			newTestClock(now),
			newTestReplayGuard(),
			UnavailableLoopbackIdentityHandler{},
			UnavailableResolverHandler{},
			handler,
		)
	}
	response, err := newDispatcher(UnavailableTrustHandler{}).Dispatch(context.Background(), validTestRequest(reference))
	if !errors.Is(err, ErrMutationUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeMutationUnavailable {
		t.Fatalf("unavailable trust Dispatch() = %#v, %v", response, err)
	}

	bad := &testTrustHandler{evidence: TrustMutationEvidence{
		AuthorityFingerprint:   strings.Repeat("f", fingerprintLength),
		Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
		ObservationFingerprint: testFingerprint(),
		Postcondition:          TrustPostconditionExact,
	}}
	response, err = newDispatcher(bad).Dispatch(context.Background(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
		t.Fatalf("mismatched trust Dispatch() = %#v, %v", response, err)
	}
}

// TestResponseForErrorRejectsSpoofedAdministratorTrustDiagnostics verifies structural lookalikes cannot bypass the concrete trust-error boundary.
func TestResponseForErrorRejectsSpoofedAdministratorTrustDiagnostics(t *testing.T) {
	diagnostic := testAdministratorTrustDiagnostic{stage: "set-root", status: -25299}
	response := responseForError(diagnostic)
	if response.Error == nil || response.Error.Code != ErrorCodeMutationFailed || response.Error.Message != "helper operation failed" {
		t.Fatalf("spoofed diagnostic response = %#v", response)
	}

	for _, err := range []error{
		errors.New("open /private/marker: certificate bytes"),
		testAdministratorTrustDiagnostic{stage: "forged-stage", status: -25299},
		testAdministratorTrustDiagnostic{stage: "set-root", status: 1 << 32},
	} {
		response = responseForError(err)
		if response.Error == nil || response.Error.Code != ErrorCodeMutationFailed || response.Error.Message != "helper operation failed" {
			t.Fatalf("unrecognized diagnostic response = %#v", response)
		}
	}
}

// TestAdministratorTrustDiagnosticMessageAllowsOnlyReviewedNativeValues verifies formatter behavior separately from the concrete trust error boundary.
func TestAdministratorTrustDiagnosticMessageAllowsOnlyReviewedNativeValues(t *testing.T) {
	message, ok := administratorTrustDiagnosticMessage("set-root", -25299)
	if !ok || message != "helper operation failed: administrator trust set-root OSStatus -25299" {
		t.Fatalf("administratorTrustDiagnosticMessage() = %q, %t", message, ok)
	}

	for _, diagnostic := range []struct {
		stage  string
		status int
	}{
		{stage: "forged-stage", status: -25299},
		{stage: "set-root", status: -(1 << 31) - 1},
		{stage: "set-root", status: 1 << 31},
	} {
		if message, ok := administratorTrustDiagnosticMessage(diagnostic.stage, diagnostic.status); ok {
			t.Fatalf("administratorTrustDiagnosticMessage(%q, %d) = %q, true", diagnostic.stage, diagnostic.status, message)
		}
	}
}

// testAdministratorTrustDiagnostic mimics the former structural diagnostic shape without being a trust error.
type testAdministratorTrustDiagnostic struct {
	stage  string
	status int
}

// Error keeps the test diagnostic distinct from the response text derived by dispatch.
func (diagnostic testAdministratorTrustDiagnostic) Error() string {
	return "native failure"
}

// AdministratorTrustDiagnostic supplies the structured test diagnostic without a native error string.
func (diagnostic testAdministratorTrustDiagnostic) AdministratorTrustDiagnostic() (string, int, bool) {
	return diagnostic.stage, diagnostic.status, true
}

// TestTrustResponseCodecRoundTrip pins strict trust evidence field names and postconditions.
func TestTrustResponseCodecRoundTrip(t *testing.T) {
	evidence := TrustMutationEvidence{
		Changed:                true,
		AuthorityFingerprint:   testFingerprint(),
		Mechanism:              networkpolicy.DarwinCurrentUserTrust,
		ObservationFingerprint: strings.Repeat("b", fingerprintLength),
		Postcondition:          TrustPostconditionPreexisting,
	}
	response := Response{
		Version: ProtocolVersion,
		OK:      true,
		Result:  &OperationResult{Operation: OperationEnsureTrust, TrustEvidence: &evidence},
	}
	var encoded bytes.Buffer
	if err := WriteResponse(&encoded, response); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}
	decoded, err := DecodeResponse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if decoded.Result == nil || decoded.Result.TrustEvidence == nil || *decoded.Result.TrustEvidence != evidence {
		t.Fatalf("DecodeResponse() = %#v", decoded)
	}
	for _, body := range []string{
		strings.Replace(encoded.String(), `"postcondition":"preexisting"`, `"postcondition":"owned_absent"`, 1),
		strings.Replace(encoded.String(), `"trust_evidence":`, `"trust_evidence":null,"trust_evidence":`, 1),
		strings.Replace(encoded.String(), `"mechanism":"darwin-current-user-trust-v1"`, `"mechanism":"unsupported"`, 1),
	} {
		if _, err := DecodeResponse(strings.NewReader(body)); err == nil {
			t.Fatal("DecodeResponse() accepted invalid trust evidence")
		}
	}
}

// testTrustHandler returns configured trust evidence without touching host state.
type testTrustHandler struct {
	evidence  TrustMutationEvidence
	err       error
	calls     int
	operation Operation
}

// EnsureTrust records the ensure dispatch and returns the configured outcome.
func (handler *testTrustHandler) EnsureTrust(context.Context, Ticket) (TrustMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationEnsureTrust
	return handler.evidence, handler.err
}

// ReleaseTrust records the release dispatch and returns the configured outcome.
func (handler *testTrustHandler) ReleaseTrust(context.Context, Ticket) (TrustMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationReleaseTrust
	return handler.evidence, handler.err
}

var _ TrustHandler = (*testTrustHandler)(nil)

// validTestTrustTicket returns one canonical policy-bound public-CA ticket.
func validTestTrustTicket(t *testing.T, now time.Time, operation Operation) Ticket {
	t.Helper()
	root := testHelperTrustRoot(t)
	localhost := netip.MustParseAddr("127.0.0.1")
	policy, err := networkpolicy.New(
		root.Fingerprint,
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(localhost, 25000), Bind: netip.AddrPortFrom(localhost, 25000)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(localhost, 80), Bind: netip.AddrPortFrom(localhost, 25001)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(localhost, 443), Bind: netip.AddrPortFrom(localhost, 25002)},
	)
	if err != nil {
		t.Fatalf("networkpolicy.New() fixture error = %v", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() fixture error = %v", err)
	}
	return Ticket{
		Version:                  ProtocolVersion,
		Operation:                operation,
		InstallationID:           "harbor-test-installation",
		RequesterIdentity:        "uid-1000",
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   networkPolicyOwnershipSchemaVersion,
		NetworkPolicyFingerprint: policyFingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		TrustRoot:                &root,
		ExpectedTrustObservation: &ExpectedTrustObservation{Fingerprint: testFingerprint()},
		Nonce:                    strings.Repeat("n", minimumNonceLength),
		ExpiresAt:                now.Add(time.Minute),
	}
}

// testHelperTrustRoot creates one deterministic public CA fixture without retaining private material.
func testHelperTrustRoot(t *testing.T) TrustRoot {
	t.Helper()
	clock := time.Date(2032, time.March, 4, 12, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{
		CAValidity:   24 * time.Hour,
		LeafValidity: time.Hour,
		Backdate:     time.Minute,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	return TrustRoot{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
}
