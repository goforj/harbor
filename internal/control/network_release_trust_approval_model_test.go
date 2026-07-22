package control

import (
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestNetworkReleaseTrustApprovalRequestValidate covers the bounded checkpoint selector.
func TestNetworkReleaseTrustApprovalRequestValidate(t *testing.T) {
	valid := PrepareNetworkReleaseTrustApprovalRequest{
		OperationID:                "operation-network-release",
		ExpectedCheckpointRevision: 7,
	}
	for _, test := range []struct {
		name    string
		request PrepareNetworkReleaseTrustApprovalRequest
		want    string
	}{
		{
			name:    "valid",
			request: valid,
		},
		{
			name: "operation ID",
			request: PrepareNetworkReleaseTrustApprovalRequest{
				ExpectedCheckpointRevision: 7,
			},
			want: "operation",
		},
		{
			name: "revision",
			request: PrepareNetworkReleaseTrustApprovalRequest{
				OperationID: valid.OperationID,
			},
			want: "checkpoint",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkReleaseTrustDispositionValidate accepts only retained ownership outcomes.
func TestNetworkReleaseTrustDispositionValidate(t *testing.T) {
	for _, disposition := range []NetworkReleaseTrustDisposition{
		NetworkReleaseTrustOwned,
		NetworkReleaseTrustPreexistingUnowned,
	} {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", disposition, err)
		}
	}
	if err := NetworkReleaseTrustDisposition("unknown").Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsupported disposition error")
	}
}

// TestNetworkReleaseTrustPublicationDispositionValidate accepts only explicit publication outcomes.
func TestNetworkReleaseTrustPublicationDispositionValidate(t *testing.T) {
	for _, disposition := range []NetworkReleaseTrustPublicationDisposition{
		NetworkReleaseTrustPublicationNotRequired,
		NetworkReleaseTrustPublicationDurable,
		NetworkReleaseTrustPublicationIndeterminate,
	} {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", disposition, err)
		}
	}
	if err := NetworkReleaseTrustPublicationDisposition("unknown").Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsupported disposition error")
	}
}

