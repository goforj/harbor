package trust

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const postMutationObservationTimeout = 5 * time.Second

const postMutationObservationInterval = 100 * time.Millisecond

// backend confines platform implementations to bounded observations and one exact trust effect.
type backend interface {
	// observe returns every bounded native entry relevant to the validated request.
	observe(context.Context, Request) (Observation, error)
	// ensure applies only the request's exact trust projection while preserving foreign entries.
	ensure(context.Context, Request, Observation) error
	// release removes only uniquely owned entries identified by the guarded observation.
	release(context.Context, Request, Observation) error
}

// Adapter applies platform-neutral classification and compare-and-swap policy around trust effects.
type Adapter struct {
	backend             backend
	reconciliationNow   func() time.Time
	reconciliationSleep func(context.Context, time.Duration) error
}

// newAdapter injects typed native effects so safety policy can be tested without host mutation.
func newAdapter(platform backend) *Adapter {
	return &Adapter{
		backend:             platform,
		reconciliationNow:   time.Now,
		reconciliationSleep: sleepForPostMutationObservation,
	}
}

// Observe returns a validated, bounded snapshot for one immutable trust request.
func (adapter *Adapter) Observe(ctx context.Context, request Request) (Observation, error) {
	const operation = "observe"
	if err := request.Validate(); err != nil {
		return Observation{}, operationError(ErrorKindInvalidRequest, operation, Observation{}, Assessment{}, err)
	}
	ctx = normalizedContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, operation, Observation{}, Assessment{}, err)
	}

	observation, err := adapter.backend.observe(ctx, request)
	if err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, operation, Observation{}, Assessment{}, err)
	}
	observation = cloneObservation(observation)
	if !sameRequest(observation.Request, request) {
		return Observation{}, operationError(
			ErrorKindInvalidFacts,
			operation,
			observation,
			Assessment{},
			fmt.Errorf("trust backend observation belongs to another request"),
		)
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, operationError(ErrorKindInvalidFacts, operation, observation, Assessment{}, err)
	}
	return observation, nil
}

// EnsureIfObserved ensures the exact owned trust entry only while admitted facts still match.
func (adapter *Adapter) EnsureIfObserved(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
) (Change, error) {
	const operation = "ensure"
	before, assessment, err := adapter.observeExpected(ctx, operation, request, expectedFingerprint)
	if err != nil {
		if before.Validate() == nil {
			return unchangedChange(before), err
		}
		return Change{}, err
	}
	switch assessment.State {
	case StateExact:
		return unchangedChange(before), nil
	case StateAbsent, StateOwnedDrifted:
	case StateForeign:
		if assessment.Owned == OwnedStateAbsent && onlyPreexistingIdenticalEntries(before) {
			return unchangedChange(before), nil
		}
		return unchangedChange(before), operationError(ErrorKindConflict, operation, before, assessment, nil)
	case StateAmbiguous:
		return unchangedChange(before), operationError(ErrorKindConflict, operation, before, assessment, nil)
	case StateIndeterminate:
		return unchangedChange(before), operationError(ErrorKindIndeterminate, operation, before, assessment, nil)
	default:
		return unchangedChange(before), operationError(ErrorKindInvalidFacts, operation, before, assessment, nil)
	}

	mutationContext := normalizedContext(ctx)
	if err := mutationContext.Err(); err != nil {
		return Change{Before: cloneObservation(before)}, operationError(ErrorKindMutationFailed, operation, before, assessment, err)
	}
	mutationErr := adapter.backend.ensure(mutationContext, request, cloneObservation(before))
	change, afterAssessment, observationErr := adapter.reconcileMutation(
		request,
		before,
		mutationErr == nil && request.Mechanism() == networkpolicy.DarwinAdministratorTrust,
	)
	if mutationErr != nil {
		return change, operationError(
			ErrorKindMutationFailed,
			operation,
			changeObservation(change),
			changeAssessment(change, assessment, afterAssessment),
			errors.Join(mutationErr, observationErr),
		)
	}
	if observationErr != nil {
		return change, operationError(ErrorKindVerificationFailed, operation, before, assessment, observationErr)
	}
	if afterAssessment.State != StateExact {
		return change, operationError(ErrorKindVerificationFailed, operation, change.After, afterAssessment, nil)
	}
	return change, nil
}

