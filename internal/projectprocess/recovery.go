package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// PriorProcessState classifies whether persisted process-birth authority still names the same host process.
type PriorProcessState string

const (
	// PriorProcessAbsent means the persisted PID no longer identifies a host process.
	PriorProcessAbsent PriorProcessState = "absent"
	// PriorProcessReplaced means the persisted PID has been reused by a different process birth.
	PriorProcessReplaced PriorProcessState = "replaced"
	// PriorProcessPresent means the persisted PID and birth token still match and require fuller reconciliation.
	PriorProcessPresent PriorProcessState = "present"
)

// PriorProcessObservation contains the conservative process-birth result used during daemon restart recovery.
type PriorProcessObservation struct {
	State PriorProcessState
}

// PriorProcessSettlementOutcome describes how persisted process authority became safe to retire.
type PriorProcessSettlementOutcome string

const (
	// PriorProcessSettlementAbsent means the persisted process was already gone before Harbor signaled it.
	PriorProcessSettlementAbsent PriorProcessSettlementOutcome = "absent"
	// PriorProcessSettlementReplaced means the persisted PID names another birth and was never signaled.
	PriorProcessSettlementReplaced PriorProcessSettlementOutcome = "replaced"
	// PriorProcessSettlementTerminated means Harbor signaled the exact persisted process and observed that birth leave.
	PriorProcessSettlementTerminated PriorProcessSettlementOutcome = "terminated"
)

// PriorProcessSettlement reports the successful terminal outcome for one persisted process birth.
type PriorProcessSettlement struct {
	Outcome PriorProcessSettlementOutcome
}

// priorProcessRecoveryControl isolates platform observation and signaling for deterministic recovery tests.
type priorProcessRecoveryControl struct {
	observe      func(int) (string, bool, error)
	observeScope func(int, string) (PriorProcessState, error)
	graceful     func(int, string) (PriorProcessState, error)
	force        func(int, string) (PriorProcessState, error)
}

// ObservePriorProcess determines whether the exact persisted process birth can still be present.
func (supervisor *Supervisor) ObservePriorProcess(
	ctx context.Context,
	evidence domain.ProcessEvidence,
) (PriorProcessObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessObservation{}, err
	}
	pid, err := validatePriorProcessEvidence(evidence)
	if err != nil {
		return PriorProcessObservation{}, err
	}

	state, err := observePriorProcessState(pid, hostProcessBirthToken(evidence.BirthToken), observeProcessBirthToken)
	if err != nil {
		return PriorProcessObservation{}, fmt.Errorf("observe prior process birth: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessObservation{}, err
	}
	return PriorProcessObservation{State: state}, nil
}

// SettlePriorProcess safely retires one exact persisted process birth during daemon recovery.
func (supervisor *Supervisor) SettlePriorProcess(
	ctx context.Context,
	evidence domain.ProcessEvidence,
) (PriorProcessSettlement, error) {
	return settlePriorProcess(ctx, evidence, supervisor.gracePeriod, newPriorProcessRecoveryControl())
}

// settlePriorProcess keeps recovery policy platform-neutral while requiring every signal seam to verify the birth again.
func settlePriorProcess(
	ctx context.Context,
	evidence domain.ProcessEvidence,
	gracePeriod time.Duration,
	control priorProcessRecoveryControl,
) (PriorProcessSettlement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessSettlement{}, err
	}
	pid, err := validatePriorProcessEvidence(evidence)
	if err != nil {
		return PriorProcessSettlement{}, err
	}
	if err := validatePriorProcessRecoveryControl(control); err != nil {
		return PriorProcessSettlement{}, err
	}
	state, err := observePriorProcessRecoveryState(pid, evidence.BirthToken, control)
	if err != nil {
		return PriorProcessSettlement{}, fmt.Errorf("observe prior process before settlement: %w", err)
	}
	if settlement, settled := priorProcessAlreadySettled(state); settled {
		return settlement, nil
	}

	state, gracefulErr := control.graceful(pid, evidence.BirthToken)
	if gracefulErr == nil {
		if settlement, settled := priorProcessAlreadySettled(state); settled {
			return settlement, nil
		}
		state, err = waitForPriorProcessSettlement(ctx, pid, evidence.BirthToken, gracePeriod, control)
		if err == nil && state != PriorProcessPresent {
			return PriorProcessSettlement{Outcome: PriorProcessSettlementTerminated}, nil
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return PriorProcessSettlement{}, err
			}
			return PriorProcessSettlement{}, fmt.Errorf("observe prior process during graceful settlement: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessSettlement{}, err
	}

	state, forceErr := control.force(pid, evidence.BirthToken)
	if forceErr != nil {
		return PriorProcessSettlement{}, errors.Join(
			wrapPriorProcessSignalError("request graceful prior process settlement", gracefulErr),
			fmt.Errorf("force prior process settlement: %w", forceErr),
		)
	}
	if state != PriorProcessPresent {
		if err := ctx.Err(); err != nil {
			return PriorProcessSettlement{}, err
		}
		if settlement, settled := priorProcessAlreadySettled(state); settled {
			return settlement, nil
		}
		return PriorProcessSettlement{}, fmt.Errorf("force prior process settlement returned unsupported state %q", state)
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessSettlement{}, err
	}

	state, err = waitForPriorProcessSettlement(ctx, pid, evidence.BirthToken, forceSettlementPeriod, control)
	if err != nil {
		return PriorProcessSettlement{}, fmt.Errorf("observe prior process after forceful settlement: %w", err)
	}
	if state == PriorProcessPresent {
		return PriorProcessSettlement{}, fmt.Errorf(
			"prior process PID %d birth remained active %s after forceful settlement",
			evidence.PID,
			forceSettlementPeriod,
		)
	}
	return PriorProcessSettlement{Outcome: PriorProcessSettlementTerminated}, nil
}

