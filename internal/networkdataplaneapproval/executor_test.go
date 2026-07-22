package networkdataplaneapproval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	testOperationID domain.OperationID = "operation-network-data-plane-setup"
	testRevision    domain.Sequence    = 17
	testPolicy                         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOwnership                      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testAuthority                      = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testObservation                    = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

// errTestFailure supplies one stable injected boundary failure.
var errTestFailure = errors.New("test failure")

// TestExecuteTrustConfirmsExactEvidence verifies the trust phase returns the same operation at low-port approval.
func TestExecuteTrustConfirmsExactEvidence(t *testing.T) {
	t.Parallel()
	client := &fakeClient{trustPreparation: validTrustPreparation(t), trustConfirmation: lowPortSetup(t, testOperationID, 24)}
	launcher := &fakeLauncher{trustOutcome: successfulTrustLaunch(validTrustEvidence())}
	outcome, err := New(client, launcher).ExecuteTrust(context.Background(), validRequest())
	if err != nil || outcome.State != Succeeded || outcome.Setup == nil || outcome.Setup.Revision != 24 {
		t.Fatalf("ExecuteTrust() = (%#v, %v)", outcome, err)
	}
	if client.trustPrepareCalls != 1 || launcher.trustCalls != 1 || client.trustConfirmCalls != 1 {
		t.Fatalf("unexpected calls: %#v %#v", client, launcher)
	}
	if client.trustConfirmRequest.TrustEvidence != validTrustEvidence() {
		t.Fatalf("trust confirmation evidence = %#v", client.trustConfirmRequest.TrustEvidence)
	}
}

// TestExecuteLowPortsConfirmsExactEvidence verifies the low-port phase returns terminal full setup confirmation.
func TestExecuteLowPortsConfirmsExactEvidence(t *testing.T) {
	t.Parallel()
	client := &fakeClient{lowPortPreparation: validLowPortPreparation(t), lowPortConfirmation: fullSetupConfirmation(t, testOperationID, 29, 28)}
	launcher := &fakeLauncher{lowPortOutcome: successfulLowPortLaunch(validLowPortEvidence())}
	outcome, err := New(client, launcher).ExecuteLowPorts(context.Background(), validRequest())
	if err != nil || outcome.State != Succeeded || outcome.Confirmation == nil || outcome.Confirmation.Revision != 29 {
		t.Fatalf("ExecuteLowPorts() = (%#v, %v)", outcome, err)
	}
	if client.lowPortPrepareCalls != 1 || launcher.lowPortCalls != 1 || client.lowPortConfirmCalls != 1 {
		t.Fatalf("unexpected calls: %#v %#v", client, launcher)
	}
	if client.lowPortConfirmRequest.LowPortEvidence != validLowPortEvidence() {
		t.Fatalf("low-port confirmation evidence = %#v", client.lowPortConfirmRequest.LowPortEvidence)
	}
}

// TestExecuteMapsNoChildAndHelperFailure verifies terminal launch states never reach confirmation.
func TestExecuteMapsNoChildAndHelperFailure(t *testing.T) {
	t.Parallel()
	failure := helper.ResponseError{Code: helper.ErrorCodeMutationFailed, Message: "safe failure"}
	states := []launcher.Outcome{
		{State: launcher.Declined}, {State: launcher.Unavailable},
		{State: launcher.HelperFailed, Response: helper.Response{Version: helper.ProtocolVersion, Error: &failure}, Exit: &launcher.ProcessExit{Code: launcher.ExitCodeHelperFailed}},
		{State: launcher.Indeterminate},
	}
	for _, phase := range []string{"trust", "low port"} {
		for _, launch := range states {
			t.Run(phase+"/"+string(launch.State), func(t *testing.T) {
				client := &fakeClient{trustPreparation: validTrustPreparation(t), lowPortPreparation: validLowPortPreparation(t)}
				helperLauncher := &fakeLauncher{trustOutcome: launch, lowPortOutcome: launch}
				if phase == "trust" {
					outcome, err := New(client, helperLauncher).ExecuteTrust(context.Background(), validRequest())
					if err != nil || outcome.State != State(launch.State) {
						t.Fatalf("trust = %#v, %v", outcome, err)
					}
					if client.trustConfirmCalls != 0 {
						t.Fatal("trust confirmed terminal outcome")
					}
				} else {
					outcome, err := New(client, helperLauncher).ExecuteLowPorts(context.Background(), validRequest())
					if err != nil || outcome.State != State(launch.State) {
						t.Fatalf("low port = %#v, %v", outcome, err)
					}
					if client.lowPortConfirmCalls != 0 {
						t.Fatal("low port confirmed terminal outcome")
					}
				}
			})
		}
	}
}

