package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// fakeDaemonControlClient records one-shot control calls and their cleanup.
type fakeDaemonControlClient struct {
	status                                 control.DaemonStatus
	snapshot                               domain.Snapshot
	projectActivity                        control.ProjectActivity
	serviceLogs                            control.ServiceLogs
	registration                           control.ProjectRegistration
	unregistration                         control.ProjectUnregistration
	startLifecycle                         control.ProjectLifecycleOperation
	stopLifecycle                          control.ProjectLifecycleOperation
	restartLifecycle                       control.ProjectLifecycleOperation
	networkSetup                           control.NetworkSetupOperation
	networkSetupPreparation                control.NetworkSetupApprovalPreparation
	networkSetupConfirmation               control.NetworkSetupApprovalConfirmation
	resolverSetup                          control.NetworkResolverSetupOperation
	resolverPreparation                    control.NetworkResolverSetupApprovalPreparation
	resolverConfirmation                   control.NetworkResolverSetupApprovalConfirmation
	dataPlaneSetup                         control.NetworkDataPlaneSetupOperation
	dataPlaneTrustPreparation              control.NetworkDataPlaneTrustApprovalPreparation
	dataPlaneTrustConfirmation             control.NetworkDataPlaneSetupOperation
	dataPlaneLowPortPreparation            control.NetworkDataPlaneLowPortApprovalPreparation
	dataPlaneLowPortConfirmation           control.NetworkDataPlaneSetupConfirmation
	networkRelease                         control.NetworkReleaseOperation
	networkReleaseLowPortPreparation       control.NetworkReleaseApprovalPreparation
	networkReleaseResolverPreparation      control.NetworkReleaseResolverApprovalPreparation
	networkReleaseTrustPreparation         control.NetworkReleaseTrustApprovalPreparation
	networkReleaseLoopbackPreparation      control.NetworkReleaseLoopbackApprovalPreparation
	networkReleaseOwnershipConfirmation    control.NetworkReleaseOperation
	statusErr                              error
	stopErr                                error
	snapshotErr                            error
	projectActivityErr                     error
	serviceLogsErr                         error
	registrationErr                        error
	unregistrationErr                      error
	startLifecycleErr                      error
	stopLifecycleErr                       error
	restartLifecycleErr                    error
	networkSetupErr                        error
	networkSetupPreparationErr             error
	networkSetupConfirmationErr            error
	resolverSetupErr                       error
	resolverPreparationErr                 error
	resolverConfirmationErr                error
	dataPlaneSetupErr                      error
	dataPlaneTrustPreparationErr           error
	dataPlaneTrustConfirmationErr          error
	dataPlaneLowPortPreparationErr         error
	dataPlaneLowPortConfirmationErr        error
	networkReleaseErr                      error
	networkReleaseLowPortPreparationErr    error
	networkReleaseResolverPreparationErr   error
	networkReleaseTrustPreparationErr      error
	networkReleaseLoopbackPreparationErr   error
	networkReleaseOwnershipConfirmationErr error
	networkReleaseStartHook                func(context.Context, control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error)
	networkReleaseOwnershipConfirmations   []control.ConfirmNetworkReleaseOwnershipRequest
	closeErr                               error
	statusCalls                            int
	stopCalls                              int
	snapshotCalls                          int
	projectActivityCalls                   int
	serviceLogsCalls                       int
	projectActivityRequests                []control.ProjectActivityRequest
	serviceLogsRequests                    []control.ServiceLogsRequest
	projectActivityHook                    func(context.Context, control.ProjectActivityRequest) (control.ProjectActivity, error)
	serviceLogsHook                        func(context.Context, control.ServiceLogsRequest) (control.ServiceLogs, error)
	registrationCalls                      int
	unregistrationCalls                    int
	startLifecycleCalls                    int
	stopLifecycleCalls                     int
	restartLifecycleCalls                  int
	networkSetupCalls                      int
	networkSetupPreparationCalls           int
	networkSetupConfirmationCalls          int
	resolverSetupCalls                     int
	resolverPreparationCalls               int
	resolverConfirmationCalls              int
	dataPlaneSetupCalls                    int
	dataPlaneTrustPreparationCalls         int
	dataPlaneTrustConfirmationCalls        int
	dataPlaneLowPortPreparationCalls       int
	dataPlaneLowPortConfirmationCalls      int
	registrationRequests                   []control.RegisterProjectRequest
	unregistrationRequests                 []control.UnregisterProjectRequest
	startLifecycleRequests                 []control.StartProjectRequest
	stopLifecycleRequests                  []control.StopProjectRequest
	restartLifecycleRequests               []control.RestartProjectRequest
	networkSetupRequests                   []control.StartNetworkSetupRequest
	networkSetupPreparationRequests        []control.PrepareNetworkSetupApprovalRequest
	networkSetupConfirmationRequests       []control.ConfirmNetworkSetupApprovalRequest
	resolverSetupRequests                  []control.StartNetworkResolverSetupRequest
	resolverPreparationRequests            []control.PrepareNetworkResolverSetupApprovalRequest
	resolverConfirmationRequests           []control.ConfirmNetworkResolverSetupApprovalRequest
	dataPlaneSetupRequests                 []control.StartNetworkDataPlaneSetupRequest
	dataPlaneTrustPreparationRequests      []control.PrepareNetworkDataPlaneTrustApprovalRequest
	dataPlaneTrustConfirmationRequests     []control.ConfirmNetworkDataPlaneTrustApprovalRequest
	dataPlaneLowPortPreparationRequests    []control.PrepareNetworkDataPlaneLowPortApprovalRequest
	dataPlaneLowPortConfirmationRequests   []control.ConfirmNetworkDataPlaneLowPortApprovalRequest
	networkSetupContexts                   []context.Context
	networkSetupPreparationContexts        []context.Context
	networkSetupConfirmationContexts       []context.Context
	resolverSetupContexts                  []context.Context
	resolverPreparationContexts            []context.Context
	resolverConfirmationContexts           []context.Context
	dataPlaneSetupContexts                 []context.Context
	dataPlaneTrustPreparationContexts      []context.Context
	dataPlaneTrustConfirmationContexts     []context.Context
	dataPlaneLowPortPreparationContexts    []context.Context
	dataPlaneLowPortConfirmationContexts   []context.Context
	networkSetupHook                       func(context.Context, control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	resolverSetupHook                      func(context.Context, control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error)
	dataPlaneSetupHook                     func(context.Context, control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error)
	closeCalls                             int
}

// StartNetworkRelease returns configured release progress.
func (client *fakeDaemonControlClient) StartNetworkRelease(ctx context.Context, request control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	if client.networkReleaseStartHook != nil {
		return client.networkReleaseStartHook(ctx, request)
	}
	return client.networkRelease, client.networkReleaseErr
}

// ReadNetworkRelease returns configured durable release progress.
func (client *fakeDaemonControlClient) ReadNetworkRelease(context.Context, control.ReadNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	return client.networkRelease, client.networkReleaseErr
}

// PrepareNetworkReleaseApproval returns configured low-port release preparation.
func (client *fakeDaemonControlClient) PrepareNetworkReleaseApproval(context.Context, control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error) {
	return client.networkReleaseLowPortPreparation, client.networkReleaseLowPortPreparationErr
}

// ConfirmNetworkReleaseApproval returns configured release progress after low-port evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkReleaseApproval(context.Context, control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error) {
	return client.networkRelease, client.networkReleaseErr
}

