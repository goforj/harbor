package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// fakeSystemdResolvedStore retains one complete Linux resolver snapshot for portable CAS tests.
type fakeSystemdResolvedStore struct {
	snapshotState          systemdResolvedSnapshot
	recoverErr             error
	snapshotErr            error
	replaceErr             error
	removeErr              error
	afterReplaceValidation func()
	afterRemoveValidation  func()
	replaceCalls           int
	removeCalls            int
	nextInode              uint64
	recoverCalls           int
	snapshotCalls          int
}

// recover exposes native transaction recovery as an injectable portable boundary.
func (store *fakeSystemdResolvedStore) recover(context.Context, Request) error {
	store.recoverCalls++
	return store.recoverErr
}

// snapshot returns independent artifact and runtime storage so observations cannot mutate the fake host.
func (store *fakeSystemdResolvedStore) snapshot(context.Context, Request) (systemdResolvedSnapshot, error) {
	store.snapshotCalls++
	if store.snapshotErr != nil {
		return systemdResolvedSnapshot{}, store.snapshotErr
	}
	return cloneSystemdResolvedTestSnapshot(store.snapshotState), nil
}

// TestSystemdResolvedObserveRecoversBeforeSnapshot keeps native crash repair ahead of public-state admission.
func TestSystemdResolvedObserveRecoversBeforeSnapshot(t *testing.T) {
	recoveryErr := errors.New("recover interrupted publication")
	store := &fakeSystemdResolvedStore{recoverErr: recoveryErr}
	backend := newSystemdResolvedBackend(store)
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)

	_, err := backend.observe(t.Context(), request)
	if !errors.Is(err, recoveryErr) {
		t.Fatalf("observe() error = %v, want %v", err, recoveryErr)
	}
	if store.recoverCalls != 1 || store.snapshotCalls != 0 {
		t.Fatalf("observe() calls = recover %d, snapshot %d, want recover 1, snapshot 0", store.recoverCalls, store.snapshotCalls)
	}
}

// replace enforces the complete observation twice around an injected race before installing canonical state.
func (store *fakeSystemdResolvedStore) replace(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard systemdResolvedGuard,
	content []byte,
) error {
	store.replaceCalls++
	if store.replaceErr != nil {
		return store.replaceErr
	}
	if err := matchSystemdResolvedMutationState(request, store.snapshotState, expectedFingerprint, guard); err != nil {
		return err
	}
	if store.afterReplaceValidation != nil {
		store.afterReplaceValidation()
	}
	if err := matchSystemdResolvedMutationState(request, store.snapshotState, expectedFingerprint, guard); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.nextInode++
	if store.nextInode < 1000 {
		store.nextInode = 1000
	}
	store.snapshotState = systemdResolvedSnapshot{
		Artifact: secureSystemdResolvedTestArtifact(content, store.nextInode),
		Runtime:  []systemdResolvedRuntimeRule{exactSystemdResolvedTestRuntime(request)},
	}
	return nil
}

// remove revalidates exact authority and preserves every runtime route not explained by the fixed artifact.
func (store *fakeSystemdResolvedStore) remove(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard systemdResolvedGuard,
) error {
	store.removeCalls++
	if store.removeErr != nil {
		return store.removeErr
	}
	if err := matchSystemdResolvedMutationState(request, store.snapshotState, expectedFingerprint, guard); err != nil {
		return err
	}
	if store.afterRemoveValidation != nil {
		store.afterRemoveValidation()
	}
	if err := matchSystemdResolvedMutationState(request, store.snapshotState, expectedFingerprint, guard); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := parseSystemdResolvedArtifact(store.snapshotState.Artifact.Content)
	if err != nil {
		return err
	}
	remaining := make([]systemdResolvedRuntimeRule, 0, len(store.snapshotState.Runtime))
	for _, rule := range store.snapshotState.Runtime {
		if !systemdResolvedTestRuntimeExplainedByArtifact(rule, parsed, request.Suffix()) {
			remaining = append(remaining, rule)
		}
	}
	store.snapshotState.Artifact = systemdResolvedArtifact{}
	store.snapshotState.Runtime = remaining
	return nil
}

