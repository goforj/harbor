package projectprocess

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"
)

const unattributedRuntimeFingerprintDomain = "goforj.harbor.unattributed-runtime-candidate.v1\x00"

// UnattributedRuntimeCandidate carries bounded display facts and an opaque native receipt for one listener scope.
type UnattributedRuntimeCandidate struct {
	// Fingerprint identifies the complete native scope without exposing its raw process facts.
	Fingerprint string
	// Display contains the bounded facts suitable for an explicit user review.
	Display RuntimeRepairDisplay
	receipt *unattributedRuntimeReceipt
}

// Validate prevents caller-created or caller-mutated unattributed candidates from crossing the inspection boundary.
func (candidate UnattributedRuntimeCandidate) Validate() error {
	if err := validateRuntimeRepairFingerprint(candidate.Fingerprint); err != nil {
		return err
	}
	if err := candidate.Display.Validate(); err != nil {
		return err
	}
	if candidate.receipt == nil {
		return fmt.Errorf("unattributed runtime candidate has no process-local receipt")
	}
	if err := candidate.receipt.validate(); err != nil {
		return fmt.Errorf("validate unattributed runtime candidate receipt: %w", err)
	}
	fingerprint, err := candidate.receipt.observation.fingerprint()
	if err != nil {
		return err
	}
	if fingerprint != candidate.Fingerprint {
		return fmt.Errorf("unattributed runtime candidate fingerprint does not match its receipt")
	}
	if candidate.Display != unattributedRuntimeDisplay(candidate.receipt.observation) {
		return fmt.Errorf("unattributed runtime candidate display does not match its receipt")
	}
	return nil
}

// Clone returns an isolated candidate while preserving its opaque process-local native receipt.
func (candidate UnattributedRuntimeCandidate) Clone() UnattributedRuntimeCandidate {
	clone := candidate
	if candidate.receipt != nil {
		receipt := candidate.receipt.clone()
		clone.receipt = &receipt
	}
	return clone
}

// UnattributedRuntimeInspection is one fixed read-only result for an already-retired listener.
type UnattributedRuntimeInspection struct {
	// State classifies whether native evidence is absent, unsafe, or actionable.
	State RuntimeRepairInspectionState
	// Diagnostic is the stable client-facing reason paired with State.
	Diagnostic RuntimeRepairDiagnostic
	// Candidate is present only when one exact same-user scope was correlated.
	Candidate *UnattributedRuntimeCandidate
}

// Validate enforces that only one fully correlated native scope contains a candidate.
func (inspection UnattributedRuntimeInspection) Validate() error {
	if err := inspection.State.Validate(); err != nil {
		return err
	}
	if err := inspection.Diagnostic.Validate(); err != nil {
		return err
	}
	if runtimeRepairDiagnosticForState(inspection.State) != inspection.Diagnostic {
		return fmt.Errorf("unattributed runtime inspection diagnostic does not match state %q", inspection.State)
	}
	if inspection.State != RuntimeRepairInspectionActionable {
		if inspection.Candidate != nil {
			return fmt.Errorf("non-actionable unattributed runtime inspection contains a candidate")
		}
		return nil
	}
	if inspection.Candidate == nil {
		return fmt.Errorf("actionable unattributed runtime inspection has no candidate")
	}
	return inspection.Candidate.Validate()
}

// UnattributedRuntimeInspector performs read-only native inspection without exposing a confirmation or signal method.
type UnattributedRuntimeInspector interface {
	Inspect(context.Context, RuntimeRepairTarget) (UnattributedRuntimeInspection, error)
}

// UnattributedRuntimeRepairer inspects and explicitly confirms one same-user scope without mutating Harbor state.
type UnattributedRuntimeRepairer interface {
	UnattributedRuntimeInspector
	Confirm(context.Context, UnattributedRuntimeCandidate) (RuntimeRepairConfirmation, error)
}

