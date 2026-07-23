package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/desktop/internal/networkprerequisite"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/networkresolverpolicymigrationapproval"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/projectapproval"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// fakeControlClient keeps lifecycle and response behavior explicit in desktop adapter tests.
type fakeControlClient struct {
	mu                           sync.Mutex
	status                       control.DaemonStatus
	statusErr                    error
	snapshot                     domain.Snapshot
	snapshotErr                  error
	snapshotHook                 func()
	registration                 control.ProjectRegistration
	registerErr                  error
	registerPath                 string
	networkSetup                 control.NetworkSetupOperation
	networkSetupErr              error
	networkSetupReq              control.StartNetworkSetupRequest
	networkSetupHook             func(control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	setupPreparation             control.NetworkSetupApprovalPreparation
	setupPrepareErr              error
	setupPrepareReq              control.PrepareNetworkSetupApprovalRequest
	setupConfirmation            control.NetworkSetupApprovalConfirmation
	setupConfirmErr              error
	setupConfirmReq              control.ConfirmNetworkSetupApprovalRequest
	resolverSetup                control.NetworkResolverSetupOperation
	resolverSetupErr             error
	resolverSetupReq             control.StartNetworkResolverSetupRequest
	resolverSetupHook            func(control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error)
	resolverPreparation          control.NetworkResolverSetupApprovalPreparation
	resolverPrepareErr           error
	resolverPrepareReq           control.PrepareNetworkResolverSetupApprovalRequest
	resolverConfirmation         control.NetworkResolverSetupApprovalConfirmation
	resolverConfirmErr           error
	resolverConfirmReq           control.ConfirmNetworkResolverSetupApprovalRequest
	migration                    control.NetworkResolverPolicyMigrationOperation
	migrationErr                 error
	migrationReqs                []control.StartNetworkResolverPolicyMigrationRequest
	migrationHook                func(control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error)
	dataPlaneSetup               control.NetworkDataPlaneSetupOperation
	dataPlaneSetupErr            error
	dataPlaneSetupReq            control.StartNetworkDataPlaneSetupRequest
	dataPlaneSetupHook           func(control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error)
	dataPlaneTrustPreparation    control.NetworkDataPlaneTrustApprovalPreparation
	dataPlaneTrustPrepareErr     error
	dataPlaneTrustPrepareReq     control.PrepareNetworkDataPlaneTrustApprovalRequest
	dataPlaneTrustConfirmation   control.NetworkDataPlaneSetupOperation
	dataPlaneTrustConfirmErr     error
	dataPlaneTrustConfirmReq     control.ConfirmNetworkDataPlaneTrustApprovalRequest
	dataPlaneLowPortPreparation  control.NetworkDataPlaneLowPortApprovalPreparation
	dataPlaneLowPortPrepareErr   error
	dataPlaneLowPortPrepareReq   control.PrepareNetworkDataPlaneLowPortApprovalRequest
	dataPlaneLowPortConfirmation control.NetworkDataPlaneSetupConfirmation
	dataPlaneLowPortConfirmErr   error
	dataPlaneLowPortConfirmReq   control.ConfirmNetworkDataPlaneLowPortApprovalRequest
	activity                     control.ProjectActivity
	activityErr                  error
	activityRequest              control.ProjectActivityRequest
	activityHook                 func(context.Context, control.ProjectActivityRequest) (control.ProjectActivity, error)
	serviceLogs                  control.ServiceLogs
	serviceLogsErr               error
	serviceLogsRequest           control.ServiceLogsRequest
	serviceLogsHook              func(context.Context, control.ServiceLogsRequest) (control.ServiceLogs, error)
	repairInspection             control.ProjectRuntimeRepairInspection
	repairInspectionErr          error
	repairInspectRequest         control.InspectProjectRuntimeRepairRequest
	repairConfirmation           control.ProjectRuntimeRepairConfirmation
	repairConfirmErr             error
	repairConfirmRequest         control.ConfirmProjectRuntimeRepairRequest
	startLifecycle               control.ProjectLifecycleOperation
	startErr                     error
	startRequest                 control.StartProjectRequest
	stopLifecycle                control.ProjectLifecycleOperation
	stopErr                      error
	stopRequest                  control.StopProjectRequest
	restartLifecycle             control.ProjectLifecycleOperation
	restartErr                   error
	restartRequest               control.RestartProjectRequest
	unregistration               control.ProjectUnregistration
	unregisterErr                error
	unregisterRequest            control.UnregisterProjectRequest
	projectPreparation           control.ProjectUnregisterApprovalPreparation
	projectPrepareErr            error
	projectPrepareReq            control.PrepareProjectUnregisterApprovalRequest
	projectConfirmation          control.ProjectUnregisterApprovalConfirmation
	projectConfirmErr            error
	projectConfirmReq            control.ConfirmProjectUnregisterApprovalRequest
	done                         chan struct{}
	closeOnce                    sync.Once
	closeCount                   atomic.Int32
}

// fakeNetworkSetupApprovalRunner records one exact setup selection and returns its configured bounded outcome.
type fakeNetworkSetupApprovalRunner struct {
	requests []networksetupapproval.Request
	outcome  networksetupapproval.Outcome
	err      error
	execute  func(context.Context, int, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// fakeNetworkResolverSetupApprovalRunner records one exact resolver selection and returns its configured bounded outcome.
type fakeNetworkResolverSetupApprovalRunner struct {
	requests []networkresolverapproval.Request
	outcome  networkresolverapproval.Outcome
	err      error
	execute  func(context.Context, int, networkresolverapproval.Request) (networkresolverapproval.Outcome, error)
}

// fakeNetworkResolverPolicyMigrationApprovalRunner records one exact migration selection without opening native consent.
type fakeNetworkResolverPolicyMigrationApprovalRunner struct {
	requests []networkresolverpolicymigrationapproval.Request
	outcome  networkresolverpolicymigrationapproval.Outcome
	err      error
	execute  func(context.Context, int, networkresolverpolicymigrationapproval.Request) (networkresolverpolicymigrationapproval.Outcome, error)
}

// fakeNetworkDataPlaneApprovalRunner records exact trust and low-port selections without opening native consent.
type fakeNetworkDataPlaneApprovalRunner struct {
	trustRequests   []networkdataplaneapproval.Request
	lowPortRequests []networkdataplaneapproval.Request
	trustOutcome    networkdataplaneapproval.TrustOutcome
	lowPortOutcome  networkdataplaneapproval.LowPortOutcome
	trustErr        error
	lowPortErr      error
	trustExecute    func(context.Context, int, networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error)
	lowPortExecute  func(context.Context, int, networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error)
}

// fakeProjectRemovalApprovalRunner records one exact unregister selection without opening native consent.
type fakeProjectRemovalApprovalRunner struct {
	requests []projectapproval.Request
	outcome  projectapproval.Outcome
	err      error
	execute  func(context.Context, int, projectapproval.Request) (projectapproval.Outcome, error)
}

// Execute records the selected setup revision without opening native consent.
func (runner *fakeNetworkSetupApprovalRunner) Execute(ctx context.Context, request networksetupapproval.Request) (networksetupapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, len(runner.requests), request)
	}
	return runner.outcome, runner.err
}

// Execute records the selected resolver revision without opening native consent.
func (runner *fakeNetworkResolverSetupApprovalRunner) Execute(ctx context.Context, request networkresolverapproval.Request) (networkresolverapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, len(runner.requests), request)
	}
	return runner.outcome, runner.err
}

// Execute records the selected migration revision without opening native consent.
func (runner *fakeNetworkResolverPolicyMigrationApprovalRunner) Execute(ctx context.Context, request networkresolverpolicymigrationapproval.Request) (networkresolverpolicymigrationapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, len(runner.requests), request)
	}
	return runner.outcome, runner.err
}

// ExecuteTrust records the exact trust selection and returns its configured bounded outcome.
func (runner *fakeNetworkDataPlaneApprovalRunner) ExecuteTrust(ctx context.Context, request networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error) {
	runner.trustRequests = append(runner.trustRequests, request)
	if runner.trustExecute != nil {
		return runner.trustExecute(ctx, len(runner.trustRequests), request)
	}
	return runner.trustOutcome, runner.trustErr
}

// ExecuteLowPorts records the exact low-port selection and returns its configured bounded outcome.
func (runner *fakeNetworkDataPlaneApprovalRunner) ExecuteLowPorts(ctx context.Context, request networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error) {
	runner.lowPortRequests = append(runner.lowPortRequests, request)
	if runner.lowPortExecute != nil {
		return runner.lowPortExecute(ctx, len(runner.lowPortRequests), request)
	}
	return runner.lowPortOutcome, runner.lowPortErr
}

// Execute records the selected unregister revision without opening native consent.
func (runner *fakeProjectRemovalApprovalRunner) Execute(ctx context.Context, request projectapproval.Request) (projectapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, len(runner.requests), request)
	}
	return runner.outcome, runner.err
}

// fakeNetworkPrerequisiteEnsurer records the bounded source-development repair handoff.
type fakeNetworkPrerequisiteEnsurer struct {
	calls int
	err   error
}

// Ensure records one attempted repair and returns its configured native result.
func (ensurer *fakeNetworkPrerequisiteEnsurer) Ensure(context.Context) error {
	ensurer.calls++
	return ensurer.err
}

// newFakeControlClient creates a connected test client with a valid replacement snapshot.
func newFakeControlClient() *fakeControlClient {
	return &fakeControlClient{
		status: control.DaemonStatus{
			State:                 control.DaemonStateReady,
			Build:                 control.Build{Version: "test"},
			Protocol:              rpc.Version{Major: 1},
			Capabilities:          []rpc.Capability{control.CapabilityNetworkResolverPolicyMigrationV1, control.CapabilityV1},
			SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
			Sequence:              8,
		},
		snapshot:           testSnapshot(),
		registration:       testRegistration(),
		networkSetup:       testNetworkSetupOperation(domain.OperationSucceeded, 10),
		resolverSetup:      testNetworkResolverSetupOperation(domain.OperationSucceeded, 13),
		dataPlaneSetup:     testNetworkDataPlaneSetupOperation(domain.OperationSucceeded, "completed", 17),
		activity:           testProjectActivity(),
		serviceLogs:        testServiceLogs(),
		repairInspection:   testProjectRuntimeRepairInspection(),
		repairConfirmation: testProjectRuntimeRepairConfirmation(),
		startLifecycle:     testProjectLifecycle(domain.OperationKindProjectStart, "desktop-start-orders"),
		stopLifecycle:      testProjectLifecycle(domain.OperationKindProjectStop, "desktop-stop-orders"),
		restartLifecycle:   testProjectLifecycle(domain.OperationKindProjectRestart, "desktop-restart-orders"),
		unregistration:     testUnregistration(),
		projectConfirmation: testProjectRemovalApprovalConfirmation(
			testProjectRemovalOperation(domain.OperationRequiresApproval, 9),
			11,
		),
		done: make(chan struct{}),
	}
}

// StartNetworkSetup records the singleton setup identity and returns the configured durable operation.
func (client *fakeControlClient) StartNetworkSetup(_ context.Context, request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.networkSetupReq = request
	if client.networkSetupHook != nil {
		return client.networkSetupHook(request)
	}
	return client.networkSetup, client.networkSetupErr
}

// PrepareNetworkSetupApproval records the selected operation revision for executor-backed tests.
func (client *fakeControlClient) PrepareNetworkSetupApproval(_ context.Context, request control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.setupPrepareReq = request
	return client.setupPreparation, client.setupPrepareErr
}

// ConfirmNetworkSetupApproval records the helper evidence selected for durable confirmation.
func (client *fakeControlClient) ConfirmNetworkSetupApproval(_ context.Context, request control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	select {
	case <-client.done:
		return control.NetworkSetupApprovalConfirmation{}, errors.New("control connection is closed")
	default:
	}
	client.setupConfirmReq = request
	return client.setupConfirmation, client.setupConfirmErr
}

// StartNetworkResolverSetup records the stable resolver setup identity and returns the configured durable operation.
func (client *fakeControlClient) StartNetworkResolverSetup(_ context.Context, request control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.resolverSetupReq = request
	if client.resolverSetupHook != nil {
		return client.resolverSetupHook(request)
	}
	return client.resolverSetup, client.resolverSetupErr
}

// StartNetworkResolverPolicyMigration records the stable migration intent and returns its scripted durable operation.
func (client *fakeControlClient) StartNetworkResolverPolicyMigration(_ context.Context, request control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.migrationReqs = append(client.migrationReqs, request)
	if client.migrationHook != nil {
		return client.migrationHook(request)
	}
	return client.migration, client.migrationErr
}

// PrepareNetworkResolverPolicyMigrationApproval is unreachable in app tests because the executor is replaced by a scripted runner.
func (*fakeControlClient) PrepareNetworkResolverPolicyMigrationApproval(context.Context, control.PrepareNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
	return control.NetworkResolverPolicyMigrationApprovalPreparation{}, errors.New("migration approval executor is not configured")
}

// ConfirmNetworkResolverPolicyMigrationApproval is unreachable in app tests because the executor is replaced by a scripted runner.
func (*fakeControlClient) ConfirmNetworkResolverPolicyMigrationApproval(context.Context, control.ConfirmNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, errors.New("migration approval executor is not configured")
}

// StartNetworkDataPlaneSetup records the stable trusted-ingress identity and returns its scripted durable operation.
func (client *fakeControlClient) StartNetworkDataPlaneSetup(_ context.Context, request control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPlaneSetupReq = request
	if client.dataPlaneSetupHook != nil {
		return client.dataPlaneSetupHook(request)
	}
	return client.dataPlaneSetup, client.dataPlaneSetupErr
}

// PrepareNetworkDataPlaneTrustApproval records the exact trust operation boundary.
func (client *fakeControlClient) PrepareNetworkDataPlaneTrustApproval(_ context.Context, request control.PrepareNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPlaneTrustPrepareReq = request
	return client.dataPlaneTrustPreparation, client.dataPlaneTrustPrepareErr
}

// ConfirmNetworkDataPlaneTrustApproval records the trust evidence selected for durable phase advancement.
func (client *fakeControlClient) ConfirmNetworkDataPlaneTrustApproval(_ context.Context, request control.ConfirmNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneSetupOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPlaneTrustConfirmReq = request
	return client.dataPlaneTrustConfirmation, client.dataPlaneTrustConfirmErr
}

// PrepareNetworkDataPlaneLowPortApproval records the exact low-port operation boundary.
func (client *fakeControlClient) PrepareNetworkDataPlaneLowPortApproval(_ context.Context, request control.PrepareNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPlaneLowPortPrepareReq = request
	return client.dataPlaneLowPortPreparation, client.dataPlaneLowPortPrepareErr
}

// ConfirmNetworkDataPlaneLowPortApproval records the low-port evidence selected for durable completion.
func (client *fakeControlClient) ConfirmNetworkDataPlaneLowPortApproval(_ context.Context, request control.ConfirmNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneSetupConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPlaneLowPortConfirmReq = request
	return client.dataPlaneLowPortConfirmation, client.dataPlaneLowPortConfirmErr
}

// PrepareNetworkResolverSetupApproval records the selected resolver operation revision for executor-backed tests.
func (client *fakeControlClient) PrepareNetworkResolverSetupApproval(_ context.Context, request control.PrepareNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.resolverPrepareReq = request
	return client.resolverPreparation, client.resolverPrepareErr
}

// ConfirmNetworkResolverSetupApproval records the resolver evidence selected for durable confirmation.
func (client *fakeControlClient) ConfirmNetworkResolverSetupApproval(_ context.Context, request control.ConfirmNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	select {
	case <-client.done:
		return control.NetworkResolverSetupApprovalConfirmation{}, errors.New("control connection is closed")
	default:
	}
	client.resolverConfirmReq = request
	return client.resolverConfirmation, client.resolverConfirmErr
}

// Status returns the configured daemon status or test failure.
func (client *fakeControlClient) Status(_ context.Context) (control.DaemonStatus, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.status, client.statusErr
}

// Snapshot returns a defensive copy of the configured replacement snapshot.
func (client *fakeControlClient) Snapshot(_ context.Context) (domain.Snapshot, error) {
	client.mu.Lock()
	snapshot := cloneSnapshot(client.snapshot)
	err := client.snapshotErr
	hook := client.snapshotHook
	client.mu.Unlock()
	if hook != nil {
		hook()
	}
	return snapshot, err
}

// RegisterProject records the selected path and returns the configured authoritative mutation result.
func (client *fakeControlClient) RegisterProject(_ context.Context, request control.RegisterProjectRequest) (control.ProjectRegistration, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.registerPath = request.Path
	return client.registration, client.registerErr
}

// ProjectActivity records the current-session cursor and returns the configured bounded output.
func (client *fakeControlClient) ProjectActivity(ctx context.Context, request control.ProjectActivityRequest) (control.ProjectActivity, error) {
	client.mu.Lock()
	client.activityRequest = request
	hook := client.activityHook
	activity := client.activity
	err := client.activityErr
	client.mu.Unlock()
	if hook != nil {
		return hook(ctx, request)
	}
	return activity, err
}

// ServiceLogs records the current-session service cursor and returns the configured bounded output.
func (client *fakeControlClient) ServiceLogs(ctx context.Context, request control.ServiceLogsRequest) (control.ServiceLogs, error) {
	client.mu.Lock()
	client.serviceLogsRequest = request
	hook := client.serviceLogsHook
	logs := client.serviceLogs
	err := client.serviceLogsErr
	client.mu.Unlock()
	if hook != nil {
		return hook(ctx, request)
	}
	return logs, err
}

// InspectProjectRuntimeRepair records the selected project and returns the configured bounded inspection.
func (client *fakeControlClient) InspectProjectRuntimeRepair(
	_ context.Context,
	request control.InspectProjectRuntimeRepairRequest,
) (control.ProjectRuntimeRepairInspection, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.repairInspectRequest = request
	return client.repairInspection, client.repairInspectionErr
}

// ConfirmProjectRuntimeRepair records only the opaque prior selection and returns the configured stopped projection.
func (client *fakeControlClient) ConfirmProjectRuntimeRepair(
	_ context.Context,
	request control.ConfirmProjectRuntimeRepairRequest,
) (control.ProjectRuntimeRepairConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.repairConfirmRequest = request
	return client.repairConfirmation, client.repairConfirmErr
}

// StartProject records the stable lifecycle identity and returns the configured start operation.
func (client *fakeControlClient) StartProject(_ context.Context, request control.StartProjectRequest) (control.ProjectLifecycleOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.startRequest = request
	return client.startLifecycle, client.startErr
}

// StopProject records the stable lifecycle identity and returns the configured stop operation.
func (client *fakeControlClient) StopProject(_ context.Context, request control.StopProjectRequest) (control.ProjectLifecycleOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.stopRequest = request
	return client.stopLifecycle, client.stopErr
}

// RestartProject records the stable lifecycle identity and returns the configured restart operation.
func (client *fakeControlClient) RestartProject(_ context.Context, request control.RestartProjectRequest) (control.ProjectLifecycleOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.restartRequest = request
	return client.restartLifecycle, client.restartErr
}

// UnregisterProject records the stable removal identity and returns the configured authoritative operation.
func (client *fakeControlClient) UnregisterProject(_ context.Context, request control.UnregisterProjectRequest) (control.ProjectUnregistration, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.unregisterRequest = request
	return client.unregistration, client.unregisterErr
}

// PrepareProjectUnregisterApproval records the exact replayed operation revision selected for helper launch.
func (client *fakeControlClient) PrepareProjectUnregisterApproval(
	_ context.Context,
	request control.PrepareProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.projectPrepareReq = request
	return client.projectPreparation, client.projectPrepareErr
}

// ConfirmProjectUnregisterApproval records the exact replayed operation revision selected for durable confirmation.
func (client *fakeControlClient) ConfirmProjectUnregisterApproval(
	_ context.Context,
	request control.ConfirmProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	select {
	case <-client.done:
		return control.ProjectUnregisterApprovalConfirmation{}, errors.New("control connection is closed")
	default:
	}
	client.projectConfirmReq = request
	return client.projectConfirmation, client.projectConfirmErr
}

// Done exposes terminal connection state to the desktop owner loop.
func (client *fakeControlClient) Done() <-chan struct{} {
	return client.done
}

// Close terminates the fake connection exactly once.
func (client *fakeControlClient) Close() error {
	client.closeOnce.Do(func() {
		client.closeCount.Add(1)
		close(client.done)
	})
	return nil
}

