package loopbackhandler

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// conditionalAdapter is the exact observation and mutation surface allowed inside the privileged handler.
type conditionalAdapter interface {
	Observe(context.Context, netip.Addr) (loopback.Observation, error)
	EnsureIfObserved(context.Context, netip.Addr, string) (loopback.Change, error)
	ReleaseIfObserved(context.Context, netip.Addr, string) (loopback.Change, error)
}

// Handler turns one admitted helper ticket into an exact observation-bound loopback effect.
type Handler struct {
	adapter conditionalAdapter
}

var _ helper.LoopbackIdentityHandler = (*Handler)(nil)

// New creates a handler backed by the current operating system's reviewed loopback adapter.
func New() *Handler {
	return newHandler(loopback.New())
}

// newHandler keeps host effects replaceable in tests without exposing adapter selection to helper requests.
func newHandler(adapter conditionalAdapter) *Handler {
	return &Handler{adapter: adapter}
}

// EnsureLoopbackIdentity ensures only the approved address whose exact precondition still holds.
func (handler *Handler) EnsureLoopbackIdentity(ctx context.Context, ticket helper.Ticket) (helper.MutationEvidence, error) {
	address, err := validateMutationTicket(ticket, helper.OperationEnsureLoopbackIdentity)
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	if _, err := handler.observeExpected(ctx, address, ticket.ExpectedObservation); err != nil {
		return helper.MutationEvidence{}, err
	}

	change, err := handler.adapter.EnsureIfObserved(ctx, address, ticket.ExpectedObservation.Fingerprint)
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, address, change)
}

// ReleaseLoopbackIdentity releases only the approved exact assignment whose ownership precondition still holds.
func (handler *Handler) ReleaseLoopbackIdentity(ctx context.Context, ticket helper.Ticket) (helper.MutationEvidence, error) {
	address, err := validateMutationTicket(ticket, helper.OperationReleaseLoopbackIdentity)
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	if ticket.ExpectedObservation.State != helper.ObservationOwned {
		return helper.MutationEvidence{}, fmt.Errorf("release loopback identity requires an owned precondition")
	}
	if _, err := handler.observeExpected(ctx, address, ticket.ExpectedObservation); err != nil {
		return helper.MutationEvidence{}, err
	}

	change, err := handler.adapter.ReleaseIfObserved(ctx, address, ticket.ExpectedObservation.Fingerprint)
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, address, change)
}

// observeExpected proves the signed state label and fingerprint describe the same fresh platform observation.
func (handler *Handler) observeExpected(ctx context.Context, address netip.Addr, expected helper.ExpectedObservation) (loopback.Observation, error) {
	observation, err := handler.adapter.Observe(ctx, address)
	if err != nil {
		return loopback.Observation{}, err
	}
	wantState := loopback.StateExact
	if expected.State == helper.ObservationAbsent {
		wantState = loopback.StateAbsent
	}
	if observation.State != wantState {
		return observation, fmt.Errorf("loopback precondition state changed before mutation")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return observation, err
	}
	if fingerprint != expected.Fingerprint {
		return observation, fmt.Errorf("loopback precondition fingerprint changed before mutation")
	}
	return observation, nil
}

// validateMutationTicket confines direct handler use to one canonical address inside the signed pool.
func validateMutationTicket(ticket helper.Ticket, operation helper.Operation) (netip.Addr, error) {
	if ticket.Operation != operation {
		return netip.Addr{}, fmt.Errorf("loopback ticket operation %q does not match handler operation %q", ticket.Operation, operation)
	}
	if err := ticket.ExpectedObservation.Validate(); err != nil {
		return netip.Addr{}, err
	}
	address, err := netip.ParseAddr(ticket.ApprovedAddress)
	if err != nil || !address.Is4() || !address.IsLoopback() || address != address.Unmap() || address.String() != ticket.ApprovedAddress {
		return netip.Addr{}, fmt.Errorf("loopback ticket address is not canonical IPv4 loopback")
	}
	pool, err := netip.ParsePrefix(ticket.ApprovedPool)
	if err != nil || !pool.Addr().Is4() || !pool.Addr().IsLoopback() || pool.Bits() < 8 || pool != pool.Masked() || pool.String() != ticket.ApprovedPool {
		return netip.Addr{}, fmt.Errorf("loopback ticket pool is not canonical IPv4 loopback")
	}
	if !pool.Contains(address) {
		return netip.Addr{}, fmt.Errorf("loopback ticket address is outside the approved pool")
	}
	return address, nil
}

// evidenceFromChange converts only the operation's required verified postcondition into the helper protocol.
func evidenceFromChange(operation helper.Operation, address netip.Addr, change loopback.Change) (helper.MutationEvidence, error) {
	wantState := loopback.StateExact
	evidenceState := helper.ObservationOwned
	if operation == helper.OperationReleaseLoopbackIdentity {
		wantState = loopback.StateAbsent
		evidenceState = helper.ObservationAbsent
	}
	if change.Indeterminate || change.After.State != wantState || change.After.Address != address {
		return helper.MutationEvidence{}, fmt.Errorf("loopback mutation did not produce its required postcondition")
	}
	fingerprint, err := change.After.Fingerprint()
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	return helper.MutationEvidence{
		Changed: change.Changed,
		Address: address.String(),
		Observation: helper.ExpectedObservation{
			State:       evidenceState,
			Fingerprint: fingerprint,
		},
	}, nil
}
