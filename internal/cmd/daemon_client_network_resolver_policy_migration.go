package cmd

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/control"
)

// networkResolverPolicyMigrationDaemonClient is the optional migration transport exposed by newer daemon control clients.
type networkResolverPolicyMigrationDaemonClient interface {
	StartNetworkResolverPolicyMigration(context.Context, control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error)
	PrepareNetworkResolverPolicyMigrationApproval(context.Context, control.PrepareNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalPreparation, error)
	ConfirmNetworkResolverPolicyMigrationApproval(context.Context, control.ConfirmNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error)
}

// StartNetworkResolverPolicyMigration starts or replays one legacy resolver-policy retirement intent.
func (client *DaemonClient) StartNetworkResolverPolicyMigration(
	ctx context.Context,
	request control.StartNetworkResolverPolicyMigrationRequest,
) (control.NetworkResolverPolicyMigrationOperation, error) {
	return withNetworkResolverPolicyMigrationDaemonConnection(ctx, client, func(connection networkResolverPolicyMigrationDaemonClient) (control.NetworkResolverPolicyMigrationOperation, error) {
		return connection.StartNetworkResolverPolicyMigration(ctx, request)
	})
}

// PrepareNetworkResolverPolicyMigrationApproval prepares one exact legacy resolver-policy retirement approval revision.
func (client *DaemonClient) PrepareNetworkResolverPolicyMigrationApproval(
	ctx context.Context,
	request control.PrepareNetworkResolverPolicyMigrationApprovalRequest,
) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
	return withNetworkResolverPolicyMigrationDaemonConnection(ctx, client, func(connection networkResolverPolicyMigrationDaemonClient) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
		return connection.PrepareNetworkResolverPolicyMigrationApproval(ctx, request)
	})
}

// ConfirmNetworkResolverPolicyMigrationApproval confirms owned-absence evidence for one exact migration approval revision.
func (client *DaemonClient) ConfirmNetworkResolverPolicyMigrationApproval(
	ctx context.Context,
	request control.ConfirmNetworkResolverPolicyMigrationApprovalRequest,
) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	return withNetworkResolverPolicyMigrationDaemonConnection(ctx, client, func(connection networkResolverPolicyMigrationDaemonClient) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
		return connection.ConfirmNetworkResolverPolicyMigrationApproval(ctx, request)
	})
}

// withNetworkResolverPolicyMigrationDaemonConnection narrows the existing connection only when the negotiated client supports migration.
func withNetworkResolverPolicyMigrationDaemonConnection[T any](
	ctx context.Context,
	client *DaemonClient,
	call func(networkResolverPolicyMigrationDaemonClient) (T, error),
) (T, error) {
	return withDaemonConnection(ctx, client, func(connection daemonControlClient) (T, error) {
		migrationClient, ok := connection.(networkResolverPolicyMigrationDaemonClient)
		if !ok {
			var zero T
			return zero, fmt.Errorf("daemon control client does not support network resolver policy migration")
		}
		return call(migrationClient)
	})
}