// TestSystemdResolvedCodecRoundTripsCanonicalOwnership pins the fixed Ubuntu drop-in and parsed authority.
func TestSystemdResolvedCodecRoundTripsCanonicalOwnership(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	content, err := marshalSystemdResolved(request)
	if err != nil {
		t.Fatalf("marshalSystemdResolved() error = %v", err)
	}
	want := fmt.Sprintf(
		"# GoForj Harbor managed systemd-resolved route.\n"+
			"# harbor-resolver-owner version=1 installation=installation-test policy=%s\n"+
			"[Resolve]\n"+
			"DNS=127.0.0.1:25000\n"+
			"Domains=~test\n",
		request.PolicyFingerprint(),
	)
	if string(content) != want {
		t.Fatalf("marshalSystemdResolved() = %q, want %q", content, want)
	}
	parsed, err := parseSystemdResolvedArtifact(content)
	if err != nil {
		t.Fatalf("parseSystemdResolvedArtifact() error = %v", err)
	}
	if parsed.Owner == nil || *parsed.Owner != request.OwnerMarker() ||
		!slices.Equal(parsed.Domains, []systemdResolvedArtifactDomain{{Namespace: ".test", RouteOnly: true}}) ||
		!slices.Equal(parsed.Servers, []systemdResolvedArtifactServer{{Endpoint: request.Endpoint()}}) {
		t.Fatalf("parseSystemdResolvedArtifact() = %#v", parsed)
	}
}

// TestSystemdResolvedObservationClassifiesNativeSafety proves ownership needs a secure global artifact and exact live route.
func TestSystemdResolvedObservationClassifiesNativeSafety(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	content := marshalSystemdResolvedValidated(request)
	exactRuntime := exactSystemdResolvedTestRuntime(request)
	foreignRuntime := cloneSystemdResolvedTestRuntime(exactRuntime)
	foreignRuntime.Namespace = ".child.test"
	wrongInterface := cloneSystemdResolvedTestRuntime(exactRuntime)
	wrongInterface.InterfaceIndex = 2
	wrongInterface.Servers[0].InterfaceIndex = 2
	insecureArtifact := secureSystemdResolvedTestArtifact(content, 12)
	insecureArtifact.Metadata.Mode = 0o666
	tests := []struct {
		name        string
		snapshot    systemdResolvedSnapshot
		wantState   State
		wantOwned   OwnedState
		wantForeign int
	}{
		{name: "absent", wantState: StateAbsent, wantOwned: OwnedStateAbsent},
		{
			name:      "exact global route",
			snapshot:  systemdResolvedSnapshot{Artifact: secureSystemdResolvedTestArtifact(content, 10), Runtime: []systemdResolvedRuntimeRule{exactRuntime}},
			wantState: StateExact,
			wantOwned: OwnedStateExact,
		},
		{
			name:      "owned artifact awaiting reload",
			snapshot:  systemdResolvedSnapshot{Artifact: secureSystemdResolvedTestArtifact(content, 11)},
			wantState: StateOwnedDrifted,
			wantOwned: OwnedStateDrifted,
		},
		{
			name:        "unsafe marker artifact",
			snapshot:    systemdResolvedSnapshot{Artifact: insecureArtifact, Runtime: []systemdResolvedRuntimeRule{exactRuntime}},
			wantState:   StateForeign,
			wantOwned:   OwnedStateAbsent,
			wantForeign: 2,
		},
		{
			name:        "foreign route",
			snapshot:    systemdResolvedSnapshot{Runtime: []systemdResolvedRuntimeRule{foreignRuntime}},
			wantState:   StateForeign,
			wantOwned:   OwnedStateAbsent,
			wantForeign: 1,
		},
		{
			name:        "per-link route cannot impersonate global drop-in",
			snapshot:    systemdResolvedSnapshot{Artifact: secureSystemdResolvedTestArtifact(content, 13), Runtime: []systemdResolvedRuntimeRule{wrongInterface}},
			wantState:   StateForeign,
			wantOwned:   OwnedStateDrifted,
			wantForeign: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, err := systemdResolvedObservationFromSnapshot(request, test.snapshot)
			if err != nil {
				t.Fatalf("systemdResolvedObservationFromSnapshot() error = %v", err)
			}
			assessment, err := observation.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if assessment.State != test.wantState || assessment.Owned != test.wantOwned || assessment.ForeignCount != test.wantForeign {
				t.Fatalf("Classify() = %#v, want state %q, owned %q, foreign %d", assessment, test.wantState, test.wantOwned, test.wantForeign)
			}
		})
	}
}

