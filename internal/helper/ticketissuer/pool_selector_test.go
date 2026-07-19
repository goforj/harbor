package ticketissuer

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// selectorLoopbackObserver records scan order and delegates facts to one test callback.
type selectorLoopbackObserver struct {
	observe func(netip.Addr) (loopback.Observation, error)
	calls   []netip.Addr
}

// Observe records the exact candidate before returning its scripted native facts.
func (observer *selectorLoopbackObserver) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.calls = append(observer.calls, address)
	return observer.observe(address)
}

// selectorConflictCall captures the route-only request and authenticated requester.
type selectorConflictCall struct {
	request   hostconflict.Request
	requester string
}

// selectorConflictObserver records scan order and delegates facts to one test callback.
type selectorConflictObserver struct {
	observe func(hostconflict.Request, string) (hostconflict.Observation, error)
	calls   []selectorConflictCall
}

// Observe records the exact request before returning its scripted native facts.
func (observer *selectorConflictObserver) Observe(_ context.Context, request hostconflict.Request, requester string) (hostconflict.Observation, error) {
	observer.calls = append(observer.calls, selectorConflictCall{request: request, requester: requester})
	return observer.observe(request, requester)
}

// TestPoolSelectorSelectWrapsAndReturnsExactEvidence verifies deterministic wraparound and the complete selected proof.
func TestPoolSelectorSelectWrapsAndReturnsExactEvidence(t *testing.T) {
	prefixes := identity.DefaultPoolPrefixes()
	installationID := selectorWrapInstallationID(t, len(prefixes)-1, len(prefixes))
	lastAddresses := selectorPrefixAddresses(prefixes[len(prefixes)-1])
	firstAddresses := selectorPrefixAddresses(prefixes[0])
	loopbackObserver := &selectorLoopbackObserver{observe: func(address netip.Addr) (loopback.Observation, error) {
		state := loopback.StateAbsent
		if address == lastAddresses[0] {
			state = loopback.StateExact
		}
		return poolLoopbackObservation(address, state), nil
	}}
	conflictObserver := selectorSafeConflictObserver(t)
	selector := NewPoolSelector(loopbackObserver, conflictObserver)

	selection, err := selector.Select(t.Context(), installationID, "1000")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Pool.Prefix() != prefixes[0] || !reflect.DeepEqual(selection.Pool.Candidates(), firstAddresses) {
		t.Fatalf("selected pool = %s / %#v, want %s / %#v", selection.Pool.Prefix(), selection.Pool.Candidates(), prefixes[0], firstAddresses)
	}
	wantLoopbackCalls := append(append([]netip.Addr(nil), lastAddresses...), firstAddresses...)
	if !reflect.DeepEqual(loopbackObserver.calls, wantLoopbackCalls) {
		t.Fatalf("loopback calls = %#v, want %#v", loopbackObserver.calls, wantLoopbackCalls)
	}
	if len(conflictObserver.calls) != poolSelectorIdentityCount {
		t.Fatalf("conflict calls = %d, want %d", len(conflictObserver.calls), poolSelectorIdentityCount)
	}
	for index, evidence := range selection.Evidence {
		address := firstAddresses[index]
		assignmentFingerprint, err := poolLoopbackObservation(address, loopback.StateAbsent).Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		hostFingerprint, err := poolSafeHostObservation(t, address).Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		want := identity.PoolAddressObservation{
			Address:              address,
			AssignmentState:      identity.PoolAssignmentAbsent,
			AssignmentEvidence:   assignmentFingerprint,
			HostConflictState:    identity.PoolHostConflictSafe,
			HostConflictEvidence: hostFingerprint,
		}
		if evidence != want {
			t.Fatalf("evidence %d = %#v, want %#v", index, evidence, want)
		}
		call := conflictObserver.calls[index]
		if call.requester != "1000" || call.request.Candidate() != address || call.request.Purpose() != hostconflict.PurposePreAssignment || len(call.request.Requirements()) != 0 {
			t.Fatalf("conflict call %d = %#v", index, call)
		}
	}
}

