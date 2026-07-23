package ownershipreleaseproof

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/platform/machinepaths"
)

const (
	// StatePending identifies a persisted release boundary that has not yet received a durable absent-ownership confirmation.
	StatePending State = "pending"
	// StateReleased identifies terminal root-authored confirmation that ownership is absent.
	StateReleased State = "released"
)

var (
	// ErrAbsentProof identifies an ownership-absence claim without matching terminal release evidence.
	ErrAbsentProof = errors.New("ownership absence has no matching release proof")
	// ErrUnsafePath identifies an untrusted proof or lock storage path.
	ErrUnsafePath = errors.New("unsafe ownership release proof path")
	// ErrNotRoot identifies a write attempt outside the privileged helper boundary.
	ErrNotRoot = errors.New("ownership release proof writer requires root")
)

// State records the durable release lifecycle state.
type State string

// Proof is the complete root-authored evidence binding a release authority to its original admitted ticket reference.
type Proof struct {
	TicketReferenceHash        string    `json:"ticket_reference_hash"`
	NonceHash                  string    `json:"nonce_hash"`
	ReleaseOperationID         string    `json:"release_operation_id"`
	OperationRevision          uint64    `json:"operation_revision"`
	CheckpointRevision         uint64    `json:"checkpoint_revision"`
	RequesterIdentity          string    `json:"requester_identity"`
	TargetOwnershipFingerprint string    `json:"target_ownership_fingerprint"`
	State                      State     `json:"state"`
	VerifiedAt                 time.Time `json:"verified_at"`
}

// Authority identifies the daemon-owned release checkpoint independently from one helper ticket attempt.
type Authority struct {
	ReleaseOperationID         string
	OperationRevision          uint64
	CheckpointRevision         uint64
	RequesterIdentity          string
	TargetOwnershipFingerprint string
}

// Request identifies an authenticated release request and its exact ownership authority.
type Request struct {
	TicketReferenceHash        string
	NonceHash                  string
	ReleaseOperationID         string
	OperationRevision          uint64
	CheckpointRevision         uint64
	RequesterIdentity          string
	TargetOwnershipFingerprint string
}

// Authority returns the durable release boundary shared by reissued helper tickets.
func (request Request) Authority() Authority {
	return Authority{
		ReleaseOperationID:         request.ReleaseOperationID,
		OperationRevision:          request.OperationRevision,
		CheckpointRevision:         request.CheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
		TargetOwnershipFingerprint: request.TargetOwnershipFingerprint,
	}
}

// Transaction supplies the ownership operations held inside the proof lock.
type Transaction struct {
	CompareAndSwap   func(context.Context) error
	ObserveOwnership func(context.Context) (bool, error)
}

// NewDefaultRootWriter opens Harbor's compiled root-owned proof and lock paths.
func NewDefaultRootWriter() (*RootWriter, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve ownership release proof path: %w", err)
	}
	return newRootWriter(paths.OwnershipReleaseProofPath, paths.OwnershipReleaseProofLockPath)
}

// NewDefaultObserver opens Harbor's compiled root-owned proof path for daemon confirmation.
func NewDefaultObserver() (*Observer, error) {
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve ownership release proof path: %w", err)
	}
	return newObserver(paths.OwnershipReleaseProofPath)
}

// Complete holds the proof lock across pending persistence, exact ownership mutation, fresh absence observation, and released persistence.
func (writer *RootWriter) Complete(ctx context.Context, request Request, transaction Transaction, verifiedAt time.Time) (Proof, error) {
	return writer.complete(ctx, request, transaction, verifiedAt)
}

// Observe returns the complete root-authored proof for diagnostics; callers must use ConfirmReleased for terminal authority.
func (observer *Observer) Observe(ctx context.Context) (Proof, bool, error) {
	return observer.observe(ctx)
}

// ConfirmReleased returns only terminal root-authored proof for the exact durable release authority.
func (observer *Observer) ConfirmReleased(ctx context.Context, authority Authority) (Proof, error) {
	proof, exists, err := observer.observe(ctx)
	if err != nil {
		return Proof{}, err
	}
	if !exists || proof.State != StateReleased || !sameAuthority(proof, authority) {
		return Proof{}, ErrAbsentProof
	}
	return proof, nil
}

// AdmitReplay admits only pending or terminal root-authored evidence for one reissued ticket's durable authority.
func (observer *Observer) AdmitReplay(ctx context.Context, authority Authority) (Proof, error) {
	proof, exists, err := observer.observe(ctx)
	if err != nil {
		return Proof{}, err
	}
	if !exists ||
		(proof.State != StatePending && proof.State != StateReleased) ||
		!sameAuthority(proof, authority) {
		return Proof{}, ErrAbsentProof
	}
	return proof, nil
}

// Fingerprint returns the SHA-256 digest of the canonical persisted proof representation.
func (proof Proof) Fingerprint() (string, error) {
	if err := validateProof(proof); err != nil {
		return "", err
	}
	encoded, err := encodeProof(proof)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// hash validates a lower-case SHA-256 hexadecimal field.
func hash(name, value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, sha256.Size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, sha256.Size*2)
	}
	return nil
}

// validateProof rejects incomplete, non-terminal, and non-UTC evidence before it reaches durable storage.
func validateProof(proof Proof) error {
	if err := hash("ticket reference hash", proof.TicketReferenceHash); err != nil {
		return err
	}
	if err := hash("nonce hash", proof.NonceHash); err != nil {
		return err
	}
	if err := hash("target ownership fingerprint", proof.TargetOwnershipFingerprint); err != nil {
		return err
	}
	if proof.ReleaseOperationID == "" ||
		proof.RequesterIdentity == "" ||
		proof.OperationRevision == 0 ||
		proof.CheckpointRevision == 0 ||
		proof.CheckpointRevision <= proof.OperationRevision ||
		proof.VerifiedAt.IsZero() {
		return errors.New("ownership release proof has an empty required field")
	}
	if proof.VerifiedAt.Location() != time.UTC || proof.VerifiedAt != proof.VerifiedAt.UTC() {
		return errors.New("ownership release proof verified time is not UTC")
	}
	if proof.State != StatePending && proof.State != StateReleased {
		return errors.New("ownership release proof has an unsupported state")
	}
	return nil
}

// sameAuthority compares only durable release authority so reissued tickets cannot replace original audit hashes.
func sameAuthority(proof Proof, authority Authority) bool {
	return proof.ReleaseOperationID == authority.ReleaseOperationID &&
		proof.OperationRevision == authority.OperationRevision &&
		proof.CheckpointRevision == authority.CheckpointRevision &&
		proof.RequesterIdentity == authority.RequesterIdentity &&
		proof.TargetOwnershipFingerprint == authority.TargetOwnershipFingerprint
}

// requestProof derives the initial pending record while preserving the reference and nonce that first crossed the root boundary.
func requestProof(request Request, verifiedAt time.Time) Proof {
	return Proof{
		TicketReferenceHash:        request.TicketReferenceHash,
		NonceHash:                  request.NonceHash,
		ReleaseOperationID:         request.ReleaseOperationID,
		OperationRevision:          request.OperationRevision,
		CheckpointRevision:         request.CheckpointRevision,
		RequesterIdentity:          request.RequesterIdentity,
		TargetOwnershipFingerprint: request.TargetOwnershipFingerprint,
		State:                      StatePending,
		VerifiedAt:                 verifiedAt.UTC(),
	}
}
