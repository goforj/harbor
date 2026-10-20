package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

// projectEvidence retains only the bounded observations necessary to establish recovery continuity.
type projectEvidence struct {
	ProjectAddress string              `json:"project_address"`
	ID             domain.ProjectID    `json:"id"`
	Path           string              `json:"path"`
	State          domain.ProjectState `json:"state"`
	Host           string              `json:"host"`
	Address        string              `json:"address"`
	SessionID      domain.SessionID    `json:"session_id"`
	SessionState   domain.SessionState `json:"session_state"`
	Generation     uint64              `json:"generation"`
	HTTPS200       bool                `json:"https_200"`
	RouteWithdrawn bool                `json:"route_withdrawn"`
}

// crashEvidence records native identity replacement independently of project session continuity.
type crashEvidence struct {
	Before daemonIdentity `json:"before"`
	After  daemonIdentity `json:"after"`
}

// receipt remains incomplete until every assertion and owned-peer cleanup succeeds.
type receipt struct {
	Stage                string                     `json:"stage"`
	Complete             bool                       `json:"complete"`
	Error                string                     `json:"error,omitempty"`
	Observations         map[string]projectEvidence `json:"observations"`
	Crashes              []crashEvidence            `json:"crashes"`
	InitialDaemon        *daemonIdentity            `json:"initial_daemon,omitempty"`
	Assertions           []string                   `json:"assertions"`
	InterruptedOperation *domain.Operation          `json:"interrupted_operation,omitempty"`
	Gate                 *gateProof                 `json:"gate,omitempty"`
	PeerDirectory        string                     `json:"peer_directory,omitempty"`
	Cleanup              string                     `json:"cleanup"`
}

// recovery owns one private fixture and replaces each client after native daemon restart.
type recovery struct {
	client             *control.Client
	original           domain.ProjectID
	peer               domain.ProjectID
	peerPath           string
	parent             string
	marker             string
	evidence           *receipt
	originalCurrent    projectEvidence
	originalNeedsRetry bool
}

// main returns a failing process status for every incomplete recovery receipt.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run bounds the entire acceptance sequence and persists partial evidence even when admission fails.
func run(arguments []string) (result error) {
	flags := flag.NewFlagSet("crashrecovery", flag.ContinueOnError)
	id := flags.String("project-id", "", "Existing GUI fixture ID")
	directory := flags.String("project-directory", "", "Canonical GUI fixture directory")
	evidencePath := flags.String("evidence", evidenceArgument(arguments), "JSON receipt destination")
	parseErr := flags.Parse(arguments)
	evidence := &receipt{Observations: map[string]projectEvidence{}, Crashes: []crashEvidence{}, Assertions: []string{}, Cleanup: "no peer registered", Stage: "admission"}
	defer func() {
		if result != nil {
			evidence.Error = result.Error()
		}
		evidence.Complete = result == nil
		if result == nil {
			evidence.Stage = "complete"
		}
		if *evidencePath == "" {
			result = errors.Join(result, errors.New("evidence destination is required"))
			return
		}
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err == nil && len(data) > 128*1024 {
			err = errors.New("recovery receipt exceeds bound")
		}
		if err == nil {
			err = os.WriteFile(*evidencePath+".tmp", append(data, '\n'), 0600)
		}
		if err == nil {
			err = os.Rename(*evidencePath+".tmp", *evidencePath)
		}
		result = errors.Join(result, err)
	}()
	if parseErr != nil {
		return parseErr
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *evidencePath == "" || !filepath.IsAbs(*evidencePath) {
		return errors.New("absolute evidence destination required")
	}
	if err := workerGuard(os.Getenv); err != nil {
		return err
	}
	selected := domain.ProjectID(*id)
	if err := selected.Validate(); err != nil {
		return err
	}
	root, err := fixtureRoot(*directory)
	if err != nil {
		return err
	}
	if _, err := installedCLI(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := connect(ctx)
	if err != nil {
		return err
	}
	driver := &recovery{client: client, original: selected, evidence: evidence}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if driver.peerPath != "" {
			if err := driver.cleanup(cleanupCtx); err != nil {
				evidence.Cleanup = err.Error()
				result = errors.Join(result, fmt.Errorf("peer cleanup: %w", err))
			} else {
				evidence.Cleanup = "peer stopped and unregistered; source preserved"
			}
		}
		if driver.client != nil {
			driver.client.Close()
		}
	}()
	initialDaemon, err := observeInstalledDaemon(ctx, client.Peer())
	if err != nil {
		return err
	}
	evidence.InitialDaemon = &initialDaemon
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return err
	}
	if _, err := initialProject(snapshot, selected, root); err != nil {
		return err
	}
	original, err := driver.observe(ctx, selected, true)
	if err != nil {
		return err
	}
	evidence.Observations["original_initial"] = original
	driver.originalCurrent = original
	evidence.Stage = "peer_generation_and_registration"
	if err := driver.createPeer(ctx); err != nil {
		return err
	}
	return driver.scenarios(ctx, original)
}

