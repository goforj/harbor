package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// registrationTestProject returns the inert initial project shape accepted by registration.
func registrationTestProject(identity string, path string, slug string, updatedAt time.Time) domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        domain.ProjectID(identity),
		Name:      "Orders API",
		Path:      path,
		Slug:      slug,
		State:     domain.ProjectStopped,
		Favorite:  false,
		UpdatedAt: updatedAt,
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
}

// TestStoreRegisterProjectCreatesAndReplaysWithoutRevisionChurn verifies client retries preserve the first durable identity.
func TestStoreRegisterProjectCreatesAndReplaysWithoutRevisionChurn(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	firstProject := registrationTestProject("project-orders", "/work/orders", "orders-api", projectStoreReadTestTime())

	first, err := store.RegisterProject(t.Context(), firstProject)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	if !first.Created || first.Record.Revision != 1 || !reflect.DeepEqual(first.Record.Project, firstProject) {
		t.Fatalf("first registration = %#v, want created revision 1", first)
	}
	retry := firstProject
	retry.UpdatedAt = firstProject.UpdatedAt.Add(time.Hour)
	second, err := store.RegisterProject(t.Context(), retry)
	if err != nil {
		t.Fatalf("replay registration: %v", err)
	}
	if second.Created || second.Record.Revision != first.Record.Revision || !reflect.DeepEqual(second.Record.Project, first.Record.Project) {
		t.Fatalf("replayed registration = %#v, want original %#v", second, first)
	}
	sequence, err := store.CurrentSequence(t.Context())
	if err != nil || sequence != 1 {
		t.Fatalf("sequence = %d, error %v, want 1", sequence, err)
	}
}

// TestStoreRegisterProjectReplaysAcrossPresentationDrift proves changed allowlisted metadata requires a later reconciliation, not another identity.
func TestStoreRegisterProjectReplaysAcrossPresentationDrift(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	firstProject := registrationTestProject("project-orders", "/work/orders", "orders-api", projectStoreReadTestTime())
	first, err := store.RegisterProject(t.Context(), firstProject)
	if err != nil {
		t.Fatalf("register project: %v", err)
	}
	changed := firstProject
	changed.ID = "project-renamed-proposal"
	changed.Name = "Renamed Orders"
	changed.Slug = "renamed-orders"
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Hour)

	replayed, err := store.RegisterProject(t.Context(), changed)
	if err != nil {
		t.Fatalf("replay renamed project: %v", err)
	}
	if replayed.Created || !reflect.DeepEqual(replayed.Record, first.Record) {
		t.Fatalf("renamed replay = %#v, want preserved %#v", replayed, first.Record)
	}
	sequence, sequenceErr := store.CurrentSequence(t.Context())
	if sequenceErr != nil || sequence != 1 {
		t.Fatalf("sequence = %d, error %v, want unchanged 1", sequence, sequenceErr)
	}
}

// TestStoreRegisterProjectFollowsNativePathCasePolicy prevents aliases on case-insensitive hosts without folding Linux identities.
func TestStoreRegisterProjectFollowsNativePathCasePolicy(t *testing.T) {
	parent := t.TempDir()
	originalPath := filepath.Join(parent, "OrdersCase")
	aliasPath := filepath.Join(parent, "orderscase")
	if err := os.Mkdir(originalPath, 0o700); err != nil {
		t.Fatalf("create original project path: %v", err)
	}

	aliasResolves := false
	if _, err := os.Stat(aliasPath); err == nil {
		aliasResolves = true
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect project path alias: %v", err)
	}
	if registrationPathNeedsFilesystemIdentity() && !aliasResolves {
		if err := os.Mkdir(aliasPath, 0o700); err != nil {
			t.Fatalf("create distinct case-sensitive project path: %v", err)
		}
	}
	wantReplay := registrationPathNeedsFilesystemIdentity() && aliasResolves
	if runtime.GOOS == "linux" && registrationPathNeedsFilesystemIdentity() {
		t.Fatal("Linux path identity unexpectedly requires filesystem folding")
	}

	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	first, err := store.RegisterProject(t.Context(), registrationTestProject("project-original", originalPath, "orders-original", projectStoreReadTestTime()))
	if err != nil {
		t.Fatalf("register original project: %v", err)
	}
	second, err := store.RegisterProject(t.Context(), registrationTestProject("project-alias", aliasPath, "orders-alias", projectStoreReadTestTime()))
	if err != nil {
		t.Fatalf("register case alias: %v", err)
	}
	if wantReplay {
		if second.Created || second.Record.Project.ID != first.Record.Project.ID || second.Record.Revision != first.Record.Revision {
			t.Fatalf("case alias registration = %#v, want replay of %#v", second, first)
		}
		if !sameRegisteredPath(originalPath, aliasPath) {
			t.Fatal("sameRegisteredPath() rejected a native alias")
		}
	} else {
		if !second.Created || second.Record.Project.ID == first.Record.Project.ID || second.Record.Revision != first.Record.Revision+1 {
			t.Fatalf("distinct-case registration = %#v, want new project after %#v", second, first)
		}
		if sameRegisteredPath(originalPath, aliasPath) {
			t.Fatal("sameRegisteredPath() folded distinct native paths")
		}
	}
}

