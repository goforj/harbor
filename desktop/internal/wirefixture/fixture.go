// Package wirefixture owns the compile-checked frontend contract artifact generated from Go wire types.
package wirefixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

// MethodMetadata records the generated Wails method names consumed by the bridge.
type MethodMetadata struct {
	AddProject                  string `json:"add_project"`
	ApproveProjectRemoval       string `json:"approve_project_removal"`
	ConfirmProjectRuntimeRepair string `json:"confirm_project_runtime_repair"`
	InspectProjectRuntimeRepair string `json:"inspect_project_runtime_repair"`
	OpenResource                string `json:"open_resource"`
	OpenTerminalURL             string `json:"open_terminal_url"`
	ResourceIconURL             string `json:"resource_icon_url"`
	ProjectActivity             string `json:"project_activity"`
	ServiceLogs                 string `json:"service_logs"`
	WaitProjectActivity         string `json:"wait_project_activity"`
	WaitServiceLogs             string `json:"wait_service_logs"`
	RemoveProject               string `json:"remove_project"`
	SetupNetwork                string `json:"setup_network"`
	Snapshot                    string `json:"snapshot"`
	StartProject                string `json:"start_project"`
	RestartProject              string `json:"restart_project"`
	Status                      string `json:"status"`
	StopProject                 string `json:"stop_project"`
}

// EventMetadata records the Wails event names consumed by the bridge.
type EventMetadata struct {
	Connection string `json:"connection"`
	Snapshot   string `json:"snapshot"`
}

// ConnectionPayloads exercises every typed connection event variant.
type ConnectionPayloads struct {
	Connecting   desktopwire.ConnectionEvent `json:"connecting"`
	Connected    desktopwire.ConnectionEvent `json:"connected"`
	Disconnected desktopwire.ConnectionEvent `json:"disconnected"`
}

// Document is the complete frontend contract fixture as encoded by production Go wire types.
type Document struct {
	Methods                           MethodMetadata                           `json:"methods"`
	Events                            EventMetadata                            `json:"events"`
	ConnectionPayloads                ConnectionPayloads                       `json:"connection_payloads"`
	Status                            control.DaemonStatus                     `json:"status"`
	Snapshot                          domain.Snapshot                          `json:"snapshot"`
	AddProject                        desktopwire.AddProjectResult             `json:"add_project"`
	ApproveProjectRemoval             control.ProjectUnregistration            `json:"approve_project_removal"`
	ProjectActivity                   control.ProjectActivity                  `json:"project_activity"`
	ServiceLogs                       control.ServiceLogs                      `json:"service_logs"`
	ProjectRuntimeRepairInspection    control.ProjectRuntimeRepairInspection   `json:"project_runtime_repair_inspection"`
	ProjectRuntimeRepairNotActionable control.ProjectRuntimeRepairInspection   `json:"project_runtime_repair_not_actionable"`
	ProjectRuntimeRepairUnsupported   control.ProjectRuntimeRepairInspection   `json:"project_runtime_repair_unsupported"`
	ProjectRuntimeRepairConfirmation  control.ProjectRuntimeRepairConfirmation `json:"project_runtime_repair_confirmation"`
	RemoveProject                     control.ProjectUnregistration            `json:"remove_project"`
	SetupNetwork                      control.NetworkSetupOperation            `json:"setup_network"`
	StartProject                      control.ProjectLifecycleOperation        `json:"start_project"`
	StopProject                       control.ProjectLifecycleOperation        `json:"stop_project"`
	RestartProject                    control.ProjectLifecycleOperation        `json:"restart_project"`
	TerminalOperation                 domain.Operation                         `json:"terminal_operation"`
}

