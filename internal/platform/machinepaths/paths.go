package machinepaths

import (
	"errors"
	"fmt"
	"path/filepath"
)

const (
	stateDirectoryName                = "state"
	ownershipFilename                 = "ownership.json"
	hostProjectionFilename            = "host-projection.json"
	replayDirectoryName               = "replay"
	ticketsDirectoryName              = "tickets"
	pendingDirectoryName              = "pending"
	claimsDirectoryName               = "claims"
	ownershipReleaseProofFilename     = "ownership-release-proof.json"
	ownershipReleaseProofLockFilename = "ownership-release-proof.lock"
)

// ErrUnsupported identifies operating systems without a reviewed machine-global path policy.
var ErrUnsupported = errors.New("machine-global privileged paths are unsupported")

// Paths contains every installer-provisioned path shared by Harbor's daemon and privileged helper.
type Paths struct {
	Root                          string
	StateDirectory                string
	OwnershipPath                 string
	HostProjectionPath            string
	ReplayDirectory               string
	TicketsDirectory              string
	PendingDirectory              string
	ClaimsDirectory               string
	OwnershipReleaseProofPath     string
	OwnershipReleaseProofLockPath string
}

// rootLookup keeps platform resolution failures testable without making the production root configurable.
type rootLookup func() (string, error)

// Resolve returns Harbor's validated, fixed machine-global privileged layout without modifying it.
func Resolve() (Paths, error) {
	return resolve(platformRoot)
}

// resolve preserves native lookup failures before deriving any privileged descendant.
func resolve(lookup rootLookup) (Paths, error) {
	root, err := lookup()
	if err != nil {
		return Paths{}, err
	}

	return buildPaths(root)
}

// buildPaths derives the complete graph at once so callers cannot independently choose privileged destinations.
func buildPaths(root string) (Paths, error) {
	paths := Paths{
		Root:                          root,
		StateDirectory:                filepath.Join(root, stateDirectoryName),
		OwnershipPath:                 filepath.Join(root, stateDirectoryName, ownershipFilename),
		HostProjectionPath:            filepath.Join(root, stateDirectoryName, hostProjectionFilename),
		ReplayDirectory:               filepath.Join(root, stateDirectoryName, replayDirectoryName),
		TicketsDirectory:              filepath.Join(root, ticketsDirectoryName),
		PendingDirectory:              filepath.Join(root, ticketsDirectoryName, pendingDirectoryName),
		ClaimsDirectory:               filepath.Join(root, ticketsDirectoryName, claimsDirectoryName),
		OwnershipReleaseProofPath:     filepath.Join(root, ownershipReleaseProofFilename),
		OwnershipReleaseProofLockPath: filepath.Join(root, ownershipReleaseProofLockFilename),
	}
	if err := validatePaths(paths, root); err != nil {
		return Paths{}, err
	}

	return paths, nil
}

// validatePaths rejects partial or redirected layouts before any path reaches an elevated storage boundary.
func validatePaths(paths Paths, root string) error {
	if err := validateAbsoluteCleanPath("platform root", root); err != nil {
		return err
	}

	expected := Paths{
		Root:                          root,
		StateDirectory:                filepath.Join(root, stateDirectoryName),
		OwnershipPath:                 filepath.Join(root, stateDirectoryName, ownershipFilename),
		HostProjectionPath:            filepath.Join(root, stateDirectoryName, hostProjectionFilename),
		ReplayDirectory:               filepath.Join(root, stateDirectoryName, replayDirectoryName),
		TicketsDirectory:              filepath.Join(root, ticketsDirectoryName),
		PendingDirectory:              filepath.Join(root, ticketsDirectoryName, pendingDirectoryName),
		ClaimsDirectory:               filepath.Join(root, ticketsDirectoryName, claimsDirectoryName),
		OwnershipReleaseProofPath:     filepath.Join(root, ownershipReleaseProofFilename),
		OwnershipReleaseProofLockPath: filepath.Join(root, ownershipReleaseProofLockFilename),
	}
	values := []struct {
		name string
		got  string
		want string
	}{
		{name: "root", got: paths.Root, want: expected.Root},
		{name: "state directory", got: paths.StateDirectory, want: expected.StateDirectory},
		{name: "ownership path", got: paths.OwnershipPath, want: expected.OwnershipPath},
		{name: "host projection path", got: paths.HostProjectionPath, want: expected.HostProjectionPath},
		{name: "replay directory", got: paths.ReplayDirectory, want: expected.ReplayDirectory},
		{name: "tickets directory", got: paths.TicketsDirectory, want: expected.TicketsDirectory},
		{name: "pending directory", got: paths.PendingDirectory, want: expected.PendingDirectory},
		{name: "claims directory", got: paths.ClaimsDirectory, want: expected.ClaimsDirectory},
		{name: "ownership release proof path", got: paths.OwnershipReleaseProofPath, want: expected.OwnershipReleaseProofPath},
		{name: "ownership release proof lock path", got: paths.OwnershipReleaseProofLockPath, want: expected.OwnershipReleaseProofLockPath},
	}
	for _, value := range values {
		if err := validateAbsoluteCleanPath(value.name, value.got); err != nil {
			return err
		}
		if value.got != value.want {
			return fmt.Errorf("machine-global %s is %q, want fixed path %q", value.name, value.got, value.want)
		}
	}

	return nil
}

// validateAbsoluteCleanPath prevents inherited working-directory semantics and ambiguous path spellings.
func validateAbsoluteCleanPath(name string, path string) error {
	if path == "" {
		return fmt.Errorf("machine-global %s is empty", name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("machine-global %s %q is not absolute", name, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("machine-global %s %q is not clean", name, path)
	}
	return nil
}