// PrepareNetworkReleaseResolverApproval returns configured resolver release preparation.
func (client *fakeDaemonControlClient) PrepareNetworkReleaseResolverApproval(context.Context, control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error) {
	return client.networkReleaseResolverPreparation, client.networkReleaseResolverPreparationErr
}

// ConfirmNetworkReleaseResolverApproval returns configured release progress after resolver evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkReleaseResolverApproval(context.Context, control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error) {
	return client.networkRelease, client.networkReleaseErr
}

// PrepareNetworkReleaseTrustApproval returns configured trust release preparation.
func (client *fakeDaemonControlClient) PrepareNetworkReleaseTrustApproval(context.Context, control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error) {
	return client.networkReleaseTrustPreparation, client.networkReleaseTrustPreparationErr
}

// ConfirmNetworkReleaseTrustApproval returns configured release progress after trust evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkReleaseTrustApproval(context.Context, control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error) {
	return client.networkRelease, client.networkReleaseErr
}

// PrepareNetworkReleaseLoopbackApproval returns configured loopback release preparation.
func (client *fakeDaemonControlClient) PrepareNetworkReleaseLoopbackApproval(context.Context, control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error) {
	return client.networkReleaseLoopbackPreparation, client.networkReleaseLoopbackPreparationErr
}

// ConfirmNetworkReleaseLoopbackApproval returns configured release progress after loopback evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkReleaseLoopbackApproval(context.Context, control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error) {
	return client.networkRelease, client.networkReleaseErr
}

// ConfirmNetworkReleaseOwnership returns configured release progress after ownership confirmation.
func (client *fakeDaemonControlClient) ConfirmNetworkReleaseOwnership(_ context.Context, request control.ConfirmNetworkReleaseOwnershipRequest) (control.NetworkReleaseOperation, error) {
	client.networkReleaseOwnershipConfirmations = append(client.networkReleaseOwnershipConfirmations, request)
	if client.networkReleaseOwnershipConfirmation != (control.NetworkReleaseOperation{}) || client.networkReleaseOwnershipConfirmationErr != nil {
		return client.networkReleaseOwnershipConfirmation, client.networkReleaseOwnershipConfirmationErr
	}
	return client.networkRelease, client.networkReleaseErr
}

// StartNetworkResolverSetup returns configured resolver progress and records the selected intent.
func (client *fakeDaemonControlClient) StartNetworkResolverSetup(
	ctx context.Context,
	request control.StartNetworkResolverSetupRequest,
) (control.NetworkResolverSetupOperation, error) {
	client.resolverSetupCalls++
	client.resolverSetupRequests = append(client.resolverSetupRequests, request)
	client.resolverSetupContexts = append(client.resolverSetupContexts, ctx)
	if client.resolverSetupHook != nil {
		return client.resolverSetupHook(ctx, request)
	}
	return client.resolverSetup, client.resolverSetupErr
}

// PrepareNetworkResolverSetupApproval returns configured resolver approval preparation and records its selection.
func (client *fakeDaemonControlClient) PrepareNetworkResolverSetupApproval(
	ctx context.Context,
	request control.PrepareNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalPreparation, error) {
	client.resolverPreparationCalls++
	client.resolverPreparationRequests = append(client.resolverPreparationRequests, request)
	client.resolverPreparationContexts = append(client.resolverPreparationContexts, ctx)
	return client.resolverPreparation, client.resolverPreparationErr
}

