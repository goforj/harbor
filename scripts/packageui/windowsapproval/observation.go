package main

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// projectObservation retains bounded current activity and distinguishes failed settlement from Stopped.
type projectObservation struct {
	Project       domain.ProjectSnapshot  `json:"project"`
	Activity      control.ProjectActivity `json:"activity"`
	FailedStart   *domain.Operation       `json:"failed_start,omitempty"`
	SettledFailed bool                    `json:"settled_failed"`
}

// observeProject admits only the selected disposable checkout and never requests project or machine mutation.
func observeProject(snapshot domain.Snapshot, activity control.ProjectActivity, id domain.ProjectID, directory string) (projectObservation, error) {
	if err := id.Validate(); err != nil {
		return projectObservation{}, err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return projectObservation{}, errors.New("observation requires a canonical absolute fixture directory")
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].ID != id || !strings.EqualFold(snapshot.Projects[0].Path, directory) || activity.ProjectID != id {
		return projectObservation{}, errors.New("observation crossed the exact fixture project or checkout")
	}
	if err := activity.Validate(); err != nil {
		return projectObservation{}, err
	}
	result := projectObservation{Project: snapshot.Projects[0], Activity: activity}
	if result.Project.State != domain.ProjectFailed || activity.Session != nil {
		return result, nil
	}
	var latestStart *domain.Operation
	for _, operation := range snapshot.Operations {
		if operation.ProjectID != id {
			continue
		}
		if err := operation.Validate(); err != nil {
			return projectObservation{}, err
		}
		if !operation.State.IsTerminal() {
			return result, nil
		}
		if operation.Kind == domain.OperationKindProjectStart && (latestStart == nil || operation.RequestedAt.After(latestStart.RequestedAt)) {
			copy := operation
			latestStart = &copy
		}
	}
	if latestStart != nil && latestStart.State == domain.OperationFailed && latestStart.FinishedAt != nil {
		result.FailedStart = latestStart
		result.SettledFailed = true
	}
	return result, nil
}
