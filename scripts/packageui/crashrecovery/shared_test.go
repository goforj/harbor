package main

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// TestWorkerGuard rejects local, self-hosted, and unrelated repository execution.
func TestWorkerGuard(t *testing.T) {
	valid := map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "goforj/harbor", "RUNNER_ENVIRONMENT": "github-hosted"}
	if err := workerGuard(func(key string) string { return valid[key] }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_REPOSITORY", "RUNNER_ENVIRONMENT"} {
		t.Run(key, func(t *testing.T) {
			if err := workerGuard(func(candidate string) string {
				if candidate == key {
					return "other"
				}
				return valid[candidate]
			}); err == nil {
				t.Fatal("unsafe worker admitted")
			}
		})
	}
}

// TestFixtureRoot rejects aliases, escaped roots, and malformed disposable names.
func TestFixtureRoot(t *testing.T) {
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.MkdirTemp(temporary, "harbor-package-ui-guard-")
	if err != nil {
		t.Fatal(err)
	}
	// These empty unit-test directories have no generated source or installed project state.
	t.Cleanup(func() { os.Remove(filepath.Join(parent, "project")); os.Remove(parent) })
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureRoot(project); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "project", parent, project + string(os.PathSeparator), filepath.Join(t.TempDir(), "project")} {
		if _, err := fixtureRoot(path); err == nil {
			t.Errorf("admitted %q", path)
		}
	}
}

// TestInitialProject requires exactly the selected Ready project and configured networking.
func TestInitialProject(t *testing.T) {
	project := domain.ProjectSnapshot{ID: "original", Path: "/fixture/project", State: domain.ProjectReady}
	snapshot := domain.Snapshot{Projects: []domain.ProjectSnapshot{project}, NetworkSetup: &domain.NetworkSetupSnapshot{Configured: true}}
	if _, err := initialProject(snapshot, project.ID, project.Path); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Snapshot){
		func(s *domain.Snapshot) { s.Projects = nil }, func(s *domain.Snapshot) { s.Projects = append(s.Projects, project) },
		func(s *domain.Snapshot) { s.NetworkSetup = nil }, func(s *domain.Snapshot) { s.NetworkSetup = &domain.NetworkSetupSnapshot{} },
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: "other", Path: project.Path, State: domain.ProjectReady}}
		},
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: project.ID, Path: "/other", State: domain.ProjectReady}}
		},
		func(s *domain.Snapshot) {
			s.Projects = []domain.ProjectSnapshot{{ID: project.ID, Path: project.Path, State: domain.ProjectStarting}}
		},
	} {
		copy := snapshot
		mutate(&copy)
		if _, err := initialProject(copy, project.ID, project.Path); err == nil {
			t.Fatal("unsafe initial snapshot admitted")
		}
	}
}

// TestContinuityAndDistinctProjects checks exact sessions, reconnect generations, and stable unique addresses.
func TestContinuityAndDistinctProjects(t *testing.T) {
	before := projectEvidence{ID: "original", Path: "/fixture/project", Host: "original.test", Address: "127.0.0.1", ProjectAddress: "127.0.0.2", SessionID: "session-original", Generation: 2, HTTPS200: true}
	after := before
	after.Generation++
	if err := continuity(before, after); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){
		func(e *projectEvidence) { e.ID = "other" }, func(e *projectEvidence) { e.Path = "/other" }, func(e *projectEvidence) { e.Host = "other.test" },
		func(e *projectEvidence) { e.Address = "127.0.0.3" }, func(e *projectEvidence) { e.ProjectAddress = "127.0.0.4" }, func(e *projectEvidence) { e.SessionID = "replacement" }, func(e *projectEvidence) { e.Generation-- }, func(e *projectEvidence) { e.HTTPS200 = false },
	} {
		changed := before
		mutate(&changed)
		if err := continuity(before, changed); err == nil {
			t.Fatal("continuity mismatch admitted")
		}
	}
	peer := projectEvidence{ID: "peer", Host: "peer.test", Address: "127.0.0.1", ProjectAddress: "127.0.0.3", SessionID: "peer-session"}
	if err := distinctProjects(before, peer); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){func(e *projectEvidence) { e.ID = before.ID }, func(e *projectEvidence) { e.Host = before.Host }, func(e *projectEvidence) { e.ProjectAddress = before.ProjectAddress }, func(e *projectEvidence) { e.SessionID = before.SessionID }, func(e *projectEvidence) { e.SessionID = "" }} {
		changed := peer
		mutate(&changed)
		if err := distinctProjects(before, changed); err == nil {
			t.Fatal("collision admitted")
		}
	}
}

