package devbootstrap

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"

	"github.com/goforj/harbor/internal/platform/helperpath"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

const (
	gatewayDirectoryMode  uint32 = 0o711
	privateDirectoryMode  uint32 = 0o700
	ancestorDirectoryMode uint32 = 0o755
)

var (
	// ErrRootRequired identifies an attempt to provision privileged development state without an existing root identity.
	ErrRootRequired = errors.New("development bootstrap requires effective UID 0")
	// ErrUnsafeObject identifies a preexisting filesystem object outside Harbor's exact development policy.
	ErrUnsafeObject = errors.New("unsafe preexisting development bootstrap object")
	// ErrDurabilityUncertain identifies a completed filesystem transition whose persistence could not be confirmed.
	ErrDurabilityUncertain = errors.New("development bootstrap durability is uncertain")
)

// Config contains the only caller-selected inputs accepted by the development bootstrap.
type Config struct {
	HelperSource string
	UserID       uint32
	GroupID      uint32
}

// directoryPlan describes one exact installer-style directory without granting callers destination authority.
type directoryPlan struct {
	path string
	mode uint32
	uid  uint32
	gid  uint32
}

// plan freezes every fixed destination and platform policy before privileged filesystem access begins.
type plan struct {
	helperSource      string
	helperDestination string
	helperMode        uint32
	helperUID         uint32
	helperGID         uint32
	directories       []directoryPlan
}

// dependencies keeps root admission and compiled destination lookup directly testable.
type dependencies struct {
	effectiveUID      func() int
	resolvePaths      func() (machinepaths.Paths, error)
	helperDestination func() string
	apply             func(plan) error
}

// Bootstrap provisions the fixed machine topology and installs one already-built helper for source development.
func Bootstrap(config Config) error {
	return bootstrap(config, productionDependencies())
}

// bootstrap admits root before resolving or touching any privileged destination.
func bootstrap(config Config, dependencies dependencies) error {
	if dependencies.effectiveUID() != 0 {
		return ErrRootRequired
	}
	paths, err := dependencies.resolvePaths()
	if err != nil {
		return fmt.Errorf("resolve development bootstrap topology: %w", err)
	}
	prepared, err := buildPlan(config, paths, dependencies.helperDestination(), runtime.GOOS)
	if err != nil {
		return err
	}
	if err := dependencies.apply(prepared); err != nil {
		return fmt.Errorf("apply development bootstrap: %w", err)
	}
	return nil
}

// productionDependencies binds privileged writes only to compiled platform destinations and native operations.
func productionDependencies() dependencies {
	return dependencies{
		effectiveUID:      platformEffectiveUID,
		resolvePaths:      machinepaths.Resolve,
		helperDestination: helperpath.Executable,
		apply:             applyPlatformPlan,
	}
}

// buildPlan validates caller identities and derives the complete exact filesystem policy as pure data.
func buildPlan(config Config, paths machinepaths.Paths, destination string, platform string) (plan, error) {
	if err := validateAbsoluteCleanPath("helper source", config.HelperSource); err != nil {
		return plan{}, err
	}
	if config.UserID == 0 {
		return plan{}, errors.New("development bootstrap user ID must be non-root")
	}
	if config.UserID == math.MaxUint32 {
		return plan{}, errors.New("development bootstrap user ID is reserved by chown")
	}
	if config.GroupID == math.MaxUint32 {
		return plan{}, errors.New("development bootstrap group ID is reserved by chown")
	}
	if err := validateMachinePaths(paths); err != nil {
		return plan{}, err
	}
	if err := validateAbsoluteCleanPath("helper destination", destination); err != nil {
		return plan{}, err
	}

	helperMode := uint32(0)
	switch platform {
	case "linux":
		helperMode = 0o755
	case "darwin":
		helperMode = 0o4755
	default:
		return plan{}, fmt.Errorf("development bootstrap is unsupported on %s", platform)
	}

	return plan{
		helperSource:      config.HelperSource,
		helperDestination: destination,
		helperMode:        helperMode,
		helperUID:         0,
		helperGID:         0,
		directories: []directoryPlan{
			{path: paths.Root, mode: gatewayDirectoryMode, uid: 0, gid: 0},
			{path: paths.TicketsDirectory, mode: gatewayDirectoryMode, uid: 0, gid: 0},
			{path: paths.PendingDirectory, mode: privateDirectoryMode, uid: config.UserID, gid: config.GroupID},
			{path: paths.ClaimsDirectory, mode: privateDirectoryMode, uid: 0, gid: 0},
			{path: paths.StateDirectory, mode: privateDirectoryMode, uid: 0, gid: 0},
			{path: paths.ReplayDirectory, mode: privateDirectoryMode, uid: 0, gid: 0},
		},
	}, nil
}

// validateMachinePaths ensures private construction cannot redirect any descendant away from one fixed-shape root.
func validateMachinePaths(paths machinepaths.Paths) error {
	if err := validateAbsoluteCleanPath("machine root", paths.Root); err != nil {
		return err
	}
	values := []struct {
		name string
		got  string
		want string
	}{
		{name: "state directory", got: paths.StateDirectory, want: filepath.Join(paths.Root, "state")},
		{name: "ownership path", got: paths.OwnershipPath, want: filepath.Join(paths.Root, "state", "ownership.json")},
		{name: "host projection path", got: paths.HostProjectionPath, want: filepath.Join(paths.Root, "state", "host-projection.json")},
		{name: "replay directory", got: paths.ReplayDirectory, want: filepath.Join(paths.Root, "state", "replay")},
		{name: "tickets directory", got: paths.TicketsDirectory, want: filepath.Join(paths.Root, "tickets")},
		{name: "pending directory", got: paths.PendingDirectory, want: filepath.Join(paths.Root, "tickets", "pending")},
		{name: "claims directory", got: paths.ClaimsDirectory, want: filepath.Join(paths.Root, "tickets", "claims")},
	}
	for _, value := range values {
		if value.got != value.want {
			return fmt.Errorf("development bootstrap %s is %q, want fixed path %q", value.name, value.got, value.want)
		}
		if err := validateAbsoluteCleanPath(value.name, value.got); err != nil {
			return err
		}
	}
	return nil
}

// validateAbsoluteCleanPath rejects ambient working-directory semantics and ambiguous privileged path spellings.
func validateAbsoluteCleanPath(name string, path string) error {
	if path == "" {
		return fmt.Errorf("development bootstrap %s is empty", name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("development bootstrap %s %q is not absolute", name, path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("development bootstrap %s %q is not canonical", name, path)
	}
	return nil
}
