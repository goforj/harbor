package desktopwire

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// TestAddProjectResultValidateRequiresExactlyOneOutcome keeps picker cancellation distinct from registration failure.
func TestAddProjectResultValidateRequiresExactlyOneOutcome(t *testing.T) {
	t.Parallel()

	registration := control.ProjectRegistration{
		Project: domain.ProjectSnapshot{
			ID:        "orders",
			Name:      "Orders",
			Path:      "/workspace/orders",
			Slug:      "orders",
			State:     domain.ProjectStopped,
			UpdatedAt: time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
			Apps:      []domain.AppSnapshot{},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 1,
		Created:  true,
	}
	tests := []struct {
		name   string
		result AddProjectResult
		want   string
	}{
		{name: "canceled", result: AddProjectResult{Canceled: true}},
		{name: "registered", result: AddProjectResult{Registration: &registration}},
		{name: "both", result: AddProjectResult{Canceled: true, Registration: &registration}, want: "must not contain"},
		{name: "neither", result: AddProjectResult{}, want: "must contain"},
		{name: "invalid registration", result: AddProjectResult{Registration: &control.ProjectRegistration{}}, want: "project ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.result.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectTerminalEventValidateRejectsMixedOrMalformedPayloads keeps PTY output and exit frames unambiguous.
func TestProjectTerminalEventValidateRejectsMixedOrMalformedPayloads(t *testing.T) {
	t.Parallel()

	output := base64.StdEncoding.EncodeToString([]byte("ready\r\n"))
	sessionID := projectTerminalSessionIDPrefix + strings.Repeat("a", projectTerminalSessionIDBytes*2)
	tests := []struct {
		name  string
		event ProjectTerminalEvent
		want  string
	}{
		{
			name: "output",
			event: ProjectTerminalEvent{
				SessionID:  sessionID,
				Kind:       ProjectTerminalOutput,
				DataBase64: output,
			},
		},
		{
			name: "exit",
			event: ProjectTerminalEvent{
				SessionID: sessionID,
				Kind:      ProjectTerminalExited,
			},
		},
		{
			name: "missing session",
			event: ProjectTerminalEvent{
				Kind:       ProjectTerminalOutput,
				DataBase64: output,
			},
			want: "session ID",
		},
		{
			name: "malformed output",
			event: ProjectTerminalEvent{
				SessionID:  sessionID,
				Kind:       ProjectTerminalOutput,
				DataBase64: "not base64",
			},
			want: "decode",
		},
		{
			name: "oversized output",
			event: ProjectTerminalEvent{
				SessionID:  sessionID,
				Kind:       ProjectTerminalOutput,
				DataBase64: base64.StdEncoding.EncodeToString(make([]byte, projectTerminalOutputBytes+1)),
			},
			want: "exceeds",
		},
		{
			name: "mixed output",
			event: ProjectTerminalEvent{
				SessionID:  sessionID,
				Kind:       ProjectTerminalOutput,
				DataBase64: output,
				Error:      "exited",
			},
			want: "must not contain",
		},
		{
			name: "mixed exit",
			event: ProjectTerminalEvent{
				SessionID:  sessionID,
				Kind:       ProjectTerminalExited,
				DataBase64: output,
			},
			want: "must not contain",
		},
		{
			name: "unknown kind",
			event: ProjectTerminalEvent{
				SessionID: sessionID,
				Kind:      "other",
			},
			want: "unknown",
		},
		{
			name: "oversized exit error",
			event: ProjectTerminalEvent{
				SessionID: sessionID,
				Kind:      ProjectTerminalExited,
				Error:     strings.Repeat("x", projectTerminalErrorBytes+1),
			},
			want: "exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.event.Validate()
			if test.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectTerminalStartedValidateRequiresOpaqueIdentity keeps process details out of the start response.
func TestProjectTerminalStartedValidateRequiresOpaqueIdentity(t *testing.T) {
	t.Parallel()

	if err := (ProjectTerminalStarted{}).Validate(); err == nil {
		t.Fatal("Validate() missing session ID error = nil")
	}
	sessionID := projectTerminalSessionIDPrefix + strings.Repeat("a", projectTerminalSessionIDBytes*2)
	if err := (ProjectTerminalStarted{SessionID: sessionID}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestEmitterMethodsMatchEventContracts prevents an event name, payload, or typed emission method from drifting independently.
func TestEmitterMethodsMatchEventContracts(t *testing.T) {
	t.Parallel()

	emitterType := reflect.TypeFor[Emitter]()
	contracts := EventContracts()
	if emitterType.NumMethod() != len(contracts) {
		t.Fatalf("Emitter exported method count = %d, want %d", emitterType.NumMethod(), len(contracts))
	}
	for _, contract := range contracts {
		method, exists := emitterType.MethodByName(contract.EmitterMethod)
		if !exists {
			t.Errorf("Emitter method %q does not exist for event %q", contract.EmitterMethod, contract.Name)
			continue
		}
		if method.Type.NumIn() != 3 {
			t.Errorf("Emitter.%s input count = %d, want receiver, context, payload", contract.EmitterMethod, method.Type.NumIn())
			continue
		}
		if method.Type.In(1) != reflect.TypeFor[context.Context]() {
			t.Errorf("Emitter.%s context type = %s, want context.Context", contract.EmitterMethod, method.Type.In(1))
		}
		if method.Type.In(2) != contract.Payload {
			t.Errorf("Emitter.%s payload type = %s, want %s", contract.EmitterMethod, method.Type.In(2), contract.Payload)
		}
		if method.Type.NumOut() != 0 {
			t.Errorf("Emitter.%s output count = %d, want 0", contract.EmitterMethod, method.Type.NumOut())
		}
	}
}

// TestEmitterPublishesDeclaredNamePayloadPairs proves the typed methods preserve both halves of each runtime event contract.
func TestEmitterPublishesDeclaredNamePayloadPairs(t *testing.T) {
	t.Parallel()

	type emission struct {
		name    string
		payload interface{}
	}
	var emissions []emission
	emitter := NewEmitter(func(_ context.Context, name string, payloads ...interface{}) {
		if len(payloads) != 1 {
			t.Fatalf("event %q payload count = %d, want 1", name, len(payloads))
		}
		emissions = append(emissions, emission{name: name, payload: payloads[0]})
	})

	connection := ConnectionEvent{State: ConnectionConnected}
	snapshot := domain.Snapshot{SchemaVersion: domain.SnapshotSchemaVersion, Sequence: 1}
	terminal := ProjectTerminalEvent{
		SessionID:  projectTerminalSessionIDPrefix + strings.Repeat("a", projectTerminalSessionIDBytes*2),
		Kind:       ProjectTerminalOutput,
		DataBase64: base64.StdEncoding.EncodeToString([]byte("ready\r\n")),
	}
	emitter.Connection(context.Background(), connection)
	emitter.Snapshot(context.Background(), snapshot)
	emitter.ProjectTerminal(context.Background(), terminal)

	want := []emission{
		{name: ConnectionEventName, payload: connection},
		{name: SnapshotEventName, payload: snapshot},
		{name: ProjectTerminalEventName, payload: terminal},
	}
	if !reflect.DeepEqual(emissions, want) {
		t.Fatalf("emissions = %#v, want %#v", emissions, want)
	}
}
