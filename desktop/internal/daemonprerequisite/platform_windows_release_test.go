//go:build windows && release

package daemonprerequisite

import (
	"context"
	"errors"
	"testing"
)

// TestWindowsEnsureRejectsAuthorityBeforeActivation prevents token or cancellation failures from reaching the scheduler.
func TestWindowsEnsureRejectsAuthorityBeforeActivation(t *testing.T) {
	denied := errors.New("effective authority refused")
	called := false
	ensurer := &windowsEnsurer{inspect: func() error { return denied }, start: func(context.Context) error { called = true; return nil }}
	if err := ensurer.Ensure(nil); !errors.Is(err, denied) || called {
		t.Fatalf("token admission: %v, activated=%v", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ensurer.inspect = func() error { t.Fatal("cancelled request inspected token"); return nil }
	if err := ensurer.Ensure(ctx); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled activation: %v, activated=%v", err, called)
	}
}

// TestWindowsEnsurePropagatesOwnedTaskResult preserves admission errors and gives activation a bounded context.
func TestWindowsEnsurePropagatesOwnedTaskResult(t *testing.T) {
	denied := errors.New("foreign task preserved")
	ensurer := &windowsEnsurer{inspect: func() error { return nil }, start: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("activation lacks deadline")
		}
		return denied
	}}
	if err := ensurer.Ensure(context.Background()); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	ensurer.start = func(context.Context) error { return nil }
	if err := ensurer.Ensure(nil); err != nil {
		t.Fatal(err)
	}
}