// TestNewAppWiresProductionDependencies covers the zero-configuration Wails composition without starting a daemon connection.
func TestNewAppWiresProductionDependencies(t *testing.T) {
	t.Parallel()

	app := NewApp()
	if app.clientFactory == nil || app.open == nil || app.choose == nil || app.setupApproval == nil || app.resolverApproval == nil || app.dataPlaneApproval == nil || app.projectApproval == nil || app.setupPrerequisite == nil || app.setupIntent == nil || app.resolverIntent == nil || app.dataPlaneIntent == nil || app.presentation == nil || app.wait == nil {
		t.Fatal("NewApp() left a production dependency unwired")
	}
}

// TestSetupNetworkDataPlaneFlow keeps the three durable setup intents ordered through trusted ingress.
func TestSetupNetworkDataPlaneFlow(t *testing.T) {
	t.Parallel()

	t.Run("completed replay remains unprivileged", func(t *testing.T) {
		app, client := connectedTestApp()
		app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
			t.Fatal("pool approval constructed")
			return nil
		}
		app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner {
			t.Fatal("resolver approval constructed")
			return nil
		}
		app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner {
			t.Fatal("data-plane approval constructed")
			return nil
		}
		result, err := app.SetupNetwork()
		if err != nil || result != client.networkSetup {
			t.Fatalf("SetupNetwork() = (%#v, %v), want original pool operation", result, err)
		}
		if client.networkSetupReq.IntentID != networkSetupIntentID || client.resolverSetupReq.IntentID != networkResolverSetupIntentID || client.dataPlaneSetupReq.IntentID != networkDataPlaneSetupIntentID {
			t.Fatalf("stable intents = %q/%q/%q", client.networkSetupReq.IntentID, client.resolverSetupReq.IntentID, client.dataPlaneSetupReq.IntentID)
		}
	})

	t.Run("first run orders all approvals on one client", func(t *testing.T) {
		app, client := connectedTestApp()
		client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
		poolConfirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
		client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
		resolverConfirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 13, 14)
		client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15)
		trusted := testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
		lowPortConfirmation := testNetworkDataPlaneSetupConfirmation(trusted, 17, 18)
		pool := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &poolConfirmation}}
		resolver := &fakeNetworkResolverSetupApprovalRunner{outcome: networkresolverapproval.Outcome{State: networkresolverapproval.Succeeded, Confirmation: &resolverConfirmation}}
		dataPlane := &fakeNetworkDataPlaneApprovalRunner{trustOutcome: networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded, Setup: &trusted}, lowPortOutcome: networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: &lowPortConfirmation}}
		app.setupApproval = func(got networksetupapproval.Client) networkSetupApprovalRunner {
			if got != client {
				t.Fatal("pool used another client")
			}
			return pool
		}
		app.resolverApproval = func(got networkresolverapproval.Client) networkResolverSetupApprovalRunner {
			if got != client || len(pool.requests) != 1 {
				t.Fatal("resolver order/client")
			}
			return resolver
		}
		app.dataPlaneApproval = func(got networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner {
			if got != client || len(resolver.requests) != 1 {
				t.Fatal("data-plane order/client")
			}
			return dataPlane
		}
		result, err := app.SetupNetwork()
		if err != nil || result.Operation.ID != poolConfirmation.Operation.ID || result.Revision != poolConfirmation.Revision {
			t.Fatalf("SetupNetwork() = (%#v, %v)", result, err)
		}
		if got, want := pool.requests, []networksetupapproval.Request{{OperationID: client.networkSetup.Operation.ID, ExpectedOperationRevision: 7}}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("pool requests = %#v, want %#v", got, want)
		}
		if got, want := resolver.requests, []networkresolverapproval.Request{{OperationID: client.resolverSetup.Operation.ID, ExpectedOperationRevision: 11}}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("resolver requests = %#v, want %#v", got, want)
		}
		if len(dataPlane.trustRequests) != 1 || dataPlane.trustRequests[0].ExpectedOperationRevision != 15 || len(dataPlane.lowPortRequests) != 1 || dataPlane.lowPortRequests[0].ExpectedOperationRevision != 16 {
			t.Fatalf("data-plane requests = %#v/%#v", dataPlane.trustRequests, dataPlane.lowPortRequests)
		}
	})

	t.Run("low-port replay skips trust", func(t *testing.T) {
		app, client := connectedTestApp()
		client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
		confirmation := testNetworkDataPlaneSetupConfirmation(client.dataPlaneSetup, 17, 18)
		runner := &fakeNetworkDataPlaneApprovalRunner{lowPortOutcome: networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: &confirmation}}
		app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }
		if _, err := app.SetupNetwork(); err != nil || len(runner.trustRequests) != 0 || len(runner.lowPortRequests) != 1 {
			t.Fatalf("SetupNetwork() = %v; trust/low = %d/%d", err, len(runner.trustRequests), len(runner.lowPortRequests))
		}
	})
}

// TestSetupNetworkDataPlaneReplaysIndeterminateApprovals keeps lost results behind the same durable intent.
func TestSetupNetworkDataPlaneReplaysIndeterminateApprovals(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, phase string }{{"trust", "awaiting trust approval"}, {"low port", "awaiting low-port approval"}} {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			first := testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, test.phase, 15)
			completed := testNetworkDataPlaneSetupOperation(domain.OperationSucceeded, "completed", 17)
			var requests []control.StartNetworkDataPlaneSetupRequest
			client.dataPlaneSetupHook = func(request control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
				requests = append(requests, request)
				if len(requests) == 1 {
					return first, nil
				}
				return completed, nil
			}
			runner := &fakeNetworkDataPlaneApprovalRunner{}
			if test.phase == "awaiting trust approval" {
				runner.trustOutcome, runner.trustErr = networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Indeterminate}, errors.New("trust response was lost")
			} else {
				runner.lowPortOutcome, runner.lowPortErr = networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Indeterminate}, errors.New("low-port response was lost")
			}
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }
			if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), "response was lost") {
				t.Fatalf("first SetupNetwork() error = %v", err)
			}
			if _, err := app.SetupNetwork(); err != nil {
				t.Fatalf("replayed SetupNetwork() error = %v", err)
			}
			if len(requests) != 2 || requests[0].IntentID != networkDataPlaneSetupIntentID || requests[1].IntentID != networkDataPlaneSetupIntentID {
				t.Fatalf("replay intents = %#v", requests)
			}
			if test.phase == "awaiting trust approval" && len(runner.trustRequests) != 1 || test.phase == "awaiting low-port approval" && len(runner.lowPortRequests) != 1 {
				t.Fatalf("approval replayed after lost %s response", test.name)
			}
		})
	}
}

// TestSetupNetworkDataPlaneRetryIntentBoundsFreshIdentities limits new identities to cancelled, retryable, and opaque fixed-intent failures.
func TestSetupNetworkDataPlaneRetryIntentBoundsFreshIdentities(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		terminal control.NetworkDataPlaneSetupOperation
		startErr error
		wantMint bool
	}{
		{"cancelled", testNetworkDataPlaneSetupOperation(domain.OperationCancelled, string(domain.OperationCancelled), 15), nil, true},
		{"retryable failed", testNetworkDataPlaneSetupOperation(domain.OperationFailed, string(domain.OperationFailed), 15), nil, true},
		{"internal", control.NetworkDataPlaneSetupOperation{}, rpc.WireError{Code: rpc.ErrorCodeInternal}, true},
		{"nonretryable failed", nonRetryableDataPlaneOperation(), nil, false},
		{"unsupported state", testNetworkDataPlaneSetupOperation(domain.OperationState("unsupported"), "unsupported", 15), nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			minted := 0
			app.dataPlaneIntent = func() (domain.IntentID, error) { minted++; return "intent-network-data-plane-setup-retry", nil }
			calls := 0
			client.dataPlaneSetupHook = func(request control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
				calls++
				if calls == 1 {
					return test.terminal, test.startErr
				}
				result := testNetworkDataPlaneSetupOperation(domain.OperationSucceeded, "completed", 17)
				result.Operation.IntentID = request.IntentID
				return result, nil
			}
			_, err := app.SetupNetwork()
			if test.wantMint && (err != nil || minted != 1 || calls != 2) {
				t.Fatalf("retry = err %v, mint/calls %d/%d", err, minted, calls)
			}
			if !test.wantMint && (err == nil || minted != 0 || calls != 1) {
				t.Fatalf("nonretryable = err %v, mint/calls %d/%d", err, minted, calls)
			}
		})
	}
	app, client := connectedTestApp()
	client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationCancelled, string(domain.OperationCancelled), 15)
	app.dataPlaneIntent = func() (domain.IntentID, error) { return "", errors.New("entropy unavailable") }
	if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), "create Harbor network data-plane setup retry: entropy unavailable") {
		t.Fatalf("entropy failure = %v", err)
	}
}

// TestSetupNetworkDataPlaneRejectsUntrustedTerminalRetriesBeforeMinting keeps malformed and crossed terminal responses outside retry authority.
func TestSetupNetworkDataPlaneRejectsUntrustedTerminalRetriesBeforeMinting(t *testing.T) {
	t.Parallel()

	malformedCancelled := testNetworkDataPlaneSetupOperation(domain.OperationCancelled, string(domain.OperationCancelled), 15)
	malformedCancelled.Operation.FinishedAt = nil
	malformedFailed := testNetworkDataPlaneSetupOperation(domain.OperationFailed, string(domain.OperationFailed), 15)
	malformedFailed.Operation.Problem.Code = ""
	crossCancelled := testNetworkDataPlaneSetupOperation(domain.OperationCancelled, string(domain.OperationCancelled), 15)
	crossCancelled.Operation.IntentID = "intent-other"
	crossFailed := testNetworkDataPlaneSetupOperation(domain.OperationFailed, string(domain.OperationFailed), 15)
	crossFailed.Operation.IntentID = "intent-other"

	for _, test := range []struct {
		name  string
		setup control.NetworkDataPlaneSetupOperation
	}{
		{name: "malformed cancelled", setup: malformedCancelled},
		{name: "malformed retryable failed", setup: malformedFailed},
		{name: "cross-intent cancelled", setup: crossCancelled},
		{name: "cross-intent retryable failed", setup: crossFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			minted := 0
			starts := 0
			app.dataPlaneIntent = func() (domain.IntentID, error) { minted++; return "intent-network-data-plane-setup-retry", nil }
			client.dataPlaneSetupHook = func(control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
				starts++
				return test.setup, nil
			}
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner {
				t.Fatal("approval runner constructed")
				return nil
			}
			if _, err := app.SetupNetwork(); err == nil || minted != 0 || starts != 1 {
				t.Fatalf("SetupNetwork() error/mints/starts = %v/%d/%d, want validation error, zero mints, and one start", err, minted, starts)
			}
		})
	}
}

// TestSetupNetworkDataPlaneRejectsUnsupportedSuccessAndApprovalPhases keeps daemon phases from selecting the wrong native boundary.
func TestSetupNetworkDataPlaneRejectsUnsupportedSuccessAndApprovalPhases(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup control.NetworkDataPlaneSetupOperation
	}{
		{name: "succeeded before completion", setup: testNetworkDataPlaneSetupOperation(domain.OperationSucceeded, "awaiting low-port approval", 17)},
		{name: "unknown approval phase", setup: testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting another approval", 15)},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			client.dataPlaneSetup = test.setup
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner {
				t.Fatal("approval runner constructed for unsupported phase")
				return nil
			}
			if _, err := app.SetupNetwork(); err == nil {
				t.Fatal("SetupNetwork() error = nil for unsupported data-plane phase")
			}
			if client.dataPlaneSetupReq.IntentID != networkDataPlaneSetupIntentID {
				t.Fatalf("StartNetworkDataPlaneSetup() intent = %q", client.dataPlaneSetupReq.IntentID)
			}
		})
	}
}

// nonRetryableDataPlaneOperation returns a terminal operation that must not mint another idempotency boundary.
func nonRetryableDataPlaneOperation() control.NetworkDataPlaneSetupOperation {
	operation := testNetworkDataPlaneSetupOperation(domain.OperationFailed, string(domain.OperationFailed), 15)
	operation.Operation.Problem.Retryable = false
	return operation
}

// TestNetworkDataPlaneApprovalValidationRejectsCrossedResponses keeps every phase and revision boundary fail-closed.
func TestNetworkDataPlaneApprovalValidationRejectsCrossedResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup control.NetworkDataPlaneSetupOperation
		trust networkdataplaneapproval.TrustOutcome
		low   networkdataplaneapproval.LowPortOutcome
		want  string
	}{
		{"wrong start intent", wrongIntentDataPlaneOperation(), networkdataplaneapproval.TrustOutcome{}, networkdataplaneapproval.LowPortOutcome{}, "another intent"},
		{"trust operation", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15), networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded, Setup: ptrDataPlane(testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16))}, networkdataplaneapproval.LowPortOutcome{}, "selected operation revision"},
		{"trust nonadvancing revision", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15), networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded, Setup: ptrDataPlane(testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 15))}, networkdataplaneapproval.LowPortOutcome{}, "selected operation revision"},
		{"trust wrong phase", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15), networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded, Setup: ptrDataPlane(testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 16))}, networkdataplaneapproval.LowPortOutcome{}, "selected operation revision"},
		{"low-port operation", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16), networkdataplaneapproval.TrustOutcome{}, networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: ptrDataPlaneConfirmation(testNetworkDataPlaneSetupConfirmation(testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16), 17, 18))}, "selected operation revision"},
		{"low-port nonadvancing revision", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16), networkdataplaneapproval.TrustOutcome{}, networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: ptrDataPlaneConfirmation(testNetworkDataPlaneSetupConfirmation(testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16), 15, 16))}, "selected operation revision"},
		{"inconsistent success", testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15), networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded}, networkdataplaneapproval.LowPortOutcome{}, "inconsistent evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			client.dataPlaneSetup = test.setup
			if test.name == "trust operation" {
				test.trust.Setup.Operation.ID = "other-operation"
			}
			if test.name == "low-port operation" {
				test.low.Confirmation.Operation.ID = "other-operation"
			}
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner {
				return &fakeNetworkDataPlaneApprovalRunner{trustOutcome: test.trust, lowPortOutcome: test.low}
			}
			if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestNetworkDataPlaneApprovalOutcomeGuidance preserves safe retry and refresh wording for every bounded outcome.
func TestNetworkDataPlaneApprovalOutcomeGuidance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, want string
		state      networkdataplaneapproval.State
	}{
		{"declined", "safe to retry", networkdataplaneapproval.Declined}, {"unavailable", "unavailable", networkdataplaneapproval.Unavailable}, {"failed", "without a problem description", networkdataplaneapproval.HelperFailed}, {"indeterminate", "refresh before retrying", networkdataplaneapproval.Indeterminate}, {"unsupported", "unsupported state", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := networkDataPlaneTrustApprovalError(networkdataplaneapproval.TrustOutcome{State: test.state}); !strings.Contains(err.Error(), test.want) {
				t.Fatalf("trust error = %v", err)
			}
			if err := networkDataPlaneLowPortApprovalError(networkdataplaneapproval.LowPortOutcome{State: test.state}); !strings.Contains(err.Error(), test.want) {
				t.Fatalf("low-port error = %v", err)
			}
		})
	}
}

// TestSetupNetworkRepairsDataPlaneApprovalsThroughTheSharedPrerequisiteBoundary bounds data-plane installation to one exact retry per phase.
func TestSetupNetworkRepairsDataPlaneApprovalsThroughTheSharedPrerequisiteBoundary(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"trust", "low port"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			ensurer := &fakeNetworkPrerequisiteEnsurer{}
			runner := &fakeNetworkDataPlaneApprovalRunner{}
			app.setupPrerequisite = ensurer
			app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }

			if phase == "trust" {
				client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15)
				trusted := testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
				confirmation := testNetworkDataPlaneSetupConfirmation(trusted, 17, 18)
				runner.trustExecute = func(_ context.Context, call int, _ networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error) {
					if call == 1 {
						return networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.HelperFailed, HelperFailure: &networkdataplaneapproval.HelperFailure{Code: helper.ErrorCodeAuthenticationFailed, Message: "helper ticket redemption failed"}}, nil
					}
					return networkdataplaneapproval.TrustOutcome{State: networkdataplaneapproval.Succeeded, Setup: &trusted}, nil
				}
				runner.lowPortOutcome = networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: &confirmation}
			} else {
				client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
				confirmation := testNetworkDataPlaneSetupConfirmation(client.dataPlaneSetup, 17, 18)
				runner.lowPortExecute = func(_ context.Context, call int, _ networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error) {
					if call == 1 {
						return networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.HelperFailed, HelperFailure: &networkdataplaneapproval.HelperFailure{Code: helper.ErrorCodeAuthenticationFailed, Message: "helper ticket redemption failed"}}, nil
					}
					return networkdataplaneapproval.LowPortOutcome{State: networkdataplaneapproval.Succeeded, Confirmation: &confirmation}, nil
				}
			}

			if _, err := app.SetupNetwork(); err != nil {
				t.Fatalf("SetupNetwork() error = %v", err)
			}
			if ensurer.calls != 1 {
				t.Fatalf("repair calls = %d, want 1", ensurer.calls)
			}
			if phase == "trust" {
				if len(runner.trustRequests) != 2 || runner.trustRequests[0] != runner.trustRequests[1] {
					t.Fatalf("trust retry crossed request boundary: %#v", runner.trustRequests)
				}
			} else if len(runner.lowPortRequests) != 2 || runner.lowPortRequests[0] != runner.lowPortRequests[1] {
				t.Fatalf("low-port retry crossed request boundary: %#v", runner.lowPortRequests)
			}
		})
	}
}

// TestSetupNetworkBoundsDataPlanePrerequisiteRepairAndPreservesFailures prevents helper repair loops and preserves uncertain or mutation outcomes.
func TestSetupNetworkBoundsDataPlanePrerequisiteRepairAndPreservesFailures(t *testing.T) {
	t.Parallel()

	type repairCase struct {
		name      string
		firstErr  error
		first     networkdataplaneapproval.State
		failure   *networkdataplaneapproval.HelperFailure
		repairErr error
		approvals int
		want      string
	}
	for _, phase := range []string{"trust", "low port"} {
		for _, test := range []repairCase{
			{name: "declined repair", firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired), repairErr: networkprerequisite.ErrDeclined, approvals: 1, want: "declined"},
			{name: "persistent missing helper", firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired), approvals: 2, want: "still cannot find the ticket directory"},
			{name: "persistent unsafe helper", firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperUnsafe), approvals: 2, want: "ownership, permissions, type, or ACLs"},
			{name: "persistent unavailable helper", first: networkdataplaneapproval.Unavailable, approvals: 2, want: "native helper is unavailable"},
			{name: "persistent authentication failure", first: networkdataplaneapproval.HelperFailed, failure: &networkdataplaneapproval.HelperFailure{Code: helper.ErrorCodeAuthenticationFailed, Message: "helper ticket redemption failed"}, approvals: 2, want: "could not authenticate a newly issued"},
		} {
			t.Run(phase+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				app, client := connectedTestApp()
				ensurer := &fakeNetworkPrerequisiteEnsurer{err: test.repairErr}
				runner := &fakeNetworkDataPlaneApprovalRunner{}
				app.setupPrerequisite = ensurer
				app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }
				if phase == "trust" {
					client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15)
					runner.trustExecute = func(_ context.Context, _ int, _ networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error) {
						return networkdataplaneapproval.TrustOutcome{State: test.first, HelperFailure: test.failure}, test.firstErr
					}
				} else {
					client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
					runner.lowPortExecute = func(_ context.Context, _ int, _ networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error) {
						return networkdataplaneapproval.LowPortOutcome{State: test.first, HelperFailure: test.failure}, test.firstErr
					}
				}

				if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
				}
				if ensurer.calls != 1 {
					t.Fatalf("repair calls = %d, want 1", ensurer.calls)
				}
				if phase == "trust" && len(runner.trustRequests) != test.approvals || phase == "low port" && len(runner.lowPortRequests) != test.approvals {
					t.Fatalf("approval calls = %d/%d, want exactly %d in %s", len(runner.trustRequests), len(runner.lowPortRequests), test.approvals, phase)
				}
			})
		}
	}

	for _, phase := range []string{"trust", "low port"} {
		for _, test := range []struct {
			name    string
			state   networkdataplaneapproval.State
			failure *networkdataplaneapproval.HelperFailure
			want    string
		}{
			{name: "indeterminate", state: networkdataplaneapproval.Indeterminate, want: "refresh before retrying"},
			{name: "mutation failure", state: networkdataplaneapproval.HelperFailed, failure: &networkdataplaneapproval.HelperFailure{Code: helper.ErrorCodeMutationFailed, Message: "mutation failed"}, want: "mutation failed"},
		} {
			t.Run(phase+" does not repair "+test.name, func(t *testing.T) {
				t.Parallel()
				app, client := connectedTestApp()
				ensurer := &fakeNetworkPrerequisiteEnsurer{}
				runner := &fakeNetworkDataPlaneApprovalRunner{}
				app.setupPrerequisite = ensurer
				app.dataPlaneApproval = func(networkdataplaneapproval.Client) networkDataPlaneSetupApprovalRunner { return runner }
				if phase == "trust" {
					client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting trust approval", 15)
					runner.trustOutcome = networkdataplaneapproval.TrustOutcome{State: test.state, HelperFailure: test.failure}
				} else {
					client.dataPlaneSetup = testNetworkDataPlaneSetupOperation(domain.OperationRequiresApproval, "awaiting low-port approval", 16)
					runner.lowPortOutcome = networkdataplaneapproval.LowPortOutcome{State: test.state, HelperFailure: test.failure}
				}

				if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
				}
				if ensurer.calls != 0 || phase == "trust" && len(runner.trustRequests) != 1 || phase == "low port" && len(runner.lowPortRequests) != 1 {
					t.Fatalf("repair/approval calls = %d/%d/%d, want 0 and one phase approval", ensurer.calls, len(runner.trustRequests), len(runner.lowPortRequests))
				}
			})
		}
	}
}

