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
	RegisterProject(context.Context, control.RegisterProjectRequest) (control.ProjectRegistration, error)
	UnregisterProject(context.Context, control.UnregisterProjectRequest) (control.ProjectUnregistration, error)
	StartProject(context.Context, control.StartProjectRequest) (control.ProjectLifecycleOperation, error)
	StopProject(context.Context, control.StopProjectRequest) (control.ProjectLifecycleOperation, error)
	StartNetworkSetup(context.Context, control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	PrepareNetworkSetupApproval(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	ConfirmNetworkSetupApproval(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
	Close() error
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
