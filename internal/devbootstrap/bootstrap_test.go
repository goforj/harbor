package devbootstrap

import (
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// TestBuildPlanPinsExactPlatformPolicies proves caller inputs cannot redirect destinations or broaden installed modes.
func TestBuildPlanPinsExactPlatformPolicies(t *testing.T) {
	paths := testMachinePaths(testAbsolutePath("fixed", "harbor"))
	configuration := Config{HelperSource: testAbsolutePath("build", "harbor-helper"), UserID: 501, GroupID: 20}
	destination := testAbsolutePath("fixed", "harbor-helper")

	linux, err := buildPlan(configuration, paths, destination, "linux")
	if err != nil {
		t.Fatalf("buildPlan(linux) error = %v", err)
	}
	if linux.helperSource != configuration.HelperSource || linux.helperDestination != destination || linux.helperMode != 0o755 ||
		linux.helperUID != 0 || linux.helperGID != 0 {
		t.Fatalf("buildPlan(linux) helper policy = %#v", linux)
	}

	darwin, err := buildPlan(configuration, paths, destination, "darwin")
	if err != nil {
		t.Fatalf("buildPlan(darwin) error = %v", err)
	}
	if darwin.helperMode != 0o4755 || darwin.helperUID != 0 || darwin.helperGID != 0 {
		t.Fatalf("buildPlan(darwin) helper policy = %#v", darwin)
	}

	wantDirectories := []directoryPlan{
		{path: paths.Root, mode: 0o711, uid: 0, gid: 0},
		{path: paths.TicketsDirectory, mode: 0o711, uid: 0, gid: 0},
		{path: paths.PendingDirectory, mode: 0o700, uid: 501, gid: 20},
		{path: paths.ClaimsDirectory, mode: 0o700, uid: 0, gid: 0},
		{path: paths.StateDirectory, mode: 0o700, uid: 0, gid: 0},
		{path: paths.ReplayDirectory, mode: 0o700, uid: 0, gid: 0},
	}
	if !reflect.DeepEqual(linux.directories, wantDirectories) || !reflect.DeepEqual(darwin.directories, wantDirectories) {
		t.Fatalf("buildPlan() directories = %#v / %#v, want %#v", linux.directories, darwin.directories, wantDirectories)
	}
}

// TestBuildPlanAllowsRootPrimaryGroup keeps pending admission aligned with the runtime's UID-only non-root rule.
func TestBuildPlanAllowsRootPrimaryGroup(t *testing.T) {
	paths := testMachinePaths(testAbsolutePath("fixed", "harbor"))
	prepared, err := buildPlan(
		Config{HelperSource: testAbsolutePath("build", "harbor-helper"), UserID: 501, GroupID: 0},
		paths,
		testAbsolutePath("fixed", "harbor-helper"),
		"linux",
	)
	if err != nil {
		t.Fatalf("buildPlan() root primary group error = %v", err)
	}
	if prepared.directories[2].gid != 0 {
		t.Fatalf("pending group = %d, want explicit 0", prepared.directories[2].gid)
	}
}

// TestBuildPlanRejectsRedirectedOrUnsupportedInputs covers every pure privileged-boundary validation branch.
func TestBuildPlanRejectsRedirectedOrUnsupportedInputs(t *testing.T) {
	root := testAbsolutePath("fixed", "harbor")
	validPaths := testMachinePaths(root)
	valid := Config{HelperSource: testAbsolutePath("build", "harbor-helper"), UserID: 501, GroupID: 20}
	destination := testAbsolutePath("fixed", "harbor-helper")
	uncleanSource := testAbsolutePath("build") + string(filepath.Separator) + ".." + string(filepath.Separator) + "build" + string(filepath.Separator) + "harbor-helper"
	tests := []struct {
		name          string
		configuration Config
		paths         machinepaths.Paths
		destination   string
		platform      string
	}{
		{name: "relative source", configuration: Config{HelperSource: "harbor-helper", UserID: 501, GroupID: 20}, paths: validPaths, destination: destination, platform: "linux"},
		{name: "unclean source", configuration: Config{HelperSource: uncleanSource, UserID: 501, GroupID: 20}, paths: validPaths, destination: destination, platform: "linux"},
		{name: "root user", configuration: Config{HelperSource: valid.HelperSource, UserID: 0, GroupID: 20}, paths: validPaths, destination: destination, platform: "linux"},
		{name: "reserved user", configuration: Config{HelperSource: valid.HelperSource, UserID: math.MaxUint32, GroupID: 20}, paths: validPaths, destination: destination, platform: "linux"},
		{name: "reserved group", configuration: Config{HelperSource: valid.HelperSource, UserID: 501, GroupID: math.MaxUint32}, paths: validPaths, destination: destination, platform: "linux"},
		{name: "redirected host projection", configuration: valid, paths: func() machinepaths.Paths { value := validPaths; value.HostProjectionPath += "-other"; return value }(), destination: destination, platform: "linux"},
		{name: "redirected replay", configuration: valid, paths: func() machinepaths.Paths { value := validPaths; value.ReplayDirectory += "-other"; return value }(), destination: destination, platform: "linux"},
		{name: "relative destination", configuration: valid, paths: validPaths, destination: "harbor-helper", platform: "linux"},
		{name: "unsupported platform", configuration: valid, paths: validPaths, destination: destination, platform: "plan9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildPlan(test.configuration, test.paths, test.destination, test.platform); err == nil {
				t.Fatal("buildPlan() accepted invalid privileged input")
			}
		})
	}
}