// TestSystemdResolvedEnsureCreatesAndRepairsOwnedState covers both admitted Linux mutation shapes end to end.
func TestSystemdResolvedEnsureCreatesAndRepairsOwnedState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	driftedContent := []byte(fmt.Sprintf(
		"%sversion=1 installation=%s policy=%s\n[Resolve]\nDNS=127.0.0.2:53\nDomains=~test\n",
		systemdResolvedOwnerPrefix,
		request.InstallationID(),
		request.PolicyFingerprint(),
	))
	driftedRuntime := systemdResolvedRuntimeRule{
		Namespace: ".test",
		RouteOnly: true,
		Servers: []systemdResolvedRuntimeServer{{
			Endpoint: netip.MustParseAddrPort("127.0.0.2:53"),
		}},
	}
	tests := []struct {
		name     string
		snapshot systemdResolvedSnapshot
	}{
		{name: "absent"},
		{
			name: "owned drift",
			snapshot: systemdResolvedSnapshot{
				Artifact: secureSystemdResolvedTestArtifact(driftedContent, 20),
				Runtime:  []systemdResolvedRuntimeRule{driftedRuntime},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeSystemdResolvedStore{snapshotState: test.snapshot}
			adapter := newAdapter(newSystemdResolvedBackend(store))
			before, err := adapter.Observe(t.Context(), request)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			change, err := adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, before))
			if err != nil {
				t.Fatalf("EnsureIfObserved() error = %v", err)
			}
			assessment, err := change.After.Classify()
			if err != nil {
				t.Fatalf("Classify() error = %v", err)
			}
			if !change.Attempted || !change.Changed || assessment.State != StateExact || store.replaceCalls != 1 {
				t.Fatalf("EnsureIfObserved() change = %#v, assessment = %#v, calls = %d", change, assessment, store.replaceCalls)
			}
		})
	}
}

// TestSystemdResolvedEnsureRejectsForeignAndRacedState proves no stale or foreign evidence can authorize replacement.
func TestSystemdResolvedEnsureRejectsForeignAndRacedState(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	foreignStore := &fakeSystemdResolvedStore{snapshotState: systemdResolvedSnapshot{
		Runtime: []systemdResolvedRuntimeRule{exactSystemdResolvedTestRuntime(request)},
	}}
	foreignAdapter := newAdapter(newSystemdResolvedBackend(foreignStore))
	foreignBefore, err := foreignAdapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() foreign error = %v", err)
	}
	_, err = foreignAdapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, foreignBefore))
	resolverAssertErrorKind(t, err, ErrorKindConflict)
	if foreignStore.replaceCalls != 0 {
		t.Fatalf("foreign replace calls = %d, want 0", foreignStore.replaceCalls)
	}

	content := marshalSystemdResolvedValidated(request)
	racedStore := &fakeSystemdResolvedStore{snapshotState: systemdResolvedSnapshot{
		Artifact: secureSystemdResolvedTestArtifact(content, 30),
	}}
	racedStore.afterReplaceValidation = func() {
		racedStore.snapshotState.Artifact.Metadata.ChangedTimeNS++
	}
	racedAdapter := newAdapter(newSystemdResolvedBackend(racedStore))
	racedBefore, err := racedAdapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() raced error = %v", err)
	}
	_, err = racedAdapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, racedBefore))
	resolverAssertErrorKind(t, err, ErrorKindMutationFailed)
	if racedStore.replaceCalls != 1 || !racedStore.snapshotState.Artifact.Exists {
		t.Fatalf("raced replace calls = %d, artifact = %#v", racedStore.replaceCalls, racedStore.snapshotState.Artifact)
	}
}

