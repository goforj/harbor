package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/trust/certroot"
	"gorm.io/gorm"
)

const maximumGlobalNetworkReleaseAuthorityPayload = 65536

const globalNetworkReleaseRuntimeOperationPhase = "releasing network runtime"

// GlobalNetworkReleasePlanPhase identifies the ordered durable cleanup step.
type GlobalNetworkReleasePlanPhase string

const (
	// GlobalNetworkReleasePlanPhaseRuntimeRelease releases runtime-owned listeners first.
	GlobalNetworkReleasePlanPhaseRuntimeRelease GlobalNetworkReleasePlanPhase = "runtime_release"
	// GlobalNetworkReleasePlanPhaseLowPorts releases privileged low-port integration.
	GlobalNetworkReleasePlanPhaseLowPorts GlobalNetworkReleasePlanPhase = "low_ports"
	// GlobalNetworkReleasePlanPhaseResolver releases the Harbor resolver route.
	GlobalNetworkReleasePlanPhaseResolver GlobalNetworkReleasePlanPhase = "resolver"
	// GlobalNetworkReleasePlanPhaseTrust releases the public-root trust entry when owned.
	GlobalNetworkReleasePlanPhaseTrust GlobalNetworkReleasePlanPhase = "trust"
	// GlobalNetworkReleasePlanPhaseLoopbacks releases confirmed loopback targets.
	GlobalNetworkReleasePlanPhaseLoopbacks GlobalNetworkReleasePlanPhase = "loopbacks"
	// GlobalNetworkReleasePlanPhaseVerifyEffects verifies all host effects are absent.
	GlobalNetworkReleasePlanPhaseVerifyEffects GlobalNetworkReleasePlanPhase = "verify_effects"
	// GlobalNetworkReleasePlanPhaseOwnership releases the machine ownership record.
	GlobalNetworkReleasePlanPhaseOwnership GlobalNetworkReleasePlanPhase = "ownership"
	// GlobalNetworkReleasePlanPhaseProjection removes the durable network projection last.
	GlobalNetworkReleasePlanPhaseProjection GlobalNetworkReleasePlanPhase = "projection"
)

// Validate rejects cleanup phases outside the durable release sequence.
func (phase GlobalNetworkReleasePlanPhase) Validate() error {
	switch phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease, GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver, GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks, GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership, GlobalNetworkReleasePlanPhaseProjection:
		return nil
	default:
		return fmt.Errorf("global network release plan phase %q is unsupported", phase)
	}
}

// GlobalNetworkReleasePlanRecord exposes immutable recovery authority and its exact source boundary.
type GlobalNetworkReleasePlanRecord struct {
	Operation          OperationRecord
	Phase              GlobalNetworkReleasePlanPhase
	CheckpointRevision domain.Sequence
	NetworkRevision    domain.Sequence
	NetworkUpdatedAt   time.Time
	Authority          GlobalNetworkReleaseAuthority
	LowPortReceipt     *GlobalNetworkReleaseLowPortReceipt
	// ResolverReceipt retains the proof required before later release phases can run.
	ResolverReceipt *GlobalNetworkReleaseResolverReceipt
}

// Clone returns a copy whose retained authority bytes and slices cannot be modified by the caller.
func (record GlobalNetworkReleasePlanRecord) Clone() GlobalNetworkReleasePlanRecord {
	record.Authority = record.Authority.Clone()
	if record.LowPortReceipt != nil {
		receipt := *record.LowPortReceipt
		record.LowPortReceipt = &receipt
	}
	if record.ResolverReceipt != nil {
		receipt := *record.ResolverReceipt
		record.ResolverReceipt = &receipt
	}
	return record
}

