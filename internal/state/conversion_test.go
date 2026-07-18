package state

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// TestOperationRecordFromModelRejectsCorruptRows verifies each nullable and domain validation boundary returns typed corruption.
func TestOperationRecordFromModelRejectsCorruptRows(t *testing.T) {
	valid := validOperationJournalModel()
	tests := []struct {
		name   string
		mutate func(*models.Operation)
	}{
		{name: "empty ID", mutate: func(row *models.Operation) { row.Id = "" }},
		{name: "invalid project", mutate: func(row *models.Operation) { row.ProjectId = null.StringFrom(" project ") }},
		{name: "partial problem", mutate: func(row *models.Operation) { row.ProblemCode = null.StringFrom("failed") }},
		{name: "zero revision", mutate: func(row *models.Operation) { row.Revision = 0 }},
		{name: "invalid state", mutate: func(row *models.Operation) { row.State = "unknown" }},
		{name: "local requested time", mutate: func(row *models.Operation) {
			row.RequestedAt = time.Date(2026, time.July, 18, 8, 0, 0, 0, time.FixedZone("local", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			_, err := operationRecordFromModel(row)
			assertCorruptStateError(t, err, "operation")
		})
	}
}

// TestOperationModelFromDomainRejectsInvalidInput verifies generated model narrowing cannot persist invalid domain values.
func TestOperationModelFromDomainRejectsInvalidInput(t *testing.T) {
	operation := newOperationJournalTestOperation(
		t,
		"operation-conversion",
		"intent-conversion",
		"",
		"project.start",
		operationJournalTestTime(),
	)
	operation.ID = ""
	if _, err := operationModelFromDomain(operation, 1); err == nil {
		t.Fatal("invalid domain operation unexpectedly converted")
	}
	operation.ID = "operation-conversion"
	if _, err := operationModelFromDomain(operation, 0); err == nil {
		t.Fatal("zero operation revision unexpectedly converted")
	}
	if strconvIntSize() < 64 {
		t.Skip("sequence overflow case requires a wider public sequence than model int")
	}
	if _, err := unsignedToModelInt("value", math.MaxUint64, false); err == nil {
		t.Fatal("overflowing model integer unexpectedly converted")
	}
}

// TestOperationTransitionFromModelRejectsCorruptRows verifies every generated history field is validated before exposure.
func TestOperationTransitionFromModelRejectsCorruptRows(t *testing.T) {
	valid := validOperationJournalTransitionModel()
	tests := []struct {
		name   string
		mutate func(*models.OperationTransition)
	}{
		{name: "nonpositive ID", mutate: func(row *models.OperationTransition) { row.Id = 0 }},
		{name: "invalid operation ID", mutate: func(row *models.OperationTransition) { row.OperationId = " operation " }},
		{name: "nonpositive ordinal", mutate: func(row *models.OperationTransition) { row.Ordinal = 0 }},
		{name: "nonpositive sequence", mutate: func(row *models.OperationTransition) { row.Sequence = 0 }},
		{name: "empty phase", mutate: func(row *models.OperationTransition) { row.Phase = "  " }},
		{name: "zero occurrence", mutate: func(row *models.OperationTransition) { row.OccurredAt = time.Time{} }},
		{name: "local occurrence", mutate: func(row *models.OperationTransition) {
			row.OccurredAt = time.Date(2026, time.July, 18, 8, 0, 0, 0, time.FixedZone("local", 3600))
		}},
		{name: "invalid state", mutate: func(row *models.OperationTransition) { row.State = "unknown" }},
		{name: "invalid previous state", mutate: func(row *models.OperationTransition) {
			row.Ordinal = 2
			row.PreviousState = null.StringFrom("unknown")
		}},
		{name: "missing previous state", mutate: func(row *models.OperationTransition) { row.Ordinal = 2 }},
		{name: "illegal edge", mutate: func(row *models.OperationTransition) {
			row.Ordinal = 2
			row.PreviousState = null.StringFrom(string(domain.OperationQueued))
			row.State = string(domain.OperationSucceeded)
		}},
		{name: "failed without problem", mutate: func(row *models.OperationTransition) {
			row.Ordinal = 2
			row.PreviousState = null.StringFrom(string(domain.OperationRunning))
			row.State = string(domain.OperationFailed)
		}},
		{name: "queued with problem", mutate: func(row *models.OperationTransition) {
			row.ProblemCode = null.StringFrom("problem")
			row.ProblemMessage = null.StringFrom("A problem occurred.")
			row.ProblemRetryable = null.BoolFrom(false)
		}},
		{name: "partial problem", mutate: func(row *models.OperationTransition) {
			row.ProblemCode = null.StringFrom("problem")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			_, err := operationTransitionFromModel(row)
			assertCorruptStateError(t, err, "operation transition")
		})
	}
}

// TestOperationTransitionConversionRoundTrip verifies nullable predecessor and failure fields survive generated model persistence.
func TestOperationTransitionConversionRoundTrip(t *testing.T) {
	previous := domain.OperationRunning
	problem := &domain.Problem{Code: "failed", Message: "The operation failed.", Retryable: false}
	transition := OperationTransition{
		OperationID:   "operation-roundtrip",
		Ordinal:       2,
		PreviousState: &previous,
		State:         domain.OperationFailed,
		Phase:         "failed",
		Problem:       problem,
		OccurredAt:    operationJournalTestTime(),
		Sequence:      9,
	}
	row, err := operationTransitionModelFromDomain(transition)
	if err != nil {
		t.Fatalf("convert transition to model: %v", err)
	}
	row.Id = 4
	converted, err := operationTransitionFromModel(row)
	if err != nil {
		t.Fatalf("convert transition from model: %v", err)
	}
	if converted.PreviousState == nil || *converted.PreviousState != previous || converted.Problem == nil || *converted.Problem != *problem {
		t.Fatalf("round-trip transition = %#v", converted)
	}
}

// TestOperationTransitionValidateRejectsInvalidValues verifies the public history type carries the same constraints as persistence.
func TestOperationTransitionValidateRejectsInvalidValues(t *testing.T) {
	valid := OperationTransition{
		OperationID: "operation-validate",
		Ordinal:     1,
		State:       domain.OperationQueued,
		Phase:       "queued",
		OccurredAt:  operationJournalTestTime(),
		Sequence:    1,
	}
	mutations := []func(*OperationTransition){
		func(transition *OperationTransition) { transition.OperationID = "" },
		func(transition *OperationTransition) { transition.Ordinal = 0 },
		func(transition *OperationTransition) { transition.Ordinal = math.MaxUint64 },
		func(transition *OperationTransition) { transition.Sequence = 0 },
		func(transition *OperationTransition) { transition.Sequence = domain.Sequence(math.MaxUint64) },
		func(transition *OperationTransition) { transition.State = "unknown" },
		func(transition *OperationTransition) { transition.Ordinal = 2 },
		func(transition *OperationTransition) { transition.Phase = "  " },
		func(transition *OperationTransition) { transition.OccurredAt = time.Time{} },
		func(transition *OperationTransition) {
			transition.Problem = &domain.Problem{Code: "bad", Message: " bad "}
		},
		func(transition *OperationTransition) {
			transition.Problem = &domain.Problem{Code: "bad", Message: "A problem occurred."}
		},
	}
	for index, mutate := range mutations {
		t.Run(strings.Repeat("case-", 1)+string(rune('a'+index)), func(t *testing.T) {
			transition := valid
			mutate(&transition)
			if err := transition.Validate(); err == nil {
				t.Fatalf("invalid transition %#v unexpectedly validated", transition)
			}
		})
	}

	previous := domain.OperationRunning
	valid.Ordinal = 2
	valid.PreviousState = &previous
	valid.State = domain.OperationFailed
	valid.Problem = &domain.Problem{Code: "failed", Message: "The operation failed."}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid failed transition: %v", err)
	}
}

// TestOperationSequenceFromModelRejectsWrongSingleton verifies only the seeded journal row can represent global sequence state.
func TestOperationSequenceFromModelRejectsWrongSingleton(t *testing.T) {
	for _, row := range []models.OperationJournalState{{Id: 2, Sequence: 1}, {Id: 1, Sequence: -1}} {
		_, err := operationSequenceFromModel(row)
		assertCorruptStateError(t, err, "operation journal state")
	}
	sequence, err := operationSequenceFromModel(models.OperationJournalState{Id: 1, Sequence: 0})
	if err != nil || sequence != 0 {
		t.Fatalf("zero singleton sequence = %d, %v", sequence, err)
	}
}

// TestOperationJournalErrorMessages verifies typed errors retain useful identity and unwrap their validation cause.
func TestOperationJournalErrorMessages(t *testing.T) {
	cause := errors.New("invalid row")
	errorsToCheck := []error{
		&OperationNotFoundError{OperationID: "operation-a"},
		&OperationIntentNotFoundError{IntentID: "intent-a"},
		&IntentConflictError{IntentID: "intent-a", ExistingOperationID: "operation-a", ExistingKind: "start", ExistingProjectID: "project-a", RequestedKind: "stop", RequestedProjectID: "project-a"},
		&OperationIDConflictError{OperationID: "operation-a", ExistingIntentID: "intent-a", RequestedIntentID: "intent-b"},
		&StaleRevisionError{OperationID: "operation-a", Expected: 1, Actual: 2},
		&CorruptStateError{Entity: "operation", Key: "operation-a", Cause: cause},
	}
	for _, err := range errorsToCheck {
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatalf("empty typed error message for %T", err)
		}
	}
	var corrupt *CorruptStateError
	if !errors.As(errorsToCheck[len(errorsToCheck)-1], &corrupt) || !errors.Is(corrupt, cause) {
		t.Fatalf("corrupt error did not unwrap cause: %v", corrupt)
	}
}

// validOperationJournalModel returns one generated row accepted by the domain conversion.
func validOperationJournalModel() models.Operation {
	return models.Operation{
		Id:          "operation-valid",
		IntentId:    "intent-valid",
		Kind:        "project.start",
		State:       string(domain.OperationQueued),
		Phase:       "queued",
		RequestedAt: operationJournalTestTime(),
		Revision:    1,
	}
}

// validOperationJournalTransitionModel returns one generated initial history row accepted by conversion.
func validOperationJournalTransitionModel() models.OperationTransition {
	return models.OperationTransition{
		Id:          1,
		OperationId: "operation-valid",
		Ordinal:     1,
		State:       string(domain.OperationQueued),
		Phase:       "queued",
		OccurredAt:  operationJournalTestTime(),
		Sequence:    1,
	}
}

// strconvIntSize reports the platform integer width without importing implementation-specific build tags.
func strconvIntSize() int {
	return 32 << (^uint(0) >> 63)
}
