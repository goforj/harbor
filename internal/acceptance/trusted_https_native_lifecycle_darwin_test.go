//go:build darwin && phase1acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/acceptance/trustedhttpsharness"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/projectapproval"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const (
	trustedHTTPSAcceptanceEnvironment   = "HARBOR_TRUSTED_HTTPS_ACCEPTANCE"
	trustedHTTPSForjBinaryEnvironment   = "HARBOR_TRUSTED_HTTPS_FORJ_BINARY"
	trustedHTTPSForjVersionEnvironment  = "HARBOR_TRUSTED_HTTPS_FORJ_VERSION"
	trustedHTTPSNativeLifecycleTimeout  = 15 * time.Minute
	trustedHTTPSSetupTimeout            = 5 * time.Minute
	trustedHTTPSProjectLifecycleTimeout = 2 * time.Minute
	trustedHTTPSProbeTimeout            = 2 * time.Minute
	trustedHTTPSPollInterval            = 100 * time.Millisecond
)

// trustedHTTPSNativeConfiguration fixes the GoForj binary and render contract before host setup can mutate anything.
type trustedHTTPSNativeConfiguration struct {
	forjBinary  string
	forjVersion string
}

// trustedHTTPSNativeLifecycle tracks only the resources this intermediate native proof can safely retire.
type trustedHTTPSNativeLifecycle struct {
	configuration       phase1Config
	sandbox             phase1Sandbox
	workspace           *goforjproject.Workspace
	baselines           []trustedhttpsharness.CheckoutBaseline
	projects            []trustedHTTPSNativeProject
	daemon              *phase1DaemonProcess
	retainDiagnostics   bool
	restoreReadyMarkers func([]trustedhttpsharness.CheckoutBaseline) error
	verifyBaselines     func([]trustedhttpsharness.CheckoutBaseline) error
	closeWorkspace      func(*goforjproject.Workspace) error
}

// trustedHTTPSNativeProject binds one generated public identity to its daemon registration and cleanup intents.
type trustedHTTPSNativeProject struct {
	specification   trustedhttpsharness.ProjectSpec
	registration    control.ProjectRegistration
	startIntent     domain.IntentID
	stopIntent      domain.IntentID
	removeIntent    domain.IntentID
	startOperation  domain.OperationID
	stopOperation   domain.OperationID
	removeOperation domain.OperationID
	stopRequired    bool
	removed         bool
}

