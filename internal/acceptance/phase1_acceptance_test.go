//go:build phase1acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

// phase1Invocation describes one real binary command launched behind a shared concurrency barrier.
type phase1Invocation struct {
	name   string
	binary string
	args   []string
}

// phase1InvocationResult binds bounded command output to the invocation that produced it.
type phase1InvocationResult struct {
	name   string
	result phase1CommandResult
}

// TestPhase1ProductionLifecycle proves the Phase-1 control and recovery contracts through production binaries and paths.
func TestPhase1ProductionLifecycle(t *testing.T) {
	configuration := phase1LoadConfig(t)
	sandbox := phase1ConfigureSandbox(t, configuration)
	evidence := phase1NewEvidence(t, configuration, sandbox)

	testContext, cancelTest := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancelTest()

	phase1AssertEndpointUnavailable(t, sandbox.endpointPath)
	phase1AssertDaemonUnavailable(t, configuration, sandbox)
	evidence.check("isolated IPC endpoint unclaimed before startup")

	phase1RunMigrations(t, testContext, configuration, sandbox)
	evidence.check("embedded migrations applied to isolated standard path")

	firstDaemon := phase1StartDaemon(t, configuration, sandbox, evidence, "daemon-generation-1-hard-kill")
	firstStatus := phase1RequireReady(t, testContext, configuration, sandbox, firstDaemon)
	phase1RequireControlCapabilities(t, firstStatus)
	evidence.check("D1 daemon readiness")

	firstObserver := phase1OpenDesktopObserver(t, testContext)
	initialSnapshot := phase1ObserverSnapshot(t, testContext, firstObserver)
	if len(initialSnapshot.Projects) != 0 || len(initialSnapshot.Operations) != 0 {
		t.Fatalf("initial snapshot contains state before registration: %#v", initialSnapshot)
	}

	firstProjectPath := phase1WriteProjectFixture(t, sandbox, evidence, "orders", "Phase One Orders")
	concurrent := phase1RunConcurrent(
		testContext,
		sandbox,
		[]phase1Invocation{
			{name: "status", binary: configuration.cliBinary, args: []string{"daemon", "status", "--json"}},
			{name: "snapshot", binary: configuration.cliBinary, args: []string{"daemon", "snapshot"}},
			{name: "add-first", binary: configuration.cliBinary, args: []string{"add", firstProjectPath, "--json"}},
			{name: "add-replay", binary: configuration.cliBinary, args: []string{"add", firstProjectPath, "--json"}},
		},
	)
	phase1DecodeConcurrentRead(t, concurrent["status"], concurrent["snapshot"])
	firstRegistration := phase1DecodeRegistration(t, concurrent["add-first"])
	replayedRegistration := phase1DecodeRegistration(t, concurrent["add-replay"])
	phase1AssertRegistrationReplay(t, firstRegistration, replayedRegistration)
	projectBeforeKill := phase1RequireProjectSnapshot(t, phase1ObserverSnapshot(t, testContext, firstObserver), firstRegistration.Project.ID)
	evidence.check("concurrent status snapshot and registration replay")

	killContext, cancelKill := context.WithTimeout(testContext, phase1ShutdownTimeout)
	killErr := firstDaemon.hardKill(killContext)
	cancelKill()
	if killErr != nil {
		t.Fatalf("hard kill production daemon: %v", killErr)
	}
	observerContext, cancelObserver := context.WithTimeout(testContext, phase1ShutdownTimeout)
	if err := phase1WaitObserverDone(observerContext, firstObserver); err != nil {
		cancelObserver()
		t.Fatalf("desktop observer did not observe hard daemon exit: %v", err)
	}
	cancelObserver()
	_ = firstObserver.Close()
	evidence.check("hard daemon termination observed by retained desktop session")
	firstAuthority := phase1ReadAuthorityEvidence(t, testContext, sandbox)

	secondDaemon := phase1StartDaemon(t, configuration, sandbox, evidence, "daemon-generation-2-restart")
	secondStatus := phase1RequireReady(t, testContext, configuration, sandbox, secondDaemon)
	phase1RequireControlCapabilities(t, secondStatus)
	secondObserver := phase1OpenDesktopObserver(t, testContext)
	recoveredSnapshot := phase1ObserverSnapshot(t, testContext, secondObserver)
	projectAfterRestart := phase1RequireProjectSnapshot(t, recoveredSnapshot, firstRegistration.Project.ID)
	if !reflect.DeepEqual(projectBeforeKill, projectAfterRestart) {
		t.Fatalf("project changed across hard restart:\nbefore: %#v\nafter:  %#v", projectBeforeKill, projectAfterRestart)
	}
	if recoveredSnapshot.Sequence != phase1ReadSnapshot(t, testContext, configuration, sandbox).Sequence {
		t.Fatal("desktop and CLI snapshots disagree after hard restart")
	}
	evidence.check("hard restart and authoritative snapshot recovery")

	phase1RequireGracefulStop(t, testContext, configuration, sandbox, secondDaemon, secondObserver)
	evidence.check("CLI graceful stop acknowledgement and joined cleanup")
	secondAuthority := phase1ReadAuthorityEvidence(t, testContext, sandbox)
	phase1AssertAuthorityIdentity(t, firstAuthority, secondAuthority, "D1", "D2")

	seedOperationID := domain.OperationID("operation-phase1-recovery")
	seedIntentID := domain.IntentID("intent-phase1-recovery")
	stateContext, cancelState := context.WithTimeout(testContext, phase1CommandTimeout)
	if _, err := phase1RunStateSubprocess(
		stateContext,
		sandbox,
		phase1StateHelperSeedMode,
		firstRegistration.Project.ID,
		seedOperationID,
		seedIntentID,
	); err != nil {
		cancelState()
		t.Fatalf("seed queued unregister while daemon is stopped: %v", err)
	}
	cancelState()
	queuedEvidence := phase1ReadDurableEvidence(
		t,
		testContext,
		sandbox,
		firstRegistration.Project.ID,
		seedOperationID,
		seedIntentID,
	)
	phase1AssertDurableEvidence(
		t,
		queuedEvidence,
		firstRegistration.Project.ID,
		seedOperationID,
		seedIntentID,
		domain.OperationQueued,
		true,
		false,
		1,
		1,
		[]domain.OperationState{domain.OperationQueued},
	)
	evidence.check("queued unregister seeded through production state API while stopped")

	thirdDaemon := phase1StartDaemon(t, configuration, sandbox, evidence, "daemon-generation-3-startup-recovery")
	thirdStatus := phase1RequireReady(t, testContext, configuration, sandbox, thirdDaemon)
	phase1RequireControlCapabilities(t, thirdStatus)
	thirdObserver := phase1OpenDesktopObserver(t, testContext)
	startupRecovered := phase1ObserverSnapshot(t, testContext, thirdObserver)
	phase1AssertProjectAbsent(t, startupRecovered, firstRegistration.Project.ID)
	if len(startupRecovered.Operations) != 0 {
		t.Fatalf("D3 readiness exposed active operations after startup recovery: %#v", startupRecovered.Operations)
	}
	evidence.check("D3 queued-operation startup recovery before readiness")

	secondProjectPath := phase1WriteProjectFixture(t, sandbox, evidence, "billing", "Phase One Billing")
	secondRegistration := phase1AddProject(t, testContext, configuration, sandbox, secondProjectPath)
	removeIntentID := domain.IntentID("intent-phase1-concurrent-remove")
	removeArguments := []string{
		"remove",
		string(secondRegistration.Project.ID),
		"--intent",
		string(removeIntentID),
		"--json",
	}
	removals := phase1RunConcurrent(
		testContext,
		sandbox,
		[]phase1Invocation{
			{name: "remove-first", binary: configuration.cliBinary, args: removeArguments},
			{name: "remove-replay", binary: configuration.cliBinary, args: append([]string(nil), removeArguments...)},
		},
	)
	firstRemoval := phase1DecodeRemoval(t, removals["remove-first"])
	replayedRemoval := phase1DecodeRemoval(t, removals["remove-replay"])
	phase1AssertRemovalReplay(t, firstRemoval, replayedRemoval, secondRegistration.Project.ID, removeIntentID)
	finalSnapshot := phase1ObserverSnapshot(t, testContext, thirdObserver)
	phase1AssertProjectAbsent(t, finalSnapshot, secondRegistration.Project.ID)
	if len(finalSnapshot.Operations) != 0 {
		t.Fatalf("terminal removals remain in the client snapshot: %#v", finalSnapshot.Operations)
	}
	evidence.check("concurrent cross-process idempotent project removal")

	phase1RequireGracefulStop(t, testContext, configuration, sandbox, thirdDaemon, thirdObserver)
	thirdAuthority := phase1ReadAuthorityEvidence(t, testContext, sandbox)
	phase1AssertAuthorityIdentity(t, firstAuthority, thirdAuthority, "D1", "D3")
	evidence.check("persisted public CA identity survived D1 D2 and D3 boundaries")

	recoveredEvidence := phase1ReadDurableEvidence(
		t,
		testContext,
		sandbox,
		firstRegistration.Project.ID,
		seedOperationID,
		seedIntentID,
	)
	phase1AssertDurableEvidence(
		t,
		recoveredEvidence,
		firstRegistration.Project.ID,
		seedOperationID,
		seedIntentID,
		domain.OperationSucceeded,
		false,
		false,
		0,
		0,
		[]domain.OperationState{domain.OperationQueued, domain.OperationRunning, domain.OperationSucceeded},
	)
	removedEvidence := phase1ReadDurableEvidence(
		t,
		testContext,
		sandbox,
		secondRegistration.Project.ID,
		firstRemoval.Operation.ID,
		removeIntentID,
	)
	phase1AssertDurableEvidence(
		t,
		removedEvidence,
		secondRegistration.Project.ID,
		firstRemoval.Operation.ID,
		removeIntentID,
		domain.OperationSucceeded,
		false,
		false,
		0,
		0,
		[]domain.OperationState{domain.OperationQueued, domain.OperationRunning, domain.OperationSucceeded},
	)
	evidence.check("durable operation and transition history inspected through production APIs")

	phase1AssertDaemonUnavailable(t, configuration, sandbox)
	phase1AssertCleanup(t, sandbox)
	evidence.check("IPC lock runtime WAL and CLI intent cleanup")
}

