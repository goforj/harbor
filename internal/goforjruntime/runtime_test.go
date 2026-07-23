package goforjruntime

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectruntime"
)

// goForjRuntimeRepairerFixture records the native target selected by the neutral repair adapter.
type goForjRuntimeRepairerFixture struct {
	targets []projectprocess.RuntimeRepairTarget
}

// Inspect records the exact native target and reports a listener that disappeared during observation.
func (fixture *goForjRuntimeRepairerFixture) Inspect(_ context.Context, target projectprocess.RuntimeRepairTarget) (projectprocess.UnattributedRuntimeInspection, error) {
	fixture.targets = append(fixture.targets, target)
	return projectprocess.UnattributedRuntimeInspection{
		State:      projectprocess.RuntimeRepairInspectionMissing,
		Diagnostic: projectprocess.RuntimeRepairDiagnosticListenerMissing,
	}, nil
}

// Confirm is unreachable when inspection reports an already-missing listener.
func (*goForjRuntimeRepairerFixture) Confirm(context.Context, projectprocess.UnattributedRuntimeCandidate) (projectprocess.RuntimeRepairConfirmation, error) {
	return projectprocess.RuntimeRepairConfirmation{}, errors.New("unexpected listener confirmation")
}

// TestRuntimeRepairListenerTranslatesNeutralTarget proves GoForj-specific repair remains behind the optional runtime capability.
func TestRuntimeRepairListenerTranslatesNeutralTarget(t *testing.T) {
	repairer := &goForjRuntimeRepairerFixture{}
	runtime := &Runtime{runtimeRepairer: repairer}
	result, err := runtime.RepairListener(t.Context(), projectruntime.ListenerRepairRequest{
		CheckoutRoot: "/test/orders",
		Endpoint:     netip.MustParseAddrPort("127.77.0.11:3000"),
	})
	if err != nil || !result.Settled {
		t.Fatalf("RepairListener() = %#v, %v, want settled missing listener", result, err)
	}
	if len(repairer.targets) != 1 || repairer.targets[0].CheckoutRoot != "/test/orders" ||
		repairer.targets[0].Endpoint != netip.MustParseAddrPort("127.77.0.11:3000") {
		t.Fatalf("native repair targets = %#v, want one exact checkout endpoint", repairer.targets)
	}
}

// TestRuntimeRepairListenerRequiresConfirmationForMissingListener proves process-backed recovery remains fail-closed without a signal-backed settlement.
func TestRuntimeRepairListenerRequiresConfirmationForMissingListener(t *testing.T) {
	runtime := &Runtime{runtimeRepairer: &goForjRuntimeRepairerFixture{}}
	result, err := runtime.RepairListener(t.Context(), projectruntime.ListenerRepairRequest{
		CheckoutRoot:        "/test/orders",
		Endpoint:            netip.MustParseAddrPort("127.77.0.11:3000"),
		RequireConfirmation: true,
	})
	if err != nil || result.Settled {
		t.Fatalf("RepairListener() = %#v, %v, want unresolved missing listener", result, err)
	}
}

// TestGoForjEnvironmentOverridesKeepsProviderKeysInsideTheAdapter proves core network facts are translated only at the GoForj boundary.
func TestGoForjEnvironmentOverridesKeepsProviderKeysInsideTheAdapter(t *testing.T) {
	assignment := projectruntime.NetworkAssignment{
		Address:     netip.MustParseAddr("127.77.0.18"),
		PrimaryPort: 3000,
	}
	overrides := goForjEnvironmentOverrides(assignment)
	want := map[string]string{
		"API_HTTP_HOST":          "127.77.0.18",
		"DEV_SERVICE_IP_ADDRESS": "127.77.0.18",
		"IP_ADDRESS":             "127.77.0.18",
		"LIGHTHOUSE_URL":         "ws://127.77.0.18:3000/lighthouse/ws/agent",
	}
	if len(overrides) != len(want) {
		t.Fatalf("GoForj environment override count = %d, want %d", len(overrides), len(want))
	}
	for key, value := range want {
		if overrides[key] != value {
			t.Fatalf("GoForj environment override %q = %q, want %q", key, overrides[key], value)
		}
	}
}

