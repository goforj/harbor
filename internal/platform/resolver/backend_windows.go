//go:build windows

package resolver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maximumWindowsNRPTInputBytes      = 256 << 10
	maximumWindowsNRPTOutputBytes     = 2 << 20
	maximumWindowsNRPTDiagnosticBytes = 16 << 10
	maximumWindowsNRPTDisplayError    = 1024
)

// windowsNRPTCommandRequest is the exact JSON authority accepted by the static PowerShell program.
type windowsNRPTCommandRequest struct {
	Operation   string                    `json:"operation"`
	Suffix      string                    `json:"suffix"`
	DisplayName string                    `json:"display_name"`
	Comment     string                    `json:"comment"`
	Server      string                    `json:"server"`
	Expected    []windowsNRPTExpectedRule `json:"expected"`
	Guard       windowsNRPTGuard          `json:"guard"`
}

// windowsNRPTSnapshotResponse is the sole successful observation shape emitted by the static program.
type windowsNRPTSnapshotResponse struct {
	Rules []windowsNRPTRule `json:"rules"`
}

// windowsNativeNRPTStore invokes one static PowerShell program with JSON data kept outside its source text.
type windowsNativeNRPTStore struct {
	runner windowsNRPTCommandRunner
}

// windowsNRPTCommandRunner isolates process execution for exact command and response tests.
type windowsNRPTCommandRunner interface {
	// run executes the static program with one bounded JSON input and returns bounded stdout.
	run(context.Context, []byte) ([]byte, error)
}

// windowsNativePowerShellRunner owns the Windows PowerShell process boundary.
type windowsNativePowerShellRunner struct{}

var _ windowsNRPTStore = windowsNativeNRPTStore{}

// New creates a resolver adapter backed by Windows 11's local NRPT DnsClient provider.
func New() *Adapter {
	return newAdapter(newWindowsNRPTBackend(windowsNativeNRPTStore{runner: windowsNativePowerShellRunner{}}))
}

// snapshot obtains one complete relevant local NRPT snapshot from the static program.
func (store windowsNativeNRPTStore) snapshot(ctx context.Context, request Request) ([]windowsNRPTRule, error) {
	output, err := store.invoke(ctx, windowsNRPTCommandRequest{
		Operation:   "observe",
		Suffix:      request.Suffix(),
		DisplayName: windowsNRPTDisplayName(request),
		Comment:     windowsNRPTOwnerComment(request),
		Server:      request.Endpoint().Addr().String(),
		Expected:    []windowsNRPTExpectedRule{},
	})
	if err != nil {
		return nil, err
	}
	response, err := decodeWindowsNRPTSnapshot(output)
	if err != nil {
		return nil, err
	}
	return response.Rules, nil
}

// ensure asks the static program to recheck complete native state before one exact Add or Set.
func (store windowsNativeNRPTStore) ensure(
	ctx context.Context,
	request Request,
	expected []windowsNRPTExpectedRule,
	guard windowsNRPTGuard,
) error {
	output, err := store.invoke(ctx, windowsNRPTCommandRequest{
		Operation:   "ensure",
		Suffix:      request.Suffix(),
		DisplayName: windowsNRPTDisplayName(request),
		Comment:     windowsNRPTOwnerComment(request),
		Server:      request.Endpoint().Addr().String(),
		Expected:    slicesCloneWindowsNRPTExpected(expected),
		Guard:       guard,
	})
	if err != nil {
		return err
	}
	return decodeWindowsNRPTMutation(output)
}

// release asks the static program to recheck complete native state before removing one exact Name.
func (store windowsNativeNRPTStore) release(
	ctx context.Context,
	request Request,
	expected []windowsNRPTExpectedRule,
	guard windowsNRPTGuard,
) error {
	output, err := store.invoke(ctx, windowsNRPTCommandRequest{
		Operation:   "release",
		Suffix:      request.Suffix(),
		DisplayName: windowsNRPTDisplayName(request),
		Comment:     windowsNRPTOwnerComment(request),
		Server:      request.Endpoint().Addr().String(),
		Expected:    slicesCloneWindowsNRPTExpected(expected),
		Guard:       guard,
	})
	if err != nil {
		return err
	}
	return decodeWindowsNRPTMutation(output)
}

