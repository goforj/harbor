package projectprocess

import (
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

var errOutputBrokerProcessIdentityUnsupported = errors.New("output broker process identity observation is unsupported")

// ObservePersistedOutputBrokerProcessEvidence revalidates every immutable broker identity before re-adoption.
//
// The durable PID is never enough: the platform implementation must reread the birth token, executable image,
// and argument digest so a recycled PID or a look-alike local endpoint cannot become output authority.
func ObservePersistedOutputBrokerProcessEvidence(expected domain.ProcessEvidence) (domain.ProcessEvidence, error) {
	if err := expected.Validate(); err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("validate persisted output broker process evidence: %w", err)
	}
	observed, err := observePersistedOutputBrokerProcessEvidence(expected.PID)
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	if observed != expected {
		return domain.ProcessEvidence{}, fmt.Errorf("persisted output broker process evidence drifted")
	}
	return observed, nil
}