// ConfirmNetworkResolverSetupApproval returns configured resolver approval confirmation and records its evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkResolverSetupApproval(
	ctx context.Context,
	request control.ConfirmNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalConfirmation, error) {
	client.resolverConfirmationCalls++
	client.resolverConfirmationRequests = append(client.resolverConfirmationRequests, request)
	client.resolverConfirmationContexts = append(client.resolverConfirmationContexts, ctx)
	return client.resolverConfirmation, client.resolverConfirmationErr
}

// StartNetworkDataPlaneSetup returns configured data-plane progress and records the selected intent.
func (client *fakeDaemonControlClient) StartNetworkDataPlaneSetup(
	ctx context.Context,
	request control.StartNetworkDataPlaneSetupRequest,
) (control.NetworkDataPlaneSetupOperation, error) {
	client.dataPlaneSetupCalls++
	client.dataPlaneSetupRequests = append(client.dataPlaneSetupRequests, request)
	client.dataPlaneSetupContexts = append(client.dataPlaneSetupContexts, ctx)
	if client.dataPlaneSetupHook != nil {
		return client.dataPlaneSetupHook(ctx, request)
	}
	return client.dataPlaneSetup, client.dataPlaneSetupErr
}

// PrepareNetworkDataPlaneTrustApproval returns configured trust approval preparation and records its selection.
func (client *fakeDaemonControlClient) PrepareNetworkDataPlaneTrustApproval(
	ctx context.Context,
	request control.PrepareNetworkDataPlaneTrustApprovalRequest,
) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
	client.dataPlaneTrustPreparationCalls++
	client.dataPlaneTrustPreparationRequests = append(client.dataPlaneTrustPreparationRequests, request)
	client.dataPlaneTrustPreparationContexts = append(client.dataPlaneTrustPreparationContexts, ctx)
	return client.dataPlaneTrustPreparation, client.dataPlaneTrustPreparationErr
}

// ConfirmNetworkDataPlaneTrustApproval returns configured trust approval progress and records its evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkDataPlaneTrustApproval(
	ctx context.Context,
	request control.ConfirmNetworkDataPlaneTrustApprovalRequest,
) (control.NetworkDataPlaneSetupOperation, error) {
	client.dataPlaneTrustConfirmationCalls++
	client.dataPlaneTrustConfirmationRequests = append(client.dataPlaneTrustConfirmationRequests, request)
	client.dataPlaneTrustConfirmationContexts = append(client.dataPlaneTrustConfirmationContexts, ctx)
	return client.dataPlaneTrustConfirmation, client.dataPlaneTrustConfirmationErr
}

// PrepareNetworkDataPlaneLowPortApproval returns configured low-port approval preparation and records its selection.
func (client *fakeDaemonControlClient) PrepareNetworkDataPlaneLowPortApproval(
	ctx context.Context,
	request control.PrepareNetworkDataPlaneLowPortApprovalRequest,
) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
	client.dataPlaneLowPortPreparationCalls++
	client.dataPlaneLowPortPreparationRequests = append(client.dataPlaneLowPortPreparationRequests, request)
	client.dataPlaneLowPortPreparationContexts = append(client.dataPlaneLowPortPreparationContexts, ctx)
	return client.dataPlaneLowPortPreparation, client.dataPlaneLowPortPreparationErr
}

// ConfirmNetworkDataPlaneLowPortApproval returns configured low-port approval confirmation and records its evidence.
func (client *fakeDaemonControlClient) ConfirmNetworkDataPlaneLowPortApproval(
	ctx context.Context,
	request control.ConfirmNetworkDataPlaneLowPortApprovalRequest,
) (control.NetworkDataPlaneSetupConfirmation, error) {
	client.dataPlaneLowPortConfirmationCalls++
	client.dataPlaneLowPortConfirmationRequests = append(client.dataPlaneLowPortConfirmationRequests, request)
	client.dataPlaneLowPortConfirmationContexts = append(client.dataPlaneLowPortConfirmationContexts, ctx)
	return client.dataPlaneLowPortConfirmation, client.dataPlaneLowPortConfirmationErr
}

// StartProject returns configured lifecycle progress and records the client-owned start intent.
func (client *fakeDaemonControlClient) StartProject(
	_ context.Context,
	request control.StartProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	client.startLifecycleCalls++
	client.startLifecycleRequests = append(client.startLifecycleRequests, request)
	return client.startLifecycle, client.startLifecycleErr
}

// StopProject returns configured lifecycle progress and records the client-owned stop intent.
func (client *fakeDaemonControlClient) StopProject(
	_ context.Context,
	request control.StopProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	client.stopLifecycleCalls++
	client.stopLifecycleRequests = append(client.stopLifecycleRequests, request)
	return client.stopLifecycle, client.stopLifecycleErr
}

// RestartProject returns configured lifecycle progress and records the client-owned restart intent.
func (client *fakeDaemonControlClient) RestartProject(
	_ context.Context,
	request control.RestartProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	client.restartLifecycleCalls++
	client.restartLifecycleRequests = append(client.restartLifecycleRequests, request)
	return client.restartLifecycle, client.restartLifecycleErr
}

// StartNetworkSetup returns the configured setup operation and records the caller-owned intent.
func (client *fakeDaemonControlClient) StartNetworkSetup(
	ctx context.Context,
	request control.StartNetworkSetupRequest,
) (control.NetworkSetupOperation, error) {
	client.networkSetupCalls++
	client.networkSetupRequests = append(client.networkSetupRequests, request)
	client.networkSetupContexts = append(client.networkSetupContexts, ctx)
	if client.networkSetupHook != nil {
		return client.networkSetupHook(ctx, request)
	}
	return client.networkSetup, client.networkSetupErr
}