// TestNetworkReleaseTrustApprovalTicketValidate covers every public ticket boundary.
func TestNetworkReleaseTrustApprovalTicketValidate(t *testing.T) {
	valid := validNetworkReleaseTrustApprovalTicket()
	for _, test := range []struct {
		name   string
		mutate func(*NetworkReleaseTrustApprovalTicket)
		want   string
	}{
		{
			name: "valid",
		},
		{
			name: "operation ID",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.OperationID = ""
			},
			want: "operation",
		},
		{
			name: "reference",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.Reference = "bad"
			},
			want: "ticket reference",
		},
		{
			name: "operation",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.Operation = helper.OperationEnsureTrust
			},
			want: "expected",
		},
		{
			name: "policy fingerprint",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.PolicyFingerprint = "bad"
			},
			want: "policy fingerprint",
		},
		{
			name: "ownership fingerprint",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.TargetOwnershipFingerprint = "bad"
			},
			want: "target ownership fingerprint",
		},
		{
			name: "authority fingerprint",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.AuthorityFingerprint = "bad"
			},
			want: "authority fingerprint",
		},
		{
			name: "mechanism",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.Mechanism = "unsupported"
			},
			want: "mechanism",
		},
		{
			name: "zero expiry",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.ExpiresAt = time.Time{}
			},
			want: "network release trust expiry",
		},
		{
			name: "non UTC expiry",
			mutate: func(ticket *NetworkReleaseTrustApprovalTicket) {
				ticket.ExpiresAt = time.Date(2026, time.July, 23, 12, 0, 0, 0, time.FixedZone("offset", 3600))
			},
			want: "network release trust expiry",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ticket := valid
			if test.mutate != nil {
				test.mutate(&ticket)
			}
			err := ticket.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNetworkReleaseTrustApprovalPreparationValidate preserves the ownership, publication, and ticket invariants.
func TestNetworkReleaseTrustApprovalPreparationValidate(t *testing.T) {
	validTicket := validNetworkReleaseTrustApprovalTicket()
	owned := NetworkReleaseTrustApprovalPreparation{
		OperationID:            validTicket.OperationID,
		CheckpointRevision:     7,
		Disposition:            NetworkReleaseTrustOwned,
		PublicationDisposition: NetworkReleaseTrustPublicationDurable,
		Ticket:                 &validTicket,
	}
	preexisting := NetworkReleaseTrustApprovalPreparation{
		OperationID:            validTicket.OperationID,
		CheckpointRevision:     7,
		Disposition:            NetworkReleaseTrustPreexistingUnowned,
		PublicationDisposition: NetworkReleaseTrustPublicationNotRequired,
	}
	for _, test := range []struct {
		name  string
		value NetworkReleaseTrustApprovalPreparation
		want  string
	}{
		{
			name:  "owned durable",
			value: owned,
		},
		{
			name: "owned indeterminate",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.PublicationDisposition = NetworkReleaseTrustPublicationIndeterminate
				return value
			}(),
		},
		{
			name:  "preexisting",
			value: preexisting,
		},
		{
			name: "selection",
			value: NetworkReleaseTrustApprovalPreparation{
				CheckpointRevision: 7,
			},
			want: "operation",
		},
		{
			name: "disposition",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.Disposition = "unknown"
				return value
			}(),
			want: "disposition",
		},
		{
			name: "publication disposition",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.PublicationDisposition = "unknown"
				return value
			}(),
			want: "publication disposition",
		},
		{
			name: "owned not required",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.PublicationDisposition = NetworkReleaseTrustPublicationNotRequired
				return value
			}(),
			want: "must publish",
		},
		{
			name: "owned missing ticket",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.Ticket = nil
				return value
			}(),
			want: "no ticket",
		},
		{
			name: "owned invalid ticket",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				value.Ticket = &NetworkReleaseTrustApprovalTicket{}
				return value
			}(),
			want: "operation",
		},
		{
			name: "owned other ticket operation",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := owned
				ticket := validTicket
				ticket.OperationID = "operation-other"
				value.Ticket = &ticket
				return value
			}(),
			want: "another operation",
		},
		{
			name: "preexisting publication",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := preexisting
				value.PublicationDisposition = NetworkReleaseTrustPublicationDurable
				return value
			}(),
			want: "must not publish",
		},
		{
			name: "preexisting ticket",
			value: func() NetworkReleaseTrustApprovalPreparation {
				value := preexisting
				value.Ticket = &validTicket
				return value
			}(),
			want: "has a ticket",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestConfirmNetworkReleaseTrustApprovalRequestValidate permits coordinator-selected preservation and validates helper evidence exactly.
func TestConfirmNetworkReleaseTrustApprovalRequestValidate(t *testing.T) {
	validEvidence := validNetworkReleaseTrustEvidence()
	valid := ConfirmNetworkReleaseTrustApprovalRequest{
		OperationID:                "operation-network-release",
		ExpectedCheckpointRevision: 7,
	}
	for _, test := range []struct {
		name    string
		request ConfirmNetworkReleaseTrustApprovalRequest
		want    string
	}{
		{
			name:    "nil evidence",
			request: valid,
		},
		{
			name: "owned absent evidence",
			request: func() ConfirmNetworkReleaseTrustApprovalRequest {
				value := valid
				value.TrustEvidence = &validEvidence
				return value
			}(),
		},
		{
			name:    "selection",
			request: ConfirmNetworkReleaseTrustApprovalRequest{},
			want:    "operation",
		},
		{
			name: "authority fingerprint",
			request: func() ConfirmNetworkReleaseTrustApprovalRequest {
				value := valid
				evidence := validEvidence
				evidence.AuthorityFingerprint = "bad"
				value.TrustEvidence = &evidence
				return value
			}(),
			want: "authority fingerprint",
		},
		{
			name: "observation fingerprint",
			request: func() ConfirmNetworkReleaseTrustApprovalRequest {
				value := valid
				evidence := validEvidence
				evidence.ObservationFingerprint = "bad"
				value.TrustEvidence = &evidence
				return value
			}(),
			want: "observation fingerprint",
		},
		{
			name: "mechanism",
			request: func() ConfirmNetworkReleaseTrustApprovalRequest {
				value := valid
				evidence := validEvidence
				evidence.Mechanism = "unsupported"
				value.TrustEvidence = &evidence
				return value
			}(),
			want: "mechanism",
		},
		{
			name: "postcondition",
			request: func() ConfirmNetworkReleaseTrustApprovalRequest {
				value := valid
				evidence := validEvidence
				evidence.Postcondition = helper.TrustPostconditionExact
				value.TrustEvidence = &evidence
				return value
			}(),
			want: "owned absence",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// validNetworkReleaseTrustApprovalTicket constructs canonical non-secret trust-release launch metadata.
func validNetworkReleaseTrustApprovalTicket() NetworkReleaseTrustApprovalTicket {
	return NetworkReleaseTrustApprovalTicket{
		OperationID:                domain.OperationID("operation-network-release"),
		Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
		Operation:                  helper.OperationReleaseTrust,
		PolicyFingerprint:          strings.Repeat("b", 64),
		TargetOwnershipFingerprint: strings.Repeat("c", 64),
		AuthorityFingerprint:       strings.Repeat("d", 64),
		Mechanism:                  networkpolicy.DarwinCurrentUserTrust,
		ExpiresAt:                  time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
	}
}

// validNetworkReleaseTrustEvidence constructs a canonical owned-absent trust-release postcondition.
func validNetworkReleaseTrustEvidence() helper.TrustMutationEvidence {
	return helper.TrustMutationEvidence{
		AuthorityFingerprint:   strings.Repeat("a", 64),
		Mechanism:              networkpolicy.DarwinCurrentUserTrust,
		ObservationFingerprint: strings.Repeat("b", 64),
		Postcondition:          helper.TrustPostconditionOwnedAbsent,
	}
}
