package cmd

import (
	"context"
	"errors"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

// daemonControlClient is the narrow control connection used for one CLI request.
type daemonControlClient interface {
	Status(context.Context) (control.DaemonStatus, error)
	Stop(context.Context) error
	Snapshot(context.Context) (domain.Snapshot, error)
	ProjectActivity(context.Context, control.ProjectActivityRequest) (control.ProjectActivity, error)
	ServiceLogs(context.Context, control.ServiceLogsRequest) (control.ServiceLogs, error)
	RegisterProject(context.Context, control.RegisterProjectRequest) (control.ProjectRegistration, error)
	UnregisterProject(context.Context, control.UnregisterProjectRequest) (control.ProjectUnregistration, error)
	StartProject(context.Context, control.StartProjectRequest) (control.ProjectLifecycleOperation, error)
	StopProject(context.Context, control.StopProjectRequest) (control.ProjectLifecycleOperation, error)
	RestartProject(context.Context, control.RestartProjectRequest) (control.ProjectLifecycleOperation, error)
	StartNetworkSetup(context.Context, control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	PrepareNetworkSetupApproval(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	ConfirmNetworkSetupApproval(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
	StartNetworkResolverSetup(context.Context, control.StartNetworkResolverSetupRequest) (control.NetworkResolverSetupOperation, error)
	PrepareNetworkResolverSetupApproval(context.Context, control.PrepareNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalPreparation, error)
	ConfirmNetworkResolverSetupApproval(context.Context, control.ConfirmNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalConfirmation, error)
	StartNetworkDataPlaneSetup(context.Context, control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error)
	PrepareNetworkDataPlaneTrustApproval(context.Context, control.PrepareNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneTrustApprovalPreparation, error)
	ConfirmNetworkDataPlaneTrustApproval(context.Context, control.ConfirmNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneSetupOperation, error)
	PrepareNetworkDataPlaneLowPortApproval(context.Context, control.PrepareNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneLowPortApprovalPreparation, error)
	ConfirmNetworkDataPlaneLowPortApproval(context.Context, control.ConfirmNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneSetupConfirmation, error)
	StartNetworkRelease(context.Context, control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error)
	ReadNetworkRelease(context.Context, control.ReadNetworkReleaseRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseApproval(context.Context, control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error)
	ConfirmNetworkReleaseApproval(context.Context, control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseResolverApproval(context.Context, control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error)
	ConfirmNetworkReleaseResolverApproval(context.Context, control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseTrustApproval(context.Context, control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error)
	ConfirmNetworkReleaseTrustApproval(context.Context, control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error)
	PrepareNetworkReleaseLoopbackApproval(context.Context, control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error)
	ConfirmNetworkReleaseLoopbackApproval(context.Context, control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error)
	ConfirmNetworkReleaseOwnership(context.Context, control.ConfirmNetworkReleaseOwnershipRequest) (control.NetworkReleaseOperation, error)
	Close() error
}

// StartNetworkRelease starts or replays one machine-global network release intent.
func (client *DaemonClient) StartNetworkRelease(ctx context.Context, request control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.StartNetworkRelease(ctx, request)
	})
}

// ReadNetworkRelease reads the exact durable network release operation.
func (client *DaemonClient) ReadNetworkRelease(ctx context.Context, request control.ReadNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ReadNetworkRelease(ctx, request)
	})
}

// PrepareNetworkReleaseApproval prepares one low-port release checkpoint.
func (client *DaemonClient) PrepareNetworkReleaseApproval(ctx context.Context, request control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseApprovalPreparation, error) {
		return connection.PrepareNetworkReleaseApproval(ctx, request)
	})
}

// ConfirmNetworkReleaseApproval confirms low-port release evidence.
func (client *DaemonClient) ConfirmNetworkReleaseApproval(ctx context.Context, request control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ConfirmNetworkReleaseApproval(ctx, request)
	})
}

// PrepareNetworkReleaseResolverApproval prepares one resolver release checkpoint.
func (client *DaemonClient) PrepareNetworkReleaseResolverApproval(ctx context.Context, request control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseResolverApprovalPreparation, error) {
		return connection.PrepareNetworkReleaseResolverApproval(ctx, request)
	})
}

// ConfirmNetworkReleaseResolverApproval confirms resolver release evidence.
func (client *DaemonClient) ConfirmNetworkReleaseResolverApproval(ctx context.Context, request control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ConfirmNetworkReleaseResolverApproval(ctx, request)
	})
}

// PrepareNetworkReleaseTrustApproval prepares one trust release checkpoint.
func (client *DaemonClient) PrepareNetworkReleaseTrustApproval(ctx context.Context, request control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseTrustApprovalPreparation, error) {
		return connection.PrepareNetworkReleaseTrustApproval(ctx, request)
	})
}

