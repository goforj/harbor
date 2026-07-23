package ownershiphandler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// Store is the narrow protected ownership authority required for one release.
type Store interface {
	// Observe reads the current protected ownership record.
	Observe(context.Context) (ownership.Observation, error)
	// Release atomically removes only the record matching the supplied fingerprint.
	Release(context.Context, string) error
	// Close releases the retained protected ownership path handle.
	Close() error
}

// proofWriter persists one root-authored ownership release proof around the protected mutation.
type proofWriter interface {
	Complete(context.Context, ownershipreleaseproof.Request, ownershipreleaseproof.Transaction, time.Time) (ownershipreleaseproof.Proof, error)
}

// Handler releases one exact admitted schema-two ownership record.
type Handler struct {
	store Store
	proof proofWriter
	clock helper.Clock
}

// OpenDefault opens Harbor's compiled ownership and root-authored proof authorities.
func OpenDefault() (*Handler, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve helper ownership paths: %w", err)
	}
	store, err := ownership.NewStore(paths.OwnershipPath)
	if err != nil {
		return nil, fmt.Errorf("open helper ownership store: %w", err)
	}
	proof, err := ownershipreleaseproof.NewDefaultRootWriter()
	if err != nil {
		if closeErr := store.Close(); closeErr != nil {
			return nil, fmt.Errorf("open helper ownership release proof writer: %w", errors.Join(err, closeErr))
		}
		return nil, fmt.Errorf("open helper ownership release proof writer: %w", err)
	}
	return New(store, proof, helper.SystemClock{}), nil
}

// New constructs a handler from already-opened protected ownership and proof authorities.
func New(store Store, proof proofWriter, clock helper.Clock) *Handler {
	if store == nil {
		panic("ownershiphandler.New requires a non-nil store")
	}
	if proof == nil {
		panic("ownershiphandler.New requires a non-nil proof writer")
	}
	if clock == nil {
		panic("ownershiphandler.New requires a non-nil clock")
	}
	return &Handler{store: store, proof: proof, clock: clock}
}

// Close releases the protected ownership store without changing ownership.
func (handler *Handler) Close() error {
	return handler.store.Close()
}

// ReleaseNetworkOwnership removes the exact current target, or proves a mutation-free already-released replay.
func (handler *Handler) ReleaseNetworkOwnership(ctx context.Context, ticket helper.Ticket, admission helper.TicketAdmission) (helper.OwnershipMutationEvidence, error) {
	if admission.TargetOwnershipFingerprint != ticket.ExpectedOwnershipFingerprint {
		return helper.OwnershipMutationEvidence{}, errors.New("ownership release admission target does not match the signed target")
	}
	evidence := helper.OwnershipMutationEvidence{
		ReleaseOperationID:           ticket.ReleaseOperationID,
		ReleaseOperationRevision:     ticket.ReleaseOperationRevision,
		ReleaseCheckpointRevision:    ticket.ReleaseCheckpointRevision,
		ReleasedOwnershipFingerprint: ticket.ExpectedOwnershipFingerprint,
		Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
	}
	request := ownershipreleaseproof.Request{
		TicketReferenceHash:        hash(string(admission.TicketReference)),
		NonceHash:                  hash(ticket.Nonce),
		ReleaseOperationID:         ticket.ReleaseOperationID,
		OperationRevision:          ticket.ReleaseOperationRevision,
		CheckpointRevision:         ticket.ReleaseCheckpointRevision,
		RequesterIdentity:          ticket.RequesterIdentity,
		TargetOwnershipFingerprint: ticket.ExpectedOwnershipFingerprint,
	}
	transaction := ownershipreleaseproof.Transaction{
		ObserveOwnership: handler.observeAbsent,
	}
	switch admission.OwnershipState {
	case helper.OwnershipAdmissionAlreadyReleased:
		transaction.CompareAndSwap = func(context.Context) error {
			return errors.New("released ownership admission must not release ownership")
		}
	case helper.OwnershipAdmissionAlreadyCurrent:
		transaction.CompareAndSwap = func(ctx context.Context) error {
			return handler.store.Release(ctx, ticket.ExpectedOwnershipFingerprint)
		}
	default:
		return helper.OwnershipMutationEvidence{}, errors.New("ownership release admission state is unsupported")
	}
	if _, err := handler.proof.Complete(ctx, request, transaction, handler.clock.Now()); err != nil {
		return helper.OwnershipMutationEvidence{}, fmt.Errorf("complete ownership release proof: %w", err)
	}
	return evidence, nil
}

// observeAbsent returns whether the protected ownership record is currently present.
func (handler *Handler) observeAbsent(ctx context.Context) (bool, error) {
	observation, err := handler.store.Observe(ctx)
	if err != nil {
		return false, err
	}
	return observation.Exists, nil
}

// hash returns the canonical SHA-256 digest used to avoid retaining ticket secrets in durable proof storage.
func hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