// PrepareNetworkSetupApproval returns the configured preparation and records the exact setup revision.
func (client *fakeDaemonControlClient) PrepareNetworkSetupApproval(
	ctx context.Context,
	request control.PrepareNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalPreparation, error) {
	client.networkSetupPreparationCalls++
	client.networkSetupPreparationRequests = append(client.networkSetupPreparationRequests, request)
	client.networkSetupPreparationContexts = append(client.networkSetupPreparationContexts, ctx)
	return client.networkSetupPreparation, client.networkSetupPreparationErr
}

// ConfirmNetworkSetupApproval returns the configured confirmation and records the helper evidence selection.
func (client *fakeDaemonControlClient) ConfirmNetworkSetupApproval(
	ctx context.Context,
	request control.ConfirmNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalConfirmation, error) {
	client.networkSetupConfirmationCalls++
	client.networkSetupConfirmationRequests = append(client.networkSetupConfirmationRequests, request)
	client.networkSetupConfirmationContexts = append(client.networkSetupConfirmationContexts, ctx)
	return client.networkSetupConfirmation, client.networkSetupConfirmationErr
}

// RegisterProject returns the configured registration and records the request.
func (client *fakeDaemonControlClient) RegisterProject(
	_ context.Context,
	request control.RegisterProjectRequest,
) (control.ProjectRegistration, error) {
	client.registrationCalls++
	client.registrationRequests = append(client.registrationRequests, request)
	return client.registration, client.registrationErr
}

// UnregisterProject returns the configured operation and records the stable client intent.
func (client *fakeDaemonControlClient) UnregisterProject(
	_ context.Context,
	request control.UnregisterProjectRequest,
) (control.ProjectUnregistration, error) {
	client.unregistrationCalls++
	client.unregistrationRequests = append(client.unregistrationRequests, request)
	return client.unregistration, client.unregistrationErr
}

// Status returns the configured daemon status and records the request.
func (client *fakeDaemonControlClient) Status(context.Context) (control.DaemonStatus, error) {
	client.statusCalls++
	return client.status, client.statusErr
}

// Stop returns the configured daemon-control result and records the request.
func (client *fakeDaemonControlClient) Stop(context.Context) error {
	client.stopCalls++
	return client.stopErr
}

// Snapshot returns the configured daemon snapshot and records the request.
func (client *fakeDaemonControlClient) Snapshot(context.Context) (domain.Snapshot, error) {
	client.snapshotCalls++
	return client.snapshot, client.snapshotErr
}

// ProjectActivity returns the configured current-session output and records the request.
func (client *fakeDaemonControlClient) ProjectActivity(ctx context.Context, request control.ProjectActivityRequest) (control.ProjectActivity, error) {
	client.projectActivityCalls++
	client.projectActivityRequests = append(client.projectActivityRequests, request)
	if client.projectActivityHook != nil {
		return client.projectActivityHook(ctx, request)
	}
	return client.projectActivity, client.projectActivityErr
}

// ServiceLogs returns the configured current-session service output and records the request.
func (client *fakeDaemonControlClient) ServiceLogs(ctx context.Context, request control.ServiceLogsRequest) (control.ServiceLogs, error) {
	client.serviceLogsCalls++
	client.serviceLogsRequests = append(client.serviceLogsRequests, request)
	if client.serviceLogsHook != nil {
		return client.serviceLogsHook(ctx, request)
	}
	return client.serviceLogs, client.serviceLogsErr
}

// Close records deterministic connection cleanup and returns its configured failure.
func (client *fakeDaemonControlClient) Close() error {
	client.closeCalls++
	return client.closeErr
}

// TestDaemonClientConnectsOnlyWhenRequested verifies application wiring cannot contact harbord during construction.
func TestDaemonClientConnectsOnlyWhenRequested(t *testing.T) {
	productionClient := NewDaemonClient()
	if productionClient == nil || productionClient.connect == nil {
		t.Fatal("production constructor returned an unusable lazy client")
	}

	connection := &fakeDaemonControlClient{status: daemonTestStatus()}
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return connection, nil
	})

	if connectCalls != 0 {
		t.Fatalf("construction opened %d connections, want 0", connectCalls)
	}
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !reflect.DeepEqual(status, connection.status) {
		t.Fatalf("status = %#v, want %#v", status, connection.status)
	}
	if connectCalls != 1 || connection.statusCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = connect:%d status:%d close:%d, want 1 each", connectCalls, connection.statusCalls, connection.closeCalls)
	}
}

// TestDaemonClientReturnsCloseFailureAfterSuccessfulRequest verifies cleanup errors remain visible without a request failure.
func TestDaemonClientReturnsCloseFailureAfterSuccessfulRequest(t *testing.T) {
	closeErr := errors.New("close failed")
	connection := &fakeDaemonControlClient{
		status:   daemonTestStatus(),
		closeErr: closeErr,
	}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})

	_, err := client.Status(t.Context())
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want %v", err, closeErr)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
}

// TestDaemonClientSnapshotUsesASeparateOneShotConnection verifies every command owns its complete connection lifetime.
func TestDaemonClientSnapshotUsesASeparateOneShotConnection(t *testing.T) {
	first := &fakeDaemonControlClient{status: daemonTestStatus()}
	second := &fakeDaemonControlClient{snapshot: daemonTestSnapshot()}
	connections := []*fakeDaemonControlClient{first, second}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connection := connections[0]
		connections = connections[1:]
		return connection, nil
	})

	if _, err := client.Status(t.Context()); err != nil {
		t.Fatalf("read status: %v", err)
	}
	snapshot, err := client.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snapshot.Sequence != second.snapshot.Sequence {
		t.Fatalf("snapshot sequence = %d, want %d", snapshot.Sequence, second.snapshot.Sequence)
	}
	if first.closeCalls != 1 || second.closeCalls != 1 || second.snapshotCalls != 1 {
		t.Fatalf("calls = first close:%d second snapshot:%d close:%d, want 1 each", first.closeCalls, second.snapshotCalls, second.closeCalls)
	}
}

