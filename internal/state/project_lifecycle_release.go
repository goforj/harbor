package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// ReleaseUnavailableProjectSessionRequest identifies a receipt-free quarantined session that must not block a replacement start.
type ReleaseUnavailableProjectSessionRequest struct {
	ProjectID domain.ProjectID
	At        time.Time
}

// ReleaseUnavailableProjectSession retires only a Harbor-owned receipt-free quarantine and projects the project to stopped.
func (store *Store) ReleaseUnavailableProjectSession(
	ctx context.Context,
	request ReleaseUnavailableProjectSessionRequest,
) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateReleaseUnavailableProjectSessionRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	var result ProjectRecord
	err := store.mutations.mutate(ctx, "release receipt-free unavailable project session", func(tx *gorm.DB) error {
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, ""); err != nil {
			return err
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectUnavailable {
			return fmt.Errorf("project %q must be unavailable before releasing its receipt-free session, got %q", request.ProjectID, project.Project.State)
		}
		operation, history, err := readRetainedProjectRuntimeRecoveryOperation(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if err := validateProcessBackedProjectRuntimeRecoveryOperation(project, operation); err != nil {
			return err
		}
		if err := validateProcessBackedProjectRuntimeRecoveryHistory(project, operation, history); err != nil {
			return err
		}

		row, generation, err := readReceiptFreeUnavailableSession(tx, request.ProjectID)
		if err != nil {
			return err
		}
		at := request.At
		if at.Before(project.Project.UpdatedAt) {
			at = project.Project.UpdatedAt
		}
		if at.Before(row.UpdatedAt) {
			at = row.UpdatedAt
		}
		deleted := tx.Where(
			`id = ? AND session_id = ? AND project_id = ? AND generation = ? AND owner = ?
			 AND state IN ? AND pid IS NULL AND birth_token IS NULL AND executable_identity IS NULL AND argument_digest IS NULL`,
			row.Id,
			row.SessionId,
			row.ProjectId,
			int(generation),
			string(domain.SessionOwnerHarbor),
			[]string{string(domain.SessionPlanned), string(domain.SessionAwaitingAttach)},
		).Delete(&models.ProjectSession{})
		if deleted.Error != nil {
			return fmt.Errorf("delete receipt-free unavailable project session: %w", deleted.Error)
		}
		if deleted.RowsAffected != 1 {
			return fmt.Errorf("project %q receipt-free session changed before replacement start", request.ProjectID)
		}
		persisted, err := persistLifecycleProject(tx, stoppedProjectProjection(project.Project, at))
		if err != nil {
			return err
		}
		result = persisted
		return nil
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("release project %q receipt-free unavailable session: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateReleaseUnavailableProjectSessionRequest rejects release requests without one project and one chronological fence.
func validateReleaseUnavailableProjectSessionRequest(request ReleaseUnavailableProjectSessionRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return validateStoredTime("receipt-free unavailable project session release time", request.At)
}

// readReceiptFreeUnavailableSession returns the only unresolved Harbor session shape eligible for automatic replacement start.
func readReceiptFreeUnavailableSession(tx *gorm.DB, projectID domain.ProjectID) (models.ProjectSession, uint64, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.ProjectSession{}, 0, fmt.Errorf("read receipt-free unavailable project session: %w", err)
	}
	if len(rows) == 0 {
		return models.ProjectSession{}, 0, &ProjectSessionNotFoundError{ProjectID: projectID}
	}
	if len(rows) != 1 {
		return models.ProjectSession{}, 0, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	row := rows[0]
	_, err := projectSessionFromModel(row)
	var missing *ProjectSessionProcessEvidenceMissingError
	if err == nil {
		if row.Pid.Valid || row.BirthToken.Valid || row.ExecutableIdentity.Valid || row.ArgumentDigest.Valid {
			return models.ProjectSession{}, 0, fmt.Errorf("session %q retains exact process evidence", row.SessionId)
		}
		if row.Owner != string(domain.SessionOwnerHarbor) ||
			(row.State != string(domain.SessionPlanned) && row.State != string(domain.SessionAwaitingAttach)) {
			return models.ProjectSession{}, 0, fmt.Errorf("session %q is not a Harbor-owned receipt-free launch boundary", row.SessionId)
		}
		return row, uint64(row.Generation), nil
	}
	if !errors.As(err, &missing) {
		return models.ProjectSession{}, 0, err
	}
	if missing.Owner != domain.SessionOwnerHarbor ||
		(missing.State != domain.SessionPlanned && missing.State != domain.SessionAwaitingAttach) {
		return models.ProjectSession{}, 0, fmt.Errorf("session %q is not a Harbor-owned receipt-free launch boundary", row.SessionId)
	}
	return row, missing.Generation, nil
}