// TestPoolSelectorSelectSkipsCandidateForConflictAndIndeterminateFacts verifies both fail-closed classes advance the scan.
func TestPoolSelectorSelectSkipsCandidateForConflictAndIndeterminateFacts(t *testing.T) {
	installationID := identity.InstallationID("selector-conflict-test")
	prefixes := identity.DefaultPoolPrefixes()
	offset, err := identity.DefaultPoolStartOffset(installationID)
	if err != nil {
		t.Fatal(err)
	}
	start := int(offset % uint64(len(prefixes)))
	blocked := selectorPrefixAddresses(prefixes[start])
	wantPrefix := prefixes[(start+1)%len(prefixes)]
	loopbackObserver := selectorAbsentLoopbackObserver()
	conflictObserver := &selectorConflictObserver{observe: func(request hostconflict.Request, _ string) (hostconflict.Observation, error) {
		address := request.Candidate()
		switch address {
		case blocked[0]:
			return selectorConflictingHostObservation(t, address), nil
		case blocked[1]:
			return selectorIndeterminateHostObservation(t, address), nil
		default:
			return poolSafeHostObservation(t, address), nil
		}
	}}

	selection, err := NewPoolSelector(loopbackObserver, conflictObserver).Select(t.Context(), installationID, "1000")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Pool.Prefix() != wantPrefix {
		t.Fatalf("selected prefix = %s, want %s", selection.Pool.Prefix(), wantPrefix)
	}
	if len(loopbackObserver.calls) != 2*poolSelectorIdentityCount || len(conflictObserver.calls) != 2*poolSelectorIdentityCount {
		t.Fatalf("loopback/conflict calls = %d/%d, want %d/%d", len(loopbackObserver.calls), len(conflictObserver.calls), 2*poolSelectorIdentityCount, 2*poolSelectorIdentityCount)
	}
}

// TestPoolSelectorSelectExhaustionCountsAssignmentBlocks verifies every candidate is visited once without conflict I/O.
func TestPoolSelectorSelectExhaustionCountsAssignmentBlocks(t *testing.T) {
	installationID := identity.InstallationID("selector-exhaustion-test")
	prefixes := identity.DefaultPoolPrefixes()
	offset, err := identity.DefaultPoolStartOffset(installationID)
	if err != nil {
		t.Fatal(err)
	}
	loopbackObserver := &selectorLoopbackObserver{observe: func(address netip.Addr) (loopback.Observation, error) {
		state := loopback.StateAbsent
		if address.As4()[3]%8 == 0 {
			state = loopback.StateExact
		}
		return poolLoopbackObservation(address, state), nil
	}}
	conflictObserver := selectorSafeConflictObserver(t)

	selection, err := NewPoolSelector(loopbackObserver, conflictObserver).Select(t.Context(), installationID, "1000")
	if err == nil || !reflect.DeepEqual(selection, identity.PoolSelection{}) {
		t.Fatalf("Select() = %#v, %v, want exhaustion", selection, err)
	}
	var exhaustion *identity.PoolSelectionExhaustionError
	if !errors.As(err, &exhaustion) {
		t.Fatalf("Select() error = %T %v, want PoolSelectionExhaustionError", err, err)
	}
	want := identity.PoolSelectionExhaustionError{
		CandidatePools:         len(prefixes),
		StartIndex:             int(offset % uint64(len(prefixes))),
		AssignmentBlockedPools: len(prefixes),
	}
	if *exhaustion != want {
		t.Fatalf("exhaustion = %#v, want %#v", *exhaustion, want)
	}
	if len(loopbackObserver.calls) != len(prefixes)*poolSelectorIdentityCount || len(conflictObserver.calls) != 0 {
		t.Fatalf("loopback/conflict calls = %d/%d, want %d/0", len(loopbackObserver.calls), len(conflictObserver.calls), len(prefixes)*poolSelectorIdentityCount)
	}
}

