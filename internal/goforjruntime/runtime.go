// Package goforjruntime adapts ordinary GoForj development commands to Harbor's project runtime boundary.
package goforjruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectreadiness"
	"github.com/goforj/harbor/internal/projectruntime"
)

// Runtime adapts a GoForj process supervisor to Harbor's neutral project runtime contract.
type Runtime struct {
	supervisor      *projectprocess.Supervisor
	runtimeRepairer projectprocess.UnattributedRuntimeRepairer
}

// New creates a GoForj runtime adapter around the process supervisor.
func New(supervisor *projectprocess.Supervisor) *Runtime {
	if supervisor == nil {
		panic("goforjruntime.New requires a supervisor")
	}
	return &Runtime{
		supervisor:      supervisor,
		runtimeRepairer: projectprocess.NewUnattributedRuntimeRepairer(),
	}
}

const (
	// listenerRepairAttempts bounds transient native process races before a listener remains unresolved.
	listenerRepairAttempts = 3
	// listenerRepairRetryDelay gives a terminating listener a short window to publish its new state.
	listenerRepairRetryDelay = 50 * time.Millisecond
)

// RepairListener settles one exact GoForj listener only after native inspection proves checkout-scoped authority.
func (runtime *Runtime) RepairListener(
	ctx context.Context,
	request projectruntime.ListenerRepairRequest,
) (projectruntime.ListenerRepairResult, error) {
	if runtime == nil || runtime.runtimeRepairer == nil {
		return projectruntime.ListenerRepairResult{}, errors.New("GoForj listener repair is unavailable")
	}
	target := projectprocess.RuntimeRepairTarget{CheckoutRoot: request.CheckoutRoot, Endpoint: request.Endpoint}
	var lastErr error
	for attempt := 0; attempt < listenerRepairAttempts; attempt++ {
		inspection, err := runtime.runtimeRepairer.Inspect(ctx, target)
		if err != nil {
			lastErr = err
			if !waitForListenerRepairRetry(ctx, attempt) {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return projectruntime.ListenerRepairResult{}, ctxErr
				}
				return projectruntime.ListenerRepairResult{}, lastErr
			}
			continue
		}
		lastErr = nil
		if err := inspection.Validate(); err != nil {
			return projectruntime.ListenerRepairResult{}, fmt.Errorf("validate automatic listener inspection: %w", err)
		}
		switch inspection.State {
		case projectprocess.RuntimeRepairInspectionMissing:
			if request.RequireConfirmation {
				return projectruntime.ListenerRepairResult{}, nil
			}
			return projectruntime.ListenerRepairResult{Settled: true}, nil
		case projectprocess.RuntimeRepairInspectionActionable:
			confirmation, err := runtime.runtimeRepairer.Confirm(ctx, *inspection.Candidate)
			if err != nil {
				lastErr = err
				if !waitForListenerRepairRetry(ctx, attempt) {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return projectruntime.ListenerRepairResult{}, ctxErr
					}
					return projectruntime.ListenerRepairResult{}, lastErr
				}
				continue
			}
			if err := confirmation.Validate(); err != nil {
				return projectruntime.ListenerRepairResult{}, fmt.Errorf("validate automatic listener cleanup: %w", err)
			}
			if confirmation.State == projectprocess.RuntimeRepairConfirmationSettled {
				return projectruntime.ListenerRepairResult{Settled: true}, nil
			}
			if confirmation.State == projectprocess.RuntimeRepairConfirmationFailed {
				lastErr = errors.New("automatic listener cleanup returned a failed confirmation")
			}
			if !waitForListenerRepairRetry(ctx, attempt) {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return projectruntime.ListenerRepairResult{}, ctxErr
				}
				return projectruntime.ListenerRepairResult{}, lastErr
			}
		default:
			return projectruntime.ListenerRepairResult{}, nil
		}
	}
	return projectruntime.ListenerRepairResult{}, lastErr
}

