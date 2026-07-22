package lowport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const postMutationObservationTimeout = 5 * time.Second

// backend confines native mutation behind bounded observation and exact effects.
type backend interface {
	observe(context.Context, Request) (Observation, error)
	ensure(context.Context, Request, Observation) error
	release(context.Context, Request, Observation) error
}

// Adapter applies compare-and-swap and post-mutation observation around one native backend.
type Adapter struct {
	backend backend
	mutate  sync.Mutex
}

// newAdapter constructs an adapter around one reviewed native backend.
func newAdapter(backend backend) *Adapter { return &Adapter{backend: backend} }

// Observe returns bounded native service facts.
func (a *Adapter) Observe(ctx context.Context, request Request) (Observation, error) {
	if err := request.Validate(); err != nil {
		return Observation{}, operationError(ErrorKindInvalidRequest, "observe", err)
	}
	ctx = normalizedContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, "observe", err)
	}
	observation, err := a.backend.observe(ctx, request)
	if err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, "observe", err)
	}
	observation = cloneObservation(observation)
	if !sameRequest(observation.Request, request) {
		return Observation{}, operationError(ErrorKindInvalidFacts, "observe", fmt.Errorf("observation belongs to another request"))
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, operationError(ErrorKindInvalidFacts, "observe", err)
	}
	return observation, nil
}

// EnsureIfObserved ensures exact owned service state only when the observation fingerprint still matches.
func (a *Adapter) EnsureIfObserved(ctx context.Context, request Request, expected string) (Change, error) {
	return a.mutateIfObserved(ctx, "ensure", request, expected)
}

// ReleaseIfObserved removes only exact uniquely owned service state when the observation fingerprint still matches.
func (a *Adapter) ReleaseIfObserved(ctx context.Context, request Request, expected string) (Change, error) {
	return a.mutateIfObserved(ctx, "release", request, expected)
}

// mutateIfObserved serializes one in-process effect and proves its postcondition with fresh native facts.
func (a *Adapter) mutateIfObserved(ctx context.Context, action string, request Request, expected string) (Change, error) {
	if err := validateCanonicalFingerprint("expected low-port observation fingerprint", expected); err != nil {
		return Change{}, operationError(ErrorKindInvalidRequest, action, err)
	}
	a.mutate.Lock()
	defer a.mutate.Unlock()

	before, err := a.Observe(ctx, request)
	if err != nil {
		return Change{}, err
	}
	if fingerprintValidated(before) != expected {
		return unchangedChange(before), operationError(ErrorKindObservationChanged, action, fmt.Errorf("low-port observation changed before mutation"))
	}
	state := classifyValidated(before)
	if action == "ensure" && state == StateExact || action == "release" && state == StateAbsent {
		return unchangedChange(before), nil
	}
	switch state {
	case StateAbsent:
		if action == "release" {
			return unchangedChange(before), nil
		}
	case StateOwnedDrifted:
		if action != "ensure" || !isExactPlistWithAbsentService(before) {
			return unchangedChange(before), operationError(ErrorKindConflict, action, nil)
		}
	case StateExact:
		if action == "ensure" {
			return unchangedChange(before), nil
		}
	case StateIndeterminate:
		return unchangedChange(before), operationError(ErrorKindIndeterminate, action, nil)
	case StateForeign, StateAmbiguous:
		return unchangedChange(before), operationError(ErrorKindConflict, action, nil)
	default:
		return unchangedChange(before), operationError(ErrorKindInvalidFacts, action, nil)
	}

	mutationContext := normalizedContext(ctx)
	if err := mutationContext.Err(); err != nil {
		return Change{Before: cloneObservation(before)}, operationError(ErrorKindMutationFailed, action, err)
	}
	if action == "ensure" {
		err = a.backend.ensure(mutationContext, request, cloneObservation(before))
	} else {
		err = a.backend.release(mutationContext, request, cloneObservation(before))
	}
	change, observationErr := a.reconcileMutation(request, before)
	if err != nil {
		return change, operationError(ErrorKindMutationFailed, action, errors.Join(err, observationErr))
	}
	if observationErr != nil {
		return change, operationError(ErrorKindVerificationFailed, action, observationErr)
	}
	afterState := classifyValidated(change.After)
	if action == "ensure" && afterState != StateExact || action == "release" && afterState != StateAbsent {
		return change, operationError(ErrorKindVerificationFailed, action, nil)
	}
	return change, nil
}

// isExactPlistWithAbsentService identifies the sole safe launchd repair: a
// canonical root-owned plist whose matching system service is not loaded.
func isExactPlistWithAbsentService(observation Observation) bool {
	var plist, service *Artifact
	for index := range observation.Artifacts {
		artifact := &observation.Artifacts[index]
		switch artifact.Kind {
		case ArtifactKindPlist:
			plist = artifact
		case ArtifactKindService:
			service = artifact
		}
	}
	return plist != nil && service != nil && plist.Exact && !service.Present
}

// reconcileMutation uses fresh cancellation authority because caller cancellation cannot prove whether an effect landed.
func (a *Adapter) reconcileMutation(request Request, before Observation) (Change, error) {
	ctx, cancel := context.WithTimeout(context.Background(), postMutationObservationTimeout)
	defer cancel()
	after, err := a.Observe(ctx, request)
	if err != nil {
		return Change{Attempted: true, Indeterminate: true, Before: cloneObservation(before)}, err
	}
	return Change{
		Attempted: true,
		Changed:   fingerprintValidated(before) != fingerprintValidated(after),
		Before:    cloneObservation(before),
		After:     cloneObservation(after),
	}, nil
}

// unchangedChange returns independent snapshots for a no-op or rejected mutation.
func unchangedChange(observation Observation) Change {
	return Change{Before: cloneObservation(observation), After: cloneObservation(observation)}
}

// normalizedContext gives nil callers cancellation-free semantics shared by every adapter boundary.
func normalizedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
