// Package projectruntime defines Harbor's neutral project runtime boundary.
package projectruntime

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

var (
	// ErrCleanupUncertain means an accepted runtime did not prove its complete ownership scope was retired.
	ErrCleanupUncertain = errors.New("project runtime cleanup is uncertain")
	// ErrNotRunning means the selected project session has no active runtime process.
	ErrNotRunning = errors.New("project runtime is not running")
	// ErrServiceChangeUnsupported means the runtime cannot notify Harbor about service topology changes.
	ErrServiceChangeUnsupported = errors.New("project runtime service change observation is unsupported")
	// ErrServiceChangeTransient means a service-change wait may succeed after a bounded retry.
	ErrServiceChangeTransient = errors.New("project runtime service change observation is transient")
	// ErrServiceObservationTransient means a complete service observation may succeed after a bounded retry.
	ErrServiceObservationTransient = errors.New("project runtime service observation is transient")
)

// NetworkAssignment identifies the private listener assignment Harbor reserves for a project runtime.
type NetworkAssignment struct {
	Address     netip.Addr
	PrimaryPort uint16
}

// ListenerRepairRequest identifies one checkout-scoped listener that an optional runtime capability may settle.
type ListenerRepairRequest struct {
	CheckoutRoot        string
	Endpoint            netip.AddrPort
	RequireConfirmation bool
}

// ListenerRepairResult reports whether a runtime capability proved the requested listener scope settled.
type ListenerRepairResult struct {
	Settled bool
}

// ListenerRepairer optionally settles a listener that the runtime can prove belongs to one checkout.
//
// Implementations must leave listeners unresolved unless they can establish checkout-scoped authority.
type ListenerRepairer interface {
	RepairListener(context.Context, ListenerRepairRequest) (ListenerRepairResult, error)
}

// PreparationRequest identifies the checkout and Harbor-assigned address a runtime must prepare before launch.
type PreparationRequest struct {
	CheckoutRoot string
	Address      netip.Addr
}

// ReadinessState distinguishes a runtime that is still starting from one that has proven ready.
type ReadinessState string

const (
	// ReadinessPending means the runtime has not yet proven ready.
	ReadinessPending ReadinessState = "pending"
	// ReadinessReady means the runtime has proven ready for its prepared plan.
	ReadinessReady ReadinessState = "ready"
)

// ReadinessProbe performs one bounded readiness observation for a prepared runtime plan.
type ReadinessProbe interface {
	Probe(context.Context) (ReadinessState, error)
}

// Presentation preserves the neutral facts Harbor projects for a runtime's primary application.
type Presentation struct {
	AppID       domain.AppID
	Name        string
	ResourceURL string
}

// Plan is the runtime-owned pre-launch contract for one Harbor-assigned address.
type Plan struct {
	NetworkAssignment NetworkAssignment
	Readiness         ReadinessProbe
	Presentation      Presentation
}

// Preparer derives a runtime plan without launching a project process.
type Preparer interface {
	Prepare(context.Context, PreparationRequest) (Plan, error)
}

// PreparationError reports a bounded, provider-selected problem that Harbor may show before launch.
type PreparationError struct {
	Problem domain.Problem
	Cause   error
}

// Error returns the provider preparation diagnostic.
func (err *PreparationError) Error() string {
	if err == nil || err.Cause == nil {
		return "project runtime preparation failed"
	}
	return err.Cause.Error()
}

