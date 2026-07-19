package loopbackhandler

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// conditionalAdapter is the exact observation and mutation surface allowed inside the privileged handler.
type conditionalAdapter interface {
	Observe(context.Context, netip.Addr) (loopback.Observation, error)
	EnsureIfObserved(context.Context, netip.Addr, string) (loopback.Change, error)
	ReleaseIfObserved(context.Context, netip.Addr, string) (loopback.Change, error)
}

// preAssignmentObserver captures the native route, socket, and policy facts immediately before an absent-state ensure.
type preAssignmentObserver func(context.Context, hostconflict.Request, string) (hostconflict.Observation, error)

// Handler turns one admitted helper ticket into an exact observation-bound loopback effect.
type Handler struct {
	adapter              conditionalAdapter
	observePreAssignment preAssignmentObserver
}

var _ helper.LoopbackIdentityHandler = (*Handler)(nil)

// New creates a handler backed by the current operating system's reviewed loopback adapter.
func New() *Handler {
	return newHandler(loopback.New(), observePlatformPreAssignment)
}

// newHandler keeps host effects replaceable in tests without exposing adapter selection to helper requests.
func newHandler(adapter conditionalAdapter, observePreAssignment preAssignmentObserver) *Handler {
	return &Handler{adapter: adapter, observePreAssignment: observePreAssignment}
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
	if ticket.ExpectedObservation.State == helper.ObservationAbsent {
		if err := handler.observeExpectedPreAssignment(ctx, address, ticket); err != nil {
			return helper.MutationEvidence{}, err
		}
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
	if _, err := handler.observeExpected(ctx, address, ticket.ExpectedObservation); err != nil {
		return helper.MutationEvidence{}, err
	}

	change, err := handler.adapter.ReleaseIfObserved(ctx, address, ticket.ExpectedObservation.Fingerprint)
	if err != nil {
		return helper.MutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, address, change)
}

// observeExpectedPreAssignment proves the signed native safety snapshot is still safe immediately before assignment.
func (handler *Handler) observeExpectedPreAssignment(ctx context.Context, address netip.Addr, ticket helper.Ticket) error {
	expected := ticket.ExpectedPreAssignment
	if expected == nil {
		return fmt.Errorf("absent ensure requires an expected pre-assignment observation")
	}
	request, err := newPreAssignmentRequest(address, *expected)
	if err != nil {
		return err
	}
	observation, err := handler.observePreAssignment(ctx, request, ticket.RequesterIdentity)
	if err != nil {
		return err
	}
	assessment, err := observation.Classify()
	if err != nil {
		return fmt.Errorf("classify pre-assignment host conflicts: %w", err)
	}
	if assessment.State != hostconflict.StateSafe {
		return fmt.Errorf("pre-assignment host conflict state is %q", assessment.State)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint pre-assignment host conflicts: %w", err)
	}
	if fingerprint != expected.Fingerprint {
		return fmt.Errorf("pre-assignment host conflict fingerprint changed before mutation")
	}
	return nil
}

// newPreAssignmentRequest converts only the signed helper vocabulary into the native observer request.
func newPreAssignmentRequest(address netip.Addr, expected helper.ExpectedPreAssignment) (hostconflict.Request, error) {
	if err := expected.Validate(); err != nil {
		return hostconflict.Request{}, err
	}
	requirements := make([]hostconflict.SocketRequirement, len(expected.Requirements))
	for index, requirement := range expected.Requirements {
		var transport hostconflict.Transport
		switch requirement.Transport {
		case helper.SocketTransportTCP4:
			transport = hostconflict.TransportTCP4
		case helper.SocketTransportUDP4:
			transport = hostconflict.TransportUDP4
		default:
			return hostconflict.Request{}, fmt.Errorf("pre-assignment socket transport %q is unsupported", requirement.Transport)
		}
		requirements[index] = hostconflict.SocketRequirement{Transport: transport, Port: requirement.Port}
	}
	request, err := hostconflict.NewPreAssignmentRequest(address, requirements)
	if err != nil {
		return hostconflict.Request{}, fmt.Errorf("construct pre-assignment host conflict request: %w", err)
	}
	return request, nil
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
	if operation == helper.OperationEnsureLoopbackIdentity && ticket.ExpectedObservation.State == helper.ObservationAbsent {
		if ticket.ExpectedPreAssignment == nil {
			return netip.Addr{}, fmt.Errorf("absent ensure requires an expected pre-assignment observation")
		}
		if err := ticket.ExpectedPreAssignment.Validate(); err != nil {
			return netip.Addr{}, err
		}
	} else if ticket.ExpectedPreAssignment != nil {
		return netip.Addr{}, fmt.Errorf("expected pre-assignment observation is not allowed for this operation")
	}
	if operation == helper.OperationReleaseLoopbackIdentity && ticket.ExpectedObservation.State != helper.ObservationOwned {
		return netip.Addr{}, fmt.Errorf("release loopback identity requires an owned precondition")
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