// TestDarwinTrustedHTTPSIntermediateNativeLifecycle proves setup and three trusted HTTPS projects through production binaries.
// It deliberately does not claim terminal machine-global cleanup while protected ownership release remains unavailable.
func TestDarwinTrustedHTTPSIntermediateNativeLifecycle(t *testing.T) {
	if os.Getenv(trustedHTTPSAcceptanceEnvironment) != "1" {
		t.Skipf("set %s=1 to run the Darwin trusted HTTPS acceptance", trustedHTTPSAcceptanceEnvironment)
	}

	nativeConfiguration := trustedHTTPSLoadNativeConfiguration(t)
	t.Setenv(
		"PATH",
		filepath.Dir(nativeConfiguration.forjBinary)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	configuration := phase1LoadConfig(t)
	sandbox := phase1ConfigureSandbox(t, configuration)
	trustedHTTPSPropagateGoCaches(t, &sandbox)
	lifecycle := &trustedHTTPSNativeLifecycle{
		configuration: configuration,
		sandbox:       sandbox,
	}

	testContext, cancelTest := context.WithTimeout(t.Context(), trustedHTTPSNativeLifecycleTimeout)
	defer cancelTest()

	projects := trustedhttpsharness.HappyPathProjects()
	renderSpecifications, err := trustedhttpsharness.RenderSpecs(projects)
	if err != nil {
		t.Fatalf("derive generated project specifications: %v", err)
	}
	workspace, err := goforjproject.Render(testContext, goforjproject.Request{
		ForjExecutable: nativeConfiguration.forjBinary,
		GoForjVersion:  nativeConfiguration.forjVersion,
		Projects:       renderSpecifications,
	})
	if err != nil {
		t.Fatalf("render generated GoForj projects: %v", err)
	}
	lifecycle.workspace = workspace
	t.Cleanup(func() {
		if err := lifecycle.closeFallbackWorkspace(); err != nil {
			t.Errorf("remove generated GoForj workspace: %v", err)
		}
	})
	if err := trustedhttpsharness.PrepareGeneratedResponses(
		testContext,
		nativeConfiguration.forjBinary,
		projects,
		workspace.Projects,
	); err != nil {
		t.Fatalf("prepare generated project responses: %v", err)
	}
	baselines, err := trustedhttpsharness.CaptureBaselines(workspace.Projects)
	if err != nil {
		t.Fatalf("capture generated checkout baselines: %v", err)
	}
	lifecycle.baselines = baselines

	phase1AssertEndpointUnavailable(t, sandbox.endpointPath)
	phase1AssertDaemonUnavailable(t, configuration, sandbox)
	phase1RunMigrations(t, testContext, configuration, sandbox)
	evidence := phase1NewEvidence(t, configuration, sandbox)
	evidence.addRedaction(workspace.Root)
	lifecycle.daemon = phase1StartDaemon(
		t,
		configuration,
		sandbox,
		evidence,
		phase1FirstDaemonLogName,
	)
	status := phase1RequireReady(t, testContext, configuration, sandbox, lifecycle.daemon)
	phase1RequireControlCapabilities(t, status)
	trustedHTTPSRequireControlCapabilities(t, status)

	// This cleanup is registered after phase1StartDaemon so it can request graceful
	// product cleanup before the harness's final forced-process safety net runs.
	t.Cleanup(func() {
		if err := lifecycle.cleanup(context.Background()); err != nil {
			t.Errorf("recover intermediate native lifecycle resources: %v", err)
		}
	})

	trustedHTTPSRequireSetup(t, testContext, configuration, sandbox, lifecycle.daemon, evidence)
	evidence.check("full macOS network setup completed")

	for index, project := range workspace.Projects {
		registration := phase1AddProject(t, testContext, configuration, sandbox, project.Root)
		lifecycle.projects = append(lifecycle.projects, trustedHTTPSNativeProject{
			specification: projects[index],
			registration:  registration,
			startIntent:   domain.IntentID(fmt.Sprintf("intent-trusted-https-start-%d", index+1)),
			stopIntent:    domain.IntentID(fmt.Sprintf("intent-trusted-https-stop-%d", index+1)),
			removeIntent:  domain.IntentID(fmt.Sprintf("intent-trusted-https-remove-%d", index+1)),
		})
	}
	for index := range lifecycle.projects {
		project := &lifecycle.projects[index]
		started, err := trustedHTTPSInvokeProjectLifecycle(
			testContext,
			configuration,
			sandbox,
			"start",
			project.registration.Project.ID,
			project.startIntent,
		)
		if err != nil {
			t.Fatalf("queue project %s start: %v", project.registration.Project.ID, err)
		}
		project.startOperation = started.Operation.ID
		project.stopRequired = true
	}
	for index := range lifecycle.projects {
		project := &lifecycle.projects[index]
		if err := trustedHTTPSAwaitProjectLifecycle(
			testContext,
			configuration,
			sandbox,
			"start",
			project.registration.Project.ID,
			project.startIntent,
			project.startOperation,
		); err != nil {
			t.Fatalf("complete project %s start: %v", project.registration.Project.ID, err)
		}
		if err := trustedHTTPSAwaitProjectState(
			testContext,
			configuration,
			sandbox,
			project.registration.Project.ID,
			domain.ProjectReady,
			"https://"+project.specification.Domain,
		); err != nil {
			t.Fatalf("observe project %s readiness: %v", project.registration.Project.ID, err)
		}
	}
	evidence.check("three generated projects reached distinct trusted HTTPS routes")

	endpoints, err := trustedhttpsharness.ProbeEndpoints(projects)
	if err != nil {
		t.Fatalf("derive trusted HTTPS endpoints: %v", err)
	}
	probeContext, cancelProbe := context.WithTimeout(testContext, trustedHTTPSProbeTimeout)
	_, err = trustedhttpsharness.Probe(
		probeContext,
		trustedhttpsharness.ExecRunner{},
		endpoints,
	)
	cancelProbe()
	if err != nil {
		t.Fatalf("probe literal system-trusted HTTPS endpoints: %v", err)
	}
	evidence.check("literal system DNS and default trust returned three distinct OpenAPI identities")

	if err := lifecycle.cleanup(testContext); err != nil {
		t.Fatalf("clean intermediate native lifecycle resources: %v", err)
	}
	phase1AssertDaemonUnavailable(t, configuration, sandbox)
	phase1AssertCleanup(t, sandbox)
	evidence.check("project processes and per-user daemon resources were removed")
}

// trustedHTTPSPropagateGoCaches preserves the explicitly selected Go build caches for generated-project dev builds.
func trustedHTTPSPropagateGoCaches(t *testing.T, sandbox *phase1Sandbox) {
	t.Helper()

	overrides := make(map[string]string, 2)
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		path := strings.TrimSpace(os.Getenv(name))
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatalf("%s must identify an absolute clean Go cache path", name)
		}
		information, err := os.Stat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", name, err)
		}
		if !information.IsDir() {
			t.Fatalf("%s path %q is not a directory", name, path)
		}
		overrides[name] = path
	}
	sandbox.environment = phase1MergedEnvironment(sandbox.environment, overrides)
}

// trustedHTTPSRequireControlCapabilities prevents a pool-only or lifecycle-incomplete daemon from entering the native proof.
func trustedHTTPSRequireControlCapabilities(t *testing.T, status control.DaemonStatus) {
	t.Helper()

	for _, capability := range trustedHTTPSRequiredControlCapabilities() {
		if !slices.Contains(status.Capabilities, capability) {
			t.Fatalf("ready daemon does not advertise trusted HTTPS capability %s: %v", capability, status.Capabilities)
		}
	}
}

// trustedHTTPSRequiredControlCapabilities lists every daemon surface the native proof can exercise after setup starts.
func trustedHTTPSRequiredControlCapabilities() []rpc.Capability {
	return []rpc.Capability{
		control.CapabilityNetworkSetupV1,
		control.CapabilityNetworkResolverSetupV1,
		control.CapabilityNetworkDataPlaneSetupV1,
		control.CapabilityProjectLifecycleV1,
		control.CapabilityProjectUnregisterApprovalV1,
	}
}

// TestTrustedHTTPSRequiredControlCapabilitiesIncludesRemovalApproval prevents native setup from entering a cleanup-incomplete daemon.
func TestTrustedHTTPSRequiredControlCapabilitiesIncludesRemovalApproval(t *testing.T) {
	t.Parallel()

	if !slices.Contains(trustedHTTPSRequiredControlCapabilities(), control.CapabilityProjectUnregisterApprovalV1) {
		t.Fatal("trusted HTTPS native proof does not require project unregister approval")
	}
}