// TestStoreRegisterProjectReplayDoesNotOverwriteRuntimeProjection proves add retries remain read-only after later state changes.
func TestStoreRegisterProjectReplayDoesNotOverwriteRuntimeProjection(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	initial := registrationTestProject("project-orders", "/work/orders", "orders-api", projectStoreReadTestTime())
	if _, err := store.RegisterProject(t.Context(), initial); err != nil {
		t.Fatalf("register initial project: %v", err)
	}
	updated := initial
	updated.State = domain.ProjectReady
	updated.Favorite = true
	updated.UpdatedAt = initial.UpdatedAt.Add(time.Minute)
	updated.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
	if _, err := store.PutProject(t.Context(), updated); err != nil {
		t.Fatalf("put runtime projection: %v", err)
	}

	replayed, err := store.RegisterProject(t.Context(), initial)
	if err != nil {
		t.Fatalf("replay registration: %v", err)
	}
	if replayed.Created || replayed.Record.Project.State != domain.ProjectReady || !replayed.Record.Project.Favorite || len(replayed.Record.Project.Apps) != 1 {
		t.Fatalf("registration replay overwrote runtime projection: %#v", replayed)
	}
	if replayed.Record.Revision != 2 {
		t.Fatalf("replay revision = %d, want current revision 2", replayed.Record.Revision)
	}
}

// TestStoreRegisterProjectRejectsReplayDuringNetworkRelease proves a new proposed identity cannot bypass the existing path owner's teardown boundary.
func TestStoreRegisterProjectRejectsReplayDuringNetworkRelease(t *testing.T) {
	fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
	existing, err := fixture.store.Project(t.Context(), fixture.begin.ProjectID)
	if err != nil {
		t.Fatalf("read releasing project: %v", err)
	}
	requested := registrationTestProject(
		"project-replay-proposal",
		existing.Project.Path,
		existing.Project.Slug,
		projectStoreReadTestTime(),
	)
	requested.Name = existing.Project.Name

	_, err = fixture.store.RegisterProject(t.Context(), requested)
	var active *ProjectNetworkReleaseActiveError
	if !errors.As(err, &active) || active.ProjectID != existing.Project.ID || active.OperationID != fixture.running.Operation.ID {
		t.Fatalf("registration error = %#v / %v, want active release for %q", active, err, existing.Project.ID)
	}
}

// TestStoreRegisterProjectReaddsCompletedPathWithFreshIdentity proves retained teardown evidence does not permanently reserve a checkout path.
func TestStoreRegisterProjectReaddsCompletedPathWithFreshIdentity(t *testing.T) {
	fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
	existing, err := fixture.store.Project(t.Context(), fixture.begin.ProjectID)
	if err != nil {
		t.Fatalf("read project before unregister: %v", err)
	}
	if _, err := fixture.store.CompleteProjectUnregister(
		t.Context(),
		fixture.begin.ProjectID,
		fixture.running.Operation.ID,
		fixture.running.Revision,
		"project removed",
		fixture.completedAt,
	); err != nil {
		t.Fatalf("complete project unregister: %v", err)
	}
	before, err := fixture.store.CurrentSequence(t.Context())
	if err != nil {
		t.Fatalf("read sequence before re-registration: %v", err)
	}
	requested := registrationTestProject(
		"project-readded",
		existing.Project.Path,
		existing.Project.Slug,
		fixture.completedAt.Add(time.Minute),
	)
	requested.Name = existing.Project.Name

	registered, err := fixture.store.RegisterProject(t.Context(), requested)
	if err != nil {
		t.Fatalf("register completed path with fresh identity: %v", err)
	}
	if !registered.Created || registered.Record.Project.ID != requested.ID || registered.Record.Revision != before+1 {
		t.Fatalf("registration = %#v, want fresh identity at revision %d", registered, before+1)
	}
	var retained models.NetworkProjectRelease
	if err := fixture.connection.Where("operation_id = ?", string(fixture.running.Operation.ID)).First(&retained).Error; err != nil {
		t.Fatalf("read retained release marker: %v", err)
	}
	if retained.SourceProjectId != string(existing.Project.ID) || retained.State != string(ProjectNetworkReleaseCompleted) {
		t.Fatalf("retained release marker = %#v, want completed source %q", retained, existing.Project.ID)
	}
}