// wrongIntentDataPlaneOperation returns an otherwise valid daemon reply for a different stable intent.
func wrongIntentDataPlaneOperation() control.NetworkDataPlaneSetupOperation {
	operation := testNetworkDataPlaneSetupOperation(domain.OperationSucceeded, "completed", 17)
	operation.Operation.IntentID = "intent-other"
	return operation
}

// ptrDataPlane keeps table fixtures readable without sharing mutable operation values.
func ptrDataPlane(value control.NetworkDataPlaneSetupOperation) *control.NetworkDataPlaneSetupOperation {
	return &value
}

// ptrDataPlaneConfirmation keeps table fixtures readable without sharing mutable confirmation values.
func ptrDataPlaneConfirmation(value control.NetworkDataPlaneSetupConfirmation) *control.NetworkDataPlaneSetupConfirmation {
	return &value
}

// TestNewDesktopClientHonorsCancellation verifies the concrete adapter forwards the desktop request context.
func TestNewDesktopClientHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newDesktopClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop}); err == nil {
		t.Fatal("newDesktopClient() error = nil for cancelled dial")
	}
}

// TestAppLifecycleConnectsAsDesktopAndPublishesSnapshots covers startup, polling, and joined shutdown ownership.
func TestAppLifecycleConnectsAsDesktopAndPublishesSnapshots(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	roles := make(chan rpc.Role, 1)
	emitted := make(chan domain.Snapshot, 1)
	connections := make(chan ConnectionState, 2)
	app := newApp(
		func(_ context.Context, config control.ClientConfig) (controlClient, error) {
			roles <- config.Role
			return client, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			switch event {
			case snapshotEventName:
				emitted <- values[0].(domain.Snapshot)
			case connectionEventName:
				connections <- values[0].(ConnectionEvent).State
			default:
				t.Errorf("unexpected event = %q", event)
			}
		},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	)

	app.startup(context.Background())
	select {
	case role := <-roles:
		if role != rpc.RoleDesktop {
			t.Fatalf("role = %q, want %q", role, rpc.RoleDesktop)
		}
	case <-time.After(time.Second):
		t.Fatal("desktop client was not created")
	}
	select {
	case snapshot := <-emitted:
		if snapshot.Sequence != client.snapshot.Sequence {
			t.Fatalf("emitted sequence = %d, want %d", snapshot.Sequence, client.snapshot.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("initial snapshot was not emitted")
	}
	for _, want := range []ConnectionState{ConnectionConnecting, ConnectionConnected} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("connection state %q was not emitted", want)
		}
	}

	app.shutdown(context.Background())
	if client.closeCount.Load() != 1 {
		t.Fatalf("Close() count = %d, want 1", client.closeCount.Load())
	}
}

// TestStartupRejectsCompetingLifecycle prevents two owner goroutines from sharing one desktop instance.
func TestStartupRejectsCompetingLifecycle(t *testing.T) {
	t.Parallel()

	app := testApp()
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	defer func() {
		if recover() == nil {
			t.Fatal("startup() panic = nil for second active lifecycle")
		}
	}()
	app.startup(context.Background())
}

// TestSecondInstanceBeforeStartupIsInert keeps an early platform callback from using a missing Wails context.
func TestSecondInstanceBeforeStartupIsInert(t *testing.T) {
	t.Parallel()

	testApp().onSecondInstanceLaunch(options.SecondInstanceData{})
}

// TestSecondInstanceRestoresTheOwnedWindow verifies relaunch remains presentation-only.
func TestSecondInstanceRestoresTheOwnedWindow(t *testing.T) {
	t.Parallel()

	app := testApp()
	restored := false
	app.presentation = newPresentationController(
		func(context.Context) {},
		func(context.Context) { restored = true },
		func(context.Context) {},
	)
	app.presentation.startup(context.Background())
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if !restored {
		t.Fatal("onSecondInstanceLaunch() did not restore the Wails window")
	}
}

// TestAppReconnectsAfterDialFailure proves daemon startup order does not strand the desktop in a fixture state.
func TestAppReconnectsAfterDialFailure(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	var attempts atomic.Int32
	emitted := make(chan struct{}, 1)
	connections := make(chan ConnectionState, 4)
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("daemon is starting")
			}
			return client, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			if event == snapshotEventName {
				emitted <- struct{}{}
				return
			}
			connections <- values[0].(ConnectionEvent).State
		},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			if attempts.Load() == 1 {
				return true
			}
			<-ctx.Done()
			return false
		},
	)

	app.startup(context.Background())
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("snapshot was not emitted after reconnect")
	}
	app.shutdown(context.Background())
	if attempts.Load() != 2 {
		t.Fatalf("connection attempts = %d, want 2", attempts.Load())
	}
	for index, want := range []ConnectionState{
		ConnectionConnecting,
		ConnectionDisconnected,
		ConnectionConnecting,
		ConnectionConnected,
	} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("connection state %d = missing, want %q", index, want)
		}
	}
}

// TestRunStopsWhenReconnectWaitIsCancelled covers a daemon that remains unavailable through shutdown.
func TestRunStopsWhenReconnectWaitIsCancelled(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			return nil, errors.New("unavailable")
		},
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { waits.Add(1); return false },
	)
	done := make(chan struct{})
	app.run(context.Background(), done)
	select {
	case <-done:
	default:
		t.Fatal("run() did not report completion")
	}
	if waits.Load() != 1 {
		t.Fatalf("wait count = %d, want 1", waits.Load())
	}
}

// TestRunClosesClientWhenLifecycleEndsDuringDial covers a factory that returns after cancellation.
func TestRunClosesClientWhenLifecycleEndsDuringDial(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	ctx, cancel := context.WithCancel(context.Background())
	app := testApp()
	app.ctx = context.Background()
	app.clientFactory = func(context.Context, control.ClientConfig) (controlClient, error) {
		cancel()
		return client, nil
	}
	done := make(chan struct{})
	app.run(ctx, done)
	if client.closeCount.Load() != 1 {
		t.Fatalf("Close() count = %d, want 1", client.closeCount.Load())
	}
}

// TestPollStopsForContextConnectionAndRetryBoundaries covers every terminal polling decision.
func TestPollStopsForContextConnectionAndRetryBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("context", func(t *testing.T) {
		client := newFakeControlClient()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		testApp().poll(ctx, client)
	})

	t.Run("connection", func(t *testing.T) {
		client := newFakeControlClient()
		_ = client.Close()
		testApp().poll(context.Background(), client)
	})

	t.Run("snapshot failure", func(t *testing.T) {
		client := newFakeControlClient()
		client.snapshotErr = errors.New("read failed")
		var waits atomic.Int32
		app := newApp(
			func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
			func(context.Context, string, ...interface{}) { t.Fatal("invalid snapshot was emitted") },
			func(context.Context, string) {},
			func(context.Context, time.Duration) bool { waits.Add(1); return false },
		)
		app.poll(context.Background(), client)
		if waits.Load() != 0 {
			t.Fatalf("wait count = %d, want 0 before reconnect", waits.Load())
		}
	})

	t.Run("snapshot failure then connection close", func(t *testing.T) {
		client := newFakeControlClient()
		client.snapshotErr = errors.New("read failed")
		client.snapshotHook = func() { _ = client.Close() }
		var waits atomic.Int32
		app := newApp(
			func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
			func(context.Context, string, ...interface{}) { t.Fatal("invalid snapshot was emitted") },
			func(context.Context, string) {},
			func(context.Context, time.Duration) bool { waits.Add(1); return false },
		)
		app.poll(context.Background(), client)
		if waits.Load() != 0 {
			t.Fatalf("wait count = %d, want 0 after connection closed", waits.Load())
		}
	})
}

// TestAppReconnectsAfterSnapshotFailureAndPublishesNewBaseline proves one unusable authority read starts a fresh ordering epoch.
func TestAppReconnectsAfterSnapshotFailureAndPublishesNewBaseline(t *testing.T) {
	t.Parallel()

	first := newFakeControlClient()
	second := newFakeControlClient()
	second.snapshot.Sequence = 3
	first.snapshotHook = func() {
		first.mu.Lock()
		first.snapshotErr = errors.New("snapshot authority failed")
		first.snapshotHook = nil
		first.mu.Unlock()
	}

	var attempts atomic.Int32
	var pollWaits atomic.Int32
	snapshots := make(chan domain.Sequence, 2)
	connections := make(chan ConnectionState, 5)
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			if attempts.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			switch event {
			case snapshotEventName:
				snapshots <- values[0].(domain.Snapshot).Sequence
			case connectionEventName:
				connections <- values[0].(ConnectionEvent).State
			}
		},
		func(context.Context, string) {},
		func(ctx context.Context, duration time.Duration) bool {
			if duration == time.Millisecond {
				if pollWaits.Add(1) == 1 {
					return true
				}
				<-ctx.Done()
				return false
			}
			return true
		},
	)
	app.pollInterval = time.Millisecond
	app.reconnectDelay = 2 * time.Millisecond

	app.startup(context.Background())
	gotSnapshots := make([]domain.Sequence, 0, 2)
	for range 2 {
		select {
		case sequence := <-snapshots:
			gotSnapshots = append(gotSnapshots, sequence)
		case <-time.After(time.Second):
			app.shutdown(context.Background())
			t.Fatal("timed out waiting for snapshots across reconnect")
		}
	}
	app.shutdown(context.Background())

	if attempts.Load() != 2 {
		t.Fatalf("connection attempts = %d, want 2", attempts.Load())
	}
	if gotSnapshots[0] != 8 || gotSnapshots[1] != 3 {
		t.Fatalf("snapshot sequences = %v, want [8 3] across reconnect", gotSnapshots)
	}
	for index, want := range []ConnectionState{
		ConnectionConnecting,
		ConnectionConnected,
		ConnectionDisconnected,
		ConnectionConnecting,
		ConnectionConnected,
	} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("connection state %d = missing, want %q", index, want)
		}
	}
}

// TestAppExportedReadsRequireAndUseCurrentConnection verifies the Wails surface never dials ad hoc.
func TestAppExportedReadsRequireAndUseCurrentConnection(t *testing.T) {
	t.Parallel()

	app := testApp()
	if _, err := app.Status(); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("Status() error = %v, want disconnected", err)
	}
	if _, err := app.Snapshot(); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("Snapshot() error = %v, want disconnected", err)
	}

	client := newFakeControlClient()
	app.ctx = context.Background()
	app.client = client
	status, err := app.Status()
	if err != nil || status.Sequence != 8 {
		t.Fatalf("Status() = (%+v, %v), want sequence 8", status, err)
	}
	snapshot, err := app.Snapshot()
	if err != nil || snapshot.Sequence != 8 {
		t.Fatalf("Snapshot() = (sequence %d, %v), want sequence 8", snapshot.Sequence, err)
	}

	client.statusErr = errors.New("status failed")
	client.snapshotErr = errors.New("snapshot failed")
	if _, err := app.Status(); err == nil || !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("Status() error = %v, want wrapped failure", err)
	}
	if _, err := app.Snapshot(); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("Snapshot() error = %v, want wrapped failure", err)
	}
}

// TestSnapshotRejectsInvalidDaemonState keeps even injected control clients behind domain validation.
func TestSnapshotRejectsInvalidDaemonState(t *testing.T) {
	t.Parallel()

	app := testApp()
	client := newFakeControlClient()
	client.snapshot.Projects = nil
	app.ctx = context.Background()
	app.client = client

	if _, err := app.Snapshot(); err == nil || !strings.Contains(err.Error(), "validate Harbor snapshot") {
		t.Fatalf("Snapshot() error = %v, want validation failure", err)
	}
}

// TestSetupNetworkReplaysCompletedOperationWithoutConsent keeps an idempotent desktop retry entirely unprivileged.
func TestSetupNetworkReplaysCompletedOperationWithoutConsent(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
		t.Fatal("setup approval was constructed for a completed operation")
		return nil
	}
	app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner {
		t.Fatal("resolver approval was constructed for a completed operation")
		return nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || result.Revision != client.networkSetup.Revision {
		t.Fatalf("SetupNetwork() = %#v", result)
	}
	if client.networkSetupReq.IntentID != networkSetupIntentID {
		t.Fatalf("setup intent = %q, want %q", client.networkSetupReq.IntentID, networkSetupIntentID)
	}
	if client.resolverSetupReq.IntentID != networkResolverSetupIntentID {
		t.Fatalf("resolver setup intent = %q, want %q", client.resolverSetupReq.IntentID, networkResolverSetupIntentID)
	}
}

// TestSetupNetworkCompletesAddressThenResolverFirstRun keeps both consent phases exact while preserving the Wails return contract.
func TestSetupNetworkCompletesAddressThenResolverFirstRun(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	networkConfirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
	networkRunner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &networkConfirmation,
	}}
	client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
	resolverConfirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 13, 14)
	resolverRunner := &fakeNetworkResolverSetupApprovalRunner{outcome: networkresolverapproval.Outcome{
		State:        networkresolverapproval.Succeeded,
		Confirmation: &resolverConfirmation,
	}}
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return networkRunner }
	app.resolverApproval = func(got networkresolverapproval.Client) networkResolverSetupApprovalRunner {
		if got != client {
			t.Fatalf("resolver approval client = %T, want installed client", got)
		}
		if len(networkRunner.requests) != 1 {
			t.Fatalf("resolver approval started before address approval completed: %#v", networkRunner.requests)
		}
		return resolverRunner
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.ID != networkConfirmation.Operation.ID || result.Revision != networkConfirmation.Revision {
		t.Fatalf("SetupNetwork() = %#v, want original network confirmation %#v", result, networkConfirmation)
	}
	if len(resolverRunner.requests) != 1 ||
		resolverRunner.requests[0].OperationID != client.resolverSetup.Operation.ID ||
		resolverRunner.requests[0].ExpectedOperationRevision != client.resolverSetup.Revision {
		t.Fatalf("resolver approval requests = %#v", resolverRunner.requests)
	}
	if client.resolverSetupReq.IntentID != networkResolverSetupIntentID {
		t.Fatalf("resolver intent = %q, want %q", client.resolverSetupReq.IntentID, networkResolverSetupIntentID)
	}
}

// TestSetupNetworkRepairsResolverApprovalThroughTheSharedPrerequisiteBoundary bounds installation to one exact retry.
func TestSetupNetworkRepairsResolverApprovalThroughTheSharedPrerequisiteBoundary(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
	confirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 17, 18)
	runner := &fakeNetworkResolverSetupApprovalRunner{
		execute: func(_ context.Context, call int, _ networkresolverapproval.Request) (networkresolverapproval.Outcome, error) {
			if call == 1 {
				return networkresolverapproval.Outcome{}, rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)
			}
			return networkresolverapproval.Outcome{
				State:        networkresolverapproval.Succeeded,
				Confirmation: &confirmation,
			}, nil
		},
	}
	ensurer := &fakeNetworkPrerequisiteEnsurer{}
	app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner { return runner }
	app.setupPrerequisite = ensurer

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || ensurer.calls != 1 || len(runner.requests) != 2 {
		t.Fatalf("setup result/repair/approvals = %#v/%d/%d, want success/1/2", result, ensurer.calls, len(runner.requests))
	}
	if runner.requests[0] != runner.requests[1] {
		t.Fatalf("resolver approval retry crossed request boundary: %#v", runner.requests)
	}
}

// TestSetupNetworkReplacesAStaleResolverHelperBeforeRetrying proves a consumed legacy ticket is never reused.
func TestSetupNetworkReplacesAStaleResolverHelperBeforeRetrying(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
	confirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 13, 14)
	runner := &fakeNetworkResolverSetupApprovalRunner{
		execute: func(_ context.Context, call int, _ networkresolverapproval.Request) (networkresolverapproval.Outcome, error) {
			if call == 1 {
				return networkresolverapproval.Outcome{
					State: networkresolverapproval.HelperFailed,
					HelperFailure: &networkresolverapproval.HelperFailure{
						Code:    helper.ErrorCodeAuthenticationFailed,
						Message: "helper ticket redemption failed",
					},
				}, nil
			}
			return networkresolverapproval.Outcome{
				State:        networkresolverapproval.Succeeded,
				Confirmation: &confirmation,
			}, nil
		},
	}
	ensurer := &fakeNetworkPrerequisiteEnsurer{}
	app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner { return runner }
	app.setupPrerequisite = ensurer

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || ensurer.calls != 1 || len(runner.requests) != 2 {
		t.Fatalf("setup result/repair/approvals = %#v/%d/%d, want success/1/2", result, ensurer.calls, len(runner.requests))
	}
	if runner.requests[0] != runner.requests[1] {
		t.Fatalf("resolver approval retry crossed request boundary: %#v", runner.requests)
	}
}

// TestSetupNetworkBoundsResolverPrerequisiteRepairAndPreservesFailures prevents resolver installation loops and opaque native errors.
func TestSetupNetworkBoundsResolverPrerequisiteRepairAndPreservesFailures(t *testing.T) {
	t.Parallel()

	privilegedRequired := rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)
	privilegedUnsafe := rpc.NewWireError(rpc.ErrorCodePrivilegedHelperUnsafe)
	tests := []struct {
		name          string
		firstOutcome  networkresolverapproval.Outcome
		firstErr      error
		repairErr     error
		retryOutcome  networkresolverapproval.Outcome
		retryErr      error
		wantApprovals int
		want          string
	}{
		{
			name:          "repair declined",
			firstErr:      privilegedRequired,
			repairErr:     networkprerequisite.ErrDeclined,
			wantApprovals: 1,
			want:          "declined",
		},
		{
			name:          "repair failed",
			firstOutcome:  networkresolverapproval.Outcome{State: networkresolverapproval.Unavailable},
			repairErr:     fmt.Errorf("%w: native repair failed", networkprerequisite.ErrFailed),
			wantApprovals: 1,
			want:          "native repair failed",
		},
		{
			name: "stale helper authentication is repaired",
			firstOutcome: networkresolverapproval.Outcome{
				State: networkresolverapproval.HelperFailed,
				HelperFailure: &networkresolverapproval.HelperFailure{
					Code:    helper.ErrorCodeAuthenticationFailed,
					Message: "helper ticket redemption failed",
				},
			},
			retryOutcome: networkresolverapproval.Outcome{
				State: networkresolverapproval.HelperFailed,
				HelperFailure: &networkresolverapproval.HelperFailure{
					Code:    helper.ErrorCodeAuthenticationFailed,
					Message: "helper ticket redemption failed",
				},
			},
			wantApprovals: 2,
			want:          "could not authenticate a newly issued ticket",
		},
		{
			name:          "retry remains missing",
			firstErr:      privilegedRequired,
			retryErr:      privilegedRequired,
			wantApprovals: 2,
			want:          "still cannot find the ticket directory",
		},
		{
			name:          "retry finds unsafe installation",
			firstErr:      privilegedUnsafe,
			retryErr:      privilegedUnsafe,
			wantApprovals: 2,
			want:          "ownership, permissions, type, or ACLs",
		},
		{
			name:          "retry cannot launch helper",
			firstErr:      privilegedRequired,
			retryOutcome:  networkresolverapproval.Outcome{State: networkresolverapproval.Unavailable},
			wantApprovals: 2,
			want:          "native helper is unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
			runner := &fakeNetworkResolverSetupApprovalRunner{
				execute: func(_ context.Context, call int, _ networkresolverapproval.Request) (networkresolverapproval.Outcome, error) {
					if call == 1 {
						return test.firstOutcome, test.firstErr
					}
					return test.retryOutcome, test.retryErr
				},
			}
			ensurer := &fakeNetworkPrerequisiteEnsurer{err: test.repairErr}
			app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner { return runner }
			app.setupPrerequisite = ensurer

			if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
			if ensurer.calls != 1 || len(runner.requests) != test.wantApprovals {
				t.Fatalf("repair/approval calls = %d/%d, want 1/%d", ensurer.calls, len(runner.requests), test.wantApprovals)
			}
		})
	}
}