// TestGateProof rejects stale markers, foreign listeners, malformed ports, and oversized fixture output.
func TestGateProof(t *testing.T) {
	proof := gateProof{Address: "127.0.0.1:12345", Marker: "marker"}
	if err := validateGateProof(proof, "marker"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"localhost:12345", "127.0.0.2:12345", "[::1]:12345", "0.0.0.0:12345", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:abc", "127.0.0.1"} {
		if err := validateGateProof(gateProof{address, "marker"}, "marker"); err == nil {
			t.Errorf("admitted %s", address)
		}
	}
	if err := validateGateProof(proof, "stale"); err == nil {
		t.Fatal("stale marker admitted")
	}
	if err := validateGateProof(gateProof{proof.Address, ""}, ""); err == nil {
		t.Fatal("empty marker admitted")
	}
	path := filepath.Join(t.TempDir(), "entered.json")
	for _, data := range []string{`{"address":"127.0.0.1:12345","marker":"marker"}`, "not JSON", strings.Repeat("x", 2049)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readGateProof(path, "marker")
		if strings.HasPrefix(data, "{") && err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(data, "{") && err == nil {
			t.Fatal("invalid output admitted")
		}
	}
}

// TestGeneratedGateCompilesAndReleases proves the gate is disabled initially and closes its live proof on release.
func TestGeneratedGateCompilesAndReleases(t *testing.T) {
	parent := t.TempDir()
	marker := "gate-native-test"
	source := gateSource(parent, marker)
	if _, err := parser.ParseFile(token.NewFileSet(), "gate.go", source, parser.AllErrors); err != nil {
		t.Fatal(err)
	}
	source += "\n// main completes the isolated gate compilation fixture.\nfunc main() {}\n"
	path := filepath.Join(parent, "gate.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(parent, "gate-test")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, path).CombinedOutput(); err != nil {
		t.Fatalf("gate compile: %v: %s", err, output)
	}
	if err := exec.CommandContext(ctx, binary).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "entered.json")); !os.IsNotExist(err) {
		t.Fatal("disabled gate wrote proof")
	}
	if err := os.WriteFile(filepath.Join(parent, "gate-enabled"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	var proof gateProof
	if err := waitFor(ctx, 5*time.Second, func(attempt context.Context) (bool, error) {
		var err error
		proof, err = readGateProof(filepath.Join(parent, "entered.json"), marker)
		return err == nil, err
	}); err != nil {
		t.Fatal(err)
	}
	if err := probe(ctx, "http://"+proof.Address+"/", marker); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(parent, "entered.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded gateProof
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "gate-release"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := listenerAbsent(ctx, proof.Address); err != nil {
		t.Fatal(err)
	}
}

// TestRunFailureWritesIncompleteReceipt proves admission failures preserve machine-readable evidence.
func TestRunFailureWritesIncompleteReceipt(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := run([]string{"--project-id", "original", "--project-directory", "/invalid/project", "--evidence", path}); err == nil {
		t.Fatal("local mutation admitted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evidence receipt
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Complete || evidence.Error == "" || len(evidence.Crashes) != 0 {
		t.Fatalf("failure receipt: %s", data)
	}
}

// TestParseFailureWritesIncompleteReceipt retains evidence when a malformed earlier flag stops parsing.
func TestParseFailureWritesIncompleteReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parse-receipt.json")
	if err := run([]string{"--unknown-argument", "--evidence=" + path}); err == nil {
		t.Fatal("invalid argument admitted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evidence receipt
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Complete || evidence.Error == "" {
		t.Fatalf("failure receipt: %s", data)
	}
}

// TestProcessBackedSessionDistinguishesSupervisionFromAuthenticatedAttachment accepts both persisted launch states.
func TestProcessBackedSessionDistinguishesSupervisionFromAuthenticatedAttachment(t *testing.T) {
	for _, state := range []domain.SessionState{domain.SessionAwaitingAttach, domain.SessionAttached} {
		project := projectEvidence{SessionID: "session", Generation: 2, SessionState: state}
		if !processBackedSession(project) {
			t.Fatalf("rejected process-backed %s", state)
		}
	}
	for _, project := range []projectEvidence{
		{Generation: 2, SessionState: domain.SessionAwaitingAttach},
		{SessionID: "session", SessionState: domain.SessionAwaitingAttach},
		{SessionID: "session", Generation: 2, SessionState: domain.SessionPlanned},
		{SessionID: "session", Generation: 2, SessionState: domain.SessionDisconnected},
		{SessionID: "session", Generation: 2, SessionState: domain.SessionStopping},
	} {
		if processBackedSession(project) {
			t.Fatalf("accepted unproven session: %#v", project)
		}
	}
}

// TestFreshSessionRequiresExplicitRetryContinuity checks identity stability while requiring a new session.
func TestFreshSessionRequiresExplicitRetryContinuity(t *testing.T) {
	before := projectEvidence{ID: "original", Path: "/fixture/project", Host: "original.test", Address: "127.0.0.1", ProjectAddress: "127.0.0.2", SessionID: "old"}
	after := before
	after.SessionID = "new"
	after.Generation = 2
	after.SessionState = domain.SessionAwaitingAttach
	after.State = domain.ProjectReady
	after.HTTPS200 = true
	if err := freshSession(before, after); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){
		func(e *projectEvidence) { e.ID = "other" }, func(e *projectEvidence) { e.Path = "/other" },
		func(e *projectEvidence) { e.Host = "other.test" }, func(e *projectEvidence) { e.Address = "127.0.0.3" }, func(e *projectEvidence) { e.ProjectAddress = "127.0.0.4" },
		func(e *projectEvidence) { e.SessionID = before.SessionID }, func(e *projectEvidence) { e.SessionID = "" },
		func(e *projectEvidence) { e.SessionState = domain.SessionPlanned }, func(e *projectEvidence) { e.State = domain.ProjectFailed },
		func(e *projectEvidence) { e.HTTPS200 = false },
	} {
		changed := after
		mutate(&changed)
		if err := freshSession(before, changed); err == nil {
			t.Fatal("unsafe retry admitted")
		}
	}
}

// TestSettledProjectRejectsQuarantineAndRetainedRuntime requires affirmative safe failure without runtime authority.
func TestSettledProjectRejectsQuarantineAndRetainedRuntime(t *testing.T) {
	project := domain.ProjectSnapshot{ID: "peer", State: domain.ProjectFailed, Apps: []domain.AppSnapshot{{State: domain.EntityStopped}}, Services: []domain.ServiceSnapshot{{State: domain.EntityStopped}}}
	activity := control.ProjectActivity{ProjectID: project.ID}
	if err := settledProject(project, activity); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.ProjectSnapshot, *control.ProjectActivity){
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) { p.State = domain.ProjectUnavailable },
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) { p.State = domain.ProjectStopped },
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) { a.ProjectID = "other" },
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) {
			a.Session = &control.ProjectSessionActivity{ID: "retained"}
		},
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) {
			p.Resources = []domain.ResourceSnapshot{{}}
		},
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) {
			p.Apps = []domain.AppSnapshot{{State: domain.EntityStopped, Active: true}}
		},
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) {
			p.Apps = []domain.AppSnapshot{{State: domain.EntityReady}}
		},
		func(p *domain.ProjectSnapshot, a *control.ProjectActivity) {
			p.Services = []domain.ServiceSnapshot{{State: domain.EntityReady}}
		},
	} {
		changed, observed := project, activity
		mutate(&changed, &observed)
		if err := settledProject(changed, observed); err == nil {
			t.Fatal("unsafe settlement admitted")
		}
	}
}