// phase1ReadAuthorityEvidence invokes the isolated public-root inspector while no daemon owns trust material.
func phase1ReadAuthorityEvidence(
	t *testing.T,
	parent context.Context,
	sandbox phase1Sandbox,
) phase1AuthorityEvidence {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	evidence, err := phase1RunAuthoritySubprocess(ctx, sandbox)
	if err != nil {
		t.Fatalf("inspect persisted public authority: %v", err)
	}
	return evidence
}

// phase1AssertAuthorityIdentity proves daemon restarts retain one canonical public trust identity.
func phase1AssertAuthorityIdentity(
	t *testing.T,
	baseline phase1AuthorityEvidence,
	candidate phase1AuthorityEvidence,
	baselineGeneration string,
	candidateGeneration string,
) {
	t.Helper()

	if err := phase1ValidateAuthorityFingerprint(baseline.Fingerprint); err != nil {
		t.Fatalf("%s public authority identity: %v", baselineGeneration, err)
	}
	if err := phase1ValidateAuthorityFingerprint(candidate.Fingerprint); err != nil {
		t.Fatalf("%s public authority identity: %v", candidateGeneration, err)
	}
	if candidate.Fingerprint != baseline.Fingerprint {
		t.Fatalf(
			"public authority changed across %s/%s boundary: %s != %s",
			baselineGeneration,
			candidateGeneration,
			candidate.Fingerprint,
			baseline.Fingerprint,
		)
	}
}

