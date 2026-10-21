package projectreadiness

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestInstalledToolsReadiness is an opt-in read-only check against the installed CLI and a selected local checkout.
func TestInstalledToolsReadiness(t *testing.T) {
	root := os.Getenv("HARBOR_READINESS_PROJECT")
	if root == "" {
		t.Skip("set HARBOR_READINESS_PROJECT for read-only installed-tool evidence")
	}
	checker := New()
	creation := checker.GoForjCreation(context.Background(), map[string]string{"components": "cli", "starter-kit": "none"})
	project := checker.GoForjProject(context.Background(), domain.ProjectSnapshot{ID: "readiness-smoke", Path: root})
	for _, report := range []Report{creation, project} {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(string(data))
		if checkByID(t, report, "forj").Status != "ready" || checkByID(t, report, "go").Status != "ready" {
			t.Fatal("installed Go/forj contract checks failed")
		}
	}
}