// globalNetworkReleasePlanRow is the private persistence shape for release recovery authority.
type globalNetworkReleasePlanRow struct {
	ID                 int       `gorm:"column:id"`
	OperationID        string    `gorm:"column:operation_id"`
	OperationRevision  int       `gorm:"column:operation_revision"`
	CheckpointRevision int       `gorm:"column:checkpoint_revision"`
	Phase              string    `gorm:"column:phase"`
	NetworkStateID     int       `gorm:"column:network_state_id"`
	NetworkRevision    int       `gorm:"column:network_revision"`
	NetworkUpdatedAt   time.Time `gorm:"column:network_updated_at"`
	AuthorityPayload   string    `gorm:"column:authority_payload"`
	AuthorityDigest    string    `gorm:"column:authority_digest"`
}

// TableName returns the durable release-authority table name.
func (globalNetworkReleasePlanRow) TableName() string { return "network_global_release_plans" }

// ReadGlobalNetworkReleasePlan restores one active global release plan after validating all durable boundaries.
func (journal *OperationJournal) ReadGlobalNetworkReleasePlan(ctx context.Context, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error) {
	if err := operationID.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, err
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, err
	}
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, fmt.Errorf("open global network release plan: %w", err)
	}
	var result GlobalNetworkReleasePlanRecord
	found := false
	err = builder.Transaction(func(tx *gorm.DB) error {
		var rows []globalNetworkReleasePlanRow
		if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
			return fmt.Errorf("read global network release plan: %w", err)
		}
		if len(rows) > 1 {
			return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton contains %d rows, expected 1", len(rows)))
		}
		planFound := len(rows) == 1
		if planFound && rows[0].OperationID != string(operationID) {
			return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton belongs to operation %q", rows[0].OperationID))
		}
		operationRow, operationFound, err := findOperationByID(tx, operationID)
		if err != nil {
			return err
		}
		if !operationFound {
			if planFound {
				return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is missing"))
			}
			return nil
		}
		operation, err := operationRecordFromModel(operationRow)
		if err != nil {
			return err
		}
		if !planFound {
			if operation.Operation.Kind == domain.OperationKindNetworkRelease &&
				operation.Operation.ProjectID == "" &&
				!operation.Operation.State.IsTerminal() {
				return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("active release operation has no durable plan"))
			}
			return nil
		}
		validated, err := validateActiveGlobalNetworkReleasePlan(tx, rows[0], operation)
		if err != nil {
			return err
		}
		result = validated
		found = true
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, fmt.Errorf("read global network release plan: %w", err)
	}
	return result.Clone(), found, nil
}

// ReadActiveGlobalNetworkReleasePlan discovers the sole active release plan without requiring a caller-supplied operation identity.
func (journal *OperationJournal) ReadActiveGlobalNetworkReleasePlan(ctx context.Context) (GlobalNetworkReleasePlanRecord, bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, err
	}
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, fmt.Errorf("open active global network release plan: %w", err)
	}
	var result GlobalNetworkReleasePlanRecord
	found := false
	err = builder.Transaction(func(tx *gorm.DB) error {
		row, planFound, readErr := readOptionalGlobalNetworkReleasePlanForStaging(tx, "global")
		if readErr != nil {
			return readErr
		}
		active, activeFound, readErr := findActiveGlobalNetworkReleaseOperation(tx)
		if readErr != nil {
			return readErr
		}
		if !planFound && !activeFound {
			return nil
		}
		if !activeFound {
			return corruptGlobalNetworkReleasePlan(domain.OperationID(row.OperationID), fmt.Errorf("durable plan exists without an active global network release operation"))
		}
		if !planFound {
			return corruptGlobalNetworkReleasePlan(active.Operation.ID, fmt.Errorf("active global network release operation has no durable plan"))
		}
		if row.OperationID != string(active.Operation.ID) {
			return corruptGlobalNetworkReleasePlan(active.Operation.ID, fmt.Errorf("singleton belongs to operation %q", row.OperationID))
		}
		result, readErr = validateActiveGlobalNetworkReleasePlan(tx, row, active)
		if readErr != nil {
			return readErr
		}
		found = true
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, false, fmt.Errorf("read active global network release plan: %w", err)
	}
	return result.Clone(), found, nil
}