// Fixture returns deterministic status and snapshot data rich enough to exercise every current desktop view.
func Fixture() Document {
	capturedAt := time.Date(2026, time.July, 18, 14, 35, 20, 0, time.UTC)
	operationRequestedAt := time.Date(2026, time.July, 18, 14, 35, 18, 0, time.UTC)
	operationStartedAt := time.Date(2026, time.July, 18, 14, 35, 19, 0, time.UTC)
	terminalRequestedAt := time.Date(2026, time.July, 18, 14, 20, 0, 0, time.UTC)
	terminalStartedAt := time.Date(2026, time.July, 18, 14, 20, 1, 0, time.UTC)
	terminalFinishedAt := time.Date(2026, time.July, 18, 14, 20, 2, 0, time.UTC)
	removeRequestedAt := time.Date(2026, time.July, 18, 14, 40, 0, 0, time.UTC)
	removeStartedAt := time.Date(2026, time.July, 18, 14, 40, 1, 0, time.UTC)
	lifecycleRequestedAt := time.Date(2026, time.July, 18, 14, 41, 0, 0, time.UTC)
	projectOutput := "· Building app: web\n  Built web in 482ms\n✔ migrations complete (0)\n"

	return Document{
		Methods: MethodMetadata{
			AddProject:                  desktopwire.MethodAddProject,
			ApproveProjectRemoval:       desktopwire.MethodApproveProjectRemoval,
			ConfirmProjectRuntimeRepair: desktopwire.MethodConfirmProjectRuntimeRepair,
			InspectProjectRuntimeRepair: desktopwire.MethodInspectProjectRuntimeRepair,
			OpenResource:                desktopwire.MethodOpenResource,
			OpenTerminalURL:             desktopwire.MethodOpenTerminalURL,
			ResourceIconURL:             desktopwire.MethodResourceIconURL,
			ProjectActivity:             desktopwire.MethodProjectActivity,
			ServiceLogs:                 desktopwire.MethodServiceLogs,
			WaitProjectActivity:         desktopwire.MethodWaitProjectActivity,
			WaitServiceLogs:             desktopwire.MethodWaitServiceLogs,
			RemoveProject:               desktopwire.MethodRemoveProject,
			SetupNetwork:                desktopwire.MethodSetupNetwork,
			Snapshot:                    desktopwire.MethodSnapshot,
			StartProject:                desktopwire.MethodStartProject,
			RestartProject:              desktopwire.MethodRestartProject,
			Status:                      desktopwire.MethodStatus,
			StopProject:                 desktopwire.MethodStopProject,
		},
		Events: EventMetadata{
			Connection: desktopwire.ConnectionEventName,
			Snapshot:   desktopwire.SnapshotEventName,
		},
		ConnectionPayloads: ConnectionPayloads{
			Connecting:   desktopwire.ConnectionEvent{State: desktopwire.ConnectionConnecting},
			Connected:    desktopwire.ConnectionEvent{State: desktopwire.ConnectionConnected},
			Disconnected: desktopwire.ConnectionEvent{State: desktopwire.ConnectionDisconnected},
		},
		Status: control.DaemonStatus{
			State:    control.DaemonStateReady,
			Build:    control.Build{Version: "dev", Revision: "fixture"},
			Protocol: rpc.Version{Major: 1, Minor: 0},
			Capabilities: []rpc.Capability{
				control.CapabilityProjectActivityWaitV1,
				control.CapabilityProjectActivityV1,
				control.CapabilityProjectLifecycleV1,
				control.CapabilityProjectRegistrationV1,
				control.CapabilityProjectRestartV1,
				control.CapabilityProjectRuntimeRepairV1,
				control.CapabilityProjectUnregisterApprovalV1,
				control.CapabilityProjectUnregisterV1,
				control.CapabilityServiceLogsWaitV1,
				control.CapabilityServiceLogsV1,
				control.CapabilityV1,
			},
			SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
			Sequence:              42,
		},
		Snapshot: domain.Snapshot{
			SchemaVersion: domain.SnapshotSchemaVersion,
			Sequence:      42,
			CapturedAt:    capturedAt,
			Projects: []domain.ProjectSnapshot{
				{
					ID:        "orders-api",
					Name:      "Orders API",
					Path:      "/workspace/apps/orders-api",
					Slug:      "orders-api",
					State:     domain.ProjectReady,
					Favorite:  true,
					UpdatedAt: time.Date(2026, time.July, 18, 14, 33, 20, 0, time.UTC),
					Apps: []domain.AppSnapshot{
						{ID: "web", Name: "Web", State: domain.EntityReady, Active: true, Required: true},
						{ID: "worker", Name: "Worker", State: domain.EntityReady, Active: true},
					},
					Services: []domain.ServiceSnapshot{
						{ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
						{ID: "redis", Name: "Redis", Kind: "cache", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
					},
					Resources: []domain.ResourceSnapshot{
						{ID: "application", Name: "Application", Kind: "application", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "web"}, URL: "https://orders.test"},
						{ID: "api-reference", Name: "API Reference", Kind: "api-reference", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "web"}, URL: "https://orders.test/swagger"},
					},
				},
				{
					ID:        "billing",
					Name:      "Billing",
					Path:      "/workspace/apps/billing",
					Slug:      "billing",
					State:     domain.ProjectFailed,
					Favorite:  true,
					UpdatedAt: time.Date(2026, time.July, 18, 14, 29, 20, 0, time.UTC),
					Apps: []domain.AppSnapshot{
						{ID: "web", Name: "Web", State: domain.EntityReady, Active: true, Required: true},
					},
					Services: []domain.ServiceSnapshot{
						{ID: "database", Name: "PostgreSQL", Kind: "database", State: domain.EntityFailed, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
					},
					Resources: []domain.ResourceSnapshot{},
				},
				{
					ID:        "storefront",
					Name:      "Storefront",
					Path:      "/workspace/apps/storefront",
					Slug:      "storefront",
					State:     domain.ProjectReady,
					UpdatedAt: time.Date(2026, time.July, 18, 13, 54, 20, 0, time.UTC),
					Apps: []domain.AppSnapshot{
						{ID: "web", Name: "Web", State: domain.EntityReady, Active: true, Required: true},
					},
					Services: []domain.ServiceSnapshot{
						{ID: "mail", Name: "Mailpit", Kind: "mail", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
					},
					Resources: []domain.ResourceSnapshot{
						{ID: "mail", Name: "Mailpit", Kind: "mail", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "mail"}, URL: "https://mail.storefront.test"},
					},
				},
				{
					ID:        "reports",
					Name:      "Reports",
					Path:      "/workspace/apps/reports",
					Slug:      "reports",
					State:     domain.ProjectStopped,
					UpdatedAt: time.Date(2026, time.July, 17, 20, 0, 0, 0, time.UTC),
					Apps: []domain.AppSnapshot{
						{ID: "web", Name: "Web", State: domain.EntityStopped, Required: true},
					},
					Services:  []domain.ServiceSnapshot{},
					Resources: []domain.ResourceSnapshot{},
				},
			},
			Operations: []domain.Operation{
				{
					ID:          "operation-42",
					IntentID:    "intent-42",
					Kind:        "project.reconcile",
					ProjectID:   "orders-api",
					State:       domain.OperationRunning,
					Phase:       "observing",
					RequestedAt: operationRequestedAt,
					StartedAt:   &operationStartedAt,
				},
			},
			RecentResourceIDs: []domain.ResourceRef{
				{ProjectID: "orders-api", ResourceID: "application"},
				{ProjectID: "orders-api", ResourceID: "api-reference"},
				{ProjectID: "storefront", ResourceID: "mail"},
			},
		},
		AddProject: desktopwire.AddProjectResult{
			Registration: &control.ProjectRegistration{
				Project: domain.ProjectSnapshot{
					ID:        "inventory",
					Name:      "Inventory",
					Path:      "/workspace/apps/inventory",
					Slug:      "inventory",
					State:     domain.ProjectStopped,
					UpdatedAt: time.Date(2026, time.July, 18, 14, 36, 0, 0, time.UTC),
					Apps:      []domain.AppSnapshot{},
					Services:  []domain.ServiceSnapshot{},
					Resources: []domain.ResourceSnapshot{},
				},
				Revision: 43,
				Created:  true,
			},
		},
		ProjectActivity: control.ProjectActivity{
			ProjectID: "orders-api",
			Session: &control.ProjectSessionActivity{
				ID:         "session-orders-api",
				State:      domain.SessionAttached,
				Generation: 2,
				Output: control.ProjectOutputChunk{
					Available:  true,
					NextCursor: uint64(len(projectOutput)),
					Text:       projectOutput,
				},
			},
		},
		ServiceLogs: control.ServiceLogs{
			ProjectID: "orders-api",
			ServiceID: "mysql",
			SessionID: "session-orders-api",
			Supported: true,
			Available: true,
			Ports:     []control.ServicePort{{Address: "127.0.0.1", Private: 3306, Public: 3306, Protocol: "tcp", Replica: 1}},
			Output: control.ServiceLogOutputChunk{
				Available:  true,
				NextCursor: 28,
				Text:       "mysql ready for connections\n",
			},
		},
		ProjectRuntimeRepairInspection: fixtureProjectRuntimeRepairInspection(
			control.ProjectRuntimeRepairInspectionConfirmable,
			"",
		),
		ProjectRuntimeRepairNotActionable: fixtureProjectRuntimeRepairInspection(
			control.ProjectRuntimeRepairInspectionNotActionable,
			control.ProjectRuntimeRepairReasonAmbiguous,
		),
		ProjectRuntimeRepairUnsupported: fixtureProjectRuntimeRepairInspection(
			control.ProjectRuntimeRepairInspectionUnsupported,
			"",
		),
		ProjectRuntimeRepairConfirmation: fixtureProjectRuntimeRepairConfirmation(),
		RemoveProject: control.ProjectUnregistration{
			Operation: domain.Operation{
				ID:          "operation-remove-orders",
				IntentID:    "desktop-remove-orders",
				Kind:        domain.OperationKindProjectUnregister,
				ProjectID:   "orders-api",
				State:       domain.OperationRequiresApproval,
				Phase:       "awaiting_host_approval",
				RequestedAt: removeRequestedAt,
				StartedAt:   &removeStartedAt,
			},
			Revision: 44,
		},
		ApproveProjectRemoval: control.ProjectUnregistration{
			Operation: domain.Operation{
				ID:          "operation-remove-orders",
				IntentID:    "desktop-remove-orders",
				Kind:        domain.OperationKindProjectUnregister,
				ProjectID:   "orders-api",
				State:       domain.OperationSucceeded,
				Phase:       "project unregistered",
				RequestedAt: removeRequestedAt,
				StartedAt:   &removeStartedAt,
				FinishedAt:  &lifecycleRequestedAt,
			},
			Revision: 46,
		},
		SetupNetwork: control.NetworkSetupOperation{
			Operation: domain.Operation{
				ID:          "operation-network-setup",
				IntentID:    "intent-network-setup",
				Kind:        domain.OperationKindNetworkSetup,
				State:       domain.OperationSucceeded,
				Phase:       string(domain.OperationSucceeded),
				RequestedAt: removeRequestedAt,
				StartedAt:   &removeStartedAt,
				FinishedAt:  &lifecycleRequestedAt,
			},
			Revision: 44,
		},
		StartProject: control.ProjectLifecycleOperation{
			Operation: domain.Operation{
				ID:          "operation-start-reports",
				IntentID:    "desktop-project-start-reports",
				Kind:        domain.OperationKindProjectStart,
				ProjectID:   "reports",
				State:       domain.OperationQueued,
				Phase:       string(domain.OperationQueued),
				RequestedAt: lifecycleRequestedAt,
			},
			Revision: 45,
		},
		StopProject: control.ProjectLifecycleOperation{
			Operation: domain.Operation{
				ID:          "operation-stop-orders",
				IntentID:    "desktop-project-stop-orders-api",
				Kind:        domain.OperationKindProjectStop,
				ProjectID:   "orders-api",
				State:       domain.OperationQueued,
				Phase:       string(domain.OperationQueued),
				RequestedAt: lifecycleRequestedAt,
			},
			Revision: 45,
		},
		RestartProject: control.ProjectLifecycleOperation{
			Operation: domain.Operation{
				ID:          "operation-restart-billing",
				IntentID:    "desktop-project-restart-billing",
				Kind:        domain.OperationKindProjectRestart,
				ProjectID:   "billing",
				State:       domain.OperationQueued,
				Phase:       string(domain.OperationQueued),
				RequestedAt: lifecycleRequestedAt,
			},
			Revision: 45,
		},
		TerminalOperation: domain.Operation{
			ID:          "operation-terminal",
			IntentID:    "intent-terminal",
			Kind:        "project.reconcile",
			ProjectID:   "billing",
			State:       domain.OperationFailed,
			Phase:       "failed",
			Problem:     &domain.Problem{Code: "service_unavailable", Message: "PostgreSQL did not become ready.", Retryable: true},
			RequestedAt: terminalRequestedAt,
			StartedAt:   &terminalStartedAt,
			FinishedAt:  &terminalFinishedAt,
		},
	}
}

// fixtureProjectRuntimeRepairInspection returns one valid example for each reviewed tagged result shape.
func fixtureProjectRuntimeRepairInspection(
	disposition control.ProjectRuntimeRepairInspectionDisposition,
	reason control.ProjectRuntimeRepairNotActionableReason,
) control.ProjectRuntimeRepairInspection {
	inspection := control.ProjectRuntimeRepairInspection{
		ProjectID:   "billing",
		Disposition: disposition,
		Reason:      reason,
	}
	if disposition == control.ProjectRuntimeRepairInspectionConfirmable {
		inspection.Confirmable = &control.ProjectRuntimeRepairConfirmable{
			Candidate: control.ProjectRuntimeRepairDisplayFacts{
				Command:     "forj dev",
				Checkout:    "/workspace/apps/billing",
				Endpoint:    "127.77.0.12:3000",
				RootPID:     4217,
				MemberCount: 3,
			},
			InspectionID:         control.ProjectRuntimeRepairInspectionID(strings.Repeat("a", 64)),
			CandidateFingerprint: control.ProjectRuntimeRepairCandidateFingerprint(strings.Repeat("b", 64)),
			ExpiresAt:            time.Date(2099, time.July, 18, 14, 37, 20, 0, time.UTC),
		}
	}
	return inspection
}

// fixtureProjectRuntimeRepairConfirmation returns the authoritative stopped project projected after repair.
func fixtureProjectRuntimeRepairConfirmation() control.ProjectRuntimeRepairConfirmation {
	return control.ProjectRuntimeRepairConfirmation{
		Project: domain.ProjectSnapshot{
			ID:        "billing",
			Name:      "Billing",
			Path:      "/workspace/apps/billing",
			Slug:      "billing",
			State:     domain.ProjectStopped,
			UpdatedAt: time.Date(2026, time.July, 18, 14, 42, 0, 0, time.UTC),
			Apps: []domain.AppSnapshot{
				{ID: "web", Name: "Web", State: domain.EntityStopped, Required: true},
			},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 46,
	}
}

// Validate proves every generated example is legal before TypeScript sees the artifact.
func (document Document) Validate() error {
	methods := map[string]string{
		desktopwire.MethodAddProject:                  document.Methods.AddProject,
		desktopwire.MethodApproveProjectRemoval:       document.Methods.ApproveProjectRemoval,
		desktopwire.MethodConfirmProjectRuntimeRepair: document.Methods.ConfirmProjectRuntimeRepair,
		desktopwire.MethodInspectProjectRuntimeRepair: document.Methods.InspectProjectRuntimeRepair,
		desktopwire.MethodOpenResource:                document.Methods.OpenResource,
		desktopwire.MethodOpenTerminalURL:             document.Methods.OpenTerminalURL,
		desktopwire.MethodResourceIconURL:             document.Methods.ResourceIconURL,
		desktopwire.MethodProjectActivity:             document.Methods.ProjectActivity,
		desktopwire.MethodServiceLogs:                 document.Methods.ServiceLogs,
		desktopwire.MethodWaitProjectActivity:         document.Methods.WaitProjectActivity,
		desktopwire.MethodWaitServiceLogs:             document.Methods.WaitServiceLogs,
		desktopwire.MethodRemoveProject:               document.Methods.RemoveProject,
		desktopwire.MethodSetupNetwork:                document.Methods.SetupNetwork,
		desktopwire.MethodSnapshot:                    document.Methods.Snapshot,
		desktopwire.MethodStartProject:                document.Methods.StartProject,
		desktopwire.MethodRestartProject:              document.Methods.RestartProject,
		desktopwire.MethodStatus:                      document.Methods.Status,
		desktopwire.MethodStopProject:                 document.Methods.StopProject,
	}
	contracts := desktopwire.MethodContracts()
	if len(methods) != len(contracts) {
		return fmt.Errorf("fixture declares %d Wails methods for %d Go contracts", len(methods), len(contracts))
	}
	for _, contract := range contracts {
		if methods[contract.Name] != contract.Name {
			return fmt.Errorf("fixture method %q does not match its Go contract", contract.Name)
		}
	}

	events := map[string]string{
		desktopwire.ConnectionEventName: document.Events.Connection,
		desktopwire.SnapshotEventName:   document.Events.Snapshot,
	}
	eventContracts := desktopwire.EventContracts()
	if len(events) != len(eventContracts) {
		return fmt.Errorf("fixture declares %d Wails events for %d Go contracts", len(events), len(eventContracts))
	}
	for _, contract := range eventContracts {
		if events[contract.Name] != contract.Name {
			return fmt.Errorf("fixture event %q does not match its Go contract", contract.Name)
		}
	}

	for name, fixture := range map[string]struct {
		event desktopwire.ConnectionEvent
		state desktopwire.ConnectionState
	}{
		"connecting":   {event: document.ConnectionPayloads.Connecting, state: desktopwire.ConnectionConnecting},
		"connected":    {event: document.ConnectionPayloads.Connected, state: desktopwire.ConnectionConnected},
		"disconnected": {event: document.ConnectionPayloads.Disconnected, state: desktopwire.ConnectionDisconnected},
	} {
		if err := fixture.event.Validate(); err != nil {
			return fmt.Errorf("validate %s fixture connection: %w", name, err)
		}
		if fixture.event.State != fixture.state {
			return fmt.Errorf("%s fixture connection state = %q, want %q", name, fixture.event.State, fixture.state)
		}
	}
	if err := document.Status.Validate(); err != nil {
		return fmt.Errorf("validate fixture status: %w", err)
	}
	if err := document.Snapshot.Validate(); err != nil {
		return fmt.Errorf("validate fixture snapshot: %w", err)
	}
	if err := document.AddProject.Validate(); err != nil {
		return fmt.Errorf("validate fixture project registration: %w", err)
	}
	if err := document.ProjectActivity.Validate(); err != nil {
		return fmt.Errorf("validate fixture project activity: %w", err)
	}
	if err := document.ServiceLogs.Validate(); err != nil {
		return fmt.Errorf("validate fixture service logs: %w", err)
	}
	if err := document.ProjectRuntimeRepairInspection.Validate(); err != nil {
		return fmt.Errorf("validate fixture project runtime repair inspection: %w", err)
	}
	if err := document.ProjectRuntimeRepairNotActionable.Validate(); err != nil {
		return fmt.Errorf("validate fixture non-actionable project runtime repair inspection: %w", err)
	}
	if err := document.ProjectRuntimeRepairUnsupported.Validate(); err != nil {
		return fmt.Errorf("validate fixture unsupported project runtime repair inspection: %w", err)
	}
	if err := document.ProjectRuntimeRepairConfirmation.Validate(); err != nil {
		return fmt.Errorf("validate fixture project runtime repair confirmation: %w", err)
	}
	if document.ProjectRuntimeRepairInspection.ProjectID != document.ProjectRuntimeRepairConfirmation.Project.ID {
		return fmt.Errorf("fixture project runtime repair inspection and confirmation belong to different projects")
	}
	if err := document.RemoveProject.Validate(); err != nil {
		return fmt.Errorf("validate fixture project removal: %w", err)
	}
	if err := document.ApproveProjectRemoval.Validate(); err != nil {
		return fmt.Errorf("validate fixture approved project removal: %w", err)
	}
	if document.RemoveProject.Operation.State != domain.OperationRequiresApproval ||
		document.ApproveProjectRemoval.Operation.State != domain.OperationSucceeded ||
		document.RemoveProject.Operation.ID != document.ApproveProjectRemoval.Operation.ID ||
		document.RemoveProject.Operation.ProjectID != document.ApproveProjectRemoval.Operation.ProjectID ||
		document.RemoveProject.Operation.IntentID != document.ApproveProjectRemoval.Operation.IntentID ||
		document.ApproveProjectRemoval.Revision <= document.RemoveProject.Revision {
		return errors.New("fixture approved project removal must complete the exact pending operation at a later revision")
	}
	if err := document.SetupNetwork.Validate(); err != nil {
		return fmt.Errorf("validate fixture network setup: %w", err)
	}
	if err := document.StartProject.Validate(); err != nil {
		return fmt.Errorf("validate fixture project start: %w", err)
	}
	if err := document.StopProject.Validate(); err != nil {
		return fmt.Errorf("validate fixture project stop: %w", err)
	}
	if err := document.RestartProject.Validate(); err != nil {
		return fmt.Errorf("validate fixture project restart: %w", err)
	}
	if err := document.TerminalOperation.Validate(); err != nil {
		return fmt.Errorf("validate fixture terminal operation: %w", err)
	}
	return nil
}

// TypeScript validates and emits the checked-in artifact that TypeScript verifies with satisfies.
func TypeScript() ([]byte, error) {
	document := Fixture()
	if err := document.Validate(); err != nil {
		return nil, err
	}

	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode fixture: %w", err)
	}

	declarations, err := typeScriptDeclarations()
	if err != nil {
		return nil, err
	}

	generated := []byte("// Code generated by go generate; DO NOT EDIT.\n\nimport type { AddProjectResult, ConnectionEvent, DaemonStatus, HarborSnapshot, NetworkSetupOperation, ProjectActivity, ProjectLifecycleOperation, ProjectRuntimeRepairConfirmation, ProjectRuntimeRepairInspection, ProjectUnregistration, ServiceLogs } from '@/domain/harbor'\nimport type { HarborWireFixture } from './types'\n\n")
	generated = append(generated, declarations...)
	generated = append(generated, []byte("\nexport const harborWireFixture = ")...)
	generated = append(generated, payload...)
	generated = append(generated, []byte(" satisfies HarborWireFixture\n")...)
	return generated, nil
}

// typeScriptDeclarations renders the narrow native method and event surface directly from Go reflection.
func typeScriptDeclarations() ([]byte, error) {
	var generated strings.Builder
	generated.WriteString("export interface WailsAppBindings {\n")
	for _, contract := range desktopwire.MethodContracts() {
		declaration, err := typeScriptMethodDeclaration(contract)
		if err != nil {
			return nil, err
		}
		generated.WriteString(declaration)
	}
	generated.WriteString("}\n\n")

	generated.WriteString("export interface WailsEventPayloads {\n")
	for _, contract := range desktopwire.EventContracts() {
		declaration, err := typeScriptEventDeclaration(contract)
		if err != nil {
			return nil, err
		}
		generated.WriteString(declaration)
	}
	generated.WriteString("}\n\n")
	generated.WriteString("export type WailsEventName = keyof WailsEventPayloads\n\n")
	generated.WriteString("export interface WailsRuntimeEvents {\n")
	generated.WriteString("  EventsOff(eventName: WailsEventName): void\n")
	generated.WriteString("  EventsOn<Name extends WailsEventName>(eventName: Name, callback: (payload: WailsEventPayloads[Name]) => void): () => void\n")
	generated.WriteString("}\n")

	return []byte(generated.String()), nil
}

// typeScriptMethodDeclaration renders one reflected method only after its entire signature is supported.
func typeScriptMethodDeclaration(contract desktopwire.MethodContract) (string, error) {
	if contract.Signature.Kind() != reflect.Func {
		return "", fmt.Errorf("Wails method %s has non-function signature %s", contract.Name, contract.Signature)
	}
	if contract.Signature.NumIn() != len(contract.ParameterNames) {
		return "", fmt.Errorf("Wails method %s has %d parameters but %d TypeScript labels", contract.Name, contract.Signature.NumIn(), len(contract.ParameterNames))
	}

	parameters := make([]string, 0, contract.Signature.NumIn())
	for index, name := range contract.ParameterNames {
		parameterType, err := typeScriptType(contract.Signature.In(index))
		if err != nil {
			return "", fmt.Errorf("Wails method %s parameter %s: %w", contract.Name, name, err)
		}
		parameters = append(parameters, fmt.Sprintf("%s: %s", name, parameterType))
	}

	result, err := typeScriptMethodResult(contract)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("  %s(%s): Promise<%s>\n", contract.Name, strings.Join(parameters, ", "), result), nil
}

// typeScriptEventDeclaration renders one event name and its exact reviewed payload type as a single mapping entry.
func typeScriptEventDeclaration(contract desktopwire.EventContract) (string, error) {
	payloadType, err := typeScriptType(contract.Payload)
	if err != nil {
		return "", fmt.Errorf("Wails event %s payload: %w", contract.Name, err)
	}
	return fmt.Sprintf("  %q: %s\n", contract.Name, payloadType), nil
}

// typeScriptMethodResult translates Harbor's error-returning Go method convention into a Wails promise result.
func typeScriptMethodResult(contract desktopwire.MethodContract) (string, error) {
	signature := contract.Signature
	if signature.NumOut() < 1 || signature.NumOut() > 2 {
		return "", fmt.Errorf("Wails method %s must return error or one value plus error", contract.Name)
	}
	if signature.Out(signature.NumOut()-1) != reflect.TypeFor[error]() {
		return "", fmt.Errorf("Wails method %s final result must be error", contract.Name)
	}
	if signature.NumOut() == 1 {
		return "void", nil
	}

	result, err := typeScriptType(signature.Out(0))
	if err != nil {
		return "", fmt.Errorf("Wails method %s result: %w", contract.Name, err)
	}
	return result, nil
}

// typeScriptType maps only the reviewed Go wire types allowed to cross the desktop boundary.
func typeScriptType(goType reflect.Type) (string, error) {
	switch goType {
	case reflect.TypeFor[string]():
		return "string", nil
	case reflect.TypeFor[uint64]():
		return "number", nil
	case reflect.TypeFor[desktopwire.AddProjectResult]():
		return "AddProjectResult", nil
	case reflect.TypeFor[desktopwire.ConnectionEvent]():
		return "ConnectionEvent", nil
	case reflect.TypeFor[control.DaemonStatus]():
		return "DaemonStatus", nil
	case reflect.TypeFor[control.ProjectUnregistration]():
		return "ProjectUnregistration", nil
	case reflect.TypeFor[control.ProjectActivity]():
		return "ProjectActivity", nil
	case reflect.TypeFor[control.ServiceLogs]():
		return "ServiceLogs", nil
	case reflect.TypeFor[control.NetworkSetupOperation]():
		return "NetworkSetupOperation", nil
	case reflect.TypeFor[control.ProjectLifecycleOperation]():
		return "ProjectLifecycleOperation", nil
	case reflect.TypeFor[control.ProjectRuntimeRepairInspection]():
		return "ProjectRuntimeRepairInspection", nil
	case reflect.TypeFor[control.ProjectRuntimeRepairConfirmation]():
		return "ProjectRuntimeRepairConfirmation", nil
	case reflect.TypeFor[domain.Snapshot]():
		return "HarborSnapshot", nil
	default:
		return "", fmt.Errorf("unsupported Go wire type %s", goType)
	}
}