// waitForListenerRepairRetry bounds the pause between native observations while a process exits.
func waitForListenerRepairRetry(ctx context.Context, attempt int) bool {
	if attempt+1 >= listenerRepairAttempts {
		return false
	}
	timer := time.NewTimer(listenerRepairRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Prepare discovers GoForj's generated primary App contract at Harbor's assigned address.
func (runtime *Runtime) Prepare(ctx context.Context, request projectruntime.PreparationRequest) (projectruntime.Plan, error) {
	if runtime == nil || runtime.supervisor == nil {
		panic("goforjruntime.Runtime.Prepare requires a constructed runtime")
	}
	target, err := projectdiscovery.NewDiscoverer().DiscoverDefaultRuntimeAtAddress(ctx, request.CheckoutRoot, request.Address)
	if err != nil {
		return projectruntime.Plan{}, goForjPreparationError(err)
	}
	return projectruntime.Plan{
		NetworkAssignment: projectruntime.NetworkAssignment{Address: target.Address, PrimaryPort: target.Port},
		Readiness:         goForjReadinessProbe{prober: projectreadiness.NewProber(&http.Client{Timeout: time.Second}), target: target},
		Presentation: projectruntime.Presentation{
			AppID:       target.AppID,
			Name:        target.Name,
			ResourceURL: target.ResourceURL,
		},
	}, nil
}

// goForjPreparationError keeps generated-project remediation actionable without exposing GoForj types to lifecycle code.
func goForjPreparationError(err error) error {
	var updateRequired *projectdiscovery.RenderUpdateRequiredError
	if errors.As(err, &updateRequired) {
		return &projectruntime.PreparationError{Problem: domain.Problem{
			Code:      "project.render.update_required",
			Message:   "This project was rendered by an older GoForj build. Run forj render with the current GoForj version, then try again.",
			Retryable: true,
		}, Cause: err}
	}
	var invalid *projectdiscovery.InvalidProjectError
	if errors.As(err, &invalid) {
		return &projectruntime.PreparationError{Problem: domain.Problem{
			Code:      "project.runtime.invalid",
			Message:   "The project runtime configuration is invalid. Fix it and try again.",
			Retryable: true,
		}, Cause: err}
	}
	return err
}

// goForjReadinessProbe adapts the generated GoForj JSON endpoint to the neutral runtime readiness boundary.
type goForjReadinessProbe struct {
	prober *projectreadiness.Prober
	target projectdiscovery.RuntimeTarget
}

// Probe translates GoForj's readiness state without leaking its response contract outside this adapter.
func (probe goForjReadinessProbe) Probe(ctx context.Context) (projectruntime.ReadinessState, error) {
	state, err := probe.prober.Probe(ctx, probe.target)
	if err != nil {
		return "", err
	}
	if state == projectreadiness.StateReady {
		return projectruntime.ReadinessReady, nil
	}
	return projectruntime.ReadinessPending, nil
}

// Launch starts the ordinary GoForj development runtime for one project session.
func (runtime *Runtime) Launch(ctx context.Context, request projectruntime.LaunchRequest) (projectruntime.Handle, error) {
	handle, err := runtime.supervisor.Start(ctx, projectprocess.StartRequest{
		ProjectID:            request.ProjectID,
		SessionID:            request.SessionID,
		CheckoutRoot:         request.CheckoutRoot,
		EnvironmentOverrides: goForjEnvironmentOverrides(request.NetworkAssignment),
		Stdout:               request.Stdout,
		Stderr:               request.Stderr,
	})
	if err != nil {
		return nil, translateRuntimeError(err)
	}
	return projectHandle{handle: handle}, nil
}

// goForjEnvironmentOverrides maps a neutral Harbor network assignment to GoForj's current dotenv bridge.
func goForjEnvironmentOverrides(assignment projectruntime.NetworkAssignment) projectprocess.EnvironmentOverrides {
	address := assignment.Address.String()
	return projectprocess.EnvironmentOverrides{
		"API_HTTP_HOST":          address,
		"DEV_SERVICE_IP_ADDRESS": address,
		"IP_ADDRESS":             address,
		"LIGHTHOUSE_URL":         fmt.Sprintf("ws://%s:%d/lighthouse/ws/agent", assignment.Address, assignment.PrimaryPort),
	}
}

// Reset withdraws any ordinary GoForj runtime left in a checkout.
func (runtime *Runtime) Reset(ctx context.Context, request projectruntime.ResetRequest) error {
	return translateRuntimeError(runtime.supervisor.Down(ctx, projectprocess.DownRequest{CheckoutRoot: request.CheckoutRoot}))
}

// Stop retires the exact supervised runtime session.
func (runtime *Runtime) Stop(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	return translateRuntimeError(runtime.supervisor.Stop(ctx, projectID, sessionID))
}

// ObserveServices delegates service observation to the GoForj supervisor.
func (runtime *Runtime) ObserveServices(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) (projectruntime.ServiceObservation, error) {
	observation, err := runtime.supervisor.ObserveServices(ctx, projectID, sessionID)
	if errors.Is(err, containerruntime.ErrProjectObservationTransient) {
		err = fmt.Errorf("%w: %v", projectruntime.ErrServiceObservationTransient, err)
	}
	if errors.Is(err, projectprocess.ErrNotRunning) {
		err = fmt.Errorf("%w: %v", projectruntime.ErrNotRunning, err)
	}
	return projectruntime.ServiceObservation{Supported: observation.Supported, Services: append([]domain.ServiceSnapshot(nil), observation.Services...)}, err
}

// ObserveResources translates GoForj resource reports into runtime-neutral, ownership-admitted snapshots.
func (runtime *Runtime) ObserveResources(ctx context.Context, request projectruntime.ResourceObservationRequest) (projectruntime.ResourceObservation, error) {
	observation, err := runtime.supervisor.ObserveFrameworkResources(ctx, request.ProjectID, request.SessionID)
	if err != nil || !observation.Supported {
		return projectruntime.ResourceObservation{Supported: observation.Supported, Resources: []domain.ResourceSnapshot{}}, err
	}
	return projectruntime.ResourceObservation{Supported: true, Resources: admittedResources(request, observation.Resources)}, nil
}

// WaitServiceChange delegates runtime wake hints while preserving neutral retry semantics.
func (runtime *Runtime) WaitServiceChange(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	err := runtime.supervisor.WaitServiceChange(ctx, projectID, sessionID)
	if errors.Is(err, projectprocess.ErrNotRunning) {
		return fmt.Errorf("%w: %v", projectruntime.ErrNotRunning, err)
	}
	if errors.Is(err, containerruntime.ErrProjectChangeUnsupported) {
		return fmt.Errorf("%w: %v", projectruntime.ErrServiceChangeUnsupported, err)
	}
	if errors.Is(err, containerruntime.ErrProjectChangeTransient) {
		return fmt.Errorf("%w: %v", projectruntime.ErrServiceChangeTransient, err)
	}
	return err
}

// ReadOutput delegates project output reads to the GoForj supervisor.
func (runtime *Runtime) ReadOutput(projectID domain.ProjectID, sessionID domain.SessionID, cursor uint64) projectprocess.OutputChunk {
	return runtime.supervisor.ReadOutput(projectID, sessionID, cursor)
}

// WaitOutput delegates project output waits to the GoForj supervisor.
func (runtime *Runtime) WaitOutput(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID, cursor uint64) (projectprocess.OutputChunk, error) {
	return runtime.supervisor.WaitOutput(ctx, projectID, sessionID, cursor)
}

// ReadServiceLogs delegates service log reads to the GoForj supervisor.
func (runtime *Runtime) ReadServiceLogs(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID, serviceID domain.ServiceID, cursor uint64) (projectprocess.ServiceLogSelection, error) {
	return runtime.supervisor.ReadServiceLogs(ctx, projectID, sessionID, serviceID, cursor)
}

// WaitServiceLogs delegates service log waits to the GoForj supervisor.
func (runtime *Runtime) WaitServiceLogs(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID, serviceID domain.ServiceID, cursor uint64) (projectprocess.ServiceLogSelection, error) {
	return runtime.supervisor.WaitServiceLogs(ctx, projectID, sessionID, serviceID, cursor)
}

// ReadOutputHistory delegates retained diagnostic output reads to the GoForj supervisor.
func (runtime *Runtime) ReadOutputHistory(projectID domain.ProjectID, sessionID domain.SessionID, cursor uint64) (projectprocess.OutputChunk, error) {
	return runtime.supervisor.ReadOutputHistory(projectID, sessionID, cursor)
}

// ObserveProjectDescriptor delegates optional descriptor enrichment to the GoForj supervisor.
func (runtime *Runtime) ObserveProjectDescriptor(ctx context.Context, checkoutRoot string) (projectprocess.ProjectDescriptorObservation, error) {
	return runtime.supervisor.ObserveProjectDescriptor(ctx, checkoutRoot)
}

// ObserveServicePorts delegates optional service-port observation to the GoForj supervisor.
func (runtime *Runtime) ObserveServicePorts(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID, serviceID domain.ServiceID) (projectprocess.ServicePortObservation, error) {
	return runtime.supervisor.ObserveServicePorts(ctx, projectID, sessionID, serviceID)
}

// AdoptOutputBroker delegates optional output-broker recovery to the GoForj supervisor.
func (runtime *Runtime) AdoptOutputBroker(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID, broker domain.OutputBrokerSession) error {
	return runtime.supervisor.AdoptOutputBroker(ctx, projectID, sessionID, broker)
}

// ObservePriorProcess delegates retained-process observation to the GoForj supervisor.
func (runtime *Runtime) ObservePriorProcess(ctx context.Context, evidence domain.ProcessEvidence) (projectruntime.PriorProcessObservation, error) {
	observation, err := runtime.supervisor.ObservePriorProcess(ctx, evidence)
	return projectruntime.PriorProcessObservation{State: priorProcessState(observation.State)}, err
}

// SettlePriorProcess delegates retained-process settlement to the GoForj supervisor.
func (runtime *Runtime) SettlePriorProcess(ctx context.Context, evidence domain.ProcessEvidence) (projectruntime.PriorProcessSettlement, error) {
	settlement, err := runtime.supervisor.SettlePriorProcess(ctx, evidence)
	return projectruntime.PriorProcessSettlement{Outcome: priorProcessSettlementOutcome(settlement.Outcome)}, err
}

// Close delegates runtime shutdown to the GoForj supervisor.
func (runtime *Runtime) Close(ctx context.Context) error {
	return runtime.supervisor.Close(ctx)
}

var _ projectruntime.Runtime = (*Runtime)(nil)
var _ projectruntime.ListenerRepairer = (*Runtime)(nil)
var _ projectruntime.ServiceObserver = (*Runtime)(nil)
var _ projectruntime.ResourceObserver = (*Runtime)(nil)
var _ projectruntime.ServiceChangeWaiter = (*Runtime)(nil)

// admittedResources keeps GoForj-specific raw resource ownership at the adapter boundary.
func admittedResources(request projectruntime.ResourceObservationRequest, reported []projectprocess.FrameworkResource) []domain.ResourceSnapshot {
	services := make(map[domain.ServiceID]struct{}, len(request.Services))
	for _, service := range request.Services {
		services[service.ID] = struct{}{}
	}
	resources := make([]domain.ResourceSnapshot, 0, len(reported))
	for _, resource := range reported {
		if resource.ID == "app-http" || !resourceUsesAssignedAddress(resource.URL, request.Plan.NetworkAssignment.Address) {
			continue
		}
		snapshot := domain.ResourceSnapshot{ID: domain.ResourceID(resource.ID), Name: resource.Name, Kind: resource.Kind, URL: resource.URL}
		switch {
		case resource.App == string(request.Plan.Presentation.AppID) && resource.Service == "":
			if equivalentHTTPResourceURL(resource.URL, request.Plan.Presentation.ResourceURL) {
				continue
			}
			snapshot.Owner = domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: request.Plan.Presentation.AppID}
		case resource.App == "" && resource.Service != "":
			serviceID := domain.ServiceID(resource.Service)
			if _, found := services[serviceID]; !found {
				continue
			}
			snapshot.Owner = domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: serviceID}
		default:
			continue
		}
		resources = append(resources, snapshot)
	}
	sort.Slice(resources, func(left, right int) bool { return resources[left].ID < resources[right].ID })
	return resources
}

