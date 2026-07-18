package state

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const harborStateSingletonID = 1

// OperationRecord couples a durable domain operation to its optimistic revision.
type OperationRecord struct {
	Operation domain.Operation
	Revision  domain.Sequence
}

// JournalSnapshot couples the current durable sequence with the active operations visible at that same database instant.
type JournalSnapshot struct {
	// Sequence is the latest globally committed journal sequence.
	Sequence domain.Sequence
	// Operations contains non-terminal operations in durable revision order.
	Operations []domain.Operation
}

// OperationTransition records one append-only lifecycle edge and its global sequence.
type OperationTransition struct {
	OperationID   domain.OperationID
	Ordinal       uint64
	PreviousState *domain.OperationState
	State         domain.OperationState
	Phase         string
	Problem       *domain.Problem
	OccurredAt    time.Time
	Sequence      domain.Sequence
}

// Validate reports whether a transition can be represented by the durable journal.
func (transition OperationTransition) Validate() error {
	if err := transition.OperationID.Validate(); err != nil {
		return err
	}
	ordinal, err := unsignedToModelInt("operation transition ordinal", transition.Ordinal, false)
	if err != nil {
		return err
	}
	if _, err := sequenceToModelInt("operation transition sequence", transition.Sequence, false); err != nil {
		return err
	}
	if err := transition.State.Validate(); err != nil {
		return err
	}
	if err := validateStoredTransitionEdge(ordinal, transition.PreviousState, transition.State); err != nil {
		return err
	}
	if strings.TrimSpace(transition.Phase) == "" {
		return fmt.Errorf("operation transition phase must not be empty")
	}
	if err := validateStoredTime("operation transition time", transition.OccurredAt); err != nil {
		return err
	}
	if transition.Problem != nil {
		if err := transition.Problem.Validate(); err != nil {
			return err
		}
	}
	if transition.State == domain.OperationFailed && transition.Problem == nil {
		return fmt.Errorf("failed transition must contain a problem")
	}
	if transition.State != domain.OperationFailed && transition.Problem != nil {
		return fmt.Errorf("%s transition must not contain a problem", transition.State)
	}
	return nil
}

// OperationJournal owns durable idempotency, lifecycle transitions, and their global ordering.
type OperationJournal struct {
	connections *database.Connections
	operations  *models.OperationRepo
	transitions *models.OperationTransitionRepo
	harborState *models.HarborStateRepo
	mutations   *MutationCoordinator
}

// NewOperationJournal constructs a journal from the generated repositories for the named harbord database.
func NewOperationJournal(
	connections *database.Connections,
	operations *models.OperationRepo,
	transitions *models.OperationTransitionRepo,
	harborState *models.HarborStateRepo,
	mutations *MutationCoordinator,
) *OperationJournal {
	return &OperationJournal{
		connections: connections,
		operations:  operations,
		transitions: transitions,
		harborState: harborState,
		mutations:   mutations,
	}
}

// Enqueue durably records one queued operation or replays the matching idempotent intent.
func (journal *OperationJournal) Enqueue(ctx context.Context, operation domain.Operation) (OperationRecord, error) {
	ctx = normalizeContext(ctx)
	if err := operation.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("enqueue operation: %w", err)
	}
	if operation.State != domain.OperationQueued {
		return OperationRecord{}, fmt.Errorf("enqueue operation must be queued")
	}

	var record OperationRecord
	err := journal.mutations.mutate(ctx, "operation journal", func(tx *gorm.DB) error {
		existing, found, err := findOperationByIntent(tx, operation.IntentID)
		if err != nil {
			return err
		}
		if found {
			existingRecord, err := operationRecordFromModel(existing)
			if err != nil {
				return err
			}
			if existingRecord.Operation.Kind != operation.Kind || existingRecord.Operation.ProjectID != operation.ProjectID {
				return &IntentConflictError{
					IntentID:            operation.IntentID,
					ExistingOperationID: existingRecord.Operation.ID,
					ExistingKind:        existingRecord.Operation.Kind,
					ExistingProjectID:   existingRecord.Operation.ProjectID,
					RequestedKind:       operation.Kind,
					RequestedProjectID:  operation.ProjectID,
				}
			}
			if _, err := validateRetainedSequenceBounds(tx); err != nil {
				return err
			}
			if _, err := operationHistoryInTransaction(tx, existingRecord); err != nil {
				return err
			}
			record = existingRecord
			return nil
		}

		existing, found, err = findOperationByID(tx, operation.ID)
		if err != nil {
			return err
		}
		if found {
			existingRecord, err := operationRecordFromModel(existing)
			if err != nil {
				return err
			}
			return &OperationIDConflictError{
				OperationID:       operation.ID,
				ExistingIntentID:  existingRecord.Operation.IntentID,
				RequestedIntentID: operation.IntentID,
			}
		}
		boundary, err := readProjectNetworkReleaseBoundary(tx, operation.ProjectID)
		if err != nil {
			return err
		}
		if err := rejectProjectNetworkReleaseMutation(boundary, operation.ProjectID, "enqueue operation"); err != nil {
			return err
		}

		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		row, err := operationModelFromDomain(operation, sequence)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create operation: %w", err)
		}
		transition, err := operationTransitionModelFromDomain(OperationTransition{
			OperationID: operation.ID,
			Ordinal:     1,
			State:       domain.OperationQueued,
			Phase:       operation.Phase,
			OccurredAt:  operation.RequestedAt,
			Sequence:    sequence,
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&transition).Error; err != nil {
			return fmt.Errorf("append queued operation transition: %w", err)
		}
		record = OperationRecord{Operation: operation, Revision: sequence}
		return nil
	})
	if err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// Transition advances one operation when its durable revision still matches the caller's expectation.