// TestNetworkResolverSetupPrerequisiteVerificationErrorRebuildsFixedWireText prevents forged peer detail from reaching Wails.
func TestNetworkResolverSetupPrerequisiteVerificationErrorRebuildsFixedWireText(t *testing.T) {
	t.Parallel()

	const secret = "APP_KEY=secret /Users/person/private"
	forged := rpc.WireError{Code: rpc.ErrorCodePrivilegedHelperUnsafe, Message: secret}
	err := networkResolverSetupPrerequisiteVerificationError(networkresolverapproval.Outcome{}, forged)
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "ownership, permissions, type, or ACLs") {
		t.Fatalf("verification error = %q, want fixed unsafe-installation guidance", err)
	}
	if err := networkResolverSetupPrerequisiteVerificationError(networkresolverapproval.Outcome{}, errors.New("other")); !strings.Contains(err.Error(), "result was inconsistent") {
		t.Fatalf("fallback verification error = %q", err)
	}
}

// TestNetworkResolverSetupApprovalErrorPreservesSafeOutcomes keeps resolver consent failures actionable and bounded.
func TestNetworkResolverSetupApprovalErrorPreservesSafeOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome networkresolverapproval.Outcome
		want    string
	}{
		{name: "declined", outcome: networkresolverapproval.Outcome{State: networkresolverapproval.Declined}, want: "safe to retry"},
		{name: "unavailable", outcome: networkresolverapproval.Outcome{State: networkresolverapproval.Unavailable}, want: "unavailable"},
		{
			name: "helper failed",
			outcome: networkresolverapproval.Outcome{
				State: networkresolverapproval.HelperFailed,
				HelperFailure: &networkresolverapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "resolver setup failed",
				},
			},
			want: "resolver setup failed",
		},
		{name: "helper failed without description", outcome: networkresolverapproval.Outcome{State: networkresolverapproval.HelperFailed}, want: "without a problem description"},
		{name: "indeterminate", outcome: networkresolverapproval.Outcome{State: networkresolverapproval.Indeterminate}, want: "refresh before retrying"},
		{name: "unsupported", outcome: networkresolverapproval.Outcome{}, want: "unsupported state"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := networkResolverSetupApprovalError(test.outcome); !strings.Contains(err.Error(), test.want) {
				t.Fatalf("networkResolverSetupApprovalError() = %q, want containing %q", err, test.want)
			}
		})
	}
}

// TestSetupNetworkRetriesRecoverableResolverTerminalOperations gives each safe resolver retry a fresh durable identity.
func TestSetupNetworkRetriesRecoverableResolverTerminalOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state domain.OperationState
	}{
		{name: "cancelled", state: domain.OperationCancelled},
		{name: "retryable failure", state: domain.OperationFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			const retryIntent domain.IntentID = "intent-network-resolver-setup-retry"
			app.resolverIntent = func() (domain.IntentID, error) { return retryIntent, nil }
			terminal := testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
			finishedAt := terminal.Operation.RequestedAt.Add(2 * time.Second)
			terminal.Operation.State = test.state
			terminal.Operation.Phase = string(test.state)
			terminal.Operation.FinishedAt = &finishedAt
			if test.state == domain.OperationFailed {
				terminal.Operation.Problem = &domain.Problem{
					Code:      "network.resolver.setup_failed",
					Message:   "Harbor could not establish local resolver policy.",
					Retryable: true,
				}
			}
			requests := make([]control.StartNetworkResolverSetupRequest, 0, 2)
			client.resolverSetupHook = func(request control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error) {
				requests = append(requests, request)
				if len(requests) == 1 {
					return terminal, nil
				}
				result := testNetworkResolverSetupOperation(domain.OperationSucceeded, 14)
				result.Operation.IntentID = request.IntentID
				return result, nil
			}

			if _, err := app.SetupNetwork(); err != nil {
				t.Fatalf("SetupNetwork() error = %v", err)
			}
			if len(requests) != 2 || requests[0].IntentID != networkResolverSetupIntentID || requests[1].IntentID != retryIntent {
				t.Fatalf("resolver retry requests = %#v", requests)
			}
		})
	}
}

// TestSetupNetworkSurfacesResolverRetryIdentityFailure avoids silently reusing a poisoned durable intent.
func TestSetupNetworkSurfacesResolverRetryIdentityFailure(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	cancelled := testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
	finishedAt := cancelled.Operation.RequestedAt.Add(2 * time.Second)
	cancelled.Operation.State = domain.OperationCancelled
	cancelled.Operation.Phase = string(domain.OperationCancelled)
	cancelled.Operation.FinishedAt = &finishedAt
	client.resolverSetup = cancelled
	app.resolverIntent = func() (domain.IntentID, error) { return "", errors.New("entropy unavailable") }

	if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), "create Harbor network resolver setup retry: entropy unavailable") {
		t.Fatalf("SetupNetwork() error = %v, want retry identity failure", err)
	}
}

// TestSetupNetworkRepairsLegacyResolverStageThroughPolicyMigration repairs the exact legacy resolver stage before retrying the flow.
func TestSetupNetworkRepairsLegacyResolverStageThroughPolicyMigration(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationSucceeded, 15)
	var startCount int
	client.resolverSetupHook = func(_ control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error) {
		startCount++
		if startCount == 1 {
			return control.NetworkResolverSetupOperation{}, errors.New(`network resolver setup requires identity stage, found "resolver"`)
		}
		result := testNetworkResolverSetupOperation(domain.OperationSucceeded, 13)
		return result, nil
	}

	if _, err := app.SetupNetwork(); err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if startCount != 2 {
		t.Fatalf("resolver setup starts = %d, want 2", startCount)
	}
	if len(client.migrationReqs) != 1 {
		t.Fatalf("legacy migration starts = %d, want 1", len(client.migrationReqs))
	}
}

// TestSetupNetworkReplaysResolverSuccessAfterLostConfirmationResponse makes an uncertain first response safe to refresh.
func TestSetupNetworkReplaysResolverSuccessAfterLostConfirmationResponse(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	awaitingApproval := testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
	completed := testNetworkResolverSetupOperation(domain.OperationSucceeded, 14)
	requests := make([]control.StartNetworkResolverSetupRequest, 0, 2)
	client.resolverSetupHook = func(request control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return awaitingApproval, nil
		}
		return completed, nil
	}
	runner := &fakeNetworkResolverSetupApprovalRunner{
		outcome: networkresolverapproval.Outcome{State: networkresolverapproval.Indeterminate},
		err:     errors.New("confirmation response was lost"),
	}
	app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner { return runner }

	if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), "response was lost") {
		t.Fatalf("first SetupNetwork() error = %v, want lost response", err)
	}
	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("replayed SetupNetwork() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || len(runner.requests) != 1 || len(requests) != 2 {
		t.Fatalf("replayed result/approval/start calls = %#v/%d/%d", result, len(runner.requests), len(requests))
	}
	if requests[0].IntentID != networkResolverSetupIntentID || requests[1].IntentID != networkResolverSetupIntentID {
		t.Fatalf("lost-response replay changed intent: %#v", requests)
	}
}

// TestSetupNetworkRejectsResolverSetupFailures prevents the address phase from masking an incomplete resolver foundation.
func TestSetupNetworkRejectsResolverSetupFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeControlClient, *fakeNetworkResolverSetupApprovalRunner)
		want   string
	}{
		{
			name: "start failure",
			mutate: func(client *fakeControlClient, _ *fakeNetworkResolverSetupApprovalRunner) {
				client.resolverSetupErr = errors.New("resolver start failed")
			},
			want: "resolver start failed",
		},
		{
			name: "invalid start",
			mutate: func(client *fakeControlClient, _ *fakeNetworkResolverSetupApprovalRunner) {
				client.resolverSetup = control.NetworkResolverSetupOperation{}
			},
			want: "validate Harbor network resolver setup",
		},
		{
			name: "wrong intent",
			mutate: func(client *fakeControlClient, _ *fakeNetworkResolverSetupApprovalRunner) {
				client.resolverSetup.Operation.IntentID = "intent-other"
			},
			want: "another intent",
		},
		{
			name: "unsupported operation state",
			mutate: func(client *fakeControlClient, _ *fakeNetworkResolverSetupApprovalRunner) {
				client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRunning, 11)
			},
			want: "is running",
		},
		{
			name: "approval failure",
			mutate: func(_ *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				runner.err = errors.New("resolver approval failed")
			},
			want: "resolver approval failed",
		},
		{
			name: "approval declined",
			mutate: func(_ *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				runner.outcome = networkresolverapproval.Outcome{State: networkresolverapproval.Declined}
			},
			want: "safe to retry",
		},
		{
			name: "missing confirmation",
			mutate: func(_ *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				runner.outcome = networkresolverapproval.Outcome{State: networkresolverapproval.Succeeded}
			},
			want: "inconsistent evidence",
		},
		{
			name: "unexpected helper failure",
			mutate: func(client *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				confirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 13, 14)
				runner.outcome = networkresolverapproval.Outcome{
					State:         networkresolverapproval.Succeeded,
					Confirmation:  &confirmation,
					HelperFailure: &networkresolverapproval.HelperFailure{},
				}
			},
			want: "inconsistent evidence",
		},
		{
			name: "invalid confirmation",
			mutate: func(_ *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				confirmation := control.NetworkResolverSetupApprovalConfirmation{}
				runner.outcome = networkresolverapproval.Outcome{
					State:        networkresolverapproval.Succeeded,
					Confirmation: &confirmation,
				}
			},
			want: "validate Harbor network resolver setup confirmation",
		},
		{
			name: "crossed historical revisions",
			mutate: func(client *fakeControlClient, runner *fakeNetworkResolverSetupApprovalRunner) {
				confirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 14, 16)
				runner.outcome = networkresolverapproval.Outcome{
					State:        networkresolverapproval.Succeeded,
					Confirmation: &confirmation,
				}
			},
			want: "immediately follow",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.resolverSetup = testNetworkResolverSetupOperation(domain.OperationRequiresApproval, 11)
			confirmation := testNetworkResolverSetupConfirmation(client.resolverSetup, 13, 14)
			runner := &fakeNetworkResolverSetupApprovalRunner{outcome: networkresolverapproval.Outcome{
				State:        networkresolverapproval.Succeeded,
				Confirmation: &confirmation,
			}}
			test.mutate(client, runner)
			app.resolverApproval = func(networkresolverapproval.Client) networkResolverSetupApprovalRunner { return runner }

			if _, err := app.SetupNetwork(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestSetupNetworkRetriesAnOpaqueFixedIntentFailure lets an older poisoned singleton record stop blocking the app forever.
func TestSetupNetworkRetriesAnOpaqueFixedIntentFailure(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	const retryIntent domain.IntentID = "intent-network-setup-retry"
	app.setupIntent = func() (domain.IntentID, error) { return retryIntent, nil }
	requests := make([]control.StartNetworkSetupRequest, 0, 2)
	client.networkSetupHook = func(request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return control.NetworkSetupOperation{}, rpc.NewWireError(rpc.ErrorCodeInternal)
		}
		result := testNetworkSetupOperation(domain.OperationSucceeded, 10)
		result.Operation.IntentID = request.IntentID
		return result, nil
	}
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
		t.Fatal("setup approval was constructed for a completed retry")
		return nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.IntentID != retryIntent || len(requests) != 2 ||
		requests[0].IntentID != networkSetupIntentID || requests[1].IntentID != retryIntent {
		t.Fatalf("setup retry result = %#v, requests = %#v", result, requests)
	}
}

// TestSetupNetworkRetriesARecoverableTerminalOperation gives each safe retry a new durable idempotency boundary.
func TestSetupNetworkRetriesARecoverableTerminalOperation(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	const retryIntent domain.IntentID = "intent-network-setup-retry"
	app.setupIntent = func() (domain.IntentID, error) { return retryIntent, nil }
	failed := testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	finishedAt := *failed.Operation.StartedAt
	failed.Operation.State = domain.OperationFailed
	failed.Operation.Phase = "setup preflight failed"
	failed.Operation.FinishedAt = &finishedAt
	failed.Operation.Problem = &domain.Problem{
		Code:      "network.setup.preflight_failed",
		Message:   "Harbor could not inspect local networking.",
		Retryable: true,
	}
	requests := make([]control.StartNetworkSetupRequest, 0, 2)
	client.networkSetupHook = func(request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return failed, nil
		}
		result := testNetworkSetupOperation(domain.OperationSucceeded, 10)
		result.Operation.IntentID = request.IntentID
		return result, nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.IntentID != retryIntent || len(requests) != 2 {
		t.Fatalf("setup retry result = %#v, requests = %#v", result, requests)
	}
}

// TestSetupNetworkApprovesOnlyTheSelectedRevision verifies the Wails action delegates the exact daemon operation boundary.
func TestSetupNetworkApprovesOnlyTheSelectedRevision(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
	runner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	app.setupApproval = func(got networksetupapproval.Client) networkSetupApprovalRunner {
		if got != client {
			t.Fatalf("approval client = %T, want installed client", got)
		}
		return runner
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if len(runner.requests) != 1 ||
		runner.requests[0].OperationID != client.networkSetup.Operation.ID ||
		runner.requests[0].ExpectedOperationRevision != client.networkSetup.Revision {
		t.Fatalf("approval requests = %#v", runner.requests)
	}
	if result.Operation.ID != confirmation.Operation.ID ||
		result.Operation.IntentID != confirmation.Operation.IntentID ||
		result.Operation.State != confirmation.Operation.State ||
		result.Revision != confirmation.Revision {
		t.Fatalf("SetupNetwork() = %#v, want confirmed operation %#v", result, confirmation)
	}
}

// TestSetupNetworkRetainsSelectedConnectionThroughConfirmation prevents polling retirement from closing a session during native consent.
func TestSetupNetworkRetainsSelectedConnectionThroughConfirmation(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	client.setupConfirmation = testNetworkSetupConfirmation(client.networkSetup, 9, 10)
	pollWaiting := make(chan struct{})
	pollAgain := make(chan struct{})
	approvalWaiting := make(chan struct{})
	confirmApproval := make(chan struct{})
	var connectionAttempts atomic.Int32
	var waits atomic.Int32
	app := newApp(
		func(ctx context.Context, _ control.ClientConfig) (controlClient, error) {
			if connectionAttempts.Add(1) == 1 {
				return client, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			if waits.Add(1) == 1 {
				close(pollWaiting)
				select {
				case <-pollAgain:
					return true
				case <-ctx.Done():
					return false
				}
			}
			<-ctx.Done()
			return false
		},
	)
	app.setupApproval = func(approvalClient networksetupapproval.Client) networkSetupApprovalRunner {
		return &fakeNetworkSetupApprovalRunner{
			execute: func(ctx context.Context, _ int, request networksetupapproval.Request) (networksetupapproval.Outcome, error) {
				close(approvalWaiting)
				select {
				case <-confirmApproval:
				case <-ctx.Done():
					return networksetupapproval.Outcome{}, ctx.Err()
				}
				confirmation, err := approvalClient.ConfirmNetworkSetupApproval(ctx, control.ConfirmNetworkSetupApprovalRequest{
					OperationID:               request.OperationID,
					ExpectedOperationRevision: request.ExpectedOperationRevision,
				})
				if err != nil {
					return networksetupapproval.Outcome{}, err
				}
				return networksetupapproval.Outcome{
					State:        networksetupapproval.Succeeded,
					Confirmation: &confirmation,
				}, nil
			},
		}
	}

	runContext, cancel := context.WithCancel(context.Background())
	app.ctx = runContext
	done := make(chan struct{})
	go app.run(runContext, done)
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-pollWaiting:
	case <-time.After(time.Second):
		t.Fatal("poll did not reach its first snapshot boundary")
	}

	result := make(chan control.NetworkSetupOperation, 1)
	setupErr := make(chan error, 1)
	go func() {
		operation, err := app.SetupNetwork()
		result <- operation
		setupErr <- err
	}()
	select {
	case <-approvalWaiting:
	case <-time.After(time.Second):
		t.Fatal("network setup did not reach native approval")
	}

	client.mu.Lock()
	client.snapshotErr = errors.New("snapshot authority failed")
	client.mu.Unlock()
	close(pollAgain)
	waitForClientRemoval(t, app)
	if got := client.closeCount.Load(); got != 0 {
		t.Fatalf("connection close count during approval = %d, want 0", got)
	}

	close(confirmApproval)
	select {
	case err := <-setupErr:
		if err != nil {
			t.Fatalf("SetupNetwork() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("network setup did not return after confirmation")
	}
	operation := <-result
	if operation.Operation.ID != client.setupConfirmation.Operation.ID || operation.Revision != client.setupConfirmation.Revision {
		t.Fatalf("SetupNetwork() = %#v, want exact confirmation %#v", operation, client.setupConfirmation)
	}
	if client.setupConfirmReq.OperationID != client.networkSetup.Operation.ID ||
		client.setupConfirmReq.ExpectedOperationRevision != client.networkSetup.Revision {
		t.Fatalf("confirmation request = %#v, want selected setup revision", client.setupConfirmReq)
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("retired connection did not close after approval released its lease")
	}
}

// TestSetupNetworkRepairsOnlyReviewedHelperPrerequisiteEvidence retries one exact approval after native source setup.
func TestSetupNetworkRepairsOnlyReviewedHelperPrerequisiteEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		firstOutcome networksetupapproval.Outcome
		firstErr     error
	}{
		{
			name:     "daemon prerequisite",
			firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired),
		},
		{
			name:     "daemon unsafe prerequisite",
			firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperUnsafe),
		},
		{
			name:         "launcher unavailable",
			firstOutcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			runner := &fakeNetworkSetupApprovalRunner{
				execute: func(_ context.Context, call int, _ networksetupapproval.Request) (networksetupapproval.Outcome, error) {
					if call == 1 {
						return test.firstOutcome, test.firstErr
					}
					return networksetupapproval.Outcome{
						State:        networksetupapproval.Succeeded,
						Confirmation: &confirmation,
					}, nil
				},
			}
			ensurer := &fakeNetworkPrerequisiteEnsurer{}
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			app.setupPrerequisite = ensurer

			result, err := app.SetupNetwork()
			if err != nil {
				t.Fatalf("SetupNetwork() error = %v", err)
			}
			if result.Operation.State != domain.OperationSucceeded || len(runner.requests) != 2 || ensurer.calls != 1 {
				t.Fatalf("setup result/calls = %#v/%d/%d, want success/2/1", result, len(runner.requests), ensurer.calls)
			}
			if runner.requests[0] != runner.requests[1] {
				t.Fatalf("approval retry crossed request boundary: %#v", runner.requests)
			}
		})
	}
}

// TestSetupNetworkBoundsPrerequisiteRepairAndPreservesNativeFailures prevents repair loops and production mutation.
func TestSetupNetworkBoundsPrerequisiteRepairAndPreservesNativeFailures(t *testing.T) {
	t.Parallel()

	privilegedRequired := rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)
	privilegedUnsafe := rpc.NewWireError(rpc.ErrorCodePrivilegedHelperUnsafe)
	tests := []struct {
		name          string
		repairErr     error
		retryOutcome  networksetupapproval.Outcome
		retryErr      error
		wantCalls     int
		wantApprovals int
		want          string
	}{
		{name: "repair failure", repairErr: networkprerequisite.ErrDeclined, wantCalls: 1, wantApprovals: 1, want: "declined"},
		{name: "repair diagnostics", repairErr: fmt.Errorf("%w: macOS authorization 1: unsafe existing directory", networkprerequisite.ErrFailed), wantCalls: 1, wantApprovals: 1, want: "macOS authorization 1: unsafe existing directory"},
		{name: "packaged build", repairErr: networkprerequisite.ErrUnavailable, wantCalls: 1, wantApprovals: 1, want: networkprerequisite.ErrUnavailable.Error()},
		{name: "retry remains missing", retryErr: privilegedRequired, wantCalls: 1, wantApprovals: 2, want: "harbord still cannot find the ticket directory"},
		{name: "retry finds unsafe installation", retryErr: privilegedUnsafe, wantCalls: 1, wantApprovals: 2, want: "harbord rejected the ticket directory's ownership, permissions, type, or ACLs"},
		{name: "retry cannot launch helper", retryOutcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable}, wantCalls: 1, wantApprovals: 2, want: "native helper is unavailable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			runner := &fakeNetworkSetupApprovalRunner{
				execute: func(_ context.Context, call int, _ networksetupapproval.Request) (networksetupapproval.Outcome, error) {
					if call == 1 {
						return networksetupapproval.Outcome{}, privilegedRequired
					}
					return test.retryOutcome, test.retryErr
				},
			}
			ensurer := &fakeNetworkPrerequisiteEnsurer{err: test.repairErr}
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			app.setupPrerequisite = ensurer

			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
			if ensurer.calls != test.wantCalls || len(runner.requests) != test.wantApprovals {
				t.Fatalf("repair/approval calls = %d/%d, want %d/%d", ensurer.calls, len(runner.requests), test.wantCalls, test.wantApprovals)
			}
		})
	}
}

