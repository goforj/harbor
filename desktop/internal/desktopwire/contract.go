// Package desktopwire defines the Go-owned method and event contract exposed through Wails.
package desktopwire

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

const (
	// MethodAddProject is the generated Wails method that selects and registers one local project.
	MethodAddProject = "AddProject"
	// MethodApproveProjectRemoval is the generated Wails method that explicitly approves one retained removal intent.
	MethodApproveProjectRemoval = "ApproveProjectRemoval"
	// MethodConfirmProjectRuntimeRepair is the generated Wails method that confirms one inspected stale runtime.
	MethodConfirmProjectRuntimeRepair = "ConfirmProjectRuntimeRepair"
	// MethodInspectProjectRuntimeRepair is the generated Wails method that inspects one quarantined project runtime.
	MethodInspectProjectRuntimeRepair = "InspectProjectRuntimeRepair"
	// MethodOpenResource is the generated Wails method that opens one reviewed project resource.
	MethodOpenResource = "OpenResource"
	// MethodOpenTerminalURL is the generated Wails method that opens one validated terminal web link.
	MethodOpenTerminalURL = "OpenTerminalURL"
	// MethodResourceIconURL is the generated Wails method that reads one resource page's declared icon.
	MethodResourceIconURL = "ResourceIconURL"
	// MethodProjectActivity is the generated Wails method that reads current project development output.
	MethodProjectActivity = "ProjectActivity"
	// MethodProjectEnvironment is the generated Wails method that reads one project's runtime environment inputs.
	MethodProjectEnvironment = "ProjectEnvironment"
	// MethodSaveProjectEnvironmentFile is the generated Wails method that writes one revision-fenced project environment file.
	MethodSaveProjectEnvironmentFile = "SaveProjectEnvironmentFile"
	// MethodServiceLogs is the generated Wails method that reads current Compose service output.
	MethodServiceLogs = "ServiceLogs"
	// MethodWaitServiceLogs is the generated Wails method that holds a service output cursor until it advances or times out.
	MethodWaitServiceLogs = "WaitServiceLogs"
	// MethodWaitProjectActivity is the generated Wails method that holds a current output cursor until it advances or times out.
	MethodWaitProjectActivity = "WaitProjectActivity"
	// MethodRemoveProject is the generated Wails method that starts or resumes one project removal intent.
	MethodRemoveProject = "RemoveProject"
	// MethodSetupNetwork is the generated Wails method that completes the machine-global network foundation.
	MethodSetupNetwork = "SetupNetwork"
	// MethodRemoveOldNetworking is the generated Wails method that retires legacy machine-global resolver networking.
	MethodRemoveOldNetworking = "RemoveOldNetworking"
	// MethodStartProject is the generated Wails method that starts one registered project.
	MethodStartProject = "StartProject"
	// MethodStopProject is the generated Wails method that stops one registered project.
	MethodStopProject = "StopProject"
	// MethodRestartProject is the generated Wails method that replaces one registered project session.
	MethodRestartProject = "RestartProject"
	// MethodStartProjectTerminal is the generated Wails method that opens one interactive project shell.
	MethodStartProjectTerminal = "StartProjectTerminal"
	// MethodAttachProjectTerminal is the generated Wails method that begins one interactive project shell's output.
	MethodAttachProjectTerminal = "AttachProjectTerminal"
	// MethodWriteProjectTerminal is the generated Wails method that writes input to one interactive project shell.
	MethodWriteProjectTerminal = "WriteProjectTerminal"
	// MethodResizeProjectTerminal is the generated Wails method that resizes one interactive project shell.
	MethodResizeProjectTerminal = "ResizeProjectTerminal"
	// MethodCloseProjectTerminal is the generated Wails method that closes one interactive project shell.
	MethodCloseProjectTerminal = "CloseProjectTerminal"
	// MethodSnapshot is the generated Wails method that returns complete desktop-visible state.
	MethodSnapshot = "Snapshot"
	// MethodStatus is the generated Wails method that returns the daemon diagnostic.
	MethodStatus = "Status"
	// ConnectionEventName carries ephemeral daemon connection lifecycle changes.
	ConnectionEventName = "harbor:connection"
	// SnapshotEventName carries validated complete replacement snapshots.
	SnapshotEventName = "harbor:snapshot"
	// ProjectTerminalEventName carries output and exit events for desktop-owned interactive project shells.
	ProjectTerminalEventName       = "harbor:project-terminal"
	projectTerminalSessionIDPrefix = "terminal-"
	projectTerminalSessionIDBytes  = 16
	projectTerminalOutputBytes     = 32 * 1024
	projectTerminalErrorBytes      = 512
)

