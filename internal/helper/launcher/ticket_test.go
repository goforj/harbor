package launcher

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestNewLaunchTicketAcceptsAllowlistedMetadata verifies both helper effects produce immutable launch values.
func TestNewLaunchTicketAcceptsAllowlistedMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	leaseKey := validLaunchLeaseKey(t)
	reference := helper.TicketReference(strings.Repeat("a", 64))
	for _, operation := range []helper.Operation{
		helper.OperationEnsureLoopbackIdentity,
		helper.OperationReleaseLoopbackIdentity,
	} {
		t.Run(string(operation), func(t *testing.T) {
			ticket, err := NewLaunchTicket(
				domain.OperationID("operation-1"),
				leaseKey,
				reference,
				operation,
				"127.77.0.10",
				now.Add(time.Minute),
			)
			if err != nil {
				t.Fatalf("NewLaunchTicket() error = %v", err)
			}
			if ticket.operationID != "operation-1" || ticket.leaseKey != leaseKey || ticket.reference != reference {
				t.Fatalf("ticket identity = %#v", ticket)
			}
			if ticket.operation != operation || ticket.address != netip.MustParseAddr("127.77.0.10") || !ticket.expiresAt.Equal(now.Add(time.Minute)) {
				t.Fatalf("ticket effect = %#v", ticket)
			}
		})
	}
}

// TestNewLaunchTicketRejectsInvalidMetadata covers every structural trust boundary in the DTO conversion.
func TestNewLaunchTicketRejectsInvalidMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	type input struct {
		operationID domain.OperationID
		leaseKey    identity.LeaseKey
		reference   helper.TicketReference
		operation   helper.Operation
		address     string
		expiresAt   time.Time
	}
	valid := input{
		operationID: domain.OperationID("operation-1"),
		leaseKey:    validLaunchLeaseKey(t),
		reference:   helper.TicketReference(strings.Repeat("a", 64)),
		operation:   helper.OperationReleaseLoopbackIdentity,
		address:     "127.77.0.10",
		expiresAt:   now.Add(time.Minute),
	}
	tests := []struct {
		name   string
		mutate func(*input)
	}{
		{name: "operation ID", mutate: func(value *input) { value.operationID = " bad " }},
		{name: "lease key", mutate: func(value *input) { value.leaseKey.SecondaryID = "bad secondary" }},
		{name: "reference", mutate: func(value *input) { value.reference = "short" }},
		{name: "helper operation", mutate: func(value *input) { value.operation = "run_command" }},
		{name: "malformed address", mutate: func(value *input) { value.address = "not-an-address" }},
		{name: "non-loopback address", mutate: func(value *input) { value.address = "192.0.2.10" }},
		{name: "IPv6 loopback address", mutate: func(value *input) { value.address = "::1" }},
		{name: "mapped IPv4 address", mutate: func(value *input) { value.address = "::ffff:127.77.0.10" }},
		{name: "zero expiry", mutate: func(value *input) { value.expiresAt = time.Time{} }},
		{name: "non-UTC expiry", mutate: func(value *input) {
			value.expiresAt = now.In(time.FixedZone("test", 0)).Add(time.Minute)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			ticket, err := NewLaunchTicket(
				candidate.operationID,
				candidate.leaseKey,
				candidate.reference,
				candidate.operation,
				candidate.address,
				candidate.expiresAt,
			)
			if err == nil {
				t.Fatalf("NewLaunchTicket() = %#v, nil", ticket)
			}
			if ticket != (LaunchTicket{}) {
				t.Fatalf("NewLaunchTicket() ticket = %#v, want zero value", ticket)
			}
			if strings.Contains(err.Error(), string(candidate.reference)) {
				t.Fatal("constructor error exposed the opaque ticket reference")
			}
		})
	}
}

// TestLaunchTicketValidateAtAcceptsProtocolLifetimeBoundary verifies the full allowed lifetime remains launchable.
func TestLaunchTicketValidateAtAcceptsProtocolLifetimeBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	ticket, err := NewLaunchTicket(
		domain.OperationID("operation-1"),
		validLaunchLeaseKey(t),
		helper.TicketReference(strings.Repeat("a", 64)),
		helper.OperationReleaseLoopbackIdentity,
		"127.77.0.10",
		now.Add(helper.MaxTicketLifetime),
	)
	if err != nil {
		t.Fatalf("NewLaunchTicket() error = %v", err)
	}
	if err := ticket.validateAt(now); err != nil {
		t.Fatalf("validateAt() error = %v", err)
	}
}