// TestNetworkSetupPrerequisiteVerificationErrorRebuildsFixedWireText prevents a forged peer message from reaching Wails.
func TestNetworkSetupPrerequisiteVerificationErrorRebuildsFixedWireText(t *testing.T) {
	t.Parallel()

	const secret = "APP_KEY=secret /Users/person/private"
	forged := rpc.WireError{
		Code:    rpc.ErrorCodePrivilegedHelperUnsafe,
		Message: secret,
	}
	err := networkSetupPrerequisiteVerificationError(networksetupapproval.Outcome{}, forged)
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "harbord rejected the ticket directory's ownership, permissions, type, or ACLs") {
		t.Fatalf("verification error = %q, want fixed unsafe-installation guidance", err)
	}
}

// TestSetupNetworkDoesNotRepairUnreviewedApprovalFailures keeps arbitrary daemon and client errors away from native elevation.
func TestSetupNetworkDoesNotRepairUnreviewedApprovalFailures(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	runner := &fakeNetworkSetupApprovalRunner{err: rpc.NewWireError(rpc.ErrorCodeInternal)}
	ensurer := &fakeNetworkPrerequisiteEnsurer{}
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
	app.setupPrerequisite = ensurer

	_, err := app.SetupNetwork()
	if err == nil || !strings.Contains(err.Error(), rpc.NewWireError(rpc.ErrorCodeInternal).Message) {
		t.Fatalf("SetupNetwork() error = %v, want reviewed internal failure", err)
	}
	if ensurer.calls != 0 || len(runner.requests) != 1 {
		t.Fatalf("repair/approval calls = %d/%d, want 0/1", ensurer.calls, len(runner.requests))
	}
}

// TestSetupNetworkPreservesSafeApprovalOutcomes verifies native consent failures remain actionable without claiming completion.
func TestSetupNetworkPreservesSafeApprovalOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome networksetupapproval.Outcome
		want    string
	}{
		{name: "declined", outcome: networksetupapproval.Outcome{State: networksetupapproval.Declined}, want: "safe to retry"},
		{name: "unavailable", outcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable}, want: "unavailable"},
		{
			name: "helper failed",
			outcome: networksetupapproval.Outcome{
				State: networksetupapproval.HelperFailed,
				HelperFailure: &networksetupapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "loopback setup failed",
				},
			},
			want: "loopback setup failed",
		},
		{name: "helper failed without description", outcome: networksetupapproval.Outcome{State: networksetupapproval.HelperFailed}, want: "without a problem description"},
		{name: "indeterminate", outcome: networksetupapproval.Outcome{State: networksetupapproval.Indeterminate}, want: "refresh before retrying"},
		{name: "unsupported", outcome: networksetupapproval.Outcome{}, want: "unsupported state"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
				return &fakeNetworkSetupApprovalRunner{outcome: test.outcome}
			}
			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestSetupNetworkRejectsInconsistentDaemonAndApprovalResults covers every response boundary before the UI can claim readiness.
func TestSetupNetworkRejectsInconsistentDaemonAndApprovalResults(t *testing.T) {
	t.Parallel()

	approvalErr := errors.New("approval failed")
	tests := []struct {
		name   string
		mutate func(*fakeControlClient, *fakeNetworkSetupApprovalRunner)
		want   string
	}{
		{name: "start failure", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetupErr = errors.New("start failed")
		}, want: "start failed"},
		{name: "invalid start", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup = control.NetworkSetupOperation{}
		}, want: "validate Harbor network setup"},
		{name: "wrong intent", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup.Operation.IntentID = "intent-other"
		}, want: "another intent"},
		{name: "unsupported operation state", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup = testNetworkSetupOperation(domain.OperationRunning, 7)
		}, want: "is running"},
		{name: "approval failure", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			runner.err = approvalErr
		}, want: "approval failed"},
		{name: "missing confirmation", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded}
		}, want: "inconsistent evidence"},
		{name: "unexpected helper failure", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(testNetworkSetupOperation(domain.OperationRequiresApproval, 7), 9, 10)
			runner.outcome = networksetupapproval.Outcome{
				State:         networksetupapproval.Succeeded,
				Confirmation:  &confirmation,
				HelperFailure: &networksetupapproval.HelperFailure{},
			}
		}, want: "inconsistent evidence"},
		{name: "invalid confirmation", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := control.NetworkSetupApprovalConfirmation{}
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "validate Harbor network setup confirmation"},
		{name: "crossed operation", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			confirmation.Operation.ID = "operation-other"
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed intent", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			confirmation.Operation.IntentID = "intent-other"
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed network revision", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 8, 9)
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed operation revision", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 10, 11)
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			runner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{
				State:        networksetupapproval.Succeeded,
				Confirmation: &confirmation,
			}}
			test.mutate(client, runner)
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewNetworkSetupIntentUsesBoundedEntropy verifies retry IDs are canonical and entropy failures remain visible.
func TestNewNetworkSetupIntentUsesBoundedEntropy(t *testing.T) {
	t.Parallel()

	first, err := newNetworkSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkSetupIntent() error = %v", err)
	}
	second, err := newNetworkSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkSetupIntent() second error = %v", err)
	}
	if first == second || !strings.HasPrefix(string(first), "intent-network-setup-") {
		t.Fatalf("network setup intents = %q and %q", first, second)
	}
	if _, err := newNetworkSetupIntentFrom(strings.NewReader("short")); err == nil {
		t.Fatal("newNetworkSetupIntentFrom(short entropy) error = nil")
	}

	resolverFirst, err := newNetworkResolverSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkResolverSetupIntent() error = %v", err)
	}
	resolverSecond, err := newNetworkResolverSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkResolverSetupIntent() second error = %v", err)
	}
	if resolverFirst == resolverSecond || !strings.HasPrefix(string(resolverFirst), "intent-network-resolver-setup-") {
		t.Fatalf("network resolver setup intents = %q and %q", resolverFirst, resolverSecond)
	}
	if _, err := newNetworkResolverSetupIntentFrom(strings.NewReader("short")); err == nil {
		t.Fatal("newNetworkResolverSetupIntentFrom(short entropy) error = nil")
	}
}

// TestAddProjectPreservesPickerAndRegistrationOutcomes covers every user-visible boundary in the native selection flow.
func TestAddProjectPreservesPickerAndRegistrationOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("disconnected", func(t *testing.T) {
		app := testApp()
		chosen := false
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			chosen = true
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("AddProject() error = %v, want disconnected", err)
		}
		if chosen {
			t.Fatal("AddProject() opened a picker without a daemon connection")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		app, client := connectedTestApp()
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) { return "", nil }

		result, err := app.AddProject()
		if err != nil {
			t.Fatalf("AddProject() error = %v", err)
		}
		if !result.Canceled || result.Registration != nil {
			t.Fatalf("AddProject() = %+v, want canceled result", result)
		}
		if client.registerPath != "" {
			t.Fatalf("RegisterProject() path = %q after cancel", client.registerPath)
		}
	})

	t.Run("picker failure", func(t *testing.T) {
		app, _ := connectedTestApp()
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "", errors.New("dialog unavailable")
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "dialog unavailable") {
			t.Fatalf("AddProject() error = %v, want picker failure", err)
		}
	})

	t.Run("registration failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.registerErr = errors.New("not a GoForj project")
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "not a GoForj project") {
			t.Fatalf("AddProject() error = %v, want registration failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.registration = control.ProjectRegistration{}
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "validate project registration") {
			t.Fatalf("AddProject() error = %v, want validation failure", err)
		}
	})

	t.Run("registered", func(t *testing.T) {
		app, client := connectedTestApp()
		var options runtime.OpenDialogOptions
		app.choose = func(_ context.Context, selectedOptions runtime.OpenDialogOptions) (string, error) {
			options = selectedOptions
			return "/workspace/orders", nil
		}

		result, err := app.AddProject()
		if err != nil {
			t.Fatalf("AddProject() error = %v", err)
		}
		if result.Canceled || result.Registration == nil || result.Registration.Project.Name != "Orders" {
			t.Fatalf("AddProject() = %+v, want Orders registration", result)
		}
		if client.registerPath != "/workspace/orders" {
			t.Fatalf("RegisterProject() path = %q, want selected path", client.registerPath)
		}
		if options.Title != "Add a GoForj project" || !options.ResolvesAliases || options.CanCreateDirectories {
			t.Fatalf("picker options = %+v, want reviewed project-directory settings", options)
		}
	})
}

// TestRemoveProjectPreservesStableIdentityAndOperationState covers the complete native removal boundary.
func TestRemoveProjectPreservesStableIdentityAndOperationState(t *testing.T) {
	t.Parallel()

	t.Run("invalid request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.RemoveProject("", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("RemoveProject() error = %v, want invalid project", err)
		}
		if client.unregisterRequest != (control.UnregisterProjectRequest{}) {
			t.Fatalf("UnregisterProject() request = %+v after local validation", client.unregisterRequest)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		if _, err := testApp().RemoveProject("orders", "desktop-remove-orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("RemoveProject() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregisterErr = errors.New("project is busy")
		if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "project is busy") {
			t.Fatalf("RemoveProject() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = control.ProjectUnregistration{}
		if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "validate project removal") {
			t.Fatalf("RemoveProject() error = %v, want invalid operation", err)
		}
	})

	t.Run("mismatched daemon result", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*control.ProjectUnregistration)
		}{
			{name: "project", mutate: func(result *control.ProjectUnregistration) { result.Operation.ProjectID = "other" }},
			{name: "intent", mutate: func(result *control.ProjectUnregistration) { result.Operation.IntentID = "desktop-remove-other" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				app, client := connectedTestApp()
				test.mutate(&client.unregistration)
				if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("RemoveProject() error = %v, want correlation failure", err)
				}
			})
		}
	})

	t.Run("started", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.RemoveProject("orders", "desktop-remove-orders")
		if err != nil {
			t.Fatalf("RemoveProject() error = %v", err)
		}
		wantRequest := control.UnregisterProjectRequest{ProjectID: "orders", IntentID: "desktop-remove-orders"}
		if client.unregisterRequest != wantRequest {
			t.Fatalf("UnregisterProject() request = %+v, want %+v", client.unregisterRequest, wantRequest)
		}
		if result.Operation.State != domain.OperationQueued || result.Revision != 9 {
			t.Fatalf("RemoveProject() = %+v, want queued operation at revision 9", result)
		}
	})
}

// TestApproveProjectRemovalReplaysTheRetainedIntentBeforeNativeConsent covers validation, replay, and exact selection.
func TestApproveProjectRemovalReplaysTheRetainedIntentBeforeNativeConsent(t *testing.T) {
	t.Parallel()

	t.Run("invalid request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.ApproveProjectRemoval("", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("ApproveProjectRemoval() error = %v, want invalid project", err)
		}
		if client.unregisterRequest != (control.UnregisterProjectRequest{}) {
			t.Fatalf("UnregisterProject() request = %+v after local validation", client.unregisterRequest)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		if _, err := testApp().ApproveProjectRemoval("orders", "desktop-remove-orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("ApproveProjectRemoval() error = %v, want disconnected", err)
		}
	})

	t.Run("replay failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregisterErr = errors.New("replay unavailable")
		if _, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "replay unavailable") {
			t.Fatalf("ApproveProjectRemoval() error = %v, want replay failure", err)
		}
	})

	t.Run("invalid replay", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = control.ProjectUnregistration{}
		if _, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "validate project removal") {
			t.Fatalf("ApproveProjectRemoval() error = %v, want invalid replay", err)
		}
	})

	t.Run("crossed replay", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*control.ProjectUnregistration)
		}{
			{name: "project", mutate: func(result *control.ProjectUnregistration) { result.Operation.ProjectID = "billing" }},
			{name: "intent", mutate: func(result *control.ProjectUnregistration) { result.Operation.IntentID = "desktop-remove-billing" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				app, client := connectedTestApp()
				test.mutate(&client.unregistration)
				if _, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("ApproveProjectRemoval() error = %v, want crossed replay", err)
				}
			})
		}
	})

	t.Run("no longer awaiting approval", func(t *testing.T) {
		for _, state := range []domain.OperationState{domain.OperationQueued, domain.OperationSucceeded} {
			t.Run(string(state), func(t *testing.T) {
				app, client := connectedTestApp()
				client.unregistration = testProjectRemovalOperation(state, 10)
				approvalCalls := 0
				app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner {
					approvalCalls++
					return &fakeProjectRemovalApprovalRunner{}
				}
				result, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
				if err != nil {
					t.Fatalf("ApproveProjectRemoval() error = %v", err)
				}
				if result.Operation.State != state || approvalCalls != 0 {
					t.Fatalf("ApproveProjectRemoval() = %+v, approval calls = %d, want replay without consent", result, approvalCalls)
				}
			})
		}
	})

	t.Run("approved", func(t *testing.T) {
		app, client := connectedTestApp()
		replayed := testProjectRemovalOperation(domain.OperationRequiresApproval, 9)
		client.unregistration = replayed
		confirmation := testProjectRemovalApprovalConfirmation(replayed, 11)
		runner := &fakeProjectRemovalApprovalRunner{outcome: projectapproval.Outcome{
			State:        projectapproval.Succeeded,
			Confirmation: &confirmation,
		}}
		app.projectApproval = func(got projectapproval.Client) projectRemovalApprovalRunner {
			if got != client {
				t.Fatalf("project approval client = %T, want replayed client", got)
			}
			return runner
		}

		result, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
		if err != nil {
			t.Fatalf("ApproveProjectRemoval() error = %v", err)
		}
		wantReplay := control.UnregisterProjectRequest{ProjectID: "orders", IntentID: "desktop-remove-orders"}
		if client.unregisterRequest != wantReplay {
			t.Fatalf("UnregisterProject() request = %+v, want %+v", client.unregisterRequest, wantReplay)
		}
		wantSelection := projectapproval.Request{
			OperationID:               replayed.Operation.ID,
			ExpectedOperationRevision: replayed.Revision,
		}
		if len(runner.requests) != 1 || runner.requests[0] != wantSelection {
			t.Fatalf("project approval requests = %+v, want %+v", runner.requests, wantSelection)
		}
		if result.Operation.State != domain.OperationSucceeded || result.Revision != confirmation.Revision {
			t.Fatalf("ApproveProjectRemoval() = %+v, want succeeded revision %d", result, confirmation.Revision)
		}
	})
}

// TestApproveProjectRemovalRetainsSelectedConnectionThroughConfirmation prevents polling retirement from closing the replayed session during native consent.
func TestApproveProjectRemovalRetainsSelectedConnectionThroughConfirmation(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	replayed := testProjectRemovalOperation(domain.OperationRequiresApproval, 9)
	client.unregistration = replayed
	client.projectConfirmation = testProjectRemovalApprovalConfirmation(replayed, 11)
	approvalWaiting := make(chan struct{})
	confirmApproval := make(chan struct{})
	app.projectApproval = func(approvalClient projectapproval.Client) projectRemovalApprovalRunner {
		return &fakeProjectRemovalApprovalRunner{
			execute: func(ctx context.Context, _ int, request projectapproval.Request) (projectapproval.Outcome, error) {
				close(approvalWaiting)
				select {
				case <-confirmApproval:
				case <-ctx.Done():
					return projectapproval.Outcome{}, ctx.Err()
				}
				confirmation, err := approvalClient.ConfirmProjectUnregisterApproval(ctx, control.ConfirmProjectUnregisterApprovalRequest{
					OperationID:               request.OperationID,
					ExpectedOperationRevision: request.ExpectedOperationRevision,
				})
				if err != nil {
					return projectapproval.Outcome{}, err
				}
				return projectapproval.Outcome{
					State:        projectapproval.Succeeded,
					Confirmation: &confirmation,
				}, nil
			},
		}
	}

	result := make(chan control.ProjectUnregistration, 1)
	approvalErr := make(chan error, 1)
	go func() {
		operation, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
		result <- operation
		approvalErr <- err
	}()
	select {
	case <-approvalWaiting:
	case <-time.After(time.Second):
		t.Fatal("project removal did not reach native approval")
	}

	retired := make(chan struct{})
	go func() {
		app.retireClient(context.Background(), client)
		close(retired)
	}()
	waitForClientRemoval(t, app)
	if got := client.closeCount.Load(); got != 0 {
		t.Fatalf("connection close count during project removal approval = %d, want 0", got)
	}
	select {
	case <-retired:
		t.Fatal("connection retirement completed before project removal confirmation")
	default:
	}

	close(confirmApproval)
	select {
	case err := <-approvalErr:
		if err != nil {
			t.Fatalf("ApproveProjectRemoval() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("project removal approval did not return after confirmation")
	}
	operation := <-result
	if operation.Operation.ID != client.projectConfirmation.Operation.ID || operation.Revision != client.projectConfirmation.Revision {
		t.Fatalf("ApproveProjectRemoval() = %#v, want exact confirmation %#v", operation, client.projectConfirmation)
	}
	if client.projectConfirmReq.OperationID != replayed.Operation.ID ||
		client.projectConfirmReq.ExpectedOperationRevision != replayed.Revision {
		t.Fatalf("confirmation request = %#v, want replayed project removal revision", client.projectConfirmReq)
	}
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("retired connection did not close after project removal approval released its lease")
	}
	if got := client.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count after project removal approval = %d, want 1", got)
	}
}

// TestApproveProjectRemovalBoundsPrerequisiteRepair permits one retry only for reviewed helper evidence.
func TestApproveProjectRemovalBoundsPrerequisiteRepair(t *testing.T) {
	t.Parallel()

	replayed := testProjectRemovalOperation(domain.OperationRequiresApproval, 9)
	confirmation := testProjectRemovalApprovalConfirmation(replayed, 11)
	success := projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
	tests := []struct {
		name         string
		firstOutcome projectapproval.Outcome
		firstErr     error
	}{
		{name: "helper required", firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)},
		{name: "helper unsafe", firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperUnsafe)},
		{name: "native unavailable", firstOutcome: projectapproval.Outcome{State: projectapproval.Unavailable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			client.unregistration = replayed
			ensurer := &fakeNetworkPrerequisiteEnsurer{}
			app.setupPrerequisite = ensurer
			runner := &fakeProjectRemovalApprovalRunner{execute: func(_ context.Context, call int, _ projectapproval.Request) (projectapproval.Outcome, error) {
				if call == 1 {
					return test.firstOutcome, test.firstErr
				}
				return success, nil
			}}
			app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner { return runner }

			result, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
			if err != nil {
				t.Fatalf("ApproveProjectRemoval() error = %v", err)
			}
			if result.Operation.State != domain.OperationSucceeded || ensurer.calls != 1 || len(runner.requests) != 2 || runner.requests[0] != runner.requests[1] {
				t.Fatalf("result/repair/requests = %+v / %d / %+v, want one repair and one exact retry", result, ensurer.calls, runner.requests)
			}
		})
	}

	t.Run("repair fails", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = replayed
		ensurer := &fakeNetworkPrerequisiteEnsurer{err: networkprerequisite.ErrDeclined}
		app.setupPrerequisite = ensurer
		runner := &fakeProjectRemovalApprovalRunner{outcome: projectapproval.Outcome{State: projectapproval.Unavailable}}
		app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner { return runner }

		_, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
		if err == nil || !strings.Contains(err.Error(), networkprerequisite.ErrDeclined.Error()) || ensurer.calls != 1 || len(runner.requests) != 1 {
			t.Fatalf("ApproveProjectRemoval() = %v, repair/requests = %d/%d, want one failed repair", err, ensurer.calls, len(runner.requests))
		}
	})

	t.Run("retry remains missing", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = replayed
		ensurer := &fakeNetworkPrerequisiteEnsurer{}
		app.setupPrerequisite = ensurer
		runner := &fakeProjectRemovalApprovalRunner{err: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)}
		app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner { return runner }

		_, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
		if err == nil || !strings.Contains(err.Error(), "still cannot find the ticket directory") || ensurer.calls != 1 || len(runner.requests) != 2 {
			t.Fatalf("ApproveProjectRemoval() = %v, repair/requests = %d/%d, want bounded verification failure", err, ensurer.calls, len(runner.requests))
		}
	})

	t.Run("unreviewed failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = replayed
		ensurer := &fakeNetworkPrerequisiteEnsurer{}
		app.setupPrerequisite = ensurer
		runner := &fakeProjectRemovalApprovalRunner{err: rpc.NewWireError(rpc.ErrorCodeInternal)}
		app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner { return runner }

		_, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders")
		if err == nil || ensurer.calls != 0 || len(runner.requests) != 1 {
			t.Fatalf("ApproveProjectRemoval() = %v, repair/requests = %d/%d, want no native repair", err, ensurer.calls, len(runner.requests))
		}
	})
}

