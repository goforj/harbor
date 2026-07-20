//go:build linux

package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/miekg/dns"
	"golang.org/x/sys/unix"
)

// TestSystemdResolvedDirectoryLockHonorsContext verifies a competing mutation cannot block forever.
func TestSystemdResolvedDirectoryLockHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(first) error = %v", err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(second) error = %v", err)
	}
	defer second.Close()
	if err := unix.Flock(int(first.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("Flock(first) error = %v", err)
	}
	defer unix.Flock(int(first.Fd()), unix.LOCK_UN)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := lockSystemdResolvedDirectory(ctx, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockSystemdResolvedDirectory() error = %v, want deadline exceeded", err)
	}

	if err := unix.Flock(int(first.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("unlock first error = %v", err)
	}
	if err := lockSystemdResolvedDirectory(t.Context(), second); err != nil {
		t.Fatalf("lockSystemdResolvedDirectory(after release) error = %v", err)
	}
	if err := unlockSystemdResolvedDirectory(second); err != nil {
		t.Fatalf("unlock second error = %v", err)
	}
}

// TestSystemdResolvedArtifactOwnedByRequestRequiresExactMarkerAndSecurity proves recovery cannot adopt foreign bytes.
func TestSystemdResolvedArtifactOwnedByRequestRequiresExactMarkerAndSecurity(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	metadata := systemdResolvedArtifactMetadata{
		Regular:   true,
		Device:    1,
		Inode:     2,
		UID:       0,
		GID:       0,
		Mode:      systemdResolvedFileMode,
		LinkCount: 1,
	}
	artifact := systemdResolvedArtifact{Exists: true, Content: marshalSystemdResolvedValidated(request), Metadata: metadata}
	if !systemdResolvedArtifactOwnedByRequest(artifact, request) {
		t.Fatal("exact owned artifact was rejected")
	}
	foreignRequest, err := NewRequest("installation-foreign", request.Policy())
	if err != nil {
		t.Fatalf("NewRequest(foreign) error = %v", err)
	}
	if systemdResolvedArtifactOwnedByRequest(artifact, foreignRequest) {
		t.Fatal("foreign owner marker was accepted")
	}
	unsafe := artifact
	unsafe.Metadata.Mode = 0o664
	if systemdResolvedArtifactOwnedByRequest(unsafe, request) {
		t.Fatal("unsafe artifact shape was accepted")
	}
	malformed := artifact
	malformed.Content = []byte("[Resolve]\nDNS=127.0.0.1\n")
	if systemdResolvedArtifactOwnedByRequest(malformed, request) {
		t.Fatal("artifact without Harbor marker was accepted")
	}
}

// TestSystemdResolvedBusctlParserRetainsCompleteRelevantState covers exact routes, foreign subdomains, and global occupancy.
func TestSystemdResolvedBusctlParserRetainsCompleteRelevantState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	output := []byte(
		`{"type":"a(isb)","data":[[0,"test",true],[3,"child.test",true],[2,"example",false]]}` + "\n" +
			`{"type":"a(iiayqs)","data":[[0,2,[127,0,0,1],25000,""],[3,2,[192,0,2,53],53,""]]}` + "\n",
	)
	rules, err := parseSystemdResolvedBusctlProperties(output, request)
	if err != nil {
		t.Fatalf("parseSystemdResolvedBusctlProperties() error = %v", err)
	}
	if len(rules) != 2 || rules[0].InterfaceIndex != 0 || rules[0].Namespace != ".test" ||
		rules[0].Servers[0].Endpoint != request.Endpoint() || rules[1].InterfaceIndex != 3 ||
		rules[1].Namespace != ".child.test" || rules[1].Servers[0].Endpoint.String() != "192.0.2.53:53" {
		t.Fatalf("parseSystemdResolvedBusctlProperties() = %#v", rules)
	}

	occupancyOutput := []byte(
		`{"type":"a(isb)","data":[[2,"example",true]]}` + "\n" +
			`{"type":"a(iiayqs)","data":[[0,2,[192,0,2,54],53,""]]}` + "\n",
	)
	occupancy, err := parseSystemdResolvedBusctlProperties(occupancyOutput, request)
	if err != nil {
		t.Fatalf("parseSystemdResolvedBusctlProperties() occupancy error = %v", err)
	}
	if len(occupancy) != 1 || occupancy[0].Namespace != request.Suffix() || occupancy[0].RouteOnly ||
		occupancy[0].Servers[0].Endpoint.String() != "192.0.2.54:53" {
		t.Fatalf("global DNS occupancy = %#v", occupancy)
	}

	rootRouteOutput := []byte(
		`{"type":"a(isb)","data":[[2,".",true]]}` + "\n" +
			`{"type":"a(iiayqs)","data":[[2,2,[192,0,2,55],53,""]]}` + "\n",
	)
	rootRoutes, err := parseSystemdResolvedBusctlProperties(rootRouteOutput, request)
	if err != nil || len(rootRoutes) != 0 {
		t.Fatalf("root route parse = %#v, %v, want no equal-or-more-specific claims", rootRoutes, err)
	}
}

