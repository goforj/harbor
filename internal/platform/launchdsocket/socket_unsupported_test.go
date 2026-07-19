//go:build !darwin

package launchdsocket

import (
	"errors"
	"testing"
)

// TestActivateIngressFailsClosedOutsideMacOS verifies no unsupported build can synthesize listener authority.
func TestActivateIngressFailsClosedOutsideMacOS(t *testing.T) {
	listeners, err := ActivateIngress()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ActivateIngress() error = %v, want %v", err, ErrUnavailable)
	}
	if listeners.HTTP != nil || listeners.HTTPS != nil {
		t.Fatalf("ActivateIngress() listeners = %#v, want empty", listeners)
	}
}