// TestPoolSelectorSelectRejectsObserverFailures covers exact echo, fingerprint, error, and cancellation boundaries.
func TestPoolSelectorSelectRejectsObserverFailures(t *testing.T) {
	sentinel := errors.New("selector sentinel")
	tests := []struct {
		name      string
		context   func() context.Context
		loopback  func(*testing.T) *selectorLoopbackObserver
		conflicts func(*testing.T) *selectorConflictObserver
		contains  string
		wantCause error
	}{
		{name: "cancelled", context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, contains: "", wantCause: context.Canceled},
		{name: "loopback error", loopback: func(*testing.T) *selectorLoopbackObserver {
			return &selectorLoopbackObserver{observe: func(netip.Addr) (loopback.Observation, error) { return loopback.Observation{}, sentinel }}
		}, contains: "observe assignment", wantCause: sentinel},
		{name: "wrong loopback address", loopback: func(*testing.T) *selectorLoopbackObserver {
			return &selectorLoopbackObserver{observe: func(address netip.Addr) (loopback.Observation, error) {
				return poolLoopbackObservation(address.Next(), loopback.StateAbsent), nil
			}}
		}, contains: "does not match"},
		{name: "loopback fingerprint", loopback: func(*testing.T) *selectorLoopbackObserver {
			return &selectorLoopbackObserver{observe: func(address netip.Addr) (loopback.Observation, error) {
				observation := poolLoopbackObservation(address, loopback.StateAbsent)
				observation.Loopback.Index = 0
				return observation, nil
			}}
		}, contains: "fingerprint assignment"},
		{name: "conflict error", conflicts: func(*testing.T) *selectorConflictObserver {
			return &selectorConflictObserver{observe: func(hostconflict.Request, string) (hostconflict.Observation, error) {
				return hostconflict.Observation{}, sentinel
			}}
		}, contains: "observe host conflicts", wantCause: sentinel},
		{name: "wrong conflict request", conflicts: func(t *testing.T) *selectorConflictObserver {
			return &selectorConflictObserver{observe: func(request hostconflict.Request, _ string) (hostconflict.Observation, error) {
				return poolSafeHostObservation(t, request.Candidate().Next()), nil
			}}
		}, contains: "does not match route-only request"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.context != nil {
				ctx = test.context()
			}
			loopbackObserver := selectorAbsentLoopbackObserver()
			if test.loopback != nil {
				loopbackObserver = test.loopback(t)
			}
			conflictObserver := selectorSafeConflictObserver(t)
			if test.conflicts != nil {
				conflictObserver = test.conflicts(t)
			}
			selection, err := NewPoolSelector(loopbackObserver, conflictObserver).Select(ctx, "selector-error-test", "1000")
			if err == nil || !reflect.DeepEqual(selection, identity.PoolSelection{}) {
				t.Fatalf("Select() = %#v, %v, want zero and error", selection, err)
			}
			if test.contains != "" && !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Select() error = %v, want substring %q", err, test.contains)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("Select() error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

// TestNewPoolSelectorRequiresObservers verifies invalid composition fails before native I/O.
func TestNewPoolSelectorRequiresObservers(t *testing.T) {
	validLoopback := selectorAbsentLoopbackObserver()
	validConflicts := selectorSafeConflictObserver(t)
	for index, construct := range []func(){
		func() { NewPoolSelector(nil, validConflicts) },
		func() { NewPoolSelector(validLoopback, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("constructor %d did not panic", index)
				}
			}()
			construct()
		}()
	}
	if selector := NewDefaultPoolSelector(); selector == nil || selector.loopback == nil || selector.conflicts == nil {
		t.Fatalf("NewDefaultPoolSelector() = %#v", selector)
	}
}

// selectorAbsentLoopbackObserver returns valid absent facts for every requested address.
func selectorAbsentLoopbackObserver() *selectorLoopbackObserver {
	return &selectorLoopbackObserver{observe: func(address netip.Addr) (loopback.Observation, error) {
		return poolLoopbackObservation(address, loopback.StateAbsent), nil
	}}
}

// selectorSafeConflictObserver returns valid safe route-only facts for every requested address.
func selectorSafeConflictObserver(t *testing.T) *selectorConflictObserver {
	t.Helper()
	return &selectorConflictObserver{observe: func(request hostconflict.Request, _ string) (hostconflict.Observation, error) {
		return poolSafeHostObservation(t, request.Candidate()), nil
	}}
}

// selectorConflictingHostObservation returns one valid observation with a candidate-specific route conflict.
func selectorConflictingHostObservation(t *testing.T, address netip.Addr) hostconflict.Observation {
	t.Helper()
	observation := poolSafeHostObservation(t, address)
	conflict := observation.Routes.Matching[0]
	conflict.Destination = netip.PrefixFrom(address, 32)
	observation.Routes.Matching = append(observation.Routes.Matching, conflict)
	return observation
}

// selectorIndeterminateHostObservation returns valid but incomplete route evidence.
func selectorIndeterminateHostObservation(t *testing.T, address netip.Addr) hostconflict.Observation {
	t.Helper()
	observation := poolSafeHostObservation(t, address)
	observation.Routes.Complete = false
	return observation
}

// selectorPrefixAddresses enumerates one /29 in canonical address order.
func selectorPrefixAddresses(prefix netip.Prefix) []netip.Addr {
	addresses := make([]netip.Addr, poolSelectorIdentityCount)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	return addresses
}

// selectorWrapInstallationID finds a stable valid ID whose hash begins at the requested canonical index.
func selectorWrapInstallationID(t *testing.T, want int, count int) identity.InstallationID {
	t.Helper()
	for index := range 100_000 {
		installationID := identity.InstallationID(fmt.Sprintf("selector-wrap-%05d", index))
		offset, err := identity.DefaultPoolStartOffset(installationID)
		if err != nil {
			t.Fatal(err)
		}
		if int(offset%uint64(count)) == want {
			return installationID
		}
	}
	t.Fatalf("no deterministic installation ID reached candidate %d", want)
	return ""
}

var _ LoopbackObserver = (*selectorLoopbackObserver)(nil)
var _ ConflictObserver = (*selectorConflictObserver)(nil)
