package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// ProjectOutputHistory selects diagnostic bytes without retaining process or broker authority.
type ProjectOutputHistory struct {
	ProjectID  domain.ProjectID `gorm:"column:project_id;primaryKey"`
	SessionID  domain.SessionID `gorm:"column:session_id"`
	Generation uint64           `gorm:"column:generation"`
	EndedAt    time.Time        `gorm:"column:ended_at"`
}

// TableName keeps diagnostic correlation separate from active lifecycle ownership.
func (ProjectOutputHistory) TableName() string { return "project_output_history" }

// RetainedProjectOutput returns only the registered project's most recent completed lifecycle.
func (store *Store) RetainedProjectOutput(ctx context.Context, projectID domain.ProjectID) (*ProjectOutputHistory, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return nil, err
	}
	var result *ProjectOutputHistory
	err = builder.Transaction(func(tx *gorm.DB) error {
		project, err := readLifecycleProject(tx, projectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectFailed && project.Project.State != domain.ProjectStopped {
			return nil
		}
		if _, err := readActiveProjectSession(tx, projectID); err == nil {
			return nil
		} else {
			var missing *ProjectSessionNotFoundError
			if !errors.As(err, &missing) {
				return err
			}
		}
		var history ProjectOutputHistory
		if err := tx.Where("project_id = ?", string(projectID)).Take(&history).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if history.ProjectID != projectID {
			return errors.New("retained output project identity mismatch")
		}
		if err := history.SessionID.Validate(); err != nil {
			return err
		}
		if history.Generation == 0 || history.Generation > uint64(domain.MaximumSequence) {
			return errors.New("retained output generation is invalid")
		}
		if history.EndedAt.IsZero() {
			return errors.New("retained output completion time is missing")
		}
		if !history.EndedAt.Equal(project.Project.UpdatedAt) {
			return nil
		}
		result = &history
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read retained project output: %w", err)
	}
	return result, nil
}
