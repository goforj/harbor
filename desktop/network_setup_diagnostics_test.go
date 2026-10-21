package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/desktop/internal/networkprerequisite"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/rpc"
)

// TestSetupNetworkPreservesLowPortReasonWhenRepairIsUnavailable keeps the first failure visible without replaying privileged approval.
func TestSetupNetworkPreservesLowPortReasonWhenRepairIsUnavailable(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		err     error
		outcome networkdataplaneapproval.LowPortOutcome
		want    string
	}{
		{
			name: "missing ticket directory",
			err:  rpc.WireError{Code: rpc.ErrorCodePrivilegedHelperRequired, Message: "private-rpc-detail"},
			want: "the helper ticket directory is missing",
		},
		{
			name: "unsafe ticket directory",
			err:  rpc.WireError{Code: rpc.ErrorCodePrivilegedHelperUnsafe, Message: "private-rpc-detail"},
			want: "the helper ticket directory has unsafe ownership or permissions",
		},
		{
			name:    "native helper unavailable",
			outcome: networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Unavailable},
			want:    "native helper launch is unavailable",
		},
		{
			name: "ticket authentication failed",
			outcome: networkdataplaneapproval.LowPortOutcome{
				State: networkdataplaneapproval.HelperFailed,
				HelperFailure: &networkdataplaneapproval.HelperFailure{
					Code: helper.ErrorCodeAuthenticationFailed, Message: "private-helper-detail",
				},
			},
			want: "the helper could not authenticate the low-port ticket",
		},
		{
			name: "verification failed",
			outcome: networkdataplaneapproval.LowPortOutcome{
				State: networkdataplaneapproval.HelperFailed,
				HelperFailure: &networkdataplaneapproval.HelperFailure{
					Code: helper.ErrorCodeMutationFailed, Message: "helper operation failed: low-port ensure verification-failed",
				},
			},
			want: "low-port ensure verification failed",
		},
		{
			name: "observation changed",
			outcome: networkdataplaneapproval.LowPortOutcome{
				State: networkdataplaneapproval.HelperFailed,
				HelperFailure: &networkdataplaneapproval.HelperFailure{
					Code: helper.ErrorCodeMutationFailed, Message: "helper operation failed: low-port ensure observation-changed",
				},
			},
			want: "the low-port observation changed during setup",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			setup := testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
			client.dataPlaneSetup = setup
			ensurer := &fakeNetworkPrerequisiteEnsurer{err: networkprerequisite.ErrUnavailable}
			runner := &fakeNetworkDataPlaneApprovalRunner{lowPortErr: test.err, lowPortOutcome: test.outcome}
			app.setupPrerequisite = ensurer
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }

			_, err := app.SetupNetwork()
			if !errors.Is(err, networkprerequisite.ErrUnavailable) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want wrapped unavailable and %q", err, test.want)
			}
			for _, private := range []string{"private-rpc-detail", "private-helper-detail"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("SetupNetwork() exposed unreviewed %q", private)
				}
			}
			if ensurer.calls != 1 || len(runner.lowPortRequests) != 1 || len(runner.trustRequests) != 0 {
				t.Fatalf("repair/low-port/trust calls = %d/%d/%d, want 1/1/0", ensurer.calls, len(runner.lowPortRequests), len(runner.trustRequests))
			}
			wantRequest := networkdataplaneapproval.Request{OperationID: setup.Operation.ID, ExpectedOperationRevision: setup.Revision}
			if runner.lowPortRequests[0] != wantRequest {
				t.Fatalf("approval request = %#v, want %#v", runner.lowPortRequests[0], wantRequest)
			}
			if client.dataPlaneLowPortConfirmReq.OperationID != "" || client.dataPlaneSetup.Revision != setup.Revision || client.dataPlaneSetup.Operation.State != domain.OperationRequiresApproval {
				t.Fatal("unavailable repair confirmed or advanced the selected operation")
			}
		})
	}
}

// TestNetworkDataPlaneLowPortRepairUnavailableErrorRejectsUnreviewedDetails prevents unknown failures from becoming arbitrary diagnostic text.
func TestNetworkDataPlaneLowPortRepairUnavailableErrorRejectsUnreviewedDetails(t *testing.T) {
	t.Parallel()
	for _, outcome := range []networkdataplaneapproval.LowPortOutcome{
		{},
		{State: networkdataplaneapproval.HelperFailed},
		{State: networkdataplaneapproval.HelperFailed, HelperFailure: &networkdataplaneapproval.HelperFailure{
			Code: helper.ErrorCodeMutationFailed, Message: "helper operation failed: low-port ensure verification-failed private-detail",
		}},
		{State: networkdataplaneapproval.HelperFailed, HelperFailure: &networkdataplaneapproval.HelperFailure{
			Code: helper.ErrorCodeInvalidTicket, Message: "helper operation failed: low-port ensure observation-changed",
		}},
	} {
		err := networkDataPlaneLowPortRepairUnavailableError(outcome, errors.New("private-detail"))
		if !errors.Is(err, networkprerequisite.ErrUnavailable) || !strings.Contains(err.Error(), "unclassified low-port approval failure") || strings.Contains(err.Error(), "private-detail") {
			t.Fatalf("unreviewed outcome exposed or misclassified: %v", err)
		}
	}
}
