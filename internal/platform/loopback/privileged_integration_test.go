package loopback

import (
	"context"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

const (
	privilegedTestEnvironment = "HARBOR_PRIVILEGED_LOOPBACK_TEST"
	privilegedTestAddress     = "127.254.253.253"
	privilegedTestTimeout     = 15 * time.Second
)

// TestPrivilegedAdapterLifecycle proves the shipping platform effect through add, bind, idempotence, and cleanup.
func TestPrivilegedAdapterLifecycle(t *testing.T) {
	if os.Getenv(privilegedTestEnvironment) != "1" {
		t.Skipf("set %s=1 and run with platform network authority", privilegedTestEnvironment)
	}

	address := netip.MustParseAddr(privilegedTestAddress)
	adapter := New()
	ctx, cancel := context.WithTimeout(context.Background(), privilegedTestTimeout)
	defer cancel()

	before, err := adapter.Observe(ctx, address)
	if err != nil {
		t.Fatalf("Observe() before lifecycle error = %v", err)
	}
	if before.State != StateAbsent {
		t.Fatalf("privileged test address is not safe to claim: %+v", before)
	}

	cleanupNeeded := false
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), privilegedTestTimeout)
		defer cleanupCancel()
		change, cleanupErr := adapter.Release(cleanupContext, address)
		if cleanupErr != nil || change.Indeterminate || change.After.State != StateAbsent {
			t.Errorf("Release() cleanup = %+v, %v", change, cleanupErr)
		}
	})

	ensured, err := adapter.Ensure(ctx, address)
	cleanupNeeded = ensured.Attempted
	if err != nil {
		t.Fatalf("Ensure() error = %v, change = %+v", err, ensured)
	}
	if !ensured.Attempted || !ensured.Changed || ensured.Indeterminate || ensured.After.State != StateExact {
		t.Fatalf("Ensure() change = %+v", ensured)
	}

	idempotentEnsure, err := adapter.Ensure(ctx, address)
	if err != nil || idempotentEnsure.Attempted || idempotentEnsure.Changed || idempotentEnsure.Indeterminate {
		t.Fatalf("idempotent Ensure() = %+v, %v", idempotentEnsure, err)
	}
	assertPrivilegedAddressReady(t, ctx, address)

	released, err := adapter.Release(ctx, address)
	if err != nil {
		t.Fatalf("Release() error = %v, change = %+v", err, released)
	}
	if !released.Attempted || !released.Changed || released.Indeterminate || released.After.State != StateAbsent {
		t.Fatalf("Release() change = %+v", released)
	}
	cleanupNeeded = false

	idempotentRelease, err := adapter.Release(ctx, address)
	if err != nil || idempotentRelease.Attempted || idempotentRelease.Changed || idempotentRelease.Indeterminate {
		t.Fatalf("idempotent Release() = %+v, %v", idempotentRelease, err)
	}
}

// assertPrivilegedAddressReady waits for Windows DAD and proves the exact address can own a TCP socket on every platform.
func assertPrivilegedAddressReady(t *testing.T, ctx context.Context, address netip.Addr) {
	t.Helper()
	var lastErr error
	for {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort(address.String(), "0"))
		if err == nil {
			bound, ok := listener.Addr().(*net.TCPAddr)
			if !ok || bound.AddrPort().Addr().Unmap() != address {
				actual := listener.Addr().String()
				_ = listener.Close()
				t.Fatalf("listener bound %q instead of %s", actual, address)
			}
			if err := listener.Close(); err != nil {
				t.Fatalf("close readiness listener: %v", err)
			}
			return
		}
		lastErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("address %s did not become bindable: %v: %v", address, lastErr, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