// TestSystemdResolvedReleasePreservesForeignRoutes proves fixed-artifact cleanup cannot erase another route.
func TestSystemdResolvedReleasePreservesForeignRoutes(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	exact := exactSystemdResolvedTestRuntime(request)
	foreign := exact
	foreign.InterfaceIndex = 4
	foreign.Namespace = ".child.test"
	foreign.Servers = []systemdResolvedRuntimeServer{{
		InterfaceIndex: 4,
		Endpoint:       netip.MustParseAddrPort("192.0.2.53:53"),
	}}
	store := &fakeSystemdResolvedStore{snapshotState: systemdResolvedSnapshot{
		Artifact: secureSystemdResolvedTestArtifact(marshalSystemdResolvedValidated(request), 40),
		Runtime:  []systemdResolvedRuntimeRule{foreign, exact},
	}}
	slices.SortFunc(store.snapshotState.Runtime, compareSystemdResolvedRuntimeRule)
	adapter := newAdapter(newSystemdResolvedBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	beforeAssessment, err := before.Classify()
	if err != nil || beforeAssessment.State != StateForeign || beforeAssessment.Owned != OwnedStateDrifted {
		t.Fatalf("Classify() before = %#v, %v", beforeAssessment, err)
	}
	change, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, before))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	afterAssessment, err := change.After.Classify()
	if err != nil {
		t.Fatalf("Classify() after error = %v", err)
	}
	if store.removeCalls != 1 || afterAssessment.Owned != OwnedStateAbsent || afterAssessment.ForeignCount != 1 ||
		len(store.snapshotState.Runtime) != 1 || store.snapshotState.Runtime[0].Namespace != foreign.Namespace {
		t.Fatalf("ReleaseIfObserved() change = %#v, assessment = %#v, runtime = %#v", change, afterAssessment, store.snapshotState.Runtime)
	}
}

// TestSystemdResolvedParserRejectsMalformedOrUnboundedArtifacts covers ownership and routing ambiguities.
func TestSystemdResolvedParserRejectsMalformedOrUnboundedArtifacts(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	marker := fmt.Sprintf(
		"%sversion=1 installation=%s policy=%s",
		systemdResolvedOwnerPrefix,
		request.InstallationID(),
		request.PolicyFingerprint(),
	)
	tests := []struct {
		name    string
		content []byte
	}{
		{name: "empty"},
		{name: "oversized", content: bytes.Repeat([]byte{'x'}, maximumSystemdResolvedFileBytes+1)},
		{name: "invalid UTF-8", content: []byte{0xff}},
		{name: "carriage return", content: []byte("[Resolve]\r\n")},
		{name: "duplicate marker", content: []byte(marker + "\n" + marker + "\n")},
		{name: "reserved marker typo", content: []byte("# harbor-resolver-owner-bad\n")},
		{name: "scoped server", content: []byte("[Resolve]\nDNS=127.0.0.1%eth0:53\n")},
		{name: "zero port", content: []byte("[Resolve]\nDNS=127.0.0.1:0\n")},
		{name: "duplicate server", content: []byte("[Resolve]\nDNS=127.0.0.1 127.0.0.1\n")},
		{name: "duplicate domain", content: []byte("[Resolve]\nDomains=~test ~test\n")},
		{name: "long line", content: []byte(strings.Repeat("x", maximumSystemdResolvedLineBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseSystemdResolvedArtifact(test.content); err == nil {
				t.Fatalf("parseSystemdResolvedArtifact(%q) succeeded", test.content)
			}
		})
	}
}

// TestSystemdResolvedFingerprintsCoverRawRuntimeAttributes proves hidden native drift changes CAS evidence.
func TestSystemdResolvedFingerprintsCoverRawRuntimeAttributes(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)
	artifact := secureSystemdResolvedTestArtifact(marshalSystemdResolvedValidated(request), 50)
	base := exactSystemdResolvedTestRuntime(request)
	mutations := []systemdResolvedRuntimeRule{
		cloneSystemdResolvedTestRuntime(base),
		cloneSystemdResolvedTestRuntime(base),
		cloneSystemdResolvedTestRuntime(base),
		cloneSystemdResolvedTestRuntime(base),
	}
	mutations[0].InterfaceIndex = 1
	mutations[0].Servers[0].InterfaceIndex = 1
	mutations[1].RouteOnly = false
	mutations[2].Servers[0].Endpoint = netip.MustParseAddrPort("127.0.0.2:25000")
	mutations[3].Servers[0].ServerName = "dns.test"
	wantDistinct := map[string]struct{}{
		systemdResolvedArtifactFingerprint(artifact, &base): {},
	}
	for _, mutation := range mutations {
		wantDistinct[systemdResolvedArtifactFingerprint(artifact, &mutation)] = struct{}{}
	}
	if len(wantDistinct) != len(mutations)+1 {
		t.Fatalf("runtime mutations produced %d distinct fingerprints, want %d", len(wantDistinct), len(mutations)+1)
	}
	changedMetadata := artifact
	changedMetadata.Metadata.ChangedTimeNS++
	if systemdResolvedArtifactFingerprint(artifact, nil) == systemdResolvedArtifactFingerprint(changedMetadata, nil) {
		t.Fatal("artifact metadata mutation did not change fingerprint")
	}
}

