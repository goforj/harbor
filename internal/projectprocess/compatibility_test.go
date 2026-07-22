package projectprocess

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestGoForjExecutableCompatibilityPolicy covers every accepted and rejected embedded-build shape.
func TestGoForjExecutableCompatibilityPolicy(t *testing.T) {
	readFailure := errors.New("build information is unreadable")
	wrongCommand := compatibleGoForjBuildInfo(compatibleGoForjVersion, compatibleGoForjRevision, false)
	wrongCommand.Path = "example.test/not-forj"
	wrongModule := compatibleGoForjBuildInfo(compatibleGoForjVersion, compatibleGoForjRevision, false)
	wrongModule.Main.Path = "example.test/not-goforj"
	replacedModule := compatibleGoForjBuildInfo(compatibleGoForjVersion, compatibleGoForjRevision, false)
	replacedModule.Main.Replace = &debug.Module{Path: "example.test/goforj-fork", Version: compatibleGoForjVersion}
	tests := []struct {
		name        string
		information *debug.BuildInfo
		readErr     error
		wantErr     bool
	}{
		{name: "unreadable", readErr: readFailure, wantErr: true},
		{name: "unverifiable", wantErr: true},
		{
			name:        "wrong command",
			information: wrongCommand,
			wantErr:     true,
		},
		{
			name:        "wrong module",
			information: wrongModule,
			wantErr:     true,
		},
		{
			name:        "replaced canonical module",
			information: replacedModule,
			wantErr:     true,
		},
		{
			name:        "old pseudo version",
			information: compatibleGoForjBuildInfo("v0.20.1-0.20260710184502-69fd75a80bf8", "69fd75a80bf8d613c89256a4f4fe9175b20675e7", false),
			wantErr:     true,
		},
		{
			name:        "exact pinned source pseudo version",
			information: compatibleGoForjBuildInfo(compatibleGoForjVersion, compatibleGoForjRevision, false),
		},
		{
			name:        "development without revision",
			information: compatibleGoForjBuildInfo("(devel)", "", false),
			wantErr:     true,
		},
		{
			name:        "development at other revision",
			information: compatibleGoForjBuildInfo("(devel)", "69fd75a80bf8d613c89256a4f4fe9175b20675e7", false),
			wantErr:     true,
		},
		{
			name:        "development exact pinned revision dirty",
			information: compatibleGoForjBuildInfo("(devel)", compatibleGoForjRevision, true),
			wantErr:     true,
		},
		{
			name:        "development exact pinned revision clean",
			information: compatibleGoForjBuildInfo("(devel)", compatibleGoForjRevision, false),
		},
		{
			name:        "unversioned exact pinned revision clean",
			information: compatibleGoForjBuildInfo("", compatibleGoForjRevision, false),
		},
		{
			name:        "exact pinned pseudo version",
			information: compatibleGoForjBuildInfo(compatibleGoForjVersion, compatibleGoForjRevision, false),
		},
		{
			name:        "unproven release",
			information: compatibleGoForjBuildInfo("v0.20.1", "release-revision", false),
			wantErr:     true,
		},
		{
			name:        "tagged release before pinned build",
			information: compatibleGoForjBuildInfo("v0.21.0", "older-release-revision", false),
			wantErr:     true,
		},
		{
			name:        "release after pinned build without proven implementation",
			information: compatibleGoForjBuildInfo("v0.21.2", "newer-release-revision", false),
			wantErr:     true,
		},
		{
			name:        "pseudo version after pinned build without proven implementation",
			information: compatibleGoForjBuildInfo("v0.21.1-0.20260723120000-deadbeefcafe", "deadbeefcafedeadbeefcafedeadbeefcafedead", false),
			wantErr:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const executable = "/test/bin/forj"
			calls := 0
			verifier := newGoForjExecutableVerifier(func(path string) (*debug.BuildInfo, error) {
				calls++
				if path != executable {
					t.Fatalf("build information path = %q, want %q", path, executable)
				}
				return test.information, test.readErr
			})
			err := verifier(executable)
			if calls != 1 {
				t.Fatalf("build information calls = %d, want 1", calls)
			}
			if !test.wantErr {
				if err != nil {
					t.Fatalf("verifier error = %v", err)
				}
				return
			}
			if !errors.Is(err, errIncompatibleGoForj) {
				t.Fatalf("verifier error = %v, want errIncompatibleGoForj", err)
			}
			for _, text := range []string{compatibleGoForjVersion, compatibleGoForjRevision, "forj render"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("verifier error = %q, want actionable text %q", err, text)
				}
			}
		})
	}
}

// TestNewGoForjExecutableVerifierRejectsNilReader keeps the build-information dependency fail-fast.
func TestNewGoForjExecutableVerifierRejectsNilReader(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("newGoForjExecutableVerifier accepted a nil build-information reader")
		}
	}()
	newGoForjExecutableVerifier(nil)
}