// validateActiveGlobalNetworkReleasePlan validates the fixed release owner, authority, checkpoint, and stopped-project boundary.
func validateActiveGlobalNetworkReleasePlan(
	tx *gorm.DB,
	row globalNetworkReleasePlanRow,
	operation OperationRecord,
) (GlobalNetworkReleasePlanRecord, error) {
	operationID := operation.Operation.ID
	if operation.Operation.Kind != domain.OperationKindNetworkRelease || operation.Operation.ProjectID != "" {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is not a global network release"))
	}
	if operation.Operation.State != domain.OperationRunning || operation.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation is %q/%q, expected running/%q", operation.Operation.State, operation.Operation.Phase, globalNetworkReleaseRuntimeOperationPhase))
	}
	plan, err := globalNetworkReleasePlanFromRow(row, operation)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	if err := validateGlobalNetworkReleaseCheckpoint(tx, plan, highWater); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	receipt, err := validateGlobalNetworkReleaseLowPortReceipt(tx, plan)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	plan.LowPortReceipt = receipt
	resolverReceipt, err := validateGlobalNetworkReleaseResolverReceipt(tx, plan)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	plan.ResolverReceipt = resolverReceipt
	if err := requireCurrentGlobalNetworkReleaseAuthority(tx, plan.Authority); err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("current network authority: %w", err))
	}
	if err := requireGlobalNetworkReleaseQuiescence(tx, plan.Authority.ProjectRevisions); err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("current project authority: %w", err))
	}
	return plan, nil
}

// globalNetworkReleasePlanFromRow validates every indexed source and canonical authority payload.
func globalNetworkReleasePlanFromRow(row globalNetworkReleasePlanRow, operation OperationRecord) (GlobalNetworkReleasePlanRecord, error) {
	id := operation.Operation.ID
	if row.ID != 1 {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("singleton ID is %d, expected 1", row.ID))
	}
	if row.OperationID != string(id) {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("operation ID does not match its owner"))
	}
	or, err := modelIntToSequence("global network release operation revision", row.OperationRevision)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if or != operation.Revision {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("plan revision is %d, operation revision is %d", or, operation.Revision))
	}
	checkpointRevision, err := modelIntToSequence("global network release checkpoint revision", row.CheckpointRevision)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if checkpointRevision < operation.Revision {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("checkpoint revision %d precedes operation revision %d", checkpointRevision, operation.Revision))
	}
	if row.NetworkStateID != networkStateSingletonID {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("network state ID is %d, expected 1", row.NetworkStateID))
	}
	nr, err := modelIntToSequence("global network release network revision", row.NetworkRevision)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if err := validateStoredTime("global network release network update time", row.NetworkUpdatedAt); err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if err := validateGlobalNetworkReleaseDigest(row.AuthorityDigest); err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if digestGlobalNetworkReleasePayload(row.AuthorityPayload) != row.AuthorityDigest {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("authority payload digest does not match"))
	}
	authority, err := decodeGlobalNetworkReleaseAuthority(row.AuthorityPayload)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	if authority.Projection.NetworkRevision != nr || !authority.Projection.NetworkUpdatedAt.Equal(row.NetworkUpdatedAt) {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, fmt.Errorf("authority projection differs from indexed network boundary"))
	}
	phase := GlobalNetworkReleasePlanPhase(row.Phase)
	if err := phase.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(id, err)
	}
	return GlobalNetworkReleasePlanRecord{
		Operation:          operation,
		Phase:              phase,
		CheckpointRevision: checkpointRevision,
		NetworkRevision:    nr,
		NetworkUpdatedAt:   row.NetworkUpdatedAt,
		Authority:          authority.Clone(),
	}, nil
}

