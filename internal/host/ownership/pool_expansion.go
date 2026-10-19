package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"

	"github.com/goforj/harbor/internal/network/poolpolicy"
)

// ValidatePoolExpansion permits only containing-pool growth while keeping every existing owner and policy binding.
func ValidatePoolExpansion(source, target Record) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	previous := netip.MustParsePrefix(source.LoopbackPoolPrefix)
	next := netip.MustParsePrefix(target.LoopbackPoolPrefix)
	if err := poolpolicy.Expansion(previous, next); err != nil {
		return err
	}
	unchanged := target
	unchanged.LoopbackPoolPrefix = source.LoopbackPoolPrefix
	if unchanged != source {
		return fmt.Errorf("pool expansion cannot change installation, owner, generation, signing key, or network policy")
	}
	return nil
}

// Expand atomically broadens an exact protected source claim and safely replays its already-current target.
func (store *Store) Expand(ctx context.Context, previousPool, expectedFingerprint string, target Record) (Observation, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	source := target
	source.LoopbackPoolPrefix = previousPool
	if err := ValidatePoolExpansion(source, target); err != nil {
		return Observation{}, err
	}
	fingerprint, err := source.Fingerprint()
	if err != nil {
		return Observation{}, err
	}
	if fingerprint != expectedFingerprint {
		return Observation{}, fmt.Errorf("pool expansion fingerprint differs from the exact source")
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		return Observation{}, err
	}
	want := Observation{Exists: true, Record: target, Fingerprint: targetFingerprint}
	var result Observation
	applied := false
	err = store.withLock(ctx, func() error {
		current, err := store.observeLocked()
		if err != nil {
			return err
		}
		if current == want {
			if err := store.operations.confirmEntry(store.root, filepath.Dir(store.path), store.name); err != nil {
				return durabilityUncertain("confirm expanded ownership", store.path, err)
			}
			result = current
			return nil
		}
		if err := compareUpgradeSource(current, expectedFingerprint, source); err != nil {
			return err
		}
		encoded, err := json.Marshal(target)
		if err != nil {
			return err
		}
		applied, err = store.writeReplaceLocked(encoded)
		if err != nil {
			return err
		}
		result, err = store.observeLocked()
		if err != nil {
			return err
		}
		if result != want {
			return fmt.Errorf("expanded ownership readback differs from signed target")
		}
		return nil
	})
	if err != nil && applied && !errors.Is(err, ErrDurabilityUncertain) {
		return Observation{}, durabilityUncertain("finish pool expansion", store.path, err)
	}
	if err != nil {
		return Observation{}, err
	}
	return result, nil
}
