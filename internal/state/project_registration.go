package state

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// ProjectRegistration records whether a durable registration was created or replayed.
type ProjectRegistration struct {
	Record  ProjectRecord
	Created bool
}

// RegisterProject atomically creates one stopped project or replays its exact canonical-path registration.
func (store *Store) RegisterProject(ctx context.Context, project domain.ProjectSnapshot) (ProjectRegistration, error) {
	ctx = normalizeContext(ctx)
	if err := project.Validate(); err != nil {
		return ProjectRegistration{}, err
	}
	project = canonicalProjectForMutation(project)
	if project.State != domain.ProjectStopped || project.Favorite || len(project.Apps) != 0 || len(project.Services) != 0 || len(project.Resources) != 0 {
		return ProjectRegistration{}, fmt.Errorf("initial project registration must be stopped, not favorite, and contain no runtime entities or resources")
	}
	if err := ctx.Err(); err != nil {
		return ProjectRegistration{}, err
	}

	var result ProjectRegistration
	err := store.mutations.mutate(ctx, "project registration", func(tx *gorm.DB) error {
		matches, err := readProjectRegistrationMatches(tx, project)
		if err != nil {
			return err
		}
		if replay, found, err := resolveProjectRegistration(matches, project); err != nil {
			return err
		} else if found {
			boundary, err := readProjectNetworkReleaseBoundary(tx, replay.Project.ID)
			if err != nil {
				return err
			}
			if err := rejectProjectNetworkReleaseMutation(boundary, replay.Project.ID, "register project"); err != nil {
				return err
			}
			result = ProjectRegistration{Record: replay, Created: false}
			return nil
		}
		boundary, err := readProjectNetworkReleaseBoundary(tx, project.ID)
		if err != nil {
			return err
		}
		if err := rejectProjectNetworkReleaseMutation(boundary, project.ID, "register project"); err != nil {
			return err
		}

		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		projectRow, appRows, serviceRows, resourceRows, err := projectModelsFromDomain(project, sequence)
		if err != nil {
			return err
		}
		if err := putProjectAggregate(tx, projectRow, appRows, serviceRows, resourceRows); err != nil {
			return err
		}
		readback, err := readProjectRecord(tx, project.ID)
		if err != nil {
			return fmt.Errorf("read project after registration: %w", err)
		}
		if readback.Revision != sequence || !reflect.DeepEqual(readback.Project, project) {
			return corruptStateError("project", string(project.ID), fmt.Errorf("registration readback differs from the committed project"))
		}
		if err := validateProjectSequenceOwner(tx, readback); err != nil {
			return err
		}
		result = ProjectRegistration{Record: readback, Created: true}
		return nil
	})
	if err != nil {
		return ProjectRegistration{}, fmt.Errorf("register project %q: %w", project.Path, err)
	}
	return result, nil
}

// readProjectRegistrationMatches loads every natural-identity collision before any sequence is allocated.
func readProjectRegistrationMatches(tx *gorm.DB, requested domain.ProjectSnapshot) ([]ProjectRecord, error) {
	var rows []models.Project
	var query *gorm.DB
	if registrationPathNeedsFilesystemIdentity() {
		query = tx.Model(&models.Project{})
	} else {
		query = tx.Where("project_id = ? OR slug = ? OR path = ?", string(requested.ID), requested.Slug, requested.Path)
	}
	if err := query.Order("project_id ASC").Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read project registration matches: %w", err)
	}
	records := make([]ProjectRecord, 0, len(rows))
	seen := make(map[domain.ProjectID]struct{}, len(rows))
	for _, row := range rows {
		projectID := domain.ProjectID(row.ProjectId)
		if _, exists := seen[projectID]; exists {
			return nil, corruptStateError("project", string(projectID), fmt.Errorf("project ID is duplicated"))
		}
		seen[projectID] = struct{}{}
		record, err := readProjectRecord(tx, projectID)
		if err != nil {
			return nil, err
		}
		if err := validateProjectSequenceOwner(tx, record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// registrationPathNeedsFilesystemIdentity avoids assuming that SQL text folding matches native path equivalence.
func registrationPathNeedsFilesystemIdentity() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

// resolveProjectRegistration distinguishes a safe replay from path, identity, and DNS-label conflicts.
func resolveProjectRegistration(matches []ProjectRecord, requested domain.ProjectSnapshot) (ProjectRecord, bool, error) {
	var pathMatches []ProjectRecord
	for _, match := range matches {
		if sameRegisteredPath(match.Project.Path, requested.Path) {
			pathMatches = append(pathMatches, match)
		}
	}
	if len(pathMatches) > 1 {
		return ProjectRecord{}, false, corruptStateError("project", requested.Path, fmt.Errorf("canonical path is owned by multiple projects"))
	}
	if len(pathMatches) == 1 {
		return pathMatches[0], true, nil
	}
	for _, match := range matches {
		if match.Project.ID == requested.ID {
			return ProjectRecord{}, false, newProjectRegistrationConflict(ProjectRegistrationConflictIdentity, requested, match.Project)
		}
	}
	for _, match := range matches {
		if match.Project.Slug == requested.Slug {
			return ProjectRecord{}, false, newProjectRegistrationConflict(ProjectRegistrationConflictSlug, requested, match.Project)
		}
	}
	return ProjectRecord{}, false, nil
}

// sameRegisteredPath follows the host filesystem's case policy for canonical checkout identities.
func sameRegisteredPath(left string, right string) bool {
	if left == right {
		return true
	}
	if !registrationPathNeedsFilesystemIdentity() {
		return false
	}
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false
	}
	return os.SameFile(leftInfo, rightInfo)
}