// trustedHTTPSLoadNativeConfiguration rejects ambiguous GoForj input before any native setup mutation.
func trustedHTTPSLoadNativeConfiguration(t *testing.T) trustedHTTPSNativeConfiguration {
	t.Helper()

	filename := strings.TrimSpace(os.Getenv(trustedHTTPSForjBinaryEnvironment))
	if filename == "" || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		t.Fatalf("%s must identify an absolute clean forj binary", trustedHTTPSForjBinaryEnvironment)
	}
	if filepath.Base(filename) != "forj" {
		t.Fatalf("%s basename is %q, want forj", trustedHTTPSForjBinaryEnvironment, filepath.Base(filename))
	}
	information, err := os.Lstat(filename)
	if err != nil {
		t.Fatalf("inspect %s: %v", trustedHTTPSForjBinaryEnvironment, err)
	}
	if information.Mode()&os.ModeSymlink != 0 ||
		!information.Mode().IsRegular() ||
		information.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s must identify a direct executable regular file", trustedHTTPSForjBinaryEnvironment)
	}
	version := strings.TrimSpace(os.Getenv(trustedHTTPSForjVersionEnvironment))
	if version == "" {
		t.Fatalf("%s must identify the numeric generated-project version", trustedHTTPSForjVersionEnvironment)
	}
	return trustedHTTPSNativeConfiguration{
		forjBinary:  filename,
		forjVersion: version,
	}
}

// trustedHTTPSRequireSetup runs the one production setup command with enough time for explicit macOS approval.
func trustedHTTPSRequireSetup(
	t *testing.T,
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	daemon *phase1DaemonProcess,
	evidence *phase1Evidence,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, trustedHTTPSSetupTimeout)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "setup")
	if result.err != nil {
		t.Fatalf(
			"run harbor setup: %v: %s%s",
			result.err,
			strings.TrimSpace(result.stderr),
			trustedHTTPSSetupFailureDiagnostic(configuration, sandbox, daemon, evidence),
		)
	}
	if !strings.Contains(result.stdout, "Network setup complete.") {
		t.Fatalf("harbor setup output did not confirm completion: %q", result.stdout)
	}
}

// trustedHTTPSSetupFailureDiagnostic captures only the active-operation fields and redacted daemon tail needed to diagnose setup failures.
func trustedHTTPSSetupFailureDiagnostic(
	configuration phase1Config,
	sandbox phase1Sandbox,
	daemon *phase1DaemonProcess,
	evidence *phase1Evidence,
) string {
	if daemon == nil || evidence == nil {
		return ""
	}

	sections := make([]string, 0, 2)
	if exited, _ := daemon.exited(); !exited {
		ctx, cancel := context.WithTimeout(context.Background(), phase1CommandTimeout)
		result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "daemon", "snapshot")
		cancel()
		var snapshot domain.Snapshot
		if result.decodeJSON(&snapshot) == nil && snapshot.Validate() == nil {
			sections = append(sections, phase1ActiveOperationDiagnostic(snapshot, evidence))
		} else {
			sections = append(sections, "daemon snapshot unavailable")
		}
	}
	if tail := strings.TrimSpace(evidence.redactedLogTail(daemon.log)); tail != "" {
		sections = append(sections, "redacted daemon log tail:\n"+tail)
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n" + strings.Join(sections, "\n")
}

// trustedHTTPSInvokeProjectLifecycle executes one production lifecycle request with an explicit replay identity.
func trustedHTTPSInvokeProjectLifecycle(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	action string,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) (control.ProjectLifecycleOperation, error) {
	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(
		ctx,
		sandbox,
		configuration.cliBinary,
		action,
		string(projectID),
		"--intent",
		string(intentID),
		"--json",
	)
	return trustedHTTPSDecodeProjectLifecycleResult(result, action, projectID, intentID)
}

// trustedHTTPSDecodeProjectLifecycleResult accepts authoritative lifecycle JSON even when a terminal CLI result exits nonzero.
func trustedHTTPSDecodeProjectLifecycleResult(
	result phase1CommandResult,
	action string,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) (control.ProjectLifecycleOperation, error) {
	var lifecycle control.ProjectLifecycleOperation
	stdoutResult := result
	stdoutResult.err = nil
	if err := stdoutResult.decodeJSON(&lifecycle); err != nil {
		return control.ProjectLifecycleOperation{}, trustedHTTPSProjectLifecycleResultError(result, fmt.Errorf("decode lifecycle JSON: %w", err))
	}
	if err := lifecycle.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, trustedHTTPSProjectLifecycleResultError(result, fmt.Errorf("validate lifecycle JSON: %w", err))
	}
	expectedKind, err := trustedHTTPSLifecycleKind(action)
	if err != nil {
		return control.ProjectLifecycleOperation{}, trustedHTTPSProjectLifecycleResultError(result, err)
	}
	if lifecycle.Operation.ProjectID != projectID ||
		lifecycle.Operation.IntentID != intentID ||
		lifecycle.Operation.Kind != expectedKind {
		return control.ProjectLifecycleOperation{}, trustedHTTPSProjectLifecycleResultError(
			result,
			errors.New("project lifecycle result crossed its requested action, project, or intent"),
		)
	}
	if result.err != nil &&
		lifecycle.Operation.State != domain.OperationFailed &&
		lifecycle.Operation.State != domain.OperationCancelled {
		return control.ProjectLifecycleOperation{}, trustedHTTPSProjectLifecycleResultError(
			result,
			fmt.Errorf("nonzero command returned nonterminal lifecycle state %q", lifecycle.Operation.State),
		)
	}
	return lifecycle, nil
}

// trustedHTTPSProjectLifecycleResultError retains a command failure when its machine-readable result is not authoritative.
func trustedHTTPSProjectLifecycleResultError(result phase1CommandResult, observation error) error {
	if result.err == nil {
		return observation
	}
	return fmt.Errorf(
		"project lifecycle command failed: %w: %s (lifecycle result: %v)",
		result.err,
		strings.TrimSpace(result.stderr),
		observation,
	)
}