// TestSystemdResolvedBusctlParserRejectsMalformedOrAmbiguousState covers strict signatures, tuples, and bounds.
func TestSystemdResolvedBusctlParserRejectsMalformedOrAmbiguousState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	domains := `{"type":"a(isb)","data":[[0,"test",true]]}`
	servers := `{"type":"a(iiayqs)","data":[[0,2,[127,0,0,1],25000,""]]}`
	tests := []struct {
		name   string
		output []byte
	}{
		{name: "empty"},
		{name: "one property", output: []byte(domains)},
		{name: "extra property", output: []byte(domains + "\n" + servers + "\n{}")},
		{name: "wrong signature", output: []byte(strings.Replace(domains, "a(isb)", "a(ssb)", 1) + "\n" + servers)},
		{name: "unknown envelope field", output: []byte(strings.Replace(domains, `}`, `,"extra":true}`, 1) + "\n" + servers)},
		{name: "domain tuple width", output: []byte(strings.Replace(domains, `,true]`, `]`, 1) + "\n" + servers)},
		{name: "server tuple width", output: []byte(domains + "\n" + strings.Replace(servers, `,""]]`, `]]`, 1))},
		{name: "negative interface", output: []byte(strings.Replace(domains, `[[0,`, `[[-1,`, 1) + "\n" + servers)},
		{name: "unsupported family", output: []byte(domains + "\n" + strings.Replace(servers, `,2,[`, `,99,[`, 1))},
		{name: "wrong address size", output: []byte(domains + "\n" + strings.Replace(servers, `[127,0,0,1]`, `[127,0,0]`, 1))},
		{name: "zero port", output: []byte(domains + "\n" + strings.Replace(servers, `25000`, `0`, 1))},
		{name: "invalid server name", output: []byte(domains + "\n" + strings.Replace(servers, `""]]`, `"DNS.Test"]]`, 1))},
		{name: "duplicate route", output: []byte(strings.Replace(domains, `]]}`, `],[0,"test",true]]}`, 1) + "\n" + servers)},
		{name: "conflicting route flags", output: []byte(strings.Replace(domains, `]]}`, `],[0,"test",false]]}`, 1) + "\n" + servers)},
		{name: "duplicate server", output: []byte(domains + "\n" + strings.Replace(servers, `]]}`, `],[0,2,[127,0,0,1],25000,""]]}`, 1))},
		{name: "oversized", output: bytes.Repeat([]byte{'x'}, maximumSystemdCommandOutputBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSystemdResolvedBusctlProperties(test.output, request); err == nil {
				t.Fatalf("parseSystemdResolvedBusctlProperties(%q) succeeded", test.output)
			}
		})
	}
}

// TestBoundedSystemdCommandBufferRejectsOverflow proves subprocess output cannot grow beyond parser authority.
func TestBoundedSystemdCommandBufferRejectsOverflow(t *testing.T) {
	buffer := new(boundedSystemdCommandBuffer)
	first := bytes.Repeat([]byte{'a'}, maximumSystemdCommandOutputBytes)
	if written, err := buffer.Write(first); err != nil || written != len(first) {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte{'b'}); err == nil || written != 0 || len(buffer.bytes) != len(first) {
		t.Fatalf("overflow Write() = %d, %v, retained = %d", written, err, len(buffer.bytes))
	}
}

