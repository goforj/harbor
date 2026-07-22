package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/state"
)

const (
	networkDataPlaneSetupTrustApprovalPhase   = "awaiting trust approval"
	networkDataPlaneSetupTrustConfirmPhase    = "verifying trust"
	networkDataPlaneSetupLowPortApprovalPhase = "awaiting low-port approval"
	networkDataPlaneSetupActivationPhase      = "activating trusted ingress"
	networkDataPlaneSetupCompletedPhase       = "completed"
)

// NetworkDataPlaneSetupStartRequest identifies one idempotent global trusted-ingress intent.
type NetworkDataPlaneSetupStartRequest struct {
	OperationID       domain.OperationID
	IntentID          domain.IntentID
	RequesterIdentity string
}

// Validate rejects identities that cannot select one machine-owner data-plane operation.
func (request NetworkDataPlaneSetupStartRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := request.IntentID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkDataPlaneSetupPrepareTrustRequest selects one exact public-root approval revision.
type NetworkDataPlaneSetupPrepareTrustRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects stale-shaped trust approval input before helper authority opens.
func (request NetworkDataPlaneSetupPrepareTrustRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkDataPlaneSetupConfirmTrustRequest carries one correlated helper trust postcondition.
type NetworkDataPlaneSetupConfirmTrustRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	TrustEvidence             helper.TrustMutationEvidence
}

// Validate rejects trust confirmation that cannot be bound to one owner and approval revision.
func (request NetworkDataPlaneSetupConfirmTrustRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	if err := validateNetworkSetupRequesterIdentity(request.RequesterIdentity); err != nil {
		return err
	}
	return validateNetworkDataPlaneSetupTrustEvidence(request.TrustEvidence)
}

// NetworkDataPlaneSetupPrepareLowPortsRequest selects one exact low-port approval revision.
type NetworkDataPlaneSetupPrepareLowPortsRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects stale-shaped low-port approval input before helper authority opens.
func (request NetworkDataPlaneSetupPrepareLowPortsRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupRequesterIdentity(request.RequesterIdentity)
}

// NetworkDataPlaneSetupConfirmLowPortsRequest carries one correlated paired-listener postcondition.
type NetworkDataPlaneSetupConfirmLowPortsRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
	LowPortEvidence           helper.LowPortMutationEvidence
}

// Validate rejects low-port confirmation outside one exact owner and approval revision.
func (request NetworkDataPlaneSetupConfirmLowPortsRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	if err := validateNetworkSetupRequesterIdentity(request.RequesterIdentity); err != nil {
		return err
	}
	return validateNetworkDataPlaneSetupLowPortEvidence(request.LowPortEvidence)
}

// NetworkDataPlaneSetupResult couples runtime-ready terminal operation state to its full durable network.
type NetworkDataPlaneSetupResult struct {
	Operation state.OperationRecord
	Network   state.NetworkMutationResult
}

// Validate rejects results that could claim completion without a full durable data plane.
func (result NetworkDataPlaneSetupResult) Validate() error {
	if err := result.Operation.Operation.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup operation: %w", err)
	}
	if result.Operation.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup ||
		result.Operation.Operation.ProjectID != "" {
		return fmt.Errorf("network data-plane setup result operation is not global data-plane setup")
	}
	if result.Operation.Operation.State != domain.OperationSucceeded ||
		result.Operation.Operation.Phase != networkDataPlaneSetupCompletedPhase {
		return fmt.Errorf(
			"network data-plane setup result operation is %q/%q, want %q/%q",
			result.Operation.Operation.State,
			result.Operation.Operation.Phase,
			domain.OperationSucceeded,
			networkDataPlaneSetupCompletedPhase,
		)
	}
	if err := validateOperationRevision(result.Operation.Revision); err != nil {
		return fmt.Errorf("network data-plane setup operation revision: %w", err)
	}
	if err := result.Network.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup network: %w", err)
	}
	if result.Network.Record.Stage != state.NetworkStageFull {
		return fmt.Errorf("network data-plane setup network stage is %q, want %q", result.Network.Record.Stage, state.NetworkStageFull)
	}
	if result.Operation.Revision <= result.Network.Record.Revision {
		return fmt.Errorf("network data-plane setup terminal revision must follow its full network revision")
	}
	return nil
}

// validateNetworkDataPlaneSetupTrustEvidence admits only successful ensure postconditions with canonical digests.
func validateNetworkDataPlaneSetupTrustEvidence(evidence helper.TrustMutationEvidence) error {
	if !canonicalNetworkDataPlaneFingerprint(evidence.AuthorityFingerprint) {
		return fmt.Errorf("trust evidence authority fingerprint is invalid")
	}
	switch evidence.Mechanism {
	case networkpolicy.DarwinCurrentUserTrust,
		networkpolicy.UbuntuSystemTrust,
		networkpolicy.WindowsCurrentUserTrust:
	default:
		return fmt.Errorf("trust evidence mechanism %q is unsupported", evidence.Mechanism)
	}
	if !canonicalNetworkDataPlaneFingerprint(evidence.ObservationFingerprint) {
		return fmt.Errorf("trust evidence observation fingerprint is invalid")
	}
	if evidence.Postcondition != helper.TrustPostconditionExact &&
		evidence.Postcondition != helper.TrustPostconditionPreexisting {
		return fmt.Errorf("trust evidence must prove exact or identical preexisting trust")
	}
	return nil
}

// validateNetworkDataPlaneSetupLowPortEvidence admits only a complete exact ensure postcondition.
func validateNetworkDataPlaneSetupLowPortEvidence(evidence helper.LowPortMutationEvidence) error {
	if !canonicalNetworkDataPlaneFingerprint(evidence.PolicyFingerprint) {
		return fmt.Errorf("low-port evidence policy fingerprint is invalid")
	}
	if !canonicalNetworkDataPlaneFingerprint(evidence.OwnershipFingerprint) {
		return fmt.Errorf("low-port evidence ownership fingerprint is invalid")
	}
	if !canonicalNetworkDataPlaneFingerprint(evidence.ObservationFingerprint) {
		return fmt.Errorf("low-port evidence observation fingerprint is invalid")
	}
	if evidence.Postcondition != helper.LowPortPostconditionExact {
		return fmt.Errorf("low-port evidence must prove the exact paired listener service")
	}
	return nil
}

// canonicalNetworkDataPlaneFingerprint accepts only lowercase canonical SHA-256 text.
func canonicalNetworkDataPlaneFingerprint(value string) bool {
	if len(value) != sha256.Size*2 || strings.TrimSpace(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