// TestTrustedHTTPSDecodeProjectLifecycleResult verifies terminal CLI failures retain their authoritative lifecycle JSON.
func TestTrustedHTTPSDecodeProjectLifecycleResult(t *testing.T) {
	t.Parallel()

	projectID := domain.ProjectID("project-trusted-https")
	intentID := domain.IntentID("intent-trusted-https")
	commandFailure := errors.New("project command exited 1")
	terminal := trustedHTTPSLifecycleResultFixture(t, projectID, intentID, domain.OperationFailed)
	queued := trustedHTTPSLifecycleResultFixture(t, projectID, intentID, domain.OperationQueued)
	running := trustedHTTPSLifecycleResultFixture(t, projectID, intentID, domain.OperationRunning)
	mismatched := trustedHTTPSLifecycleResultFixture(t, projectID, "intent-other", domain.OperationFailed)

	tests := []struct {
		name               string
		result             phase1CommandResult
		wantState          domain.OperationState
		wantCommandFailure bool
		wantError          bool
	}{
		{
			name:      "terminal JSON with nonzero exit",
			result:    trustedHTTPSLifecycleCommandResult(t, terminal, commandFailure),
			wantState: domain.OperationFailed,
		},
		{
			name: "malformed stdout retains command failure",
			result: phase1CommandResult{
				stdout: "{",
				err:    commandFailure,
			},
			wantCommandFailure: true,
			wantError:          true,
		},
		{
			name: "empty stdout retains command failure",
			result: phase1CommandResult{
				err: commandFailure,
			},
			wantCommandFailure: true,
			wantError:          true,
		},
		{
			name:               "correlation mismatch",
			result:             trustedHTTPSLifecycleCommandResult(t, mismatched, commandFailure),
			wantCommandFailure: true,
			wantError:          true,
		},
		{
			name:               "nonzero queued result",
			result:             trustedHTTPSLifecycleCommandResult(t, queued, commandFailure),
			wantCommandFailure: true,
			wantError:          true,
		},
		{
			name:      "queued",
			result:    trustedHTTPSLifecycleCommandResult(t, queued, nil),
			wantState: domain.OperationQueued,
		},
		{
			name:      "running",
			result:    trustedHTTPSLifecycleCommandResult(t, running, nil),
			wantState: domain.OperationRunning,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, err := trustedHTTPSDecodeProjectLifecycleResult(test.result, "start", projectID, intentID)
			if (err != nil) != test.wantError {
				t.Fatalf("trustedHTTPSDecodeProjectLifecycleResult() error = %v, want error %t", err, test.wantError)
			}
			if test.wantCommandFailure && !errors.Is(err, commandFailure) {
				t.Fatalf("trustedHTTPSDecodeProjectLifecycleResult() error = %v, want command failure", err)
			}
			if !test.wantCommandFailure && errors.Is(err, commandFailure) {
				t.Fatalf("trustedHTTPSDecodeProjectLifecycleResult() error = %v, unexpectedly retained command failure", err)
			}
			if err == nil && got.Operation.State != test.wantState {
				t.Fatalf("trustedHTTPSDecodeProjectLifecycleResult() state = %s, want %s", got.Operation.State, test.wantState)
			}
		})
	}
}

// TestTrustedHTTPSProjectLifecycleTerminalErrorIncludesProblem verifies failed lifecycle diagnostics remain actionable.
func TestTrustedHTTPSProjectLifecycleTerminalErrorIncludesProblem(t *testing.T) {
	t.Parallel()

	lifecycle := trustedHTTPSLifecycleResultFixture(
		t,
		domain.ProjectID("project-trusted-https"),
		domain.IntentID("intent-trusted-https"),
		domain.OperationFailed,
	)
	err := trustedHTTPSProjectLifecycleTerminalError(lifecycle.Operation)
	for _, want := range []string{"code=\"project.start.failed\"", "retryable=true"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("trustedHTTPSProjectLifecycleTerminalError() = %q, want %q", err, want)
		}
	}
}

