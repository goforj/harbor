//go:build windows

package resolver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// TestWindowsNRPTExpectedClonePreservesEmptyArray keeps an absent CAS precondition
// from becoming JSON null, which Windows PowerShell binds as one expected object.
func TestWindowsNRPTExpectedClonePreservesEmptyArray(t *testing.T) {
	expected := slicesCloneWindowsNRPTExpected([]windowsNRPTExpectedRule{})
	if expected == nil {
		t.Fatal("empty Windows NRPT expected clone is nil")
	}
	body, err := json.Marshal(windowsNRPTCommandRequest{Expected: expected})
	if err != nil {
		t.Fatalf("marshal empty Windows NRPT expected clone: %v", err)
	}
	if !strings.Contains(string(body), `"expected":[]`) {
		t.Fatalf("empty Windows NRPT expected clone JSON = %s, want array", body)
	}

	request := resolverTestRequest(t, networkpolicy.WindowsNRPT)
	err = validateWindowsNRPTCommandRequest(windowsNRPTCommandRequest{
		Operation:   "observe",
		Suffix:      request.Suffix(),
		DisplayName: windowsNRPTDisplayName(request),
		Comment:     windowsNRPTOwnerComment(request),
		Server:      request.Endpoint().Addr().String(),
	})
	if err == nil || !strings.Contains(err.Error(), "expected rules must be an array") {
		t.Fatalf("null Windows NRPT expected rules error = %v", err)
	}
}

// TestWindowsNRPTPowerShellFingerprintProgramTracksEveryGoField keeps the static native CAS program aligned with Go's reviewed rule identity.
func TestWindowsNRPTPowerShellFingerprintProgramTracksEveryGoField(t *testing.T) {
	orderedLines := []string{
		"$lines.Add('goforj.harbor.windows-nrpt-rule.v1')",
		"$lines.Add(([uint32]$Rule.version).ToString([Globalization.CultureInfo]::InvariantCulture))",
		"Add-ArrayLines $lines @($Rule.namespaces)",
		"Add-TextLine $lines ([string]$Rule.name)",
		"Add-TextLine $lines ([string]$Rule.ipsec_ca_restriction)",
		"Add-ArrayLines $lines @($Rule.direct_access_dns_servers)",
		"Add-BoolLine $lines ([bool]$Rule.direct_access_enabled)",
		"Add-TextLine $lines ([string]$Rule.direct_access_proxy_type)",
		"Add-TextLine $lines ([string]$Rule.direct_access_proxy_name)",
		"Add-TextLine $lines ([string]$Rule.direct_access_query_ipsec_encryption)",
		"Add-BoolLine $lines ([bool]$Rule.direct_access_query_ipsec_required)",
		"Add-ArrayLines $lines @($Rule.name_servers)",
		"Add-BoolLine $lines ([bool]$Rule.dnssec_enabled)",
		"Add-TextLine $lines ([string]$Rule.dnssec_query_ipsec_encryption)",
		"Add-BoolLine $lines ([bool]$Rule.dnssec_query_ipsec_required)",
		"Add-BoolLine $lines ([bool]$Rule.dnssec_validation_required)",
		"Add-TextLine $lines ([string]$Rule.name_encoding)",
		"Add-TextLine $lines ([string]$Rule.display_name)",
		"Add-TextLine $lines ([string]$Rule.comment)",
	}
	previous := 0
	for _, line := range orderedLines {
		next := strings.Index(windowsNRPTPowerShellProgram[previous:], line)
		if next < 0 {
			t.Fatalf("windows NRPT PowerShell fingerprint omits %q", line)
		}
		previous += next + len(line)
	}
	for _, line := range []string{
		"[Text.Encoding]::UTF8.GetBytes($Value)",
		"[Convert]::ToBase64String($bytes)",
		"[string]::Join($lineFeed, $lines) + $lineFeed",
		"[Security.Cryptography.SHA256]::Create()",
	} {
		if !strings.Contains(windowsNRPTPowerShellProgram, line) {
			t.Fatalf("windows NRPT PowerShell fingerprint lacks canonical encoding step %q", line)
		}
	}
}

// TestWindowsNRPTPowerShellRepairOmitsDisabledFeatureDependents keeps Set from
// passing parameters that Windows rejects when their parent feature is disabled.
func TestWindowsNRPTPowerShellRepairOmitsDisabledFeatureDependents(t *testing.T) {
	for _, parameter := range []string{
		"-DAIPsecRequired",
		"-DnsSecIPsecRequired",
		"-DnsSecValidationRequired",
	} {
		if strings.Contains(windowsNRPTPowerShellProgram, parameter) {
			t.Fatalf("Windows NRPT repair contains dependent parameter %s", parameter)
		}
	}
}