// AddProjectResult distinguishes a dismissed native picker from a completed daemon registration.
type AddProjectResult struct {
	Canceled     bool                         `json:"canceled"`
	Registration *control.ProjectRegistration `json:"registration,omitempty"`
}

// ProjectTerminalStarted identifies the desktop-owned shell created for one project.
type ProjectTerminalStarted struct {
	SessionID string `json:"session_id"`
}

// Validate reports whether the started terminal has an opaque session identity.
func (started ProjectTerminalStarted) Validate() error {
	return validateProjectTerminalSessionID(started.SessionID)
}

// ProjectTerminalEventKind identifies one terminal stream event.
type ProjectTerminalEventKind string

const (
	// ProjectTerminalOutput carries one base64-encoded PTY output frame.
	ProjectTerminalOutput ProjectTerminalEventKind = "output"
	// ProjectTerminalExited reports that the PTY shell has exited.
	ProjectTerminalExited ProjectTerminalEventKind = "exited"
)

// ProjectTerminalEvent carries one session-scoped PTY output or exit event.
type ProjectTerminalEvent struct {
	SessionID  string                   `json:"session_id"`
	Kind       ProjectTerminalEventKind `json:"kind"`
	DataBase64 string                   `json:"data_base64,omitempty"`
	Error      string                   `json:"error,omitempty"`
	Dropped    bool                     `json:"dropped,omitempty"`
}

// Validate reports whether the terminal event contains exactly the payload for its kind.
func (event ProjectTerminalEvent) Validate() error {
	if err := validateProjectTerminalSessionID(event.SessionID); err != nil {
		return err
	}
	switch event.Kind {
	case ProjectTerminalOutput:
		if event.DataBase64 == "" {
			return fmt.Errorf("project terminal output data is required")
		}
		if len(event.DataBase64) > base64.StdEncoding.EncodedLen(projectTerminalOutputBytes) {
			return fmt.Errorf("project terminal output exceeds %d bytes", projectTerminalOutputBytes)
		}
		decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
		if err != nil {
			return fmt.Errorf("decode project terminal output: %w", err)
		}
		if len(decoded) > projectTerminalOutputBytes {
			return fmt.Errorf("project terminal output exceeds %d bytes", projectTerminalOutputBytes)
		}
		if event.Error != "" {
			return fmt.Errorf("project terminal output must not contain an exit error")
		}
	case ProjectTerminalExited:
		if event.DataBase64 != "" {
			return fmt.Errorf("project terminal exit must not contain output data")
		}
	default:
		return fmt.Errorf("unknown project terminal event kind %q", event.Kind)
	}
	if len(event.Error) > projectTerminalErrorBytes {
		return fmt.Errorf("project terminal exit error exceeds %d bytes", projectTerminalErrorBytes)
	}
	return nil
}

// validateProjectTerminalSessionID requires the exact random identity shape created by the PTY manager.
func validateProjectTerminalSessionID(sessionID string) error {
	if !strings.HasPrefix(sessionID, projectTerminalSessionIDPrefix) {
		return fmt.Errorf("project terminal session ID has an invalid prefix")
	}
	encoded := strings.TrimPrefix(sessionID, projectTerminalSessionIDPrefix)
	if len(encoded) != projectTerminalSessionIDBytes*2 {
		return fmt.Errorf("project terminal session ID has an invalid length")
	}
	if _, err := hex.DecodeString(encoded); err != nil {
		return fmt.Errorf("project terminal session ID is not hexadecimal: %w", err)
	}
	return nil
}

// Validate reports whether the picker outcome contains exactly the state appropriate to its disposition.
func (result AddProjectResult) Validate() error {
	if result.Canceled {
		if result.Registration != nil {
			return fmt.Errorf("canceled project selection must not contain a registration")
		}
		return nil
	}
	if result.Registration == nil {
		return fmt.Errorf("completed project selection must contain a registration")
	}
	return result.Registration.Validate()
}

