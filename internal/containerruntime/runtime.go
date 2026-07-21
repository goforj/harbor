// Package containerruntime exposes Harbor's narrow host-container observation and log boundary.
package containerruntime

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrProjectChangeUnsupported indicates that the host runtime cannot stream container changes.
	ErrProjectChangeUnsupported = errors.New("container runtime change stream unsupported")
	// ErrProjectChangeTransient indicates that a runtime event stream ended while the caller may reconnect safely.
	//
	// The event stream is only a wake hint. A caller that receives this error must
	// reconnect and perform a fresh observation rather than treating the failed
	// stream as a topology change.
	ErrProjectChangeTransient = errors.New("container runtime change stream is transiently unavailable")
)

// Runtime observes Compose-owned containers admitted to one canonical Harbor checkout.
type Runtime interface {
	// ObserveProject returns one point-in-time logical-service view without transferring container lifecycle authority.
	ObserveProject(context.Context, string) (ProjectObservation, error)
	// OpenServiceLogs opens one dynamic follower for every admitted current and recreated replica of a logical service.
	OpenServiceLogs(context.Context, string, string, int) (LogFollower, error)
	// Close releases runtime client transport resources after all observations and followers have ended.
	Close() error
}

// ProjectChangeSource wakes a caller after a host container event without treating the event as trusted topology.
//
// Callers must perform a fresh ObserveProject after every wake. The event stream is only an efficiency hint because
// neighboring projects share the same local Engine and event payloads do not prove checkout ownership.
type ProjectChangeSource interface {
	WaitProjectChange(context.Context, string) error
}

// ProjectObservation is one ephemeral replacement view of the runtime services for a checkout.
type ProjectObservation struct {
	Services []Service
}

// Service groups ephemeral containers under their Compose logical-service identity.
type Service struct {
	ID         string
	Name       string
	Project    string
	State      string
	Health     string
	Active     bool
	Containers []Container
}

// Container carries ephemeral runtime context that Harbor must never persist as project identity.
type Container struct {
	ID          string
	Name        string
	Image       string
	State       string
	Health      string
	ExitCode    int
	Replica     int
	TTY         bool
	Environment []string
	Ports       []Port
}

// Port describes one published or internal container port reported by the runtime.
type Port struct {
	Address  string
	Private  uint16
	Public   uint16
	Protocol string
}

// LogFollower copies correctly decoded, attributed output across current and recreated service replicas.
type LogFollower interface {
	// Available reports whether the latest runtime selection contains at least one admitted container.
	Available() bool
	// WaitAvailable holds until at least one admitted container appears or the caller cancels.
	WaitAvailable(context.Context) error
	// WaitStateChange holds until current container availability differs from the supplied state or the caller cancels.
	WaitStateChange(context.Context, bool) error
	// CopyTo follows runtime changes and copies combined application bytes until cancellation.
	CopyTo(io.Writer) error
	// Close interrupts every underlying runtime response body and polling operation.
	Close() error
}

// unavailableRuntime preserves daemon startup when the host runtime client cannot be configured.
type unavailableRuntime struct {
	cause error
}

// NewUnavailable returns a runtime whose operations report one immutable configuration failure.
func NewUnavailable(cause error) Runtime {
	if cause == nil {
		cause = errors.New("container runtime is unavailable")
	}
	return &unavailableRuntime{cause: cause}
}

// ObserveProject reports the runtime configuration failure without inventing project state.
func (runtime *unavailableRuntime) ObserveProject(context.Context, string) (ProjectObservation, error) {
	return ProjectObservation{}, runtime.cause
}

// OpenServiceLogs reports the runtime configuration failure without opening a follower.
func (runtime *unavailableRuntime) OpenServiceLogs(context.Context, string, string, int) (LogFollower, error) {
	return nil, runtime.cause
}

// Close is inert because no runtime transport was created.
func (*unavailableRuntime) Close() error {
	return nil
}
