//go:build windows

package resolver

import (
	"errors"
	"strings"
	"testing"
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