// evidenceArgument retains the receipt destination even when flag parsing rejects an earlier argument.
func evidenceArgument(arguments []string) string {
	for index, argument := range arguments {
		if strings.HasPrefix(argument, "--evidence=") {
			return strings.TrimPrefix(argument, "--evidence=")
		}
		if argument == "--evidence" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}

// connect authenticates the current installed daemon using the production desktop role.
func connect(ctx context.Context) (*control.Client, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return control.NewClient(bounded, control.ClientConfig{Role: rpc.RoleDesktop})
}

// nonce makes each project name, marker, and mutation intent unique to this invocation.
func nonce() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

// boundedOutput drains child output while retaining a small diagnostic prefix.
type boundedOutput struct{ data []byte }

// Write drains successful child writes without allocating beyond the diagnostic bound.
func (output *boundedOutput) Write(data []byte) (int, error) {
	count := len(data)
	remaining := 64*1024 - len(output.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		output.data = append(output.data, data...)
	}
	return count, nil
}

// command runs a direct argument vector with a finite deadline and bounded diagnostics.
func command(ctx context.Context, executable string, arguments ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bounded, executable, arguments...)
	cmd.WaitDelay = 5 * time.Second
	output := &boundedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", filepath.Base(executable), err, string(output.data))
	}
	return string(output.data), nil
}

// createPeer generates a public released GoForj fixture and customizes only its test App init.
func (driver *recovery) createPeer(ctx context.Context) error {
	forj, err := exec.LookPath("forj")
	if err != nil {
		return err
	}
	version, err := command(ctx, forj, "--version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(version) != "v0.35.1" {
		return fmt.Errorf("forj must report v0.35.1, received %q", version)
	}
	temporary, err := fixtureTemporaryRoot()
	if err != nil {
		return err
	}
	parent, err := os.MkdirTemp(temporary, "harbor-package-ui-recovery-")
	if err != nil {
		return err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	driver.parent = parent
	driver.marker = nonce()
	path := filepath.Join(parent, "project")
	driver.evidence.PeerDirectory = path
	if _, err := command(ctx, forj, "new", "--no-interaction", "--name", "Harbor Recovery Peer "+driver.marker[:8], "--module", "example.com/harbor-recovery-peer", "--path", path, "--components", "cli,web_api", "--atlas", "skip"); err != nil {
		return err
	}
	path, err = fixtureRoot(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(path, "cmd", "app", "harbor_recovery_gate.go"), []byte(gateSource(parent, driver.marker)), 0600); err != nil {
		return err
	}
	driver.peerPath = path
	registration, err := driver.client.RegisterProject(ctx, control.RegisterProjectRequest{Path: path})
	if err != nil {
		return err
	}
	if !registration.Created || registration.Project.Path != path || registration.Project.ID == driver.original {
		return errors.New("peer registration did not create the exact independent fixture")
	}
	driver.peer = registration.Project.ID
	driver.peerPath = path
	return nil
}

// waitFor polls a bounded observation without retaining stale transport clients or blocking cancellation.
func waitFor(ctx context.Context, duration time.Duration, check func(context.Context) (bool, error)) error {
	bounded, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var last error
	for {
		attempt, attemptCancel := context.WithTimeout(bounded, 10*time.Second)
		done, err := check(attempt)
		attemptCancel()
		if done && err == nil {
			return nil
		}
		if err != nil {
			last = err
		}
		select {
		case <-bounded.Done():
			return errors.Join(bounded.Err(), last)
		case <-time.After(350 * time.Millisecond):
		}
	}
}

// project selects one authoritative project without inferring state from files or child output.
func (driver *recovery) project(ctx context.Context, id domain.ProjectID) (domain.ProjectSnapshot, error) {
	snapshot, err := driver.client.Snapshot(ctx)
	if err != nil {
		return domain.ProjectSnapshot{}, err
	}
	for _, project := range snapshot.Projects {
		if project.ID == id {
			return project, nil
		}
	}
	return domain.ProjectSnapshot{}, errors.New("project missing from authoritative snapshot")
}

// resolveAddress requires one OS-resolved loopback IPv4 identity for the stable public hostname.
func resolveAddress(ctx context.Context, host string) (string, error) {
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	distinct := map[string]bool{}
	for _, address := range addresses {
		if address.To4() == nil || !address.IsLoopback() {
			return "", errors.New("project DNS is outside IPv4 loopback")
		}
		distinct[address.String()] = true
	}
	if len(distinct) != 1 {
		return "", errors.New("project DNS must resolve exactly one distinct IPv4 address")
	}
	for address := range distinct {
		return address, nil
	}
	return "", errors.New("empty project DNS")
}

// probe performs an unproxied system-trust HTTP request and always closes its bounded response.
func probe(ctx context.Context, url string, expected string) error {
	status, body, err := probeResponse(ctx, url)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("probe returned HTTP %d", status)
	}
	if expected != "" && body != expected {
		return errors.New("gate listener marker differs")
	}
	return nil
}

