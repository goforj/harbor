package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"gorm.io/gorm"
)

const networkDataPlaneSetupPlanSingletonID = 1

type networkDataPlaneSetupPlanPhase string

const (
	networkDataPlaneSetupPlanLowPortApproval networkDataPlaneSetupPlanPhase = "low_port_approval"
	networkDataPlaneSetupPlanActivation      networkDataPlaneSetupPlanPhase = "activation"
)

// networkDataPlaneSetupPlanRow is the private persistence shape for sanitized crash-recovery authority.
type networkDataPlaneSetupPlanRow struct {
	ID                    int       `gorm:"column:id"`
	OperationID           string    `gorm:"column:operation_id"`
	OperationRevision     int       `gorm:"column:operation_revision"`
	Phase                 string    `gorm:"column:phase"`
	NetworkStateID        int       `gorm:"column:network_state_id"`
	NetworkRevision       int       `gorm:"column:network_revision"`
	NetworkUpdatedAt      time.Time `gorm:"column:network_updated_at"`
	AuthorityPayload      string    `gorm:"column:authority_payload"`
	AuthorityDigest       string    `gorm:"column:authority_digest"`
	TrustEvidenceDigest   string    `gorm:"column:trust_evidence_digest"`
	TrustVerifiedAt       time.Time `gorm:"column:trust_verified_at"`
	LowPortEvidenceDigest *string   `gorm:"column:low_port_evidence_digest"`
	ActivationPayload     *string   `gorm:"column:activation_payload"`
	ActivationDigest      *string   `gorm:"column:activation_digest"`
}

// TableName returns the fixed transient-authority table name.
func (networkDataPlaneSetupPlanRow) TableName() string {
	return "network_data_plane_setup_plans"
}

// networkDataPlaneSetupAuthority is the exact resolver predecessor and policy approved before helper work.
type networkDataPlaneSetupAuthority struct {
	Projection NetworkDataPlaneSetupProjection `json:"projection"`
	Policy     networkpolicy.Policy            `json:"policy"`
}

// Validate rejects authority that cannot own one resolver-to-full transition.
func (authority networkDataPlaneSetupAuthority) Validate() error {
	if authority.Projection.Stage != NetworkStageResolver {
		return fmt.Errorf("network data-plane setup authority requires %q projection", NetworkStageResolver)
	}
	if err := authority.Projection.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup authority projection: %w", err)
	}
	if err := authority.Policy.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup authority policy: %w", err)
	}
	policyFingerprint, err := authority.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("network data-plane setup authority policy fingerprint: %w", err)
	}
	if policyFingerprint != authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint {
		return fmt.Errorf("network data-plane setup authority policy does not match confirmed ownership")
	}
	return nil
}

// networkDataPlaneSetupPlan is the validated in-memory form of one durable lifecycle receipt.
type networkDataPlaneSetupPlan struct {
	Operation             OperationRecord
	Phase                 networkDataPlaneSetupPlanPhase
	Authority             networkDataPlaneSetupAuthority
	TrustEvidenceDigest   string
	TrustVerifiedAt       time.Time
	LowPortEvidenceDigest string
	Activation            *ActivateNetworkDataPlaneRequest
}

// readOptionalNetworkDataPlaneSetupPlan reads the singleton without filtering away a foreign owner.
func readOptionalNetworkDataPlaneSetupPlan(
	tx *gorm.DB,
	operationID domain.OperationID,
) (networkDataPlaneSetupPlanRow, bool, error) {
	var rows []networkDataPlaneSetupPlanRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return networkDataPlaneSetupPlanRow{}, false, fmt.Errorf("read network data-plane setup plan: %w", err)
	}
	if len(rows) == 0 {
		return networkDataPlaneSetupPlanRow{}, false, nil
	}
	if len(rows) != 1 {
		return networkDataPlaneSetupPlanRow{}, false, corruptNetworkDataPlaneSetupPlan(
			operationID,
			fmt.Errorf("singleton contains %d rows, expected 1", len(rows)),
		)
	}
	if rows[0].OperationID != string(operationID) {
		return networkDataPlaneSetupPlanRow{}, false, corruptNetworkDataPlaneSetupPlan(
			operationID,
			fmt.Errorf("singleton belongs to operation %q", rows[0].OperationID),
		)
	}
	return rows[0], true, nil
}

