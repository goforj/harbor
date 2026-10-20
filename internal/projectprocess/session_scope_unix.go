//go:build !windows

package projectprocess

import (
	"context"
	"os/exec"

	"github.com/goforj/harbor/internal/domain"
)

// settleRetainedSessionScope leaves complete Unix session settlement with the existing native census.
func settleRetainedSessionScope(context.Context, domain.ProjectSession, string) (PriorProcessSettlement, bool, error) {
	return PriorProcessSettlement{}, false, nil
}

// prepareSessionPlatformProcess uses the kernel's persistent session membership on Unix.
func prepareSessionPlatformProcess(command *exec.Cmd, projectID domain.ProjectID, sessionID domain.SessionID, directory string, broker bool) (*platformProcess, error) {
	return preparePlatformProcess(command)
}

// retainsProcessScope reports that Unix sessions require no broker-held kernel handle.
func (*platformProcess) retainsProcessScope() bool { return false }

// retainOutputBrokerProcessScope leaves Unix process ownership with the native session boundary.
func retainOutputBrokerProcessScope(OutputBrokerRuntimeConfig) (func(), error) {
	return func() {}, nil
}