// TestIncompatibleGoForjErrorSanitizesUntrustedEvidence keeps durable lifecycle problems bounded and visibly printable.
func TestIncompatibleGoForjErrorSanitizesUntrustedEvidence(t *testing.T) {
	path := "/test/\x00/\u202eforj" + strings.Repeat("\xff", maximumErrorPathBytes*2)
	reason := "invalid\nmetadata\u202e" + strings.Repeat("\xff", maximumErrorReasonBytes*2)
	message := incompatibleGoForjError(path, reason).Error()
	if len(message) >= 4096 {
		t.Fatalf("incompatibility message length = %d, want less than 4096", len(message))
	}
	for _, character := range message {
		if character < 0x20 || character > 0x7e {
			t.Fatalf("incompatibility message contains non-visible character %U", character)
		}
	}
	for _, text := range []string{"\\x00", "\\n", "\\u202e", "...", "forj render"} {
		if !strings.Contains(message, text) {
			t.Fatalf("incompatibility message = %q, want sanitized text %q", message, text)
		}
	}
}

// TestStartReportsMissingGoForjWithoutInspectingOrStartingAnything keeps an absent PATH entry actionable and side-effect free.
func TestStartReportsMissingGoForjWithoutInspectingOrStartingAnything(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	supervisor := NewWithExecutableVerifier(Options{}, func(string) error {
		t.Fatal("missing GoForj reached executable verification")
		return nil
	})
	handle, err := supervisor.Start(t.Context(), compatibleStartRequest(t, "missing"))
	if handle != nil || !errors.Is(err, errIncompatibleGoForj) {
		t.Fatalf("Start() = %#v, %v, want nil incompatible result", handle, err)
	}
	if !strings.Contains(err.Error(), "PATH") || !strings.Contains(err.Error(), "forj render") {
		t.Fatalf("Start() missing error = %q", err)
	}
}

// TestStartRejectsIncompatibleExecutableBeforeProcessCreation proves failed metadata admission cannot create process authority or output.
func TestStartRejectsIncompatibleExecutableBeforeProcessCreation(t *testing.T) {
	installForjHelper(t, "exit")
	startedFile := t.TempDir() + "/started"
	t.Setenv(helperStartedFileEnvironment, startedFile)
	output := &synchronizedBuffer{}
	verifiedPath := ""
	supervisor := NewWithExecutableVerifier(Options{}, func(path string) error {
		verifiedPath = path
		return incompatibleGoForjError(path, "test incompatibility")
	})
	request := compatibleStartRequest(t, "incompatible")
	request.Stdout = output
	request.Stderr = output
	handle, err := supervisor.Start(t.Context(), request)
	if handle != nil || !errors.Is(err, errIncompatibleGoForj) {
		t.Fatalf("Start() = %#v, %v, want nil incompatible result", handle, err)
	}
	if verifiedPath == "" || !strings.Contains(err.Error(), boundedVisibleASCII(verifiedPath, maximumErrorPathBytes)) {
		t.Fatalf("verified path/error = %q/%q", verifiedPath, err)
	}
	if output.String() != "" || len(supervisor.projects) != 0 || len(supervisor.sessions) != 0 {
		t.Fatalf("incompatible launch produced output or authority: %q, %d/%d", output.String(), len(supervisor.projects), len(supervisor.sessions))
	}
	if _, statErr := os.Stat(startedFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("incompatible launch process marker error = %v, want not exist", statErr)
	}
}

// TestStartLaunchesTheCanonicalVerifiedExecutable binds compatibility evidence and process arguments to one path.
func TestStartLaunchesTheCanonicalVerifiedExecutable(t *testing.T) {
	installForjHelper(t, "exit")
	verifiedPath := ""
	supervisor := NewWithExecutableVerifier(Options{}, func(path string) error {
		verifiedPath = path
		return nil
	})
	handle, err := supervisor.Start(t.Context(), compatibleStartRequest(t, "canonical"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	information := handle.Info()
	if verifiedPath == "" || information.Evidence.ExecutableIdentity != verifiedPath || information.Arguments[0] != verifiedPath {
		t.Fatalf("verified/evidence/argument paths = %q/%q/%q", verifiedPath, information.Evidence.ExecutableIdentity, information.Arguments[0])
	}
}

// TestNewWithExecutableVerifierRejectsNil keeps the injected compatibility boundary fail-fast.
func TestNewWithExecutableVerifierRejectsNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewWithExecutableVerifier accepted a nil verifier")
		}
	}()
	NewWithExecutableVerifier(Options{}, nil)
}

// TestNewUsesProductionExecutableVerifier prevents the ordinary constructor from bypassing embedded build admission.
func TestNewUsesProductionExecutableVerifier(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	supervisor := New(Options{})
	if err := supervisor.verifyExecutable(executable); !errors.Is(err, errIncompatibleGoForj) {
		t.Fatalf("production verifier error = %v, want errIncompatibleGoForj", err)
	}
}

// compatibleGoForjBuildInfo creates canonical metadata with independently selectable version and VCS evidence.
func compatibleGoForjBuildInfo(version string, revision string, modified bool) *debug.BuildInfo {
	return &debug.BuildInfo{
		Path: goForjCommandPath,
		Main: debug.Module{Path: goForjModulePath, Version: version},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: fmt.Sprintf("%t", modified)},
		},
	}
}

// compatibleStartRequest creates a complete request whose identity remains unique inside one test.
func compatibleStartRequest(t *testing.T, suffix string) StartRequest {
	t.Helper()
	return StartRequest{
		ProjectID:            domain.ProjectID("project-" + suffix),
		SessionID:            domain.SessionID("session-" + suffix),
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	}
}
