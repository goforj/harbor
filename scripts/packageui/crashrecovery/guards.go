package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/domain"
)

// workerGuard confines destructive recovery proof to the repository's disposable hosted worker.
func workerGuard(environment func(string) string) error {
	if environment("GITHUB_ACTIONS") != "true" || environment("GITHUB_REPOSITORY") != "goforj/harbor" || environment("RUNNER_ENVIRONMENT") != "github-hosted" {
		return errors.New("recovery requires a goforj/harbor GitHub-hosted disposable worker")
	}
	return nil
}

// fixtureRoot requires the caller's exact canonical package-test checkout rather than accepting aliases.
func fixtureRoot(directory string) (string, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", errors.New("canonical absolute project directory required")
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	if root != directory {
		return "", errors.New("project directory is not canonical")
	}
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(root)
	if filepath.Base(root) != "project" || filepath.Dir(parent) != temporary || !strings.HasPrefix(filepath.Base(parent), "harbor-package-ui-") || filepath.Base(parent) == "harbor-package-ui-" {
		return "", errors.New("project is outside the disposable package-test boundary")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("project is not a directory")
	}
	return root, nil
}

// initialProject admits precisely the selected Ready checkout and configured host networking.
func initialProject(snapshot domain.Snapshot, id domain.ProjectID, directory string) (domain.ProjectSnapshot, error) {
	if len(snapshot.Projects) != 1 || snapshot.NetworkSetup == nil || !snapshot.NetworkSetup.Configured {
		return domain.ProjectSnapshot{}, errors.New("initial snapshot must contain one project and configured networking")
	}
	project := snapshot.Projects[0]
	if project.ID != id || project.Path != directory || project.State != domain.ProjectReady {
		return domain.ProjectSnapshot{}, errors.New("initial project differs from the selected Ready fixture")
	}
	return project, nil
}