// probeResponse bounds direct HTTP observations without converting network failures into route absence.
func probeResponse(ctx context.Context, url string) (int, string, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected probe redirect") }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return 0, "", err
	}
	if len(body) > 4096 {
		return 0, "", errors.New("probe response exceeds bound")
	}
	return response.StatusCode, string(body), nil
}

// validProjectAddress requires a canonical private IPv4 loopback assignment separate from shared ingress.
func validProjectAddress(value, ingress string) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && address.Is4() && address.IsLoopback() && address.String() == value && value != ingress
}

// assignedProjectAddress retains only the agreeing provider address invariants without copying environment files or unrelated values.
func assignedProjectAddress(environment control.ProjectEnvironment, id domain.ProjectID, ingress string) (string, error) {
	if environment.ProjectID != id || !environment.OverridesAvailable {
		return "", errors.New("project address overrides are unavailable or uncorrelated")
	}
	address, serviceAddress := "", ""
	for _, override := range environment.Overrides {
		if override.Name != "IP_ADDRESS" && override.Name != "DEV_SERVICE_IP_ADDRESS" {
			continue
		}
		if override.Source != "runtime.provider" || !validProjectAddress(override.Value, ingress) {
			return "", errors.New("provider project address is invalid")
		}
		if override.Name == "IP_ADDRESS" {
			if address != "" {
				return "", errors.New("provider project address is duplicated")
			}
			address = override.Value
		} else {
			if serviceAddress != "" {
				return "", errors.New("provider service address is duplicated")
			}
			serviceAddress = override.Value
		}
	}
	if address == "" || serviceAddress == "" || address != serviceAddress {
		return "", errors.New("provider project address invariants are missing or disagree")
	}
	return address, nil
}

// processBackedSession recognizes supervised launch evidence without claiming authenticated managed attachment.
// ProjectActivity deliberately omits native process facts; these states require persisted process evidence in Harbor.
func processBackedSession(project projectEvidence) bool {
	return project.SessionID != "" && project.Generation > 0 && (project.SessionState == domain.SessionAwaitingAttach || project.SessionState == domain.SessionAttached)
}

// freshSession requires a successful explicit retry with a new lifecycle and the same public identity.
func freshSession(before, after projectEvidence) error {
	if before.ID != after.ID || before.Path != after.Path || before.Host != after.Host || before.Address != after.Address || before.ProjectAddress != after.ProjectAddress || after.SessionID == before.SessionID || !processBackedSession(after) || after.State != domain.ProjectReady || !after.HTTPS200 {
		return errors.New("explicit retry did not create a fresh Ready session with stable public identity")
	}
	return nil
}

// observe records session and DNS facts and optionally verifies a Ready project's trusted HTTPS route.
func (driver *recovery) observe(ctx context.Context, id domain.ProjectID, healthy bool) (projectEvidence, error) {
	project, err := driver.project(ctx, id)
	if err != nil {
		return projectEvidence{}, err
	}
	result := projectEvidence{ID: id, Path: project.Path, State: project.State, Host: project.Slug + ".test"}
	activity, err := driver.client.ProjectActivity(ctx, control.ProjectActivityRequest{ProjectID: id})
	if err != nil {
		return result, err
	}
	if activity.Session != nil {
		result.SessionID = activity.Session.ID
		result.SessionState = activity.Session.State
		result.Generation = activity.Session.Generation
	}
	result.Address, err = resolveAddress(ctx, result.Host)
	if err != nil {
		return result, err
	}
	environment, err := driver.client.ProjectEnvironment(ctx, control.ProjectEnvironmentRequest{ProjectID: id})
	if err != nil {
		return result, err
	}
	result.ProjectAddress, err = assignedProjectAddress(environment, id, result.Address)
	if err != nil {
		return result, err
	}
	if healthy {
		if project.State != domain.ProjectReady || !processBackedSession(result) {
			return result, errors.New("project is not Ready with a process-backed session")
		}
		if err := probe(ctx, "https://"+result.Host+"/-/ready", ""); err != nil {
			return result, err
		}
		result.HTTPS200 = true
	}
	return result, nil
}