// TestDaemonClientConfirmsNetworkReleaseOwnershipUsesOneShotConnection verifies the authenticated terminal confirmation keeps the CLI cleanup contract.
func TestDaemonClientConfirmsNetworkReleaseOwnershipUsesOneShotConnection(t *testing.T) {
	request := control.ConfirmNetworkReleaseOwnershipRequest{
		OperationID:                "operation-network-release",
		ExpectedCheckpointRevision: 6,
	}
	connection := &fakeDaemonControlClient{}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})

	if _, err := client.ConfirmNetworkReleaseOwnership(t.Context(), request); err != nil {
		t.Fatalf("confirm network release ownership: %v", err)
	}
	if !reflect.DeepEqual(connection.networkReleaseOwnershipConfirmations, []control.ConfirmNetworkReleaseOwnershipRequest{request}) || connection.closeCalls != 1 {
		t.Fatalf("confirmations = %#v, close calls = %d", connection.networkReleaseOwnershipConfirmations, connection.closeCalls)
	}
}

// TestDaemonClientStopUsesOneAcknowledgedConnection verifies shutdown acknowledgement and cleanup share one transport lifetime.
func TestDaemonClientStopUsesOneAcknowledgedConnection(t *testing.T) {
	connection := &fakeDaemonControlClient{}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})

	if err := client.Stop(t.Context()); err != nil {
		t.Fatalf("stop daemon: %v", err)
	}
	if connection.stopCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = stop:%d close:%d, want 1 each", connection.stopCalls, connection.closeCalls)
	}
}

// TestDaemonClientRegistrationUsesASeparateOneShotConnection verifies project mutations share the CLI cleanup contract.
func TestDaemonClientRegistrationUsesASeparateOneShotConnection(t *testing.T) {
	registration := addTestRegistration(t.TempDir(), true)
	connection := &fakeDaemonControlClient{registration: registration}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})
	request := control.RegisterProjectRequest{Path: registration.Project.Path}

	got, err := client.RegisterProject(t.Context(), request)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	if !reflect.DeepEqual(got, registration) {
		t.Fatalf("registration = %#v, want %#v", got, registration)
	}
	if connection.registrationCalls != 1 || connection.closeCalls != 1 || len(connection.registrationRequests) != 1 || connection.registrationRequests[0] != request {
		t.Fatalf("calls = registration:%d close:%d requests:%#v", connection.registrationCalls, connection.closeCalls, connection.registrationRequests)
	}
}

// TestDaemonClientUnregistrationUsesASeparateOneShotConnection verifies removal shares the CLI cleanup contract without opening a second transport.
func TestDaemonClientUnregistrationUsesASeparateOneShotConnection(t *testing.T) {
	unregistration := removeTestUnregistration(t, domain.OperationSucceeded)
	connection := &fakeDaemonControlClient{unregistration: unregistration}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})
	request := control.UnregisterProjectRequest{
		ProjectID: unregistration.Operation.ProjectID,
		IntentID:  unregistration.Operation.IntentID,
	}

	got, err := client.UnregisterProject(t.Context(), request)
	if err != nil {
		t.Fatalf("unregister project: %v", err)
	}
	if !reflect.DeepEqual(got, unregistration) {
		t.Fatalf("unregistration = %#v, want %#v", got, unregistration)
	}
	if connection.unregistrationCalls != 1 || connection.closeCalls != 1 || len(connection.unregistrationRequests) != 1 || connection.unregistrationRequests[0] != request {
		t.Fatalf("calls = unregistration:%d close:%d requests:%#v", connection.unregistrationCalls, connection.closeCalls, connection.unregistrationRequests)
	}
}

