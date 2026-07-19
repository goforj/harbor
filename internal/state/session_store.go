package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// ActiveProjectSession returns the one durable session currently correlated with a registered project.
func (store *Store) ActiveProjectSession(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSession, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return domain.ProjectSession{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ProjectSession{}, err
	}
	builder, err := store.sessions.WithContext(ctx).Builder()
	if err != nil {
		return domain.ProjectSession{}, fmt.Errorf("open project session state: %w", err)
	}

	var session domain.ProjectSession
	if err := builder.Transaction(func(tx *gorm.DB) error {
		read, readErr := readActiveProjectSession(tx, projectID)
		if readErr != nil {
			return readErr
		}
		session = read
		return nil
	}); err != nil {
		return domain.ProjectSession{}, fmt.Errorf("read active session for project %q: %w", projectID, err)
	}
	return session, nil
}

// ProjectSession returns one durable session only when both caller-supplied identities correlate exactly.
func (store *Store) ProjectSession(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) (domain.ProjectSession, error) {
	if err := sessionID.Validate(); err != nil {
		return domain.ProjectSession{}, err
	}
	session, err := store.ActiveProjectSession(ctx, projectID)
	if err != nil {
		return domain.ProjectSession{}, err
	}
	if session.ID != sessionID {
		return domain.ProjectSession{}, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	return session, nil
}

// readActiveProjectSession loads one row while still detecting duplicates under a weakened or corrupt schema.
func readActiveProjectSession(tx *gorm.DB, projectID domain.ProjectID) (domain.ProjectSession, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return domain.ProjectSession{}, fmt.Errorf("read project session row: %w", err)
	}
	if len(rows) == 0 {
		return domain.ProjectSession{}, &ProjectSessionNotFoundError{ProjectID: projectID}
	}
	if len(rows) != 1 {
		return domain.ProjectSession{}, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	return projectSessionFromModel(rows[0])
}
