package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

// MutationCoordinator serializes every durable Harbor mutation through the daemon's single SQLite writer.
type MutationCoordinator struct {
	connections *database.Connections
	permit      chan struct{}
}

// NewMutationCoordinator creates the shared writer coordinator for the named harbord database.
func NewMutationCoordinator(connections *database.Connections) *MutationCoordinator {
	permit := make(chan struct{}, 1)
	permit <- struct{}{}
	return &MutationCoordinator{connections: connections, permit: permit}
}

// mutate waits without defeating cancellation, then executes one immediate database transaction.
func (coordinator *MutationCoordinator) mutate(ctx context.Context, scope string, mutation func(*gorm.DB) error) error {
	ctx = normalizeContext(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-coordinator.permit:
	}
	defer func() {
		coordinator.permit <- struct{}{}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	connection, err := coordinator.connections.GetHarbord()
	if err != nil {
		return fmt.Errorf("open %s state: %w", scope, err)
	}
	return connection.WithContext(ctx).Transaction(mutation)
}