// TestNewPoolLaunchTicketAcceptsCanonicalMetadata verifies aggregate ensure and release approvals become immutable launch values.
func TestNewPoolLaunchTicketAcceptsCanonicalMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	reference := helper.TicketReference(strings.Repeat("b", 64))
	for _, operation := range []helper.Operation{
		helper.OperationEnsureLoopbackPool,
		helper.OperationReleaseLoopbackPool,
	} {
		t.Run(string(operation), func(t *testing.T) {
			ticket, err := NewPoolLaunchTicket(
				domain.OperationID("operation-pool"),
				reference,
				operation,
				"127.77.0.8/29",
				now.Add(time.Minute),
			)
			if err != nil {
				t.Fatalf("NewPoolLaunchTicket() error = %v", err)
			}
			if ticket.operationID != "operation-pool" || ticket.reference != reference {
				t.Fatalf("pool ticket identity = %#v", ticket)
			}
			if ticket.operation != operation ||
				ticket.pool != netip.MustParsePrefix("127.77.0.8/29") ||
				!ticket.expiresAt.Equal(now.Add(time.Minute)) {
				t.Fatalf("pool ticket effect = %#v", ticket)
			}
		})
	}
}

// TestNewPoolLaunchTicketRejectsInvalidMetadata covers every aggregate constructor trust boundary.
func TestNewPoolLaunchTicketRejectsInvalidMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	type input struct {
		operationID domain.OperationID
		reference   helper.TicketReference
		operation   helper.Operation
		pool        string
		expiresAt   time.Time
	}
	valid := input{
		operationID: "operation-pool",
		reference:   helper.TicketReference(strings.Repeat("b", 64)),
		operation:   helper.OperationEnsureLoopbackPool,
		pool:        "127.77.0.8/29",
		expiresAt:   now.Add(time.Minute),
	}
	tests := []struct {
		name   string
		mutate func(*input)
	}{
		{name: "operation ID", mutate: func(value *input) { value.operationID = " bad " }},
		{name: "reference", mutate: func(value *input) { value.reference = "short" }},
		{name: "scalar helper operation", mutate: func(value *input) { value.operation = helper.OperationEnsureLoopbackIdentity }},
		{name: "malformed pool", mutate: func(value *input) { value.pool = "not-a-prefix" }},
		{name: "non-loopback pool", mutate: func(value *input) { value.pool = "192.0.2.8/29" }},
		{name: "IPv6 loopback pool", mutate: func(value *input) { value.pool = "::/125" }},
		{name: "mapped IPv4 pool", mutate: func(value *input) { value.pool = "::ffff:127.77.0.8/125" }},
		{name: "broad pool", mutate: func(value *input) { value.pool = "127.77.0.0/24" }},
		{name: "narrow pool", mutate: func(value *input) { value.pool = "127.77.0.8/30" }},
		{name: "host bits", mutate: func(value *input) { value.pool = "127.77.0.9/29" }},
		{name: "zero expiry", mutate: func(value *input) { value.expiresAt = time.Time{} }},
		{name: "non-UTC expiry", mutate: func(value *input) {
			value.expiresAt = now.In(time.FixedZone("test", 0)).Add(time.Minute)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			ticket, err := NewPoolLaunchTicket(
				candidate.operationID,
				candidate.reference,
				candidate.operation,
				candidate.pool,
				candidate.expiresAt,
			)
			if err == nil {
				t.Fatalf("NewPoolLaunchTicket() = %#v, nil", ticket)
			}
			if ticket != (PoolLaunchTicket{}) {
				t.Fatalf("NewPoolLaunchTicket() ticket = %#v, want zero value", ticket)
			}
			if strings.Contains(err.Error(), string(candidate.reference)) {
				t.Fatal("constructor error exposed the opaque ticket reference")
			}
		})
	}
}

// TestPoolLaunchTicketValidateAtEnforcesProtocolLifetime verifies trusted-clock checks at both expiry bounds.
func TestPoolLaunchTicketValidateAtEnforcesProtocolLifetime(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	ticket, err := NewPoolLaunchTicket(
		"operation-pool",
		helper.TicketReference(strings.Repeat("b", 64)),
		helper.OperationEnsureLoopbackPool,
		"127.77.0.8/29",
		now.Add(helper.MaxTicketLifetime),
	)
	if err != nil {
		t.Fatalf("NewPoolLaunchTicket() error = %v", err)
	}
	if err := ticket.validateAt(now); err != nil {
		t.Fatalf("validateAt(boundary) error = %v", err)
	}

	tests := []struct {
		name      string
		expiresAt time.Time
	}{
		{name: "expired", expiresAt: now},
		{name: "excessive", expiresAt: now.Add(helper.MaxTicketLifetime + time.Nanosecond)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := ticket
			candidate.expiresAt = test.expiresAt
			if err := candidate.validateAt(now); err == nil {
				t.Fatal("validateAt() error = nil")
			}
		})
	}
}

// validLaunchLeaseKey returns one canonical primary lease identity for constructor tests.
func validLaunchLeaseKey(t *testing.T) identity.LeaseKey {
	t.Helper()
	leaseKey, err := identity.NewPrimaryKey(domain.ProjectID("project-1"))
	if err != nil {
		t.Fatalf("create lease key: %v", err)
	}
	return leaseKey
}
