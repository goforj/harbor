//go:build darwin && phase1acceptance

package acceptance

import (
	"context"
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
	"github.com/goforj/harbor/internal/rpc"
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
	configuration phase1Config
	sandbox       phase1Sandbox
	workspace     *goforjproject.Workspace
	baselines     []trustedhttpsharness.CheckoutBaseline
	projects      []trustedHTTPSNativeProject
	daemon        *phase1DaemonProcess
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
		if lifecycle.workspace != nil {
			if err := lifecycle.workspace.Close(); err != nil {
				t.Errorf("remove generated GoForj workspace: %v", err)
			}
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

	trustedHTTPSRequireSetup(t, testContext, configuration, sandbox)
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

// trustedHTTPSRequireControlCapabilities prevents a pool-only or lifecycle-incomplete daemon from entering the native proof.
func trustedHTTPSRequireControlCapabilities(t *testing.T, status control.DaemonStatus) {
	t.Helper()

	for _, capability := range []rpc.Capability{
		control.CapabilityNetworkSetupV1,
		control.CapabilityNetworkResolverSetupV1,
		control.CapabilityNetworkDataPlaneSetupV1,
		control.CapabilityProjectLifecycleV1,
	} {
		if !slices.Contains(status.Capabilities, capability) {
			t.Fatalf("ready daemon does not advertise trusted HTTPS capability %s: %v", capability, status.Capabilities)
		}
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
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, trustedHTTPSSetupTimeout)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "setup")
	if result.err != nil {
		t.Fatalf("run harbor setup: %v: %s", result.err, strings.TrimSpace(result.stderr))
	}
	if !strings.Contains(result.stdout, "Network setup complete.") {
		t.Fatalf("harbor setup output did not confirm completion: %q", result.stdout)
	}
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
	var lifecycle control.ProjectLifecycleOperation
	if err := result.decodeJSON(&lifecycle); err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	if err := lifecycle.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	expectedKind, err := trustedHTTPSLifecycleKind(action)
	if err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	if lifecycle.Operation.ProjectID != projectID ||
		lifecycle.Operation.IntentID != intentID ||
		lifecycle.Operation.Kind != expectedKind {
		return control.ProjectLifecycleOperation{}, errors.New("project lifecycle result crossed its requested action, project, or intent")
	}
	return lifecycle, nil
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
				return fmt.Errorf("operation %s ended %s in phase %q", lifecycle.Operation.ID, lifecycle.Operation.State, lifecycle.Operation.Phase)
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
	if lifecycle.daemon != nil && cleanupErr == nil {
		if err := trustedHTTPSVerifyEmptySnapshot(
			parent,
			lifecycle.configuration,
			lifecycle.sandbox,
		); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if lifecycle.daemon != nil {
		ctx, cancel := context.WithTimeout(parent, phase1ShutdownTimeout)
		stopErr := phase1StopDaemon(
			ctx,
			lifecycle.configuration,
			lifecycle.sandbox,
			lifecycle.daemon,
		)
		cancel()
		if stopErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("gracefully stop daemon: %w", stopErr))
			terminateContext, cancelTerminate := context.WithTimeout(context.Background(), 5*time.Second)
			terminateErr := lifecycle.daemon.terminate(terminateContext)
			cancelTerminate()
			if terminateErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("terminate daemon after graceful stop failure: %w", terminateErr))
			}
		}
		lifecycle.daemon = nil
	}
	if len(lifecycle.baselines) != 0 {
		if err := trustedhttpsharness.VerifyBaselines(lifecycle.baselines); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else {
			lifecycle.baselines = nil
		}
	}
	if lifecycle.workspace != nil {
		if err := lifecycle.workspace.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove generated GoForj workspace: %w", err))
		} else {
			lifecycle.workspace = nil
		}
	}
	return cleanupErr
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
	ctx, cancel := context.WithTimeout(parent, trustedHTTPSProjectLifecycleTimeout)
	defer cancel()
	ticker := time.NewTicker(trustedHTTPSPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		removal, err := trustedHTTPSInvokeProjectRemoval(
			ctx,
			configuration,
			sandbox,
			projectID,
			intentID,
		)
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
			case domain.OperationFailed, domain.OperationCancelled, domain.OperationRequiresApproval:
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