// TestFixedSystemdCommandRunnerRejectsCallerPaths proves privileged execution cannot be redirected to another binary.
func TestFixedSystemdCommandRunnerRejectsCallerPaths(t *testing.T) {
	command, err := openFixedSystemdCommand(fixedSystemdBusctlPath)
	if err != nil {
		t.Fatalf("openFixedSystemdCommand() error = %v", err)
	}
	if err := command.Close(); err != nil {
		t.Fatalf("Close() fixed command error = %v", err)
	}
	if _, err := runFixedSystemdCommand(t.Context(), "/bin/true"); err == nil {
		t.Fatal("runFixedSystemdCommand() accepted a caller-selected binary")
	}
	if _, err := runFixedSystemdCommand(t.Context(), fixedSystemctlPath, "status"); err == nil {
		t.Fatal("runFixedSystemdCommand() accepted caller-selected systemctl arguments")
	}
}

// TestSystemdResolvedFixedAncestorsAreSafelyOpenable exercises the retained no-follow path walk without creating state.
func TestSystemdResolvedFixedAncestorsAreSafelyOpenable(t *testing.T) {
	directory, err := openSystemdResolvedDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("openSystemdResolvedDirectory(false) error = %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestSystemdResolvedTransactionScanFindsLateCrashRemnants proves ordinary drop-ins cannot hide a private transaction.
func TestSystemdResolvedTransactionScanFindsLateCrashRemnants(t *testing.T) {
	directoryPath := t.TempDir()
	for index := 0; index < maximumSystemdResolvedTransactions+8; index++ {
		path := filepath.Join(directoryPath, fmt.Sprintf("ordinary-%03d.conf", index))
		if err := os.WriteFile(path, []byte("[Resolve]\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	transaction := filepath.Join(directoryPath, systemdResolvedStagePrefix+strings.Repeat("a", systemdResolvedTransactionHexBytes*2))
	if err := os.WriteFile(transaction, []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile(transaction) error = %v", err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer directory.Close()
	if err := requireNoSystemdResolvedTransactionsAt(directory); err == nil {
		t.Fatal("requireNoSystemdResolvedTransactionsAt() accepted a late transaction")
	}
}

// TestSystemdResolvedTransactionScanAdmitsOnlyTheRetainedName proves mutation verification cannot overlook a second writer.
func TestSystemdResolvedTransactionScanAdmitsOnlyTheRetainedName(t *testing.T) {
	directoryPath := t.TempDir()
	allowed := systemdResolvedStagePrefix + strings.Repeat("a", systemdResolvedTransactionHexBytes*2)
	if err := os.WriteFile(filepath.Join(directoryPath, allowed), []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile(allowed) error = %v", err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer directory.Close()
	if err := requireSystemdResolvedTransactionsAt(directory, allowed); err != nil {
		t.Fatalf("requireSystemdResolvedTransactionsAt() allowed error = %v", err)
	}
	unexpected := systemdResolvedQuarantinePrefix + strings.Repeat("b", systemdResolvedTransactionHexBytes*2)
	if err := os.WriteFile(filepath.Join(directoryPath, unexpected), []byte("pending"), 0o600); err != nil {
		t.Fatalf("WriteFile(unexpected) error = %v", err)
	}
	if err := requireSystemdResolvedTransactionsAt(directory, allowed); err == nil {
		t.Fatal("requireSystemdResolvedTransactionsAt() accepted a second transaction")
	}
}

// TestVerifySystemdResolvedReleasePreservesForeignRuntime pins the exact post-removal runtime expectation.
func TestVerifySystemdResolvedReleasePreservesForeignRuntime(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	exact := exactSystemdResolvedTestRuntime(request)
	foreign := cloneSystemdResolvedTestRuntime(exact)
	foreign.InterfaceIndex = 3
	foreign.Namespace = ".child.test"
	foreign.Servers[0].InterfaceIndex = 3
	foreign.Servers[0].Endpoint = request.Policy().HTTP.Bind
	before := systemdResolvedSnapshot{
		Artifact: secureSystemdResolvedTestArtifact(marshalSystemdResolvedValidated(request), 60),
		Runtime:  []systemdResolvedRuntimeRule{exact, foreign},
	}
	slices.SortFunc(before.Runtime, compareSystemdResolvedRuntimeRule)
	after := systemdResolvedSnapshot{Runtime: []systemdResolvedRuntimeRule{cloneSystemdResolvedTestRuntime(foreign)}}
	if err := verifySystemdResolvedRelease(before, after, request); err != nil {
		t.Fatalf("verifySystemdResolvedRelease() error = %v", err)
	}
	after.Runtime[0].Servers[0].Endpoint = request.Endpoint()
	if err := verifySystemdResolvedRelease(before, after, request); err == nil {
		t.Fatal("verifySystemdResolvedRelease() accepted foreign runtime mutation")
	}
}

// TestSystemdResolvedCapturedArtifactAllowsOnlyRenameMetadata proves quarantine keeps exact bytes and authority shape.
func TestSystemdResolvedCapturedArtifactAllowsOnlyRenameMetadata(t *testing.T) {
	directory := t.TempDir()
	beforePath := filepath.Join(directory, "before.conf")
	afterPath := filepath.Join(directory, "after.conf")
	if err := os.WriteFile(beforePath, []byte("[Resolve]\nDomains=~test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	beforeFile, err := os.Open(beforePath)
	if err != nil {
		t.Fatalf("Open(before) error = %v", err)
	}
	before, err := readSystemdResolvedArtifactFile(beforeFile)
	if err != nil {
		t.Fatalf("readSystemdResolvedArtifactFile(before) error = %v", err)
	}
	if err := os.Rename(beforePath, afterPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	afterFile, err := os.Open(afterPath)
	if err != nil {
		t.Fatalf("Open(after) error = %v", err)
	}
	after, err := readSystemdResolvedArtifactFile(afterFile)
	if err != nil {
		t.Fatalf("readSystemdResolvedArtifactFile(after) error = %v", err)
	}
	if !sameSystemdResolvedCapturedArtifact(before, after) {
		t.Fatalf("rename changed admitted artifact: before %#v, after %#v", before.Metadata, after.Metadata)
	}
	after.Metadata.Mode = 0o666
	if sameSystemdResolvedCapturedArtifact(before, after) {
		t.Fatal("captured artifact accepted changed mode")
	}
}

// TestPrivilegedSystemdResolvedAdapterLifecycle exercises the production fixed artifact and resolve1 state when opted in.
func TestPrivilegedSystemdResolvedAdapterLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 on a disposable Ubuntu systemd host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged systemd-resolved lifecycle requires root")
	}
	if output, err := exec.Command(fixedSystemctlPath, "is-active", "--quiet", fixedSystemdResolvedService).CombinedOutput(); err != nil {
		t.Fatalf("systemd-resolved is not active: %v: %s", err, output)
	}
	if _, err := os.Lstat(fixedSystemdResolvedPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("fixed Harbor systemd-resolved artifact must be absent before lifecycle test: %v", err)
	}

	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	startSystemdResolvedTestDNSServer(t, request.Endpoint().String())
	adapter := New()
	before, err := observePrivilegedSystemdResolved(t.Context(), adapter, request)
	if err != nil {
		t.Fatalf("Observe() before error = %v", err)
	}
	assessment, err := before.Classify()
	if err != nil || assessment.State != StateAbsent {
		t.Fatalf("Classify() before = %#v, %v; host must have no equal-or-more-specific or global DNS occupancy", assessment, err)
	}
	defer func() {
		observation, observeErr := adapter.Observe(t.Context(), request)
		if observeErr != nil {
			t.Errorf("cleanup Observe() error = %v", observeErr)
			return
		}
		if _, releaseErr := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, observation)); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v", releaseErr)
		}
	}()
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, before))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	ensuredAssessment, err := ensured.After.Classify()
	if err != nil || ensuredAssessment.State != StateExact {
		t.Fatalf("Classify() ensured = %#v, %v", ensuredAssessment, err)
	}
	queryOutput, err := exec.Command(
		"/usr/bin/resolvectl",
		"query",
		"--legend=no",
		"--type=A",
		"harbor-ci-resolver.test.",
	).CombinedOutput()
	if err != nil || !bytes.Contains(queryOutput, []byte("127.77.0.1")) {
		t.Fatalf("resolvectl query did not traverse Harbor's route: %v: %s", err, queryOutput)
	}
	released, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, ensured.After))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	releasedAssessment, err := released.After.Classify()
	if err != nil || releasedAssessment.State != StateAbsent {
		t.Fatalf("Classify() released = %#v, %v", releasedAssessment, err)
	}
}

// observePrivilegedSystemdResolved adds a bounded native cause only to the opt-in root-host diagnostic path.
func observePrivilegedSystemdResolved(ctx context.Context, adapter *Adapter, request Request) (Observation, error) {
	observation, err := adapter.Observe(ctx, request)
	if err == nil {
		return observation, nil
	}
	var resolverError *Error
	if !errors.As(err, &resolverError) || resolverError.Unwrap() == nil {
		return observation, err
	}
	cause := resolverError.Unwrap().Error()
	const maximumDiagnosticBytes = 4096
	if len(cause) > maximumDiagnosticBytes {
		cause = cause[:maximumDiagnosticBytes] + "...[truncated]"
	}
	return observation, fmt.Errorf("%w; native cause: %s", err, cause)
}

// TestPrivilegedSystemdResolvedAdapterCrashRecovery proves Harbor repairs only its own interrupted stage and quarantine states.
func TestPrivilegedSystemdResolvedAdapterCrashRecovery(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 on a disposable Ubuntu systemd host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged systemd-resolved recovery requires root")
	}
	if output, err := exec.Command(fixedSystemctlPath, "is-active", "--quiet", fixedSystemdResolvedService).CombinedOutput(); err != nil {
		t.Fatalf("systemd-resolved is not active: %v: %s", err, output)
	}
	if _, err := os.Lstat(fixedSystemdResolvedPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("fixed Harbor systemd-resolved artifact must be absent before recovery test: %v", err)
	}

	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	startSystemdResolvedTestDNSServer(t, request.Endpoint().String())
	adapter := New()
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), systemdResolvedCommandTimeout)
		defer cancel()
		observation, observeErr := adapter.Observe(cleanupContext, request)
		if observeErr != nil {
			t.Errorf("cleanup Observe() error = %v", observeErr)
			return
		}
		if _, releaseErr := adapter.ReleaseIfObserved(cleanupContext, request, resolverFingerprint(t, observation)); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v", releaseErr)
		}
	})
	directory, err := openSystemdResolvedDirectory(true)
	if err != nil {
		t.Fatalf("openSystemdResolvedDirectory() error = %v", err)
	}
	defer directory.Close()
	if err := lockSystemdResolvedDirectory(t.Context(), directory); err != nil {
		t.Fatalf("lockSystemdResolvedDirectory() error = %v", err)
	}
	stageName, _, err := createSystemdResolvedStaging(directory, marshalSystemdResolvedValidated(request))
	if unlockErr := unlockSystemdResolvedDirectory(directory); unlockErr != nil {
		t.Fatalf("unlockSystemdResolvedDirectory() error = %v", unlockErr)
	}
	if err != nil {
		t.Fatalf("createSystemdResolvedStaging() error = %v", err)
	}

	observation, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() after staged crash error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateAbsent {
		t.Fatalf("Classify() after staged crash = %#v, %v", assessment, err)
	}
	if _, err := os.Lstat(filepath.Join(fixedSystemdResolvedDirectory, stageName)); !os.IsNotExist(err) {
		t.Fatalf("staged crash artifact remains after recovery: %v", err)
	}

	_, err = adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, observation))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	directory, err = openSystemdResolvedDirectory(false)
	if err != nil {
		t.Fatalf("reopen systemd-resolved directory error = %v", err)
	}
	defer directory.Close()
	if err := lockSystemdResolvedDirectory(t.Context(), directory); err != nil {
		t.Fatalf("relock systemd-resolved directory error = %v", err)
	}
	quarantineName, err := uniqueSystemdResolvedTransactionName(directory, systemdResolvedQuarantinePrefix)
	if err == nil {
		err = unix.Renameat2(
			int(directory.Fd()), fixedSystemdResolvedName,
			int(directory.Fd()), quarantineName,
			unix.RENAME_NOREPLACE,
		)
	}
	if err == nil {
		err = directory.Sync()
	}
	if unlockErr := unlockSystemdResolvedDirectory(directory); unlockErr != nil && err == nil {
		err = unlockErr
	}
	if err != nil {
		t.Fatalf("quarantine exact Harbor artifact error = %v", err)
	}

	recovered, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() after quarantined crash error = %v", err)
	}
	recoveredAssessment, err := recovered.Classify()
	if err != nil || recoveredAssessment.State != StateExact {
		t.Fatalf("Classify() after quarantined crash = %#v, %v", recoveredAssessment, err)
	}
	if _, err := os.Lstat(filepath.Join(fixedSystemdResolvedDirectory, quarantineName)); !os.IsNotExist(err) {
		t.Fatalf("quarantine crash artifact remains after recovery: %v", err)
	}
	if _, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, recovered)); err != nil {
		t.Fatalf("ReleaseIfObserved() cleanup error = %v", err)
	}
}

