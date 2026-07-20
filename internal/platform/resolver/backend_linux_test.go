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
)

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
	before, err := adapter.Observe(t.Context(), request)
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
