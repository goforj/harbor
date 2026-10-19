package loopbackhandler

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// ExpandLoopbackPool verifies the complete signed preconditions before widening protected ownership.
func (handler *Handler) ExpandLoopbackPool(ctx context.Context, ticket helper.Ticket, admission helper.TicketAdmission) (helper.PoolMutationEvidence, error) {
	return handler.expandLoopbackPool(ctx, ticket, admission, expandProtectedOwnership)
}

// expandLoopbackPool keeps the protected-store effect injectable without giving callers a configurable host path.
func (handler *Handler) expandLoopbackPool(ctx context.Context, ticket helper.Ticket, admission helper.TicketAdmission, expand func(context.Context, string, string, ownership.Record) (ownership.Observation, error)) (helper.PoolMutationEvidence, error) {
	plan, err := validatePoolMutationTicket(ticket, helper.OperationEnsureLoopbackPool)
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	previous, err := netip.ParsePrefix(ticket.PreviousPool)
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	target := ownership.Record{SchemaVersion: ticket.OwnershipSchemaVersion, InstallationID: ticket.InstallationID, OwnerIdentity: ticket.RequesterIdentity, Generation: ticket.OwnershipGeneration, LoopbackPoolPrefix: ticket.ApprovedPool, NetworkPolicyFingerprint: ticket.NetworkPolicyFingerprint, TicketVerifierKey: admission.TicketVerifierKey}
	source := target
	source.LoopbackPoolPrefix = ticket.PreviousPool
	if err := ownership.ValidatePoolExpansion(source, target); err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	sourceFingerprint, err := source.Fingerprint()
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if ticket.ExpectedOwnershipFingerprint != sourceFingerprint || admission.TargetOwnershipFingerprint != targetFingerprint || admission.PostOwnershipFingerprint != targetFingerprint {
		return helper.PoolMutationEvidence{}, fmt.Errorf("pool expansion ownership fingerprint drift")
	}
	switch admission.OwnershipState {
	case helper.OwnershipAdmissionPoolExpansion:
		if admission.OwnershipFingerprint != sourceFingerprint {
			return helper.PoolMutationEvidence{}, fmt.Errorf("pool expansion source is not current")
		}
		for _, item := range plan.identities {
			if !previous.Contains(item.address) && item.expectedObservation.State != helper.ObservationAbsent {
				return helper.PoolMutationEvidence{}, fmt.Errorf("pool expansion cannot adopt an existing assignment at %s", item.address)
			}
		}
	case helper.OwnershipAdmissionAlreadyCurrent:
		if admission.OwnershipFingerprint != targetFingerprint {
			return helper.PoolMutationEvidence{}, fmt.Errorf("pool expansion target is not current")
		}
	default:
		return helper.PoolMutationEvidence{}, fmt.Errorf("pool expansion admission is unsupported")
	}
	for _, item := range plan.identities {
		if _, err := handler.observeExpected(ctx, item.address, item.expectedObservation); err != nil {
			return helper.PoolMutationEvidence{}, err
		}
		if item.expectedObservation.State == helper.ObservationAbsent {
			if err := handler.observeExpectedPreAssignment(ctx, item.address, &item.expectedPreAssignment, plan.requesterIdentity); err != nil {
				return helper.PoolMutationEvidence{}, err
			}
		}
	}
	changed, err := expand(ctx, ticket.PreviousPool, sourceFingerprint, target)
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if !changed.Exists || changed.Record != target || changed.Fingerprint != targetFingerprint {
		return helper.PoolMutationEvidence{}, fmt.Errorf("expanded protected ownership differs from approved target")
	}
	return handler.ensureLoopbackPool(ctx, ticket)
}

// expandProtectedOwnership opens only Harbor's fixed root-owned store after authenticated approval.
func expandProtectedOwnership(ctx context.Context, previousPool, fingerprint string, target ownership.Record) (observation ownership.Observation, err error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return observation, err
	}
	store, err := ownership.NewStore(paths.OwnershipPath)
	if err != nil {
		return observation, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil {
			err = closeErr
		}
	}()
	return store.Expand(ctx, previousPool, fingerprint, target)
}
