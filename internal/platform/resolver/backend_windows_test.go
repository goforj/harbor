//go:build windows

package resolver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"golang.org/x/sys/windows"
)

// TestWindowsPowerShellExecutableFromSystemDirectory rejects untrusted path input before selecting the fixed host executable.
func TestWindowsPowerShellExecutableFromSystemDirectory(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		err       error
		want      string
	}{
		{
			name:      "canonical system directory",
			directory: `C:\Windows\System32`,
			want:      `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		},
		{name: "native failure", err: errors.New("unavailable")},
		{name: "empty directory"},
		{name: "relative directory", directory: `Windows\System32`},
		{name: "unclean directory", directory: `C:\Windows\System32\..`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := windowsPowerShellExecutableFromSystemDirectory(func() (string, error) {
				return test.directory, test.err
			})
			if test.want == "" {
				if err == nil {
					t.Fatalf("windowsPowerShellExecutableFromSystemDirectory() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("windowsPowerShellExecutableFromSystemDirectory() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}

// TestWindowsNativePowerShellRunnerRejectsMissingLookup prevents a zero-value runner from falling back to PATH resolution.
func TestWindowsNativePowerShellRunnerRejectsMissingLookup(t *testing.T) {
	_, err := (windowsNativePowerShellRunner{}).run(t.Context(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "missing fixed executable lookup") {
		t.Fatalf("windowsNativePowerShellRunner.run() error = %v, want missing fixed executable lookup", err)
	}
}

// TestPrivilegedWindowsNRPTAdapterLifecycle proves the fixed native PowerShell boundary creates, verifies, and removes only one fresh local rule.
func TestPrivilegedWindowsNRPTAdapterLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 on a disposable elevated Windows runner")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("privileged Windows NRPT lifecycle requires an elevated process")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate native NRPT installation identity: %v", err)
	}
	request, err := NewRequest("installation-native-"+hex.EncodeToString(random), resolverTestPolicy(t, networkpolicy.WindowsNRPT))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	adapter := New()
	cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(func() {
		defer cancelCleanup()
		observation, observeErr := adapter.Observe(cleanupContext, request)
		if observeErr != nil {
			t.Errorf("cleanup Observe() error = %v", observeErr)
			return
		}
		assessment, assessmentErr := observation.Classify()
		if assessmentErr != nil {
			t.Errorf("cleanup Classify() error = %v", assessmentErr)
			return
		}
		if assessment.Owned == OwnedStateAbsent {
			return
		}
		fingerprint, fingerprintErr := observation.Fingerprint()
		if fingerprintErr != nil {
			t.Errorf("cleanup Fingerprint() error = %v", fingerprintErr)
			return
		}
		if _, releaseErr := adapter.ReleaseIfObserved(cleanupContext, request, fingerprint); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v", releaseErr)
		}
	})

	observation, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateAbsent {
		t.Fatalf("Classify(before) = %#v, %v; want absent", assessment, err)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(before) error = %v", err)
	}
	change, err := adapter.EnsureIfObserved(t.Context(), request, fingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if !change.Attempted || !change.Changed {
		t.Fatalf("EnsureIfObserved() = %#v, want a published rule", change)
	}
	assessment, err = change.After.Classify()
	if err != nil || assessment.State != StateExact {
		t.Fatalf("Classify(after ensure) = %#v, %v; want exact", assessment, err)
	}
	fingerprint, err = change.After.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(after ensure) error = %v", err)
	}
	change, err = adapter.ReleaseIfObserved(t.Context(), request, fingerprint)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	assessment, err = change.After.Classify()
	if err != nil || assessment.State != StateAbsent {
		t.Fatalf("Classify(after release) = %#v, %v; want absent", assessment, err)
	}
}
