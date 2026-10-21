package reconcile

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforjruntime"
	"github.com/goforj/harbor/internal/projectprocess"
)

// TestFailedLaunchRetainsOutputAfterSessionRetirement reproduces a fast child failure through real process pipes and migrated SQLite.
func TestFailedLaunchRetainsOutputAfterSessionRetirement(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	t.Setenv(projectLifecycleHelperModeEnvironment, "compose-failure")
	spool := t.TempDir()
	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{OutputSpoolDirectory: spool})
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(t, store, journal, supervisor, netip.MustParseAddr("127.0.0.1"), uint16(port))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{ProjectID: project.ID, OperationID: "operation-failed-output", IntentID: "intent-failed-output"}); err != nil {
		t.Fatal(err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectFailed)
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("failed launch kept process authority")
	}
	// A new supervisor has no live handle, just as after a daemon restart.
	reopened := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{OutputSpoolDirectory: spool})
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	activity, err := readCurrentProjectActivity(t.Context(), store, goforjruntime.New(reopened), ProjectActivityRequest{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if activity.Session == nil || !activity.Session.Output.Historical || activity.Session.Output.Available || !strings.Contains(activity.Session.Output.Text, "port is already allocated") {
		t.Fatalf("retired failure output = %#v", activity)
	}
}
