package networkprerequisite

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestSourceEnsurerDerivesExactSiblingArtifacts verifies no UI or daemon input can select an elevated executable.
func TestSourceEnsurerDerivesExactSiblingArtifacts(t *testing.T) {
	t.Parallel()

	var inspected []string
	var elevated sourceBootstrapRequest
	ensurer := newSourceEnsurer(sourceEnsurerDependencies{
		executable:              func() (string, error) { return "/workspace/harbor/desktop/build/bin/harbor-desktop", nil },
		effectiveUID:            func() int { return 501 },
		effectiveGID:            func() int { return 20 },
		platformDirectoryExists: func(string) (bool, error) { return true, nil },
		inspect: func(path string, userID uint32, groupID uint32) error {
			if userID != 501 || groupID != 20 {
				t.Fatalf("artifact identity = %d:%d, want 501:20", userID, groupID)
			}
			inspected = append(inspected, path)
			return nil
		},
		elevate: func(_ context.Context, request sourceBootstrapRequest) error {
			elevated = request
			return nil
		},
	})

	if err := ensurer.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	runtimeDirectory := developmentArtifactRuntimeDirectory(runtime.GOOS, runtime.GOARCH)
	wantPaths := []string{
		"/workspace/harbor/desktop/build/bin/devtools/" + runtimeDirectory + "/devbootstrap",
		"/workspace/harbor/desktop/build/bin/devtools/" + runtimeDirectory + "/helper",
	}
	if !reflect.DeepEqual(inspected, wantPaths) {
		t.Fatalf("inspected paths = %#v, want %#v", inspected, wantPaths)
	}
	if elevated.bootstrapPath != wantPaths[0] || elevated.helperPath != wantPaths[1] ||
		elevated.userID != 501 || elevated.groupID != 20 {
		t.Fatalf("elevated request = %#v", elevated)
	}
}

// TestSourceEnsurerRejectsUnsafeDiscoveryBeforeElevation covers every authority input admitted before native consent.
func TestSourceEnsurerRejectsUnsafeDiscoveryBeforeElevation(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("unsafe artifact")
	tests := []struct {
		name       string
		executable string
		uid        int
		gid        int
		inspectErr error
		want       string
	}{
		{name: "empty executable", uid: 501, gid: 20, want: "path is empty"},
		{name: "relative executable", executable: "build/bin/harbor-desktop", uid: 501, gid: 20, want: "not absolute"},
		{name: "noncanonical executable", executable: "/workspace/../harbor-desktop", uid: 501, gid: 20, want: "not absolute and canonical"},
		{name: "root desktop", executable: "/workspace/harbor-desktop", gid: 20, want: "non-root"},
		{name: "negative user", executable: "/workspace/harbor-desktop", uid: -1, gid: 20, want: "user ID"},
		{name: "negative group", executable: "/workspace/harbor-desktop", uid: 501, gid: -1, want: "group ID"},
		{name: "artifact", executable: "/workspace/harbor-desktop", uid: 501, gid: 20, inspectErr: sentinel, want: "unsafe artifact"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			elevated := false
			ensurer := newSourceEnsurer(sourceEnsurerDependencies{
				executable:              func() (string, error) { return test.executable, nil },
				effectiveUID:            func() int { return test.uid },
				effectiveGID:            func() int { return test.gid },
				platformDirectoryExists: func(string) (bool, error) { return true, nil },
				inspect:                 func(string, uint32, uint32) error { return test.inspectErr },
				elevate: func(context.Context, sourceBootstrapRequest) error {
					elevated = true
					return nil
				},
			})
			if err := ensurer.Ensure(t.Context()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want containing %q", err, test.want)
			}
			if elevated {
				t.Fatal("Ensure() elevated an unsafe development artifact")
			}
		})
	}
}

// TestSourceEnsurerFallsBackOnlyWhenThePlatformDirectoryIsAbsent keeps one bounded transition for existing Wails builds.
func TestSourceEnsurerFallsBackOnlyWhenThePlatformDirectoryIsAbsent(t *testing.T) {
	t.Parallel()

	var inspected []string
	var elevated sourceBootstrapRequest
	ensurer := newSourceEnsurer(sourceEnsurerDependencies{
		executable:              func() (string, error) { return "/workspace/harbor/desktop/build/bin/harbor-desktop", nil },
		effectiveUID:            func() int { return 501 },
		effectiveGID:            func() int { return 20 },
		platformDirectoryExists: func(string) (bool, error) { return false, nil },
		inspect: func(path string, _ uint32, _ uint32) error {
			inspected = append(inspected, path)
			return nil
		},
		elevate: func(_ context.Context, request sourceBootstrapRequest) error {
			elevated = request
			return nil
		},
	})

	if err := ensurer.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPaths := []string{
		"/workspace/harbor/desktop/build/bin/devtools/devbootstrap",
		"/workspace/harbor/desktop/build/bin/devtools/helper",
	}
	if !reflect.DeepEqual(inspected, wantPaths) {
		t.Fatalf("inspected paths = %#v, want %#v", inspected, wantPaths)
	}
	if elevated.bootstrapPath != wantPaths[0] || elevated.helperPath != wantPaths[1] {
		t.Fatalf("elevated request = %#v", elevated)
	}
}