// TestAdmittedResourcesKeepsRawGoForjOwnershipAtTheAdapter verifies lifecycle receives only validated snapshots.
func TestAdmittedResourcesKeepsRawGoForjOwnershipAtTheAdapter(t *testing.T) {
	plan := projectruntime.Plan{NetworkAssignment: projectruntime.NetworkAssignment{Address: netip.MustParseAddr("127.77.4.8")}, Presentation: projectruntime.Presentation{AppID: "app", ResourceURL: "http://127.77.4.8:3000"}}
	resources := admittedResources(projectruntime.ResourceObservationRequest{
		Plan:     plan,
		Services: []domain.ServiceSnapshot{{ID: "mailpit"}},
	}, []projectprocess.FrameworkResource{
		{ID: "docs", Name: "Docs", Kind: "documentation", URL: "http://127.77.4.8:3000/docs", App: "app"},
		{ID: "mailpit", Name: "Mailpit", Kind: "mail", URL: "http://127.77.4.8:8025", Service: "mailpit"},
		{ID: "unknown", Name: "Unknown", Kind: "mail", URL: "http://127.77.4.8:8026", Service: "unknown"},
		{ID: "foreign", Name: "Foreign", Kind: "documentation", URL: "http://127.77.4.9:3000/docs", App: "app"},
		{ID: "app-http", Name: "Duplicate", Kind: "application", URL: "http://127.77.4.8:3000", App: "app"},
	})
	if len(resources) != 2 || resources[0].ID != "docs" || resources[1].ID != "mailpit" {
		t.Fatalf("admitted resources = %#v", resources)
	}
	if resources[0].Owner.AppID != "app" || resources[1].Owner.ServiceID != "mailpit" {
		t.Fatalf("resource owners = %#v", resources)
	}
}

// TestEquivalentHTTPResourceURLPreservesPrimaryDeduplication verifies only the historic root-path normalization applies.
func TestEquivalentHTTPResourceURLPreservesPrimaryDeduplication(t *testing.T) {
	if !equivalentHTTPResourceURL("HTTP://127.77.4.8:3000/", "http://127.77.4.8:3000") {
		t.Fatal("equivalentHTTPResourceURL() rejected the primary root resource")
	}
	if equivalentHTTPResourceURL("http://127.77.4.8:3000/docs", "http://127.77.4.8:3000") {
		t.Fatal("equivalentHTTPResourceURL() accepted a distinct path")
	}
}

// TestGoForjPreparationErrorPreservesActionableProblems proves provider diagnostics cross the neutral admission boundary safely.
func TestGoForjPreparationErrorPreservesActionableProblems(t *testing.T) {
	tests := []struct {
		name      string
		input     error
		wantCode  string
		retryable bool
	}{
		{
			name:      "render update",
			input:     &projectdiscovery.RenderUpdateRequiredError{},
			wantCode:  "project.render.update_required",
			retryable: true,
		},
		{
			name:      "invalid project",
			input:     &projectdiscovery.InvalidProjectError{},
			wantCode:  "project.runtime.invalid",
			retryable: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := goForjPreparationError(test.input)
			var preparation *projectruntime.PreparationError
			if !errors.As(err, &preparation) {
				t.Fatalf("GoForj preparation error = %T %v, want neutral PreparationError", err, err)
			}
			if string(preparation.Problem.Code) != test.wantCode || preparation.Problem.Retryable != test.retryable {
				t.Fatalf("GoForj preparation problem = %#v, want code %q retryable %t", preparation.Problem, test.wantCode, test.retryable)
			}
			if !errors.Is(err, test.input) {
				t.Fatalf("GoForj preparation error no longer wraps %T", test.input)
			}
		})
	}
	sentinel := errors.New("host discovery failed")
	if goForjPreparationError(sentinel) != sentinel {
		t.Fatal("unclassified preparation error identity changed")
	}
}