// TestWindowsNRPTPowerShellImportsOnlyInboxModules prevents elevated command
// discovery from consulting a caller-controlled PowerShell module path.
func TestWindowsNRPTPowerShellImportsOnlyInboxModules(t *testing.T) {
	moduleRoot := "$moduleRoot = [IO.Path]::Combine($PSHOME, 'Modules')"
	modulePath := "$env:PSModulePath = $moduleRoot"
	autoload := "$PSModuleAutoLoadingPreference = 'None'"
	importModules := "foreach ($module in @('Microsoft.PowerShell.Management', 'Microsoft.PowerShell.Utility', 'CimCmdlets', 'DnsClient'))"
	manifest := `$manifest = [IO.Path]::Combine($moduleRoot, $module, "$module.psd1")`
	importModule := "Import-Module -Name $manifest -Force -ErrorAction Stop"
	moduleRootIndex := strings.Index(windowsNRPTPowerShellProgram, moduleRoot)
	modulePathIndex := strings.Index(windowsNRPTPowerShellProgram, modulePath)
	autoloadIndex := strings.Index(windowsNRPTPowerShellProgram, autoload)
	importModulesIndex := strings.Index(windowsNRPTPowerShellProgram, importModules)
	manifestIndex := strings.Index(windowsNRPTPowerShellProgram, manifest)
	importIndex := strings.Index(windowsNRPTPowerShellProgram, importModule)
	firstCmdletIndex := strings.Index(windowsNRPTPowerShellProgram, "Get-DnsClientNrptRule")
	if moduleRootIndex < 0 ||
		modulePathIndex <= moduleRootIndex ||
		autoloadIndex <= modulePathIndex ||
		importModulesIndex <= autoloadIndex ||
		manifestIndex <= importModulesIndex ||
		importIndex <= manifestIndex ||
		firstCmdletIndex <= importIndex {
		t.Fatalf(
			"Windows NRPT module boundary ordering = %d/%d/%d/%d/%d/%d/%d",
			moduleRootIndex,
			modulePathIndex,
			autoloadIndex,
			importModulesIndex,
			manifestIndex,
			importIndex,
			firstCmdletIndex,
		)
	}
}

// TestWindowsNRPTPowerShellEmitsOnlyFiniteStageMarkers keeps native failures classifiable without exposing their text.
func TestWindowsNRPTPowerShellEmitsOnlyFiniteStageMarkers(t *testing.T) {
	for _, marker := range []string{
		"harbor-stage=module-import",
		"$script:HarborStage = 'input'",
		"$script:HarborStage = 'parse'",
		"$script:HarborStage = 'validation'",
		"$script:HarborStage = 'enumerate'",
		"$script:HarborStage = 'output'",
		"$script:HarborStage = 'precondition'",
		"$script:HarborStage = 'mutation'",
	} {
		if !strings.Contains(windowsNRPTPowerShellProgram, marker) {
			t.Fatalf("Windows NRPT PowerShell program lacks bounded marker %q", marker)
		}
	}
}

