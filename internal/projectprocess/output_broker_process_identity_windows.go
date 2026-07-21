//go:build windows

package projectprocess

import "github.com/goforj/harbor/internal/domain"

// observePersistedOutputBrokerProcessEvidence keeps Windows adoption fail-closed until command-line proof is native.
func observePersistedOutputBrokerProcessEvidence(int64) (domain.ProcessEvidence, error) {
	return domain.ProcessEvidence{}, errOutputBrokerProcessIdentityUnsupported
}
