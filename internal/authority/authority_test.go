package authority

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingStore provides immutable durable results with race-safe call accounting.
type recordingStore struct {
	sequence             domain.Sequence
	snapshot             domain.Snapshot
	sequenceErr          error
	snapshotErr          error
	sequenceCalls        atomic.Int64
	snapshotCalls        atomic.Int64
	nilContexts          atomic.Int64
	registration         state.ProjectRegistration
	registrationErr      error
	registrationCalls    atomic.Int64
	registrationMu       sync.Mutex
	registrationProjects []domain.ProjectSnapshot
}

// CurrentSequence returns the configured global sequence while preserving caller cancellation.
func (store *recordingStore) CurrentSequence(ctx context.Context) (domain.Sequence, error) {
	store.sequenceCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return 0, errors.New("current sequence received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if store.sequenceErr != nil {
		return 0, store.sequenceErr
	}

	return store.sequence, nil
}

// Snapshot returns the configured replacement state while preserving caller cancellation.
func (store *recordingStore) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	store.snapshotCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return domain.Snapshot{}, errors.New("snapshot received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return domain.Snapshot{}, err
	}
	if store.snapshotErr != nil {
		return domain.Snapshot{}, store.snapshotErr
	}

	return store.snapshot, nil
}

// RegisterProject returns the configured atomic registration while preserving caller cancellation.
func (store *recordingStore) RegisterProject(ctx context.Context, project domain.ProjectSnapshot) (state.ProjectRegistration, error) {
	store.registrationCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return state.ProjectRegistration{}, errors.New("registration received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return state.ProjectRegistration{}, err
	}
	if store.registrationErr != nil {
		return state.ProjectRegistration{}, store.registrationErr
	}
	store.registrationMu.Lock()
	store.registrationProjects = append(store.registrationProjects, project)
	store.registrationMu.Unlock()
	return store.registration, nil
}

// emptySnapshot returns a valid complete replacement with canonical initialized collections.
func emptySnapshot(sequence domain.Sequence) domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          sequence,
		CapturedAt:        time.Date(2026, time.July, 18, 12, 30, 0, 0, time.UTC),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// controlCaller returns a negotiated control peer suitable for direct authority tests.
func controlCaller(capabilities []rpc.Capability) control.Caller {
	return control.Caller{Session: session.Peer{
		Role:         rpc.RoleCLI,
		BuildVersion: "v2.4.0",
		Protocol:     rpc.Version{Major: 1, Minor: 0},
		Capabilities: capabilities,
	}}
}

// TestNewAuthorityUsesCurrentBuild verifies production construction captures build identity with its complete required Store graph.
func TestNewAuthorityUsesCurrentBuild(t *testing.T) {
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close database connections: %v", err)
		}
	})
	store := state.NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		state.NewMutationCoordinator(connections),
	)
	want := buildinfo.Current()

	authority := NewAuthority(store, new(reconcile.ProjectUnregisterCoordinator))
	if authority == nil {
		t.Fatal("NewAuthority() returned nil")
	}
	if authority.build != want {
		t.Fatalf("NewAuthority() build = %#v, want %#v", authority.build, want)
	}
}

// TestAuthorityStatusMapsServingState verifies status comes only from the serving build, caller negotiation, and durable sequence.
func TestAuthorityStatusMapsServingState(t *testing.T) {
	store := &recordingStore{sequence: 42}
	build := buildinfo.Info{Version: "v3.2.1", Revision: "abc123", Modified: true}
	authority := newAuthority(store, testProjectUnregisterApprovals(), build)
	caller := controlCaller([]rpc.Capability{control.CapabilityV1, "events.v1"})

	status, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  build.Version,
			Revision: build.Revision,
			Modified: build.Modified,
		},
		Protocol:              caller.Session.Protocol,
		Capabilities:          []rpc.Capability{control.CapabilityV1, "events.v1"},
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              store.sequence,
	}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("Status() = %#v, want %#v", status, want)
	}
	if got := store.sequenceCalls.Load(); got != 1 {
		t.Fatalf("CurrentSequence() calls = %d, want 1", got)
	}
	if got := store.snapshotCalls.Load(); got != 0 {
		t.Fatalf("Snapshot() calls = %d, want 0", got)
	}
}

// TestAuthorityStatusReturnsFreshCanonicalCapabilities verifies caller-owned slices cannot alter status results across calls.
func TestAuthorityStatusReturnsFreshCanonicalCapabilities(t *testing.T) {
	store := &recordingStore{sequence: 9}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
	capabilities := []rpc.Capability{"events.v1", control.CapabilityV1, "events.v1"}
	caller := controlCaller(capabilities)

	first, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("first Status() error = %v", err)
	}
	want := []rpc.Capability{control.CapabilityV1, "events.v1"}
	if !reflect.DeepEqual(first.Capabilities, want) {
		t.Fatalf("first capabilities = %v, want %v", first.Capabilities, want)
	}
	capabilities[0] = "changed.v1"
	if !reflect.DeepEqual(first.Capabilities, want) {
		t.Fatalf("caller mutation changed first capabilities to %v", first.Capabilities)
	}

	first.Capabilities[0] = "response.changed.v1"
	caller.Session.Capabilities = []rpc.Capability{control.CapabilityV1, "events.v1"}
	second, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("second Status() error = %v", err)
	}
	if !reflect.DeepEqual(second.Capabilities, want) {
		t.Fatalf("second capabilities = %v, want fresh %v", second.Capabilities, want)
	}
}

