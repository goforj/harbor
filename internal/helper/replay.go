package helper

import (
	"context"
	"fmt"
	"time"
)

// ReplayKey is the canonical identity consumed once regardless of ticket encoding details.
type ReplayKey struct {
	InstallationID      string
	OwnershipGeneration uint64
	Nonce               string
}

// Validate rejects replay identities that cannot be compared canonically across helper launches.
func (key ReplayKey) Validate() error {
	if err := ValidateInstallationID(key.InstallationID); err != nil {
		return fmt.Errorf("helper replay key: %w", err)
	}
	if key.OwnershipGeneration == 0 {
		return fmt.Errorf("helper replay key: ownership generation must be positive")
	}
	if !validToken(key.Nonce, minimumNonceLength, maximumNonceLength) {
		return fmt.Errorf("helper replay key: nonce is invalid")
	}
	return nil
}

// ReplayClaim carries the canonical key and expiry needed for bounded durable retention.
type ReplayClaim struct {
	Key       ReplayKey
	ExpiresAt time.Time
}

// Validate rejects replay claims that cannot have come from a currently valid bounded helper ticket.
func (claim ReplayClaim) Validate(now time.Time) error {
	if err := claim.Key.Validate(); err != nil {
		return err
	}
	if claim.ExpiresAt.IsZero() || !claim.ExpiresAt.After(now) {
		return fmt.Errorf("helper replay claim is expired")
	}
	if claim.ExpiresAt.Location() != time.UTC {
		return fmt.Errorf("helper replay claim expiry must use UTC")
	}
	if claim.ExpiresAt.After(now.Add(MaxTicketLifetime)) {
		return fmt.Errorf("helper replay claim expiry exceeds the maximum ticket lifetime")
	}
	return nil
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