// slicesCloneWindowsNRPTExpected prevents caller-owned preconditions from crossing the process boundary by alias.
func slicesCloneWindowsNRPTExpected(expected []windowsNRPTExpectedRule) []windowsNRPTExpectedRule {
	return append([]windowsNRPTExpectedRule(nil), expected...)
}

// invoke validates and marshals one fixed-schema request before handing it to the static runner.
func (store windowsNativeNRPTStore) invoke(
	ctx context.Context,
	request windowsNRPTCommandRequest,
) ([]byte, error) {
	if err := validateWindowsNRPTCommandRequest(request); err != nil {
		return nil, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal Windows NRPT command: %w", err)
	}
	if len(body) > maximumWindowsNRPTInputBytes {
		return nil, fmt.Errorf("Windows NRPT command exceeds %d bytes", maximumWindowsNRPTInputBytes)
	}
	return store.runner.run(ctx, body)
}

// validateWindowsNRPTCommandRequest prevents internal callers from expanding the static program's authority.
func validateWindowsNRPTCommandRequest(request windowsNRPTCommandRequest) error {
	if request.Operation != "observe" && request.Operation != "ensure" && request.Operation != "release" {
		return fmt.Errorf("Windows NRPT operation %q is unsupported", request.Operation)
	}
	if request.Suffix != ".test" {
		return fmt.Errorf("Windows NRPT suffix must be exactly .test")
	}
	if err := validateWindowsNRPTText("Windows NRPT display name", request.DisplayName, maximumWindowsNRPTDisplayNameBytes, false); err != nil {
		return err
	}
	if err := validateWindowsNRPTText("Windows NRPT comment", request.Comment, maximumWindowsNRPTCommentBytes, false); err != nil {
		return err
	}
	address, err := netip.ParseAddr(request.Server)
	if err != nil || !address.Is4() || !address.IsLoopback() || address != address.Unmap() ||
		address.String() != request.Server || address == netip.MustParseAddr("127.0.0.1") {
		return fmt.Errorf("Windows NRPT server must be one canonical IPv4 address")
	}
	marker, err := parseWindowsNRPTOwnerComment(request.Comment)
	if err != nil {
		return err
	}
	if request.DisplayName != windowsNRPTDisplayNamePrefix+marker.InstallationID {
		return fmt.Errorf("Windows NRPT display name does not match its owner marker")
	}
	if len(request.Expected) > maximumRuleFacts {
		return fmt.Errorf("Windows NRPT expected rules exceed limit %d", maximumRuleFacts)
	}
	previous := ""
	for _, expected := range request.Expected {
		if err := validateBoundedText("Windows NRPT expected rule name", expected.Name, maximumNativeIDLength); err != nil {
			return err
		}
		if err := validateFingerprintText("Windows NRPT expected native fingerprint", expected.NativeAttributesSHA256); err != nil {
			return err
		}
		if previous != "" && expected.Name <= previous {
			return fmt.Errorf("Windows NRPT expected rules must have unique canonical order")
		}
		previous = expected.Name
	}
	if request.Guard.Exists {
		if err := validateBoundedText("Windows NRPT guard name", request.Guard.Name, maximumNativeIDLength); err != nil {
			return err
		}
		if err := validateFingerprintText("Windows NRPT guard native fingerprint", request.Guard.NativeAttributesSHA256); err != nil {
			return err
		}
	} else if request.Guard.Name != "" || request.Guard.NativeAttributesSHA256 != "" {
		return fmt.Errorf("absent Windows NRPT guard contains native identity")
	}
	if request.Guard.Exists {
		guardFound := false
		for _, expected := range request.Expected {
			if expected.Name == request.Guard.Name && expected.NativeAttributesSHA256 == request.Guard.NativeAttributesSHA256 {
				guardFound = true
				break
			}
		}
		if !guardFound {
			return fmt.Errorf("Windows NRPT guard is outside the complete expected rule set")
		}
	}
	if request.Operation == "observe" && (len(request.Expected) != 0 || request.Guard != (windowsNRPTGuard{})) {
		return fmt.Errorf("Windows NRPT observation contains mutation authority")
	}
	if request.Operation == "ensure" && !request.Guard.Exists && len(request.Expected) != 0 {
		return fmt.Errorf("Windows NRPT creation requires an absent relevant rule set")
	}
	if request.Operation == "release" && !request.Guard.Exists {
		return fmt.Errorf("Windows NRPT release requires an existing guard")
	}
	return nil
}

