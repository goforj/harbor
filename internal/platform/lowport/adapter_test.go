package lowport

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// TestAdapterRepairsOnlyAnExactPlistWhoseServiceIsAbsent keeps the launchd
// repair path narrower than generic owned-state replacement.
func TestAdapterRepairsOnlyAnExactPlistWhoseServiceIsAbsent(t *testing.T) {
	request := testRequest(t)
	repair := observationFor(request, exactArtifact(ArtifactKindPlist), absentArtifact(ArtifactKindService))
	exact := observationFor(request, exactArtifact(ArtifactKindPlist), exactArtifact(ArtifactKindService))
	backend := &scriptedBackend{observations: []Observation{repair, exact}}
	adapter := newAdapter(backend)
	expected, err := repair.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	change, err := adapter.EnsureIfObserved(context.Background(), request, expected)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if !change.Attempted || !change.Changed || backend.ensureCalls != 1 {
		t.Fatalf("EnsureIfObserved() change/calls = %#v/%d", change, backend.ensureCalls)
	}
}

// TestAdapterRefusesReleaseOfOwnedDrift prevents bootout from becoming a
// cleanup mechanism for a service whose exact current ownership is not proven.
func TestAdapterRefusesReleaseOfOwnedDrift(t *testing.T) {
	request := testRequest(t)
	drifted := observationFor(request, exactArtifact(ArtifactKindPlist), Artifact{Kind: ArtifactKindService, Present: true, Owned: true, Fingerprint: strings.Repeat("b", canonicalFingerprintBytes)})
	backend := &scriptedBackend{observations: []Observation{drifted}}
	adapter := newAdapter(backend)
	expected, err := drifted.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ReleaseIfObserved(context.Background(), request, expected)
	if typed, ok := err.(*Error); !ok || typed.Kind != ErrorKindConflict || backend.releaseCalls != 0 {
		t.Fatalf("ReleaseIfObserved() error/calls = %v/%d", err, backend.releaseCalls)
	}
}

// TestObservationFingerprintIncludesBothUpstreams prevents HTTP and HTTPS
// policy swaps from reusing a stale compare-and-swap token.
func TestObservationFingerprintIncludesBothUpstreams(t *testing.T) {
	request := testRequest(t)
	observation := observationFor(request, absentArtifact(ArtifactKindPlist), absentArtifact(ArtifactKindService))
	baseline, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	changed := observation
	changed.Request.httpsUpstream = netip.MustParseAddrPort("127.0.0.1:25003")
	updated, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if baseline == updated {
		t.Fatal("Fingerprint() did not bind the HTTPS upstream")
	}
}

// scriptedBackend supplies fresh snapshots so adapter tests exercise its CAS and post-observe boundary.
type scriptedBackend struct {
	mu           sync.Mutex
	observations []Observation
	ensureCalls  int
	releaseCalls int
}

// observe returns the next scripted native state, retaining the final state for repeated reads.
func (b *scriptedBackend) observe(_ context.Context, _ Request) (Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	observation := b.observations[0]
	if len(b.observations) > 1 {
		b.observations = b.observations[1:]
	}
	return observation, nil
}

// ensure records the singular approved repair.
func (b *scriptedBackend) ensure(context.Context, Request, Observation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureCalls++
	return nil
}

// release records release attempts for fail-closed assertions.
func (b *scriptedBackend) release(context.Context, Request, Observation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseCalls++
	return nil
}

// testRequest derives one complete immutable request fixture.
func testRequest(t *testing.T) Request {
	t.Helper()
	policy := testPolicy(t)
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(testOwnership(fingerprint), policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// observationFor forms a complete typed low-port snapshot.
func observationFor(request Request, artifacts ...Artifact) Observation {
	return Observation{Request: request, Complete: true, Artifacts: artifacts}
}

// exactArtifact returns one exact canonical native fact.
func exactArtifact(kind ArtifactKind) Artifact {
	return Artifact{Kind: kind, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("a", canonicalFingerprintBytes)}
}

// absentArtifact returns one canonical absent native fact.
func absentArtifact(kind ArtifactKind) Artifact {
	return Artifact{Kind: kind, Fingerprint: strings.Repeat("c", canonicalFingerprintBytes)}
}
