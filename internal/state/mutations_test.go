package state

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/inspects"
	"gorm.io/gorm"
)

// TestMutationCoordinatorSerializesWriters verifies separate state owners cannot overlap SQLite mutation transactions.
func TestMutationCoordinatorSerializesWriters(t *testing.T) {
	coordinator := newMutationCoordinatorTestHarness(t)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)

	go func() {
		firstResult <- coordinator.mutate(context.Background(), "first", func(*gorm.DB) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	go func() {
		secondResult <- coordinator.mutate(context.Background(), "second", func(*gorm.DB) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("second mutation entered while the first mutation held authority")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second mutation did not enter after authority was released")
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
}

// TestMutationCoordinatorCancellationDoesNotWaitForAuthority verifies a cancelled caller leaves the active writer undisturbed.
func TestMutationCoordinatorCancellationDoesNotWaitForAuthority(t *testing.T) {
	coordinator := newMutationCoordinatorTestHarness(t)
	<-coordinator.permit
	t.Cleanup(func() {
		coordinator.permit <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var entered atomic.Bool
	err := coordinator.mutate(ctx, "cancelled", func(*gorm.DB) error {
		entered.Store(true)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled mutation error = %v, want context.Canceled", err)
	}
	if entered.Load() {
		t.Fatal("cancelled mutation entered its transaction")
	}
}

// TestMutationCoordinatorReleasesAuthorityAfterFailure verifies one failed owner cannot strand every later mutation.
func TestMutationCoordinatorReleasesAuthorityAfterFailure(t *testing.T) {
	coordinator := newMutationCoordinatorTestHarness(t)
	failure := errors.New("mutation failed")
	if err := coordinator.mutate(nil, "failing", func(*gorm.DB) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("failed mutation error = %v, want %v", err, failure)
	}
	if err := coordinator.mutate(context.Background(), "recovery", func(*gorm.DB) error { return nil }); err != nil {
		t.Fatalf("recovery mutation: %v", err)
	}
}

// TestMutationCoordinatorReportsConnectionFailure verifies storage setup errors retain the mutation scope without invoking its callback.
func TestMutationCoordinatorReportsConnectionFailure(t *testing.T) {
	t.Setenv("DB_HARBORD_DRIVER", "unsupported")
	connections := database.NewConnections(inspects.NewManager())
	coordinator := NewMutationCoordinator(connections)
	var entered atomic.Bool
	err := coordinator.mutate(context.Background(), "project projection", func(*gorm.DB) error {
		entered.Store(true)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "open project projection state") || !strings.Contains(err.Error(), "unsupported driver") {
		t.Fatalf("connection error = %v, want scoped unsupported-driver failure", err)
	}
	if entered.Load() {
		t.Fatal("connection failure invoked the mutation callback")
	}
}

// newMutationCoordinatorTestHarness creates a coordinator over an isolated single-writer Harbor database.
func newMutationCoordinatorTestHarness(t *testing.T) *MutationCoordinator {
	t.Helper()
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", filepath.Join(t.TempDir(), "harbor.db")+"?_txlock=immediate")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close mutation database: %v", err)
		}
	})
	return NewMutationCoordinator(connections)
}
