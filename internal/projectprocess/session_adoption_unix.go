//go:build darwin || linux

package projectprocess

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/goforj/harbor/internal/domain"
)

// adoptPlatformProcess accepts only Harbor's persisted complete Unix session boundary.
func adoptPlatformProcess(ctx context.Context, session domain.ProjectSession, outputDirectory string) (*platformProcess, *os.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !strings.HasPrefix(session.Process.BirthToken, ownedUnixSessionBirthTokenPrefix) {
		return nil, nil, ErrSessionAdoptionUnsupported
	}
	if session.Process.PID > int64(^uint32(0)>>1) {
		return nil, nil, errors.New("session process identity exceeds native PID bounds")
	}
	platform := &platformProcess{birthToken: hostProcessBirthToken(session.Process.BirthToken)}
	alive, err := platform.treeAlive(int(session.Process.PID))
	if err != nil || !alive {
		return nil, nil, errors.Join(errors.New("session process scope is no longer live"), err)
	}
	child, err := os.FindProcess(int(session.Process.PID))
	return platform, child, err
}

// platformProcessCanDetach requires a native session boundary whose output is held by the exact durable broker.
func platformProcessCanDetach(platform *platformProcess, info Info) error {
	if !strings.HasPrefix(info.Evidence.BirthToken, ownedUnixSessionBirthTokenPrefix) || info.OutputBroker == nil {
		return ErrSessionAdoptionUnsupported
	}
	_, err := ObservePersistedOutputBrokerProcessEvidence(info.OutputBroker.Process)
	return err
}

// rootAlive distinguishes an exited supervisor from descendants that still require exact-scope settlement.
func (process *platformProcess) rootAlive(pid int) (bool, error) {
	birth, present, err := observeProcessBirthToken(pid)
	return present && birth == process.birthToken, err
}