// TestAuthorityNormalizesNilContexts verifies both public reads give the Store a usable context.
func TestAuthorityNormalizesNilContexts(t *testing.T) {
	snapshot := emptySnapshot(7)
	store := &recordingStore{sequence: snapshot.Sequence, snapshot: snapshot}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
	caller := controlCaller([]rpc.Capability{control.CapabilityV1})

	if _, err := authority.Status(nil, caller); err != nil {
		t.Fatalf("Status(nil) error = %v", err)
	}
	if _, err := authority.Snapshot(nil, caller); err != nil {
		t.Fatalf("Snapshot(nil) error = %v", err)
	}
	if got := store.nilContexts.Load(); got != 0 {
		t.Fatalf("Store nil contexts = %d, want 0", got)
	}
}

// TestAuthorityPreservesStoreErrorsAndCancellation verifies control transport classification can inspect original causes.
func TestAuthorityPreservesStoreErrorsAndCancellation(t *testing.T) {
	statusFailure := errors.New("sequence unavailable")
	snapshotFailure := errors.New("snapshot unavailable")
	caller := controlCaller([]rpc.Capability{control.CapabilityV1})

	statusAuthority := newAuthority(&recordingStore{sequenceErr: statusFailure}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
	if _, err := statusAuthority.Status(context.Background(), caller); !errors.Is(err, statusFailure) {
		t.Fatalf("Status() error = %v, want %v", err, statusFailure)
	}

	snapshotAuthority := newAuthority(&recordingStore{snapshotErr: snapshotFailure}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
	if _, err := snapshotAuthority.Snapshot(context.Background(), caller); !errors.Is(err, snapshotFailure) {
		t.Fatalf("Snapshot() error = %v, want %v", err, snapshotFailure)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "cancelled", ctx: cancelled, want: context.Canceled},
		{name: "deadline", ctx: expired, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newAuthority(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
			if _, err := authority.Status(test.ctx, caller); !errors.Is(err, test.want) {
				t.Fatalf("Status() error = %v, want %v", err, test.want)
			}
			if _, err := authority.Snapshot(test.ctx, caller); !errors.Is(err, test.want) {
				t.Fatalf("Snapshot() error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestAuthorityRejectsInvalidNegotiatedCapabilities verifies malformed direct calls do not emit invalid status data.
func TestAuthorityRejectsInvalidNegotiatedCapabilities(t *testing.T) {
	store := &recordingStore{sequence: 5}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})
	caller := controlCaller([]rpc.Capability{"bad capability"})

	if _, err := authority.Status(context.Background(), caller); err == nil {
		t.Fatal("Status() error = nil, want invalid capability error")
	}
	if got := store.sequenceCalls.Load(); got != 0 {
		t.Fatalf("CurrentSequence() calls = %d, want 0 after invalid negotiation", got)
	}
}

// TestAuthoritySnapshotPassesThroughStoreState verifies authority neither invents nor filters durable project state.
func TestAuthoritySnapshotPassesThroughStoreState(t *testing.T) {
	snapshot := emptySnapshot(17)
	store := &recordingStore{snapshot: snapshot}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"})

	got, err := authority.Snapshot(context.Background(), control.Caller{})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("Snapshot() = %#v, want exact Store snapshot %#v", got, snapshot)
	}
	if calls := store.snapshotCalls.Load(); calls != 1 {
		t.Fatalf("Snapshot() calls = %d, want 1", calls)
	}
}

// TestAuthoritySupportsConcurrentReads verifies status and snapshot calls share no mutable response state.
func TestAuthoritySupportsConcurrentReads(t *testing.T) {
	const readers = 64
	snapshot := emptySnapshot(71)
	store := &recordingStore{sequence: snapshot.Sequence, snapshot: snapshot}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "v1.0.0", Revision: "race-safe"})
	caller := controlCaller([]rpc.Capability{control.CapabilityV1, "events.v1"})
	errorsFound := make(chan error, readers*2)
	start := make(chan struct{})
	var wait sync.WaitGroup

	for range readers {
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			status, err := authority.Status(context.Background(), caller)
			if err == nil && status.Sequence != snapshot.Sequence {
				err = errors.New("Status() returned the wrong sequence")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			got, err := authority.Snapshot(context.Background(), caller)
			if err == nil && !reflect.DeepEqual(got, snapshot) {
				err = errors.New("Snapshot() returned different state")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
	}

	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent authority read: %v", err)
	}
	if got := store.sequenceCalls.Load(); got != readers {
		t.Fatalf("CurrentSequence() calls = %d, want %d", got, readers)
	}
	if got := store.snapshotCalls.Load(); got != readers {
		t.Fatalf("Snapshot() calls = %d, want %d", got, readers)
	}
}
