// Package networkprerequisite repairs source-development networking prerequisites through a bounded native approval flow.
package networkprerequisite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
)

var (
	// ErrUnavailable reports that this desktop build cannot install missing privileged networking support.
	ErrUnavailable = errors.New("privileged networking repair is unavailable in this desktop build")
	// ErrDeclined reports that the user dismissed native installation consent before the bootstrap ran.
	ErrDeclined = errors.New("privileged networking installation was declined")
	// ErrFailed reports that native installation did not establish the required source-development topology.
	ErrFailed = errors.New("privileged networking installation failed")
)

const (
	developmentArtifactDirectory = "devtools"
	developmentBootstrapName     = "devbootstrap"
	developmentHelperName        = "helper"
)

// Ensurer establishes the fixed privileged networking prerequisite after the daemon reports that it is missing.
type Ensurer interface {
	// Ensure requests native approval only for the fixed source-development bootstrap contract.
	Ensure(context.Context) error
}

// sourceEnsurerDependencies keeps source discovery, artifact admission, and native elevation independently testable.
type sourceEnsurerDependencies struct {
	executable              func() (string, error)
	effectiveUID            func() int
	effectiveGID            func() int
	platformDirectoryExists func(string) (bool, error)
	inspect                 func(string, uint32, uint32) error
	elevate                 func(context.Context, sourceBootstrapRequest) error
}

// sourceEnsurer owns the development-only handoff from an unelevated desktop to the bespoke bootstrap binary.
type sourceEnsurer struct {
	dependencies sourceEnsurerDependencies
}

// sourceBootstrapRequest contains only paths derived from the running development binary and its native user identity.
type sourceBootstrapRequest struct {
	bootstrapPath string
	helperPath    string
	userID        uint32
	groupID       uint32
}

// unavailableEnsurer preserves the packaged-app installer boundary instead of discovering executable content at runtime.
type unavailableEnsurer struct{}

// New creates the platform implementation selected by the desktop build mode.
func New() Ensurer {
	return newPlatformEnsurer()
}

// newSourceEnsurer creates a development repair boundary from complete native dependencies.
func newSourceEnsurer(dependencies sourceEnsurerDependencies) Ensurer {
	if dependencies.executable == nil || dependencies.effectiveUID == nil || dependencies.effectiveGID == nil ||
		dependencies.platformDirectoryExists == nil || dependencies.inspect == nil || dependencies.elevate == nil {
		panic("networkprerequisite source ensurer requires every dependency")
	}

	return &sourceEnsurer{dependencies: dependencies}
}

// Ensure derives both artifacts from the running desktop and never accepts a caller-selected executable or destination.
func (ensurer *sourceEnsurer) Ensure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	executable, err := ensurer.dependencies.executable()
	if err != nil {
		return fmt.Errorf("locate Harbor development desktop: %w", err)
	}
	executable, err = canonicalExecutablePath(executable)
	if err != nil {
		return err
	}

	userID, err := canonicalNativeID("user", ensurer.dependencies.effectiveUID(), false)
	if err != nil {
		return err
	}
	groupID, err := canonicalNativeID("group", ensurer.dependencies.effectiveGID(), true)
	if err != nil {
		return err
	}

	artifactParent, err := developmentArtifactParent(executable)
	if err != nil {
		return err
	}
	artifactRoot := filepath.Join(artifactParent, developmentArtifactDirectory)
	artifactDirectory := filepath.Join(artifactRoot, developmentArtifactRuntimeDirectory(runtime.GOOS, runtime.GOARCH))
	platformDirectoryExists, err := ensurer.dependencies.platformDirectoryExists(artifactDirectory)
	if err != nil {
		return fmt.Errorf("inspect Harbor development artifact directory: %w", err)
	}
	if !platformDirectoryExists {
		artifactDirectory = artifactRoot
	}
	request := sourceBootstrapRequest{
		bootstrapPath: filepath.Join(artifactDirectory, developmentBootstrapName),
		helperPath:    filepath.Join(artifactDirectory, developmentHelperName),
		userID:        userID,
		groupID:       groupID,
	}
	for _, artifact := range []string{request.bootstrapPath, request.helperPath} {
		if err := ensurer.dependencies.inspect(artifact, userID, groupID); err != nil {
			return fmt.Errorf("admit Harbor development networking artifact: %w", err)
		}
	}

	if err := ensurer.dependencies.elevate(ctx, request); err != nil {
		return err
	}
	return nil
}

// developmentArtifactRuntimeDirectory keeps desktop discovery aligned with the cross-platform Wails build hook.
func developmentArtifactRuntimeDirectory(goos string, goarch string) string {
	return goos + "-" + goarch
}

// developmentArtifactParent recognizes Wails' exact macOS application bundle and raw development binary layouts.
func developmentArtifactParent(executable string) (string, error) {
	directory := filepath.Dir(executable)
	if filepath.Base(directory) != "MacOS" {
		return directory, nil
	}

	contents := filepath.Dir(directory)
	if filepath.Base(contents) != "Contents" {
		return "", fmt.Errorf("Harbor development macOS executable %q has a malformed application bundle", executable)
	}
	application := filepath.Dir(contents)
	name := filepath.Base(application)
	if filepath.Ext(name) != ".app" || name == ".app" {
		return "", fmt.Errorf("Harbor development macOS executable %q has a malformed application bundle", executable)
	}

	return filepath.Dir(application), nil
}

// Ensure keeps production and unsupported builds on their native installer or repair path.
func (unavailableEnsurer) Ensure(context.Context) error {
	return ErrUnavailable
}

// canonicalExecutablePath rejects relative and non-canonical development process locations before deriving privileged inputs.
func canonicalExecutablePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("Harbor development desktop executable path is empty")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("Harbor development desktop executable path %q is not absolute and canonical", path)
	}
	return path, nil
}

// canonicalNativeID prevents root desktops and platform sentinel identities from becoming bootstrap ownership inputs.
func canonicalNativeID(name string, value int, allowRoot bool) (uint32, error) {
	if value < 0 || uint64(value) >= math.MaxUint32 {
		return 0, fmt.Errorf("Harbor development desktop %s ID %d is invalid", name, value)
	}
	if value == 0 && !allowRoot {
		return 0, errors.New("Harbor development desktop must run as a non-root user")
	}
	return uint32(value), nil
}