func (journal *OperationJournal) Transition(
	ctx context.Context,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
	next domain.OperationState,
	phase string,
	at time.Time,
	problem *domain.Problem,
) (OperationRecord, error) {
	ctx = normalizeContext(ctx)
	if err := operationID.Validate(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	err := journal.mutations.mutate(ctx, "operation journal", func(tx *gorm.DB) error {
		row, found, err := findOperationByID(tx, operationID)
		if err != nil {
			return err
		}
		if !found {
			return &OperationNotFoundError{OperationID: operationID}
		}
		current, err := operationRecordFromModel(row)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return &StaleRevisionError{
				OperationID: operationID,
				Expected:    expectedRevision,
				Actual:      current.Revision,
			}
		}
		if current.Operation.Kind == domain.OperationKindProjectUnregister && next == domain.OperationSucceeded {
			return fmt.Errorf("project unregister operations must complete through the project Store")
		}

		nextOperation, err := current.Operation.Transition(next, phase, at, problem)
		if err != nil {
			return err
		}
		history, err := operationHistoryInTransaction(tx, current)
		if err != nil {
			return err
		}
		boundary, err := readProjectNetworkReleaseBoundary(tx, current.Operation.ProjectID)
		if err != nil {
			return err
		}
		if err := validateProjectNetworkReleaseTransition(boundary, current.Operation, next); err != nil {
			return err
		}
		lastTransition := history[len(history)-1]

		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		nextRow, err := operationModelFromDomain(nextOperation, sequence)
		if err != nil {
			return err
		}
		update := operationUpdateColumns(nextRow)
		updated := tx.Model(&models.Operation{}).
			Where("id = ? AND revision = ?", string(operationID), int(expectedRevision)).
			Updates(update)
		if updated.Error != nil {
			return fmt.Errorf("update operation: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return &StaleRevisionError{
				OperationID: operationID,
				Expected:    expectedRevision,
				Actual:      current.Revision,
			}
		}

		previousState := current.Operation.State
		transition := OperationTransition{
			OperationID:   operationID,
			Ordinal:       lastTransition.Ordinal + 1,
			PreviousState: &previousState,
			State:         nextOperation.State,
			Phase:         nextOperation.Phase,
			Problem:       nextOperation.Problem,
			OccurredAt:    at,
			Sequence:      sequence,
		}
		transitionRow, err := operationTransitionModelFromDomain(transition)
		if err != nil {
			return err
		}
		if err := tx.Create(&transitionRow).Error; err != nil {
			return fmt.Errorf("append operation transition: %w", err)
		}
		result = OperationRecord{Operation: nextOperation, Revision: sequence}
		return nil
	})
	if err != nil {
		return OperationRecord{}, err
	}
	return result, nil
}