// TestStoreRegisterProjectReportsNaturalIdentityConflicts verifies every durable uniqueness boundary has a typed failure.
func TestStoreRegisterProjectReportsNaturalIdentityConflicts(t *testing.T) {
	for _, test := range []struct {
		name      string
		existing  domain.ProjectSnapshot
		requested domain.ProjectSnapshot
		want      ProjectRegistrationConflictKind
	}{
		{
			name:      "identity",
			existing:  registrationTestProject("project-orders", "/work/original", "original", projectStoreReadTestTime()),
			requested: registrationTestProject("project-orders", "/work/moved", "moved", projectStoreReadTestTime()),
			want:      ProjectRegistrationConflictIdentity,
		},
		{
			name:      "slug",
			existing:  registrationTestProject("project-existing", "/work/first", "orders", projectStoreReadTestTime()),
			requested: registrationTestProject("project-requested", "/work/second", "orders", projectStoreReadTestTime()),
			want:      ProjectRegistrationConflictSlug,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			if _, err := store.RegisterProject(t.Context(), test.existing); err != nil {
				t.Fatalf("register existing project: %v", err)
			}
			_, err := store.RegisterProject(t.Context(), test.requested)
			var conflict *ProjectRegistrationConflictError
			if !errors.As(err, &conflict) || conflict.Kind != test.want {
				t.Fatalf("conflict error = %#v / %v, want kind %q", conflict, err, test.want)
			}
			sequence, sequenceErr := store.CurrentSequence(t.Context())
			if sequenceErr != nil || sequence != 1 {
				t.Fatalf("sequence = %d, error %v, want unchanged 1", sequence, sequenceErr)
			}
		})
	}
}

// TestProjectRegistrationConflictErrorReportsUnknownKinds safely bounds malformed diagnostic values.
func TestProjectRegistrationConflictErrorReportsUnknownKinds(t *testing.T) {
	err := (&ProjectRegistrationConflictError{Kind: "future"}).Error()
	if err != `project registration conflict "future"` {
		t.Fatalf("unknown conflict message = %q", err)
	}
}

// TestStoreRegisterProjectRejectsNonInitialShapes verifies callers cannot use registration as a general projection mutation.
func TestStoreRegisterProjectRejectsNonInitialShapes(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	base := registrationTestProject("project-orders", "/work/orders", "orders", projectStoreReadTestTime())
	for _, test := range []struct {
		name   string
		mutate func(*domain.ProjectSnapshot)
	}{
		{name: "running state", mutate: func(project *domain.ProjectSnapshot) { project.State = domain.ProjectReady }},
		{name: "favorite", mutate: func(project *domain.ProjectSnapshot) { project.Favorite = true }},
		{name: "App projection", mutate: func(project *domain.ProjectSnapshot) {
			project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := base
			test.mutate(&project)
			if _, err := store.RegisterProject(t.Context(), project); err == nil {
				t.Fatal("RegisterProject() error = nil, want initial-shape rejection")
			}
		})
	}
}

// TestStoreRegisterProjectRollsBackSequenceOnWriteFailure proves registration and global ordering share one transaction.
func TestStoreRegisterProjectRollsBackSequenceOnWriteFailure(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	want := errors.New("create failed")
	callback := "project-registration-create-failure"
	if err := connection.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "projects" {
			tx.AddError(want)
		}
	}); err != nil {
		t.Fatalf("register create failure: %v", err)
	}
	t.Cleanup(func() { _ = connection.Callback().Create().Remove(callback) })

	_, err := store.RegisterProject(t.Context(), registrationTestProject("project-orders", "/work/orders", "orders", projectStoreReadTestTime()))
	if !errors.Is(err, want) {
		t.Fatalf("registration error = %v, want %v", err, want)
	}
	sequence, sequenceErr := store.CurrentSequence(t.Context())
	if sequenceErr != nil || sequence != 0 {
		t.Fatalf("sequence = %d, error %v, want rolled back 0", sequence, sequenceErr)
	}
}

// TestStoreRegisterProjectConcurrentRetriesCreateExactlyOnce proves daemon-level serialization closes the check-create race.
func TestStoreRegisterProjectConcurrentRetriesCreateExactlyOnce(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 8, projectStoreMutationTestClock)
	project := registrationTestProject("project-orders", "/work/orders", "orders", projectStoreReadTestTime())
	const callers = 32
	results := make(chan ProjectRegistration, callers)
	errorsFound := make(chan error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			requested := project
			requested.ID = domain.ProjectID(fmt.Sprintf("project-orders-%d", index))
			requested.UpdatedAt = requested.UpdatedAt.Add(time.Duration(index) * time.Second)
			registered, err := store.RegisterProject(context.Background(), requested)
			if err != nil {
				errorsFound <- fmt.Errorf("caller %d: %w", index, err)
				return
			}
			results <- registered
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent registration: %v", err)
	}
	created := 0
	for result := range results {
		if result.Created {
			created++
		}
		if result.Record.Revision != 1 {
			t.Errorf("registration revision = %d, want 1", result.Record.Revision)
		}
	}
	if created != 1 {
		t.Fatalf("created results = %d, want exactly 1", created)
	}
	sequence, err := store.CurrentSequence(t.Context())
	if err != nil || sequence != 1 {
		t.Fatalf("sequence = %d, error %v, want 1", sequence, err)
	}
}