// TestTrustedHTTPSAwaitProjectRemovalApprovesAndReplays proves normal approval-bound cleanup does not abandon its removal intent.
func TestTrustedHTTPSAwaitProjectRemovalApprovesAndReplays(t *testing.T) {
	t.Parallel()

	projectID := domain.ProjectID("project-trusted-https")
	intentID := domain.IntentID("intent-trusted-https-remove")
	operationID := domain.OperationID("operation-trusted-https-remove")
	removals := []control.ProjectUnregistration{
		{
			Operation: domain.Operation{
				ID:        operationID,
				ProjectID: projectID,
				IntentID:  intentID,
				State:     domain.OperationRequiresApproval,
			},
		},
		{
			Operation: domain.Operation{
				ID:        operationID,
				ProjectID: projectID,
				IntentID:  intentID,
				State:     domain.OperationSucceeded,
			},
		},
	}
	invocations := 0
	approvals := 0
	err := trustedHTTPSAwaitProjectRemovalWith(
		t.Context(),
		projectID,
		intentID,
		&operationID,
		func(context.Context) (control.ProjectUnregistration, error) {
			removal := removals[invocations]
			invocations++
			return removal, nil
		},
		func(_ context.Context, removal control.ProjectUnregistration) error {
			approvals++
			if removal.Operation.ID != operationID || removal.Operation.State != domain.OperationRequiresApproval {
				t.Fatalf("approval selected %#v, want requires-approval operation %s", removal.Operation, operationID)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("trustedHTTPSAwaitProjectRemovalWith() error = %v", err)
	}
	if approvals != 1 || invocations != 2 {
		t.Fatalf("approval/replay calls = %d/%d, want 1/2", approvals, invocations)
	}
}

// TestTrustedHTTPSApprovalClientConfigDialsSandboxEndpoint prevents approval from attaching to an ambient daemon.
func TestTrustedHTTPSApprovalClientConfigDialsSandboxEndpoint(t *testing.T) {
	t.Parallel()

	const endpoint = "/tmp/harbor-trusted-https.sock"
	dialFailure := errors.New("dial sentinel")
	dialedEndpoint := ""
	configuration := trustedHTTPSApprovalClientConfig(endpoint, func(_ context.Context, got string) (local.Conn, error) {
		dialedEndpoint = got
		return nil, dialFailure
	})
	if configuration.Role != rpc.RoleDesktop {
		t.Fatalf("approval client role = %q, want desktop", configuration.Role)
	}
	if _, err := configuration.Dial(t.Context()); !errors.Is(err, dialFailure) {
		t.Fatalf("approval dial error = %v, want %v", err, dialFailure)
	}
	if dialedEndpoint != endpoint {
		t.Fatalf("approval dial endpoint = %q, want %q", dialedEndpoint, endpoint)
	}
}

// TestTrustedHTTPSFinalizeCheckoutRetainsArtifactsAfterCleanupFailure proves a failed cleanup leaves exact diagnostic evidence untouched.
func TestTrustedHTTPSFinalizeCheckoutRetainsArtifactsAfterCleanupFailure(t *testing.T) {
	t.Parallel()

	restored := false
	verified := false
	closed := false
	lifecycle := &trustedHTTPSNativeLifecycle{
		workspace: &goforjproject.Workspace{
			Root: "/tmp/harbor-trusted-https-diagnostics",
		},
		baselines: []trustedhttpsharness.CheckoutBaseline{
			{},
		},
		restoreReadyMarkers: func([]trustedhttpsharness.CheckoutBaseline) error {
			restored = true
			return nil
		},
		verifyBaselines: func([]trustedhttpsharness.CheckoutBaseline) error {
			verified = true
			return nil
		},
		closeWorkspace: func(*goforjproject.Workspace) error {
			closed = true
			return nil
		},
	}
	cleanupFailure := errors.New("project removal failed")
	err := lifecycle.finalizeCheckout(cleanupFailure, true)
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("finalizeCheckout() error = %v, want cleanup failure", err)
	}
	if restored || verified || closed {
		t.Fatalf("finalizeCheckout() restore/verify/close = %t/%t/%t, want false/false/false", restored, verified, closed)
	}
	if !lifecycle.retainDiagnostics {
		t.Fatal("finalizeCheckout() did not mark the failed cleanup workspace for diagnostic retention")
	}
	if err := lifecycle.closeFallbackWorkspace(); err != nil {
		t.Fatalf("closeFallbackWorkspace() error = %v", err)
	}
	if closed {
		t.Fatal("fallback cleanup removed a workspace retained by lifecycle cleanup")
	}
	if lifecycle.workspace == nil || len(lifecycle.baselines) != 1 {
		t.Fatalf("finalizeCheckout() discarded diagnostic artifacts: workspace=%#v baselines=%#v", lifecycle.workspace, lifecycle.baselines)
	}
}

// TestTrustedHTTPSFallbackWorkspaceCleanupReleasesPreLifecycleFailure proves rendering failures still clean up before lifecycle retention exists.
func TestTrustedHTTPSFallbackWorkspaceCleanupReleasesPreLifecycleFailure(t *testing.T) {
	t.Parallel()

	closed := false
	lifecycle := &trustedHTTPSNativeLifecycle{
		workspace: &goforjproject.Workspace{
			Root: "/tmp/harbor-trusted-https-pre-lifecycle",
		},
		closeWorkspace: func(*goforjproject.Workspace) error {
			closed = true
			return nil
		},
	}
	if err := lifecycle.closeFallbackWorkspace(); err != nil {
		t.Fatalf("closeFallbackWorkspace() error = %v", err)
	}
	if !closed || lifecycle.workspace != nil {
		t.Fatalf("fallback cleanup close/workspace = %t/%#v, want true/nil", closed, lifecycle.workspace)
	}
}

// TestTrustedHTTPSFinalizeCheckoutRestoresAndDeletesAfterSuccess proves exact checkout restoration remains part of successful cleanup.
func TestTrustedHTTPSFinalizeCheckoutRestoresAndDeletesAfterSuccess(t *testing.T) {
	t.Parallel()

	restored := false
	verified := false
	closed := false
	lifecycle := &trustedHTTPSNativeLifecycle{
		workspace: &goforjproject.Workspace{
			Root: "/tmp/harbor-trusted-https-success",
		},
		baselines: []trustedhttpsharness.CheckoutBaseline{
			{},
		},
		restoreReadyMarkers: func([]trustedhttpsharness.CheckoutBaseline) error {
			restored = true
			return nil
		},
		verifyBaselines: func([]trustedhttpsharness.CheckoutBaseline) error {
			verified = true
			return nil
		},
		closeWorkspace: func(*goforjproject.Workspace) error {
			closed = true
			return nil
		},
	}
	if err := lifecycle.finalizeCheckout(nil, true); err != nil {
		t.Fatalf("finalizeCheckout() error = %v", err)
	}
	if !restored || !verified || !closed {
		t.Fatalf("finalizeCheckout() restore/verify/close = %t/%t/%t, want true/true/true", restored, verified, closed)
	}
	if lifecycle.workspace != nil || lifecycle.baselines != nil {
		t.Fatalf("finalizeCheckout() retained successful artifacts: workspace=%#v baselines=%#v", lifecycle.workspace, lifecycle.baselines)
	}
}

// trustedHTTPSLifecycleCommandResult encodes one CLI lifecycle result without mixing it with diagnostic output.
func trustedHTTPSLifecycleCommandResult(t *testing.T, lifecycle control.ProjectLifecycleOperation, err error) phase1CommandResult {
	t.Helper()

	encoded, marshalErr := json.Marshal(lifecycle)
	if marshalErr != nil {
		t.Fatalf("marshal lifecycle result: %v", marshalErr)
	}
	return phase1CommandResult{
		stdout: string(encoded),
		err:    err,
	}
}

// trustedHTTPSLifecycleResultFixture constructs one validated lifecycle state for decoder tests.
func trustedHTTPSLifecycleResultFixture(
	t *testing.T,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	state domain.OperationState,
) control.ProjectLifecycleOperation {
	t.Helper()

	requestedAt := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation("operation-trusted-https", intentID, domain.OperationKindProjectStart, projectID, requestedAt)
	if err != nil {
		t.Fatalf("new lifecycle operation: %v", err)
	}
	if state == domain.OperationQueued {
		return control.ProjectLifecycleOperation{
			Operation: operation,
			Revision:  1,
		}
	}
	operation, err = operation.Transition(domain.OperationRunning, "starting project", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("start lifecycle operation: %v", err)
	}
	if state == domain.OperationRunning {
		return control.ProjectLifecycleOperation{
			Operation: operation,
			Revision:  2,
		}
	}
	if state != domain.OperationFailed {
		t.Fatalf("unsupported lifecycle fixture state %s", state)
	}
	operation, err = operation.Transition(domain.OperationFailed, "project failed", requestedAt.Add(2*time.Second), &domain.Problem{
		Code:      "project.start.failed",
		Message:   "project did not become ready",
		Retryable: true,
	})
	if err != nil {
		t.Fatalf("fail lifecycle operation: %v", err)
	}
	return control.ProjectLifecycleOperation{
		Operation: operation,
		Revision:  3,
	}
}

// trustedHTTPSAwaitProjectLifecycle replays one action until its daemon-owned operation is terminal.
func trustedHTTPSAwaitProjectLifecycle(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	action string,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	operationID domain.OperationID,
) error {
	ctx, cancel := context.WithTimeout(parent, trustedHTTPSProjectLifecycleTimeout)
	defer cancel()
	ticker := time.NewTicker(trustedHTTPSPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		lifecycle, err := trustedHTTPSInvokeProjectLifecycle(
			ctx,
			configuration,
			sandbox,
			action,
			projectID,
			intentID,
		)
		if err == nil {
			if lifecycle.Operation.ID != operationID {
				return fmt.Errorf(
					"project lifecycle replay returned operation %s, expected %s",
					lifecycle.Operation.ID,
					operationID,
				)
			}
			switch lifecycle.Operation.State {
			case domain.OperationSucceeded:
				return nil
			case domain.OperationFailed, domain.OperationCancelled, domain.OperationRequiresApproval:
				return trustedHTTPSProjectLifecycleTerminalError(lifecycle.Operation)
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("await project %s %s: %w (last observation: %v)", projectID, action, ctx.Err(), lastErr)
			}
			return fmt.Errorf("await project %s %s: %w", projectID, action, ctx.Err())
		case <-ticker.C:
		}
	}
}

// trustedHTTPSProjectLifecycleTerminalError reports durable terminal state with its bounded failure details when available.
func trustedHTTPSProjectLifecycleTerminalError(operation domain.Operation) error {
	if operation.Problem != nil {
		return fmt.Errorf(
			"operation %s ended %s in phase %q (problem code=%q retryable=%t)",
			operation.ID,
			operation.State,
			operation.Phase,
			operation.Problem.Code,
			operation.Problem.Retryable,
		)
	}
	return fmt.Errorf("operation %s ended %s in phase %q", operation.ID, operation.State, operation.Phase)
}

// trustedHTTPSAwaitProjectState confirms lifecycle completion produced the exact public project projection.
func trustedHTTPSAwaitProjectState(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	projectID domain.ProjectID,
	wantState domain.ProjectState,
	wantURL string,
) error {
	ctx, cancel := context.WithTimeout(parent, trustedHTTPSProjectLifecycleTimeout)
	defer cancel()
	ticker := time.NewTicker(trustedHTTPSPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		project, err := trustedHTTPSReadProject(ctx, configuration, sandbox, projectID)
		if err == nil {
			if project.State == domain.ProjectFailed || project.State == domain.ProjectUnavailable {
				return fmt.Errorf("project reached terminal state %q", project.State)
			}
			if project.State == wantState {
				if wantURL == "" {
					if len(project.Resources) == 0 {
						return nil
					}
					lastErr = fmt.Errorf(
						"project retains %d resources after reaching %s",
						len(project.Resources),
						wantState,
					)
				} else {
					matches := 0
					for _, resource := range project.Resources {
						if resource.URL == wantURL {
							matches++
						}
					}
					if matches == 1 {
						return nil
					}
					lastErr = fmt.Errorf("project has %d resources at %q, want exactly one", matches, wantURL)
				}
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("await project %s state %s: %w (last observation: %v)", projectID, wantState, ctx.Err(), lastErr)
			}
			return fmt.Errorf("await project %s state %s: %w", projectID, wantState, ctx.Err())
		case <-ticker.C:
		}
	}
}

// trustedHTTPSReadProject reads one validated project through the production CLI.
func trustedHTTPSReadProject(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	projectID domain.ProjectID,
) (domain.ProjectSnapshot, error) {
	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(
		ctx,
		sandbox,
		configuration.cliBinary,
		"status",
		string(projectID),
		"--json",
	)
	var project domain.ProjectSnapshot
	if err := result.decodeJSON(&project); err != nil {
		return domain.ProjectSnapshot{}, err
	}
	if err := project.Validate(); err != nil {
		return domain.ProjectSnapshot{}, err
	}
	if project.ID != projectID {
		return domain.ProjectSnapshot{}, errors.New("project status crossed the requested project identity")
	}
	return project, nil
}

// trustedHTTPSLifecycleKind admits only the two actions used by this bounded lifecycle proof.
func trustedHTTPSLifecycleKind(action string) (domain.OperationKind, error) {
	switch action {
	case "start":
		return domain.OperationKindProjectStart, nil
	case "stop":
		return domain.OperationKindProjectStop, nil
	default:
		return "", fmt.Errorf("trusted HTTPS lifecycle action %q is unsupported", action)
	}
}

// cleanup attempts every project stop/removal, snapshot check, daemon shutdown, and checkout verification.
func (lifecycle *trustedHTTPSNativeLifecycle) cleanup(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	var cleanupErr error
	for index := len(lifecycle.projects) - 1; index >= 0; index-- {
		project := &lifecycle.projects[index]
		if project.stopRequired {
			if project.stopOperation == "" {
				stopped, err := trustedHTTPSInvokeProjectLifecycle(
					parent,
					lifecycle.configuration,
					lifecycle.sandbox,
					"stop",
					project.registration.Project.ID,
					project.stopIntent,
				)
				if err != nil {
					cleanupErr = errors.Join(
						cleanupErr,
						fmt.Errorf("begin stop project %s: %w", project.registration.Project.ID, err),
					)
				} else {
					project.stopOperation = stopped.Operation.ID
				}
			}
			if project.stopOperation != "" {
				err := trustedHTTPSAwaitProjectLifecycle(
					parent,
					lifecycle.configuration,
					lifecycle.sandbox,
					"stop",
					project.registration.Project.ID,
					project.stopIntent,
					project.stopOperation,
				)
				if err == nil {
					err = trustedHTTPSAwaitProjectState(
						parent,
						lifecycle.configuration,
						lifecycle.sandbox,
						project.registration.Project.ID,
						domain.ProjectStopped,
						"",
					)
				}
				if err != nil {
					cleanupErr = errors.Join(
						cleanupErr,
						fmt.Errorf("stop project %s: %w", project.registration.Project.ID, err),
					)
				} else {
					project.stopRequired = false
				}
			}
		}
		if !project.removed {
			if err := trustedHTTPSAwaitProjectRemoval(
				parent,
				lifecycle.configuration,
				lifecycle.sandbox,
				project.registration.Project.ID,
				project.removeIntent,
				&project.removeOperation,
			); err != nil {
				cleanupErr = errors.Join(
					cleanupErr,
					fmt.Errorf("remove project %s: %w", project.registration.Project.ID, err),
				)
			} else {
				project.removed = true
			}
		}
	}
	if lifecycle.daemon != nil {
		if err := trustedHTTPSVerifyEmptySnapshot(
			parent,
			lifecycle.configuration,
			lifecycle.sandbox,
		); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	daemonStopped := lifecycle.daemon == nil
	if lifecycle.daemon != nil {
		ctx, cancel := context.WithTimeout(parent, phase1ShutdownTimeout)
		stopErr := phase1StopDaemon(
			ctx,
			lifecycle.configuration,
			lifecycle.sandbox,
			lifecycle.daemon,
		)
		cancel()
		if stopErr == nil {
			daemonStopped = true
		} else {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("gracefully stop daemon: %w", stopErr))
			terminateContext, cancelTerminate := context.WithTimeout(context.Background(), 5*time.Second)
			terminateErr := lifecycle.daemon.terminate(terminateContext)
			cancelTerminate()
			if terminateErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("terminate daemon after graceful stop failure: %w", terminateErr))
			} else {
				daemonStopped = true
			}
		}
		if daemonStopped {
			lifecycle.daemon = nil
		}
	}
	return lifecycle.finalizeCheckout(cleanupErr, daemonStopped)
}