// unattributedRuntimeControl isolates native observation for the no-session listener path.
type unattributedRuntimeControl struct {
	inspect  func(context.Context, RuntimeRepairTarget) (unattributedRuntimeNativeInspection, error)
	graceful func(context.Context, unattributedRuntimeReceipt) (bool, error)
	force    func(context.Context, unattributedRuntimeReceipt) (bool, error)
	settled  func(context.Context, unattributedRuntimeReceipt) (bool, error)
}

// unattributedRuntimeAdapter applies common validation and receipt projection to native inspection.
type unattributedRuntimeAdapter struct {
	control          unattributedRuntimeControl
	settlementPeriod time.Duration
	settlementPoll   time.Duration
}

// NewUnattributedRuntimeInspector selects the reviewed platform backend for read-only listener inspection.
func NewUnattributedRuntimeInspector() UnattributedRuntimeInspector {
	return newUnattributedRuntimeInspectorWithControl(newUnattributedRuntimeControl())
}

// NewUnattributedRuntimeRepairer selects the reviewed platform backend for explicit no-session cleanup.
func NewUnattributedRuntimeRepairer() UnattributedRuntimeRepairer {
	return newUnattributedRuntimeRepairerWithControl(newUnattributedRuntimeControl(), runtimeRepairSettlementPeriod, runtimeRepairSettlementPoll)
}

// newUnattributedRuntimeInspectorWithControl constructs the deterministic native seam used by tests.
func newUnattributedRuntimeInspectorWithControl(control unattributedRuntimeControl) *unattributedRuntimeAdapter {
	if control.inspect == nil {
		panic("projectprocess unattributed runtime inspection requires native control")
	}
	return &unattributedRuntimeAdapter{
		control:          control,
		settlementPeriod: runtimeRepairSettlementPeriod,
		settlementPoll:   runtimeRepairSettlementPoll,
	}
}

// newUnattributedRuntimeRepairerWithControl constructs the deterministic signal and settlement seam used by tests.
func newUnattributedRuntimeRepairerWithControl(control unattributedRuntimeControl, settlementPeriod, settlementPoll time.Duration) *unattributedRuntimeAdapter {
	if control.inspect == nil || control.graceful == nil || control.force == nil || control.settled == nil {
		panic("projectprocess unattributed runtime repair requires complete native control")
	}
	if settlementPeriod <= 0 || settlementPoll <= 0 {
		panic("projectprocess unattributed runtime repair requires positive settlement bounds")
	}
	return &unattributedRuntimeAdapter{control: control, settlementPeriod: settlementPeriod, settlementPoll: settlementPoll}
}

// Inspect returns only a fixed classification or one validated opaque candidate and never signals a process.
func (inspector *unattributedRuntimeAdapter) Inspect(ctx context.Context, target RuntimeRepairTarget) (UnattributedRuntimeInspection, error) {
	if inspector == nil {
		panic("projectprocess unattributed runtime inspection requires a non-nil receiver")
	}
	ctx = normalizeRuntimeRepairContext(ctx)
	if err := ctx.Err(); err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	if err := target.Validate(); err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	native, err := inspector.control.inspect(ctx, target)
	if err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	inspection, err := unattributedRuntimeInspection(native)
	if err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	return inspection, nil
}

