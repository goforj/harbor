//go:build !darwin && !linux && !windows

package projectprocess

import (
	"context"
	"os"

	"github.com/goforj/harbor/internal/domain"
)

// adoptPlatformProcess withholds process authority until the platform has a durable native ownership boundary.
func adoptPlatformProcess(context.Context, domain.ProjectSession, string) (*platformProcess, *os.Process, error) {
	return nil, nil, ErrSessionAdoptionUnsupported
}

// platformProcessCanDetach keeps an unsupported process on the existing joined shutdown path.
func platformProcessCanDetach(*platformProcess, Info) error {
	return ErrSessionAdoptionUnsupported
}

// rootAlive is unreachable because this platform never grants adopted process authority.
func (*platformProcess) rootAlive(int) (bool, error) { return false, ErrSessionAdoptionUnsupported }
