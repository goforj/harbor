//go:build windows

package trustedhttpsharness

import (
	"path/filepath"
	"testing"
)

// TestSystemCurlPathUsesOnlyCanonicalWindowsRoots proves ambient PATH cannot select the native probe executable.
func TestSystemCurlPathUsesOnlyCanonicalWindowsRoots(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Windows`)
	t.Setenv("WINDIR", `E:\Windows`)
	if got, want := systemCurlPath(), filepath.Join(`D:\Windows`, "System32", "curl.exe"); got != want {
		t.Fatalf("systemCurlPath() = %q, want %q", got, want)
	}

	t.Setenv("SystemRoot", "")
	if got, want := systemCurlPath(), filepath.Join(`E:\Windows`, "System32", "curl.exe"); got != want {
		t.Fatalf("systemCurlPath() WINDIR fallback = %q, want %q", got, want)
	}

	t.Setenv("WINDIR", "")
	if got, want := systemCurlPath(), filepath.Join(`C:\Windows`, "System32", "curl.exe"); got != want {
		t.Fatalf("systemCurlPath() canonical fallback = %q, want %q", got, want)
	}
}