// Confirm reobserves the exact scope before graceful signaling and waits for its target listener to settle.
func (repairer *unattributedRuntimeAdapter) Confirm(ctx context.Context, candidate UnattributedRuntimeCandidate) (RuntimeRepairConfirmation, error) {
	if repairer == nil {
		panic("projectprocess unattributed runtime repair Confirm requires a non-nil receiver")
	}
	ctx = normalizeRuntimeRepairContext(ctx)
	if err := ctx.Err(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	candidate = candidate.Clone()
	if err := candidate.Validate(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	if repairer.control.graceful == nil || repairer.control.settled == nil {
		return runtimeRepairFailed(false, fmt.Errorf("unattributed runtime repair confirmation is unsupported on this platform"))
	}
	receipt := candidate.receipt.clone()
	current, err := repairer.control.inspect(ctx, receipt.observation.Target)
	if err != nil {
		return runtimeRepairFailed(false, err)
	}
	if current.State != RuntimeRepairInspectionActionable || current.Observation == nil ||
		!unattributedRuntimeObservationsEqual(*current.Observation, receipt.observation) {
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationDrifted}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	signaled, err := repairer.control.graceful(ctx, receipt)
	if errors.Is(err, ErrRuntimeRepairDrift) {
		if signaled {
			return runtimeRepairFailed(true, fmt.Errorf("unattributed runtime repair backend reported drift after signaling: %w", err))
		}
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationDrifted}, nil
	}
	if err != nil {
		return runtimeRepairFailed(signaled, err)
	}
	if !signaled {
		return runtimeRepairFailed(false, fmt.Errorf("unattributed runtime repair backend returned without signaling or drift"))
	}
	settled, err := repairer.waitForSettlement(ctx, receipt)
	if err != nil {
		return runtimeRepairFailed(true, err)
	}
	if settled {
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationSettled, Signaled: true}, nil
	}

	// The candidate was explicitly confirmed and remains bound to one exact scope, so a bounded
	// forceful pass can finish descendants that ignored the graceful root signal.
	forced, err := repairer.control.force(ctx, receipt)
	if errors.Is(err, ErrRuntimeRepairDrift) {
		return runtimeRepairFailed(true, fmt.Errorf("unattributed runtime scope drifted during forceful settlement: %w", err))
	}
	if err != nil {
		return runtimeRepairFailed(true, err)
	}
	if !forced {
		return runtimeRepairFailed(true, ErrRuntimeRepairNotSettled)
	}
	settled, err = repairer.waitForSettlement(ctx, receipt)
	if err != nil {
		return runtimeRepairFailed(true, err)
	}
	if !settled {
		return runtimeRepairFailed(true, ErrRuntimeRepairNotSettled)
	}
	return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationSettled, Signaled: true}, nil
}

// waitForSettlement polls until every captured birth and the exact target socket are absent.
func (repairer *unattributedRuntimeAdapter) waitForSettlement(ctx context.Context, receipt unattributedRuntimeReceipt) (bool, error) {
	timer := time.NewTimer(repairer.settlementPeriod)
	defer timer.Stop()
	ticker := time.NewTicker(repairer.settlementPoll)
	defer ticker.Stop()
	for {
		settled, err := repairer.control.settled(ctx, receipt)
		if err != nil || settled {
			return settled, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return repairer.control.settled(ctx, receipt)
		case <-ticker.C:
		}
	}
}

// unattributedRuntimeNativeInspection retains native-only facts until safe public projection.
type unattributedRuntimeNativeInspection struct {
	State       RuntimeRepairInspectionState
	Observation *unattributedRuntimeNativeObservation
}

// validate enforces that only an actionable native state carries process authority facts.
func (inspection unattributedRuntimeNativeInspection) validate() error {
	if err := inspection.State.Validate(); err != nil {
		return err
	}
	if inspection.State != RuntimeRepairInspectionActionable {
		if inspection.Observation != nil {
			return fmt.Errorf("non-actionable unattributed native inspection contains authority facts")
		}
		return nil
	}
	if inspection.Observation == nil {
		return fmt.Errorf("actionable unattributed native inspection is missing authority facts")
	}
	return inspection.Observation.validate()
}

// unattributedRuntimeReceipt retains native scope authority only inside the daemon process.
type unattributedRuntimeReceipt struct {
	observation unattributedRuntimeNativeObservation
}

// validate rejects receipts that cannot identify one exact same-user process scope.
func (receipt unattributedRuntimeReceipt) validate() error {
	return receipt.observation.validate()
}

