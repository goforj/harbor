package projectprocess

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	// runtimeRepairFingerprintDomain prevents candidate digests from being reused as another receipt kind.
	runtimeRepairFingerprintDomain = "goforj.harbor.runtime-repair-candidate.v1\x00"
	// runtimeRepairCommand is fixed because client text must not expose native command data.
	runtimeRepairCommand = "forj dev"
	// runtimeRepairProjectListenerCommand is fixed because project-owned fallback displays must not expose native command data.
	runtimeRepairProjectListenerCommand = "project listener"
	// runtimeRepairMaximumProcesses bounds both native collection and receipt validation.
	runtimeRepairMaximumProcesses = 4096
	// runtimeRepairMaximumArguments bounds argv collection before native bytes become receipt facts.
	runtimeRepairMaximumArguments = 256
	// runtimeRepairMaximumTextBytes bounds each native text fact retained by the daemon.
	runtimeRepairMaximumTextBytes = 4096
	// runtimeRepairSettlementPeriod limits graceful observation without authorizing escalation.
	runtimeRepairSettlementPeriod = 3 * time.Second
	// runtimeRepairSettlementPoll keeps convergence responsive while avoiding an unbounded busy loop.
	runtimeRepairSettlementPoll = 25 * time.Millisecond
)

var (
	// ErrRuntimeRepairDrift means the inspected native candidate changed before Harbor could signal it.
	ErrRuntimeRepairDrift = errors.New("runtime repair candidate drifted")
	// ErrRuntimeRepairNotSettled means graceful termination did not release every captured birth and the target socket.
	ErrRuntimeRepairNotSettled = errors.New("runtime repair candidate did not settle")
)

// RuntimeRepairTarget identifies one daemon-derived checkout and exact primary App listener.
type RuntimeRepairTarget struct {
	CheckoutRoot string
	Endpoint     netip.AddrPort
}

