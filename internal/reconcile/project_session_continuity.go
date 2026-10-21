package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectruntime"
)

// settleRecoveredProjectSession uses durable job authority when available instead of stopping only its leader.
func (coordinator *ProjectLifecycleCoordinator) settleRecoveredProjectSession(ctx context.Context, operation string, session domain.ProjectSession) (projectruntime.PriorProcessSettlement, error) {
	settler, supported := coordinator.projectRuntimeCapabilities().(projectruntime.SessionSettler)
	if !supported || session.OutputBroker == nil {
		return coordinator.settleRecoveredProjectProcess(ctx, operation, *session.Process)
	}
	project, err := coordinator.state.Project(ctx, session.ProjectID)
	if err != nil {
		return projectruntime.PriorProcessSettlement{}, err
	}
	result, err := settler.SettleSession(ctx, projectruntime.SessionAdoptionRequest{Session: session, CheckoutRoot: project.Project.Path})
	if err != nil {
		return projectruntime.PriorProcessSettlement{}, fmt.Errorf("settle prior session for %s: %w", operation, err)
	}
	switch result.Outcome {
	case projectruntime.PriorProcessSettlementAbsent, projectruntime.PriorProcessSettlementReplaced, projectruntime.PriorProcessSettlementTerminated:
		return result, nil
	default:
		return projectruntime.PriorProcessSettlement{}, fmt.Errorf("settle prior session for %s: unsupported outcome %q", operation, result.Outcome)
	}
}

// recoveredProjectSession delays process monitoring until routes and host integrations have resumed.
type recoveredProjectSession struct {
	session domain.ProjectSession
	handle  projectruntime.Handle
}

// adoptRecoveredSession requires the runtime's native ownership and broker proofs before retaining a live environment.
func (coordinator *ProjectLifecycleCoordinator) adoptRecoveredSession(ctx context.Context, project domain.ProjectSnapshot, session domain.ProjectSession) (bool, error) {
	adopter, supported := coordinator.projectRuntimeCapabilities().(projectruntime.SessionAdopter)
	if !supported || session.Owner != domain.SessionOwnerHarbor || session.OutputBroker == nil ||
		(session.State != domain.SessionAwaitingAttach && session.State != domain.SessionAttached) {
		return false, nil
	}
	if coordinator.handle(project.ID, session.ID) != nil {
		return true, nil
	}
	handle, err := adopter.AdoptSession(ctx, projectruntime.SessionAdoptionRequest{Session: session, CheckoutRoot: project.Path})
	if errors.Is(err, projectruntime.ErrSessionAdoptionUnsupported) {
		return false, nil
	}
	if err != nil {
		observation, observeErr := coordinator.runtime.ObservePriorProcess(ctx, *session.Process)
		if observeErr == nil && (observation.State == projectruntime.PriorProcessAbsent || observation.State == projectruntime.PriorProcessReplaced) {
			return false, nil
		}
		if session.State == domain.SessionAttached {
			return false, nil
		}
		return true, coordinator.quarantineProcessBackedTerminalProjectSession(ctx, project, session)
	}
	info := handle.Info()
	if info.ProjectID != session.ProjectID || info.SessionID != session.ID || info.CheckoutRoot != project.Path ||
		info.Evidence.PID != session.Process.PID || info.Evidence.BirthToken != session.Process.BirthToken ||
		info.Evidence.ExecutableIdentity != session.Process.ExecutableIdentity || info.Evidence.ArgumentDigest != session.Process.ArgumentDigest {
		return true, errors.New("recovered session handle differs from its durable ownership fence")
	}
	coordinator.retainHandle(project.ID, handle)
	coordinator.recoveredSessions = append(coordinator.recoveredSessions, recoveredProjectSession{session: session, handle: handle})
	return true, nil
}

// resumeRecoveredSession resumes observation without launching processes or rewriting the existing session generation.
func (coordinator *ProjectLifecycleCoordinator) resumeRecoveredSession(recovered recoveredProjectSession) {
	coordinator.mutex.Lock()
	if coordinator.closed {
		coordinator.mutex.Unlock()
		return
	}
	// Keep setup joined until both watchers are registered, even if shutdown begins between them.
	coordinator.wait.Add(1)
	coordinator.mutex.Unlock()
	defer coordinator.wait.Done()
	coordinator.wait.Add(1)
	go func() {
		defer coordinator.wait.Done()
		coordinator.watchReadyProcess(recovered.session, recovered.handle)
	}()
	project, err := coordinator.state.Project(coordinator.ctx, recovered.session.ProjectID)
	if err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	coordinator.startReadyServiceWatcher(project, recovered.session, recovered.handle, coordinator.recoveredRuntimePlan(project.Project))
}

// recoveredRuntimePlan restores observation facts from the admitted resource and lease without rerunning project preparation.
func (coordinator *ProjectLifecycleCoordinator) recoveredRuntimePlan(project domain.ProjectSnapshot) projectruntime.Plan {
	network, initialized, err := coordinator.primaryLeases.state.Network(coordinator.ctx)
	if err != nil || !initialized {
		return projectruntime.Plan{}
	}
	return runtimePlanForRecoveredResources(project, network.Leases)
}

// runtimePlanForRecoveredResources admits only the persisted primary resource on this project's exact reserved address.
func runtimePlanForRecoveredResources(project domain.ProjectSnapshot, leases []identity.Lease) projectruntime.Plan {
	lease, found := primaryLeaseForKey(leases, identity.LeaseKey{ProjectID: project.ID})
	if !found {
		return projectruntime.Plan{}
	}
	for _, resource := range project.Resources {
		if resource.ID != "app-http" || resource.Owner.Kind != domain.ResourceOwnedByApp {
			continue
		}
		endpoint, err := url.Parse(resource.URL)
		if err != nil {
			return projectruntime.Plan{}
		}
		address, addressErr := netip.ParseAddr(endpoint.Hostname())
		port, portErr := strconv.ParseUint(endpoint.Port(), 10, 16)
		if addressErr != nil || portErr != nil || port == 0 || address != lease.Address {
			return projectruntime.Plan{}
		}
		return projectruntime.Plan{
			NetworkAssignment: projectruntime.NetworkAssignment{Address: address, PrimaryPort: uint16(port)},
			Presentation:      projectruntime.Presentation{AppID: resource.Owner.AppID, Name: resource.Name, ResourceURL: resource.URL},
		}
	}
	return projectruntime.Plan{}
}

// detachReadySessions preserves only already-ready, durably identified processes supported by the runtime's native boundary.
func (coordinator *ProjectLifecycleCoordinator) detachReadySessions(ctx context.Context) {
	detacher, supported := coordinator.projectRuntimeCapabilities().(projectruntime.SessionDetacher)
	if !supported {
		return
	}
	snapshot, err := coordinator.state.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, project := range snapshot.Projects {
		if project.State != domain.ProjectReady && project.State != domain.ProjectDegraded {
			continue
		}
		session, err := coordinator.state.ActiveProjectSession(ctx, project.ID)
		if err != nil || session.Owner != domain.SessionOwnerHarbor || session.Process == nil || session.OutputBroker == nil ||
			(session.State != domain.SessionAwaitingAttach && session.State != domain.SessionAttached) {
			continue
		}
		if err := detacher.DetachSession(ctx, project.ID, session.ID); err != nil {
			continue
		}
		coordinator.mutex.Lock()
		coordinator.preservedSessions[session.ID] = true
		coordinator.mutex.Unlock()
	}
}