// clone isolates every native fact held by one process-local inspection result.
func (receipt unattributedRuntimeReceipt) clone() unattributedRuntimeReceipt {
	return unattributedRuntimeReceipt{observation: receipt.observation.clone()}
}

// unattributedRuntimeNativeObservation is the complete same-user process tree behind one exact listener.
type unattributedRuntimeNativeObservation struct {
	Target     RuntimeRepairTarget
	DaemonUID  uint32
	Root       runtimeRepairProcessFact
	RootParent runtimeRepairParentFact
	Members    []runtimeRepairProcessFact
	Listener   runtimeRepairSocketFact
}

// validate proves the root command, descendant scope, process births, and listener owner correlate exactly.
func (observation unattributedRuntimeNativeObservation) validate() error {
	if err := observation.Target.Validate(); err != nil {
		return err
	}
	if err := observation.Root.validate(); err != nil {
		return fmt.Errorf("unattributed runtime root: %w", err)
	}
	if err := observation.RootParent.validate(); err != nil {
		return err
	}
	if err := observation.Listener.validate(); err != nil {
		return err
	}
	if len(observation.Members) == 0 || len(observation.Members) > runtimeRepairMaximumProcesses {
		return fmt.Errorf("unattributed runtime scope member count must be from 1 through %d", runtimeRepairMaximumProcesses)
	}
	if observation.Root.ParentPID != observation.RootParent.PID {
		return fmt.Errorf("unattributed runtime root parent does not match its captured boundary")
	}
	if observation.Root.EffectiveUID != observation.DaemonUID || observation.Root.RealUID != observation.DaemonUID {
		return fmt.Errorf("unattributed runtime root is not owned exclusively by the daemon user")
	}
	if observation.Root.WorkingDirectory != observation.Target.CheckoutRoot {
		return fmt.Errorf("unattributed runtime root working directory does not match the target checkout")
	}
	if filepath.Base(observation.Root.ExecutableIdentity) != "forj" || observation.Root.ArgumentCount != 2 || !observation.Root.CommandExact {
		return fmt.Errorf("unattributed runtime root command is not exactly forj dev")
	}
	if observation.Listener.Endpoint != observation.Target.Endpoint {
		return fmt.Errorf("unattributed runtime listener does not match the target endpoint")
	}
	memberByPID := make(map[int]runtimeRepairProcessFact, len(observation.Members))
	for index, member := range observation.Members {
		if err := member.validate(); err != nil {
			return fmt.Errorf("unattributed runtime scope member %d: %w", index, err)
		}
		if index > 0 && observation.Members[index-1].PID >= member.PID {
			return fmt.Errorf("unattributed runtime scope members are not in unique PID order")
		}
		if member.EffectiveUID != observation.DaemonUID || member.RealUID != observation.DaemonUID {
			return fmt.Errorf("unattributed runtime scope member %d escaped the daemon user", member.PID)
		}
		memberByPID[member.PID] = member
	}
	root, found := memberByPID[observation.Root.PID]
	if !found || !reflect.DeepEqual(root, observation.Root) {
		return fmt.Errorf("unattributed runtime scope does not contain its exact root")
	}
	if _, parentInsideScope := memberByPID[observation.RootParent.PID]; parentInsideScope {
		return fmt.Errorf("unattributed runtime root parent remains inside the observed scope")
	}
	owner, found := memberByPID[observation.Listener.OwnerPID]
	if !found || owner.BirthToken != observation.Listener.OwnerBirthToken {
		return fmt.Errorf("unattributed runtime listener owner is not one exact scope member")
	}
	for _, member := range observation.Members {
		if member.PID == observation.Root.PID {
			continue
		}
		if !runtimeRepairDescendsFromRoot(member.PID, observation.Root.PID, memberByPID) {
			return fmt.Errorf("unattributed runtime scope member %d is not a descendant of the exact root", member.PID)
		}
	}
	return nil
}