// TestInterruptedStartRequiresExactRetryableFailure rejects unrelated operations and recovery quarantine.
func TestInterruptedStartRequiresExactRetryableFailure(t *testing.T) {
	before := domain.Operation{ID: "operation", ProjectID: "peer", IntentID: "intent", Kind: domain.OperationKindProjectStart}
	after := before
	after.State = domain.OperationFailed
	after.Problem = &domain.Problem{Code: "project.recovery.process_absent", Message: "Previous exact process settled.", Retryable: true}
	if err := interruptedStart(before, after); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Operation){
		func(o *domain.Operation) { o.ID = "other" }, func(o *domain.Operation) { o.ProjectID = "other" }, func(o *domain.Operation) { o.IntentID = "other" },
		func(o *domain.Operation) { o.Kind = domain.OperationKindProjectStop }, func(o *domain.Operation) { o.State = domain.OperationCancelled },
		func(o *domain.Operation) { o.Problem = nil }, func(o *domain.Operation) { o.Problem = &domain.Problem{} },
		func(o *domain.Operation) {
			o.Problem = &domain.Problem{Code: "project.recovery.ambiguous_launch", Message: "Quarantined.", Retryable: false}
		},
		func(o *domain.Operation) {
			o.Problem = &domain.Problem{Code: "project.recovery.process_absent", Message: "Settled.", Retryable: false}
		},
	} {
		changed := after
		mutate(&changed)
		if err := interruptedStart(before, changed); err == nil {
			t.Fatal("unsafe interrupted operation admitted")
		}
	}
}