// Operation returns one durable operation by its daemon-owned ID.
func (journal *OperationJournal) Operation(ctx context.Context, operationID domain.OperationID) (OperationRecord, error) {
	ctx = normalizeContext(ctx)
	if err := operationID.Validate(); err != nil {
		return OperationRecord{}, err
	}
	row, err := journal.operations.WithContext(ctx).ByID(string(operationID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OperationRecord{}, &OperationNotFoundError{OperationID: operationID}
	}
	if err != nil {
		return OperationRecord{}, fmt.Errorf("read operation: %w", err)
	}
	return operationRecordFromModel(*row)
}

// OperationByIntent returns one durable operation by its client-stable idempotency key.
func (journal *OperationJournal) OperationByIntent(ctx context.Context, intentID domain.IntentID) (OperationRecord, error) {
	ctx = normalizeContext(ctx)
	if err := intentID.Validate(); err != nil {
		return OperationRecord{}, err
	}
	row, err := journal.operations.WithContext(ctx).FirstWhere(map[string]any{"intent_id": string(intentID)})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OperationRecord{}, &OperationIntentNotFoundError{IntentID: intentID}
	}
	if err != nil {
		return OperationRecord{}, fmt.Errorf("read operation intent: %w", err)
	}
	return operationRecordFromModel(*row)
}

// ActiveOperations returns queued and in-progress operations in durable revision order.
func (journal *OperationJournal) ActiveOperations(ctx context.Context) ([]OperationRecord, error) {
	ctx = normalizeContext(ctx)
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return nil, fmt.Errorf("build active operations query: %w", err)
	}
	var rows []models.Operation
	err = builder.
		Where("state IN ?", []string{
			string(domain.OperationQueued),
			string(domain.OperationRunning),
			string(domain.OperationRequiresApproval),
		}).
		Order("revision ASC").
		Order("id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read active operations: %w", err)
	}

	records := make([]OperationRecord, 0, len(rows))
	for _, row := range rows {
		record, err := operationRecordFromModel(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Snapshot returns the journal sequence and active operations from one consistent read transaction.
func (journal *OperationJournal) Snapshot(ctx context.Context) (JournalSnapshot, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return JournalSnapshot{}, err
	}
	connection, err := journal.connections.GetHarbord()
	if err != nil {
		return JournalSnapshot{}, fmt.Errorf("open operation journal snapshot: %w", err)
	}

	var snapshot JournalSnapshot
	err = connection.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		read, err := readJournalSnapshot(tx)
		if err != nil {
			return err
		}
		snapshot = read
		return nil
	})
	if err != nil {
		return JournalSnapshot{}, fmt.Errorf("read operation journal snapshot: %w", err)
	}
	return snapshot, nil
}

// Transitions returns an operation's complete append-only history in ordinal order.
func (journal *OperationJournal) Transitions(ctx context.Context, operationID domain.OperationID) ([]OperationTransition, error) {
	ctx = normalizeContext(ctx)
	if err := operationID.Validate(); err != nil {
		return nil, err
	}
	record, err := journal.Operation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	builder, err := journal.transitions.WithContext(ctx).Builder()
	if err != nil {
		return nil, fmt.Errorf("build operation transitions query: %w", err)
	}
	var rows []models.OperationTransition
	if err := builder.Where("operation_id = ?", string(operationID)).Order("ordinal ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read operation transitions: %w", err)
	}
	transitions, err := operationTransitionsFromModels(rows, operationID)
	if err != nil {
		return nil, err
	}
	if err := validateOperationHistory(record, transitions); err != nil {
		return nil, err
	}
	return transitions, nil
}

// CurrentSequence returns the latest globally committed Harbor mutation sequence.
func (journal *OperationJournal) CurrentSequence(ctx context.Context) (domain.Sequence, error) {
	ctx = normalizeContext(ctx)
	row, err := journal.harborState.WithContext(ctx).ByID(harborStateSingletonID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, corruptStateError("harbor state", "1", fmt.Errorf("singleton row is missing"))
	}
	if err != nil {
		return 0, fmt.Errorf("read Harbor sequence: %w", err)
	}
	return harborStateSequenceFromModel(*row)
}

// readJournalSnapshot validates the singleton sequence and every active operation before returning transaction-local state.
func readJournalSnapshot(tx *gorm.DB) (JournalSnapshot, error) {
	sequence, err := readSnapshotSequence(tx)
	if err != nil {
		return JournalSnapshot{}, err
	}
	records, err := readSnapshotOperations(tx, sequence)
	if err != nil {
		return JournalSnapshot{}, err
	}

	operations := make([]domain.Operation, 0, len(records))
	for _, record := range records {
		operations = append(operations, record.Operation)
	}
	return JournalSnapshot{Sequence: sequence, Operations: operations}, nil
}