// TestProjectRemovalApprovalErrorPreservesSafeOutcomes pins every non-success result shown by Wails.
func TestProjectRemovalApprovalErrorPreservesSafeOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome projectapproval.Outcome
		want    string
	}{
		{name: "declined", outcome: projectapproval.Outcome{State: projectapproval.Declined}, want: "safe to retry"},
		{name: "unavailable", outcome: projectapproval.Outcome{State: projectapproval.Unavailable}, want: "unavailable"},
		{name: "helper failure", outcome: projectapproval.Outcome{
			State: projectapproval.HelperFailed,
			HelperFailure: &projectapproval.HelperFailure{
				Code:    helper.ErrorCodeMutationFailed,
				Message: "release was rejected",
			},
		}, want: "release was rejected"},
		{name: "helper failure without detail", outcome: projectapproval.Outcome{State: projectapproval.HelperFailed}, want: "without a problem description"},
		{name: "indeterminate", outcome: projectapproval.Outcome{State: projectapproval.Indeterminate}, want: "refresh before retrying"},
		{name: "unsupported", outcome: projectapproval.Outcome{}, want: "unsupported state"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := projectRemovalApprovalError(test.outcome); !strings.Contains(err.Error(), test.want) {
				t.Fatalf("projectRemovalApprovalError() = %q, want containing %q", err, test.want)
			}
		})
	}
}

// TestApproveProjectRemovalRejectsInconsistentSuccess prevents defective runners from crossing retained authority.
func TestApproveProjectRemovalRejectsInconsistentSuccess(t *testing.T) {
	t.Parallel()

	replayed := testProjectRemovalOperation(domain.OperationRequiresApproval, 9)
	valid := testProjectRemovalApprovalConfirmation(replayed, 11)
	tests := []struct {
		name    string
		outcome func() projectapproval.Outcome
		want    string
	}{
		{name: "missing confirmation", outcome: func() projectapproval.Outcome {
			return projectapproval.Outcome{State: projectapproval.Succeeded}
		}, want: "inconsistent evidence"},
		{name: "unexpected helper failure", outcome: func() projectapproval.Outcome {
			confirmation := valid
			return projectapproval.Outcome{
				State:         projectapproval.Succeeded,
				Confirmation:  &confirmation,
				HelperFailure: &projectapproval.HelperFailure{Code: helper.ErrorCodeMutationFailed, Message: "unexpected"},
			}
		}, want: "inconsistent evidence"},
		{name: "invalid confirmation", outcome: func() projectapproval.Outcome {
			confirmation := control.ProjectUnregisterApprovalConfirmation{}
			return projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
		}, want: "validate project removal approval confirmation"},
		{name: "operation", outcome: func() projectapproval.Outcome {
			confirmation := valid
			confirmation.Operation.ID = "operation-remove-other"
			return projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed"},
		{name: "project", outcome: func() projectapproval.Outcome {
			confirmation := valid
			confirmation.Operation.ProjectID = "billing"
			return projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed"},
		{name: "intent", outcome: func() projectapproval.Outcome {
			confirmation := valid
			confirmation.Operation.IntentID = "desktop-remove-other"
			return projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed"},
		{name: "revision", outcome: func() projectapproval.Outcome {
			confirmation := valid
			confirmation.Revision = replayed.Revision
			return projectapproval.Outcome{State: projectapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, client := connectedTestApp()
			client.unregistration = replayed
			runner := &fakeProjectRemovalApprovalRunner{outcome: test.outcome()}
			app.projectApproval = func(projectapproval.Client) projectRemovalApprovalRunner { return runner }

			if _, err := app.ApproveProjectRemoval("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ApproveProjectRemoval() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectActivityPreservesCurrentSessionCursor covers validation, correlation, and daemon delegation.
func TestProjectActivityPreservesCurrentSessionCursor(t *testing.T) {
	t.Parallel()

	t.Run("invalid request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.ProjectActivity("orders", "", 1); err == nil || !strings.Contains(err.Error(), "requires a session ID") {
			t.Fatalf("ProjectActivity() error = %v, want cursor validation", err)
		}
		if client.activityRequest != (control.ProjectActivityRequest{}) {
			t.Fatalf("ProjectActivity() request = %+v after local validation", client.activityRequest)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		if _, err := testApp().ProjectActivity("orders", "", 0); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("ProjectActivity() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.activityErr = errors.New("activity unavailable")
		if _, err := app.ProjectActivity("orders", "", 0); err == nil || !strings.Contains(err.Error(), "activity unavailable") {
			t.Fatalf("ProjectActivity() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.activity = control.ProjectActivity{}
		if _, err := app.ProjectActivity("orders", "", 0); err == nil || !strings.Contains(err.Error(), "validate project activity") {
			t.Fatalf("ProjectActivity() error = %v, want invalid activity", err)
		}
	})

	t.Run("mismatched project", func(t *testing.T) {
		app, client := connectedTestApp()
		client.activity.ProjectID = "billing"
		if _, err := app.ProjectActivity("orders", "", 0); err == nil || !strings.Contains(err.Error(), "another project") {
			t.Fatalf("ProjectActivity() error = %v, want project correlation failure", err)
		}
	})

	t.Run("mismatched session without reset", func(t *testing.T) {
		app, client := connectedTestApp()
		client.activity.Session.ID = "session-new"
		if _, err := app.ProjectActivity("orders", "session-orders", 4); err == nil || !strings.Contains(err.Error(), "without resetting") {
			t.Fatalf("ProjectActivity() error = %v, want session correlation failure", err)
		}
	})

	t.Run("current output", func(t *testing.T) {
		app, client := connectedTestApp()
		activity, err := app.ProjectActivity("orders", "session-orders", 4)
		if err != nil {
			t.Fatalf("ProjectActivity() error = %v", err)
		}
		wantRequest := control.ProjectActivityRequest{ProjectID: "orders", SessionID: "session-orders", Cursor: 4}
		if client.activityRequest != wantRequest {
			t.Fatalf("ProjectActivity() request = %+v, want %+v", client.activityRequest, wantRequest)
		}
		if activity.Session == nil || activity.Session.Output.Text != "ding app\n" {
			t.Fatalf("ProjectActivity() = %+v", activity)
		}
	})

	t.Run("held output", func(t *testing.T) {
		app, client := connectedTestApp()
		activity, err := app.WaitProjectActivity("orders", "session-orders", 4, 25_000)
		if err != nil {
			t.Fatalf("WaitProjectActivity() error = %v", err)
		}
		wantRequest := control.ProjectActivityRequest{
			ProjectID:        "orders",
			SessionID:        "session-orders",
			Cursor:           4,
			WaitMilliseconds: 25_000,
		}
		if client.activityRequest != wantRequest {
			t.Fatalf("ProjectActivity() request = %+v, want %+v", client.activityRequest, wantRequest)
		}
		if activity.Session == nil || activity.Session.Output.Text != "ding app\n" {
			t.Fatalf("WaitProjectActivity() = %+v", activity)
		}
	})

	t.Run("wait too long", func(t *testing.T) {
		app, client := connectedTestApp()
		_, err := app.WaitProjectActivity(
			"orders",
			"session-orders",
			4,
			uint64(control.MaximumProjectActivityWaitMilliseconds)+1,
		)
		if err == nil || !strings.Contains(err.Error(), "wait exceeds") {
			t.Fatalf("WaitProjectActivity() error = %v, want wait validation", err)
		}
		if client.activityRequest != (control.ProjectActivityRequest{}) {
			t.Fatalf("WaitProjectActivity() request = %+v after local validation", client.activityRequest)
		}
	})
}

// TestProjectActivityCancelsAStaleHeldRead keeps rapid project selection from exhausting daemon request slots.
func TestProjectActivityCancelsAStaleHeldRead(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	heldStarted := make(chan struct{})
	client.activityHook = func(ctx context.Context, request control.ProjectActivityRequest) (control.ProjectActivity, error) {
		if request.WaitMilliseconds == 0 {
			activity := testProjectActivity()
			activity.ProjectID = request.ProjectID
			activity.Session = nil
			return activity, nil
		}
		close(heldStarted)
		<-ctx.Done()
		return control.ProjectActivity{}, ctx.Err()
	}

	heldResult := make(chan error, 1)
	go func() {
		_, err := app.WaitProjectActivity("orders", "session-orders", 4, 25_000)
		heldResult <- err
	}()
	select {
	case <-heldStarted:
	case <-time.After(time.Second):
		t.Fatal("held project activity did not reach the control client")
	}

	if _, err := app.ProjectActivity("billing", "", 0); err != nil {
		t.Fatalf("replacement ProjectActivity() error = %v", err)
	}
	select {
	case err := <-heldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("held WaitProjectActivity() error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement project activity did not cancel the stale held read")
	}
}

// TestServiceLogsPreservesCurrentSessionCursor covers validation, correlation, and daemon delegation.
func TestServiceLogsPreservesCurrentSessionCursor(t *testing.T) {
	t.Parallel()

	t.Run("invalid request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.ServiceLogs("orders", "", "mysql", 1); err == nil || !strings.Contains(err.Error(), "requires a session ID") {
			t.Fatalf("ServiceLogs() error = %v, want cursor validation", err)
		}
		if client.serviceLogsRequest != (control.ServiceLogsRequest{}) {
			t.Fatalf("ServiceLogs() request = %+v after local validation", client.serviceLogsRequest)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		if _, err := testApp().ServiceLogs("orders", "", "mysql", 0); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("ServiceLogs() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.serviceLogsErr = errors.New("logs unavailable")
		if _, err := app.ServiceLogs("orders", "", "mysql", 0); err == nil || !strings.Contains(err.Error(), "logs unavailable") {
			t.Fatalf("ServiceLogs() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.serviceLogs = control.ServiceLogs{}
		if _, err := app.ServiceLogs("orders", "", "mysql", 0); err == nil || !strings.Contains(err.Error(), "validate service logs") {
			t.Fatalf("ServiceLogs() error = %v, want invalid logs", err)
		}
	})

	t.Run("mismatched project", func(t *testing.T) {
		app, client := connectedTestApp()
		client.serviceLogs.ProjectID = "billing"
		if _, err := app.ServiceLogs("orders", "", "mysql", 0); err == nil || !strings.Contains(err.Error(), "another project") {
			t.Fatalf("ServiceLogs() error = %v, want project correlation failure", err)
		}
	})

	t.Run("mismatched service", func(t *testing.T) {
		app, client := connectedTestApp()
		client.serviceLogs.ServiceID = "redis"
		if _, err := app.ServiceLogs("orders", "", "mysql", 0); err == nil || !strings.Contains(err.Error(), "another service") {
			t.Fatalf("ServiceLogs() error = %v, want service correlation failure", err)
		}
	})

	t.Run("mismatched session without reset", func(t *testing.T) {
		app, client := connectedTestApp()
		client.serviceLogs.SessionID = "session-new"
		if _, err := app.ServiceLogs("orders", "session-orders", "mysql", 4); err == nil || !strings.Contains(err.Error(), "without resetting") {
			t.Fatalf("ServiceLogs() error = %v, want session correlation failure", err)
		}
	})

	t.Run("current output", func(t *testing.T) {
		app, client := connectedTestApp()
		logs, err := app.ServiceLogs("orders", "session-orders", "mysql", 4)
		if err != nil {
			t.Fatalf("ServiceLogs() error = %v", err)
		}
		wantRequest := control.ServiceLogsRequest{
			ProjectID: "orders",
			SessionID: "session-orders",
			ServiceID: "mysql",
			Cursor:    4,
		}
		if client.serviceLogsRequest != wantRequest {
			t.Fatalf("ServiceLogs() request = %+v, want %+v", client.serviceLogsRequest, wantRequest)
		}
		if logs.Output.Text != "ready for connections\n" {
			t.Fatalf("ServiceLogs() = %+v", logs)
		}
	})

	t.Run("held output", func(t *testing.T) {
		app, client := connectedTestApp()
		logs, err := app.WaitServiceLogs("orders", "session-orders", "mysql", 4, 25_000)
		if err != nil {
			t.Fatalf("WaitServiceLogs() error = %v", err)
		}
		wantRequest := control.ServiceLogsRequest{
			ProjectID:        "orders",
			SessionID:        "session-orders",
			ServiceID:        "mysql",
			Cursor:           4,
			WaitMilliseconds: 25_000,
		}
		if client.serviceLogsRequest != wantRequest {
			t.Fatalf("ServiceLogs() request = %+v, want %+v", client.serviceLogsRequest, wantRequest)
		}
		if logs.Output.Text != "ready for connections\n" {
			t.Fatalf("WaitServiceLogs() = %+v", logs)
		}
	})

	t.Run("wait too long", func(t *testing.T) {
		app, client := connectedTestApp()
		_, err := app.WaitServiceLogs(
			"orders",
			"session-orders",
			"mysql",
			4,
			uint64(control.MaximumServiceLogsWaitMilliseconds)+1,
		)
		if err == nil || !strings.Contains(err.Error(), "wait exceeds") {
			t.Fatalf("WaitServiceLogs() error = %v, want wait validation", err)
		}
		if client.serviceLogsRequest != (control.ServiceLogsRequest{}) {
			t.Fatalf("WaitServiceLogs() request = %+v after local validation", client.serviceLogsRequest)
		}
	})
}

// TestServiceLogsCancelsAStaleHeldRead keeps rapid service selection from exhausting daemon request slots.
func TestServiceLogsCancelsAStaleHeldRead(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	heldStarted := make(chan struct{})
	client.serviceLogsHook = func(ctx context.Context, request control.ServiceLogsRequest) (control.ServiceLogs, error) {
		if request.WaitMilliseconds == 0 {
			logs := testServiceLogs()
			logs.ProjectID = request.ProjectID
			logs.ServiceID = request.ServiceID
			return logs, nil
		}
		close(heldStarted)
		<-ctx.Done()
		return control.ServiceLogs{}, ctx.Err()
	}

	heldResult := make(chan error, 1)
	go func() {
		_, err := app.WaitServiceLogs("orders", "session-orders", "mysql", 4, 25_000)
		heldResult <- err
	}()
	select {
	case <-heldStarted:
	case <-time.After(time.Second):
		t.Fatal("held service logs did not reach the control client")
	}

	if _, err := app.ServiceLogs("orders", "", "redis", 0); err != nil {
		t.Fatalf("replacement ServiceLogs() error = %v", err)
	}
	select {
	case err := <-heldResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("held WaitServiceLogs() error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement service logs did not cancel the stale held read")
	}
}

// TestServiceLogsWaitCancellationIsIndependentFromProjectActivity keeps simultaneous log panels from interrupting each other.
func TestServiceLogsWaitCancellationIsIndependentFromProjectActivity(t *testing.T) {
	t.Parallel()

	app := testApp()
	activityContext, releaseActivity := app.activityRequestContext(context.Background(), true)
	defer releaseActivity()
	serviceContext, releaseService := app.serviceLogsRequestContext(context.Background(), true)
	defer releaseService()

	replacementServiceContext, releaseReplacementService := app.serviceLogsRequestContext(context.Background(), false)
	defer releaseReplacementService()
	select {
	case <-serviceContext.Done():
	default:
		t.Fatal("replacement service read did not cancel the prior held service read")
	}
	select {
	case <-activityContext.Done():
		t.Fatal("replacement service read canceled held project activity")
	default:
	}
	select {
	case <-replacementServiceContext.Done():
		t.Fatal("replacement service request was canceled unexpectedly")
	default:
	}

	replacementActivityContext, releaseReplacementActivity := app.activityRequestContext(context.Background(), false)
	defer releaseReplacementActivity()
	select {
	case <-activityContext.Done():
	default:
		t.Fatal("replacement activity read did not cancel the prior held activity read")
	}
	select {
	case <-replacementServiceContext.Done():
		t.Fatal("replacement activity read canceled the service request")
	default:
	}
	select {
	case <-replacementActivityContext.Done():
		t.Fatal("replacement activity request was canceled unexpectedly")
	default:
	}
}

// TestProjectLifecyclePreservesActionIdentityAndOperationState covers every native lifecycle boundary.
func TestProjectLifecyclePreservesActionIdentityAndOperationState(t *testing.T) {
	t.Parallel()

	t.Run("invalid start request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.StartProject("", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("StartProject() error = %v, want invalid project", err)
		}
		if client.startRequest != (control.StartProjectRequest{}) {
			t.Fatalf("StartProject() request = %+v after local validation", client.startRequest)
		}
	})

	t.Run("disconnected stop", func(t *testing.T) {
		if _, err := testApp().StopProject("orders", "desktop-stop-orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("StopProject() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.startErr = errors.New("project is busy")
		if _, err := app.StartProject("orders", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "project is busy") {
			t.Fatalf("StartProject() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.stopLifecycle = control.ProjectLifecycleOperation{}
		if _, err := app.StopProject("orders", "desktop-stop-orders"); err == nil || !strings.Contains(err.Error(), "validate project stop") {
			t.Fatalf("StopProject() error = %v, want invalid operation", err)
		}
	})

	t.Run("mismatched daemon result", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*control.ProjectLifecycleOperation)
		}{
			{name: "kind", mutate: func(result *control.ProjectLifecycleOperation) {
				result.Operation.Kind = domain.OperationKindProjectStop
			}},
			{name: "project", mutate: func(result *control.ProjectLifecycleOperation) { result.Operation.ProjectID = "other" }},
			{name: "intent", mutate: func(result *control.ProjectLifecycleOperation) { result.Operation.IntentID = "desktop-start-other" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				app, client := connectedTestApp()
				test.mutate(&client.startLifecycle)
				if _, err := app.StartProject("orders", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("StartProject() error = %v, want correlation failure", err)
				}
			})
		}
	})

	t.Run("started", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.StartProject("orders", "desktop-start-orders")
		if err != nil {
			t.Fatalf("StartProject() error = %v", err)
		}
		wantRequest := control.StartProjectRequest{ProjectID: "orders", IntentID: "desktop-start-orders"}
		if client.startRequest != wantRequest {
			t.Fatalf("StartProject() request = %+v, want %+v", client.startRequest, wantRequest)
		}
		if result.Operation.Kind != domain.OperationKindProjectStart || result.Revision != 9 {
			t.Fatalf("StartProject() = %+v, want queued start at revision 9", result)
		}
	})

	t.Run("stopped", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.StopProject("orders", "desktop-stop-orders")
		if err != nil {
			t.Fatalf("StopProject() error = %v", err)
		}
		wantRequest := control.StopProjectRequest{ProjectID: "orders", IntentID: "desktop-stop-orders"}
		if client.stopRequest != wantRequest {
			t.Fatalf("StopProject() request = %+v, want %+v", client.stopRequest, wantRequest)
		}
		if result.Operation.Kind != domain.OperationKindProjectStop || result.Revision != 9 {
			t.Fatalf("StopProject() = %+v, want queued stop at revision 9", result)
		}
	})

	t.Run("restarted", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.RestartProject("orders", "desktop-restart-orders")
		if err != nil {
			t.Fatalf("RestartProject() error = %v", err)
		}
		wantRequest := control.RestartProjectRequest{ProjectID: "orders", IntentID: "desktop-restart-orders"}
		if client.restartRequest != wantRequest {
			t.Fatalf("RestartProject() request = %+v, want %+v", client.restartRequest, wantRequest)
		}
		if result.Operation.Kind != domain.OperationKindProjectRestart || result.Revision != 9 {
			t.Fatalf("RestartProject() = %+v, want queued restart at revision 9", result)
		}
	})
}

// TestProjectRuntimeRepairPreservesOpaqueInspectionAndConfirmation covers both native stale-runtime boundaries.
func TestProjectRuntimeRepairPreservesOpaqueInspectionAndConfirmation(t *testing.T) {
	t.Parallel()

	t.Run("invalid inspection request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.InspectProjectRuntimeRepair(""); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want invalid project", err)
		}
		if client.repairInspectRequest != (control.InspectProjectRuntimeRepairRequest{}) {
			t.Fatalf("InspectProjectRuntimeRepair() request = %+v after local validation", client.repairInspectRequest)
		}
	})

	t.Run("disconnected inspection", func(t *testing.T) {
		if _, err := testApp().InspectProjectRuntimeRepair("orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want disconnected", err)
		}
	})

	t.Run("inspection daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairInspectionErr = errors.New("inspection unavailable")
		if _, err := app.InspectProjectRuntimeRepair("orders"); err == nil || !strings.Contains(err.Error(), "inspection unavailable") {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid inspection result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairInspection = control.ProjectRuntimeRepairInspection{}
		if _, err := app.InspectProjectRuntimeRepair("orders"); err == nil || !strings.Contains(err.Error(), "validate project runtime repair inspection") {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want validation failure", err)
		}
	})

	t.Run("mismatched inspection result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairInspection.ProjectID = "billing"
		if _, err := app.InspectProjectRuntimeRepair("orders"); err == nil || !strings.Contains(err.Error(), "another project") {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want project correlation failure", err)
		}
	})

	t.Run("inspected", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.InspectProjectRuntimeRepair("orders")
		if err != nil {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v", err)
		}
		wantRequest := control.InspectProjectRuntimeRepairRequest{ProjectID: "orders"}
		if client.repairInspectRequest != wantRequest {
			t.Fatalf("InspectProjectRuntimeRepair() request = %+v, want %+v", client.repairInspectRequest, wantRequest)
		}
		if result.Disposition != control.ProjectRuntimeRepairInspectionConfirmable || result.Confirmable == nil {
			t.Fatalf("InspectProjectRuntimeRepair() = %+v, want confirmable inspection", result)
		}
	})

	t.Run("invalid confirmation request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.ConfirmProjectRuntimeRepair("orders", "bad", strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "inspection ID") {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want invalid inspection ID", err)
		}
		if client.repairConfirmRequest != (control.ConfirmProjectRuntimeRepairRequest{}) {
			t.Fatalf("ConfirmProjectRuntimeRepair() request = %+v after local validation", client.repairConfirmRequest)
		}
	})

	t.Run("disconnected confirmation", func(t *testing.T) {
		inspection := testProjectRuntimeRepairInspection().Confirmable
		if inspection == nil {
			t.Fatal("test inspection is not confirmable")
		}
		_, err := testApp().ConfirmProjectRuntimeRepair(
			"orders",
			string(inspection.InspectionID),
			string(inspection.CandidateFingerprint),
		)
		if !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want disconnected", err)
		}
	})

	t.Run("confirmation daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairConfirmErr = errors.New("candidate drifted")
		inspection := client.repairInspection.Confirmable
		if inspection == nil {
			t.Fatal("test inspection is not confirmable")
		}
		_, err := app.ConfirmProjectRuntimeRepair(
			"orders",
			string(inspection.InspectionID),
			string(inspection.CandidateFingerprint),
		)
		if err == nil || !strings.Contains(err.Error(), "candidate drifted") {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid confirmation result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairConfirmation = control.ProjectRuntimeRepairConfirmation{}
		inspection := client.repairInspection.Confirmable
		if inspection == nil {
			t.Fatal("test inspection is not confirmable")
		}
		_, err := app.ConfirmProjectRuntimeRepair(
			"orders",
			string(inspection.InspectionID),
			string(inspection.CandidateFingerprint),
		)
		if err == nil || !strings.Contains(err.Error(), "validate project runtime repair confirmation") {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want validation failure", err)
		}
	})

	t.Run("mismatched confirmation result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.repairConfirmation.Project.ID = "billing"
		client.repairConfirmation.Project.Path = "/workspace/billing"
		client.repairConfirmation.Project.Slug = "billing"
		inspection := client.repairInspection.Confirmable
		if inspection == nil {
			t.Fatal("test inspection is not confirmable")
		}
		_, err := app.ConfirmProjectRuntimeRepair(
			"orders",
			string(inspection.InspectionID),
			string(inspection.CandidateFingerprint),
		)
		if err == nil || !strings.Contains(err.Error(), "another project") {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want project correlation failure", err)
		}
	})

	t.Run("confirmed", func(t *testing.T) {
		app, client := connectedTestApp()
		inspection := client.repairInspection.Confirmable
		if inspection == nil {
			t.Fatal("test inspection is not confirmable")
		}
		result, err := app.ConfirmProjectRuntimeRepair(
			"orders",
			string(inspection.InspectionID),
			string(inspection.CandidateFingerprint),
		)
		if err != nil {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v", err)
		}
		wantRequest := control.ConfirmProjectRuntimeRepairRequest{
			ProjectID:    "orders",
			InspectionID: inspection.InspectionID,
			Fingerprint:  inspection.CandidateFingerprint,
		}
		if client.repairConfirmRequest != wantRequest {
			t.Fatalf("ConfirmProjectRuntimeRepair() request = %+v, want %+v", client.repairConfirmRequest, wantRequest)
		}
		if result.Project.State != domain.ProjectStopped || result.Revision != 9 {
			t.Fatalf("ConfirmProjectRuntimeRepair() = %+v, want stopped project at revision 9", result)
		}
	})
}