// phase1RunMigrations invokes the daemon's embedded migration command before any runtime authority is acquired.
func phase1RunMigrations(t *testing.T, parent context.Context, configuration phase1Config, sandbox phase1Sandbox) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.daemonBinary, "migrate")
	if result.err != nil {
		t.Fatalf("run embedded migrations: %v: %s", result.err, strings.TrimSpace(result.stderr))
	}
	info, err := os.Stat(sandbox.databasePath)
	if err != nil {
		t.Fatalf("inspect migrated database: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("migrated database %q is not a regular file", sandbox.databasePath)
	}
}

// phase1RequireReady joins readiness polling to the daemon generation under test.
func phase1RequireReady(
	t *testing.T,
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	process *phase1DaemonProcess,
) control.DaemonStatus {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1StartupTimeout)
	defer cancel()
	status, err := phase1WaitReady(ctx, configuration, sandbox, process)
	if err != nil {
		t.Fatalf("wait for production daemon readiness: %v\n%s", err, string(process.log.snapshot()))
	}
	return status
}

// phase1RequireControlCapabilities verifies readiness advertises every Phase-1 surface used by the acceptance flow.
func phase1RequireControlCapabilities(t *testing.T, status control.DaemonStatus) {
	t.Helper()

	for _, capability := range []rpc.Capability{
		control.CapabilityV1,
		control.CapabilityDaemonControlV1,
		control.CapabilityProjectRegistrationV1,
		control.CapabilityProjectUnregisterV1,
	} {
		if !slices.Contains(status.Capabilities, capability) {
			t.Fatalf("ready daemon does not advertise %s: %v", capability, status.Capabilities)
		}
	}
}