// validatePriorProcessEvidence converts the persisted PID only after the complete authority record passes validation.
func validatePriorProcessEvidence(evidence domain.ProcessEvidence) (int, error) {
	if err := evidence.Validate(); err != nil {
		return 0, fmt.Errorf("validate prior process evidence: %w", err)
	}
	pid := int(evidence.PID)
	if int64(pid) != evidence.PID {
		return 0, fmt.Errorf("prior process PID %d exceeds this platform's integer range", evidence.PID)
	}
	return pid, nil
}

// validatePriorProcessRecoveryControl fails closed when a production or test seam cannot uphold the settlement contract.
func validatePriorProcessRecoveryControl(control priorProcessRecoveryControl) error {
	if control.observe == nil || control.graceful == nil || control.force == nil {
		return fmt.Errorf("prior process recovery control is incomplete")
	}
	return nil
}

// observePriorProcessState classifies one PID against immutable process-birth evidence.
func observePriorProcessState(
	pid int,
	expectedBirth string,
	observe func(int) (string, bool, error),
) (PriorProcessState, error) {
	birthToken, present, err := observe(pid)
	if err != nil {
		return "", err
	}
	if !present {
		return PriorProcessAbsent, nil
	}
	if birthToken != expectedBirth {
		return PriorProcessReplaced, nil
	}
	return PriorProcessPresent, nil
}

// observePriorProcessRecoveryState extends exact-root observation when the platform owns a durable descendant scope.
func observePriorProcessRecoveryState(
	pid int,
	expectedBirth string,
	control priorProcessRecoveryControl,
) (PriorProcessState, error) {
	if control.observeScope != nil {
		return control.observeScope(pid, expectedBirth)
	}
	return observePriorProcessState(pid, expectedBirth, control.observe)
}

// priorProcessAlreadySettled maps nonsignal observations onto successful durable recovery outcomes.
func priorProcessAlreadySettled(state PriorProcessState) (PriorProcessSettlement, bool) {
	switch state {
	case PriorProcessAbsent:
		return PriorProcessSettlement{Outcome: PriorProcessSettlementAbsent}, true
	case PriorProcessReplaced:
		return PriorProcessSettlement{Outcome: PriorProcessSettlementReplaced}, true
	default:
		return PriorProcessSettlement{}, false
	}
}

// signalPriorProcessIfExact prevents a platform signal after the persisted PID disappears or changes birth.
func signalPriorProcessIfExact(
	pid int,
	expectedBirth string,
	observe func(int) (string, bool, error),
	signal func(int) error,
) (PriorProcessState, error) {
	state, err := observePriorProcessState(pid, expectedBirth, observe)
	if err != nil || state != PriorProcessPresent {
		return state, err
	}
	if err := signal(pid); err != nil {
		return PriorProcessPresent, err
	}
	return PriorProcessPresent, nil
}

// waitForPriorProcessBirthChange observes until the exact birth leaves or the bounded interval expires.
func waitForPriorProcessBirthChange(
	ctx context.Context,
	pid int,
	expectedBirth string,
	limit time.Duration,
	observe func(int) (string, bool, error),
) (PriorProcessState, error) {
	if limit <= 0 {
		return observePriorProcessState(pid, expectedBirth, observe)
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	ticker := time.NewTicker(priorProcessSettlementPollPeriod(limit))
	defer ticker.Stop()

	for {
		state, err := observePriorProcessState(pid, expectedBirth, observe)
		if err != nil || state != PriorProcessPresent {
			return state, err
		}
		select {
		case <-ctx.Done():
			return PriorProcessPresent, ctx.Err()
		case <-timer.C:
			return observePriorProcessState(pid, expectedBirth, observe)
		case <-ticker.C:
		}
	}
}

// waitForPriorProcessSettlement observes the complete platform ownership scope when one is available.
func waitForPriorProcessSettlement(
	ctx context.Context,
	pid int,
	expectedBirth string,
	limit time.Duration,
	control priorProcessRecoveryControl,
) (PriorProcessState, error) {
	if control.observeScope == nil {
		return waitForPriorProcessBirthChange(ctx, pid, expectedBirth, limit, control.observe)
	}
	if limit <= 0 {
		return observePriorProcessRecoveryState(pid, expectedBirth, control)
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	ticker := time.NewTicker(priorProcessSettlementPollPeriod(limit))
	defer ticker.Stop()

	for {
		state, err := observePriorProcessRecoveryState(pid, expectedBirth, control)
		if err != nil || state != PriorProcessPresent {
			return state, err
		}
		select {
		case <-ctx.Done():
			return PriorProcessPresent, ctx.Err()
		case <-timer.C:
			return observePriorProcessRecoveryState(pid, expectedBirth, control)
		case <-ticker.C:
		}
	}
}

// priorProcessSettlementPollPeriod preserves responsive observations without spinning for short test and production bounds.
func priorProcessSettlementPollPeriod(limit time.Duration) time.Duration {
	if limit < forceSettlementPoll {
		return limit
	}
	return forceSettlementPoll
}

// wrapPriorProcessSignalError omits an optional graceful failure when forceful settlement is the only failing action.
func wrapPriorProcessSignalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
