//go:build darwin

package projectprocess

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

// observePersistedOutputBrokerProcessEvidence uses the reviewed libproc readers already used by Darwin recovery.
func observePersistedOutputBrokerProcessEvidence(pid int64) (domain.ProcessEvidence, error) {
	if pid <= 0 || int64(int(pid)) != pid {
		return domain.ProcessEvidence{}, fmt.Errorf("output broker process PID %d is outside this platform's range", pid)
	}
	birthToken, present, err := observeProcessBirthToken(int(pid))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("observe output broker process birth: %w", err)
	}
	if !present {
		return domain.ProcessEvidence{}, fmt.Errorf("observe output broker process birth: process is absent")
	}
	executable, err := observeDarwinRuntimeRepairExecutable(int(pid))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("read output broker executable: %w", err)
	}
	arguments, err := observeDarwinRuntimeRepairArguments(int(pid))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("read output broker arguments: %w", err)
	}
	finalBirthToken, finalPresent, err := observeProcessBirthToken(int(pid))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("reobserve output broker process birth: %w", err)
	}
	if !finalPresent || finalBirthToken != birthToken {
		return domain.ProcessEvidence{}, fmt.Errorf("output broker process identity changed during observation")
	}
	evidence := domain.ProcessEvidence{
		PID:                pid,
		BirthToken:         birthToken,
		ExecutableIdentity: executable,
		ArgumentDigest:     digestArguments(arguments),
	}
	if err := evidence.Validate(); err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("validate observed output broker process evidence: %w", err)
	}
	return evidence, nil
}
