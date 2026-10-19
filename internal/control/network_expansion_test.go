package control

import (
	"encoding/json"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"strings"
	"testing"
)

// TestNetworkExpansionRequestIsExplicitAndStrict protects the additional authority from ambiguous or duplicate flags.
func TestNetworkExpansionRequestIsExplicitAndStrict(t *testing.T) {
	for _, payload := range []string{`{"intent_id":"intent-expand","expand_pool":true}`, `{"expand_pool":true,"intent_id":"intent-expand"}`} {
		request, err := decodeStartNetworkSetupRequest([]byte(payload))
		if err != nil || !request.ExpandPool {
			t.Fatalf("explicit expansion: %v", err)
		}
	}
	for _, payload := range []string{`{"intent_id":"intent-expand","expand_pool":null}`, `{"intent_id":"intent-expand","expand_pool":"true"}`, `{"intent_id":"intent-expand","expand_pool":true,"expand_pool":false}`, `{"intent_id":"intent-expand","pool":"127.0.0.0/8"}`} {
		if _, err := decodeStartNetworkSetupRequest([]byte(payload)); err == nil {
			t.Fatalf("ambiguous expansion accepted: %s", payload)
		}
	}
	legacy, err := decodeStartNetworkSetupRequest([]byte(`{"intent_id":"intent-repair"}`))
	if err != nil || legacy.ExpandPool {
		t.Fatal("ordinary repair widened authority")
	}
}

// TestNetworkSetupConfirmationBindsApproval distinguishes growth from repair without permitting revision drift.
func TestNetworkSetupConfirmationBindsApproval(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		repair                bool
		root, final, approval domain.Sequence
		pool                  string
		valid                 bool
	}{
		{"initial", false, 7, 8, 5, "127.42.0.0/24", true},
		{"legacy initial", false, 7, 8, 5, "127.42.0.0/29", true},
		{"repair", true, 3, 7, 5, "127.42.0.0/29", true},
		{"large repair", true, 3, 7, 5, "127.42.0.0/24", true},
		{"expansion", true, 7, 8, 5, "127.42.0.0/24", true},
		{"legacy growth", true, 7, 8, 5, "127.42.0.0/29", false},
		{"changed root", true, 6, 8, 5, "127.42.0.0/24", false},
		{"changed final", true, 7, 9, 5, "127.42.0.0/24", false},
		{"changed repair", true, 3, 8, 5, "127.42.0.0/24", false},
		{"zero approval", true, 3, 8, 0, "127.42.0.0/24", false},
		{"oversized approval", true, 3, 8, domain.MaximumSequence + 1, "127.42.0.0/24", false},
		{"invalid proof", true, 0, 8, 5, "127.42.0.0/24", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validNetworkSetupApprovalConfirmation()
			c.Repair = tc.repair
			c.NetworkRevision = tc.root
			c.Revision = tc.final
			c.Pool = tc.pool
			if err := c.ValidateApproval(c.Operation.ID, tc.approval); (err == nil) != tc.valid {
				t.Fatalf("ValidateApproval() = %v, valid %v", err, tc.valid)
			}
			if tc.valid && c.ValidateApproval("operation-another", tc.approval) == nil {
				t.Fatal("accepted another operation")
			}
		})
	}
}

// TestApprovalReceiptAllowsConcurrentStateChanges preserves exact approval identity without requiring adjacent global sequences.
func TestApprovalReceiptAllowsConcurrentStateChanges(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		repair, expanded      bool
		root, final, approval domain.Sequence
		valid                 bool
	}{
		{"initial", false, false, 15, 16, 5, true}, {"repair", true, false, 11, 16, 5, true}, {"expansion", true, true, 15, 16, 5, true},
		{"crossed approval", true, true, 15, 16, 6, false}, {"missing receipt", true, true, 15, 16, 0, false},
		{"early completion", true, true, 6, 7, 5, false}, {"split commit", true, true, 15, 17, 5, false},
		{"growth without repair", false, true, 15, 16, 5, false}, {"oversized approval", true, true, 15, 16, domain.MaximumSequence + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validNetworkSetupApprovalConfirmation()
			c.Pool = "127.42.0.0/24"
			c.Repair = tc.repair
			c.Expanded = tc.expanded
			c.NetworkRevision = tc.root
			c.Revision = tc.final
			c.ApprovalRevision = tc.approval
			if err := c.ValidateApproval(c.Operation.ID, 5); (err == nil) != tc.valid {
				t.Fatalf("receipt = %v, valid %v", err, tc.valid)
			}
		})
	}
}

// TestPendingNetworkSetupIntentResumesOnlyOneValidatedApproval rejects ambiguous snapshot authority.
func TestPendingNetworkSetupIntentResumesOnlyOneValidatedApproval(t *testing.T) {
	operation := validNetworkSetupOperation(domain.OperationRequiresApproval)
	for _, tc := range []struct {
		name       string
		operations []domain.Operation
		valid      bool
		want       domain.IntentID
	}{
		{"none", nil, true, ""}, {"one", []domain.Operation{operation}, true, operation.IntentID},
		{"two", []domain.Operation{operation, operation}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PendingNetworkSetupIntent(domain.Snapshot{Operations: tc.operations})
			if (err == nil) != tc.valid || got != tc.want {
				t.Fatalf("intent %q: %v", got, err)
			}
		})
	}
	operation.ProjectID = "project-invalid"
	if _, err := PendingNetworkSetupIntent(domain.Snapshot{Operations: []domain.Operation{operation}}); err == nil {
		t.Fatal("accepted project setup authority")
	}
}

// TestSetupResumeTransportUsesOnlyRetainedEvidence protects the narrow resume wire contract.
func TestSetupResumeTransportUsesOnlyRetainedEvidence(t *testing.T) {
	request := ConfirmNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 3, Resume: true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "pool_evidence") {
		t.Fatal("resume marshaled replacement proof")
	}
	if decoded, err := decodeConfirmNetworkSetupApprovalRequest(body); err != nil || !decoded.Resume {
		t.Fatal("resume decode", err)
	}
	for _, payload := range []string{
		`{"operation_id":"operation-network-setup","expected_operation_revision":3,"resume":null}`,
		`{"operation_id":"operation-network-setup","expected_operation_revision":3,"resume":"true"}`,
		`{"operation_id":"operation-network-setup","expected_operation_revision":3,"resume":true,"resume":false}`,
		`{"operation_id":"operation-network-setup","expected_operation_revision":3,"resume":true,"pool_evidence":{}}`,
	} {
		if _, err := decodeConfirmNetworkSetupApprovalRequest([]byte(payload)); err == nil {
			t.Fatal("ambiguous resume accepted", payload)
		}
	}
	replacement := request
	replacement.PoolEvidence = validNetworkSetupPoolEvidence()
	if replacement.Validate() == nil {
		t.Fatal("resume accepted caller-replaced proof")
	}
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			confirmation := validNetworkSetupApprovalConfirmation()
			confirmation.ApprovalRevision = 3
			confirmation.NetworkRevision = 7
			confirmation.Revision = 8
			authority := &recordingAuthority{networkSetupConfirmation: confirmation}
			running := newRunningControlClient(t, role, authority, nil)
			if _, err := running.client.ConfirmNetworkSetupApproval(t.Context(), request); err != nil {
				t.Fatal(err)
			}
		})
	}
}