// Validate proves the target is one existing canonical checkout and one exact IPv4 loopback endpoint.
func (target RuntimeRepairTarget) Validate() error {
	if target.CheckoutRoot == "" || len(target.CheckoutRoot) > runtimeRepairMaximumTextBytes {
		return fmt.Errorf("runtime repair checkout root must be non-empty and bounded")
	}
	if !filepath.IsAbs(target.CheckoutRoot) || filepath.Clean(target.CheckoutRoot) != target.CheckoutRoot {
		return fmt.Errorf("runtime repair checkout root must be a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(target.CheckoutRoot)
	if err != nil {
		return fmt.Errorf("canonicalize runtime repair checkout root: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if canonical != target.CheckoutRoot {
		return fmt.Errorf("runtime repair checkout root must already be canonical")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("inspect runtime repair checkout root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime repair checkout root must be a directory")
	}
	address := target.Endpoint.Addr()
	if !target.Endpoint.IsValid() || target.Endpoint.Port() == 0 || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return fmt.Errorf("runtime repair endpoint must be a canonical IPv4 loopback address and non-zero port")
	}
	return nil
}

// RuntimeRepairInspectionState classifies whether native facts authorize a user-confirmable candidate.
type RuntimeRepairInspectionState string

const (
	// RuntimeRepairInspectionMissing means the exact target listener is no longer present.
	RuntimeRepairInspectionMissing RuntimeRepairInspectionState = "missing"
	// RuntimeRepairInspectionAmbiguous means more than one native candidate or scope could own the target.
	RuntimeRepairInspectionAmbiguous RuntimeRepairInspectionState = "ambiguous"
	// RuntimeRepairInspectionForeign means the target exists but does not belong to the daemon user.
	RuntimeRepairInspectionForeign RuntimeRepairInspectionState = "foreign"
	// RuntimeRepairInspectionUnreadable means native facts were incomplete or inaccessible.
	RuntimeRepairInspectionUnreadable RuntimeRepairInspectionState = "unreadable"
	// RuntimeRepairInspectionUnsupported means the current operating system has no reviewed backend.
	RuntimeRepairInspectionUnsupported RuntimeRepairInspectionState = "unsupported"
	// RuntimeRepairInspectionActionable means one stable candidate passed every native correlation check.
	RuntimeRepairInspectionActionable RuntimeRepairInspectionState = "actionable"
)

// Validate rejects states outside the fixed control-protocol vocabulary.
func (state RuntimeRepairInspectionState) Validate() error {
	switch state {
	case RuntimeRepairInspectionMissing,
		RuntimeRepairInspectionAmbiguous,
		RuntimeRepairInspectionForeign,
		RuntimeRepairInspectionUnreadable,
		RuntimeRepairInspectionUnsupported,
		RuntimeRepairInspectionActionable:
		return nil
	default:
		return fmt.Errorf("unknown runtime repair inspection state %q", state)
	}
}

// RuntimeRepairDiagnostic is a bounded, non-native reason suitable for daemon policy and client mapping.
type RuntimeRepairDiagnostic string

const (
	// RuntimeRepairDiagnosticListenerMissing reports that the exact listener disappeared.
	RuntimeRepairDiagnosticListenerMissing RuntimeRepairDiagnostic = "listener_missing"
	// RuntimeRepairDiagnosticCandidateAmbiguous reports that native facts did not select one scope.
	RuntimeRepairDiagnosticCandidateAmbiguous RuntimeRepairDiagnostic = "candidate_ambiguous"
	// RuntimeRepairDiagnosticForeignOwner reports that no same-user scope owns the listener.
	RuntimeRepairDiagnosticForeignOwner RuntimeRepairDiagnostic = "foreign_owner"
	// RuntimeRepairDiagnosticNativeUnreadable reports incomplete or inaccessible native evidence.
	RuntimeRepairDiagnosticNativeUnreadable RuntimeRepairDiagnostic = "native_unreadable"
	// RuntimeRepairDiagnosticPlatformUnsupported reports a platform without a reviewed implementation.
	RuntimeRepairDiagnosticPlatformUnsupported RuntimeRepairDiagnostic = "platform_unsupported"
	// RuntimeRepairDiagnosticCandidateExact reports one fully correlated candidate.
	RuntimeRepairDiagnosticCandidateExact RuntimeRepairDiagnostic = "candidate_exact"
)

// Validate rejects diagnostics outside the bounded public vocabulary.
func (diagnostic RuntimeRepairDiagnostic) Validate() error {
	switch diagnostic {
	case RuntimeRepairDiagnosticListenerMissing,
		RuntimeRepairDiagnosticCandidateAmbiguous,
		RuntimeRepairDiagnosticForeignOwner,
		RuntimeRepairDiagnosticNativeUnreadable,
		RuntimeRepairDiagnosticPlatformUnsupported,
		RuntimeRepairDiagnosticCandidateExact:
		return nil
	default:
		return fmt.Errorf("unknown runtime repair diagnostic %q", diagnostic)
	}
}

// RuntimeRepairDisplay contains only bounded facts safe to show during explicit confirmation.
type RuntimeRepairDisplay struct {
	RootPID      int64
	Command      string
	CheckoutRoot string
	Endpoint     netip.AddrPort
	ProcessCount int
}

// Validate binds display text to fixed or already-validated receipt facts.
func (display RuntimeRepairDisplay) Validate() error {
	if display.RootPID <= 0 {
		return fmt.Errorf("runtime repair display root PID must be positive")
	}
	if display.Command != runtimeRepairCommand && display.Command != runtimeRepairProjectListenerCommand {
		return fmt.Errorf("runtime repair display command must be %q or %q", runtimeRepairCommand, runtimeRepairProjectListenerCommand)
	}
	if display.CheckoutRoot == "" || len(display.CheckoutRoot) > runtimeRepairMaximumTextBytes ||
		!filepath.IsAbs(display.CheckoutRoot) || filepath.Clean(display.CheckoutRoot) != display.CheckoutRoot {
		return fmt.Errorf("runtime repair display checkout root must be a bounded clean absolute path")
	}
	address := display.Endpoint.Addr()
	if !display.Endpoint.IsValid() || display.Endpoint.Port() == 0 || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return fmt.Errorf("runtime repair display endpoint must be canonical IPv4 loopback")
	}
	if display.ProcessCount <= 0 || display.ProcessCount > runtimeRepairMaximumProcesses {
		return fmt.Errorf("runtime repair display process count must be from 1 through %d", runtimeRepairMaximumProcesses)
	}
	return nil
}

// RuntimeRepairCandidate carries client-safe facts plus an opaque process-local native receipt.
type RuntimeRepairCandidate struct {
	Fingerprint string
	Display     RuntimeRepairDisplay
	receipt     *runtimeRepairReceipt
}

// Validate prevents caller-created or caller-mutated candidates from reaching native confirmation.
func (candidate RuntimeRepairCandidate) Validate() error {
	if err := validateRuntimeRepairFingerprint(candidate.Fingerprint); err != nil {
		return err
	}
	if err := candidate.Display.Validate(); err != nil {
		return err
	}
	if candidate.receipt == nil {
		return fmt.Errorf("runtime repair candidate has no process-local receipt")
	}
	if err := candidate.receipt.validate(); err != nil {
		return fmt.Errorf("validate runtime repair candidate receipt: %w", err)
	}
	fingerprint, err := candidate.receipt.observation.fingerprint()
	if err != nil {
		return err
	}
	if fingerprint != candidate.Fingerprint {
		return fmt.Errorf("runtime repair candidate fingerprint does not match its receipt")
	}
	wantDisplay := runtimeRepairDisplay(candidate.receipt.observation)
	if candidate.Display != wantDisplay {
		return fmt.Errorf("runtime repair candidate display does not match its receipt")
	}
	return nil
}

// Clone returns an isolated candidate while preserving its unforgeable process-local receipt.
func (candidate RuntimeRepairCandidate) Clone() RuntimeRepairCandidate {
	clone := candidate
	if candidate.receipt != nil {
		receipt := candidate.receipt.clone()
		clone.receipt = &receipt
	}
	return clone
}

// RuntimeRepairInspection is one fixed-state native inspection result.
type RuntimeRepairInspection struct {
	State      RuntimeRepairInspectionState
	Diagnostic RuntimeRepairDiagnostic
	Candidate  *RuntimeRepairCandidate
}

// Validate enforces that only an actionable inspection contains a candidate.
func (inspection RuntimeRepairInspection) Validate() error {
	if err := inspection.State.Validate(); err != nil {
		return err
	}
	if err := inspection.Diagnostic.Validate(); err != nil {
		return err
	}
	if runtimeRepairDiagnosticForState(inspection.State) != inspection.Diagnostic {
		return fmt.Errorf("runtime repair inspection diagnostic does not match state %q", inspection.State)
	}
	if inspection.State != RuntimeRepairInspectionActionable {
		if inspection.Candidate != nil {
			return fmt.Errorf("non-actionable runtime repair inspection contains a candidate")
		}
		return nil
	}
	if inspection.Candidate == nil {
		return fmt.Errorf("actionable runtime repair inspection has no candidate")
	}
	return inspection.Candidate.Validate()
}

// RuntimeRepairConfirmationState describes the terminal result of one explicit confirmation.
type RuntimeRepairConfirmationState string

const (
	// RuntimeRepairConfirmationSettled means SIGTERM was delivered and every captured birth and socket left.
	RuntimeRepairConfirmationSettled RuntimeRepairConfirmationState = "settled"
	// RuntimeRepairConfirmationDrifted means exact revalidation failed and Harbor emitted no signal.
	RuntimeRepairConfirmationDrifted RuntimeRepairConfirmationState = "drifted"
	// RuntimeRepairConfirmationFailed means observation, signaling, or post-signal convergence failed.
	RuntimeRepairConfirmationFailed RuntimeRepairConfirmationState = "failed"
)

// RuntimeRepairConfirmation distinguishes zero-signal failure from post-signal nonconvergence.
type RuntimeRepairConfirmation struct {
	State    RuntimeRepairConfirmationState
	Signaled bool
}

// Validate enforces the signal semantics promised by each confirmation state.
func (confirmation RuntimeRepairConfirmation) Validate() error {
	switch confirmation.State {
	case RuntimeRepairConfirmationSettled:
		if !confirmation.Signaled {
			return fmt.Errorf("settled runtime repair confirmation must record graceful signaling")
		}
	case RuntimeRepairConfirmationDrifted:
		if confirmation.Signaled {
			return fmt.Errorf("drifted runtime repair confirmation cannot record a signal")
		}
	case RuntimeRepairConfirmationFailed:
	default:
		return fmt.Errorf("unknown runtime repair confirmation state %q", confirmation.State)
	}
	return nil
}

// RuntimeRepairer inspects and gracefully confirms one process-local native candidate receipt.
type RuntimeRepairer interface {
	Inspect(context.Context, RuntimeRepairTarget) (RuntimeRepairInspection, error)
	Confirm(context.Context, RuntimeRepairCandidate) (RuntimeRepairConfirmation, error)
}

// runtimeRepairAdapter applies platform-neutral validation, receipt, and convergence policy.
type runtimeRepairAdapter struct {
	control          runtimeRepairControl
	settlementPeriod time.Duration
	settlementPoll   time.Duration
}

// runtimeRepairControl isolates native observation and the only permitted graceful effect.
type runtimeRepairControl struct {
	inspect  func(context.Context, RuntimeRepairTarget) (runtimeRepairNativeInspection, error)
	graceful func(context.Context, runtimeRepairReceipt) (bool, error)
	force    func(context.Context, runtimeRepairReceipt) (bool, error)
	settled  func(context.Context, runtimeRepairReceipt) (bool, error)
}

// NewRuntimeRepairer selects the reviewed platform backend without exposing native process authority.
func NewRuntimeRepairer() RuntimeRepairer {
	return newRuntimeRepairerWithControl(newRuntimeRepairControl(), runtimeRepairSettlementPeriod, runtimeRepairSettlementPoll)
}

// newRuntimeRepairerWithControl constructs the deterministic policy seam used by unit tests.
func newRuntimeRepairerWithControl(control runtimeRepairControl, settlementPeriod, settlementPoll time.Duration) *runtimeRepairAdapter {
	if control.inspect == nil || control.graceful == nil || control.force == nil || control.settled == nil {
		panic("projectprocess runtime repair requires complete native control")
	}
	if settlementPeriod <= 0 || settlementPoll <= 0 {
		panic("projectprocess runtime repair requires positive settlement bounds")
	}
	return &runtimeRepairAdapter{control: control, settlementPeriod: settlementPeriod, settlementPoll: settlementPoll}
}

// Inspect returns only a fixed classification or one validated opaque candidate.
func (repairer *runtimeRepairAdapter) Inspect(ctx context.Context, target RuntimeRepairTarget) (RuntimeRepairInspection, error) {
	if repairer == nil {
		panic("projectprocess runtime repair Inspect requires a non-nil receiver")
	}
	ctx = normalizeRuntimeRepairContext(ctx)
	if err := ctx.Err(); err != nil {
		return RuntimeRepairInspection{}, err
	}
	if err := target.Validate(); err != nil {
		return RuntimeRepairInspection{}, err
	}
	native, err := repairer.control.inspect(ctx, target)
	if err != nil {
		return RuntimeRepairInspection{}, err
	}
	inspection, err := runtimeRepairInspection(native)
	if err != nil {
		return RuntimeRepairInspection{}, err
	}
	return inspection, nil
}

// Confirm reobserves exact equality before invoking the backend's root-only SIGTERM boundary.
func (repairer *runtimeRepairAdapter) Confirm(ctx context.Context, candidate RuntimeRepairCandidate) (RuntimeRepairConfirmation, error) {
	if repairer == nil {
		panic("projectprocess runtime repair Confirm requires a non-nil receiver")
	}
	ctx = normalizeRuntimeRepairContext(ctx)
	if err := ctx.Err(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	candidate = candidate.Clone()
	if err := candidate.Validate(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	receipt := candidate.receipt.clone()
	current, err := repairer.control.inspect(ctx, receipt.observation.Target)
	if err != nil {
		return runtimeRepairFailed(false, err)
	}
	if current.State != RuntimeRepairInspectionActionable || current.Observation == nil ||
		!runtimeRepairObservationsEqual(*current.Observation, receipt.observation) {
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationDrifted}, nil
	}
	if err := ctx.Err(); err != nil {
		return runtimeRepairFailed(false, err)
	}
	signaled, err := repairer.control.graceful(ctx, receipt)
	if errors.Is(err, ErrRuntimeRepairDrift) {
		if signaled {
			return runtimeRepairFailed(true, fmt.Errorf("runtime repair backend reported drift after signaling: %w", err))
		}
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationDrifted}, nil
	}
	if err != nil {
		return runtimeRepairFailed(signaled, err)
	}
	if !signaled {
		return runtimeRepairFailed(false, fmt.Errorf("runtime repair backend returned without signaling or drift"))
	}
	settled, err := repairer.waitForSettlement(ctx, receipt)
	if err != nil {
		return runtimeRepairFailed(true, err)
	}
	if settled {
		return RuntimeRepairConfirmation{State: RuntimeRepairConfirmationSettled, Signaled: true}, nil
	}

	// A confirmed repair owns one exact session scope, so a bounded SIGKILL escalation can finish a
	// process tree whose root ignored SIGTERM without widening the user's explicit confirmation.
	forced, err := repairer.control.force(ctx, receipt)
	if errors.Is(err, ErrRuntimeRepairDrift) {
		return runtimeRepairFailed(true, fmt.Errorf("runtime repair scope drifted during forceful settlement: %w", err))
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

// waitForSettlement polls until both captured births and the exact socket are absent within one hard bound.
func (repairer *runtimeRepairAdapter) waitForSettlement(ctx context.Context, receipt runtimeRepairReceipt) (bool, error) {
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

// runtimeRepairFailed constructs and validates the explicit failure/signal pairing.
func runtimeRepairFailed(signaled bool, err error) (RuntimeRepairConfirmation, error) {
	confirmation := RuntimeRepairConfirmation{State: RuntimeRepairConfirmationFailed, Signaled: signaled}
	return confirmation, err
}

// runtimeRepairNativeInspection retains native-only evidence without exposing it to clients.
type runtimeRepairNativeInspection struct {
	State       RuntimeRepairInspectionState
	Observation *runtimeRepairNativeObservation
}

// validate enforces that only actionable native state carries authority facts.
func (inspection runtimeRepairNativeInspection) validate() error {
	if err := inspection.State.Validate(); err != nil {
		return err
	}
	if inspection.State != RuntimeRepairInspectionActionable {
		if inspection.Observation != nil {
			return fmt.Errorf("non-actionable native runtime inspection contains authority facts")
		}
		return nil
	}
	if inspection.Observation == nil {
		return fmt.Errorf("actionable native runtime inspection is missing authority facts")
	}
	return inspection.Observation.validate()
}

// runtimeRepairReceipt is the immutable native authority retained only in daemon memory.
type runtimeRepairReceipt struct {
	observation runtimeRepairNativeObservation
}

// validate rejects receipts that cannot authorize exact reobservation.
func (receipt runtimeRepairReceipt) validate() error {
	return receipt.observation.validate()
}

// clone isolates every native fact slice held by one process-local plan.
func (receipt runtimeRepairReceipt) clone() runtimeRepairReceipt {
	return runtimeRepairReceipt{observation: receipt.observation.clone()}
}

// runtimeRepairProcessFact contains every process fact revalidated before graceful termination.
type runtimeRepairProcessFact struct {
	PID                int
	BirthToken         string
	ParentPID          int
	ProcessGroupID     int
	SessionID          int
	EffectiveUID       uint32
	RealUID            uint32
	ExecutableIdentity string
	ArgumentDigest     string
	ArgumentCount      int
	CommandExact       bool
	WorkingDirectory   string
}

// validate rejects incomplete or unbounded process identity.
func (fact runtimeRepairProcessFact) validate() error {
	if fact.PID <= 0 || fact.ParentPID < 0 || fact.ProcessGroupID <= 0 || fact.SessionID <= 0 {
		return fmt.Errorf("runtime repair process identifiers are incomplete")
	}
	if err := validateRuntimeRepairText("process birth token", fact.BirthToken); err != nil {
		return err
	}
	if err := validateRuntimeRepairPath("process executable", fact.ExecutableIdentity); err != nil {
		return err
	}
	if fact.ArgumentCount <= 0 || fact.ArgumentCount > runtimeRepairMaximumArguments {
		return fmt.Errorf("runtime repair process argument count must be from 1 through %d", runtimeRepairMaximumArguments)
	}
	if err := validateRuntimeRepairFingerprint(fact.ArgumentDigest); err != nil {
		return fmt.Errorf("runtime repair process argument digest: %w", err)
	}
	return validateRuntimeRepairPath("process working directory", fact.WorkingDirectory)
}

// runtimeRepairParentFact binds the dedicated root's external parent against reparenting or PID reuse.
type runtimeRepairParentFact struct {
	PID        int
	BirthToken string
}

// validate rejects an absent or unbounded parent identity.
func (fact runtimeRepairParentFact) validate() error {
	if fact.PID <= 0 {
		return fmt.Errorf("runtime repair root parent PID must be positive")
	}
	return validateRuntimeRepairText("runtime repair root parent birth token", fact.BirthToken)
}

// runtimeRepairSocketFact binds one exact listening file descriptor to one member birth.
type runtimeRepairSocketFact struct {
	OwnerPID        int
	OwnerBirthToken string
	FileDescriptor  int
	SocketHandle    uint64
	PCBHandle       uint64
	Generation      uint64
	Endpoint        netip.AddrPort
}

// validate rejects socket facts that cannot identify one exact TCP listener.
func (fact runtimeRepairSocketFact) validate() error {
	if fact.OwnerPID <= 0 || fact.FileDescriptor < 0 || fact.SocketHandle == 0 || fact.PCBHandle == 0 || fact.Generation == 0 {
		return fmt.Errorf("runtime repair socket identity is incomplete")
	}
	if err := validateRuntimeRepairText("runtime repair socket owner birth token", fact.OwnerBirthToken); err != nil {
		return err
	}
	address := fact.Endpoint.Addr()
	if !fact.Endpoint.IsValid() || fact.Endpoint.Port() == 0 || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return fmt.Errorf("runtime repair socket endpoint must be canonical IPv4 loopback")
	}
	return nil
}

// runtimeRepairNativeObservation is the complete actionable Darwin scope stored in a receipt.
type runtimeRepairNativeObservation struct {
	Target     RuntimeRepairTarget
	DaemonUID  uint32
	Root       runtimeRepairProcessFact
	RootParent runtimeRepairParentFact
	Members    []runtimeRepairProcessFact
	Listener   runtimeRepairSocketFact
}

// validate proves one sorted, same-user, dedicated session tree owns the exact target listener.
func (observation runtimeRepairNativeObservation) validate() error {
	if err := observation.Target.Validate(); err != nil {
		return err
	}
	if err := observation.Root.validate(); err != nil {
		return fmt.Errorf("runtime repair root: %w", err)
	}
	if err := observation.RootParent.validate(); err != nil {
		return err
	}
	if err := observation.Listener.validate(); err != nil {
		return err
	}
	if len(observation.Members) == 0 || len(observation.Members) > runtimeRepairMaximumProcesses {
		return fmt.Errorf("runtime repair session member count must be from 1 through %d", runtimeRepairMaximumProcesses)
	}
	if observation.Root.PID != observation.Root.SessionID || observation.Root.PID != observation.Root.ProcessGroupID ||
		observation.Root.ParentPID != observation.RootParent.PID {
		return fmt.Errorf("runtime repair root does not define one dedicated session, process group, and parent boundary")
	}
	if observation.Root.EffectiveUID != observation.DaemonUID || observation.Root.RealUID != observation.DaemonUID {
		return fmt.Errorf("runtime repair root is not owned exclusively by the daemon user")
	}
	if observation.Root.WorkingDirectory != observation.Target.CheckoutRoot {
		return fmt.Errorf("runtime repair root working directory does not match the target checkout")
	}
	if filepath.Base(observation.Root.ExecutableIdentity) != "forj" || observation.Root.ArgumentCount != 2 || !observation.Root.CommandExact {
		return fmt.Errorf("runtime repair root command is not exactly forj dev")
	}
	if observation.Listener.Endpoint != observation.Target.Endpoint {
		return fmt.Errorf("runtime repair listener does not match the target endpoint")
	}
	memberByPID := make(map[int]runtimeRepairProcessFact, len(observation.Members))
	for index, member := range observation.Members {
		if err := member.validate(); err != nil {
			return fmt.Errorf("runtime repair session member %d: %w", index, err)
		}
		if index > 0 && observation.Members[index-1].PID >= member.PID {
			return fmt.Errorf("runtime repair session members are not in unique PID order")
		}
		if member.SessionID != observation.Root.SessionID || member.EffectiveUID != observation.DaemonUID || member.RealUID != observation.DaemonUID {
			return fmt.Errorf("runtime repair session member %d escaped the exact user session", member.PID)
		}
		memberByPID[member.PID] = member
	}
	root, found := memberByPID[observation.Root.PID]
	if !found || !reflect.DeepEqual(root, observation.Root) {
		return fmt.Errorf("runtime repair session does not contain its exact root")
	}
	if _, parentInsideSession := memberByPID[observation.RootParent.PID]; parentInsideSession {
		return fmt.Errorf("runtime repair dedicated root parent remains inside the observed session")
	}
	owner, found := memberByPID[observation.Listener.OwnerPID]
	if !found || owner.BirthToken != observation.Listener.OwnerBirthToken {
		return fmt.Errorf("runtime repair listener owner is not one exact session member")
	}
	for _, member := range observation.Members {
		if member.PID == observation.Root.PID {
			continue
		}
		if !runtimeRepairDescendsFromRoot(member.PID, observation.Root.PID, memberByPID) {
			return fmt.Errorf("runtime repair session member %d is not in the root process tree", member.PID)
		}
	}
	return nil
}

// clone isolates process slices before a receipt crosses a coordinator boundary.
func (observation runtimeRepairNativeObservation) clone() runtimeRepairNativeObservation {
	clone := observation
	clone.Root = observation.Root.clone()
	clone.Members = make([]runtimeRepairProcessFact, len(observation.Members))
	for index, member := range observation.Members {
		clone.Members[index] = member.clone()
	}
	return clone
}

// fingerprint returns a domain-separated digest over every native authority fact.
func (observation runtimeRepairNativeObservation) fingerprint() (string, error) {
	if err := observation.validate(); err != nil {
		return "", fmt.Errorf("fingerprint runtime repair observation: %w", err)
	}
	payload := append([]byte(nil), runtimeRepairFingerprintDomain...)
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

// runtimeRepairInspection converts native state without forwarding native diagnostics.
func runtimeRepairInspection(native runtimeRepairNativeInspection) (RuntimeRepairInspection, error) {
	if err := native.validate(); err != nil {
		return RuntimeRepairInspection{}, err
	}
	inspection := RuntimeRepairInspection{
		State:      native.State,
		Diagnostic: runtimeRepairDiagnosticForState(native.State),
	}
	if native.State == RuntimeRepairInspectionActionable {
		observation := native.Observation.clone()
		fingerprint, err := observation.fingerprint()
		if err != nil {
			return RuntimeRepairInspection{}, err
		}
		receipt := runtimeRepairReceipt{observation: observation}
		candidate := RuntimeRepairCandidate{
			Fingerprint: fingerprint,
			Display:     runtimeRepairDisplay(observation),
			receipt:     &receipt,
		}
		inspection.Candidate = &candidate
	}
	if err := inspection.Validate(); err != nil {
		return RuntimeRepairInspection{}, err
	}
	return inspection, nil
}

// runtimeRepairDisplay derives all public facts from one validated native observation.
func runtimeRepairDisplay(observation runtimeRepairNativeObservation) RuntimeRepairDisplay {
	return RuntimeRepairDisplay{
		RootPID:      int64(observation.Root.PID),
		Command:      runtimeRepairCommand,
		CheckoutRoot: observation.Target.CheckoutRoot,
		Endpoint:     observation.Target.Endpoint,
		ProcessCount: len(observation.Members),
	}
}

// runtimeRepairDiagnosticForState maps every state without parsing native errors.
func runtimeRepairDiagnosticForState(state RuntimeRepairInspectionState) RuntimeRepairDiagnostic {
	switch state {
	case RuntimeRepairInspectionMissing:
		return RuntimeRepairDiagnosticListenerMissing
	case RuntimeRepairInspectionAmbiguous:
		return RuntimeRepairDiagnosticCandidateAmbiguous
	case RuntimeRepairInspectionForeign:
		return RuntimeRepairDiagnosticForeignOwner
	case RuntimeRepairInspectionUnreadable:
		return RuntimeRepairDiagnosticNativeUnreadable
	case RuntimeRepairInspectionUnsupported:
		return RuntimeRepairDiagnosticPlatformUnsupported
	case RuntimeRepairInspectionActionable:
		return RuntimeRepairDiagnosticCandidateExact
	default:
		return ""
	}
}

// runtimeRepairListenerCardinality classifies global listener shape before process ownership can refine it.
func runtimeRepairListenerCardinality(exactListeners, conflictingBinds int) RuntimeRepairInspectionState {
	if exactListeners < 0 || conflictingBinds < 0 || exactListeners > runtimeRepairMaximumProcesses || conflictingBinds > runtimeRepairMaximumProcesses {
		return RuntimeRepairInspectionUnreadable
	}
	if exactListeners == 0 && conflictingBinds == 0 {
		return RuntimeRepairInspectionMissing
	}
	if exactListeners != 1 || conflictingBinds != 0 {
		return RuntimeRepairInspectionAmbiguous
	}
	return RuntimeRepairInspectionActionable
}

// runtimeRepairProjectListenerPresence admits any bounded listener evidence for later same-user ownership correlation.
//
// A single process can expose one port through multiple exact, wildcard, or dual-stack records. The native backend
// must correlate those records to socket owners before deciding whether the scope is unique; counting endpoint rows
// here would reject a safe one-process repair before that proof exists.
func runtimeRepairProjectListenerPresence(exactListeners, conflictingBinds int) RuntimeRepairInspectionState {
	if exactListeners < 0 || conflictingBinds < 0 || exactListeners > runtimeRepairMaximumProcesses || conflictingBinds > runtimeRepairMaximumProcesses {
		return RuntimeRepairInspectionUnreadable
	}
	if exactListeners == 0 && conflictingBinds == 0 {
		return RuntimeRepairInspectionMissing
	}
	return RuntimeRepairInspectionActionable
}

// runtimeRepairProjectListenerOwnerCardinality classifies the number of distinct native socket owners after correlation.
func runtimeRepairProjectListenerOwnerCardinality(ownerCount int) RuntimeRepairInspectionState {
	if ownerCount < 0 || ownerCount > runtimeRepairMaximumProcesses {
		return RuntimeRepairInspectionUnreadable
	}
	if ownerCount == 0 {
		return RuntimeRepairInspectionMissing
	}
	if ownerCount != 1 {
		return RuntimeRepairInspectionAmbiguous
	}
	return RuntimeRepairInspectionActionable
}

// runtimeRepairObservationsEqual requires structural equality in addition to matching digests.
func runtimeRepairObservationsEqual(left, right runtimeRepairNativeObservation) bool {
	leftFingerprint, leftErr := left.fingerprint()
	rightFingerprint, rightErr := right.fingerprint()
	return leftErr == nil && rightErr == nil && leftFingerprint == rightFingerprint && reflect.DeepEqual(left, right)
}

// runtimeRepairDescendsFromRoot rejects parent cycles and paths that leave the observed session tree.
func runtimeRepairDescendsFromRoot(pid, rootPID int, members map[int]runtimeRepairProcessFact) bool {
	seen := make(map[int]struct{}, len(members))
	for pid != rootPID {
		if _, duplicate := seen[pid]; duplicate {
			return false
		}
		seen[pid] = struct{}{}
		member, found := members[pid]
		if !found || member.ParentPID <= 0 {
			return false
		}
		pid = member.ParentPID
	}
	return true
}

// runtimeRepairArgumentEvidence immediately reduces exact argv text to bounded receipt evidence.
func runtimeRepairArgumentEvidence(arguments []string) (string, int, bool, error) {
	return runtimeRepairArgumentEvidenceForExecutable("", arguments)
}

// runtimeRepairArgumentEvidenceForExecutable reduces argv while binding the exact root command to its observed executable.
func runtimeRepairArgumentEvidenceForExecutable(executable string, arguments []string) (string, int, bool, error) {
	if len(arguments) == 0 || len(arguments) > runtimeRepairMaximumArguments {
		return "", 0, false, fmt.Errorf("runtime repair argument count must be from 1 through %d", runtimeRepairMaximumArguments)
	}
	hash := sha256.New()
	count := binary.AppendUvarint(nil, uint64(len(arguments)))
	_, _ = hash.Write(count)
	for index, argument := range arguments {
		if argument == "" || len(argument) > runtimeRepairMaximumTextBytes || strings.IndexByte(argument, 0) >= 0 {
			return "", 0, false, fmt.Errorf("runtime repair argument %d must be non-empty, bounded, and contain no NUL", index)
		}
		length := binary.AppendUvarint(nil, uint64(len(argument)))
		_, _ = hash.Write(length)
		_, _ = hash.Write([]byte(argument))
	}
	commandExact := len(arguments) == 2 && arguments[1] == "dev" &&
		((executable != "" && arguments[0] == executable) || (executable == "" && filepath.Base(arguments[0]) == "forj"))
	return hex.EncodeToString(hash.Sum(nil)), len(arguments), commandExact, nil
}

// clone preserves the process fact seam if future bounded native fields require deep copies.
func (fact runtimeRepairProcessFact) clone() runtimeRepairProcessFact {
	return fact
}

// validateRuntimeRepairText rejects empty, unbounded, malformed, or padded native identity text.
func validateRuntimeRepairText(name, value string) error {
	if value == "" || len(value) > runtimeRepairMaximumTextBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be non-empty, bounded, and have no surrounding whitespace", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

// validateRuntimeRepairPath rejects aliases and relative paths in native process facts.
func validateRuntimeRepairPath(name, value string) error {
	if err := validateRuntimeRepairText(name, value); err != nil {
		return err
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a clean absolute path", name)
	}
	return nil
}

// validateRuntimeRepairFingerprint requires canonical lowercase SHA-256 text.
func validateRuntimeRepairFingerprint(value string) error {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return fmt.Errorf("runtime repair fingerprint must be a lowercase SHA-256 digest")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("runtime repair fingerprint must be a lowercase SHA-256 digest")
	}
	return nil
}

// runtimeRepairAppendProcess encodes every revalidated process fact in fixed field order.
func runtimeRepairAppendProcess(destination []byte, fact runtimeRepairProcessFact) []byte {
	destination = binary.AppendVarint(destination, int64(fact.PID))
	destination = runtimeRepairAppendString(destination, fact.BirthToken)
	destination = binary.AppendVarint(destination, int64(fact.ParentPID))
	destination = binary.AppendVarint(destination, int64(fact.ProcessGroupID))
	destination = binary.AppendVarint(destination, int64(fact.SessionID))
	destination = binary.AppendUvarint(destination, uint64(fact.EffectiveUID))
	destination = binary.AppendUvarint(destination, uint64(fact.RealUID))
	destination = runtimeRepairAppendString(destination, fact.ExecutableIdentity)
	destination = runtimeRepairAppendString(destination, fact.ArgumentDigest)
	destination = binary.AppendVarint(destination, int64(fact.ArgumentCount))
	if fact.CommandExact {
		destination = append(destination, 1)
	} else {
		destination = append(destination, 0)
	}
	return runtimeRepairAppendString(destination, fact.WorkingDirectory)
}

// runtimeRepairAppendAddressPort encodes the fixed IPv4 address bytes before its port.
func runtimeRepairAppendAddressPort(destination []byte, endpoint netip.AddrPort) []byte {
	address := endpoint.Addr().As4()
	destination = append(destination, address[:]...)
	return binary.AppendUvarint(destination, uint64(endpoint.Port()))
}

// runtimeRepairAppendString prevents adjacent variable-length facts from colliding.
func runtimeRepairAppendString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// normalizeRuntimeRepairContext keeps nil callers on the cancellable code path.
func normalizeRuntimeRepairContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