// TestDaemonClientNetworkSetupMethodsForwardRequestsAndCleanup verifies each setup phase keeps its exact DTO and one-shot lifetime.
func TestDaemonClientNetworkSetupMethodsForwardRequestsAndCleanup(t *testing.T) {
	closeErr := errors.New("close failed")
	startRequest := control.StartNetworkSetupRequest{
		IntentID: "intent-network-setup",
	}
	startResult := control.NetworkSetupOperation{
		Operation: domain.Operation{
			ID:       "operation-network-setup",
			IntentID: startRequest.IntentID,
		},
		Revision: 7,
	}
	prepareRequest := control.PrepareNetworkSetupApprovalRequest{
		OperationID:               startResult.Operation.ID,
		ExpectedOperationRevision: startResult.Revision,
	}
	prepareResult := control.NetworkSetupApprovalPreparation{
		OperationID:       prepareRequest.OperationID,
		OperationRevision: prepareRequest.ExpectedOperationRevision,
		Ticket: control.NetworkSetupApprovalTicket{
			OperationID: prepareRequest.OperationID,
		},
	}
	confirmRequest := control.ConfirmNetworkSetupApprovalRequest{
		OperationID:               prepareResult.OperationID,
		ExpectedOperationRevision: prepareResult.OperationRevision,
	}
	confirmRequest.PoolEvidence.Pool = "127.42.0.0/29"
	confirmResult := control.NetworkSetupApprovalConfirmation{
		Operation: domain.Operation{
			ID: confirmRequest.OperationID,
		},
		Revision:        8,
		NetworkRevision: 7,
		Pool:            confirmRequest.PoolEvidence.Pool,
	}

	for _, test := range []struct {
		name       string
		connection *fakeDaemonControlClient
		call       func(*DaemonClient) (any, error)
		want       any
		assertCall func(*testing.T, *fakeDaemonControlClient)
	}{
		{
			name: "start",
			connection: &fakeDaemonControlClient{
				networkSetup: startResult,
				closeErr:     closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				result, err := client.StartNetworkSetup(nil, startRequest)
				return result, err
			},
			want: startResult,
			assertCall: func(t *testing.T, connection *fakeDaemonControlClient) {
				t.Helper()
				if connection.networkSetupCalls != 1 ||
					!reflect.DeepEqual(connection.networkSetupRequests, []control.StartNetworkSetupRequest{startRequest}) ||
					len(connection.networkSetupContexts) != 1 || connection.networkSetupContexts[0] != nil {
					t.Fatalf("calls = %d, requests = %#v, contexts = %#v", connection.networkSetupCalls, connection.networkSetupRequests, connection.networkSetupContexts)
				}
			},
		},
		{
			name: "prepare approval",
			connection: &fakeDaemonControlClient{
				networkSetupPreparation: prepareResult,
				closeErr:                closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				result, err := client.PrepareNetworkSetupApproval(nil, prepareRequest)
				return result, err
			},
			want: prepareResult,
			assertCall: func(t *testing.T, connection *fakeDaemonControlClient) {
				t.Helper()
				if connection.networkSetupPreparationCalls != 1 ||
					!reflect.DeepEqual(connection.networkSetupPreparationRequests, []control.PrepareNetworkSetupApprovalRequest{prepareRequest}) ||
					len(connection.networkSetupPreparationContexts) != 1 || connection.networkSetupPreparationContexts[0] != nil {
					t.Fatalf("calls = %d, requests = %#v, contexts = %#v", connection.networkSetupPreparationCalls, connection.networkSetupPreparationRequests, connection.networkSetupPreparationContexts)
				}
			},
		},
		{
			name: "confirm approval",
			connection: &fakeDaemonControlClient{
				networkSetupConfirmation: confirmResult,
				closeErr:                 closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				result, err := client.ConfirmNetworkSetupApproval(nil, confirmRequest)
				return result, err
			},
			want: confirmResult,
			assertCall: func(t *testing.T, connection *fakeDaemonControlClient) {
				t.Helper()
				if connection.networkSetupConfirmationCalls != 1 ||
					!reflect.DeepEqual(connection.networkSetupConfirmationRequests, []control.ConfirmNetworkSetupApprovalRequest{confirmRequest}) ||
					len(connection.networkSetupConfirmationContexts) != 1 || connection.networkSetupConfirmationContexts[0] != nil {
					t.Fatalf("calls = %d, requests = %#v, contexts = %#v", connection.networkSetupConfirmationCalls, connection.networkSetupConfirmationRequests, connection.networkSetupConfirmationContexts)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var connectContexts []context.Context
			client := newDaemonClient(func(ctx context.Context) (daemonControlClient, error) {
				connectContexts = append(connectContexts, ctx)
				return test.connection, nil
			})

			got, err := test.call(client)
			if !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want %v", err, closeErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
			if len(connectContexts) != 1 || connectContexts[0] != nil {
				t.Fatalf("connect contexts = %#v, want one nil context", connectContexts)
			}
			if test.connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", test.connection.closeCalls)
			}
			test.assertCall(t, test.connection)
		})
	}
}

