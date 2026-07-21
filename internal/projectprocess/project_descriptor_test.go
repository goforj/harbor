package projectprocess

import (
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/goforj"
)

// TestAcceptedGoForjExecutableRejectsRelativeDescriptorBinding prevents a preflight from selecting a PATH-relative replacement.
func TestAcceptedGoForjExecutableRejectsRelativeDescriptorBinding(t *testing.T) {
	supervisor := NewWithExecutableVerifier(Options{}, func(string) error {
		t.Fatal("relative descriptor binding reached executable verification")
		return nil
	})
	_, err := supervisor.acceptedGoForjExecutable("forj")
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("acceptedGoForjExecutable() error = %v, want absolute-path rejection", err)
	}
}

// TestCloneServiceRequirementsCopiesNestedIntent keeps descriptor observations immutable across lifecycle boundaries.
func TestCloneServiceRequirementsCopiesNestedIntent(t *testing.T) {
	original := []goforj.ServiceRequirement{{
		ID:        "requirement.database.primary",
		Consumers: []string{"app"},
		Endpoints: []goforj.ServiceEndpoint{{ID: "endpoint.database.primary.tcp", NativePort: 3306}},
	}}
	clone := cloneServiceRequirements(original)
	clone[0].Consumers[0] = "worker"
	clone[0].Endpoints[0].ID = "changed"
	if original[0].Consumers[0] != "app" || original[0].Endpoints[0].ID != "endpoint.database.primary.tcp" {
		t.Fatalf("clone mutated original: %#v", original)
	}
}