// readSnapshotSequence reads every singleton-table row so weakened schemas cannot hide extra journal authorities.
func readSnapshotSequence(tx *gorm.DB) (domain.Sequence, error) {
	var rows []models.HarborState
	if err := tx.Order("id ASC").Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("read Harbor snapshot sequence: %w", err)
	}
	if len(rows) == 0 {
		return 0, corruptStateError("harbor state", "1", fmt.Errorf("singleton row is missing"))
	}
	for _, row := range rows {
		if _, err := harborStateSequenceFromModel(row); err != nil {
			return 0, err
		}
	}
	return harborStateSequenceFromModel(rows[0])
}

// readSnapshotOperations validates and deterministically orders every non-terminal operation visible in the transaction.
func readSnapshotOperations(tx *gorm.DB, sequence domain.Sequence) ([]OperationRecord, error) {
	var rows []models.Operation
	if err := tx.
		Where("state NOT IN ? OR state IS NULL", []string{
			string(domain.OperationSucceeded),
			string(domain.OperationFailed),
			string(domain.OperationCancelled),
		}).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read active operation snapshot: %w", err)
	}

	records := make([]OperationRecord, 0, len(rows))
	for _, row := range rows {
		record, err := operationRecordFromModel(row)
		if err != nil {
			return nil, err
		}
		if record.Revision > sequence {
			return nil, corruptStateError(
				"operation",
				string(record.Operation.ID),
				fmt.Errorf("revision %d exceeds journal sequence %d", record.Revision, sequence),
			)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Revision != records[j].Revision {
			return records[i].Revision < records[j].Revision
		}
		return records[i].Operation.ID < records[j].Operation.ID
	})
	for index := 1; index < len(records); index++ {
		if records[index-1].Revision == records[index].Revision {
			return nil, corruptStateError(
				"operation",
				string(records[index].Operation.ID),
				fmt.Errorf("revision %d is also used by operation %q", records[index].Revision, records[index-1].Operation.ID),
			)
		}
	}
	return records, nil
}

// allocateHarborSequence validates retained sequence bounds, then advances and reads the singleton inside the caller's transaction.
func allocateHarborSequence(tx *gorm.DB) (domain.Sequence, error) {
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return 0, err
	}
	updated := tx.Model(&models.HarborState{}).
		Where("id = ?", harborStateSingletonID).
		UpdateColumn("sequence", gorm.Expr("sequence + 1"))
	if updated.Error != nil {
		return 0, fmt.Errorf("advance Harbor sequence: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return 0, corruptStateError("harbor state", "1", fmt.Errorf("singleton row is missing"))
	}
	var row models.HarborState
	if err := tx.Where("id = ?", harborStateSingletonID).First(&row).Error; err != nil {
		return 0, fmt.Errorf("read advanced Harbor sequence: %w", err)
	}
	return harborStateSequenceFromModel(row)
}

// findOperationByID distinguishes an absent row from a storage failure inside a mutation transaction.
func findOperationByID(tx *gorm.DB, operationID domain.OperationID) (models.Operation, bool, error) {
	var row models.Operation
	err := tx.Where("id = ?", string(operationID)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Operation{}, false, nil
	}
	if err != nil {
		return models.Operation{}, false, fmt.Errorf("find operation: %w", err)
	}
	return row, true, nil
}

// findOperationByIntent distinguishes an absent idempotency key from a storage failure inside a mutation transaction.
func findOperationByIntent(tx *gorm.DB, intentID domain.IntentID) (models.Operation, bool, error) {
	var row models.Operation
	err := tx.Where("intent_id = ?", string(intentID)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Operation{}, false, nil
	}
	if err != nil {
		return models.Operation{}, false, fmt.Errorf("find operation intent: %w", err)
	}
	return row, true, nil
}

// operationHistoryInTransaction validates every existing edge before a mutation appends another one.
func operationHistoryInTransaction(tx *gorm.DB, record OperationRecord) ([]OperationTransition, error) {
	var rows []models.OperationTransition
	if err := tx.
		Where("operation_id = ?", string(record.Operation.ID)).
		Order("ordinal ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read operation transition history: %w", err)
	}
	history, err := operationTransitionsFromModels(rows, record.Operation.ID)
	if err != nil {
		return nil, err
	}
	if err := validateOperationHistory(record, history); err != nil {
		return nil, err
	}
	return history, nil
}