// finalizeCheckout restores and deletes generated checkouts only after every cleanup boundary completed successfully.
func (lifecycle *trustedHTTPSNativeLifecycle) finalizeCheckout(cleanupErr error, daemonStopped bool) error {
	if cleanupErr != nil || !daemonStopped {
		return lifecycle.retainWorkspace(cleanupErr)
	}
	if len(lifecycle.baselines) != 0 {
		restore := lifecycle.restoreReadyMarkers
		if restore == nil {
			restore = trustedhttpsharness.RestoreReadyMarkers
		}
		if err := restore(lifecycle.baselines); err != nil {
			return lifecycle.retainWorkspace(err)
		}
		verify := lifecycle.verifyBaselines
		if verify == nil {
			verify = trustedhttpsharness.VerifyBaselinesExact
		}
		if err := verify(lifecycle.baselines); err != nil {
			return lifecycle.retainWorkspace(err)
		}
		lifecycle.baselines = nil
	}
	if lifecycle.workspace != nil {
		if err := lifecycle.closeGeneratedWorkspace(); err != nil {
			return lifecycle.retainWorkspace(fmt.Errorf("remove generated GoForj workspace: %w", err))
		}
		lifecycle.workspace = nil
	}
	return nil
}

// closeFallbackWorkspace releases an early-render failure workspace unless a later lifecycle cleanup retained it for diagnosis.
func (lifecycle *trustedHTTPSNativeLifecycle) closeFallbackWorkspace() error {
	if lifecycle.workspace == nil || lifecycle.retainDiagnostics {
		return nil
	}
	if err := lifecycle.closeGeneratedWorkspace(); err != nil {
		return err
	}
	lifecycle.workspace = nil
	return nil
}