// TestSourceEnsurerNeverFallsBackFromAnInvalidPlatformArtifact prevents stale legacy binaries from bypassing scoped admission.
func TestSourceEnsurerNeverFallsBackFromAnInvalidPlatformArtifact(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("wrong executable format")
	var inspected []string
	elevated := false
	ensurer := newSourceEnsurer(sourceEnsurerDependencies{
		executable:              func() (string, error) { return "/workspace/harbor/desktop/build/bin/harbor-desktop", nil },
		effectiveUID:            func() int { return 501 },
		effectiveGID:            func() int { return 20 },
		platformDirectoryExists: func(string) (bool, error) { return true, nil },
		inspect: func(path string, _ uint32, _ uint32) error {
			inspected = append(inspected, path)
			return sentinel
		},
		elevate: func(context.Context, sourceBootstrapRequest) error {
			elevated = true
			return nil
		},
	})

	err := ensurer.Ensure(t.Context())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Ensure() error = %v, want wrong executable format", err)
	}
	if len(inspected) != 1 || strings.HasSuffix(inspected[0], "/devtools/devbootstrap") {
		t.Fatalf("inspected paths = %#v, want only the scoped bootstrap", inspected)
	}
	if elevated {
		t.Fatal("Ensure() elevated after scoped artifact admission failed")
	}
}

// TestDevelopmentArtifactRuntimeDirectoryMatchesTheBuilderConvention prevents silent loader and hook divergence.
func TestDevelopmentArtifactRuntimeDirectoryMatchesTheBuilderConvention(t *testing.T) {
	t.Parallel()

	if got := developmentArtifactRuntimeDirectory("darwin", "arm64"); got != "darwin-arm64" {
		t.Fatalf("developmentArtifactRuntimeDirectory() = %q, want darwin-arm64", got)
	}
}

// TestDevelopmentArtifactParentRecognizesWailsBundlesAndRawBinaries pins source artifact discovery to reviewed layouts.
func TestDevelopmentArtifactParentRecognizesWailsBundlesAndRawBinaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		want       string
		wantErr    bool
	}{
		{
			name:       "Wails bundle",
			executable: "/workspace/harbor/desktop/build/bin/Harbor.app/Contents/MacOS/Harbor",
			want:       "/workspace/harbor/desktop/build/bin",
		},
		{
			name:       "raw binary",
			executable: "/workspace/harbor/desktop/build/bin/harbor-desktop",
			want:       "/workspace/harbor/desktop/build/bin",
		},
		{
			name:       "missing Contents",
			executable: "/workspace/harbor/desktop/build/bin/Harbor.app/Resources/MacOS/Harbor",
			wantErr:    true,
		},
		{
			name:       "missing app suffix",
			executable: "/workspace/harbor/desktop/build/bin/Harbor/Contents/MacOS/Harbor",
			wantErr:    true,
		},
		{
			name:       "empty app name",
			executable: "/workspace/harbor/desktop/build/bin/.app/Contents/MacOS/Harbor",
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := developmentArtifactParent(test.executable)
			if test.wantErr {
				if err == nil {
					t.Fatalf("developmentArtifactParent(%q) error = nil", test.executable)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("developmentArtifactParent(%q) = (%q, %v), want (%q, nil)", test.executable, got, err, test.want)
			}
		})
	}
}

// TestSourceEnsurerPreservesCancellationAndElevationFailures keeps ambiguous native outcomes visible to the desktop.
func TestSourceEnsurerPreservesCancellationAndElevationFailures(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ensurer := newTestSourceEnsurer(func(context.Context, sourceBootstrapRequest) error {
		t.Fatal("cancelled Ensure() elevated")
		return nil
	})
	if err := ensurer.Ensure(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure(cancelled) error = %v, want context cancellation", err)
	}

	ensurer = newTestSourceEnsurer(func(context.Context, sourceBootstrapRequest) error { return ErrDeclined })
	if err := ensurer.Ensure(t.Context()); !errors.Is(err, ErrDeclined) {
		t.Fatalf("Ensure(declined) error = %v, want ErrDeclined", err)
	}
}

// TestUnavailableEnsurerKeepsPackagedBuildsOnTheInstallerBoundary verifies production never discovers sibling tools.
func TestUnavailableEnsurerKeepsPackagedBuildsOnTheInstallerBoundary(t *testing.T) {
	t.Parallel()

	if err := (unavailableEnsurer{}).Ensure(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ensure() error = %v, want ErrUnavailable", err)
	}
}

// newTestSourceEnsurer creates one fully admitted source fixture around a selected native result.
func newTestSourceEnsurer(elevate func(context.Context, sourceBootstrapRequest) error) Ensurer {
	return newSourceEnsurer(sourceEnsurerDependencies{
		executable:              func() (string, error) { return "/workspace/harbor-desktop", nil },
		effectiveUID:            func() int { return 501 },
		effectiveGID:            func() int { return 20 },
		platformDirectoryExists: func(string) (bool, error) { return true, nil },
		inspect:                 func(string, uint32, uint32) error { return nil },
		elevate:                 elevate,
	})
}