// encodeGlobalNetworkReleaseAuthority returns canonical public authority and its integrity digest.
func encodeGlobalNetworkReleaseAuthority(authority GlobalNetworkReleaseAuthority) (string, string, error) {
	authority = authority.Clone()
	if err := authority.Validate(); err != nil {
		return "", "", err
	}
	dto := globalNetworkReleaseAuthorityDTOFromAuthority(authority)
	payload, err := json.Marshal(dto)
	if err != nil {
		return "", "", fmt.Errorf("encode global network release authority: %w", err)
	}
	if len(payload) > maximumGlobalNetworkReleaseAuthorityPayload {
		return "", "", fmt.Errorf("global network release authority payload exceeds %d bytes", maximumGlobalNetworkReleaseAuthorityPayload)
	}
	value := string(payload)
	return value, digestGlobalNetworkReleasePayload(value), nil
}

// decodeGlobalNetworkReleaseAuthority strictly restores canonical public release authority.
func decodeGlobalNetworkReleaseAuthority(payload string) (GlobalNetworkReleaseAuthority, error) {
	if len(payload) == 0 || len(payload) > maximumGlobalNetworkReleaseAuthorityPayload {
		return GlobalNetworkReleaseAuthority{}, fmt.Errorf("global network release authority payload has invalid size")
	}
	var dto globalNetworkReleaseAuthorityDTO
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dto); err != nil {
		return GlobalNetworkReleaseAuthority{}, fmt.Errorf("decode global network release authority: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return GlobalNetworkReleaseAuthority{}, fmt.Errorf("global network release authority contains a trailing JSON value")
		}
		return GlobalNetworkReleaseAuthority{}, err
	}
	authority, err := dto.authority()
	if err != nil {
		return GlobalNetworkReleaseAuthority{}, err
	}
	if err := authority.Validate(); err != nil {
		return GlobalNetworkReleaseAuthority{}, fmt.Errorf("validate global network release authority: %w", err)
	}
	canonical, _, err := encodeGlobalNetworkReleaseAuthority(authority)
	if err != nil {
		return GlobalNetworkReleaseAuthority{}, err
	}
	if canonical != payload {
		return GlobalNetworkReleaseAuthority{}, fmt.Errorf("global network release authority is not canonical JSON")
	}
	return authority.Clone(), nil
}

// validateGlobalNetworkReleaseDigest accepts one lowercase SHA-256 digest.
func validateGlobalNetworkReleaseDigest(value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("global network release authority digest must contain 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("global network release authority digest must contain 64 lowercase hexadecimal characters")
	}
	return nil
}

