//go:build !darwin && !linux && !windows

package cmd

import (
	"context"
	"errors"
	"os"
)

// prepareProjectRemovalIntentObject defers rejection to the unsupported platform validator.
func prepareProjectRemovalIntentObject(*os.File, bool, bool) error {
	return nil
}

// acquireProjectRemovalIntentLock rejects targets without a reviewed durable interprocess lock implementation.
func acquireProjectRemovalIntentLock(context.Context, *os.File) error {
	return errors.New("project removal intent locking is unsupported on this platform")
}

// releaseProjectRemovalIntentLock has no lock to release on unsupported targets.
func releaseProjectRemovalIntentLock(*os.File) error {
	return nil
}

// validateProjectRemovalIntentObject rejects targets without reviewed owner-local object validation.
func validateProjectRemovalIntentObject(*os.File, bool) error {
	return errors.New("project removal intent object validation is unsupported on this platform")
}

// syncProjectRemovalIntentDirectory has no supported durability contract on unsupported targets.
func syncProjectRemovalIntentDirectory(*os.File) error {
	return nil
}