// TestDaemonClientTrustedIngressMethodsForwardRequestsAndCleanup covers the resolver and data-plane client surfaces.
func TestDaemonClientTrustedIngressMethodsForwardRequestsAndCleanup(t *testing.T) {
	closeErr := errors.New("close failed")
	resolverStartRequest := control.StartNetworkResolverSetupRequest{
		IntentID: "intent-network-resolver-setup",
	}
	resolverStartResult := control.NetworkResolverSetupOperation{
		Operation: domain.Operation{
			ID:       "operation-network-resolver",
			IntentID: resolverStartRequest.IntentID,
		},
		Revision: 7,
	}
	resolverPrepareRequest := control.PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               resolverStartResult.Operation.ID,
		ExpectedOperationRevision: resolverStartResult.Revision,
	}
	resolverPrepareResult := control.NetworkResolverSetupApprovalPreparation{
		OperationID:       resolverPrepareRequest.OperationID,
		OperationRevision: resolverPrepareRequest.ExpectedOperationRevision,
		Ticket: control.NetworkResolverSetupApprovalTicket{
			OperationID: resolverPrepareRequest.OperationID,
		},
	}
	resolverConfirmRequest := control.ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               resolverPrepareResult.OperationID,
		ExpectedOperationRevision: resolverPrepareResult.OperationRevision,
	}
	resolverConfirmResult := control.NetworkResolverSetupApprovalConfirmation{
		Operation: domain.Operation{
			ID: resolverConfirmRequest.OperationID,
		},
		NetworkRevision: 8,
		Revision:        9,
	}
	dataPlaneStartRequest := control.StartNetworkDataPlaneSetupRequest{
		IntentID: "intent-network-data-plane-setup",
	}
	dataPlaneStartResult := control.NetworkDataPlaneSetupOperation{
		Operation: domain.Operation{
			ID:       "operation-network-data-plane",
			IntentID: dataPlaneStartRequest.IntentID,
		},
		Revision: 10,
	}
	trustPrepareRequest := control.PrepareNetworkDataPlaneTrustApprovalRequest{
		OperationID:               dataPlaneStartResult.Operation.ID,
		ExpectedOperationRevision: dataPlaneStartResult.Revision,
	}
	trustPrepareResult := control.NetworkDataPlaneTrustApprovalPreparation{
		OperationID:       trustPrepareRequest.OperationID,
		OperationRevision: trustPrepareRequest.ExpectedOperationRevision,
		Ticket: control.NetworkDataPlaneTrustApprovalTicket{
			OperationID: trustPrepareRequest.OperationID,
		},
	}
	trustConfirmRequest := control.ConfirmNetworkDataPlaneTrustApprovalRequest{
		OperationID:               trustPrepareResult.OperationID,
		ExpectedOperationRevision: trustPrepareResult.OperationRevision,
	}
	trustConfirmResult := control.NetworkDataPlaneSetupOperation{
		Operation: domain.Operation{
			ID: trustConfirmRequest.OperationID,
		},
		Revision: 11,
	}
	lowPortPrepareRequest := control.PrepareNetworkDataPlaneLowPortApprovalRequest{
		OperationID:               trustConfirmResult.Operation.ID,
		ExpectedOperationRevision: trustConfirmResult.Revision,
	}
	lowPortPrepareResult := control.NetworkDataPlaneLowPortApprovalPreparation{
		OperationID:       lowPortPrepareRequest.OperationID,
		OperationRevision: lowPortPrepareRequest.ExpectedOperationRevision,
		Ticket: control.NetworkDataPlaneLowPortApprovalTicket{
			OperationID: lowPortPrepareRequest.OperationID,
		},
	}
	lowPortConfirmRequest := control.ConfirmNetworkDataPlaneLowPortApprovalRequest{
		OperationID:               lowPortPrepareResult.OperationID,
		ExpectedOperationRevision: lowPortPrepareResult.OperationRevision,
	}
	lowPortConfirmResult := control.NetworkDataPlaneSetupConfirmation{
		Operation: domain.Operation{
			ID: lowPortConfirmRequest.OperationID,
		},
		NetworkRevision: 12,
		Revision:        13,
	}

	tests := []struct {
		name        string
		connection  *fakeDaemonControlClient
		call        func(*DaemonClient) (any, error)
		want        any
		calls       func(*fakeDaemonControlClient) int
		requests    func(*fakeDaemonControlClient) any
		wantRequest any
		contexts    func(*fakeDaemonControlClient) []context.Context
	}{
		{
			name: "start resolver",
			connection: &fakeDaemonControlClient{
				resolverSetup: resolverStartResult,
				closeErr:      closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.StartNetworkResolverSetup(nil, resolverStartRequest)
			},
			want: resolverStartResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.resolverSetupCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.resolverSetupRequests
			},
			wantRequest: []control.StartNetworkResolverSetupRequest{
				resolverStartRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.resolverSetupContexts
			},
		},
		{
			name: "prepare resolver",
			connection: &fakeDaemonControlClient{
				resolverPreparation: resolverPrepareResult,
				closeErr:            closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.PrepareNetworkResolverSetupApproval(nil, resolverPrepareRequest)
			},
			want: resolverPrepareResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.resolverPreparationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.resolverPreparationRequests
			},
			wantRequest: []control.PrepareNetworkResolverSetupApprovalRequest{
				resolverPrepareRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.resolverPreparationContexts
			},
		},
		{
			name: "confirm resolver",
			connection: &fakeDaemonControlClient{
				resolverConfirmation: resolverConfirmResult,
				closeErr:             closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.ConfirmNetworkResolverSetupApproval(nil, resolverConfirmRequest)
			},
			want: resolverConfirmResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.resolverConfirmationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.resolverConfirmationRequests
			},
			wantRequest: []control.ConfirmNetworkResolverSetupApprovalRequest{
				resolverConfirmRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.resolverConfirmationContexts
			},
		},
		{
			name: "start data plane",
			connection: &fakeDaemonControlClient{
				dataPlaneSetup: dataPlaneStartResult,
				closeErr:       closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.StartNetworkDataPlaneSetup(nil, dataPlaneStartRequest)
			},
			want: dataPlaneStartResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.dataPlaneSetupCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.dataPlaneSetupRequests
			},
			wantRequest: []control.StartNetworkDataPlaneSetupRequest{
				dataPlaneStartRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.dataPlaneSetupContexts
			},
		},
		{
			name: "prepare trust",
			connection: &fakeDaemonControlClient{
				dataPlaneTrustPreparation: trustPrepareResult,
				closeErr:                  closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.PrepareNetworkDataPlaneTrustApproval(nil, trustPrepareRequest)
			},
			want: trustPrepareResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.dataPlaneTrustPreparationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.dataPlaneTrustPreparationRequests
			},
			wantRequest: []control.PrepareNetworkDataPlaneTrustApprovalRequest{
				trustPrepareRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.dataPlaneTrustPreparationContexts
			},
		},
		{
			name: "confirm trust",
			connection: &fakeDaemonControlClient{
				dataPlaneTrustConfirmation: trustConfirmResult,
				closeErr:                   closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.ConfirmNetworkDataPlaneTrustApproval(nil, trustConfirmRequest)
			},
			want: trustConfirmResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.dataPlaneTrustConfirmationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.dataPlaneTrustConfirmationRequests
			},
			wantRequest: []control.ConfirmNetworkDataPlaneTrustApprovalRequest{
				trustConfirmRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.dataPlaneTrustConfirmationContexts
			},
		},
		{
			name: "prepare low ports",
			connection: &fakeDaemonControlClient{
				dataPlaneLowPortPreparation: lowPortPrepareResult,
				closeErr:                    closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.PrepareNetworkDataPlaneLowPortApproval(nil, lowPortPrepareRequest)
			},
			want: lowPortPrepareResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.dataPlaneLowPortPreparationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.dataPlaneLowPortPreparationRequests
			},
			wantRequest: []control.PrepareNetworkDataPlaneLowPortApprovalRequest{
				lowPortPrepareRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.dataPlaneLowPortPreparationContexts
			},
		},
		{
			name: "confirm low ports",
			connection: &fakeDaemonControlClient{
				dataPlaneLowPortConfirmation: lowPortConfirmResult,
				closeErr:                     closeErr,
			},
			call: func(client *DaemonClient) (any, error) {
				return client.ConfirmNetworkDataPlaneLowPortApproval(nil, lowPortConfirmRequest)
			},
			want: lowPortConfirmResult,
			calls: func(connection *fakeDaemonControlClient) int {
				return connection.dataPlaneLowPortConfirmationCalls
			},
			requests: func(connection *fakeDaemonControlClient) any {
				return connection.dataPlaneLowPortConfirmationRequests
			},
			wantRequest: []control.ConfirmNetworkDataPlaneLowPortApprovalRequest{
				lowPortConfirmRequest,
			},
			contexts: func(connection *fakeDaemonControlClient) []context.Context {
				return connection.dataPlaneLowPortConfirmationContexts
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
				return test.connection, nil
			})

			got, err := test.call(client)
			if !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want %v", err, closeErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
			if test.calls(test.connection) != 1 || test.connection.closeCalls != 1 {
				t.Fatalf(
					"request calls = %d, close calls = %d",
					test.calls(test.connection),
					test.connection.closeCalls,
				)
			}
			if gotRequests := test.requests(test.connection); !reflect.DeepEqual(gotRequests, test.wantRequest) {
				t.Fatalf("requests = %#v, want %#v", gotRequests, test.wantRequest)
			}
			contexts := test.contexts(test.connection)
			if len(contexts) != 1 || contexts[0] != nil {
				t.Fatalf("contexts = %#v, want one nil context", contexts)
			}
		})
	}
}

