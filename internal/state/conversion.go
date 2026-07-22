package state

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// operationRecordFromModel converts a generated database model into its validated domain representation.
func operationRecordFromModel(row models.Operation) (OperationRecord, error) {
	key := row.Id
	if key == "" {
		key = "<empty>"
	}

	problem, err := problemFromModel(row.ProblemCode, row.ProblemMessage, row.ProblemRetryable)
	if err != nil {
		return OperationRecord{}, corruptStateError("operation", key, err)
	}
	if row.Revision <= 0 {
		return OperationRecord{}, corruptStateError("operation", key, fmt.Errorf("revision must be positive"))
	}

	operation := domain.Operation{
		ID:          domain.OperationID(row.Id),
		IntentID:    domain.IntentID(row.IntentId),
		Kind:        domain.OperationKind(row.Kind),
		State:       domain.OperationState(row.State),
		Phase:       row.Phase,
		Problem:     problem,
		RequestedAt: row.RequestedAt,
		StartedAt:   copyTimePointer(row.StartedAt),
		FinishedAt:  copyTimePointer(row.FinishedAt),
	}
	if row.ProjectId.Valid {
		operation.ProjectID = domain.ProjectID(row.ProjectId.String)
	}
	if err := operation.Validate(); err != nil {
		return OperationRecord{}, corruptStateError("operation", key, err)
	}

	return OperationRecord{
		Operation: operation,
		Revision:  domain.Sequence(row.Revision),
	}, nil
}

// operationModelFromDomain prepares one validated operation for generated repository persistence.
func operationModelFromDomain(operation domain.Operation, revision domain.Sequence) (models.Operation, error) {
	if err := operation.Validate(); err != nil {
		return models.Operation{}, err
	}
	modelRevision, err := sequenceToModelInt("operation revision", revision, false)
	if err != nil {
		return models.Operation{}, err
	}

	problemCode, problemMessage, problemRetryable := problemToModel(operation.Problem)
	return models.Operation{
		Id:               string(operation.ID),
		IntentId:         string(operation.IntentID),
		Kind:             string(operation.Kind),
		ProjectId:        optionalString(string(operation.ProjectID)),
		State:            string(operation.State),
		Phase:            operation.Phase,
		ProblemCode:      problemCode,
		ProblemMessage:   problemMessage,
		ProblemRetryable: problemRetryable,
		RequestedAt:      operation.RequestedAt,
		StartedAt:        copyTimePointer(operation.StartedAt),
		FinishedAt:       copyTimePointer(operation.FinishedAt),
		Revision:         modelRevision,
	}, nil
}

// operationTransitionFromModel converts and validates one append-only transition row.
func operationTransitionFromModel(row models.OperationTransition) (OperationTransition, error) {
	key := strconv.Itoa(row.Id)
	if row.Id <= 0 {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("ID must be positive"))
	}
	operationID := domain.OperationID(row.OperationId)
	if err := operationID.Validate(); err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}
	if row.Ordinal <= 0 {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("ordinal must be positive"))
	}
	if row.Sequence <= 0 {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("sequence must be positive"))
	}
	if strings.TrimSpace(row.Phase) == "" {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("phase must not be empty"))
	}
	if err := validateStoredTime("transition occurrence time", row.OccurredAt); err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}

	state := domain.OperationState(row.State)
	if err := state.Validate(); err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}
	previousState, err := previousStateFromModel(row.PreviousState)
	if err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}
	if err := validateStoredTransitionEdge(row.Ordinal, previousState, state); err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}

	problem, err := problemFromModel(row.ProblemCode, row.ProblemMessage, row.ProblemRetryable)
	if err != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, err)
	}
	if state == domain.OperationFailed && problem == nil {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("failed transition must contain a problem"))
	}
	if state != domain.OperationFailed && problem != nil {
		return OperationTransition{}, corruptStateError("operation transition", key, fmt.Errorf("%s transition must not contain a problem", state))
	}

	return OperationTransition{
		OperationID:   operationID,
		Ordinal:       uint64(row.Ordinal),
		PreviousState: previousState,
		State:         state,
		Phase:         row.Phase,
		Problem:       problem,
		OccurredAt:    row.OccurredAt,
		Sequence:      domain.Sequence(row.Sequence),
	}, nil
}

// operationTransitionModelFromDomain prepares one validated lifecycle edge for append-only persistence.
func operationTransitionModelFromDomain(transition OperationTransition) (models.OperationTransition, error) {
	if err := transition.Validate(); err != nil {
		return models.OperationTransition{}, err
	}
	ordinal, err := unsignedToModelInt("operation transition ordinal", transition.Ordinal, false)
	if err != nil {
		return models.OperationTransition{}, err
	}
	sequence, err := sequenceToModelInt("operation transition sequence", transition.Sequence, false)
	if err != nil {
		return models.OperationTransition{}, err
	}

	problemCode, problemMessage, problemRetryable := problemToModel(transition.Problem)
	previousState := null.String{}
	if transition.PreviousState != nil {
		previousState = null.StringFrom(string(*transition.PreviousState))
	}
	return models.OperationTransition{
		OperationId:      string(transition.OperationID),
		Ordinal:          ordinal,
		PreviousState:    previousState,
		State:            string(transition.State),
		Phase:            transition.Phase,
		ProblemCode:      problemCode,
		ProblemMessage:   problemMessage,
		ProblemRetryable: problemRetryable,
		OccurredAt:       transition.OccurredAt,
		Sequence:         sequence,
	}, nil
}

