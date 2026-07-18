// Package desktopwire defines the Go-owned method and event contract exposed through Wails.
package desktopwire

import (
	"context"
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

const (
	// MethodOpenResource is the generated Wails method that opens one reviewed project resource.
	MethodOpenResource = "OpenResource"
	// MethodSnapshot is the generated Wails method that returns complete desktop-visible state.
	MethodSnapshot = "Snapshot"
	// MethodStatus is the generated Wails method that returns the daemon diagnostic.
	MethodStatus = "Status"
	// ConnectionEventName carries ephemeral daemon connection lifecycle changes.
	ConnectionEventName = "harbor:connection"
	// SnapshotEventName carries validated complete replacement snapshots.
	SnapshotEventName = "harbor:snapshot"
)

// AppContract is the complete exported method surface Wails may bind from App.
type AppContract interface {
	OpenResource(projectID string, resourceID string) error
	Snapshot() (domain.Snapshot, error)
	Status() (control.DaemonStatus, error)
}

// MethodContract describes one reflected App method and its stable TypeScript parameter labels.
type MethodContract struct {
	Name           string
	ParameterNames []string
	Signature      reflect.Type
}

// MethodContracts reflects the Go interface that the generated TypeScript binding must match exactly.
func MethodContracts() []MethodContract {
	contractType := reflect.TypeOf((*AppContract)(nil)).Elem()
	parameterNames := map[string][]string{
		MethodOpenResource: []string{"projectId", "resourceId"},
		MethodSnapshot:     []string{},
		MethodStatus:       []string{},
	}
	contracts := make([]MethodContract, 0, contractType.NumMethod())
	for index := range contractType.NumMethod() {
		method := contractType.Method(index)
		contracts = append(contracts, MethodContract{
			Name:           method.Name,
			ParameterNames: append([]string(nil), parameterNames[method.Name]...),
			Signature:      method.Type,
		})
	}
	return contracts
}

// ConnectionState identifies the desktop backend's current relationship to harbord.
type ConnectionState string

const (
	// ConnectionConnecting means the desktop is opening or negotiating a daemon session.
	ConnectionConnecting ConnectionState = "connecting"
	// ConnectionConnected means the desktop owns a negotiated daemon session.
	ConnectionConnected ConnectionState = "connected"
	// ConnectionDisconnected means the last connection attempt or live session ended.
	ConnectionDisconnected ConnectionState = "disconnected"
)

// Validate reports whether state is one of the lifecycle values understood by the frontend.
func (state ConnectionState) Validate() error {
	switch state {
	case ConnectionConnecting, ConnectionConnected, ConnectionDisconnected:
		return nil
	default:
		return fmt.Errorf("unknown desktop connection state %q", state)
	}
}

// ConnectionEvent reports connection lifecycle independently from durable snapshot revisions.
type ConnectionEvent struct {
	State ConnectionState `json:"state"`
}

// Validate reports whether the connection payload can cross the Wails event boundary.
func (event ConnectionEvent) Validate() error {
	return event.State.Validate()
}

// RawEmitter is the untyped Wails event function kept behind Harbor's typed event boundary.
type RawEmitter func(context.Context, string, ...interface{})

// Emitter publishes only the event-name and payload pairs declared by EventContracts.
type Emitter struct {
	emit RawEmitter
}

// NewEmitter wraps Wails' generic emitter with Harbor's typed event methods.
func NewEmitter(emit RawEmitter) Emitter {
	return Emitter{emit: emit}
}

// Connection publishes one typed connection lifecycle payload.
func (emitter Emitter) Connection(ctx context.Context, payload ConnectionEvent) {
	emitter.emit(ctx, ConnectionEventName, payload)
}

// Snapshot publishes one validated replacement snapshot payload.
func (emitter Emitter) Snapshot(ctx context.Context, payload domain.Snapshot) {
	emitter.emit(ctx, SnapshotEventName, payload)
}

// EventContract binds one event name to its Go payload and typed emitter method.
type EventContract struct {
	Name          string
	EmitterMethod string
	Payload       reflect.Type
}

// EventContracts returns the complete event map used by Go emission and generated TypeScript subscriptions.
func EventContracts() []EventContract {
	return []EventContract{
		{Name: ConnectionEventName, EmitterMethod: "Connection", Payload: reflect.TypeFor[ConnectionEvent]()},
		{Name: SnapshotEventName, EmitterMethod: "Snapshot", Payload: reflect.TypeFor[domain.Snapshot]()},
	}
}
