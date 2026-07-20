//go:build darwin

package resolverhandler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestPrivilegedDarwinResolverHandlerLifecycle proves the helper path installs, verifies, and removes its fixed rule.
func TestPrivilegedDarwinResolverHandlerLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 and run as root to exercise the helper resolver lifecycle")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged Darwin resolver handler test requires root")
	}

	request, policy := resolverHandlerTestRequest(t)
	adapter := resolver.New()
	handler := newHandler(adapter, &testOwnershipUpgrader{})
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe resolver before helper ensure: %v", err)
	}
	assessment, err := before.Classify()
	if err != nil {
		t.Fatalf("classify resolver before helper ensure: %v", err)
	}
	if assessment.State != resolver.StateAbsent {
		t.Fatalf("resolver fixture begins in %q/%q state; refusing to overwrite it", assessment.State, assessment.Owned)
	}
	beforeFingerprint, err := before.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint resolver before helper ensure: %v", err)
	}

	cleanupNeeded := true
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		observation, observeErr := adapter.Observe(context.Background(), request)
		if observeErr != nil {
			t.Errorf("cleanup observe Darwin resolver: %v", observeErr)
			return
		}
		cleanupAssessment, classifyErr := observation.Classify()
		if classifyErr != nil {
			t.Errorf("cleanup classify Darwin resolver: %v", classifyErr)
			return
		}
		switch cleanupAssessment.Owned {
		case resolver.OwnedStateAbsent:
			return
		case resolver.OwnedStateExact, resolver.OwnedStateDrifted:
		case resolver.OwnedStateAmbiguous:
			t.Errorf("cleanup found ambiguous owned Darwin resolver state; refusing mutation")
			return
		default:
			t.Errorf("cleanup found unknown owned Darwin resolver state %q; refusing mutation", cleanupAssessment.Owned)
			return
		}
		observedFingerprint, fingerprintErr := observation.Fingerprint()
		if fingerprintErr != nil {
			t.Errorf("cleanup fingerprint Darwin resolver: %v", fingerprintErr)
			return
		}
		ticket := privilegedDarwinResolverTicket(t, request, policy, helper.OperationReleaseResolver, observedFingerprint, 'c')
		admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
		if _, releaseErr := handler.ReleaseResolver(context.Background(), ticket, admission); releaseErr != nil {
			t.Errorf("cleanup release Darwin resolver: %v", releaseErr)
		}
	})

	ensureTicket := privilegedDarwinResolverTicket(t, request, policy, helper.OperationEnsureResolver, beforeFingerprint, 'a')
	ensureResponse := dispatchPrivilegedResolverTicket(t, handler, ensureTicket, helper.TicketReference(strings.Repeat("a", 64)))
	if ensureResponse.Result == nil || ensureResponse.Result.ResolverEvidence == nil ||
		ensureResponse.Result.ResolverEvidence.Postcondition != helper.ResolverPostconditionExact {
		t.Fatalf("helper ensure response = %#v", ensureResponse)
	}

	installed, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("independently observe installed resolver: %v", err)
	}
	installedAssessment, err := installed.Classify()
	if err != nil || installedAssessment.State != resolver.StateExact {
		t.Fatalf("installed resolver assessment = %#v, %v", installedAssessment, err)
	}
	installedFingerprint, err := installed.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint installed resolver: %v", err)
	}

	releaseTicket := privilegedDarwinResolverTicket(t, request, policy, helper.OperationReleaseResolver, installedFingerprint, 'b')
	releaseResponse := dispatchPrivilegedResolverTicket(t, handler, releaseTicket, helper.TicketReference(strings.Repeat("b", 64)))
	if releaseResponse.Result == nil || releaseResponse.Result.ResolverEvidence == nil ||
		releaseResponse.Result.ResolverEvidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		t.Fatalf("helper release response = %#v", releaseResponse)
	}

	released, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("independently observe released resolver: %v", err)
	}
	releasedAssessment, err := released.Classify()
	if err != nil || releasedAssessment.State != resolver.StateAbsent || releasedAssessment.Owned != resolver.OwnedStateAbsent {
		t.Fatalf("released resolver assessment = %#v, %v", releasedAssessment, err)
	}
	cleanupNeeded = false
}

// privilegedDarwinResolverTicket constructs one complete policy-bound ticket for the root lifecycle.
func privilegedDarwinResolverTicket(
	t *testing.T,
	request resolver.Request,
	policy networkpolicy.Policy,
	operation helper.Operation,
	expectedFingerprint string,
	nonceMarker byte,
) helper.Ticket {
	t.Helper()
	now := time.Now().UTC()
	return helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                operation,
		InstallationID:           request.InstallationID(),
		RequesterIdentity:        "501",
		OwnershipGeneration:      1,
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: request.PolicyFingerprint(),
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		ExpectedResolverObservation: &helper.ExpectedResolverObservation{
			Fingerprint: expectedFingerprint,
		},
		Nonce:     strings.Repeat(string(nonceMarker), 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// dispatchPrivilegedResolverTicket drives the native effect through production dispatcher validation.
func dispatchPrivilegedResolverTicket(
	t *testing.T,
	resolverHandler helper.ResolverHandler,
	ticket helper.Ticket,
	reference helper.TicketReference,
) helper.Response {
	t.Helper()
	dispatcher := helper.NewDispatcherWithResolver(
		privilegedResolverRedeemer{
			ticket:    ticket,
			reference: reference,
			admission: func() helper.TicketAdmission {
				admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
				admission.TicketReference = reference
				return admission
			}(),
		},
		helper.SystemClock{},
		privilegedResolverReplayGuard{},
		helper.UnavailableLoopbackIdentityHandler{},
		resolverHandler,
	)
	response, err := dispatcher.Dispatch(t.Context(), helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	})
	if err != nil {
		t.Fatalf("helper resolver dispatch: %v", err)
	}
	if !response.OK || response.Error != nil {
		t.Fatalf("helper resolver dispatch response = %#v", response)
	}
	return response
}

// privilegedResolverRedeemer supplies one already authenticated root-test capability.
type privilegedResolverRedeemer struct {
	ticket    helper.Ticket
	reference helper.TicketReference
	admission helper.TicketAdmission
}

// Redeem returns admission dimensions independently reconstructed from the test's policy-bound ticket.
func (redeemer privilegedResolverRedeemer) Redeem(_ context.Context, reference helper.TicketReference) (helper.TicketRedemption, error) {
	if reference != redeemer.reference {
		return helper.TicketRedemption{}, fmt.Errorf("unexpected resolver ticket reference")
	}
	ticket := redeemer.ticket
	return helper.TicketRedemption{
		Ticket:    ticket,
		Admission: redeemer.admission,
	}, nil
}

// privilegedResolverReplayGuard accepts one unique in-process ticket used by the disposable root test.
type privilegedResolverReplayGuard struct{}

// Consume accepts the fixture because each dispatch receives a distinct nonce and reference.
func (privilegedResolverReplayGuard) Consume(context.Context, helper.ReplayClaim) error {
	return nil
}

var _ helper.TicketRedeemer = privilegedResolverRedeemer{}
var _ helper.ReplayGuard = privilegedResolverReplayGuard{}