// TestTranslateRuntimeErrorPreservesCleanupUncertainty proves lifecycle recovery can recognize adapter cleanup failures.
func TestTranslateRuntimeErrorPreservesCleanupUncertainty(t *testing.T) {
	translated := translateRuntimeError(projectprocess.ErrCleanupUncertain)
	if !errors.Is(translated, projectruntime.ErrCleanupUncertain) {
		t.Fatalf("translated cleanup error = %v, want project runtime cleanup uncertainty", translated)
	}
	translated = translateRuntimeError(projectprocess.ErrNotRunning)
	if !errors.Is(translated, projectruntime.ErrNotRunning) {
		t.Fatalf("translated not-running error = %v, want project runtime not-running state", translated)
	}
	sentinel := errors.New("ordinary runtime failure")
	if translateRuntimeError(sentinel) != sentinel {
		t.Fatal("ordinary runtime error identity changed across the adapter")
	}
}

// TestRuntimeExitPreservesLifecycleFacts proves the adapter does not lose process completion evidence.
func TestRuntimeExitPreservesLifecycleFacts(t *testing.T) {
	exitErr := errors.New("process failed")
	settlementErr := errors.New("scope unsettled")
	exitedAt := time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC)
	got := runtimeExit(projectprocess.Exit{
		ExitCode:           17,
		Err:                exitErr,
		ScopeSettlementErr: settlementErr,
		StopRequested:      true,
		DroppedOutputLines: 23,
		ExitedAt:           exitedAt,
	})
	if got.ExitCode != 17 || got.Err != exitErr || got.ScopeSettlementErr != settlementErr ||
		!got.StopRequested || got.DroppedOutputLines != 23 || !got.ExitedAt.Equal(exitedAt) {
		t.Fatalf("translated runtime exit = %#v", got)
	}
}

// TestOutputBrokerSessionPreservesDurableEvidence proves optional output continuity remains provider-neutral after launch.
func TestOutputBrokerSessionPreservesDurableEvidence(t *testing.T) {
	if outputBrokerSession(nil) != nil {
		t.Fatal("nil output broker produced durable evidence")
	}
	peer := &projectprocess.OutputBrokerPeer{
		EndpointReference: "unix:///tmp/harbor-output.sock",
		ManifestPath:      "/tmp/harbor-output.json",
		TicketDigest:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Process: domain.ProcessEvidence{
			PID:                42,
			BirthToken:         "birth",
			ExecutableIdentity: "/usr/local/bin/harbor-output",
			ArgumentDigest:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	got := outputBrokerSession(peer)
	if got == nil || got.EndpointReference != peer.EndpointReference || got.ManifestPath != peer.ManifestPath ||
		got.CredentialDigest != peer.TicketDigest || got.Process != peer.Process {
		t.Fatalf("translated output broker = %#v, want %#v", got, peer)
	}
}

// TestRecoveryValuesRemainFailClosed proves known values translate exactly and unknown values remain unknown.
func TestRecoveryValuesRemainFailClosed(t *testing.T) {
	states := map[projectprocess.PriorProcessState]projectruntime.PriorProcessState{
		projectprocess.PriorProcessAbsent:   projectruntime.PriorProcessAbsent,
		projectprocess.PriorProcessReplaced: projectruntime.PriorProcessReplaced,
		projectprocess.PriorProcessPresent:  projectruntime.PriorProcessPresent,
		"future":                            "future",
	}
	for input, want := range states {
		if got := priorProcessState(input); got != want {
			t.Fatalf("prior process state %q = %q, want %q", input, got, want)
		}
	}
	outcomes := map[projectprocess.PriorProcessSettlementOutcome]projectruntime.PriorProcessSettlementOutcome{
		projectprocess.PriorProcessSettlementAbsent:     projectruntime.PriorProcessSettlementAbsent,
		projectprocess.PriorProcessSettlementReplaced:   projectruntime.PriorProcessSettlementReplaced,
		projectprocess.PriorProcessSettlementTerminated: projectruntime.PriorProcessSettlementTerminated,
		"future": "future",
	}
	for input, want := range outcomes {
		if got := priorProcessSettlementOutcome(input); got != want {
			t.Fatalf("prior process settlement %q = %q, want %q", input, got, want)
		}
	}
}