// continuity checks that ordinary peer lifecycle work preserves the original session and public identity.
func continuity(before, after projectEvidence) error {
	if before.ID != after.ID || before.Path != after.Path || before.Host != after.Host || before.Address != after.Address || before.ProjectAddress != after.ProjectAddress || before.SessionID == "" || before.SessionID != after.SessionID || after.Generation < before.Generation || !after.HTTPS200 {
		return errors.New("ordinary peer lifecycle changed the original session or public identity")
	}
	return nil
}

// distinctProjects requires isolated project addresses and sessions while allowing shared public ingress.
func distinctProjects(original, peer projectEvidence) error {
	if original.ID == peer.ID || original.Host == peer.Host || original.ProjectAddress == peer.ProjectAddress || !validProjectAddress(original.ProjectAddress, original.Address) || !validProjectAddress(peer.ProjectAddress, peer.Address) || original.SessionID == "" || peer.SessionID == "" || original.SessionID == peer.SessionID {
		return errors.New("independent projects collided in identity, DNS, or session")
	}
	return nil
}

// healthy waits for the authoritative Ready state and verifies the public route using system trust.
func (driver *recovery) healthy(ctx context.Context, id domain.ProjectID) (projectEvidence, error) {
	var result projectEvidence
	err := waitFor(ctx, 4*time.Minute, func(attempt context.Context) (bool, error) {
		var err error
		result, err = driver.observe(attempt, id, true)
		return err == nil, err
	})
	return result, err
}

// start requests a fresh idempotent start without replaying a failed recovery intent.
func (driver *recovery) start(ctx context.Context, id domain.ProjectID) (domain.Operation, error) {
	response, err := driver.client.StartProject(ctx, control.StartProjectRequest{ProjectID: id, IntentID: domain.IntentID("recovery-start-" + nonce())})
	return response.Operation, err
}

// stop settles only this invocation's peer through the normal lifecycle request.
func (driver *recovery) stop(ctx context.Context) error {
	if _, err := driver.client.StopProject(ctx, control.StopProjectRequest{ProjectID: driver.peer, IntentID: domain.IntentID("recovery-stop-" + nonce())}); err != nil {
		return err
	}
	return waitFor(ctx, 90*time.Second, func(attempt context.Context) (bool, error) {
		project, err := driver.project(attempt, driver.peer)
		if err != nil {
			return false, err
		}
		activity, err := driver.client.ProjectActivity(attempt, control.ProjectActivityRequest{ProjectID: driver.peer})
		return project.State == domain.ProjectStopped && activity.Session == nil, err
	})
}

// crash kills only the admitted authenticated native daemon then reconnects through its installed manager.
func (driver *recovery) crash(ctx context.Context) error {
	before, err := observeInstalledDaemon(ctx, driver.client.Peer())
	if err != nil {
		return err
	}
	index := len(driver.evidence.Crashes)
	driver.evidence.Crashes = append(driver.evidence.Crashes, crashEvidence{Before: before})
	if err := crashInstalledDaemon(ctx, before); err != nil {
		return err
	}
	driver.client.Close()
	driver.client = nil
	return waitFor(ctx, 90*time.Second, func(attempt context.Context) (bool, error) {
		if err := ensureInstalledDaemon(attempt); err != nil {
			return false, err
		}
		client, err := connect(attempt)
		if err != nil {
			return false, err
		}
		after, err := observeInstalledDaemon(attempt, client.Peer())
		if err != nil {
			client.Close()
			return false, err
		}
		if after == before || (after.PID == before.PID && after.Birth == before.Birth) {
			client.Close()
			return false, errors.New("installed daemon native identity did not change")
		}
		status, err := client.Status(attempt)
		if err != nil || status.State != control.DaemonStateReady {
			client.Close()
			return false, errors.Join(err, errors.New("replacement daemon is not ready"))
		}
		driver.client = client
		driver.evidence.Crashes[index].After = after
		return true, nil
	})
}
