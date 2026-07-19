//go:build linux

package ticketissuer

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// TestObserveLinuxConflictsWithBindsCanonicalRequester proves policy lookup receives the authenticated interactive UID.
func TestObserveLinuxConflictsWithBindsCanonicalRequester(t *testing.T) {
	request := linuxConflictRequest(t)
	wantContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	wantObservation := hostconflict.Observation{Scope: hostconflict.NewMacOSScope()}
	wantError := errors.New("native observer sentinel")
	calls := 0
	observe := func(ctx context.Context, gotRequest hostconflict.Request, requesterUID uint32) (hostconflict.Observation, error) {
		calls++
		if ctx != wantContext || requesterUID != 1000 || gotRequest.Candidate() != request.Candidate() || !slices.Equal(gotRequest.Requirements(), request.Requirements()) {
			t.Fatalf("native observer received context %v, UID %d, candidate %s, requirements %#v", ctx == wantContext, requesterUID, gotRequest.Candidate(), gotRequest.Requirements())
		}
		return wantObservation, wantError
	}

	observation, err := observeLinuxConflictsWith(wantContext, request, "1000", observe)
	if !errors.Is(err, wantError) || observation.Scope != wantObservation.Scope || calls != 1 {
		t.Fatalf("observeLinuxConflictsWith() = %#v, %v after %d calls", observation, err, calls)
	}
}

// TestObserveLinuxConflictsWithRejectsUnsafeRequester proves malformed and root identities never reach native observation.
func TestObserveLinuxConflictsWithRejectsUnsafeRequester(t *testing.T) {
	request := linuxConflictRequest(t)
	for _, identity := range []string{"", "0", "00", "+1", "-1", " 1", "1 ", "4294967296", "uid-1000"} {
		t.Run(identity, func(t *testing.T) {
			calls := 0
			observe := func(context.Context, hostconflict.Request, uint32) (hostconflict.Observation, error) {
				calls++
				return hostconflict.Observation{}, nil
			}
			if observation, err := observeLinuxConflictsWith(context.Background(), request, identity, observe); err == nil {
				t.Fatalf("observeLinuxConflictsWith() = %#v, want error", observation)
			}
			if calls != 0 {
				t.Fatalf("native observer calls = %d, want 0", calls)
			}
		})
	}
}

// linuxConflictRequest returns one canonical request for the Linux boundary tests.
func linuxConflictRequest(t *testing.T) hostconflict.Request {
	t.Helper()
	request, err := hostconflict.NewPreAssignmentRequest(netip.MustParseAddr("127.77.0.10"), []hostconflict.SocketRequirement{
		{Transport: hostconflict.TransportTCP4, Port: 443},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	return request
}