// TestOpenResourceUsesFreshProjectScopedState proves JavaScript cannot supply a URL or rely on a globally unique resource ID.
func TestOpenResourceUsesFreshProjectScopedState(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	secondProject := client.snapshot.Projects[0]
	secondProject.Apps = append([]domain.AppSnapshot(nil), secondProject.Apps...)
	secondProject.Services = append([]domain.ServiceSnapshot(nil), secondProject.Services...)
	secondProject.Resources = append([]domain.ResourceSnapshot(nil), secondProject.Resources...)
	secondProject.ID = "billing"
	secondProject.Name = "Billing"
	secondProject.Path = "/workspace/billing"
	secondProject.Slug = "billing"
	secondProject.Resources[0].URL = "https://billing.example.test"
	client.snapshot.Projects = append(client.snapshot.Projects, secondProject)
	var opened string
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
		func(context.Context, string, ...interface{}) {},
		func(_ context.Context, target string) { opened = target },
		func(context.Context, time.Duration) bool { return false },
	)
	app.ctx = context.Background()
	app.client = client

	if err := app.OpenResource("billing", "web"); err != nil {
		t.Fatalf("OpenResource() error = %v", err)
	}
	if opened != "https://billing.example.test" {
		t.Fatalf("opened URL = %q, want billing resource", opened)
	}
}

// TestOpenResourceRejectsInvalidAndMissingIdentities covers every client-controlled lookup boundary.
func TestOpenResourceRejectsInvalidAndMissingIdentities(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	app := testApp()
	if err := app.OpenResource("orders", "web"); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("OpenResource() disconnected error = %v, want disconnected", err)
	}
	app.ctx = context.Background()
	app.client = client

	tests := []struct {
		name       string
		projectID  string
		resourceID string
		want       string
	}{
		{name: "invalid project", resourceID: "web", want: "project ID"},
		{name: "invalid resource", projectID: "orders", want: "resource ID"},
		{name: "missing project", projectID: "billing", resourceID: "web", want: "project \"billing\" was not found"},
		{name: "missing resource", projectID: "orders", resourceID: "mail", want: "resource \"mail\" was not found"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := app.OpenResource(test.projectID, test.resourceID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenResource() error = %v, want containing %q", err, test.want)
			}
		})
	}

	client.snapshotErr = errors.New("read failed")
	if err := app.OpenResource("orders", "web"); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("OpenResource() read error = %v, want wrapped failure", err)
	}
	client.snapshotErr = nil
	client.snapshot.Projects = nil
	if err := app.OpenResource("orders", "web"); err == nil || !strings.Contains(err.Error(), "validate Harbor snapshot") {
		t.Fatalf("OpenResource() validation error = %v, want invalid snapshot", err)
	}
}

// TestOpenTerminalURLRestrictsLogLinksToSafeWebTargets prevents terminal output from selecting arbitrary native actions.
func TestOpenTerminalURLRestrictsLogLinksToSafeWebTargets(t *testing.T) {
	t.Parallel()

	var opened string
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(context.Context, string, ...interface{}) {},
		func(_ context.Context, target string) { opened = target },
		func(context.Context, time.Duration) bool { return false },
	)
	app.ctx = context.Background()
	if err := app.OpenTerminalURL("https://orders.example.test/docs"); err != nil {
		t.Fatalf("OpenTerminalURL() error = %v", err)
	}
	if opened != "https://orders.example.test/docs" {
		t.Fatalf("opened URL = %q", opened)
	}
	for _, target := range []string{"", "ftp://orders.example.test", "https://user:password@orders.example.test"} {
		if err := app.OpenTerminalURL(target); err == nil {
			t.Fatalf("OpenTerminalURL(%q) error = nil", target)
		}
	}
}

// TestEmitSnapshotRejectsInvalidUnchangedAndStaleState prevents polling from repeatedly publishing one durable revision.
func TestEmitSnapshotRejectsInvalidUnchangedAndStaleState(t *testing.T) {
	t.Parallel()

	var sequences []domain.Sequence
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(_ context.Context, _ string, values ...interface{}) {
			sequences = append(sequences, values[0].(domain.Snapshot).Sequence)
		},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)

	snapshot := testSnapshot()
	cursor := snapshotCursor{}
	app.emitSnapshot(context.Background(), snapshot, &cursor)
	app.emitSnapshot(context.Background(), snapshot, &cursor)
	stale := snapshot
	stale.Sequence--
	app.emitSnapshot(context.Background(), stale, &cursor)
	invalid := snapshot
	invalid.SchemaVersion = 0
	app.emitSnapshot(context.Background(), invalid, &cursor)

	newer := snapshot
	newer.Sequence++
	app.emitSnapshot(context.Background(), newer, &cursor)

	if len(sequences) != 2 || sequences[0] != 8 || sequences[1] != 9 {
		t.Fatalf("emitted sequences = %v, want [8 9]", sequences)
	}
}

// TestEmitSnapshotResetsOrderingForEachNegotiatedConnection prevents a restarted daemon from being hidden by an old cursor.
func TestEmitSnapshotResetsOrderingForEachNegotiatedConnection(t *testing.T) {
	t.Parallel()

	var sequences []domain.Sequence
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(_ context.Context, event string, values ...interface{}) {
			if event == snapshotEventName {
				sequences = append(sequences, values[0].(domain.Snapshot).Sequence)
			}
		},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)

	snapshot := testSnapshot()
	firstConnection := snapshotCursor{}
	secondConnection := snapshotCursor{}
	app.emitSnapshot(context.Background(), snapshot, &firstConnection)
	app.emitSnapshot(context.Background(), snapshot, &secondConnection)

	if len(sequences) != 2 || sequences[0] != snapshot.Sequence || sequences[1] != snapshot.Sequence {
		t.Fatalf("emitted sequences = %v, want the first revision from both connections", sequences)
	}
}

// TestRunRejectsNilFactoryResult keeps a broken composition from becoming a silent reconnect loop.
func TestRunRejectsNilFactoryResult(t *testing.T) {
	t.Parallel()

	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return nil, nil },
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)
	app.ctx = context.Background()
	done := make(chan struct{})
	defer func() {
		if recover() == nil {
			t.Fatal("run() panic = nil, want nil client failure")
		}
		select {
		case <-done:
		default:
			t.Fatal("run() did not close its completion channel")
		}
	}()
	app.run(context.Background(), done)
}

// TestInstallAndRemoveClientHonorLifecycleOwnership covers cancellation and replacement-safe cleanup.
func TestInstallAndRemoveClientHonorLifecycleOwnership(t *testing.T) {
	t.Parallel()

	app := testApp()
	client := newFakeControlClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.ctx = context.Background()
	if app.installClient(ctx, client) {
		t.Fatal("installClient() = true for cancelled lifecycle")
	}
	if !app.installClient(context.Background(), client) {
		t.Fatal("installClient() = false for active lifecycle")
	}
	other := newFakeControlClient()
	app.removeClient(other)
	if app.client != client {
		t.Fatal("removeClient() cleared a different connection")
	}
	app.removeClient(client)
	if app.client != nil {
		t.Fatal("removeClient() retained the owned connection")
	}
}

// TestConnectionLeasesDrainAfterEveryApproval verifies one retired session remains usable until its last owner releases it.
func TestConnectionLeasesDrainAfterEveryApproval(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	_, firstClient, releaseFirst, err := app.leaseCurrentConnection()
	if err != nil || firstClient != client {
		t.Fatalf("leaseCurrentConnection() first = (%T, %v), want installed client", firstClient, err)
	}
	_, secondClient, releaseSecond, err := app.leaseCurrentConnection()
	if err != nil || secondClient != client {
		t.Fatalf("leaseCurrentConnection() second = (%T, %v), want installed client", secondClient, err)
	}

	drained := app.removeClient(client)
	if drained == nil {
		t.Fatal("removeClient() drain = nil with active leases")
	}
	releaseFirst()
	select {
	case <-drained:
		t.Fatal("connection drained before its final lease was released")
	default:
	}
	releaseSecond()
	releaseSecond()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("connection did not drain after its final lease was released")
	}
}

// TestConnectionLeaseRejectsDisconnectedAndImpossibleOwnership covers fail-fast lifecycle invariants around session retirement.
func TestConnectionLeaseRejectsDisconnectedAndImpossibleOwnership(t *testing.T) {
	t.Parallel()

	if _, err := testApp().SetupNetwork(); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("SetupNetwork() error = %v, want disconnected", err)
	}

	tests := []struct {
		name string
		run  func()
		want string
	}{
		{
			name: "published while draining",
			run: func() {
				app, _ := connectedTestApp()
				app.clientDrain = make(chan struct{})
				_, _, _, _ = app.leaseCurrentConnection()
			},
			want: "published while retirement is pending",
		},
		{
			name: "release without lease",
			run: func() {
				app, client := connectedTestApp()
				app.releaseClientLease(client)
			},
			want: "released without ownership",
		},
		{
			name: "release across replacement",
			run: func() {
				app, client := connectedTestApp()
				app.clientLeases = 1
				app.client = newFakeControlClient()
				app.releaseClientLease(client)
			},
			want: "crossed a replacement session",
		},
		{
			name: "install before drain",
			run: func() {
				app := testApp()
				app.ctx = context.Background()
				app.clientLeases = 1
				app.installClient(context.Background(), newFakeControlClient())
			},
			want: "installed before the retired session drained",
		},
		{
			name: "retire twice",
			run: func() {
				app, client := connectedTestApp()
				app.clientLeases = 1
				app.clientDrain = make(chan struct{})
				app.removeClient(client)
			},
			want: "retirement started more than once",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertPanicContains(t, test.want, test.run)
		})
	}
}

// TestWaitForContextCoversTimerAndCancellation proves production waits cannot delay shutdown.
func TestWaitForContextCoversTimerAndCancellation(t *testing.T) {
	t.Parallel()

	if !waitForContext(context.Background(), time.Millisecond) {
		t.Fatal("waitForContext() = false after timer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForContext(ctx, time.Hour) {
		t.Fatal("waitForContext() = true after cancellation")
	}
}

// testApp creates an adapter whose side effects are inert until a test installs a fake connection.
func testApp() *App {
	return newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)
}

// connectedTestApp creates a desktop adapter with one installed fake daemon connection.
func connectedTestApp() (*App, *fakeControlClient) {
	app := testApp()
	client := newFakeControlClient()
	app.ctx = context.Background()
	app.client = client
	return app, client
}

// waitForClientRemoval waits until the poll owner has unpublished its failing session without relying on connection close as the signal.
func waitForClientRemoval(t *testing.T, app *App) {
	t.Helper()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		app.mu.RLock()
		client := app.client
		app.mu.RUnlock()
		if client == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("poll did not retire its failed connection")
		case <-tick.C:
		}
	}
}

// assertPanicContains requires a fail-fast invariant to retain its diagnostic boundary.
func assertPanicContains(t *testing.T, want string, run func()) {
	t.Helper()
	defer func() {
		value := recover()
		if value == nil || !strings.Contains(fmt.Sprint(value), want) {
			t.Fatalf("panic = %v, want containing %q", value, want)
		}
	}()
	run()
}

// testSnapshot returns the smallest valid snapshot with one project-scoped HTTP resource.
func testSnapshot() domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:      8,
		CapturedAt:    time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		Projects: []domain.ProjectSnapshot{
			{
				ID:        "orders",
				Name:      "Orders",
				Path:      "/workspace/orders",
				Slug:      "orders",
				State:     domain.ProjectReady,
				UpdatedAt: time.Date(2026, time.July, 18, 11, 0, 0, 0, time.UTC),
				Apps: []domain.AppSnapshot{
					{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true},
				},
				Services: []domain.ServiceSnapshot{},
				Resources: []domain.ResourceSnapshot{
					{
						ID:    "web",
						Name:  "Web",
						Kind:  "application",
						Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
						URL:   "https://orders.example.test",
					},
				},
			},
		},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{{ProjectID: "orders", ResourceID: "web"}},
	}
}

// testNetworkSetupOperation returns one valid singleton setup operation at the requested lifecycle state and revision.
func testNetworkSetupOperation(state domain.OperationState, revision domain.Sequence) control.NetworkSetupOperation {
	requestedAt := time.Date(2026, time.July, 18, 12, 10, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := requestedAt.Add(2 * time.Second)
	operation := domain.Operation{
		ID:          "operation-network-setup",
		IntentID:    networkSetupIntentID,
		Kind:        domain.OperationKindNetworkSetup,
		State:       state,
		Phase:       string(state),
		RequestedAt: requestedAt,
	}
	switch state {
	case domain.OperationRunning, domain.OperationRequiresApproval:
		operation.StartedAt = &startedAt
	case domain.OperationSucceeded:
		operation.StartedAt = &startedAt
		operation.FinishedAt = &finishedAt
	}
	return control.NetworkSetupOperation{Operation: operation, Revision: revision}
}

// testNetworkSetupConfirmation advances one approval operation to a valid completed confirmation.
func testNetworkSetupConfirmation(
	setup control.NetworkSetupOperation,
	networkRevision domain.Sequence,
	revision domain.Sequence,
) control.NetworkSetupApprovalConfirmation {
	operation := setup.Operation
	finishedAt := operation.RequestedAt.Add(2 * time.Second)
	operation.State = domain.OperationSucceeded
	operation.Phase = string(domain.OperationSucceeded)
	operation.FinishedAt = &finishedAt
	return control.NetworkSetupApprovalConfirmation{
		Operation:       operation,
		Revision:        revision,
		NetworkRevision: networkRevision,
		Pool:            "127.42.0.0/29",
	}
}

// testNetworkResolverSetupOperation returns one valid resolver setup operation at the requested lifecycle state and revision.
func testNetworkResolverSetupOperation(state domain.OperationState, revision domain.Sequence) control.NetworkResolverSetupOperation {
	requestedAt := time.Date(2026, time.July, 18, 12, 11, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := requestedAt.Add(2 * time.Second)
	operation := domain.Operation{
		ID:          "operation-network-resolver-setup",
		IntentID:    networkResolverSetupIntentID,
		Kind:        domain.OperationKindNetworkResolverSetup,
		State:       state,
		Phase:       string(state),
		RequestedAt: requestedAt,
	}
	switch state {
	case domain.OperationRunning, domain.OperationRequiresApproval:
		operation.StartedAt = &startedAt
	case domain.OperationSucceeded:
		operation.StartedAt = &startedAt
		operation.FinishedAt = &finishedAt
	}
	return control.NetworkResolverSetupOperation{Operation: operation, Revision: revision}
}

// testNetworkResolverSetupConfirmation advances one resolver approval operation to a valid historical confirmation.
func testNetworkResolverSetupConfirmation(
	setup control.NetworkResolverSetupOperation,
	networkRevision domain.Sequence,
	revision domain.Sequence,
) control.NetworkResolverSetupApprovalConfirmation {
	operation := setup.Operation
	finishedAt := operation.RequestedAt.Add(2 * time.Second)
	operation.State = domain.OperationSucceeded
	operation.Phase = string(domain.OperationSucceeded)
	operation.FinishedAt = &finishedAt
	return control.NetworkResolverSetupApprovalConfirmation{
		Operation:       operation,
		Revision:        revision,
		NetworkRevision: networkRevision,
	}
}

// TestRemoveOldNetworkingCompletesApproval binds native consent to the exact migration operation revision.
func TestRemoveOldNetworkingCompletesApproval(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
	confirmation := testNetworkResolverPolicyMigrationConfirmation(client.migration, 13, 14)
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{
		State:        networkresolverpolicymigrationapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	app.migrationApproval = func(got networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		if got != client {
			t.Fatalf("migration approval client = %T, want installed client", got)
		}
		return runner
	}

	result, err := app.RemoveOldNetworking()
	if err != nil {
		t.Fatalf("RemoveOldNetworking() error = %v", err)
	}
	expected := control.NetworkResolverPolicyMigrationOperation{
		Operation: confirmation.Operation,
		Revision:  confirmation.Revision,
	}
	if result != expected {
		t.Fatalf("RemoveOldNetworking() = %#v", result)
	}
	if len(runner.requests) != 1 || runner.requests[0].OperationID != client.migration.Operation.ID || runner.requests[0].ExpectedOperationRevision != client.migration.Revision {
		t.Fatalf("migration approval requests = %#v", runner.requests)
	}
	if len(client.migrationReqs) != 1 || client.migrationReqs[0].IntentID != networkResolverPolicyMigrationIntentID {
		t.Fatalf("migration start requests = %#v", client.migrationReqs)
	}
}

// TestRemoveOldNetworkingRejectsAnUnsupportedSessionWithoutMutation preserves the live session for the daemon's normal restart and reconnect lifecycle.
func TestRemoveOldNetworkingRejectsAnUnsupportedSessionWithoutMutation(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.status.Capabilities = []rpc.Capability{control.CapabilityV1}

	if _, err := app.RemoveOldNetworking(); !errors.Is(err, control.ErrNetworkResolverPolicyMigrationUnsupported) {
		t.Fatalf("RemoveOldNetworking() error = %v, want unsupported migration capability", err)
	}
	if len(client.migrationReqs) != 0 {
		t.Fatalf("migration requests = %#v, want no daemon mutation", client.migrationReqs)
	}
	if client.closeCount.Load() != 0 {
		t.Fatalf("desktop client closes = %d, want 0", client.closeCount.Load())
	}
	app.mu.RLock()
	installed := app.client
	app.mu.RUnlock()
	if installed != client {
		t.Fatal("unsupported session was replaced")
	}
}

// TestRemoveOldNetworkingRejectsConfirmationOutsideSelectedRevision verifies desktop does not publish a migration completion from another transition.
func TestRemoveOldNetworkingRejectsConfirmationOutsideSelectedRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*control.NetworkResolverPolicyMigrationApprovalConfirmation)
	}{
		{
			name: "network revision skips one transition",
			mutate: func(confirmation *control.NetworkResolverPolicyMigrationApprovalConfirmation) {
				confirmation.NetworkRevision++
				confirmation.Revision++
			},
		},
		{
			name: "terminal operation revision skips network revision",
			mutate: func(confirmation *control.NetworkResolverPolicyMigrationApprovalConfirmation) {
				confirmation.Revision++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			app, client := connectedTestApp()
			client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
			confirmation := testNetworkResolverPolicyMigrationConfirmation(client.migration, 13, 14)
			test.mutate(&confirmation)
			app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
				return &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{
					State:        networkresolverpolicymigrationapproval.Succeeded,
					Confirmation: &confirmation,
				}}
			}

			if _, err := app.RemoveOldNetworking(); err == nil || !strings.Contains(err.Error(), "confirmation") {
				t.Fatalf("RemoveOldNetworking() error = %v, want confirmation validation failure", err)
			}
		})
	}
}

