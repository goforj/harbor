package projectprocess

import (
	"os/exec"
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestFrameworkResourceQueryUsesExactAcceptedProcessContext protects the executable, checkout, and environment trust boundary.
func TestFrameworkResourceQueryUsesExactAcceptedProcessContext(t *testing.T) {
	projectID := domain.ProjectID("project-resources")
	sessionID := domain.SessionID("session-resources")
	command := exec.Command("/opt/goforj/bin/forj", "dev")
	command.Dir = "/work/checkouts/project-resources"
	command.Env = []string{"PATH=/opt/goforj/bin", "IP_ADDRESS=127.77.1.8"}
	process := &managedProcess{command: command, acceptingStop: true}
	supervisor := &Supervisor{
		projects: map[domain.ProjectID]*managedProcess{projectID: process},
		sessions: map[domain.SessionID]*managedProcess{sessionID: process},
	}

	query, found := supervisor.frameworkResourceQuery(projectID, sessionID)
	if !found {
		t.Fatal("frameworkResourceQuery() found = false")
	}
	if query.Executable != command.Path || query.Checkout != command.Dir || !reflect.DeepEqual(query.Environment, command.Env) {
		t.Fatalf("frameworkResourceQuery() = %#v, want accepted command context", query)
	}
	query.Environment[0] = "PATH=/changed"
	if command.Env[0] != "PATH=/opt/goforj/bin" {
		t.Fatalf("frameworkResourceQuery() retained mutable process environment: %#v", command.Env)
	}
}

// TestFrameworkResourceQueryRejectsMismatchedOrStoppingAuthority prevents observations from crossing session boundaries.
func TestFrameworkResourceQueryRejectsMismatchedOrStoppingAuthority(t *testing.T) {
	projectID := domain.ProjectID("project-resources")
	sessionID := domain.SessionID("session-resources")
	process := &managedProcess{command: exec.Command("/opt/goforj/bin/forj", "dev"), acceptingStop: true}
	other := &managedProcess{command: exec.Command("/opt/goforj/bin/forj", "dev"), acceptingStop: true}

	for _, test := range []struct {
		name        string
		project     *managedProcess
		session     *managedProcess
		requestStop bool
	}{
		{name: "missing project", session: process},
		{name: "missing session", project: process},
		{name: "mismatched session", project: process, session: other},
		{name: "stopping process", project: process, session: process, requestStop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.requestStop {
				test.project.stopRequested.Store(true)
				defer test.project.stopRequested.Store(false)
			}
			supervisor := &Supervisor{
				projects: map[domain.ProjectID]*managedProcess{},
				sessions: map[domain.SessionID]*managedProcess{},
			}
			if test.project != nil {
				supervisor.projects[projectID] = test.project
			}
			if test.session != nil {
				supervisor.sessions[sessionID] = test.session
			}
			if _, found := supervisor.frameworkResourceQuery(projectID, sessionID); found {
				t.Fatal("frameworkResourceQuery() found = true")
			}
		})
	}
}