// digestGlobalNetworkReleasePayload fingerprints exact canonical release authority JSON.
func digestGlobalNetworkReleasePayload(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// corruptGlobalNetworkReleasePlan identifies operation-bound global release authority corruption.
func corruptGlobalNetworkReleasePlan(operationID domain.OperationID, cause error) error {
	return corruptStateError("global network release plan", string(operationID), cause)
}

// globalNetworkReleaseAuthorityDTO pins every serialized dimension independently of package JSON encoders.
type globalNetworkReleaseAuthorityDTO struct {
	Projection                     globalNetworkReleaseProjectionDTO        `json:"projection"`
	Policy                         globalNetworkReleasePolicyDTO            `json:"policy"`
	Root                           globalNetworkReleaseRootDTO              `json:"root"`
	ExpectedOwnershipFingerprint   string                                   `json:"expected_ownership_fingerprint"`
	TrustDisposition               GlobalNetworkReleaseTrustDisposition     `json:"trust_disposition"`
	LowPortObservationFingerprint  string                                   `json:"low_port_observation_fingerprint"`
	ResolverObservationFingerprint string                                   `json:"resolver_observation_fingerprint"`
	TrustObservationFingerprint    string                                   `json:"trust_observation_fingerprint"`
	LoopbackTargets                []globalNetworkReleaseLoopbackTargetDTO  `json:"loopback_targets"`
	ProjectRevisions               []globalNetworkReleaseProjectRevisionDTO `json:"project_revisions"`
}
type globalNetworkReleaseProjectionDTO struct {
	Stage              NetworkStage                        `json:"stage"`
	NetworkRevision    domain.Sequence                     `json:"network_revision"`
	NetworkUpdatedAt   time.Time                           `json:"network_updated_at"`
	ResolverProof      NetworkSetupProof                   `json:"resolver_proof"`
	LowPortProof       NetworkSetupProof                   `json:"low_port_proof"`
	Listeners          globalNetworkReleaseReservationsDTO `json:"listeners"`
	ConfirmedOwnership globalNetworkReleaseOwnershipDTO    `json:"confirmed_ownership"`
}
type globalNetworkReleaseReservationsDTO struct {
	DNS   globalNetworkReleaseReservationDTO `json:"dns"`
	HTTP  globalNetworkReleaseReservationDTO `json:"http"`
	HTTPS globalNetworkReleaseReservationDTO `json:"https"`
}
type globalNetworkReleaseReservationDTO struct {
	Mode       ListenerMode `json:"mode"`
	Advertised string       `json:"advertised"`
	Bind       string       `json:"bind"`
	Generation uint64       `json:"generation"`
	VerifiedAt time.Time    `json:"verified_at"`
}
type globalNetworkReleaseOwnershipDTO struct {
	Exists      bool                                   `json:"exists"`
	Record      globalNetworkReleaseOwnershipRecordDTO `json:"record"`
	Fingerprint string                                 `json:"fingerprint"`
}
type globalNetworkReleaseOwnershipRecordDTO struct {
	SchemaVersion            uint32 `json:"schema_version"`
	InstallationID           string `json:"installation_id"`
	OwnerIdentity            string `json:"owner_identity"`
	Generation               uint64 `json:"generation"`
	LoopbackPoolPrefix       string `json:"loopback_pool_prefix"`
	NetworkPolicyFingerprint string `json:"network_policy_fingerprint"`
	TicketVerifierKey        string `json:"ticket_verifier_key"`
}
type globalNetworkReleasePolicyDTO struct {
	Suffix               string                                `json:"suffix"`
	AuthorityFingerprint string                                `json:"authority_fingerprint"`
	Mechanisms           networkpolicy.Mechanisms              `json:"mechanisms"`
	DNS                  globalNetworkReleasePolicyListenerDTO `json:"dns"`
	HTTP                 globalNetworkReleasePolicyListenerDTO `json:"http"`
	HTTPS                globalNetworkReleasePolicyListenerDTO `json:"https"`
}
type globalNetworkReleasePolicyListenerDTO struct {
	Advertised string `json:"advertised"`
	Bind       string `json:"bind"`
}
type globalNetworkReleaseRootDTO struct {
	CertificatePEM []byte    `json:"certificate_pem"`
	Fingerprint    string    `json:"fingerprint"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
}
type globalNetworkReleaseLoopbackTargetDTO struct {
	Address                string `json:"address"`
	ObservationFingerprint string `json:"observation_fingerprint"`
}
type globalNetworkReleaseProjectRevisionDTO struct {
	ProjectID domain.ProjectID `json:"project_id"`
	Revision  domain.Sequence  `json:"revision"`
}

// globalNetworkReleaseAuthorityDTOFromAuthority converts values to explicit text-only boundary shapes.
func globalNetworkReleaseAuthorityDTOFromAuthority(a GlobalNetworkReleaseAuthority) globalNetworkReleaseAuthorityDTO {
	return globalNetworkReleaseAuthorityDTO{
		Projection:                     globalNetworkReleaseProjectionDTOFromProjection(a.Projection),
		Policy:                         globalNetworkReleasePolicyDTOFromPolicy(a.Policy),
		Root:                           globalNetworkReleaseRootDTOFromRoot(a.Root),
		ExpectedOwnershipFingerprint:   a.ExpectedOwnershipFingerprint,
		TrustDisposition:               a.TrustDisposition,
		LowPortObservationFingerprint:  a.LowPortObservationFingerprint,
		ResolverObservationFingerprint: a.ResolverObservationFingerprint,
		TrustObservationFingerprint:    a.TrustObservationFingerprint,
		LoopbackTargets:                globalNetworkReleaseLoopbackTargetsDTOFromTargets(a.LoopbackTargets),
		ProjectRevisions:               globalNetworkReleaseProjectRevisionsDTOFromRevisions(a.ProjectRevisions),
	}
}

// authority converts explicit wire values back to domain values and rejects malformed address text.
func (d globalNetworkReleaseAuthorityDTO) authority() (GlobalNetworkReleaseAuthority, error) {
	projection, err := d.Projection.projection()
	if err != nil {
		return GlobalNetworkReleaseAuthority{}, err
	}
	policy, err := d.Policy.policy()
	if err != nil {
		return GlobalNetworkReleaseAuthority{}, err
	}
	targets, err := d.loopbackTargets()
	if err != nil {
		return GlobalNetworkReleaseAuthority{}, err
	}
	return GlobalNetworkReleaseAuthority{
		Projection:                     projection,
		Policy:                         policy,
		Root:                           d.Root.root(),
		ExpectedOwnershipFingerprint:   d.ExpectedOwnershipFingerprint,
		TrustDisposition:               d.TrustDisposition,
		LowPortObservationFingerprint:  d.LowPortObservationFingerprint,
		ResolverObservationFingerprint: d.ResolverObservationFingerprint,
		TrustObservationFingerprint:    d.TrustObservationFingerprint,
		LoopbackTargets:                targets,
		ProjectRevisions:               d.projectRevisions(),
	}, nil
}

// globalNetworkReleaseProjectionDTOFromProjection converts a projection without package-defined JSON encoding.
func globalNetworkReleaseProjectionDTOFromProjection(value NetworkDataPlaneSetupProjection) globalNetworkReleaseProjectionDTO {
	return globalNetworkReleaseProjectionDTO{
		Stage:            value.Stage,
		NetworkRevision:  value.NetworkRevision,
		NetworkUpdatedAt: value.NetworkUpdatedAt,
		ResolverProof:    value.ResolverProof,
		LowPortProof:     value.LowPortProof,
		Listeners: globalNetworkReleaseReservationsDTO{
			DNS:   globalNetworkReleaseReservationDTOFromReservation(value.Listeners.DNS),
			HTTP:  globalNetworkReleaseReservationDTOFromReservation(value.Listeners.HTTP),
			HTTPS: globalNetworkReleaseReservationDTOFromReservation(value.Listeners.HTTPS),
		},
		ConfirmedOwnership: globalNetworkReleaseOwnershipDTOFromObservation(value.ConfirmedOwnership),
	}
}

// globalNetworkReleaseReservationDTOFromReservation converts a listener reservation to canonical socket text.
func globalNetworkReleaseReservationDTOFromReservation(value ListenerReservation) globalNetworkReleaseReservationDTO {
	return globalNetworkReleaseReservationDTO{
		Mode:       value.Mode,
		Advertised: value.Advertised.String(),
		Bind:       value.Bind.String(),
		Generation: value.Generation,
		VerifiedAt: value.VerifiedAt,
	}
}

// globalNetworkReleaseOwnershipDTOFromObservation converts every ownership dimension explicitly.
func globalNetworkReleaseOwnershipDTOFromObservation(value ownership.Observation) globalNetworkReleaseOwnershipDTO {
	r := value.Record
	return globalNetworkReleaseOwnershipDTO{
		Exists: value.Exists,
		Record: globalNetworkReleaseOwnershipRecordDTO{
			SchemaVersion:            r.SchemaVersion,
			InstallationID:           r.InstallationID,
			OwnerIdentity:            r.OwnerIdentity,
			Generation:               r.Generation,
			LoopbackPoolPrefix:       r.LoopbackPoolPrefix,
			NetworkPolicyFingerprint: r.NetworkPolicyFingerprint,
			TicketVerifierKey:        r.TicketVerifierKey,
		},
		Fingerprint: value.Fingerprint,
	}
}

// globalNetworkReleasePolicyDTOFromPolicy converts policy sockets to canonical text.
func globalNetworkReleasePolicyDTOFromPolicy(value networkpolicy.Policy) globalNetworkReleasePolicyDTO {
	return globalNetworkReleasePolicyDTO{
		Suffix:               value.Suffix,
		AuthorityFingerprint: value.AuthorityFingerprint,
		Mechanisms:           value.Mechanisms,
		DNS: globalNetworkReleasePolicyListenerDTO{
			Advertised: value.DNS.Advertised.String(),
			Bind:       value.DNS.Bind.String(),
		},
		HTTP: globalNetworkReleasePolicyListenerDTO{
			Advertised: value.HTTP.Advertised.String(),
			Bind:       value.HTTP.Bind.String(),
		},
		HTTPS: globalNetworkReleasePolicyListenerDTO{
			Advertised: value.HTTPS.Advertised.String(),
			Bind:       value.HTTPS.Bind.String(),
		},
	}
}

// globalNetworkReleaseRootDTOFromRoot retains only public certificate material.
func globalNetworkReleaseRootDTOFromRoot(value certroot.Root) globalNetworkReleaseRootDTO {
	return globalNetworkReleaseRootDTO{
		CertificatePEM: slices.Clone(value.CertificatePEM),
		Fingerprint:    value.Fingerprint,
		NotBefore:      value.NotBefore,
		NotAfter:       value.NotAfter,
	}
}

// globalNetworkReleaseLoopbackTargetsDTOFromTargets converts addresses to canonical text.
func globalNetworkReleaseLoopbackTargetsDTOFromTargets(values []GlobalNetworkReleaseLoopbackTarget) []globalNetworkReleaseLoopbackTargetDTO {
	result := make([]globalNetworkReleaseLoopbackTargetDTO, 0, len(values))
	for _, value := range values {
		result = append(result, globalNetworkReleaseLoopbackTargetDTO{
			Address:                value.Address.String(),
			ObservationFingerprint: value.ObservationFingerprint,
		})
	}
	return result
}

// globalNetworkReleaseProjectRevisionsDTOFromRevisions converts the complete project revision snapshot.
func globalNetworkReleaseProjectRevisionsDTOFromRevisions(values []NetworkProjectRevision) []globalNetworkReleaseProjectRevisionDTO {
	result := make([]globalNetworkReleaseProjectRevisionDTO, 0, len(values))
	for _, value := range values {
		result = append(result, globalNetworkReleaseProjectRevisionDTO{
			ProjectID: value.ProjectID,
			Revision:  value.Revision,
		})
	}
	return result
}

// projection restores explicit projection values.
func (value globalNetworkReleaseProjectionDTO) projection() (NetworkDataPlaneSetupProjection, error) {
	dns, err := value.Listeners.DNS.reservation()
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	http, err := value.Listeners.HTTP.reservation()
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	https, err := value.Listeners.HTTPS.reservation()
	if err != nil {
		return NetworkDataPlaneSetupProjection{}, err
	}
	return NetworkDataPlaneSetupProjection{
		Stage:            value.Stage,
		NetworkRevision:  value.NetworkRevision,
		NetworkUpdatedAt: value.NetworkUpdatedAt,
		ResolverProof:    value.ResolverProof,
		LowPortProof:     value.LowPortProof,
		Listeners: SharedListenerReservations{
			DNS:   dns,
			HTTP:  http,
			HTTPS: https,
		},
		ConfirmedOwnership: value.ConfirmedOwnership.observation(),
	}, nil
}

// reservation restores a canonical listener reservation.
func (value globalNetworkReleaseReservationDTO) reservation() (ListenerReservation, error) {
	advertised, err := parseGlobalNetworkReleaseSocket(value.Advertised)
	if err != nil {
		return ListenerReservation{}, err
	}
	bind, err := parseGlobalNetworkReleaseSocket(value.Bind)
	if err != nil {
		return ListenerReservation{}, err
	}
	return ListenerReservation{
		Mode:       value.Mode,
		Advertised: advertised,
		Bind:       bind,
		Generation: value.Generation,
		VerifiedAt: value.VerifiedAt,
	}, nil
}

// observation restores all ownership dimensions.
func (value globalNetworkReleaseOwnershipDTO) observation() ownership.Observation {
	r := value.Record
	return ownership.Observation{
		Exists: value.Exists,
		Record: ownership.Record{
			SchemaVersion:            r.SchemaVersion,
			InstallationID:           r.InstallationID,
			OwnerIdentity:            r.OwnerIdentity,
			Generation:               r.Generation,
			LoopbackPoolPrefix:       r.LoopbackPoolPrefix,
			NetworkPolicyFingerprint: r.NetworkPolicyFingerprint,
			TicketVerifierKey:        r.TicketVerifierKey,
		},
		Fingerprint: value.Fingerprint,
	}
}

// policy restores a canonical host policy.
func (value globalNetworkReleasePolicyDTO) policy() (networkpolicy.Policy, error) {
	dns, err := value.DNS.listener()
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	http, err := value.HTTP.listener()
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	https, err := value.HTTPS.listener()
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	return networkpolicy.Policy{
		Suffix:               value.Suffix,
		AuthorityFingerprint: value.AuthorityFingerprint,
		Mechanisms:           value.Mechanisms,
		DNS:                  dns,
		HTTP:                 http,
		HTTPS:                https,
	}, nil
}

// listener restores a canonical policy listener.
func (value globalNetworkReleasePolicyListenerDTO) listener() (networkpolicy.Listener, error) {
	advertised, err := parseGlobalNetworkReleaseSocket(value.Advertised)
	if err != nil {
		return networkpolicy.Listener{}, err
	}
	bind, err := parseGlobalNetworkReleaseSocket(value.Bind)
	if err != nil {
		return networkpolicy.Listener{}, err
	}
	return networkpolicy.Listener{
		Advertised: advertised,
		Bind:       bind,
	}, nil
}

// root restores public root bytes defensively.
func (value globalNetworkReleaseRootDTO) root() certroot.Root {
	return certroot.Root{
		CertificatePEM: slices.Clone(value.CertificatePEM),
		Fingerprint:    value.Fingerprint,
		NotBefore:      value.NotBefore,
		NotAfter:       value.NotAfter,
	}
}

// loopbackTargets restores exact canonical loopback target text.
func (value globalNetworkReleaseAuthorityDTO) loopbackTargets() ([]GlobalNetworkReleaseLoopbackTarget, error) {
	result := make([]GlobalNetworkReleaseLoopbackTarget, 0, len(value.LoopbackTargets))
	for _, target := range value.LoopbackTargets {
		address, err := netip.ParseAddr(target.Address)
		if err != nil || address.String() != target.Address {
			return nil, fmt.Errorf("invalid canonical loopback address %q", target.Address)
		}
		result = append(result, GlobalNetworkReleaseLoopbackTarget{
			Address:                address,
			ObservationFingerprint: target.ObservationFingerprint,
		})
	}
	return result, nil
}

// projectRevisions restores the exact project revision snapshot.
func (value globalNetworkReleaseAuthorityDTO) projectRevisions() []NetworkProjectRevision {
	result := make([]NetworkProjectRevision, 0, len(value.ProjectRevisions))
	for _, revision := range value.ProjectRevisions {
		result = append(result, NetworkProjectRevision{
			ProjectID: revision.ProjectID,
			Revision:  revision.Revision,
		})
	}
	return result
}

// parseGlobalNetworkReleaseSocket rejects noncanonical socket text.
func parseGlobalNetworkReleaseSocket(value string) (netip.AddrPort, error) {
	socket, err := netip.ParseAddrPort(value)
	if err != nil || socket.String() != value {
		return netip.AddrPort{}, fmt.Errorf("invalid canonical socket %q", value)
	}
	return socket, nil
}