// secureSystemdResolvedTestArtifact returns one fixed-file identity accepted as Harbor mutation authority.
func secureSystemdResolvedTestArtifact(content []byte, inode uint64) systemdResolvedArtifact {
	return systemdResolvedArtifact{
		Exists:  true,
		Content: slices.Clone(content),
		Metadata: systemdResolvedArtifactMetadata{
			Regular:        true,
			Device:         1,
			Inode:          inode,
			UID:            0,
			GID:            0,
			Mode:           systemdResolvedFileMode,
			LinkCount:      1,
			Size:           int64(len(content)),
			ModifiedTimeNS: 1,
			ChangedTimeNS:  1,
		},
	}
}

// exactSystemdResolvedTestRuntime returns the global resolve1 route emitted by the fixed canonical artifact.
func exactSystemdResolvedTestRuntime(request Request) systemdResolvedRuntimeRule {
	return systemdResolvedRuntimeRule{
		InterfaceIndex: 0,
		Namespace:      request.Suffix(),
		RouteOnly:      true,
		Servers: []systemdResolvedRuntimeServer{{
			InterfaceIndex: 0,
			Endpoint:       request.Endpoint(),
		}},
	}
}

// cloneSystemdResolvedTestSnapshot copies every byte and nested runtime server slice.
func cloneSystemdResolvedTestSnapshot(snapshot systemdResolvedSnapshot) systemdResolvedSnapshot {
	cloned := snapshot
	cloned.Artifact.Content = slices.Clone(snapshot.Artifact.Content)
	cloned.Runtime = make([]systemdResolvedRuntimeRule, len(snapshot.Runtime))
	for index, rule := range snapshot.Runtime {
		cloned.Runtime[index] = rule
		cloned.Runtime[index].Servers = slices.Clone(rule.Servers)
	}
	return cloned
}

// cloneSystemdResolvedTestRuntime copies the nested DNS server slice used by adversarial mutations.
func cloneSystemdResolvedTestRuntime(rule systemdResolvedRuntimeRule) systemdResolvedRuntimeRule {
	cloned := rule
	cloned.Servers = slices.Clone(rule.Servers)
	return cloned
}

// systemdResolvedTestRuntimeExplainedByArtifact identifies only one global route represented by the fixed file.
func systemdResolvedTestRuntimeExplainedByArtifact(
	runtimeRule systemdResolvedRuntimeRule,
	artifact parsedSystemdResolvedArtifact,
	suffix string,
) bool {
	if runtimeRule.InterfaceIndex != 0 {
		return false
	}
	domains := relevantSystemdResolvedArtifactDomains(artifact.Domains, suffix)
	if len(domains) != 1 || runtimeRule.Namespace != domains[0].Namespace || runtimeRule.RouteOnly != domains[0].RouteOnly {
		return false
	}
	return slices.Equal(runtimeRule.Servers, systemdResolvedArtifactRuntimeServers(artifact.Servers, 0))
}

var _ systemdResolvedStore = (*fakeSystemdResolvedStore)(nil)