// clone isolates process facts before the candidate crosses a package boundary.
func (observation unattributedRuntimeNativeObservation) clone() unattributedRuntimeNativeObservation {
	clone := observation
	clone.Root = observation.Root.clone()
	clone.Members = make([]runtimeRepairProcessFact, len(observation.Members))
	for index, member := range observation.Members {
		clone.Members[index] = member.clone()
	}
	return clone
}

// fingerprint returns a domain-separated digest over every unattributed native fact.
func (observation unattributedRuntimeNativeObservation) fingerprint() (string, error) {
	if err := observation.validate(); err != nil {
		return "", fmt.Errorf("fingerprint unattributed runtime observation: %w", err)
	}
	payload := append([]byte(nil), unattributedRuntimeFingerprintDomain...)
	payload = runtimeRepairAppendString(payload, observation.Target.CheckoutRoot)
	payload = runtimeRepairAppendAddressPort(payload, observation.Target.Endpoint)
	payload = binary.AppendUvarint(payload, uint64(observation.DaemonUID))
	payload = runtimeRepairAppendProcess(payload, observation.Root)
	payload = binary.AppendVarint(payload, int64(observation.RootParent.PID))
	payload = runtimeRepairAppendString(payload, observation.RootParent.BirthToken)
	payload = binary.AppendUvarint(payload, uint64(len(observation.Members)))
	for _, member := range observation.Members {
		payload = runtimeRepairAppendProcess(payload, member)
	}
	payload = binary.AppendVarint(payload, int64(observation.Listener.OwnerPID))
	payload = runtimeRepairAppendString(payload, observation.Listener.OwnerBirthToken)
	payload = binary.AppendVarint(payload, int64(observation.Listener.FileDescriptor))
	payload = binary.AppendUvarint(payload, observation.Listener.SocketHandle)
	payload = binary.AppendUvarint(payload, observation.Listener.PCBHandle)
	payload = binary.AppendUvarint(payload, observation.Listener.Generation)
	payload = runtimeRepairAppendAddressPort(payload, observation.Listener.Endpoint)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// unattributedRuntimeInspection converts native state without forwarding native details or signal authority.
func unattributedRuntimeInspection(native unattributedRuntimeNativeInspection) (UnattributedRuntimeInspection, error) {
	if err := native.validate(); err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	inspection := UnattributedRuntimeInspection{
		State:      native.State,
		Diagnostic: runtimeRepairDiagnosticForState(native.State),
	}
	if native.State == RuntimeRepairInspectionActionable {
		observation := native.Observation.clone()
		fingerprint, err := observation.fingerprint()
		if err != nil {
			return UnattributedRuntimeInspection{}, err
		}
		receipt := unattributedRuntimeReceipt{observation: observation}
		inspection.Candidate = &UnattributedRuntimeCandidate{
			Fingerprint: fingerprint,
			Display:     unattributedRuntimeDisplay(observation),
			receipt:     &receipt,
		}
	}
	if err := inspection.Validate(); err != nil {
		return UnattributedRuntimeInspection{}, err
	}
	return inspection, nil
}

// unattributedRuntimeDisplay derives bounded confirmation facts from one validated native observation.
func unattributedRuntimeDisplay(observation unattributedRuntimeNativeObservation) RuntimeRepairDisplay {
	return RuntimeRepairDisplay{
		RootPID:      int64(observation.Root.PID),
		Command:      runtimeRepairCommand,
		CheckoutRoot: observation.Target.CheckoutRoot,
		Endpoint:     observation.Target.Endpoint,
		ProcessCount: len(observation.Members),
	}
}

// unattributedRuntimeObservationsEqual requires structural equality in addition to matching fingerprints.
func unattributedRuntimeObservationsEqual(left, right unattributedRuntimeNativeObservation) bool {
	leftFingerprint, leftErr := left.fingerprint()
	rightFingerprint, rightErr := right.fingerprint()
	return leftErr == nil && rightErr == nil && leftFingerprint == rightFingerprint && reflect.DeepEqual(left, right)
}
