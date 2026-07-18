package authority

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// controlState limits daemon authority to the complete durable reads needed by control clients.
type controlState interface {
	// CurrentSequence establishes the diagnostic revision without loading the larger replacement snapshot.
	CurrentSequence(context.Context) (domain.Sequence, error)
	// Snapshot supplies one transactionally consistent replacement for every client projection.
	Snapshot(context.Context) (domain.Snapshot, error)
	// RegisterProject creates or replays one inert project registration atomically.
	RegisterProject(context.Context, domain.ProjectSnapshot) (state.ProjectRegistration, error)
}

// projectDiscoverer isolates filesystem discovery from durable registration policy.
type projectDiscoverer interface {
	// Discover returns one canonical marker-validated checkout and its allowlisted presentation metadata.
	Discover(context.Context, string) (projectdiscovery.Discovery, error)
}

// Authority projects the daemon's durable state through the bounded control protocol.
type Authority struct {
	store        controlState
	build        buildinfo.Info
	discoverer   projectDiscoverer
	now          func() time.Time
	newProjectID func() (domain.ProjectID, error)
}

var _ control.Authority = (*Authority)(nil)

// NewAuthority creates the production control authority for the daemon's shared state store.
func NewAuthority(store *state.Store) *Authority {
	return newAuthority(store, buildinfo.Current())
}

// newAuthority keeps process build metadata deterministic without broadening production injection.
func newAuthority(store controlState, build buildinfo.Info) *Authority {
	return newAuthorityWithRegistration(store, build, projectdiscovery.NewDiscoverer(), time.Now, newOpaqueProjectID)
}

// newAuthorityWithRegistration keeps discovery and clock behavior deterministic in registration tests.
func newAuthorityWithRegistration(
	store controlState,
	build buildinfo.Info,
	discoverer projectDiscoverer,
	now func() time.Time,
	newProjectID func() (domain.ProjectID, error),
) *Authority {
	return &Authority{store: store, build: build, discoverer: discoverer, now: now, newProjectID: newProjectID}
}

// Status joins session negotiation with one durable sequence so diagnostics identify the exact authority serving the caller.
func (authority *Authority) Status(ctx context.Context, caller control.Caller) (control.DaemonStatus, error) {
	ctx = normalizeContext(ctx)
	capabilities, err := rpc.CanonicalCapabilities(caller.Session.Capabilities)
	if err != nil {
		return control.DaemonStatus{}, fmt.Errorf("canonicalize negotiated capabilities: %w", err)
	}
	sequence, err := authority.store.CurrentSequence(ctx)
	if err != nil {
		return control.DaemonStatus{}, err
	}

	return control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  authority.build.Version,
			Revision: authority.build.Revision,
			Modified: authority.build.Modified,
		},
		Protocol:              caller.Session.Protocol,
		Capabilities:          capabilities,
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              sequence,
	}, nil
}

// Snapshot delegates the complete durable replacement so the control layer cannot drift from the Store's transaction boundary.
func (authority *Authority) Snapshot(ctx context.Context, _ control.Caller) (domain.Snapshot, error) {
	return authority.store.Snapshot(normalizeContext(ctx))
}

// RegisterProject discovers one canonical checkout and commits its inert stopped projection.
func (authority *Authority) RegisterProject(
	ctx context.Context,
	_ control.Caller,
	request control.RegisterProjectRequest,
) (control.ProjectRegistration, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectRegistration{}, err
	}
	discovery, err := authority.discoverer.Discover(ctx, request.Path)
	if err != nil {
		var invalidProject *projectdiscovery.InvalidProjectError
		if errors.As(err, &invalidProject) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationInvalidError(err)
		}
		return control.ProjectRegistration{}, err
	}
	projectID, err := authority.newProjectID()
	if err != nil {
		return control.ProjectRegistration{}, fmt.Errorf("generate project identity: %w", err)
	}
	project, err := discovery.ProjectSnapshot(projectID, authority.now())
	if err != nil {
		return control.ProjectRegistration{}, err
	}
	registered, err := authority.store.RegisterProject(ctx, project)
	if err != nil {
		var conflict *state.ProjectRegistrationConflictError
		if errors.As(err, &conflict) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationConflictError(err)
		}
		var releaseActive *state.ProjectNetworkReleaseActiveError
		if errors.As(err, &releaseActive) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationConflictError(err)
		}
		return control.ProjectRegistration{}, err
	}
	result := control.ProjectRegistration{
		Project:  registered.Record.Project,
		Revision: registered.Record.Revision,
		Created:  registered.Created,
	}
	if err := result.Validate(); err != nil {
		return control.ProjectRegistration{}, fmt.Errorf("project registration result: %w", err)
	}
	return result, nil
}

// newOpaqueProjectID generates an identity that remains independent of checkout path, slug, and configuration.
func newOpaqueProjectID() (domain.ProjectID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	projectID := domain.ProjectID("project-" + hex.EncodeToString(random))
	if err := projectID.Validate(); err != nil {
		return "", err
	}
	return projectID, nil
}

// normalizeContext keeps nil control calls usable while preserving explicit cancellation.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