// AppContract is the complete exported method surface Wails may bind from App.
type AppContract interface {
	AddProject() (AddProjectResult, error)
	ApproveProjectRemoval(projectID string, intentID string) (control.ProjectUnregistration, error)
	ConfirmProjectRuntimeRepair(projectID string, inspectionID string, candidateFingerprint string) (control.ProjectRuntimeRepairConfirmation, error)
	InspectProjectRuntimeRepair(projectID string) (control.ProjectRuntimeRepairInspection, error)
	OpenResource(projectID string, resourceID string) error
	OpenTerminalURL(rawURL string) error
	ResourceIconURL(projectID string, resourceID string) (string, error)
	ProjectActivity(projectID string, sessionID string, cursor uint64) (control.ProjectActivity, error)
	ProjectEnvironment(projectID string) (control.ProjectEnvironment, error)
	SaveProjectEnvironmentFile(projectID string, name string, contents string, revision string) (control.ProjectEnvironmentFile, error)
	ServiceLogs(projectID string, sessionID string, serviceID string, cursor uint64) (control.ServiceLogs, error)
	RemoveProject(projectID string, intentID string) (control.ProjectUnregistration, error)
	RemoveOldNetworking() (control.NetworkResolverPolicyMigrationOperation, error)
	SetupNetwork() (control.NetworkSetupOperation, error)
	Snapshot() (domain.Snapshot, error)
	StartProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error)
	Status() (control.DaemonStatus, error)
	StopProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error)
	RestartProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error)
	StartProjectTerminal(projectID string, columns uint16, rows uint16) (ProjectTerminalStarted, error)
	AttachProjectTerminal(sessionID string) error
	WriteProjectTerminal(sessionID string, data string) error
	ResizeProjectTerminal(sessionID string, columns uint16, rows uint16) error
	CloseProjectTerminal(sessionID string) error
	WaitProjectActivity(projectID string, sessionID string, cursor uint64, waitMilliseconds uint64) (control.ProjectActivity, error)
	WaitServiceLogs(projectID string, sessionID string, serviceID string, cursor uint64, waitMilliseconds uint64) (control.ServiceLogs, error)
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
		MethodAddProject:                  {},
		MethodApproveProjectRemoval:       []string{"projectId", "intentId"},
		MethodConfirmProjectRuntimeRepair: []string{"projectId", "inspectionId", "candidateFingerprint"},
		MethodInspectProjectRuntimeRepair: []string{"projectId"},
		MethodOpenResource:                []string{"projectId", "resourceId"},
		MethodOpenTerminalURL:             []string{"rawURL"},
		MethodResourceIconURL:             []string{"projectId", "resourceId"},
		MethodProjectActivity:             []string{"projectId", "sessionId", "cursor"},
		MethodProjectEnvironment:          []string{"projectId"},
		MethodSaveProjectEnvironmentFile:  []string{"projectId", "name", "contents", "revision"},
		MethodServiceLogs:                 []string{"projectId", "sessionId", "serviceId", "cursor"},
		MethodRemoveProject:               []string{"projectId", "intentId"},
		MethodRemoveOldNetworking:         {},
		MethodSetupNetwork:                {},
		MethodSnapshot:                    []string{},
		MethodStartProject:                []string{"projectId", "intentId"},
		MethodStatus:                      []string{},
		MethodStopProject:                 []string{"projectId", "intentId"},
		MethodRestartProject:              []string{"projectId", "intentId"},
		MethodStartProjectTerminal:        []string{"projectId", "columns", "rows"},
		MethodAttachProjectTerminal:       []string{"sessionId"},
		MethodWriteProjectTerminal:        []string{"sessionId", "data"},
		MethodResizeProjectTerminal:       []string{"sessionId", "columns", "rows"},
		MethodCloseProjectTerminal:        []string{"sessionId"},
		MethodWaitProjectActivity:         []string{"projectId", "sessionId", "cursor", "waitMilliseconds"},
		MethodWaitServiceLogs:             []string{"projectId", "sessionId", "serviceId", "cursor", "waitMilliseconds"},
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

// ProjectTerminal publishes one typed interactive terminal stream payload.
func (emitter Emitter) ProjectTerminal(ctx context.Context, payload ProjectTerminalEvent) {
	emitter.emit(ctx, ProjectTerminalEventName, payload)
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
		{Name: ProjectTerminalEventName, EmitterMethod: "ProjectTerminal", Payload: reflect.TypeFor[ProjectTerminalEvent]()},
	}
}
