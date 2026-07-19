//go:build windows

package loopbackhandler

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// TestObserveWindowsPreAssignmentWithDelegatesExactRequest proves the Windows adapter adds no ambient authority or translation.
func TestObserveWindowsPreAssignmentWithDelegatesExactRequest(t *testing.T) {
	request, err := hostconflict.NewPreAssignmentRequest(netip.MustParseAddr("127.77.0.10"), nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	scope, err := hostconflict.NewWindowsScope(1)
	if err != nil {
		t.Fatalf("NewWindowsScope() error = %v", err)
	}
	cause := errors.New("sentinel observer result")
	wantObservation := hostconflict.Observation{Scope: scope}
	wantContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	observe := func(ctx context.Context, gotRequest hostconflict.Request) (hostconflict.Observation, error) {
		calls++
		if ctx != wantContext || gotRequest.Purpose() != request.Purpose() || gotRequest.Candidate() != request.Candidate() || len(gotRequest.Requirements()) != 0 {
			t.Fatalf("native observer received unexpected context or request")
		}
		return wantObservation, cause
	}

	observation, err := observeWindowsPreAssignmentWith(wantContext, request, observe)
	if !errors.Is(err, cause) || observation.Scope.Platform != wantObservation.Scope.Platform || calls != 1 {
		t.Fatalf("observeWindowsPreAssignmentWith() = %#v, %v after %d calls", observation, err, calls)
	}
}