// networkDataPlaneSetupPlanFromRow validates every durable field before exposing recovery authority.
func networkDataPlaneSetupPlanFromRow(
	row networkDataPlaneSetupPlanRow,
	operation OperationRecord,
) (networkDataPlaneSetupPlan, error) {
	operationID := operation.Operation.ID
	if row.ID != networkDataPlaneSetupPlanSingletonID {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("singleton ID is %d, expected 1", row.ID))
	}
	if row.OperationID != string(operationID) {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("operation ID does not match its owner"))
	}
	operationRevision, err := modelIntToSequence("network data-plane setup plan operation revision", row.OperationRevision)
	if err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if operationRevision != operation.Revision {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(
			operationID,
			fmt.Errorf("plan revision is %d, operation revision is %d", operationRevision, operation.Revision),
		)
	}
	if row.NetworkStateID != networkStateSingletonID {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("network state ID is %d, expected 1", row.NetworkStateID))
	}
	networkRevision, err := modelIntToSequence("network data-plane setup plan network revision", row.NetworkRevision)
	if err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if err := validateStoredTime("network data-plane setup plan network update time", row.NetworkUpdatedAt); err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if err := validateNetworkDataPlaneSetupDigest("authority digest", row.AuthorityDigest); err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if digestNetworkDataPlaneSetupPayload(row.AuthorityPayload) != row.AuthorityDigest {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("authority payload digest does not match"))
	}
	authority, err := decodeNetworkDataPlaneSetupAuthority(row.AuthorityPayload)
	if err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if authority.Projection.NetworkRevision != networkRevision ||
		!authority.Projection.NetworkUpdatedAt.Equal(row.NetworkUpdatedAt) {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("authority projection differs from the indexed network boundary"))
	}
	if err := validateNetworkDataPlaneSetupDigest("trust evidence digest", row.TrustEvidenceDigest); err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if err := validateStoredTime("network data-plane setup trust verification time", row.TrustVerifiedAt); err != nil {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
	}
	if row.TrustVerifiedAt.Before(authority.Projection.NetworkUpdatedAt) {
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("trust verification time precedes the source network boundary"))
	}

	plan := networkDataPlaneSetupPlan{
		Operation:           operation,
		Phase:               networkDataPlaneSetupPlanPhase(row.Phase),
		Authority:           authority,
		TrustEvidenceDigest: row.TrustEvidenceDigest,
		TrustVerifiedAt:     row.TrustVerifiedAt,
	}
	switch plan.Phase {
	case networkDataPlaneSetupPlanLowPortApproval:
		if row.LowPortEvidenceDigest != nil || row.ActivationPayload != nil || row.ActivationDigest != nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("low-port approval plan contains activation authority"))
		}
	case networkDataPlaneSetupPlanActivation:
		if row.LowPortEvidenceDigest == nil || row.ActivationPayload == nil || row.ActivationDigest == nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("activation plan is incomplete"))
		}
		if err := validateNetworkDataPlaneSetupDigest("low-port evidence digest", *row.LowPortEvidenceDigest); err != nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		if err := validateNetworkDataPlaneSetupDigest("activation digest", *row.ActivationDigest); err != nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		if digestNetworkDataPlaneSetupPayload(*row.ActivationPayload) != *row.ActivationDigest {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("activation payload digest does not match"))
		}
		activation, decodeErr := decodeNetworkDataPlaneSetupActivation(*row.ActivationPayload)
		if decodeErr != nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, decodeErr)
		}
		if err := requireNetworkDataPlaneSetupActivationMatchesAuthority(activation, authority, row.TrustVerifiedAt); err != nil {
			return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, err)
		}
		plan.LowPortEvidenceDigest = *row.LowPortEvidenceDigest
		plan.Activation = &activation
	default:
		return networkDataPlaneSetupPlan{}, corruptNetworkDataPlaneSetupPlan(operationID, fmt.Errorf("phase %q is unsupported", row.Phase))
	}
	return plan, nil
}

// encodeNetworkDataPlaneSetupAuthority returns deterministic sanitized authority and its integrity digest.
func encodeNetworkDataPlaneSetupAuthority(
	projection NetworkDataPlaneSetupProjection,
	policy networkpolicy.Policy,
) (string, string, error) {
	authority := networkDataPlaneSetupAuthority{Projection: projection, Policy: policy}
	if err := authority.Validate(); err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(authority)
	if err != nil {
		return "", "", fmt.Errorf("encode network data-plane setup authority: %w", err)
	}
	encoded := string(payload)
	return encoded, digestNetworkDataPlaneSetupPayload(encoded), nil
}