// closeGeneratedWorkspace applies the injectable workspace closer used by both cleanup registrations.
func (lifecycle *trustedHTTPSNativeLifecycle) closeGeneratedWorkspace() error {
	closeWorkspace := lifecycle.closeWorkspace
	if closeWorkspace == nil {
		closeWorkspace = func(workspace *goforjproject.Workspace) error {
			return workspace.Close()
		}
	}
	return closeWorkspace(lifecycle.workspace)
}

// retainWorkspace joins an explicit diagnostic retention record to a cleanup failure without deleting the checkout.
func (lifecycle *trustedHTTPSNativeLifecycle) retainWorkspace(cleanupErr error) error {
	if lifecycle.workspace == nil {
		return cleanupErr
	}
	lifecycle.retainDiagnostics = true
	return errors.Join(cleanupErr, fmt.Errorf("retain generated GoForj workspace %q because cleanup did not complete", lifecycle.workspace.Root))
}

// trustedHTTPSAwaitProjectRemoval replays removal until the explicit intent is terminal and its local journal is cleared.
func trustedHTTPSAwaitProjectRemoval(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	operationID *domain.OperationID,
) error {
	return trustedHTTPSAwaitProjectRemovalWith(
		parent,
		projectID,
		intentID,
		operationID,
		func(ctx context.Context) (control.ProjectUnregistration, error) {
			return trustedHTTPSInvokeProjectRemoval(ctx, configuration, sandbox, projectID, intentID)
		},
		func(ctx context.Context, removal control.ProjectUnregistration) error {
			return trustedHTTPSApproveProjectRemoval(ctx, sandbox, removal)
		},
	)
}