// phase1OpenDesktopObserver retains one honest desktop-role control session across daemon lifecycle changes.
func phase1OpenDesktopObserver(t *testing.T, parent context.Context) *control.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	observer, err := control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop})
	if err != nil {
		t.Fatalf("open desktop observer: %v", err)
	}
	return observer
}

// phase1ObserverSnapshot reads one authoritative replacement through a retained desktop-role session.
func phase1ObserverSnapshot(t *testing.T, parent context.Context, observer *control.Client) domain.Snapshot {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	snapshot, err := observer.Snapshot(ctx)
	if err != nil {
		t.Fatalf("read desktop observer snapshot: %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("validate desktop observer snapshot: %v", err)
	}
	return snapshot
}

// phase1RunConcurrent starts every production command only after all goroutines are ready at the same barrier.
func phase1RunConcurrent(parent context.Context, sandbox phase1Sandbox, invocations []phase1Invocation) map[string]phase1CommandResult {
	start := make(chan struct{})
	results := make(chan phase1InvocationResult, len(invocations))
	for _, invocation := range invocations {
		invocation := invocation
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
			defer cancel()
			results <- phase1InvocationResult{
				name:   invocation.name,
				result: phase1RunCommand(ctx, sandbox, invocation.binary, invocation.args...),
			}
		}()
	}
	close(start)
	collected := make(map[string]phase1CommandResult, len(invocations))
	for range invocations {
		result := <-results
		collected[result.name] = result.result
	}
	return collected
}