// TestExecuteRejectsUncertainty verifies launch and confirmation failures remain indeterminate after a helper may start.
func TestExecuteRejectsUncertainty(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"trust", "low port"} {
		for _, failure := range []string{"launch", "confirm", "cancellation"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				client := &fakeClient{trustPreparation: validTrustPreparation(t), lowPortPreparation: validLowPortPreparation(t), trustConfirmation: lowPortSetup(t, testOperationID, 24), lowPortConfirmation: fullSetupConfirmation(t, testOperationID, 29, 28)}
				helperLauncher := &fakeLauncher{trustOutcome: successfulTrustLaunch(validTrustEvidence()), lowPortOutcome: successfulLowPortLaunch(validLowPortEvidence())}
				if failure == "launch" {
					helperLauncher.err = errTestFailure
				}
				if failure == "confirm" {
					client.confirmErr = errTestFailure
				}
				ctx := context.Background()
				if failure == "cancellation" {
					cancelled, cancel := context.WithCancel(context.Background())
					helperLauncher.afterLaunch = cancel
					ctx = cancelled
				}
				if phase == "trust" {
					outcome, err := New(client, helperLauncher).ExecuteTrust(ctx, validRequest())
					if outcome.State != Indeterminate || err == nil {
						t.Fatalf("trust = %#v, %v", outcome, err)
					}
				} else {
					outcome, err := New(client, helperLauncher).ExecuteLowPorts(ctx, validRequest())
					if outcome.State != Indeterminate || err == nil {
						t.Fatalf("low port = %#v, %v", outcome, err)
					}
				}
			})
		}
	}
}

// TestExecuteRejectsMismatchedEvidenceAndPostconditions verifies alternate evidence cannot cross either phase.
func TestExecuteRejectsMismatchedEvidenceAndPostconditions(t *testing.T) {
	t.Parallel()
	wrongTrust := validTrustEvidence()
	wrongTrust.AuthorityFingerprint = testObservation
	wrongLowPort := validLowPortEvidence()
	wrongLowPort.OwnershipFingerprint = testAuthority
	for _, test := range []struct {
		name  string
		trust launcher.Outcome
		low   launcher.Outcome
	}{
		{"trust evidence", successfulTrustLaunch(wrongTrust), successfulLowPortLaunch(validLowPortEvidence())},
		{"low-port evidence", successfulTrustLaunch(validTrustEvidence()), successfulLowPortLaunch(wrongLowPort)},
		{"trust unrelated evidence", launcher.Outcome{State: launcher.Succeeded, Response: helper.Response{Version: helper.ProtocolVersion, OK: true, Result: &helper.OperationResult{Operation: helper.OperationEnsureTrust, TrustEvidence: ptrTrust(validTrustEvidence()), LowPortEvidence: ptrLowPort(validLowPortEvidence())}}, Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded}}, successfulLowPortLaunch(validLowPortEvidence())},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{trustPreparation: validTrustPreparation(t), lowPortPreparation: validLowPortPreparation(t)}
			helperLauncher := &fakeLauncher{trustOutcome: test.trust, lowPortOutcome: test.low}
			if strings.HasPrefix(test.name, "low") {
				outcome, err := New(client, helperLauncher).ExecuteLowPorts(context.Background(), validRequest())
				if outcome.State != Indeterminate || !errors.Is(err, ErrInconsistentResponse) {
					t.Fatalf("low port = %#v, %v", outcome, err)
				}
			} else {
				outcome, err := New(client, helperLauncher).ExecuteTrust(context.Background(), validRequest())
				if outcome.State != Indeterminate || !errors.Is(err, ErrInconsistentResponse) {
					t.Fatalf("trust = %#v, %v", outcome, err)
				}
			}
		})
	}
}

// TestExecuteRejectsMismatchedDaemonResponses verifies preparations and postconditions cannot cross the selected operation.
func TestExecuteRejectsMismatchedDaemonResponses(t *testing.T) {
	t.Parallel()
	wrongPreparation := validTrustPreparation(t)
	wrongPreparation.OperationRevision = 18
	wrongSetup := lowPortSetup(t, "other-operation", 24)
	for _, test := range []struct {
		name       string
		client     *fakeClient
		wantLaunch int
	}{
		{"preparation", &fakeClient{trustPreparation: wrongPreparation}, 0},
		{"confirmation", &fakeClient{trustPreparation: validTrustPreparation(t), trustConfirmation: wrongSetup}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			launcher := &fakeLauncher{trustOutcome: successfulTrustLaunch(validTrustEvidence())}
			outcome, err := New(test.client, launcher).ExecuteTrust(context.Background(), validRequest())
			if outcome.State != Indeterminate && test.wantLaunch != 0 {
				t.Fatalf("outcome = %#v", outcome)
			}
			if !errors.Is(err, ErrInconsistentResponse) || launcher.trustCalls != test.wantLaunch {
				t.Fatalf("error/calls = %v/%d", err, launcher.trustCalls)
			}
		})
	}
}