// harborStateSequenceFromModel validates the singleton Harbor row before exposing its global sequence.
func harborStateSequenceFromModel(row models.HarborState) (domain.Sequence, error) {
	if row.Id != 1 {
		return 0, corruptStateError("harbor state", strconv.Itoa(row.Id), fmt.Errorf("singleton ID must be 1"))
	}
	if row.Sequence < 0 {
		return 0, corruptStateError("harbor state", "1", fmt.Errorf("sequence must not be negative"))
	}
	if domain.Sequence(row.Sequence) > domain.MaximumSequence {
		return 0, corruptStateError("harbor state", "1", fmt.Errorf("sequence exceeds the cross-client ordering range"))
	}
	return domain.Sequence(row.Sequence), nil
}

// problemFromModel enforces the all-or-none nullable shape used by operation failures.
func problemFromModel(code, message null.String, retryable null.Bool) (*domain.Problem, error) {
	validCount := 0
	for _, valid := range []bool{code.Valid, message.Valid, retryable.Valid} {
		if valid {
			validCount++
		}
	}
	if validCount == 0 {
		return nil, nil
	}
	if validCount != 3 {
		return nil, fmt.Errorf("problem fields must either all be null or all contain values")
	}
	problem := &domain.Problem{
		Code:      domain.ProblemCode(code.String),
		Message:   message.String,
		Retryable: retryable.Bool,
	}
	if err := problem.Validate(); err != nil {
		return nil, err
	}
	return problem, nil
}

// problemToModel preserves the nullable database representation for optional domain problems.
func problemToModel(problem *domain.Problem) (null.String, null.String, null.Bool) {
	if problem == nil {
		return null.String{}, null.String{}, null.Bool{}
	}
	return null.StringFrom(string(problem.Code)), null.StringFrom(problem.Message), null.BoolFrom(problem.Retryable)
}

// previousStateFromModel validates an optional transition predecessor.
func previousStateFromModel(value null.String) (*domain.OperationState, error) {
	if !value.Valid {
		return nil, nil
	}
	state := domain.OperationState(value.String)
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("previous state: %w", err)
	}
	return &state, nil
}

// validateStoredTransitionEdge proves a row represents one legal domain lifecycle edge.
func validateStoredTransitionEdge(ordinal int, previous *domain.OperationState, state domain.OperationState) error {
	if ordinal == 1 {
		if previous != nil || state != domain.OperationQueued {
			return fmt.Errorf("first transition must enter queued state without a previous state")
		}
		return nil
	}
	if previous == nil {
		return fmt.Errorf("transition ordinal %d must contain a previous state", ordinal)
	}
	if !previous.CanTransitionTo(state) {
		return fmt.Errorf("operation cannot transition from %q to %q", *previous, state)
	}
	return nil
}

// validateStoredTime applies the domain's UTC and non-zero guarantees to history rows.
func validateStoredTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s must not be zero", name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", name)
	}
	return nil
}

// optionalString maps an empty optional domain identifier to SQL NULL.
func optionalString(value string) null.String {
	if value == "" {
		return null.String{}
	}
	return null.StringFrom(value)
}

// copyTimePointer prevents returned records from sharing mutable timestamp pointers with generated models.
func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// sequenceToModelInt rejects global sequence values that cannot fit generated integer fields.
func sequenceToModelInt(name string, value domain.Sequence, allowZero bool) (int, error) {
	if value > domain.MaximumSequence {
		return 0, fmt.Errorf("%s exceeds the cross-client ordering range", name)
	}
	return unsignedToModelInt(name, uint64(value), allowZero)
}

// modelIntToSequence validates a generated integer before exposing it as a public sequence.
func modelIntToSequence(name string, value int) (domain.Sequence, error) {
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	sequence := domain.Sequence(value)
	if sequence > domain.MaximumSequence {
		return 0, fmt.Errorf("%s exceeds the cross-client ordering range", name)
	}
	return sequence, nil
}

// unsignedToModelInt safely narrows public counters into generated model fields.
func unsignedToModelInt(name string, value uint64, allowZero bool) (int, error) {
	if !allowZero && value == 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	converted := int(value)
	if converted < 0 || uint64(converted) != value {
		return 0, fmt.Errorf("%s exceeds the supported database range", name)
	}
	return converted, nil
}
