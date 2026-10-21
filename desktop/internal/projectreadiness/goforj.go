package projectreadiness

import (
	"context"
	"encoding/json"
	"errors"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
)

// GoForjCreation checks the authoring contract before reading a schema and the selected optional tools before creation.
func (checker *Checker) GoForjCreation(ctx context.Context, options map[string]string) Report {
	cli, _ := checker.goForjCLI()
	if cli.Status == "ready" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		schema, err := checker.schema(ctx)
		if err == nil {
			cli.Detail = "Graphical creation contract is available."
			cli.Version = safeVersion(schema.Version)
		}
		if err != nil {
			cli.Status, cli.Detail = "incompatible", "The installed GoForj CLI could not provide a supported creation schema. Update it and recheck."
			if ctx.Err() != nil {
				cli.Status, cli.Detail = "unavailable", "The GoForj CLI did not respond before the check timed out. Recheck when it is available."
			}
		}
	}
	components := strings.Split(options["components"], ",")
	containers := contains(components, "docker") || options["demo"] == "true"
	frontend := (contains(components, "web_ui") && contains([]string{"vue", "react", "templ_htmx"}, options["starter-kit"])) || options["demo"] == "true"
	checks := append([]Check{cli}, checker.inspect(ctx, requirements(frontend, containers, frontend))...)
	for index := range checks {
		checks[index].RequiredFor = "start"
		if checks[index].ID == "go" || checks[index].ID == "forj" {
			checks[index].RequiredFor = "creation"
		}
	}
	return Report{ProviderID: "goforj", Checks: checks}
}

// GoForjProject reads GoForj's static service intent and generated frontend metadata without starting the project.
func (checker *Checker) GoForjProject(ctx context.Context, project domain.ProjectSnapshot) Report {
	cli, executable := checker.goForjCLI()
	report := Report{ProviderID: "goforj", ProjectID: string(project.ID), Checks: []Check{cli}}
	containers := false
	if cli.Status == "ready" {
		observation, err := checker.observe(ctx, goforj.Query{Executable: executable, Checkout: project.Path, Environment: os.Environ()})
		if err != nil {
			report.Checks[0].Status, report.Checks[0].Detail = "unavailable", "GoForj could not describe this project. Check its configuration and CLI compatibility, then recheck."
		} else {
			report.Checks[0].Version = safeVersion(observation.GoForj.Version)
			report.Checks[0].Detail = "Project descriptor contract is available."
			for _, service := range observation.ServiceRequirements {
				containers = containers || service.Owner == goforj.ServiceRequirementOwnerCompose
			}
			if !observation.ServiceRequirementsSupported {
				for _, service := range project.Services {
					containers = containers || service.Kind == "compose"
				}
			}
		}
	}
	frontend, modern, metadata := checker.frontend(project.Path)
	report.Checks = append(report.Checks, metadata...)
	report.Checks = append(report.Checks, checker.inspect(ctx, requirements(frontend, containers, modern))...)
	checker.projectGoVersion(project.Path, &report)
	return report
}

// goForjCLI preserves managed-launch executable admission before invoking any GoForj contract.
func (checker *Checker) goForjCLI() (Check, string) {
	check := Check{ID: "forj", Name: "GoForj CLI", Status: "ready", Detail: "GoForj CLI is available.", Action: "Run go install github.com/goforj/goforj/cmd/forj@latest, then recheck. Reopen Harbor if PATH changed.", URL: "https://goforj.dev/getting-started/quickstart"}
	path, err := checker.lookPath("forj")
	if err != nil {
		check.Status, check.Detail = "missing", "GoForj CLI was not found in Harbor's PATH."
		if !errors.Is(err, exec.ErrNotFound) {
			check.Status, check.Detail = "unavailable", "GoForj CLI could not be resolved safely from Harbor's PATH."
		}
		return check, ""
	}
	path, err = filepath.Abs(path)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil || checker.verifyCLI(path) != nil {
		check.Status, check.Detail = "incompatible", "This executable is not a supported canonical GoForj CLI. Install GoForj and recheck."
	}
	return check, path
}

// frontend inspects only generated conventional package locations, not arbitrary scripts from project configuration.
func (checker *Checker) frontend(root string) (bool, bool, []Check) {
	frontend, modern := false, false
	for _, path := range []string{"frontend/package.json", "package.json"} {
		data, err := checker.readFile(root, filepath.FromSlash(path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var manifest struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err != nil || json.Unmarshal(data, &manifest) != nil {
			return false, false, []Check{{ID: "frontend-manifest", Name: "Frontend requirements", Status: "unavailable", Detail: "Harbor could not read the generated frontend package metadata.", Action: "Check that package.json is valid and stays inside the project, then recheck."}}
		}
		if manifest.Scripts["build"] == "" && manifest.Scripts["dev"] == "" {
			continue
		}
		frontend = true
		for _, dependencies := range []map[string]string{manifest.Dependencies, manifest.DevDependencies} {
			if match := numericVersion.FindStringSubmatch(dependencies["vite"]); len(match) == 2 {
				major, _ := strconv.Atoi(strings.Split(match[1], ".")[0])
				modern = modern || major >= 7
			}
		}
	}
	return frontend, modern, nil
}

// projectGoVersion explains native toolchain selection rather than falsely rejecting Go's automatic toolchains.
func (checker *Checker) projectGoVersion(root string, report *Report) {
	data, err := checker.readFile(root, "go.mod")
	if err != nil {
		report.Checks = append(report.Checks, Check{ID: "go-module", Name: "Go requirements", Status: "unavailable", Detail: "Harbor could not read this project's go.mod.", Action: "Restore a readable go.mod inside the project and recheck."})
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" && len(fields[1]) > 64 {
			report.Checks = append(report.Checks, Check{ID: "go-module", Name: "Go requirements", Status: "unavailable", Detail: "The project's Go version declaration is not supported.", Action: "Check the go directive in go.mod, then recheck."})
			return
		}
		if len(fields) < 2 || fields[0] != "go" || !version.IsValid("go"+fields[1]) {
			continue
		}
		for index, check := range report.Checks {
			if check.ID != "go" || check.Status != "ready" {
				continue
			}
			if version.Compare("go"+check.Version, "go"+fields[1]) < 0 {
				if os.Getenv("GOTOOLCHAIN") == "local" || version.Compare("go"+check.Version, "go1.21") < 0 {
					report.Checks[index].Status = "incompatible"
					report.Checks[index].Detail = "This project requires Go " + fields[1] + "; install that version or enable Go toolchain selection."
				} else {
					report.Checks[index].Detail = "Go is available; the project requests Go " + fields[1] + ". Go's configured toolchain selection applies at Start."
				}
			}
		}
		return
	}
}

// contains tests compiled-in option identities without accepting executable names from a schema.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// safeVersion limits CLI metadata to a parsed version rather than arbitrary process text.
func safeVersion(value string) string {
	if matches := numericVersion.FindStringSubmatch(value); len(matches) == 2 {
		return matches[1]
	}
	return ""
}