// ConfirmNetworkReleaseTrustApproval confirms trust release evidence or safe preservation.
func (client *DaemonClient) ConfirmNetworkReleaseTrustApproval(ctx context.Context, request control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ConfirmNetworkReleaseTrustApproval(ctx, request)
	})
}

// PrepareNetworkReleaseLoopbackApproval prepares one loopback release checkpoint.
func (client *DaemonClient) PrepareNetworkReleaseLoopbackApproval(ctx context.Context, request control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseLoopbackApprovalPreparation, error) {
		return connection.PrepareNetworkReleaseLoopbackApproval(ctx, request)
	})
}

// ConfirmNetworkReleaseLoopbackApproval confirms loopback release evidence.
func (client *DaemonClient) ConfirmNetworkReleaseLoopbackApproval(ctx context.Context, request control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ConfirmNetworkReleaseLoopbackApproval(ctx, request)
	})
}

// ConfirmNetworkReleaseOwnership confirms the authenticated owner's released machine ownership checkpoint.
func (client *DaemonClient) ConfirmNetworkReleaseOwnership(ctx context.Context, request control.ConfirmNetworkReleaseOwnershipRequest) (control.NetworkReleaseOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkReleaseOperation, error) {
		return connection.ConfirmNetworkReleaseOwnership(ctx, request)
	})
}

// StartNetworkResolverSetup starts or replays one resolver setup intent.
func (client *DaemonClient) StartNetworkResolverSetup(
	ctx context.Context,
	request control.StartNetworkResolverSetupRequest,
) (control.NetworkResolverSetupOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkResolverSetupOperation, error) {
		return connection.StartNetworkResolverSetup(ctx, request)
	})
}

// PrepareNetworkResolverSetupApproval prepares one exact resolver approval revision.
func (client *DaemonClient) PrepareNetworkResolverSetupApproval(
	ctx context.Context,
	request control.PrepareNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkResolverSetupApprovalPreparation, error) {
		return connection.PrepareNetworkResolverSetupApproval(ctx, request)
	})
}

// ConfirmNetworkResolverSetupApproval confirms one resolver approval result.
func (client *DaemonClient) ConfirmNetworkResolverSetupApproval(
	ctx context.Context,
	request control.ConfirmNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalConfirmation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkResolverSetupApprovalConfirmation, error) {
		return connection.ConfirmNetworkResolverSetupApproval(ctx, request)
	})
}

// StartNetworkDataPlaneSetup starts or replays one trusted-ingress setup intent.
func (client *DaemonClient) StartNetworkDataPlaneSetup(
	ctx context.Context,
	request control.StartNetworkDataPlaneSetupRequest,
) (control.NetworkDataPlaneSetupOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkDataPlaneSetupOperation, error) {
		return connection.StartNetworkDataPlaneSetup(ctx, request)
	})
}

// PrepareNetworkDataPlaneTrustApproval prepares one exact trust approval revision.
func (client *DaemonClient) PrepareNetworkDataPlaneTrustApproval(
	ctx context.Context,
	request control.PrepareNetworkDataPlaneTrustApprovalRequest,
) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
		return connection.PrepareNetworkDataPlaneTrustApproval(ctx, request)
	})
}

// ConfirmNetworkDataPlaneTrustApproval confirms one trust approval result.
func (client *DaemonClient) ConfirmNetworkDataPlaneTrustApproval(
	ctx context.Context,
	request control.ConfirmNetworkDataPlaneTrustApprovalRequest,
) (control.NetworkDataPlaneSetupOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkDataPlaneSetupOperation, error) {
		return connection.ConfirmNetworkDataPlaneTrustApproval(ctx, request)
	})
}

// PrepareNetworkDataPlaneLowPortApproval prepares one exact low-port approval revision.
func (client *DaemonClient) PrepareNetworkDataPlaneLowPortApproval(
	ctx context.Context,
	request control.PrepareNetworkDataPlaneLowPortApprovalRequest,
) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
		return connection.PrepareNetworkDataPlaneLowPortApproval(ctx, request)
	})
}

// ConfirmNetworkDataPlaneLowPortApproval confirms one low-port approval result.
func (client *DaemonClient) ConfirmNetworkDataPlaneLowPortApproval(
	ctx context.Context,
	request control.ConfirmNetworkDataPlaneLowPortApprovalRequest,
) (control.NetworkDataPlaneSetupConfirmation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkDataPlaneSetupConfirmation, error) {
		return connection.ConfirmNetworkDataPlaneLowPortApproval(ctx, request)
	})
}

// daemonConnect opens one authenticated control connection when a command runs.
type daemonConnect func(context.Context) (daemonControlClient, error)

// DaemonClient gives CLI commands one-shot access to the local Harbor daemon.
type DaemonClient struct {
	connect daemonConnect
}