// TestRemoveOldNetworkingReturnsSucceededReplay avoids reopening native consent for an already-completed durable operation.
func TestRemoveOldNetworkingReturnsSucceededReplay(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationSucceeded, 14)
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}

	result, err := app.RemoveOldNetworking()
	if err != nil {
		t.Fatalf("RemoveOldNetworking() error = %v", err)
	}
	if result != client.migration || len(runner.requests) != 0 {
		t.Fatalf("result/approval requests = %#v/%#v", result, runner.requests)
	}
}

// TestRemoveOldNetworkingReturnsDeclineWithoutReplay preserves the user's explicit native-consent decision.
func TestRemoveOldNetworkingReturnsDeclineWithoutReplay(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{State: networkresolverpolicymigrationapproval.Declined}}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}

	if _, err := app.RemoveOldNetworking(); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("RemoveOldNetworking() error = %v, want declined result", err)
	}
	if len(client.migrationReqs) != 1 {
		t.Fatalf("migration starts = %d, want 1", len(client.migrationReqs))
	}
}

// TestRemoveOldNetworkingRepairsStaleHelperOnce refreshes only the reviewed fixed helper boundary.
func TestRemoveOldNetworkingRepairsStaleHelperOnce(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
	confirmation := testNetworkResolverPolicyMigrationConfirmation(client.migration, 13, 14)
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{execute: func(_ context.Context, call int, _ networkresolverpolicymigrationapproval.Request) (networkresolverpolicymigrationapproval.Outcome, error) {
		if call == 1 {
			return networkresolverpolicymigrationapproval.Outcome{
				State: networkresolverpolicymigrationapproval.HelperFailed,
				HelperFailure: &networkresolverpolicymigrationapproval.HelperFailure{
					Code:    helper.ErrorCodeAuthenticationFailed,
					Message: "stale ticket",
				},
			}, nil
		}
		return networkresolverpolicymigrationapproval.Outcome{
			State:        networkresolverpolicymigrationapproval.Succeeded,
			Confirmation: &confirmation,
		}, nil
	}}
	ensurer := &fakeNetworkPrerequisiteEnsurer{}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}
	app.setupPrerequisite = ensurer

	if _, err := app.RemoveOldNetworking(); err != nil {
		t.Fatalf("RemoveOldNetworking() error = %v", err)
	}
	if ensurer.calls != 1 || len(runner.requests) != 2 || runner.requests[0] != runner.requests[1] {
		t.Fatalf("repair/approval requests = %d/%#v", ensurer.calls, runner.requests)
	}
}

// TestRemoveOldNetworkingRecoversIndeterminateApprovalByReplayingStart lets the coordinator report already-applied completion.
func TestRemoveOldNetworkingRecoversIndeterminateApprovalByReplayingStart(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	awaiting := testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
	completed := testNetworkResolverPolicyMigrationOperation(domain.OperationSucceeded, 14)
	client.migrationHook = func(control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error) {
		if len(client.migrationReqs) == 1 {
			return awaiting, nil
		}
		return completed, nil
	}
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{State: networkresolverpolicymigrationapproval.Indeterminate}}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}

	result, err := app.RemoveOldNetworking()
	if err != nil {
		t.Fatalf("RemoveOldNetworking() error = %v", err)
	}
	if result != completed || len(client.migrationReqs) != 2 || len(runner.requests) != 1 {
		t.Fatalf("result/start/approval = %#v/%d/%d", result, len(client.migrationReqs), len(runner.requests))
	}
}

// TestRemoveOldNetworkingRetriesAuthorityDriftRetry starts migration once more after a stale authority response.
func TestRemoveOldNetworkingRetriesAuthorityDriftRetry(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	migration := testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 14)
	confirmation := testNetworkResolverPolicyMigrationConfirmation(migration, 16, 17)
	client.migrationHook = func(control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error) {
		if len(client.migrationReqs) == 1 {
			return control.NetworkResolverPolicyMigrationOperation{}, errors.New(`start resolver policy migration: stage network resolver policy migration authority differs from the exact resolver stage`)
		}
		return migration, nil
	}
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{
		State:        networkresolverpolicymigrationapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}

	if _, err := app.RemoveOldNetworking(); err != nil {
		t.Fatalf("RemoveOldNetworking() error = %v", err)
	}
	if len(client.migrationReqs) != 2 {
		t.Fatalf("migration starts = %d, want 2", len(client.migrationReqs))
	}
	if len(runner.requests) != 1 {
		t.Fatalf("migration approval requests = %d, want 1", len(runner.requests))
	}
}

// TestRemoveOldNetworkingAuthorityDriftRetriesAreBounded after repeated authority mismatch returns the final migration error.
func TestRemoveOldNetworkingAuthorityDriftRetriesAreBounded(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migrationHook = func(control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error) {
		return control.NetworkResolverPolicyMigrationOperation{}, errors.New(`start resolver policy migration: stage operation: stage network resolver policy migration: network resolver policy migration authority differs from the exact resolver stage`)
	}

	if _, err := app.RemoveOldNetworking(); err == nil {
		t.Fatalf("RemoveOldNetworking() error = %v, want authority drift error", err)
	}
	if len(client.migrationReqs) != 3 {
		t.Fatalf("migration starts = %d, want 3", len(client.migrationReqs))
	}
}

// TestRemoveOldNetworkingDoesNotReplayMutationFailure avoids blindly repeating a helper mutation after a bounded failure.
func TestRemoveOldNetworkingDoesNotReplayMutationFailure(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.migration = testNetworkResolverPolicyMigrationOperation(domain.OperationRequiresApproval, 11)
	runner := &fakeNetworkResolverPolicyMigrationApprovalRunner{outcome: networkresolverpolicymigrationapproval.Outcome{
		State: networkresolverpolicymigrationapproval.HelperFailed,
		HelperFailure: &networkresolverpolicymigrationapproval.HelperFailure{
			Code:    helper.ErrorCodeMutationFailed,
			Message: "resolver retirement failed",
		},
	}}
	app.migrationApproval = func(networkresolverpolicymigrationapproval.Client) networkResolverPolicyMigrationApprovalRunner {
		return runner
	}

	if _, err := app.RemoveOldNetworking(); err == nil || !strings.Contains(err.Error(), "resolver retirement failed") {
		t.Fatalf("RemoveOldNetworking() error = %v, want mutation failure", err)
	}
	if len(client.migrationReqs) != 1 || len(runner.requests) != 1 {
		t.Fatalf("start/approval calls = %d/%d, want 1/1", len(client.migrationReqs), len(runner.requests))
	}
}

// testNetworkResolverPolicyMigrationOperation returns one valid global retirement migration at the requested state and revision.
func testNetworkResolverPolicyMigrationOperation(state domain.OperationState, revision domain.Sequence) control.NetworkResolverPolicyMigrationOperation {
	requestedAt := time.Date(2026, time.July, 22, 12, 11, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	operation := domain.Operation{
		ID:          "operation-network-resolver-policy-migration",
		IntentID:    networkResolverPolicyMigrationIntentID,
		Kind:        domain.OperationKindNetworkResolverPolicyMigration,
		State:       state,
		RequestedAt: requestedAt,
		StartedAt:   &startedAt,
	}
	if state == domain.OperationRequiresApproval {
		operation.Phase = "awaiting resolver policy migration approval"
	} else {
		finishedAt := requestedAt.Add(2 * time.Second)
		operation.Phase = "completed"
		operation.FinishedAt = &finishedAt
	}
	return control.NetworkResolverPolicyMigrationOperation{
		Operation: operation,
		Revision:  revision,
	}
}

// testNetworkResolverPolicyMigrationConfirmation advances one selected migration to a valid contiguous terminal result.
func testNetworkResolverPolicyMigrationConfirmation(
	operation control.NetworkResolverPolicyMigrationOperation,
	networkRevision domain.Sequence,
	revision domain.Sequence,
) control.NetworkResolverPolicyMigrationApprovalConfirmation {
	completed := operation.Operation
	finishedAt := completed.RequestedAt.Add(2 * time.Second)
	completed.State = domain.OperationSucceeded
	completed.Phase = "completed"
	completed.FinishedAt = &finishedAt
	return control.NetworkResolverPolicyMigrationApprovalConfirmation{
		Operation:       completed,
		Revision:        revision,
		NetworkRevision: networkRevision,
	}
}

// testNetworkDataPlaneSetupOperation returns one valid trusted-ingress operation at the requested lifecycle state and phase.
func testNetworkDataPlaneSetupOperation(state domain.OperationState, phase string, revision domain.Sequence) control.NetworkDataPlaneSetupOperation {
	requestedAt := time.Date(2026, time.July, 18, 12, 12, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := requestedAt.Add(2 * time.Second)
	operation := domain.Operation{
		ID:          "operation-network-data-plane-setup",
		IntentID:    networkDataPlaneSetupIntentID,
		Kind:        domain.OperationKindNetworkDataPlaneSetup,
		State:       state,
		Phase:       phase,
		RequestedAt: requestedAt,
	}
	switch state {
	case domain.OperationRunning, domain.OperationRequiresApproval:
		operation.StartedAt = &startedAt
	case domain.OperationSucceeded:
		operation.StartedAt = &startedAt
		operation.FinishedAt = &finishedAt
	case domain.OperationFailed:
		operation.StartedAt = &startedAt
		operation.FinishedAt = &finishedAt
		operation.Problem = &domain.Problem{Code: "data_plane_failed", Message: "data plane failed", Retryable: true}
	case domain.OperationCancelled:
		operation.FinishedAt = &finishedAt
	}
	return control.NetworkDataPlaneSetupOperation{Operation: operation, Revision: revision}
}

// testNetworkDataPlaneSetupConfirmation completes one exact low-port approval replay.
func testNetworkDataPlaneSetupConfirmation(setup control.NetworkDataPlaneSetupOperation, networkRevision domain.Sequence, revision domain.Sequence) control.NetworkDataPlaneSetupConfirmation {
	operation := setup.Operation
	finishedAt := operation.RequestedAt.Add(3 * time.Second)
	operation.State = domain.OperationSucceeded
	operation.Phase = "completed"
	operation.FinishedAt = &finishedAt
	operation.Problem = nil
	return control.NetworkDataPlaneSetupConfirmation{Operation: operation, Revision: revision, NetworkRevision: networkRevision}
}

// testRegistration returns a valid inert registration without claiming network-backed resources.
func testRegistration() control.ProjectRegistration {
	project := testSnapshot().Projects[0]
	project.State = domain.ProjectStopped
	project.Apps = []domain.AppSnapshot{}
	project.Services = []domain.ServiceSnapshot{}
	project.Resources = []domain.ResourceSnapshot{}
	return control.ProjectRegistration{
		Project:  project,
		Revision: 9,
		Created:  true,
	}
}

// testUnregistration returns a valid queued operation before project-specific cleanup has been observed.
func testUnregistration() control.ProjectUnregistration {
	return control.ProjectUnregistration{
		Operation: domain.Operation{
			ID:          "operation-remove-orders",
			IntentID:    "desktop-remove-orders",
			Kind:        domain.OperationKindProjectUnregister,
			ProjectID:   "orders",
			State:       domain.OperationQueued,
			Phase:       string(domain.OperationQueued),
			RequestedAt: time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
		},
		Revision: 9,
	}
}

// testProjectRemovalOperation returns one valid removal replay at the requested state and revision.
func testProjectRemovalOperation(state domain.OperationState, revision domain.Sequence) control.ProjectUnregistration {
	result := testUnregistration()
	result.Revision = revision
	startedAt := result.Operation.RequestedAt.Add(time.Second)
	finishedAt := result.Operation.RequestedAt.Add(2 * time.Second)
	result.Operation.State = state
	result.Operation.Phase = string(state)
	result.Operation.StartedAt = nil
	result.Operation.FinishedAt = nil
	result.Operation.Problem = nil
	switch state {
	case domain.OperationRunning, domain.OperationRequiresApproval:
		result.Operation.StartedAt = &startedAt
	case domain.OperationSucceeded:
		result.Operation.StartedAt = &startedAt
		result.Operation.FinishedAt = &finishedAt
	case domain.OperationFailed:
		result.Operation.StartedAt = &startedAt
		result.Operation.FinishedAt = &finishedAt
		result.Operation.Problem = &domain.Problem{
			Code:      "release_failed",
			Message:   "release failed",
			Retryable: true,
		}
	case domain.OperationCancelled:
		result.Operation.FinishedAt = &finishedAt
	}
	return result
}

// testProjectRemovalApprovalConfirmation completes one exact approval replay.
func testProjectRemovalApprovalConfirmation(
	replayed control.ProjectUnregistration,
	revision domain.Sequence,
) control.ProjectUnregisterApprovalConfirmation {
	operation := replayed.Operation
	if operation.StartedAt == nil {
		startedAt := operation.RequestedAt.Add(time.Second)
		operation.StartedAt = &startedAt
	}
	finishedAt := operation.RequestedAt.Add(3 * time.Second)
	operation.State = domain.OperationSucceeded
	operation.Phase = string(domain.OperationSucceeded)
	operation.FinishedAt = &finishedAt
	operation.Problem = nil
	return control.ProjectUnregisterApprovalConfirmation{
		Operation: operation,
		Revision:  revision,
	}
}

// testProjectActivity returns one valid current-session output chunk.
func testProjectActivity() control.ProjectActivity {
	return control.ProjectActivity{
		ProjectID: "orders",
		Session: &control.ProjectSessionActivity{
			ID:         "session-orders",
			State:      domain.SessionAwaitingAttach,
			Generation: 1,
			Output: control.ProjectOutputChunk{
				Available:  true,
				NextCursor: 13,
				Text:       "ding app\n",
			},
		},
	}
}

// testServiceLogs returns one valid current-session Compose output chunk.
func testServiceLogs() control.ServiceLogs {
	return control.ServiceLogs{
		ProjectID: "orders",
		ServiceID: "mysql",
		SessionID: "session-orders",
		Supported: true,
		Available: true,
		Output: control.ServiceLogOutputChunk{
			Available:  true,
			NextCursor: 22,
			Text:       "ready for connections\n",
		},
	}
}

// testProjectLifecycle returns a valid queued operation for one exact desktop lifecycle intent.
func testProjectLifecycle(kind domain.OperationKind, intentID domain.IntentID) control.ProjectLifecycleOperation {
	return control.ProjectLifecycleOperation{
		Operation: domain.Operation{
			ID:          domain.OperationID("operation-" + string(kind)),
			IntentID:    intentID,
			Kind:        kind,
			ProjectID:   "orders",
			State:       domain.OperationQueued,
			Phase:       string(domain.OperationQueued),
			RequestedAt: time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
		},
		Revision: 9,
	}
}

// testProjectRuntimeRepairInspection returns one valid bounded display with opaque confirmation selectors.
func testProjectRuntimeRepairInspection() control.ProjectRuntimeRepairInspection {
	return control.ProjectRuntimeRepairInspection{
		ProjectID:   "orders",
		Disposition: control.ProjectRuntimeRepairInspectionConfirmable,
		Confirmable: &control.ProjectRuntimeRepairConfirmable{
			Candidate: control.ProjectRuntimeRepairDisplayFacts{
				Command:     "forj dev",
				Checkout:    "/workspace/orders",
				Endpoint:    "127.77.0.10:3000",
				RootPID:     4127,
				MemberCount: 3,
			},
			InspectionID:         control.ProjectRuntimeRepairInspectionID(strings.Repeat("a", 64)),
			CandidateFingerprint: control.ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", 64)),
			ExpiresAt:            time.Date(2099, time.July, 18, 12, 7, 0, 0, time.UTC),
		},
	}
}

// testProjectRuntimeRepairConfirmation returns one valid stopped projection for the inspected project.
func testProjectRuntimeRepairConfirmation() control.ProjectRuntimeRepairConfirmation {
	project := testSnapshot().Projects[0]
	project.State = domain.ProjectStopped
	project.UpdatedAt = time.Date(2026, time.July, 18, 12, 8, 0, 0, time.UTC)
	project.Apps = []domain.AppSnapshot{
		{ID: "app", Name: "App", State: domain.EntityStopped, Required: true},
	}
	project.Services = []domain.ServiceSnapshot{}
	project.Resources = []domain.ResourceSnapshot{}
	return control.ProjectRuntimeRepairConfirmation{Project: project, Revision: 9}
}

// cloneSnapshot copies the nested collections a test may mutate after a fake response.
func cloneSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	copySnapshot := snapshot
	copySnapshot.Projects = make([]domain.ProjectSnapshot, len(snapshot.Projects))
	copy(copySnapshot.Projects, snapshot.Projects)
	for index := range copySnapshot.Projects {
		copySnapshot.Projects[index].Apps = make([]domain.AppSnapshot, len(snapshot.Projects[index].Apps))
		copy(copySnapshot.Projects[index].Apps, snapshot.Projects[index].Apps)
		copySnapshot.Projects[index].Services = make([]domain.ServiceSnapshot, len(snapshot.Projects[index].Services))
		copy(copySnapshot.Projects[index].Services, snapshot.Projects[index].Services)
		copySnapshot.Projects[index].Resources = make([]domain.ResourceSnapshot, len(snapshot.Projects[index].Resources))
		copy(copySnapshot.Projects[index].Resources, snapshot.Projects[index].Resources)
	}
	copySnapshot.Operations = make([]domain.Operation, len(snapshot.Operations))
	copy(copySnapshot.Operations, snapshot.Operations)
	copySnapshot.RecentResourceIDs = make([]domain.ResourceRef, len(snapshot.RecentResourceIDs))
	copy(copySnapshot.RecentResourceIDs, snapshot.RecentResourceIDs)
	return copySnapshot
}