// ReleaseIfObserved removes uniquely owned entries only while admitted facts still match.
func (adapter *Adapter) ReleaseIfObserved(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
) (Change, error) {
	const operation = "release"
	before, assessment, err := adapter.observeExpected(ctx, operation, request, expectedFingerprint)
	if err != nil {
		if before.Validate() == nil {
			return unchangedChange(before), err
		}
		return Change{}, err
	}
	if assessment.State == StateIndeterminate {
		return unchangedChange(before), operationError(ErrorKindIndeterminate, operation, before, assessment, nil)
	}
	switch assessment.Owned {
	case OwnedStateAbsent:
		return unchangedChange(before), nil
	case OwnedStateExact, OwnedStateDrifted:
	case OwnedStateAmbiguous:
		return unchangedChange(before), operationError(ErrorKindConflict, operation, before, assessment, nil)
	default:
		return unchangedChange(before), operationError(ErrorKindInvalidFacts, operation, before, assessment, nil)
	}

	mutationContext := normalizedContext(ctx)
	if err := mutationContext.Err(); err != nil {
		return Change{Before: cloneObservation(before)}, operationError(ErrorKindMutationFailed, operation, before, assessment, err)
	}
	mutationErr := adapter.backend.release(mutationContext, request, cloneObservation(before))
	change, afterAssessment, observationErr := adapter.reconcileMutation(request, before, false)
	if mutationErr != nil {
		kind := ErrorKindMutationFailed
		switch {
		case errors.Is(mutationErr, errNativeObservationChanged):
			kind = ErrorKindObservationChanged
		case errors.Is(mutationErr, errNativeMutationConflict):
			kind = ErrorKindConflict
		}
		return change, operationError(
			kind,
			operation,
			changeObservation(change),
			changeAssessment(change, assessment, afterAssessment),
			errors.Join(mutationErr, observationErr),
		)
	}
	if observationErr != nil {
		return change, operationError(ErrorKindVerificationFailed, operation, before, assessment, observationErr)
	}
	if afterAssessment.State == StateIndeterminate || afterAssessment.Owned != OwnedStateAbsent {
		return change, operationError(ErrorKindVerificationFailed, operation, change.After, afterAssessment, nil)
	}
	return change, nil
}

// observeExpected reobserves native state and enforces the caller's exact fingerprint precondition.
func (adapter *Adapter) observeExpected(
	ctx context.Context,
	operation string,
	request Request,
	expectedFingerprint string,
) (Observation, Assessment, error) {
	if err := validateFingerprintText("expected trust observation fingerprint", expectedFingerprint); err != nil {
		return Observation{}, Assessment{}, operationError(ErrorKindInvalidRequest, operation, Observation{}, Assessment{}, err)
	}
	observation, err := adapter.Observe(ctx, request)
	if err != nil {
		return Observation{}, Assessment{}, err
	}
	assessment := classifyValidated(observation)
	actualFingerprint := fingerprintValidated(observation)
	if actualFingerprint != expectedFingerprint {
		return observation, assessment, operationError(
			ErrorKindObservationChanged,
			operation,
			observation,
			assessment,
			fmt.Errorf("trust observation changed before mutation"),
		)
	}
	return observation, assessment, nil
}

// reconcileMutation uses fresh cancellation authority because caller cancellation cannot prove whether an effect landed.
func (adapter *Adapter) reconcileMutation(request Request, before Observation, retryUntilExact bool) (Change, Assessment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), postMutationObservationTimeout)
	defer cancel()
	deadline := adapter.reconciliationNow().Add(postMutationObservationTimeout)
	for {
		after, err := adapter.Observe(ctx, request)
		if err != nil {
			return Change{
				Attempted:     true,
				Indeterminate: true,
				Before:        cloneObservation(before),
			}, Assessment{}, err
		}
		afterAssessment := classifyValidated(after)
		change := Change{
			Attempted: true,
			Changed:   observationChanged(before, after),
			Before:    cloneObservation(before),
			After:     cloneObservation(after),
		}
		if !retryUntilExact || afterAssessment.State == StateExact || !adapter.reconciliationNow().Before(deadline) {
			return change, afterAssessment, nil
		}
		interval := min(postMutationObservationInterval, deadline.Sub(adapter.reconciliationNow()))
		if err := adapter.reconciliationSleep(ctx, interval); err != nil {
			return Change{
				Attempted:     true,
				Indeterminate: true,
				Before:        cloneObservation(before),
			}, Assessment{}, err
		}
	}
}

// sleepForPostMutationObservation keeps asynchronous native visibility cancellable between fresh observations.
func sleepForPostMutationObservation(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// observationChanged compares canonical facts so same-state native replacements still report a change.
func observationChanged(before Observation, after Observation) bool {
	return fingerprintValidated(before) != fingerprintValidated(after)
}

// unchangedChange returns independent before and after snapshots for a no-op or rejected effect.
func unchangedChange(observation Observation) Change {
	return Change{
		Before: cloneObservation(observation),
		After:  cloneObservation(observation),
	}
}

// changeObservation selects post-effect evidence only when reconciliation completed.
func changeObservation(change Change) Observation {
	if !change.Indeterminate {
		return change.After
	}
	return change.Before
}

// changeAssessment selects post-effect classification only when reconciliation completed.
func changeAssessment(change Change, before Assessment, after Assessment) Assessment {
	if !change.Indeterminate {
		return after
	}
	return before
}

// normalizedContext gives nil callers cancellation-free semantics shared by every adapter boundary.
func normalizedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