// TestBootstrapRejectsNonRootBeforeLookups proves failed admission reaches no path or mutation authority.
func TestBootstrapRejectsNonRootBeforeLookups(t *testing.T) {
	lookups := 0
	dependencies := dependencies{
		effectiveUID: func() int { return 501 },
		resolvePaths: func() (machinepaths.Paths, error) {
			lookups++
			return machinepaths.Paths{}, errors.New("must not run")
		},
		helperDestination: func() string {
			lookups++
			return ""
		},
		apply: func(plan) error {
			lookups++
			return nil
		},
	}
	if err := bootstrap(Config{}, dependencies); !errors.Is(err, ErrRootRequired) {
		t.Fatalf("bootstrap() error = %v, want ErrRootRequired", err)
	}
	if lookups != 0 {
		t.Fatalf("non-root bootstrap performed %d privileged lookups", lookups)
	}
}

// TestBootstrapPassesOnlyPreparedFixedPlan verifies the public workflow cannot add a destination after validation.
func TestBootstrapPassesOnlyPreparedFixedPlan(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("workflow is supported only on Unix development hosts")
	}
	paths := testMachinePaths(testAbsolutePath("fixed", "harbor"))
	configuration := Config{HelperSource: testAbsolutePath("build", "harbor-helper"), UserID: 501, GroupID: 20}
	destination := testAbsolutePath("fixed", "harbor-helper")
	var applied plan
	dependencies := dependencies{
		effectiveUID:      func() int { return 0 },
		resolvePaths:      func() (machinepaths.Paths, error) { return paths, nil },
		helperDestination: func() string { return destination },
		apply: func(prepared plan) error {
			applied = prepared
			return nil
		},
	}
	if err := bootstrap(configuration, dependencies); err != nil {
		t.Fatalf("bootstrap() error = %v", err)
	}
	if applied.helperSource != configuration.HelperSource || applied.helperDestination != destination || len(applied.directories) != 6 {
		t.Fatalf("bootstrap() applied plan = %#v", applied)
	}
}

// testMachinePaths returns one complete fixed-shape layout for pure planning tests.
func testMachinePaths(root string) machinepaths.Paths {
	return machinepaths.Paths{
		Root:               root,
		StateDirectory:     filepath.Join(root, "state"),
		OwnershipPath:      filepath.Join(root, "state", "ownership.json"),
		HostProjectionPath: filepath.Join(root, "state", "host-projection.json"),
		ReplayDirectory:    filepath.Join(root, "state", "replay"),
		TicketsDirectory:   filepath.Join(root, "tickets"),
		PendingDirectory:   filepath.Join(root, "tickets", "pending"),
		ClaimsDirectory:    filepath.Join(root, "tickets", "claims"),
	}
}

// testAbsolutePath constructs a host-native absolute path without requiring it to exist.
func testAbsolutePath(elements ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elements...)...)
}