// TestWindowsNRPTProgressOnly accepts only the complete ordered marker sequence.
func TestWindowsNRPTProgressOnly(t *testing.T) {
	if !windowsNRPTProgressOnly("harbor-progress=module-imported\r\nharbor-progress=enumerating\r\n") {
		t.Fatal("windowsNRPTProgressOnly() rejected the complete marker sequence")
	}
	for _, value := range []string{
		"",
		"harbor-progress=module-imported",
		"harbor-progress=enumerating\r\nharbor-progress=module-imported",
		"harbor-progress=module-imported\r\nharbor-progress=enumerating\r\nprivate detail",
	} {
		if windowsNRPTProgressOnly(value) {
			t.Fatalf("windowsNRPTProgressOnly(%q) accepted an unsupported diagnostic", value)
		}
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
	change, err := ensurePrivilegedWindowsNRPT(t.Context(), adapter, request, fingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", privilegedWindowsNRPTDiagnostic(err))
	}
	if !change.Attempted || !change.Changed {
		t.Fatalf("EnsureIfObserved() = %#v, want a published rule", change)
	}
	assessment, err = change.After.Classify()
	if err != nil || assessment.State != StateExact {
		t.Fatalf("Classify(after ensure) = %#v, %v; want exact", assessment, err)
	}
	if len(change.After.Rules) != 1 || change.After.Rules[0].NativeID == "" {
		t.Fatalf("EnsureIfObserved() rule facts = %#v, want one native rule", change.After.Rules)
	}
	if err := driftWindowsNRPTNameServers(t.Context(), change.After.Rules[0].NativeID, "127.0.0.3"); err != nil {
		t.Fatalf("drift native NRPT name servers: %v", err)
	}
	drifted, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(after drift) error = %v", err)
	}
	assessment, err = drifted.Classify()
	if err != nil || assessment.State != StateOwnedDrifted {
		t.Fatalf("Classify(after drift) = %#v, %v; want owned drifted", assessment, err)
	}
	fingerprint, err = drifted.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(after drift) error = %v", err)
	}
	change, err = adapter.EnsureIfObserved(t.Context(), request, fingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved(after drift) error = %v", privilegedWindowsNRPTDiagnostic(err))
	}
	assessment, err = change.After.Classify()
	if err != nil || assessment.State != StateExact {
		t.Fatalf("Classify(after repair) = %#v, %v; want exact", assessment, err)
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

// ensurePrivilegedWindowsNRPT retries a fresh absent snapshot when Windows changes unrelated NRPT state between observation and mutation.
func ensurePrivilegedWindowsNRPT(ctx context.Context, adapter *Adapter, request Request, fingerprint string) (Change, error) {
	const maximumAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		change, err := adapter.EnsureIfObserved(ctx, request, fingerprint)
		if err == nil || !windowsNRPTPreconditionChanged(err) {
			return change, err
		}
		lastErr = err

		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Change{}, ctx.Err()
		case <-timer.C:
		}

		observation, err := adapter.Observe(ctx, request)
		if err != nil {
			return Change{}, err
		}
		assessment, err := observation.Classify()
		if err != nil {
			return Change{}, err
		}
		if assessment.State != StateAbsent {
			return Change{}, lastErr
		}
		fingerprint, err = observation.Fingerprint()
		if err != nil {
			return Change{}, err
		}
	}
	return Change{}, fmt.Errorf("Windows NRPT precondition did not stabilize: %w", lastErr)
}

// windowsNRPTPreconditionChanged reports the native optimistic-concurrency miss that is safe to retry from a new observation.
func windowsNRPTPreconditionChanged(err error) bool {
	var resolverError *Error
	return errors.As(err, &resolverError) &&
		resolverError.Unwrap() != nil &&
		strings.Contains(resolverError.Unwrap().Error(), "NRPT relevant rule set changed before mutation")
}

// privilegedWindowsNRPTDiagnostic exposes a bounded native cause only inside the opt-in administrator test.
func privilegedWindowsNRPTDiagnostic(err error) error {
	if err == nil {
		return nil
	}
	var resolverError *Error
	if !errors.As(err, &resolverError) || resolverError.Unwrap() == nil {
		return err
	}
	cause := resolverError.Unwrap().Error()
	const maximumDiagnosticBytes = 4096
	if len(cause) > maximumDiagnosticBytes {
		cause = cause[:maximumDiagnosticBytes] + "...[truncated]"
	}
	return fmt.Errorf("%w; native cause: %s", err, cause)
}

// driftWindowsNRPTNameServers changes only a freshly admitted test rule so the shipping Set path must restore its exact server list.
func driftWindowsNRPTNameServers(ctx context.Context, name string, server string) error {
	if name == "" || server == "" {
		return errors.New("Windows NRPT drift requires a rule name and server")
	}
	executable, err := windowsPowerShellExecutable()
	if err != nil {
		return err
	}
	input, err := json.Marshal(struct {
		Name   string `json:"name"`
		Server string `json:"server"`
	}{Name: name, Server: server})
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsNRPTTestDriftProgram)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute owned Windows NRPT drift: %w: %s", err, windowsNRPTDisplayDiagnostic(string(output)))
	}
	return nil
}

const windowsNRPTTestDriftProgram = `$ErrorActionPreference = 'Stop'
$inputText = [Console]::In.ReadToEnd()
$request = $inputText | ConvertFrom-Json -ErrorAction Stop
$names = @($request.PSObject.Properties.Name | Sort-Object -CaseSensitive)
if ($names.Count -ne 2 -or $names[0] -cne 'name' -or $names[1] -cne 'server') { throw 'invalid test drift request' }
Set-DnsClientNrptRule -Name ([string]$request.name) -NameServers @([string]$request.server) -Confirm:$false -ErrorAction Stop | Out-Null`
