//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

const parentLivenessDescriptor = 4

// runWithPlatformParentLiveness requires the Darwin launcher parent to retain FD 4 through helper completion.
func runWithPlatformParentLiveness(run parentLivenessRunner) error {
	liveness, err := openDarwinParentLiveness(parentLivenessDescriptor, os.NewFile)
	if err != nil {
		return err
	}
	return runWithParentLiveness(context.Background(), liveness, run)
}

// parentLivenessFailed reports whether the helper must terminate without waiting for unfinished work.
func parentLivenessFailed(err error) bool {
	return errors.Is(err, errParentLivenessLost)
}

// darwinParentLivenessOpener creates the inherited reader for testable FD validation.
type darwinParentLivenessOpener func(uintptr, string) *os.File

// openDarwinParentLiveness rejects missing, invalid, and non-pipe inherited descriptors before authority opens.
func openDarwinParentLiveness(descriptor uintptr, open darwinParentLivenessOpener) (*os.File, error) {
	if open == nil {
		return nil, errors.New("helper parent liveness opener is required")
	}
	liveness := open(descriptor, "harbor-parent-liveness")
	if liveness == nil {
		return nil, fmt.Errorf("helper parent liveness descriptor %d is unavailable", descriptor)
	}
	information, err := liveness.Stat()
	if err != nil {
		_ = liveness.Close()
		return nil, fmt.Errorf("inspect helper parent liveness descriptor %d: %w", descriptor, err)
	}
	if information.Mode()&os.ModeNamedPipe == 0 {
		_ = liveness.Close()
		return nil, fmt.Errorf("helper parent liveness descriptor %d is not a pipe", descriptor)
	}
	syscall.CloseOnExec(int(liveness.Fd()))
	return liveness, nil
}
