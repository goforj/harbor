package reconcile

import (
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/state"
)

// TestManagedPublicationNetworkProblemDistinguishesIdentityAndResolverStages keeps the desktop action honest after DNS setup.
func TestManagedPublicationNetworkProblemDistinguishesIdentityAndResolverStages(t *testing.T) {
	tests := []struct {
		name        string
		initialized bool
		stage       state.NetworkStage
		code        string
		message     string
	}{
		{
			name:        "identity missing",
			initialized: false,
			stage:       state.NetworkStageIdentity,
			code:        "project.network.setup_required",
			message:     "network identity is not initialized",
		},
		{
			name:        "resolver only",
			initialized: true,
			stage:       state.NetworkStageResolver,
			code:        "project.network.full_setup_required",
			message:     "full network and trust setup is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problem := managedPublicationNetworkProblem(test.initialized, test.stage)
			if string(problem.Code) != test.code {
				t.Fatalf("problem code = %q, want %q", problem.Code, test.code)
			}
			if !problem.Retryable || !strings.Contains(problem.Message, test.message) {
				t.Fatalf("problem = %#v, want retryable message containing %q", problem, test.message)
			}
		})
	}
}