// operationTransitionsFromModels converts a complete query result while enforcing its append-only chain shape.
func operationTransitionsFromModels(rows []models.OperationTransition, operationID domain.OperationID) ([]OperationTransition, error) {
	if len(rows) == 0 {
		return nil, corruptStateError("operation", string(operationID), fmt.Errorf("transition history must not be empty"))
	}

	transitions := make([]OperationTransition, 0, len(rows))
	for index, row := range rows {
		transition, err := operationTransitionFromModel(row)
		if err != nil {
			return nil, err
		}
		if transition.OperationID != operationID {
			return nil, corruptStateError("operation transition", fmt.Sprint(row.Id), fmt.Errorf("operation ID does not match history"))
		}
		if transition.Ordinal != uint64(index+1) {
			return nil, corruptStateError("operation transition", fmt.Sprint(row.Id), fmt.Errorf("history ordinal is not contiguous"))
		}
		if index > 0 {
			previous := transitions[index-1]
			if transition.PreviousState == nil || *transition.PreviousState != previous.State {
				return nil, corruptStateError("operation transition", fmt.Sprint(row.Id), fmt.Errorf("previous state does not match prior transition"))
			}
			if transition.Sequence <= previous.Sequence {
				return nil, corruptStateError("operation transition", fmt.Sprint(row.Id), fmt.Errorf("sequence must increase across operation history"))
			}
			if transition.OccurredAt.Before(previous.OccurredAt) {
				return nil, corruptStateError("operation transition", fmt.Sprint(row.Id), fmt.Errorf("occurrence time precedes prior transition"))
			}
		}
		transitions = append(transitions, transition)
	}
	return transitions, nil
}

// validateOperationHistory proves the materialized operation is exactly explained by its append-only history.
func validateOperationHistory(record OperationRecord, history []OperationTransition) error {
	key := string(record.Operation.ID)
	if len(history) == 0 {
		return corruptStateError("operation", key, fmt.Errorf("transition history must not be empty"))
	}
	if !history[0].OccurredAt.Equal(record.Operation.RequestedAt) {
		return corruptStateError("operation", key, fmt.Errorf("requested time does not match initial transition"))
	}

	var startedAt *time.Time
	for _, transition := range history {
		if startedAt == nil && transition.State == domain.OperationRunning {
			startedAt = copyTimePointer(&transition.OccurredAt)
		}
	}
	last := history[len(history)-1]
	if last.State != record.Operation.State {
		return corruptStateError("operation", key, fmt.Errorf("state does not match latest transition"))
	}
	if last.Sequence != record.Revision {
		return corruptStateError("operation", key, fmt.Errorf("revision does not match latest transition sequence"))
	}
	if last.Phase != record.Operation.Phase {
		return corruptStateError("operation", key, fmt.Errorf("phase does not match latest transition"))
	}
	if !operationProblemsEqual(last.Problem, record.Operation.Problem) {
		return corruptStateError("operation", key, fmt.Errorf("problem does not match latest transition"))
	}
	if !operationTimesEqual(startedAt, record.Operation.StartedAt) {
		return corruptStateError("operation", key, fmt.Errorf("start time does not match transition history"))
	}

	var finishedAt *time.Time
	if last.State.IsTerminal() {
		finishedAt = copyTimePointer(&last.OccurredAt)
	}
	if !operationTimesEqual(finishedAt, record.Operation.FinishedAt) {
		return corruptStateError("operation", key, fmt.Errorf("finish time does not match transition history"))
	}
	return nil
}

// operationProblemsEqual compares optional failure details by value instead of pointer identity.
func operationProblemsEqual(left, right *domain.Problem) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// operationTimesEqual compares optional lifecycle instants by value instead of pointer identity.
func operationTimesEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

// operationUpdateColumns includes nullable fields explicitly so state transitions can clear stale values safely.
func operationUpdateColumns(row models.Operation) map[string]any {
	return map[string]any{
		"state":             row.State,
		"phase":             row.Phase,
		"problem_code":      row.ProblemCode,
		"problem_message":   row.ProblemMessage,
		"problem_retryable": row.ProblemRetryable,
		"started_at":        row.StartedAt,
		"finished_at":       row.FinishedAt,
		"revision":          row.Revision,
	}
}

// normalizeContext gives public journal methods a usable cancellation boundary when callers pass nil.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
