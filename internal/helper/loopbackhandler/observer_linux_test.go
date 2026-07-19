//go:build linux

package loopbackhandler

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// TestObserveLinuxPreAssignmentWithBindsCanonicalRequesterUID proves Linux observations use the signed interactive identity exactly.
func TestObserveLinuxPreAssignmentWithBindsCanonicalRequesterUID(t *testing.T) {
	request := linuxObserverRequest(t)
	cause := errors.New("sentinel observer result")
	wantObservation := hostconflict.Observation{Scope: hostconflict.NewMacOSScope()}
	wantContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	observe := func(ctx context.Context, gotRequest hostconflict.Request, requesterUID uint32) (hostconflict.Observation, error) {
		calls++
		if ctx != wantContext {
			t.Fatal("native observer received a different context")
		}
		if gotRequest.Purpose() != request.Purpose() || gotRequest.Candidate() != request.Candidate() || !slices.Equal(gotRequest.Requirements(), request.Requirements()) {
			t.Fatalf("native observer request = %q %s %#v", gotRequest.Purpose(), gotRequest.Candidate(), gotRequest.Requirements())
		}
		if requesterUID != 1000 {
			t.Fatalf("native observer requester UID = %d, want 1000", requesterUID)
		}
		return wantObservation, cause
	}

	observation, err := observeLinuxPreAssignmentWith(wantContext, request, "1000", observe)
	if !errors.Is(err, cause) || observation.Scope.Platform != wantObservation.Scope.Platform || calls != 1 {
		t.Fatalf("observeLinuxPreAssignmentWith() = %#v, %v after %d calls", observation, err, calls)
	}
}

// TestObserveLinuxPreAssignmentWithRejectsNoncanonicalRequesterUIDs proves malformed or elevated identities never reach native observation.
func TestObserveLinuxPreAssignmentWithRejectsNoncanonicalRequesterUIDs(t *testing.T) {
	request := linuxObserverRequest(t)
	identities := []string{"", "0", "00", "+1", "-1", " 1", "1 ", "4294967296", "uid-1000"}
	for _, identity := range identities {
		t.Run(identity, func(t *testing.T) {
			calls := 0
			observe := func(context.Context, hostconflict.Request, uint32) (hostconflict.Observation, error) {
				calls++
				return hostconflict.Observation{}, nil
			}
			if observation, err := observeLinuxPreAssignmentWith(context.Background(), request, identity, observe); err == nil {
				t.Fatalf("observeLinuxPreAssignmentWith() = %#v, want error", observation)
			}
			if calls != 0 {
				t.Fatalf("native observer calls = %d, want 0", calls)
			}
		})
	}
}

// linuxObserverRequest returns one canonical request for adapter-only tests.
func linuxObserverRequest(t *testing.T) hostconflict.Request {
	t.Helper()
	request, err := hostconflict.NewPreAssignmentRequest(netip.MustParseAddr("127.77.0.10"), []hostconflict.SocketRequirement{
		{Transport: hostconflict.TransportTCP4, Port: 443},
	})
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	return request
}
