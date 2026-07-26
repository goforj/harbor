// Package daemonprerequisite activates Harbor's installed per-user daemon service.
package daemonprerequisite

import (
	"context"
	"errors"
)

var (
	// ErrUnavailable reports a desktop build without an installed-daemon activation adapter.
	ErrUnavailable = errors.New("installed Harbor daemon activation is unavailable")
)

// Ensurer activates the installed daemon service for the current interactive user.
type Ensurer interface {
	// Ensure installs or starts only Harbor's fixed admitted per-user service.
	Ensure(context.Context) error
}

// unavailableEnsurer preserves ordinary source-development daemon ownership.
type unavailableEnsurer struct{}

// New creates the daemon activation adapter selected by the platform and build mode.
func New() Ensurer {
	return newPlatformEnsurer()
}

// Ensure leaves source development and unsupported package layouts unchanged.
func (unavailableEnsurer) Ensure(context.Context) error {
	return ErrUnavailable
}
