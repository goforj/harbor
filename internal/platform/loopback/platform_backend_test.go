package loopback

import (
	"context"
	"runtime"
	"testing"
)

// TestPlatformBackendFindsOneNativeLoopback exercises each CI host's native interface facts.
func TestPlatformBackendFindsOneNativeLoopback(t *testing.T) {
	facts, err := newPlatformBackend().interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces() error = %v", err)
	}
	interf, err := selectLoopback(facts)
	if err != nil {
		t.Fatalf("selectLoopback() error = %v", err)
	}
	want := map[string]InterfaceKind{
		"linux":   InterfaceKindLinuxNative,
		"darwin":  InterfaceKindDarwinNative,
		"windows": InterfaceKindWindowsSoftware,
	}[runtime.GOOS]
	if interf.Kind != want {
		t.Fatalf("loopback kind = %q, want %q", interf.Kind, want)
	}
}