// TestDaemonClientPreservesRequestAndCloseFailures verifies cleanup never hides the actionable request cause.
func TestDaemonClientPreservesRequestAndCloseFailures(t *testing.T) {
	requestErr := errors.New("request failed")
	closeErr := errors.New("close failed")

	for _, test := range []struct {
		name           string
		call           func(*DaemonClient) error
		makeConnection func() *fakeDaemonControlClient
	}{
		{
			name: "status",
			call: func(client *DaemonClient) error {
				_, err := client.Status(t.Context())
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					statusErr: requestErr,
					closeErr:  closeErr,
				}
			},
		},
		{
			name: "stop",
			call: func(client *DaemonClient) error {
				return client.Stop(t.Context())
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					stopErr:  requestErr,
					closeErr: closeErr,
				}
			},
		},
		{
			name: "snapshot",
			call: func(client *DaemonClient) error {
				_, err := client.Snapshot(t.Context())
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					snapshotErr: requestErr,
					closeErr:    closeErr,
				}
			},
		},
		{
			name: "unregister project",
			call: func(client *DaemonClient) error {
				_, err := client.UnregisterProject(t.Context(), control.UnregisterProjectRequest{
					ProjectID: "project-orders",
					IntentID:  "intent-remove",
				})
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					unregistrationErr: requestErr,
					closeErr:          closeErr,
				}
			},
		},
		{
			name: "start network setup",
			call: func(client *DaemonClient) error {
				_, err := client.StartNetworkSetup(t.Context(), control.StartNetworkSetupRequest{IntentID: "intent-network-setup"})
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					networkSetupErr: requestErr,
					closeErr:        closeErr,
				}
			},
		},
		{
			name: "prepare network setup approval",
			call: func(client *DaemonClient) error {
				_, err := client.PrepareNetworkSetupApproval(t.Context(), control.PrepareNetworkSetupApprovalRequest{
					OperationID:               "operation-network-setup",
					ExpectedOperationRevision: 7,
				})
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					networkSetupPreparationErr: requestErr,
					closeErr:                   closeErr,
				}
			},
		},
		{
			name: "confirm network setup approval",
			call: func(client *DaemonClient) error {
				_, err := client.ConfirmNetworkSetupApproval(t.Context(), control.ConfirmNetworkSetupApprovalRequest{
					OperationID:               "operation-network-setup",
					ExpectedOperationRevision: 7,
				})
				return err
			},
			makeConnection: func() *fakeDaemonControlClient {
				return &fakeDaemonControlClient{
					networkSetupConfirmationErr: requestErr,
					closeErr:                    closeErr,
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := test.makeConnection()
			client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
				return connection, nil
			})

			err := test.call(client)
			if !errors.Is(err, requestErr) || !errors.Is(err, closeErr) {
				t.Fatalf("error = %v, want request and close causes", err)
			}
			if connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestDaemonClientReturnsConnectionFailureWithoutCleanup verifies a failed dial is not treated as an owned connection.
func TestDaemonClientReturnsConnectionFailureWithoutCleanup(t *testing.T) {
	connectErr := errors.New("connect failed")
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return nil, connectErr
	})

	_, err := client.Status(t.Context())
	if !errors.Is(err, connectErr) {
		t.Fatalf("error = %v, want %v", err, connectErr)
	}
}
