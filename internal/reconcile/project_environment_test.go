package reconcile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectenvironment"
	"github.com/goforj/harbor/internal/state"
)

// projectEnvironmentTestState supplies one checkout while the embedded store satisfies unused lifecycle edges.
type projectEnvironmentTestState struct {
	*state.Store
	project state.ProjectRecord
}

// Project returns the checkout selected by the environment request.
func (source *projectEnvironmentTestState) Project(context.Context, domain.ProjectID) (state.ProjectRecord, error) {
	return source.project, nil
}

// TestSaveProjectEnvironmentFilePersistsTheRepositoryContract proves configuration files bypass runtime providers.
func TestSaveProjectEnvironmentFilePersistsTheRepositoryContract(t *testing.T) {
	checkoutRoot := t.TempDir()
	projectID := domain.ProjectID("project-environment")
	coordinator := &ProjectLifecycleCoordinator{
		state: &projectEnvironmentTestState{
			Store: &state.Store{},
			project: state.ProjectRecord{
				Project: domain.ProjectSnapshot{
					ID:   projectID,
					Path: checkoutRoot,
				},
			},
		},
	}
	contents := "version: 1\n\nenvironment:\n  SEARCH_HOST:\n    from: project.address\n"

	saved, err := coordinator.SaveProjectEnvironmentFile(t.Context(), ProjectEnvironmentFileSaveRequest{
		ProjectID: projectID,
		Name:      projectenvironment.Filename,
		Contents:  contents,
	})
	if err != nil {
		t.Fatalf("SaveProjectEnvironmentFile() error = %v", err)
	}
	if saved.Name != projectenvironment.Filename || saved.Contents != contents || len(saved.Revision) != 64 {
		t.Fatalf("saved file = %#v, want revisioned repository configuration", saved)
	}
	onDisk, err := os.ReadFile(filepath.Join(checkoutRoot, projectenvironment.Filename))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", projectenvironment.Filename, err)
	}
	if string(onDisk) != contents {
		t.Fatalf("saved contents = %q, want %q", onDisk, contents)
	}

	_, err = coordinator.SaveProjectEnvironmentFile(t.Context(), ProjectEnvironmentFileSaveRequest{
		ProjectID: projectID,
		Name:      projectenvironment.Filename,
		Contents:  "version: 1\n\nenvironment: {}\n",
	})
	if !errors.Is(err, projectenvironment.ErrConfigurationChanged) {
		t.Fatalf("stale SaveProjectEnvironmentFile() error = %v, want %v", err, projectenvironment.ErrConfigurationChanged)
	}
}