// projectRemovalInvoker replays one exact removal request through its production transport boundary.
type projectRemovalInvoker func(context.Context) (control.ProjectUnregistration, error)

// projectRemovalApprover completes the native approval protocol for one daemon-selected removal revision.
type projectRemovalApprover func(context.Context, control.ProjectUnregistration) error

// trustedHTTPSAwaitProjectRemovalWith keeps approval replay observable without weakening the production protocol.
func trustedHTTPSAwaitProjectRemovalWith(
	parent context.Context,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	operationID *domain.OperationID,
	invoke projectRemovalInvoker,
	approve projectRemovalApprover,
) error {
	ctx, cancel := context.WithTimeout(parent, trustedHTTPSProjectLifecycleTimeout)
	defer cancel()
	ticker := time.NewTicker(trustedHTTPSPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		removal, err := invoke(ctx)
		if err == nil {
			if *operationID == "" {
				*operationID = removal.Operation.ID
			}
			if removal.Operation.ID != *operationID {
				return fmt.Errorf(
					"project removal replay returned operation %s, expected %s",
					removal.Operation.ID,
					*operationID,
				)
			}
			switch removal.Operation.State {
			case domain.OperationSucceeded:
				return nil
			case domain.OperationRequiresApproval:
				if err := approve(ctx, removal); err != nil {
					return fmt.Errorf("approve project removal %s: %w", projectID, err)
				}
			case domain.OperationFailed, domain.OperationCancelled:
				return fmt.Errorf("operation %s ended %s in phase %q", removal.Operation.ID, removal.Operation.State, removal.Operation.Phase)
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("await project %s removal: %w (last observation: %v)", projectID, ctx.Err(), lastErr)
			}
			return fmt.Errorf("await project %s removal: %w", projectID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// trustedHTTPSApproveProjectRemoval uses the same desktop-role approval executor and native helper protocol as the product client.
func trustedHTTPSApproveProjectRemoval(ctx context.Context, sandbox phase1Sandbox, removal control.ProjectUnregistration) error {
	client, err := control.NewClient(ctx, trustedHTTPSApprovalClientConfig(sandbox.endpointPath, local.DialAt))
	if err != nil {
		return fmt.Errorf("open desktop approval client: %w", err)
	}
	defer client.Close()

	runner := projectapproval.New(client, launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}))
	outcome, err := runner.Execute(ctx, projectapproval.Request{
		OperationID:               removal.Operation.ID,
		ExpectedOperationRevision: removal.Revision,
	})
	if err != nil {
		return err
	}
	if outcome.State != projectapproval.Succeeded || outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return fmt.Errorf("native project removal approval ended %s", outcome.State)
	}
	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return fmt.Errorf("validate project removal approval confirmation: %w", err)
	}
	if confirmation.Operation.ID != removal.Operation.ID ||
		confirmation.Operation.ProjectID != removal.Operation.ProjectID ||
		confirmation.Operation.IntentID != removal.Operation.IntentID ||
		confirmation.Revision <= removal.Revision ||
		confirmation.Operation.State != domain.OperationSucceeded {
		return errors.New("project removal approval confirmation crossed the selected operation")
	}
	return nil
}

// localDialAt dials one exact acceptance-controlled endpoint.
type localDialAt func(context.Context, string) (local.Conn, error)

// trustedHTTPSApprovalClientConfig keeps approval bound to the daemon instance created inside this acceptance sandbox.
func trustedHTTPSApprovalClientConfig(endpoint string, dialAt localDialAt) control.ClientConfig {
	return control.ClientConfig{
		Role: rpc.RoleDesktop,
		Dial: func(ctx context.Context) (local.Conn, error) {
			return dialAt(ctx, endpoint)
		},
	}
}

// trustedHTTPSInvokeProjectRemoval executes one production removal request with an explicit replay identity.
func trustedHTTPSInvokeProjectRemoval(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) (control.ProjectUnregistration, error) {
	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(
		ctx,
		sandbox,
		configuration.cliBinary,
		"remove",
		string(projectID),
		"--intent",
		string(intentID),
		"--json",
	)
	var removal control.ProjectUnregistration
	if err := result.decodeJSON(&removal); err != nil {
		return control.ProjectUnregistration{}, err
	}
	if err := removal.Validate(); err != nil {
		return control.ProjectUnregistration{}, err
	}
	if removal.Operation.ProjectID != projectID || removal.Operation.IntentID != intentID {
		return control.ProjectUnregistration{}, errors.New("project removal result crossed its requested project or intent")
	}
	return removal, nil
}

// trustedHTTPSVerifyEmptySnapshot proves project cleanup left no registered project or active operation.
func trustedHTTPSVerifyEmptySnapshot(
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
) error {
	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "daemon", "snapshot")
	var snapshot domain.Snapshot
	if err := result.decodeJSON(&snapshot); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if len(snapshot.Projects) != 0 {
		return fmt.Errorf("final snapshot contains %d registered projects", len(snapshot.Projects))
	}
	for _, operation := range snapshot.Operations {
		if !operation.State.IsTerminal() {
			return fmt.Errorf("final snapshot retains active operation %s in state %s", operation.ID, operation.State)
		}
	}
	return nil
}
