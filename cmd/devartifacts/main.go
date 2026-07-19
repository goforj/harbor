// Package main builds Harbor's fixed source-development networking artifacts for Wails.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const artifactMode = os.FileMode(0o755)

// buildInvocation records one fixed root-module command without exposing destinations to callers.
type buildInvocation struct {
	packagePath string
	outputName  string
}

// commandRunner executes one Go build from the repository root.
type commandRunner func(context.Context, string, string, ...string) error

var artifactBuilds = []buildInvocation{
	{packagePath: "./cmd/helper", outputName: "helper"},
	{packagePath: "./cmd/devbootstrap", outputName: "devbootstrap"},
}

// main builds the adjacent tools before Wails compiles or recompiles the development desktop.
func main() {
	workingDirectory, err := os.Getwd()
	if err == nil {
		err = run(context.Background(), workingDirectory, os.Args[1:], runCommand)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "build Harbor development artifacts: %v\n", err)
		os.Exit(1)
	}
}

// run admits only Wails' fixed build directory and rejects caller-selected build inputs.
func run(ctx context.Context, workingDirectory string, arguments []string, runner commandRunner) error {
	if len(arguments) != 0 {
		return errors.New("arguments are not supported")
	}
	if runner == nil {
		panic("development artifact builder requires a command runner")
	}

	repositoryRoot, outputDirectory, err := developmentPaths(workingDirectory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return fmt.Errorf("create development artifact directory: %w", err)
	}

	for _, build := range artifactBuilds {
		outputPath := filepath.Join(outputDirectory, build.outputName)
		if err := runner(ctx, repositoryRoot, "go", "build", "-o", outputPath, build.packagePath); err != nil {
			return fmt.Errorf("build %s: %w", build.outputName, err)
		}
		if err := os.Chmod(outputPath, artifactMode); err != nil {
			return fmt.Errorf("set %s permissions: %w", build.outputName, err)
		}
	}
	return nil
}

// developmentPaths recognizes Wails' project and hook directories while keeping the output location fixed.
func developmentPaths(workingDirectory string) (string, string, error) {
	absolute, err := filepath.Abs(workingDirectory)
	if err != nil {
		return "", "", fmt.Errorf("resolve Wails build directory: %w", err)
	}
	absolute = filepath.Clean(absolute)

	desktopDirectory := absolute
	if filepath.Base(absolute) != "desktop" {
		buildDirectory := filepath.Dir(absolute)
		desktopDirectory = filepath.Dir(buildDirectory)
		if filepath.Base(absolute) != "bin" || filepath.Base(buildDirectory) != "build" || filepath.Base(desktopDirectory) != "desktop" {
			return "", "", fmt.Errorf("Wails hook directory %q is not desktop or desktop/build/bin", absolute)
		}
	}

	repositoryRoot := filepath.Dir(desktopDirectory)
	return repositoryRoot, filepath.Join(desktopDirectory, "build", "bin", "devtools"), nil
}

// runCommand preserves Go's diagnostics while fixing the command's working directory to the root module.
func runCommand(ctx context.Context, directory string, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
