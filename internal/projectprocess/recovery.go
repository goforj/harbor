package projectprocess

import (
	"context"
	"fmt"

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
	if err := evidence.Validate(); err != nil {
		return PriorProcessObservation{}, fmt.Errorf("validate prior process evidence: %w", err)
	}
	pid := int(evidence.PID)
	if int64(pid) != evidence.PID {
		return PriorProcessObservation{}, fmt.Errorf("prior process PID %d exceeds this platform's integer range", evidence.PID)
	}

	birthToken, present, err := observeProcessBirthToken(pid)
	if err != nil {
		return PriorProcessObservation{}, fmt.Errorf("observe prior process birth: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PriorProcessObservation{}, err
	}
	if !present {
		return PriorProcessObservation{State: PriorProcessAbsent}, nil
	}
	if birthToken != evidence.BirthToken {
		return PriorProcessObservation{State: PriorProcessReplaced}, nil
	}
	return PriorProcessObservation{State: PriorProcessPresent}, nil
}