// run executes only the embedded program and bounds both output streams without command interpolation.
func (windowsNativePowerShellRunner) run(ctx context.Context, input []byte) ([]byte, error) {
	stdout := newWindowsNRPTBoundedBuffer(maximumWindowsNRPTOutputBytes)
	stderr := newWindowsNRPTBoundedBuffer(maximumWindowsNRPTDiagnosticBytes)
	command := exec.CommandContext(
		ctx,
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-EncodedCommand",
		windowsNRPTEncodedPowerShell(),
	)
	command.Stdin = bytes.NewReader(input)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.exceeded {
		return nil, fmt.Errorf("Windows NRPT output exceeds %d bytes", maximumWindowsNRPTOutputBytes)
	}
	if stderr.exceeded {
		return nil, fmt.Errorf("Windows NRPT diagnostic exceeds %d bytes", maximumWindowsNRPTDiagnosticBytes)
	}
	if err != nil {
		detail := windowsNRPTDisplayDiagnostic(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("execute Windows NRPT PowerShell: %w", err)
		}
		return nil, fmt.Errorf("execute Windows NRPT PowerShell: %w: %s", err, detail)
	}
	if stderr.Len() != 0 {
		return nil, fmt.Errorf("Windows NRPT PowerShell wrote unexpected diagnostics: %s", windowsNRPTDisplayDiagnostic(stderr.String()))
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

// windowsNRPTBoundedBuffer retains only a fixed prefix while allowing the child process to terminate normally.
type windowsNRPTBoundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

// newWindowsNRPTBoundedBuffer constructs one process stream limit.
func newWindowsNRPTBoundedBuffer(maximum int) *windowsNRPTBoundedBuffer {
	return &windowsNRPTBoundedBuffer{maximum: maximum}
}

// Write consumes all child bytes while retaining at most the configured bound.
func (buffer *windowsNRPTBoundedBuffer) Write(payload []byte) (int, error) {
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining < len(payload) {
		buffer.exceeded = true
		if remaining > 0 {
			_, _ = buffer.buffer.Write(payload[:remaining])
		}
		return len(payload), nil
	}
	_, _ = buffer.buffer.Write(payload)
	return len(payload), nil
}

// Bytes returns the retained stream prefix.
func (buffer *windowsNRPTBoundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

// Len returns the retained stream length.
func (buffer *windowsNRPTBoundedBuffer) Len() int {
	return buffer.buffer.Len()
}

// String returns the retained stream prefix as text.
func (buffer *windowsNRPTBoundedBuffer) String() string {
	return buffer.buffer.String()
}

// windowsNRPTDisplayDiagnostic keeps native failures single-line and bounded before they enter local diagnostics.
func windowsNRPTDisplayDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maximumWindowsNRPTDisplayError {
		value = value[:maximumWindowsNRPTDisplayError]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

// decodeWindowsNRPTSnapshot accepts only one bounded exact observation envelope.
func decodeWindowsNRPTSnapshot(body []byte) (windowsNRPTSnapshotResponse, error) {
	if len(body) == 0 || len(body) > maximumWindowsNRPTOutputBytes {
		return windowsNRPTSnapshotResponse{}, fmt.Errorf("Windows NRPT snapshot has an invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response windowsNRPTSnapshotResponse
	if err := decoder.Decode(&response); err != nil {
		return windowsNRPTSnapshotResponse{}, fmt.Errorf("decode Windows NRPT snapshot: %w", err)
	}
	if err := requireWindowsNRPTJSONEnd(decoder); err != nil {
		return windowsNRPTSnapshotResponse{}, err
	}
	if response.Rules == nil || len(response.Rules) > maximumRuleFacts {
		return windowsNRPTSnapshotResponse{}, fmt.Errorf("Windows NRPT snapshot rules are missing or exceed their bound")
	}
	for _, rule := range response.Rules {
		if err := validateWindowsNRPTRule(rule); err != nil {
			return windowsNRPTSnapshotResponse{}, err
		}
	}
	return response, nil
}

// decodeWindowsNRPTMutation requires the static program's one exact acknowledgement.
func decodeWindowsNRPTMutation(body []byte) error {
	if string(bytes.TrimSpace(body)) != `{"ok":true}` {
		return fmt.Errorf("Windows NRPT mutation returned an invalid acknowledgement")
	}
	return nil
}

// requireWindowsNRPTJSONEnd rejects a second value after the one expected response.
func requireWindowsNRPTJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("Windows NRPT response must contain exactly one JSON value")
	}
	return nil
}

// windowsNRPTEncodedPowerShell returns the immutable script in Windows PowerShell's UTF-16LE command format.
func windowsNRPTEncodedPowerShell() string {
	runes := utf16.Encode([]rune(windowsNRPTPowerShellProgram))
	encoded := make([]byte, 0, len(runes)*2)
	for _, value := range runes {
		encoded = binary.LittleEndian.AppendUint16(encoded, value)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

const windowsNRPTPowerShellProgram = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'
$WarningPreference = 'Stop'
Set-StrictMode -Version 3.0

function Require-Fields([object]$Value, [string[]]$Names, [string]$Label) {
    if ($null -eq $Value) { throw "$Label is missing" }
    $actual = @($Value.PSObject.Properties.Name | Sort-Object -CaseSensitive)
    $wanted = @($Names | Sort-Object -CaseSensitive)
    if ($actual.Count -ne $wanted.Count) { throw "$Label has the wrong field count" }
    for ($index = 0; $index -lt $wanted.Count; $index++) {
        if ($actual[$index] -cne $wanted[$index]) { throw "$Label contains an unsupported field" }
    }
}

function Text([object]$Value) {
    if ($null -eq $Value) { return '' }
    return [string]$Value
}

function Texts([object]$Value) {
    if ($null -eq $Value) { return @() }
    return @($Value | ForEach-Object { [string]$_ })
}

function Convert-Rule([object]$Rule) {
    return [ordered]@{
        version = [uint32]$Rule.Version
        name = Text $Rule.Name
        namespaces = @(Texts $Rule.Namespace)
        ipsec_ca_restriction = Text $Rule.IPsecCARestriction
        direct_access_dns_servers = @(Texts $Rule.DirectAccessDnsServers)
        direct_access_enabled = [bool]$Rule.DirectAccessEnabled
        direct_access_proxy_type = Text $Rule.DirectAccessProxyType
        direct_access_proxy_name = Text $Rule.DirectAccessProxyName
        direct_access_query_ipsec_encryption = Text $Rule.DirectAccessQueryIPsecEncryption
        direct_access_query_ipsec_required = [bool]$Rule.DirectAccessQueryIPsecRequired
        name_servers = @(Texts $Rule.NameServers)
        dnssec_enabled = [bool]$Rule.DnsSecEnabled
        dnssec_query_ipsec_encryption = Text $Rule.DnsSecQueryIPsecEncryption
        dnssec_query_ipsec_required = [bool]$Rule.DnsSecQueryIPsecRequired
        dnssec_validation_required = [bool]$Rule.DnsSecValidationRequired
        name_encoding = Text $Rule.NameEncoding
        display_name = Text $Rule.DisplayName
        comment = Text $Rule.Comment
    }
}

function Test-RelevantNamespace([string]$Namespace, [string]$Suffix) {
    $lower = $Namespace.ToLowerInvariant()
    if ($lower -eq '.') { return $true }
    if (-not $lower.StartsWith('.')) { $lower = '.' + $lower }
    return $lower -eq $Suffix -or $lower.EndsWith($Suffix, [StringComparison]::Ordinal)
}

function Test-RelevantRule([object]$Rule, [string]$Suffix, [string]$DisplayName) {
    if ([string]$Rule.display_name -ceq $DisplayName) { return $true }
    foreach ($namespace in @($Rule.namespaces)) {
        if (Test-RelevantNamespace ([string]$namespace) $Suffix) { return $true }
    }
    return $false
}

function Add-TextLine([Collections.Generic.List[string]]$Lines, [string]$Value) {
    $bytes = [Text.Encoding]::UTF8.GetBytes($Value)
    $Lines.Add([Convert]::ToBase64String($bytes))
}

function Add-ArrayLines([Collections.Generic.List[string]]$Lines, [object[]]$Values) {
    $Lines.Add([string]$Values.Count)
    foreach ($value in $Values) { Add-TextLine $Lines ([string]$value) }
}

function Add-BoolLine([Collections.Generic.List[string]]$Lines, [bool]$Value) {
    if ($Value) { $Lines.Add('1') } else { $Lines.Add('0') }
}

function Get-RuleFingerprint([object]$Rule) {
    $lines = New-Object 'Collections.Generic.List[string]'
    $lines.Add('goforj.harbor.windows-nrpt-rule.v1')
    $lines.Add(([uint32]$Rule.version).ToString([Globalization.CultureInfo]::InvariantCulture))
    Add-ArrayLines $lines @($Rule.namespaces)
    Add-TextLine $lines ([string]$Rule.name)
    Add-TextLine $lines ([string]$Rule.ipsec_ca_restriction)
    Add-ArrayLines $lines @($Rule.direct_access_dns_servers)
    Add-BoolLine $lines ([bool]$Rule.direct_access_enabled)
    Add-TextLine $lines ([string]$Rule.direct_access_proxy_type)
    Add-TextLine $lines ([string]$Rule.direct_access_proxy_name)
    Add-TextLine $lines ([string]$Rule.direct_access_query_ipsec_encryption)
    Add-BoolLine $lines ([bool]$Rule.direct_access_query_ipsec_required)
    Add-ArrayLines $lines @($Rule.name_servers)
    Add-BoolLine $lines ([bool]$Rule.dnssec_enabled)
    Add-TextLine $lines ([string]$Rule.dnssec_query_ipsec_encryption)
    Add-BoolLine $lines ([bool]$Rule.dnssec_query_ipsec_required)
    Add-BoolLine $lines ([bool]$Rule.dnssec_validation_required)
    Add-TextLine $lines ([string]$Rule.name_encoding)
    Add-TextLine $lines ([string]$Rule.display_name)
    Add-TextLine $lines ([string]$Rule.comment)
    $lineFeed = [string][char]10
    $payload = [Text.Encoding]::UTF8.GetBytes(([string]::Join($lineFeed, $lines) + $lineFeed))
    $algorithm = [Security.Cryptography.SHA256]::Create()
    try { $digest = $algorithm.ComputeHash($payload) } finally { $algorithm.Dispose() }
    return ([BitConverter]::ToString($digest)).Replace('-', '').ToLowerInvariant()
}

function Get-RelevantRules([string]$Suffix, [string]$DisplayName) {
    $result = New-Object 'Collections.Generic.List[object]'
    $names = New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
    foreach ($native in @(Get-DnsClientNrptRule -ErrorAction Stop)) {
        $rule = Convert-Rule $native
        if (-not (Test-RelevantRule $rule $Suffix $DisplayName)) { continue }
        if (-not $names.Add([string]$rule.name)) { throw 'NRPT enumeration repeated a native rule name' }
        $result.Add($rule)
        if ($result.Count -gt 256) { throw 'NRPT relevant rule count exceeds 256' }
    }
    return @($result | Sort-Object -Property name -CaseSensitive)
}

function Assert-Expected([object[]]$Current, [object[]]$Expected) {
    if ($Current.Count -ne $Expected.Count) { throw 'NRPT relevant rule set changed before mutation' }
    for ($index = 0; $index -lt $Expected.Count; $index++) {
        Require-Fields $Expected[$index] @('name', 'native_attributes_sha256') 'expected rule'
        if ([string]$Current[$index].name -cne [string]$Expected[$index].name -or
            (Get-RuleFingerprint $Current[$index]) -cne [string]$Expected[$index].native_attributes_sha256) {
            throw 'NRPT relevant rule set changed before mutation'
        }
    }
}

try {
    $inputText = [Console]::In.ReadToEnd()
    if ($inputText.Length -eq 0 -or $inputText.Length -gt 262144) { throw 'NRPT command input has an invalid size' }
    $request = $inputText | ConvertFrom-Json -ErrorAction Stop
    Require-Fields $request @('operation', 'suffix', 'display_name', 'comment', 'server', 'expected', 'guard') 'request'
    Require-Fields $request.guard @('exists', 'name', 'native_attributes_sha256') 'guard'
    if ([string]$request.suffix -cne '.test') { throw 'NRPT suffix is not authorized' }
    if ([string]$request.operation -notin @('observe', 'ensure', 'release')) { throw 'NRPT operation is not authorized' }
    $serverAddress = $null
    if (-not [Net.IPAddress]::TryParse([string]$request.server, [ref]$serverAddress) -or
        $serverAddress.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork -or
        -not $serverAddress.ToString().StartsWith('127.')) {
        throw 'NRPT server is not an IPv4 loopback address'
    }

    $current = @(Get-RelevantRules ([string]$request.suffix) ([string]$request.display_name))
    if ([string]$request.operation -ceq 'observe') {
        $response = [ordered]@{ rules = @($current) }
        [Console]::Out.Write(($response | ConvertTo-Json -Depth 5 -Compress))
        exit 0
    }

    $expected = @($request.expected)
    Assert-Expected $current $expected
    $guard = $request.guard
    if ([bool]$guard.exists) {
        $target = @($current | Where-Object { [string]$_.name -ceq [string]$guard.name })
        if ($target.Count -ne 1 -or (Get-RuleFingerprint $target[0]) -cne [string]$guard.native_attributes_sha256) {
            throw 'NRPT guarded rule changed before mutation'
        }
    } elseif ([string]$guard.name -ne '' -or [string]$guard.native_attributes_sha256 -ne '') {
        throw 'NRPT absent guard contains native identity'
    }

    if ([string]$request.operation -ceq 'ensure') {
        if ([bool]$guard.exists) {
            Set-DnsClientNrptRule -Name ([string]$guard.name) -Namespace @([string]$request.suffix) -NameServers @([string]$request.server) -NameEncoding 'Disable' -DisplayName ([string]$request.display_name) -Comment ([string]$request.comment) -DAEnable $false -DAIPsecRequired $false -DnsSecEnable $false -DnsSecIPsecRequired $false -DnsSecValidationRequired $false -Confirm:$false -ErrorAction Stop | Out-Null
        } else {
            Add-DnsClientNrptRule -Namespace @([string]$request.suffix) -NameServers @([string]$request.server) -NameEncoding 'Disable' -DisplayName ([string]$request.display_name) -Comment ([string]$request.comment) -Confirm:$false -ErrorAction Stop | Out-Null
        }
    } else {
        if (-not [bool]$guard.exists) { throw 'NRPT release requires an existing guard' }
        Remove-DnsClientNrptRule -Name ([string]$guard.name) -Force -Confirm:$false -ErrorAction Stop | Out-Null
    }
    [Console]::Out.Write('{"ok":true}')
} catch {
    $message = [string]$_.Exception.Message
    if ($message.Length -gt 4096) { $message = $message.Substring(0, 4096) }
    [Console]::Error.Write($message)
    exit 1
}`