// TestPrivilegedSystemdResolvedRecoveryPreservesForeignQuarantine proves a reserved Harbor transaction name alone never authorizes removal.
func TestPrivilegedSystemdResolvedRecoveryPreservesForeignQuarantine(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_RESOLVER_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_RESOLVER_TEST=1 on a disposable Ubuntu systemd host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged systemd-resolved recovery requires root")
	}
	if output, err := exec.Command(fixedSystemctlPath, "is-active", "--quiet", fixedSystemdResolvedService).CombinedOutput(); err != nil {
		t.Fatalf("systemd-resolved is not active: %v: %s", err, output)
	}
	if _, err := os.Lstat(fixedSystemdResolvedPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("fixed Harbor systemd-resolved artifact must be absent before foreign recovery test: %v", err)
	}

	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	foreignRequest, err := NewRequest("installation-foreign", request.Policy())
	if err != nil {
		t.Fatalf("NewRequest(foreign) error = %v", err)
	}
	directory, err := openSystemdResolvedDirectory(true)
	if err != nil {
		t.Fatalf("openSystemdResolvedDirectory() error = %v", err)
	}
	defer directory.Close()
	if err := lockSystemdResolvedDirectory(t.Context(), directory); err != nil {
		t.Fatalf("lockSystemdResolvedDirectory() error = %v", err)
	}
	quarantineName, nameErr := uniqueSystemdResolvedTransactionName(directory, systemdResolvedQuarantinePrefix)
	if unlockErr := unlockSystemdResolvedDirectory(directory); unlockErr != nil && nameErr == nil {
		nameErr = unlockErr
	}
	if nameErr != nil {
		t.Fatalf("allocate foreign quarantine name error = %v", nameErr)
	}
	path := filepath.Join(fixedSystemdResolvedDirectory, quarantineName)
	content := marshalSystemdResolvedValidated(foreignRequest)
	if err := os.WriteFile(path, content, os.FileMode(systemdResolvedFileMode)); err != nil {
		t.Fatalf("write foreign quarantine fixture error = %v", err)
	}
	t.Cleanup(func() {
		retained, readErr := os.ReadFile(path)
		if readErr != nil {
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Errorf("read foreign quarantine fixture during cleanup: %v", readErr)
			}
			return
		}
		if !bytes.Equal(retained, content) {
			t.Errorf("preserve foreign quarantine fixture because its content changed")
			return
		}
		if removeErr := os.Remove(path); removeErr != nil {
			t.Errorf("remove foreign quarantine fixture: %v", removeErr)
		}
	})

	_, err = New().Observe(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "not owned by this request") {
		t.Fatalf("Observe() error = %v, want foreign quarantine rejection", err)
	}
	retained, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read foreign quarantine after recovery error = %v", err)
	}
	if !bytes.Equal(retained, content) {
		t.Fatal("foreign quarantine bytes changed during recovery")
	}
}