// TestExecuteRejectsInvalidRequestAndPreflightCancellation verifies no helper starts before a valid selected revision.
func TestExecuteRejectsInvalidRequestAndPreflightCancellation(t *testing.T) {
	t.Parallel()
	for _, ctx := range []context.Context{context.Background(), cancelledContext()} {
		client := &fakeClient{}
		helperLauncher := &fakeLauncher{}
		request := Request{}
		if ctx.Err() != nil {
			request = validRequest()
		}
		_, err := New(client, helperLauncher).ExecuteTrust(ctx, request)
		if err == nil || client.trustPrepareCalls != 0 || helperLauncher.trustCalls != 0 {
			t.Fatalf("err/calls = %v/%d/%d", err, client.trustPrepareCalls, helperLauncher.trustCalls)
		}
	}
}

// fakeClient records the four bounded daemon calls.
type fakeClient struct {
	trustPreparation                                                               control.NetworkDataPlaneTrustApprovalPreparation
	lowPortPreparation                                                             control.NetworkDataPlaneLowPortApprovalPreparation
	trustConfirmation                                                              control.NetworkDataPlaneSetupOperation
	lowPortConfirmation                                                            control.NetworkDataPlaneSetupConfirmation
	err                                                                            error
	confirmErr                                                                     error
	trustPrepareCalls, lowPortPrepareCalls, trustConfirmCalls, lowPortConfirmCalls int
	trustConfirmRequest                                                            control.ConfirmNetworkDataPlaneTrustApprovalRequest
	lowPortConfirmRequest                                                          control.ConfirmNetworkDataPlaneLowPortApprovalRequest
}

// PrepareNetworkDataPlaneTrustApproval records and returns the scripted trust preparation.
func (client *fakeClient) PrepareNetworkDataPlaneTrustApproval(context.Context, control.PrepareNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
	client.trustPrepareCalls++
	return client.trustPreparation, client.err
}

// ConfirmNetworkDataPlaneTrustApproval records exact helper evidence before returning the scripted transition.
func (client *fakeClient) ConfirmNetworkDataPlaneTrustApproval(_ context.Context, request control.ConfirmNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneSetupOperation, error) {
	client.trustConfirmCalls++
	client.trustConfirmRequest = request
	return client.trustConfirmation, client.confirmErr
}

// PrepareNetworkDataPlaneLowPortApproval records and returns the scripted low-port preparation.
func (client *fakeClient) PrepareNetworkDataPlaneLowPortApproval(context.Context, control.PrepareNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
	client.lowPortPrepareCalls++
	return client.lowPortPreparation, client.err
}

// ConfirmNetworkDataPlaneLowPortApproval records exact helper evidence before returning the scripted completion.
func (client *fakeClient) ConfirmNetworkDataPlaneLowPortApproval(_ context.Context, request control.ConfirmNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneSetupConfirmation, error) {
	client.lowPortConfirmCalls++
	client.lowPortConfirmRequest = request
	return client.lowPortConfirmation, client.confirmErr
}

// fakeLauncher records each phase-specific helper launch.
type fakeLauncher struct {
	trustOutcome, lowPortOutcome launcher.Outcome
	err                          error
	afterLaunch                  context.CancelFunc
	trustCalls, lowPortCalls     int
}

// InvokeTrust records one native-consent attempt and returns its scripted outcome.
func (launcher *fakeLauncher) InvokeTrust(context.Context, launcher.TrustLaunchTicket) (launcher.Outcome, error) {
	launcher.trustCalls++
	if launcher.afterLaunch != nil {
		launcher.afterLaunch()
	}
	return launcher.trustOutcome, launcher.err
}

// InvokeLowPorts records one native-consent attempt and returns its scripted outcome.
func (launcher *fakeLauncher) InvokeLowPorts(context.Context, launcher.LowPortLaunchTicket) (launcher.Outcome, error) {
	launcher.lowPortCalls++
	if launcher.afterLaunch != nil {
		launcher.afterLaunch()
	}
	return launcher.lowPortOutcome, launcher.err
}

// validRequest selects the shared exact operation revision fixture.
func validRequest() Request {
	return Request{OperationID: testOperationID, ExpectedOperationRevision: testRevision}
}

