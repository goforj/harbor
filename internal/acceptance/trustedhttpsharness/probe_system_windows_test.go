//go:build windows

package trustedhttpsharness

import (
	"path/filepath"
	"strings"
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

// TestNativeCurlTLSArgumentsRetainSchannelVerification proves Windows softens only unavailable
// revocation data instead of disabling revocation or certificate validation.
func TestNativeCurlTLSArgumentsRetainSchannelVerification(t *testing.T) {
	arguments := curlCommand("orders.test").Arguments
	if count := strings.Count(strings.Join(arguments, "\x00"), "--ssl-revoke-best-effort"); count != 1 {
		t.Fatalf("curl arguments = %#v, want exactly one Schannel best-effort policy", arguments)
	}
	joined := strings.Join(arguments, " ")
	for _, forbidden := range []string{"--ssl-no-revoke", "--insecure", "--cacert", "--capath"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("curl arguments contain forbidden %q: %#v", forbidden, arguments)
		}
	}
}