// resourceUsesAssignedAddress keeps optional links on the private identity Harbor proved for the session.
func resourceUsesAssignedAddress(rawURL string, assignedAddress netip.Addr) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	return err == nil && address.Unmap() == assignedAddress.Unmap()
}

// equivalentHTTPResourceURL treats a root trailing slash as the same primary application resource.
func equivalentHTTPResourceURL(left string, right string) bool {
	leftURL, leftErr := url.Parse(left)
	rightURL, rightErr := url.Parse(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(leftURL.Scheme, rightURL.Scheme) &&
		strings.EqualFold(leftURL.Host, rightURL.Host) &&
		strings.TrimSuffix(leftURL.EscapedPath(), "/") == strings.TrimSuffix(rightURL.EscapedPath(), "/") &&
		leftURL.RawQuery == rightURL.RawQuery && leftURL.Fragment == rightURL.Fragment
}

// projectHandle translates supervisor-specific process details into the neutral runtime handle shape.
type projectHandle struct {
	handle *projectprocess.Handle
}

// Info returns an immutable runtime launch description.
func (handle projectHandle) Info() projectruntime.Info {
	info := handle.handle.Info()
	return projectruntime.Info{
		ProjectID:    info.ProjectID,
		SessionID:    info.SessionID,
		CheckoutRoot: info.CheckoutRoot,
		Evidence: projectruntime.Evidence{
			PID:                info.Evidence.PID,
			BirthToken:         info.Evidence.BirthToken,
			ExecutableIdentity: info.Evidence.ExecutableIdentity,
			ArgumentDigest:     info.Evidence.ArgumentsSHA256,
		},
		OutputBroker: outputBrokerSession(info.OutputBroker),
		StartedAt:    info.StartedAt,
	}
}

// Done closes when the runtime has exited.
func (handle projectHandle) Done() <-chan struct{} {
	return handle.handle.Done()
}

// Result returns the completed runtime result when available.
func (handle projectHandle) Result() (projectruntime.Exit, bool) {
	exit, complete := handle.handle.Result()
	return runtimeExit(exit), complete
}

// Wait waits for completion or context cancellation.
func (handle projectHandle) Wait(ctx context.Context) (projectruntime.Exit, error) {
	exit, err := handle.handle.Wait(ctx)
	return runtimeExit(exit), err
}

// runtimeExit converts one process-supervisor result into the neutral runtime shape.
func runtimeExit(exit projectprocess.Exit) projectruntime.Exit {
	return projectruntime.Exit{
		ExitCode:           exit.ExitCode,
		Err:                exit.Err,
		ScopeSettlementErr: exit.ScopeSettlementErr,
		StopRequested:      exit.StopRequested,
		DroppedOutputLines: exit.DroppedOutputLines,
		ExitedAt:           exit.ExitedAt,
	}
}

// translateRuntimeError preserves generic runtime failure semantics across the adapter boundary.
func translateRuntimeError(err error) error {
	if errors.Is(err, projectprocess.ErrCleanupUncertain) {
		return fmt.Errorf("%w: %v", projectruntime.ErrCleanupUncertain, err)
	}
	if errors.Is(err, projectprocess.ErrNotRunning) {
		return fmt.Errorf("%w: %v", projectruntime.ErrNotRunning, err)
	}
	return err
}

// priorProcessState translates known supervisor recovery states without treating new states as safe.
func priorProcessState(state projectprocess.PriorProcessState) projectruntime.PriorProcessState {
	switch state {
	case projectprocess.PriorProcessAbsent:
		return projectruntime.PriorProcessAbsent
	case projectprocess.PriorProcessReplaced:
		return projectruntime.PriorProcessReplaced
	case projectprocess.PriorProcessPresent:
		return projectruntime.PriorProcessPresent
	default:
		return projectruntime.PriorProcessState(state)
	}
}

// priorProcessSettlementOutcome translates known supervisor settlement outcomes without treating new outcomes as safe.
func priorProcessSettlementOutcome(outcome projectprocess.PriorProcessSettlementOutcome) projectruntime.PriorProcessSettlementOutcome {
	switch outcome {
	case projectprocess.PriorProcessSettlementAbsent:
		return projectruntime.PriorProcessSettlementAbsent
	case projectprocess.PriorProcessSettlementReplaced:
		return projectruntime.PriorProcessSettlementReplaced
	case projectprocess.PriorProcessSettlementTerminated:
		return projectruntime.PriorProcessSettlementTerminated
	default:
		return projectruntime.PriorProcessSettlementOutcome(outcome)
	}
}

// outputBrokerSession converts optional supervisor output continuity evidence into durable session form.
func outputBrokerSession(peer *projectprocess.OutputBrokerPeer) *domain.OutputBrokerSession {
	if peer == nil {
		return nil
	}
	return &domain.OutputBrokerSession{
		EndpointReference: peer.EndpointReference,
		ManifestPath:      peer.ManifestPath,
		CredentialDigest:  peer.TicketDigest,
		Process: domain.ProcessEvidence{
			PID:                peer.Process.PID,
			BirthToken:         peer.Process.BirthToken,
			ExecutableIdentity: peer.Process.ExecutableIdentity,
			ArgumentDigest:     peer.Process.ArgumentDigest,
		},
	}
}