// validTrustPreparation constructs canonical trust launch metadata for the selected revision.
func validTrustPreparation(t *testing.T) control.NetworkDataPlaneTrustApprovalPreparation {
	t.Helper()
	value := control.NetworkDataPlaneTrustApprovalPreparation{OperationID: testOperationID, OperationRevision: testRevision, Ticket: control.NetworkDataPlaneTrustApprovalTicket{OperationID: testOperationID, Reference: helper.TicketReference(strings.Repeat("e", 64)), Operation: helper.OperationEnsureTrust, PolicyFingerprint: testPolicy, TargetOwnershipFingerprint: testOwnership, AuthorityFingerprint: testAuthority, Mechanism: networkpolicy.DarwinCurrentUserTrust, ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

// validLowPortPreparation constructs canonical low-port launch metadata for the selected revision.
func validLowPortPreparation(t *testing.T) control.NetworkDataPlaneLowPortApprovalPreparation {
	t.Helper()
	value := control.NetworkDataPlaneLowPortApprovalPreparation{OperationID: testOperationID, OperationRevision: testRevision, Ticket: control.NetworkDataPlaneLowPortApprovalTicket{OperationID: testOperationID, Reference: helper.TicketReference(strings.Repeat("f", 64)), Operation: helper.OperationEnsureLowPorts, PolicyFingerprint: testPolicy, TargetOwnershipFingerprint: testOwnership, ObservationFingerprint: testObservation, ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

// validTrustEvidence constructs an exact public-root postcondition.
func validTrustEvidence() helper.TrustMutationEvidence {
	return helper.TrustMutationEvidence{Changed: true, AuthorityFingerprint: testAuthority, ObservationFingerprint: testObservation, Mechanism: networkpolicy.DarwinCurrentUserTrust, Postcondition: helper.TrustPostconditionExact}
}

// validLowPortEvidence constructs an exact paired-listener postcondition.
func validLowPortEvidence() helper.LowPortMutationEvidence {
	return helper.LowPortMutationEvidence{Changed: true, PolicyFingerprint: testPolicy, OwnershipFingerprint: testOwnership, ObservationFingerprint: testObservation, Postcondition: helper.LowPortPostconditionExact}
}

// successfulTrustLaunch wraps exact evidence in the reviewed helper success envelope.
func successfulTrustLaunch(evidence helper.TrustMutationEvidence) launcher.Outcome {
	return launcher.Outcome{State: launcher.Succeeded, Response: helper.Response{Version: helper.ProtocolVersion, OK: true, Result: &helper.OperationResult{Operation: helper.OperationEnsureTrust, TrustEvidence: &evidence}}, Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded}}
}

// successfulLowPortLaunch wraps exact evidence in the reviewed helper success envelope.
func successfulLowPortLaunch(evidence helper.LowPortMutationEvidence) launcher.Outcome {
	return launcher.Outcome{State: launcher.Succeeded, Response: helper.Response{Version: helper.ProtocolVersion, OK: true, Result: &helper.OperationResult{Operation: helper.OperationEnsureLowPorts, LowPortEvidence: &evidence}}, Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded}}
}

// lowPortSetup constructs the sole post-trust approval phase accepted by the executor.
func lowPortSetup(t *testing.T, id domain.OperationID, revision domain.Sequence) control.NetworkDataPlaneSetupOperation {
	t.Helper()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	value := control.NetworkDataPlaneSetupOperation{Operation: domain.Operation{ID: id, IntentID: "intent-network-data-plane-setup", Kind: domain.OperationKindNetworkDataPlaneSetup, State: domain.OperationRequiresApproval, Phase: "awaiting low-port approval", RequestedAt: now, StartedAt: &now}, Revision: revision}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

// fullSetupConfirmation constructs one terminal full-network setup result.
func fullSetupConfirmation(t *testing.T, id domain.OperationID, revision, networkRevision domain.Sequence) control.NetworkDataPlaneSetupConfirmation {
	t.Helper()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	value := control.NetworkDataPlaneSetupConfirmation{Operation: domain.Operation{ID: id, IntentID: "intent-network-data-plane-setup", Kind: domain.OperationKindNetworkDataPlaneSetup, State: domain.OperationSucceeded, Phase: "completed", RequestedAt: now, StartedAt: &now, FinishedAt: &now}, Revision: revision, NetworkRevision: networkRevision}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}

// ptrTrust returns independently addressable trust evidence for mixed-envelope rejection tests.
func ptrTrust(value helper.TrustMutationEvidence) *helper.TrustMutationEvidence { return &value }

// ptrLowPort returns independently addressable low-port evidence for mixed-envelope rejection tests.
func ptrLowPort(value helper.LowPortMutationEvidence) *helper.LowPortMutationEvidence { return &value }

// cancelledContext returns a context cancelled before any daemon or helper boundary.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