// NewDaemonClient creates a lazy CLI client without opening a daemon connection.
func NewDaemonClient() *DaemonClient {
	return newDaemonClient(func(ctx context.Context) (daemonControlClient, error) {
		return control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleCLI})
	})
}

// newDaemonClient keeps connection timing and cleanup observable in command tests.
func newDaemonClient(connect daemonConnect) *DaemonClient {
	return &DaemonClient{connect: connect}
}

// Status reads one daemon status and closes the one-shot control connection.
func (client *DaemonClient) Status(ctx context.Context) (control.DaemonStatus, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.DaemonStatus, error) {
		return connection.Status(ctx)
	})
}

// Stop requests daemon shutdown and closes the one-shot control connection after acknowledgement.
func (client *DaemonClient) Stop(ctx context.Context) error {
	_, err := withDaemonConnection(ctx, client, func(connection daemonControlClient) (struct{}, error) {
		return struct{}{}, connection.Stop(ctx)
	})

	return err
}

// Snapshot reads one authoritative snapshot and closes the one-shot control connection.
func (client *DaemonClient) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (domain.Snapshot, error) {
		return connection.Snapshot(ctx)
	})
}

// ProjectActivity reads one bounded current-session output chunk and closes its one-shot control connection.
func (client *DaemonClient) ProjectActivity(
	ctx context.Context,
	request control.ProjectActivityRequest,
) (control.ProjectActivity, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectActivity, error) {
		return connection.ProjectActivity(ctx, request)
	})
}

// ServiceLogs reads one bounded current-session service chunk and closes its one-shot control connection.
func (client *DaemonClient) ServiceLogs(
	ctx context.Context,
	request control.ServiceLogsRequest,
) (control.ServiceLogs, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ServiceLogs, error) {
		return connection.ServiceLogs(ctx, request)
	})
}

// RegisterProject creates or replays one project through the daemon and closes the one-shot connection.
func (client *DaemonClient) RegisterProject(
	ctx context.Context,
	request control.RegisterProjectRequest,
) (control.ProjectRegistration, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectRegistration, error) {
		return connection.RegisterProject(ctx, request)
	})
}

// UnregisterProject starts or resumes one project removal through the daemon and closes the one-shot connection.
func (client *DaemonClient) UnregisterProject(
	ctx context.Context,
	request control.UnregisterProjectRequest,
) (control.ProjectUnregistration, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectUnregistration, error) {
		return connection.UnregisterProject(ctx, request)
	})
}

// StartProject starts or resumes one project lifecycle through the daemon and closes the one-shot connection.
func (client *DaemonClient) StartProject(
	ctx context.Context,
	request control.StartProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectLifecycleOperation, error) {
		return connection.StartProject(ctx, request)
	})
}

// StopProject stops or resumes one project lifecycle through the daemon and closes the one-shot connection.
func (client *DaemonClient) StopProject(
	ctx context.Context,
	request control.StopProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectLifecycleOperation, error) {
		return connection.StopProject(ctx, request)
	})
}

// RestartProject stops and replaces one project lifecycle through the daemon and closes the one-shot connection.
func (client *DaemonClient) RestartProject(
	ctx context.Context,
	request control.RestartProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.ProjectLifecycleOperation, error) {
		return connection.RestartProject(ctx, request)
	})
}

// StartNetworkSetup starts or replays one machine-global setup intent and closes the one-shot connection.
func (client *DaemonClient) StartNetworkSetup(
	ctx context.Context,
	request control.StartNetworkSetupRequest,
) (control.NetworkSetupOperation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkSetupOperation, error) {
		return connection.StartNetworkSetup(ctx, request)
	})
}

// PrepareNetworkSetupApproval requests helper authorization for one setup revision and closes the one-shot connection.
func (client *DaemonClient) PrepareNetworkSetupApproval(
	ctx context.Context,
	request control.PrepareNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalPreparation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkSetupApprovalPreparation, error) {
		return connection.PrepareNetworkSetupApproval(ctx, request)
	})
}

// ConfirmNetworkSetupApproval submits setup evidence for one approved revision and closes the one-shot connection.
func (client *DaemonClient) ConfirmNetworkSetupApproval(
	ctx context.Context,
	request control.ConfirmNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalConfirmation, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (control.NetworkSetupApprovalConfirmation, error) {
		return connection.ConfirmNetworkSetupApproval(ctx, request)
	})
}

// withDaemonConnection retains both request and cleanup failures for operator diagnosis.
func withDaemonConnection[T any](ctx context.Context, client *DaemonClient, call func(daemonControlClient) (T, error)) (result T, err error) {
	connection, err := client.connect(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, connection.Close())
	}()

	return call(connection)
}
