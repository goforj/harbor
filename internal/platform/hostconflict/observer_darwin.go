//go:build darwin

package hostconflict

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	darwinObservationRetries    = 7
	darwinObservationRetryDelay = 10 * time.Millisecond
)

var (
	// errDarwinPCBSnapshotChanged identifies a kernel generation race that may be retried from a fresh table.
	errDarwinPCBSnapshotChanged = errors.New("Darwin PCB snapshot changed during observation")
	// errDarwinRouteSnapshotChanged identifies route lookup and dump facts that did not describe one state.
	errDarwinRouteSnapshotChanged = errors.New("Darwin route snapshot changed during observation")
)

// darwinObservationPass supplies one complete native pass for stability-policy tests.
type darwinObservationPass func(context.Context, Request) (Observation, error)

// darwinPassOperations isolates orchestration from the native RIB and PCB codecs.
type darwinPassOperations struct {
	interfaces func(context.Context) (darwinInterfaceSnapshot, error)
	routes     func(context.Context, Request, darwinInterfaceSnapshot) (RouteSnapshot, error)
	sockets    func(context.Context, Request) (SocketSnapshot, error)
}

// ObserveDarwin returns two consecutive matching observations from macOS's process-global network stack.
func ObserveDarwin(ctx context.Context, request Request) (Observation, error) {
	if err := request.Validate(); err != nil {
		return Observation{}, fmt.Errorf("observe Darwin host conflicts: %w", err)
	}
	ctx = normalizeDarwinObservationContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, fmt.Errorf("observe Darwin host conflicts: %w", err)
	}
	return observeStableDarwin(ctx, request, observeDarwinPass)
}

// observeStableDarwin requires consecutive equality and restarts after bounded native generation races.
func observeStableDarwin(ctx context.Context, request Request, observe darwinObservationPass) (Observation, error) {
	previousFingerprint := ""
	transientRaces := 0
	for attempt := 0; attempt <= darwinObservationRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Observation{}, fmt.Errorf("observe Darwin host conflicts: %w", err)
		}
		observation, err := observe(ctx, request)
		if err != nil {
			if errors.Is(err, errDarwinPCBSnapshotChanged) || errors.Is(err, errDarwinRouteSnapshotChanged) {
				transientRaces++
				previousFingerprint = ""
				if attempt < darwinObservationRetries {
					if err := waitDarwinObservationRetry(ctx); err != nil {
						return Observation{}, fmt.Errorf("observe Darwin host conflicts: %w", err)
					}
				}
				continue
			}
			return Observation{}, fmt.Errorf("observe Darwin host conflicts: %w", err)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return Observation{}, fmt.Errorf("observe Darwin host conflicts: invalid native facts: %w", err)
		}
		if previousFingerprint != "" && fingerprint == previousFingerprint {
			return observation, nil
		}
		previousFingerprint = fingerprint
	}
	if transientRaces > 0 {
		return Observation{}, fmt.Errorf("observe Darwin host conflicts: native tables could not complete after %d transient races", transientRaces)
	}
	return Observation{}, fmt.Errorf("observe Darwin host conflicts: kernel facts did not stabilize after %d passes", darwinObservationRetries+1)
}

// waitDarwinObservationRetry lets a changing kernel generation settle without extending cancellation semantics.
func waitDarwinObservationRetry(ctx context.Context) error {
	timer := time.NewTimer(darwinObservationRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// observeDarwinPass gathers interfaces before route and socket facts so every native identity can be cross-checked.
func observeDarwinPass(ctx context.Context, request Request) (Observation, error) {
	operations := darwinPassOperations{
		interfaces: observeDarwinInterfaces,
		routes:     observeDarwinRoutes,
		sockets:    observeDarwinSockets,
	}
	return observeDarwinPassWith(ctx, request, operations)
}

// observeDarwinPassWith rejects partial composition before returning a process-global observation.
func observeDarwinPassWith(ctx context.Context, request Request, operations darwinPassOperations) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	interfaces, err := operations.interfaces(ctx)
	if err != nil {
		return Observation{}, err
	}
	routes, err := operations.routes(ctx, request, interfaces)
	if err != nil {
		return Observation{}, err
	}
	sockets := SocketSnapshot{Complete: true}
	if len(request.Requirements()) > 0 {
		sockets, err = operations.sockets(ctx, request)
		if err != nil {
			return Observation{}, err
		}
	}
	observation := Observation{
		Request:  request,
		Scope:    NewMacOSScope(),
		Loopback: interfaces.loopback,
		Routes:   routes,
		Sockets:  sockets,
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, fmt.Errorf("host conflict Darwin native observation: %w", err)
	}
	return observation, nil
}

// normalizeDarwinObservationContext keeps nil callers on the same cancellable path as explicit contexts.
func normalizeDarwinObservationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