// phase1DecodeConcurrentRead validates the status and snapshot that raced with project registration.
func phase1DecodeConcurrentRead(t *testing.T, statusResult phase1CommandResult, snapshotResult phase1CommandResult) {
	t.Helper()

	var status control.DaemonStatus
	if err := statusResult.decodeJSON(&status); err != nil {
		t.Fatalf("decode concurrent daemon status: %v", err)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("validate concurrent daemon status: %v", err)
	}
	var snapshot domain.Snapshot
	if err := snapshotResult.decodeJSON(&snapshot); err != nil {
		t.Fatalf("decode concurrent daemon snapshot: %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("validate concurrent daemon snapshot: %v", err)
	}
}

// phase1DecodeRegistration parses one machine-readable add result without accepting command diagnostics.
func phase1DecodeRegistration(t *testing.T, result phase1CommandResult) control.ProjectRegistration {
	t.Helper()

	var registration control.ProjectRegistration
	if err := result.decodeJSON(&registration); err != nil {
		t.Fatalf("decode project registration: %v", err)
	}
	if err := registration.Validate(); err != nil {
		t.Fatalf("validate project registration: %v", err)
	}
	return registration
}

// phase1AssertRegistrationReplay proves concurrent natural-identity registration creates one project and replays it once.
func phase1AssertRegistrationReplay(t *testing.T, first control.ProjectRegistration, second control.ProjectRegistration) {
	t.Helper()

	if first.Created == second.Created {
		t.Fatalf("concurrent registration created flags = %t and %t, want exactly one creation", first.Created, second.Created)
	}
	if first.Revision != second.Revision || !reflect.DeepEqual(first.Project, second.Project) {
		t.Fatalf("registration replay differs:\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

// phase1WriteProjectFixture creates only the marker and allowlisted display metadata consumed by project discovery.
func phase1WriteProjectFixture(t *testing.T, sandbox phase1Sandbox, evidence *phase1Evidence, leaf string, name string) string {
	t.Helper()

	root := filepath.Join(sandbox.root, "projects", leaf)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create project fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goforj.yml"), []byte("module_name: example.test/"+leaf+"\n"), 0o600); err != nil {
		t.Fatalf("write project fixture marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("APP_NAME='"+name+"'\n"), 0o600); err != nil {
		t.Fatalf("write project fixture name: %v", err)
	}
	evidence.addRedaction(root)
	return root
}

// phase1RequireProjectSnapshot finds exactly one requested project in a validated authoritative snapshot.
func phase1RequireProjectSnapshot(t *testing.T, snapshot domain.Snapshot, projectID domain.ProjectID) domain.ProjectSnapshot {
	t.Helper()

	var matches []domain.ProjectSnapshot
	for _, project := range snapshot.Projects {
		if project.ID == projectID {
			matches = append(matches, project)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("snapshot contains %d copies of project %s", len(matches), projectID)
	}
	return matches[0]
}

// phase1ReadSnapshot obtains a canonical CLI snapshot after a lifecycle boundary.
func phase1ReadSnapshot(t *testing.T, parent context.Context, configuration phase1Config, sandbox phase1Sandbox) domain.Snapshot {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	result := phase1RunCommand(ctx, sandbox, configuration.cliBinary, "daemon", "snapshot")
	var snapshot domain.Snapshot
	if err := result.decodeJSON(&snapshot); err != nil {
		t.Fatalf("decode CLI daemon snapshot: %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("validate CLI daemon snapshot: %v", err)
	}
	return snapshot
}

// phase1RequireGracefulStop proves acknowledgement, process exit, and observer termination belong to one joined shutdown.
func phase1RequireGracefulStop(
	t *testing.T,
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	process *phase1DaemonProcess,
	observer *control.Client,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1ShutdownTimeout)
	defer cancel()
	if err := phase1StopDaemon(ctx, configuration, sandbox, process); err != nil {
		t.Fatalf("gracefully stop daemon: %v", err)
	}
	if err := phase1WaitObserverDone(ctx, observer); err != nil {
		t.Fatalf("desktop observer did not observe graceful daemon stop: %v", err)
	}
	_ = observer.Close()
}

// phase1ReadDurableEvidence invokes the isolated state inspector while no daemon owns the database.
func phase1ReadDurableEvidence(
	t *testing.T,
	parent context.Context,
	sandbox phase1Sandbox,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	intentID domain.IntentID,
) phase1DurableEvidence {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	evidence, err := phase1RunStateSubprocess(
		ctx,
		sandbox,
		phase1StateHelperInspectMode,
		projectID,
		operationID,
		intentID,
	)
	if err != nil {
		t.Fatalf("inspect durable operation %s: %v", operationID, err)
	}
	return evidence
}

// phase1AssertDurableEvidence verifies one exact project intent and its strictly ordered persisted transition chain.
func phase1AssertDurableEvidence(
	t *testing.T,
	evidence phase1DurableEvidence,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	intentID domain.IntentID,
	state domain.OperationState,
	projectPresent bool,
	networkInitialized bool,
	projectCount int,
	activeOperations int,
	transitionStates []domain.OperationState,
) {
	t.Helper()

	if evidence.ProjectID != projectID || evidence.OperationID != operationID || evidence.IntentID != intentID {
		t.Fatalf(
			"durable identity = %s/%s/%s, want %s/%s/%s",
			evidence.ProjectID,
			evidence.OperationID,
			evidence.IntentID,
			projectID,
			operationID,
			intentID,
		)
	}
	if evidence.OperationState != state ||
		evidence.ProjectPresent != projectPresent ||
		evidence.NetworkInitialized != networkInitialized ||
		evidence.ProjectCount != projectCount ||
		evidence.ActiveOperationCount != activeOperations {
		t.Fatalf(
			"durable state = %s project_present=%t projects=%d network_initialized=%t active=%d, want %s project_present=%t projects=%d network_initialized=%t active=%d",
			evidence.OperationState,
			evidence.ProjectPresent,
			evidence.ProjectCount,
			evidence.NetworkInitialized,
			evidence.ActiveOperationCount,
			state,
			projectPresent,
			projectCount,
			networkInitialized,
			activeOperations,
		)
	}
	if !slices.Equal(evidence.TransitionStates, transitionStates) {
		t.Fatalf("transition states = %v, want %v", evidence.TransitionStates, transitionStates)
	}
	if len(evidence.TransitionSequences) != len(transitionStates) {
		t.Fatalf("transition sequences = %v, want %d entries", evidence.TransitionSequences, len(transitionStates))
	}
	for index := 1; index < len(evidence.TransitionSequences); index++ {
		if evidence.TransitionSequences[index] <= evidence.TransitionSequences[index-1] {
			t.Fatalf("transition sequences are not strictly increasing: %v", evidence.TransitionSequences)
		}
	}
	if len(evidence.TransitionSequences) == 0 || evidence.OperationRevision != evidence.TransitionSequences[len(evidence.TransitionSequences)-1] {
		t.Fatalf("operation revision %d does not own the final transition %v", evidence.OperationRevision, evidence.TransitionSequences)
	}
	if evidence.Sequence < evidence.OperationRevision {
		t.Fatalf("snapshot sequence %d precedes operation revision %d", evidence.Sequence, evidence.OperationRevision)
	}
}

// phase1AssertProjectAbsent rejects stale project projection after a successful unregister boundary.
func phase1AssertProjectAbsent(t *testing.T, snapshot domain.Snapshot, projectID domain.ProjectID) {
	t.Helper()

	for _, project := range snapshot.Projects {
		if project.ID == projectID {
			t.Fatalf("project %s remains in authoritative snapshot", projectID)
		}
	}
}

// phase1AddProject registers one fixture through the real production CLI.
func phase1AddProject(
	t *testing.T,
	parent context.Context,
	configuration phase1Config,
	sandbox phase1Sandbox,
	path string,
) control.ProjectRegistration {
	t.Helper()

	ctx, cancel := context.WithTimeout(parent, phase1CommandTimeout)
	defer cancel()
	return phase1DecodeRegistration(t, phase1RunCommand(ctx, sandbox, configuration.cliBinary, "add", path, "--json"))
}

// phase1DecodeRemoval parses and validates one machine-readable terminal project removal.
func phase1DecodeRemoval(t *testing.T, result phase1CommandResult) control.ProjectUnregistration {
	t.Helper()

	var removal control.ProjectUnregistration
	if err := result.decodeJSON(&removal); err != nil {
		t.Fatalf("decode project removal: %v", err)
	}
	if err := removal.Validate(); err != nil {
		t.Fatalf("validate project removal: %v", err)
	}
	return removal
}

// phase1AssertRemovalReplay proves concurrent callers converge on one terminal operation and revision.
func phase1AssertRemovalReplay(
	t *testing.T,
	first control.ProjectUnregistration,
	second control.ProjectUnregistration,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) {
	t.Helper()

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("concurrent removal replay differs:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if first.Operation.ProjectID != projectID || first.Operation.IntentID != intentID {
		t.Fatalf("removal correlation = %s/%s, want %s/%s", first.Operation.ProjectID, first.Operation.IntentID, projectID, intentID)
	}
	if first.Operation.State != domain.OperationSucceeded {
		t.Fatalf("concurrent removal state = %s, want succeeded", first.Operation.State)
	}
}
