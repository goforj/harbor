package wirefixture

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/internal/domain"
)

// TestTypeScriptMethodDeclarationRejectsSignatureDrift covers every unsupported change before it can reach the checked-in bridge contract.
func TestTypeScriptMethodDeclarationRejectsSignatureDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contract desktopwire.MethodContract
		want     string
	}{
		{
			name:     "non function",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeFor[string]()},
			want:     "non-function signature",
		},
		{
			name:     "parameter label count",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeOf(func(string) error { return nil })},
			want:     "1 parameters but 0 TypeScript labels",
		},
		{
			name:     "unsupported parameter",
			contract: desktopwire.MethodContract{Name: "Broken", ParameterNames: []string{"value"}, Signature: reflect.TypeOf(func(int) error { return nil })},
			want:     "parameter value: unsupported Go wire type int",
		},
		{
			name:     "missing error",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeOf(func() {})},
			want:     "must return error or one value plus error",
		},
		{
			name:     "too many results",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeOf(func() (string, string, error) { return "", "", nil })},
			want:     "must return error or one value plus error",
		},
		{
			name:     "final result",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeOf(func() string { return "" })},
			want:     "final result must be error",
		},
		{
			name:     "unsupported result",
			contract: desktopwire.MethodContract{Name: "Broken", Signature: reflect.TypeOf(func() (int, error) { return 0, nil })},
			want:     "result: unsupported Go wire type int",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := typeScriptMethodDeclaration(test.contract)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("typeScriptMethodDeclaration() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestTypeScriptMethodDeclarationPreservesExactShape proves the supported void and typed-result conventions remain explicit.
func TestTypeScriptMethodDeclarationPreservesExactShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contract desktopwire.MethodContract
		want     string
	}{
		{
			name: "void",
			contract: desktopwire.MethodContract{
				Name:           "Open",
				ParameterNames: []string{"projectId", "resourceId"},
				Signature:      reflect.TypeOf(func(string, string) error { return nil }),
			},
			want: "  Open(projectId: string, resourceId: string): Promise<void>\n",
		},
		{
			name: "typed result",
			contract: desktopwire.MethodContract{
				Name:      desktopwire.MethodSnapshot,
				Signature: reflect.TypeOf(func() (domain.Snapshot, error) { return domain.Snapshot{}, nil }),
			},
			want: "  Snapshot(): Promise<HarborSnapshot>\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			declaration, err := typeScriptMethodDeclaration(test.contract)
			if err != nil {
				t.Fatalf("typeScriptMethodDeclaration() error = %v", err)
			}
			if declaration != test.want {
				t.Fatalf("declaration = %q, want %q", declaration, test.want)
			}
		})
	}
}

// TestTypeScriptEventDeclarationRejectsUnknownPayload prevents an unreviewed event payload from degrading to an untyped callback.
func TestTypeScriptEventDeclarationRejectsUnknownPayload(t *testing.T) {
	t.Parallel()

	_, err := typeScriptEventDeclaration(desktopwire.EventContract{Name: "harbor:unknown", Payload: reflect.TypeFor[int]()})
	if err == nil || !strings.Contains(err.Error(), "unsupported Go wire type int") {
		t.Fatalf("typeScriptEventDeclaration() error = %v, want unsupported payload", err)
	}
}

// TestDocumentValidateRejectsContractMetadataDrift keeps fixture metadata tied to the authoritative method and event names.
func TestDocumentValidateRejectsContractMetadataDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Document)
		want   string
	}{
		{
			name: "method",
			mutate: func(document *Document) {
				document.Methods.Status = "DaemonStatus"
			},
			want: "fixture method",
		},
		{
			name: "event",
			mutate: func(document *Document) {
				document.Events.Snapshot = "harbor:state"
			},
			want: "fixture event",
		},
		{
			name: "connection variant",
			mutate: func(document *Document) {
				document.ConnectionPayloads.Connecting.State = desktopwire.ConnectionConnected
			},
			want: "connecting fixture connection state",
		},
		{
			name: "approved project removal",
			mutate: func(document *Document) {
				document.ApproveProjectRemoval.Operation.IntentID = "desktop-remove-other"
			},
			want: "exact pending operation",
		},
		{
			name: "service logs",
			mutate: func(document *Document) {
				document.ServiceLogs.ServiceID = ""
			},
			want: "validate fixture service logs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			document := Fixture()
			test.mutate(&document)
			err := document.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Document.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