// startSystemdResolvedTestDNSServer serves one deterministic A record through Harbor's nonstandard DNS socket.
func startSystemdResolvedTestDNSServer(t *testing.T, address string) {
	t.Helper()
	connection, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatalf("ListenPacket(%q) error = %v", address, err)
	}
	started := make(chan struct{})
	serveResult := make(chan error, 1)
	server := &dns.Server{
		PacketConn: connection,
		Handler: dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
			response := new(dns.Msg)
			response.SetReply(request)
			response.Authoritative = true
			for _, question := range request.Question {
				if question.Qtype != dns.TypeA || question.Name != "harbor-ci-resolver.test." {
					continue
				}
				response.Answer = append(response.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   question.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    1,
					},
					A: net.ParseIP("127.77.0.1").To4(),
				})
			}
			_ = writer.WriteMsg(response)
		}),
		NotifyStartedFunc: func() {
			close(started)
		},
	}
	go func() {
		serveResult <- server.ActivateAndServe()
	}()
	select {
	case <-started:
	case serveErr := <-serveResult:
		t.Fatalf("start DNS server error = %v", serveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("start DNS server timed out")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := server.ShutdownContext(ctx); shutdownErr != nil {
			t.Errorf("shutdown DNS server error = %v", shutdownErr)
		}
		select {
		case serveErr := <-serveResult:
			if serveErr != nil {
				t.Errorf("serve DNS shutdown error = %v", serveErr)
			}
		case <-ctx.Done():
			t.Errorf("wait for DNS server shutdown: %v", ctx.Err())
		}
	})
}