// Unwrap preserves the provider diagnostic for logs and local callers.
func (err *PreparationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// LaunchRequest identifies a project runtime launch without selecting a runtime implementation.
type LaunchRequest struct {
	ProjectID            domain.ProjectID
	SessionID            domain.SessionID
	CheckoutRoot         string
	NetworkAssignment    NetworkAssignment
	EnvironmentOverrides []EnvironmentVariable
	Stdout               io.Writer
	Stderr               io.Writer
}

// PriorProcessState classifies whether persisted runtime evidence still names the same host process.
type PriorProcessState string

const (
	// PriorProcessAbsent means the persisted PID no longer identifies a host process.
	PriorProcessAbsent PriorProcessState = "absent"
	// PriorProcessReplaced means the persisted PID has been reused by another process birth.
	PriorProcessReplaced PriorProcessState = "replaced"
	// PriorProcessPresent means the persisted PID and birth token still match.
	PriorProcessPresent PriorProcessState = "present"
)

// PriorProcessObservation contains the conservative recovery result for one persisted process birth.
type PriorProcessObservation struct {
	State PriorProcessState
}

// PriorProcessSettlementOutcome describes how persisted process authority became safe to retire.
type PriorProcessSettlementOutcome string

const (
	// PriorProcessSettlementAbsent means the process was already gone before a signal.
	PriorProcessSettlementAbsent PriorProcessSettlementOutcome = "absent"
	// PriorProcessSettlementReplaced means the PID names another birth and was never signaled.
	PriorProcessSettlementReplaced PriorProcessSettlementOutcome = "replaced"
	// PriorProcessSettlementTerminated means the exact process was observed leaving after a signal.
	PriorProcessSettlementTerminated PriorProcessSettlementOutcome = "terminated"
)

// PriorProcessSettlement reports the successful terminal outcome for one persisted process birth.
type PriorProcessSettlement struct {
	Outcome PriorProcessSettlementOutcome
}

// ResetRequest identifies the checkout whose leftover runtime must be withdrawn before a replacement launch.
type ResetRequest struct {
	CheckoutRoot string
}

// Evidence binds future runtime actions to one exact process birth.
type Evidence struct {
	PID                int64
	BirthToken         string
	ExecutableIdentity string
	ArgumentDigest     string
}

// Info describes an accepted runtime launch.
type Info struct {
	ProjectID    domain.ProjectID
	SessionID    domain.SessionID
	CheckoutRoot string
	Evidence     Evidence
	OutputBroker *domain.OutputBrokerSession
	StartedAt    time.Time
}

// Exit describes a completed runtime process.
type Exit struct {
	ExitCode           int
	Err                error
	ScopeSettlementErr error
	StopRequested      bool
	DroppedOutputLines uint64
	ExitedAt           time.Time
}

// Handle observes one launched runtime without exposing implementation process handles.
type Handle interface {
	Info() Info
	Done() <-chan struct{}
	Result() (Exit, bool)
	Wait(context.Context) (Exit, error)
}

// Runtime owns project runtime launch, reset, supervision, and observation for the daemon lifetime.
type Runtime interface {
	Preparer
	Launch(context.Context, LaunchRequest) (Handle, error)
	Reset(context.Context, ResetRequest) error
	Stop(context.Context, domain.ProjectID, domain.SessionID) error
	ObservePriorProcess(context.Context, domain.ProcessEvidence) (PriorProcessObservation, error)
	SettlePriorProcess(context.Context, domain.ProcessEvidence) (PriorProcessSettlement, error)
	Close(context.Context) error
}

// ServiceObservation is one complete optional replacement view of runtime services.
type ServiceObservation struct {
	Supported bool
	Services  []domain.ServiceSnapshot
}

// ServicePort is one non-secret host publication observed for a runtime service.
type ServicePort struct {
	Address  string
	Private  uint16
	Public   uint16
	Protocol string
	Replica  int
}

// ServicePortObservation is the current, non-durable port view for one runtime service.
type ServicePortObservation struct {
	Supported bool
	Available bool
	Ports     []ServicePort
}

// ResourceObservationRequest supplies the proven runtime facts needed to admit optional resource links.
type ResourceObservationRequest struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	Plan      Plan
	Services  []domain.ServiceSnapshot
}

// ResourceObservation is one complete optional replacement view of runtime resources.
type ResourceObservation struct {
	Supported bool
	Resources []domain.ResourceSnapshot
}

// ServiceObserver optionally observes the complete runtime service topology.
type ServiceObserver interface {
	ObserveServices(context.Context, domain.ProjectID, domain.SessionID) (ServiceObservation, error)
}

// ServicePortObserver optionally observes current host publications for one runtime service.
type ServicePortObserver interface {
	ObserveServicePorts(context.Context, domain.ProjectID, domain.SessionID, domain.ServiceID) (ServicePortObservation, error)
}

// ResourceObserver optionally observes resource links already admitted against runtime facts.
type ResourceObserver interface {
	ObserveResources(context.Context, ResourceObservationRequest) (ResourceObservation, error)
}

// ServiceChangeWaiter optionally blocks until runtime service topology may have changed.
type ServiceChangeWaiter interface {
	WaitServiceChange(context.Context, domain.ProjectID, domain.SessionID) error
}
