package reconcile

import (
	"net/netip"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
)

// TestDefaultRuntimeKeepsProvenAppAndNeutralEnrichment verifies lifecycle only persists already-admitted snapshots.
func TestDefaultRuntimeKeepsProvenAppAndNeutralEnrichment(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", netip.MustParseAddr("127.77.4.8"), 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	runtime := defaultRuntime(projectRuntimePlanForTest(target), []domain.ServiceSnapshot{}, []domain.ResourceSnapshot{{
		ID: "docs", Name: "Docs", Kind: "documentation", URL: target.ResourceURL + "/docs",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: target.AppID},
	}})
	if len(runtime.Resources) != 2 || runtime.Resources[0].ID != "app-http" || runtime.Resources[1].ID != "docs" {
		t.Fatalf("defaultRuntime() resources = %#v", runtime.Resources)
	}
	if err := runtime.Validate(); err != nil {
		t.Fatalf("DefaultProjectRuntime.Validate() error = %v", err)
	}
}