// TestWithdrawnRouteRequiresAffirmativeIngressResponse rejects live routes and ambiguous transport failures.
func TestWithdrawnRouteRequiresAffirmativeIngressResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		accepted bool
	}{
		{"withdrawn", 421, "Harbor has no route for this host.\n", true},
		{"live", 200, "ready", false}, {"upstream failed", 502, "upstream failed", false},
		{"foreign response", 421, "other server", false}, {"not found", 404, "missing", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer server.Close()
			// The absent .test record must not prevent reaching the last Ready ingress.
			err := withdrawnRoute(context.Background(), "withdrawn-no-record.test", strings.TrimPrefix(server.URL, "http://"))
			select {
			case request := <-requests:
				if request.Host != "withdrawn-no-record.test" || request.URL.Path != "/-/ready" {
					t.Fatalf("withdrawal probe lost public route identity: host %q, path %q", request.Host, request.URL.Path)
				}
			default:
				t.Fatalf("last Ready ingress was not reached: %v", err)
			}
			if (err == nil) != test.accepted {
				t.Fatalf("route result = %v", err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := strings.TrimPrefix(server.URL, "http://")
	server.Close()
	if err := withdrawnRoute(context.Background(), "withdrawn-no-record.test", address); err == nil {
		t.Fatal("network failure counted as route absence")
	}
	for _, invalid := range []string{"withdrawn-no-record.test:80", "192.0.2.1:80", "127.00.0.1:80", "[::1]:80", "127.0.0.1"} {
		if err := withdrawnRoute(context.Background(), "withdrawn-no-record.test", invalid); err == nil {
			t.Fatalf("invalid ingress endpoint accepted: %q", invalid)
		}
	}
}

// TestInactiveIdentityAcceptsWithdrawnDNS requires stable private facts independently of public DNS.
func TestInactiveIdentityAcceptsWithdrawnDNS(t *testing.T) {
	before := projectEvidence{ID: "peer", Path: "/fixture/peer", Host: "withdrawn-no-record.test", Address: "127.0.0.1", ProjectAddress: "127.77.0.10", State: domain.ProjectReady, SessionID: "ready-session"}
	for _, state := range []domain.ProjectState{domain.ProjectStarting, domain.ProjectFailed} {
		after := before
		after.State = state
		after.Address = ""
		if err := inactiveIdentity(before, after); err != nil {
			t.Fatalf("inactive %s without public DNS: %v", state, err)
		}
		for _, mutate := range []func(*projectEvidence){
			func(p *projectEvidence) { p.ID = "other" },
			func(p *projectEvidence) { p.Path = "/other" },
			func(p *projectEvidence) { p.Host = "other.test" },
			func(p *projectEvidence) { p.ProjectAddress = "" },
			func(p *projectEvidence) { p.ProjectAddress = "127.77.0.11" },
			func(p *projectEvidence) { p.ProjectAddress = before.Address },
		} {
			changed := after
			mutate(&changed)
			if err := inactiveIdentity(before, changed); err == nil {
				t.Fatalf("inactive identity drift accepted: %#v", changed)
			}
		}
	}
	invalid := before
	invalid.ProjectAddress = "127.077.0.10"
	if err := inactiveIdentity(invalid, invalid); err == nil {
		t.Fatal("noncanonical private assignment accepted")
	}
}

// TestSettledObservationRejectsFinalReadDrift prevents an earlier retirement read from admitting a restarted session.
func TestSettledObservationRejectsFinalReadDrift(t *testing.T) {
	settled := projectEvidence{ID: "peer", State: domain.ProjectFailed}
	if err := settledObservation(settled); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*projectEvidence){
		func(project *projectEvidence) { project.State = domain.ProjectStarting },
		func(project *projectEvidence) { project.State = domain.ProjectReady },
		func(project *projectEvidence) { project.State = domain.ProjectUnavailable },
		func(project *projectEvidence) { project.SessionID = "new-session" },
		func(project *projectEvidence) { project.SessionState = domain.SessionAwaitingAttach },
		func(project *projectEvidence) { project.Generation = 1 },
	} {
		changed := settled
		mutate(&changed)
		if err := settledObservation(changed); err == nil {
			t.Fatalf("admitted final observation drift: %#v", changed)
		}
	}
}

// TestAssignedProjectAddressSelectsOnlyCanonicalProviderAddress excludes unrelated environment material from evidence.
func TestAssignedProjectAddressSelectsOnlyCanonicalProviderAddress(t *testing.T) {
	environment := control.ProjectEnvironment{ProjectID: "peer", OverridesAvailable: true, Overrides: []control.ProjectEnvironmentVariable{
		{Name: "IP_ADDRESS", Value: "127.77.0.10", Source: "runtime.provider"},
		{Name: "DEV_SERVICE_IP_ADDRESS", Value: "127.77.0.10", Source: "runtime.provider"},
		{Name: "APP_HOST", Value: "127.77.0.99", Source: "project.address"},
		{Name: "PRIVATE_KEY", Value: "do-not-retain", Source: "provider"},
	}, Files: []control.ProjectEnvironmentFile{{Name: ".env", Contents: "do-not-retain"}}}
	address, err := assignedProjectAddress(environment, "peer", "127.0.0.1")
	if err != nil || address != "127.77.0.10" {
		t.Fatalf("address = %q, error = %v", address, err)
	}
	for _, mutate := range []func(*control.ProjectEnvironment){
		func(e *control.ProjectEnvironment) { e.ProjectID = "other" },
		func(e *control.ProjectEnvironment) { e.OverridesAvailable = false },
		func(e *control.ProjectEnvironment) { e.Overrides = nil },
		func(e *control.ProjectEnvironment) { e.Overrides = e.Overrides[1:] },
		func(e *control.ProjectEnvironment) { e.Overrides = append(e.Overrides[:1], e.Overrides[2:]...) },
		func(e *control.ProjectEnvironment) { e.Overrides[0].Source = "project.address" },
		func(e *control.ProjectEnvironment) { e.Overrides[1].Source = "project.address" },
		func(e *control.ProjectEnvironment) { e.Overrides[1].Value = "127.77.0.11" },
		func(e *control.ProjectEnvironment) { e.Overrides = append(e.Overrides, e.Overrides[0]) },
		func(e *control.ProjectEnvironment) { e.Overrides = append(e.Overrides, e.Overrides[1]) },
	} {
		changed := environment
		changed.Overrides = append([]control.ProjectEnvironmentVariable{}, environment.Overrides...)
		mutate(&changed)
		if _, err := assignedProjectAddress(changed, "peer", "127.0.0.1"); err == nil {
			t.Fatal("invalid project assignment accepted")
		}
	}
	for _, value := range []string{"", "127.0.0.1", "127.077.0.10", "127.77.0.10 ", "::1", "::ffff:127.77.0.10", "192.0.2.1", "localhost"} {
		if validProjectAddress(value, "127.0.0.1") {
			t.Errorf("invalid assignment accepted: %q", value)
		}
		changed := environment
		changed.Overrides = append([]control.ProjectEnvironmentVariable{}, environment.Overrides...)
		changed.Overrides[0].Value = value
		changed.Overrides[1].Value = value
		if _, err := assignedProjectAddress(changed, "peer", "127.0.0.1"); err == nil {
			t.Fatal("invalid provider assignment accepted")
		}
	}
}

// TestCreatePeerParentMatchesExistingCleanupBoundary proves UUID shape, canonical fixture admission, and exclusive preservation.
func TestCreatePeerParentMatchesExistingCleanupBoundary(t *testing.T) {
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		t.Fatal(err)
	}
	token := nonce()
	parent, err := createPeerParent(temporary, token)
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(parent, "project")
	marker := filepath.Join(parent, "preserved")
	t.Cleanup(func() { os.Remove(marker); os.Remove(project); os.Remove(parent) })
	if !regexp.MustCompile(`^harbor-package-ui-[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`).MatchString(filepath.Base(parent)) {
		t.Fatalf("cleanup-incompatible fixture name: %s", parent)
	}
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if canonical, err := fixtureRoot(project); err != nil || canonical != project {
		t.Fatalf("fixture admission: %q, %v", canonical, err)
	}
	if err := os.WriteFile(marker, []byte("preserve existing fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := createPeerParent(temporary, token); err == nil {
		t.Fatal("existing fixture reused")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "preserve existing fixture" {
		t.Fatal("existing fixture changed")
	}
	for _, invalid := range []string{"", strings.Repeat("a", 31), strings.Repeat("g", 32), strings.Repeat("a", 33)} {
		if _, err := createPeerParent(temporary, invalid); err == nil {
			t.Fatal("invalid fixture identifier accepted")
		}
	}
}
