package helper

import (
	"context"
	"time"
)

// ReplayKey is the canonical identity consumed once regardless of ticket encoding details.
type ReplayKey struct {
	InstallationID      string
	OwnershipGeneration uint64
	Nonce               string
}

// ReplayClaim carries the canonical key and expiry needed for bounded durable retention.
type ReplayClaim struct {
	Key       ReplayKey
	ExpiresAt time.Time
}

// ReplayGuard atomically consumes valid ticket claims before any mutation begins.
type ReplayGuard interface {
	Consume(context.Context, ReplayClaim) error
}

// UnavailableReplayGuard fails closed until a durable platform replay store is installed.
type UnavailableReplayGuard struct{}

// Consume rejects admission because process-local memory cannot protect a one-shot helper from relaunch replay.
func (UnavailableReplayGuard) Consume(context.Context, ReplayClaim) error {
	return ErrReplayProtectionUnavailable
}
