//go:build !linux && !darwin && !windows

package projectprocess

import "github.com/goforj/harbor/internal/domain"

// observePersistedOutputBrokerProcessEvidence refuses adoption on kernels without reviewed identity readers.
func observePersistedOutputBrokerProcessEvidence(int64) (domain.ProcessEvidence, error) {
	return domain.ProcessEvidence{}, errOutputBrokerProcessIdentityUnsupported
}