// decodeNetworkDataPlaneSetupAuthority strictly restores one sanitized resolver authority payload.
func decodeNetworkDataPlaneSetupAuthority(payload string) (networkDataPlaneSetupAuthority, error) {
	var authority networkDataPlaneSetupAuthority
	if err := decodeNetworkDataPlaneSetupPayload(payload, &authority); err != nil {
		return networkDataPlaneSetupAuthority{}, fmt.Errorf("decode network data-plane setup authority: %w", err)
	}
	if err := authority.Validate(); err != nil {
		return networkDataPlaneSetupAuthority{}, err
	}
	return authority, nil
}

// encodeNetworkDataPlaneSetupActivation returns canonical replay facts and their integrity digest.
func encodeNetworkDataPlaneSetupActivation(request ActivateNetworkDataPlaneRequest) (string, string, error) {
	request = cloneActivateNetworkDataPlaneRequest(request)
	if err := request.Validate(); err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", "", fmt.Errorf("encode network data-plane setup activation: %w", err)
	}
	encoded := string(payload)
	return encoded, digestNetworkDataPlaneSetupPayload(encoded), nil
}

// decodeNetworkDataPlaneSetupActivation strictly restores one sanitized activation request.
func decodeNetworkDataPlaneSetupActivation(payload string) (ActivateNetworkDataPlaneRequest, error) {
	var request ActivateNetworkDataPlaneRequest
	if err := decodeNetworkDataPlaneSetupPayload(payload, &request); err != nil {
		return ActivateNetworkDataPlaneRequest{}, fmt.Errorf("decode network data-plane setup activation: %w", err)
	}
	request = cloneActivateNetworkDataPlaneRequest(request)
	if err := request.Validate(); err != nil {
		return ActivateNetworkDataPlaneRequest{}, fmt.Errorf("validate network data-plane setup activation: %w", err)
	}
	return request, nil
}

// decodeNetworkDataPlaneSetupPayload rejects unknown fields and trailing values in durable authority blobs.
func decodeNetworkDataPlaneSetupPayload(payload string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("payload contains a trailing JSON value")
		}
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if string(canonical) != payload {
		return fmt.Errorf("payload is not canonical JSON")
	}
	return nil
}

// requireNetworkDataPlaneSetupActivationMatchesAuthority binds every activation fact to its trust-approved source.
func requireNetworkDataPlaneSetupActivationMatchesAuthority(
	activation ActivateNetworkDataPlaneRequest,
	authority networkDataPlaneSetupAuthority,
	trustVerifiedAt time.Time,
) error {
	if activation.ExpectedNetworkRevision != authority.Projection.NetworkRevision {
		return fmt.Errorf("activation network revision differs from trust-approved authority")
	}
	if activation.ConfirmedOwnership != authority.Projection.ConfirmedOwnership {
		return fmt.Errorf("activation ownership differs from trust-approved authority")
	}
	if activation.Policy != authority.Policy {
		return fmt.Errorf("activation policy differs from trust-approved authority")
	}
	if len(activation.Setup) != 2 || !sameNetworkDataPlaneSetupResolverProof(
		activation.Setup[0],
		authority.Projection.ResolverProof,
	) {
		return fmt.Errorf("activation resolver proof differs from trust-approved authority")
	}
	if activation.At.Before(trustVerifiedAt) {
		return fmt.Errorf("activation time precedes trust verification")
	}
	return nil
}

// validateNetworkDataPlaneSetupDigest accepts only one lowercase SHA-256 digest.
func validateNetworkDataPlaneSetupDigest(name string, value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("network data-plane setup %s must contain 64 lowercase hexadecimal characters", name)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("network data-plane setup %s must contain 64 lowercase hexadecimal characters", name)
	}
	return nil
}

// digestNetworkDataPlaneSetupPayload fingerprints one exact canonical JSON payload.
func digestNetworkDataPlaneSetupPayload(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// NetworkDataPlaneSetupEvidenceDigest returns the canonical digest retained for a helper postcondition.
func NetworkDataPlaneSetupEvidenceDigest(evidence any) (string, error) {
	payload, err := json.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("encode network data-plane setup evidence: %w", err)
	}
	if len(payload) < 2 || len(payload) > 65536 {
		return "", fmt.Errorf("network data-plane setup evidence payload has invalid size")
	}
	return digestNetworkDataPlaneSetupPayload(string(payload)), nil
}

// corruptNetworkDataPlaneSetupPlan identifies operation-bound sanitized authority corruption.
func corruptNetworkDataPlaneSetupPlan(operationID domain.OperationID, cause error) error {
	return corruptStateError("network data-plane setup plan", string(operationID), cause)
}
