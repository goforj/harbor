//go:build !darwin && !linux

package ownershipreleaseproof

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RootWriter is unavailable until the platform supplies reviewed root-owned durable storage primitives.
type RootWriter struct{}

// Observer is unavailable until the platform supplies reviewed root-owned durable storage primitives.
type Observer struct{}

// newRootWriter rejects operating systems without a reviewed ownership-release proof boundary.
func newRootWriter(string, string) (*RootWriter, error) {
	return nil, fmt.Errorf("%w: ownership release proof storage", ErrUnsafePath)
}

// newObserver rejects operating systems without a reviewed ownership-release proof boundary.
func newObserver(string) (*Observer, error) {
	return nil, fmt.Errorf("%w: ownership release proof storage", ErrUnsafePath)
}

// complete is unavailable without reviewed ownership-release proof storage.
func (writer *RootWriter) complete(context.Context, Request, Transaction, time.Time) (Proof, error) {
	return Proof{}, fmt.Errorf("%w: ownership release proof storage", ErrUnsafePath)
}

// observe is unavailable without reviewed ownership-release proof storage.
func (observer *Observer) observe(context.Context) (Proof, bool, error) {
	return Proof{}, false, fmt.Errorf("%w: ownership release proof storage", ErrUnsafePath)
}

// encodeProof preserves the stable public fingerprint surface on unsupported platforms.
func encodeProof(proof Proof) ([]byte, error) { return json.Marshal(proof) }
