package containerruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/containerd/errdefs"
	dockercontext "github.com/docker/go-sdk/context"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	composeProjectLabel          = "com.docker.compose.project"
	composeServiceLabel          = "com.docker.compose.service"
	composeWorkingDirectoryLabel = "com.docker.compose.project.working_dir"
	composeContainerNumberLabel  = "com.docker.compose.container-number"
	composeOneoffLabel           = "com.docker.compose.oneoff"
	maximumProjectContainers     = 512
	maximumRuntimeCandidates     = 4096
	defaultLogReconcilePeriod    = 500 * time.Millisecond
	defaultLogClosePeriod        = 2 * time.Second
	replacementContainerLogTail  = 200
)

// dockerEngine limits the adapter to the Engine calls needed for observation and logs.
type dockerEngine interface {
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	Close() error
}

// DockerRuntime observes the local Docker-compatible Engine without accepting container lifecycle authority.
type DockerRuntime struct {
	engine             dockerEngine
	logReconcilePeriod time.Duration
}

// NewDocker creates a cross-platform Engine client from Docker's standard host and TLS environment.
func NewDocker() (*DockerRuntime, error) {
	host, err := selectedDockerHost()
	if err != nil {
		return nil, fmt.Errorf("resolve current container runtime context: %w", err)
	}
	if err := validateLocalEngineHost(host); err != nil {
		return nil, err
	}
	engine, err := client.New(client.FromEnv, client.WithHost(host))
	if err != nil {
		return nil, fmt.Errorf("configure container runtime client: %w", err)
	}
	return newDockerRuntime(engine), nil
}

// selectedDockerHost keeps Docker's explicit host override ahead of context and rootless auto-discovery.
func selectedDockerHost() (string, error) {
	if host := os.Getenv(dockercontext.EnvOverrideHost); host != "" {
		return host, nil
	}
	return dockercontext.CurrentDockerHost()
}

// validateLocalEngineHost prevents remote daemons from impersonating local checkout-label authority.
func validateLocalEngineHost(host string) error {
	parsed, err := client.ParseHostURL(host)
	if err != nil {
		return fmt.Errorf("parse container runtime host: %w", err)
	}
	wantScheme := "unix"
	if runtime.GOOS == "windows" {
		wantScheme = "npipe"
	}
	if parsed.Scheme != wantScheme {
		return fmt.Errorf("container runtime host must use the local %s transport", wantScheme)
	}
	return nil
}

// newDockerRuntime keeps Engine behavior deterministic behind a mockable package boundary.
func newDockerRuntime(engine dockerEngine) *DockerRuntime {
	if engine == nil {
		panic("containerruntime.newDockerRuntime requires a non-nil Engine client")
	}
	return &DockerRuntime{engine: engine, logReconcilePeriod: defaultLogReconcilePeriod}
}

// ObserveProject returns logical Compose services only after exact canonical checkout admission.
func (runtime *DockerRuntime) ObserveProject(ctx context.Context, checkoutRoot string) (ProjectObservation, error) {
	containers, err := runtime.projectContainers(ctx, checkoutRoot)
	if err != nil {
		return ProjectObservation{}, err
	}
	services := groupProjectServices(containers)
	return ProjectObservation{Services: services}, nil
}

// OpenServiceLogs opens every admitted replica in deterministic order and closes partial results on failure.
func (runtime *DockerRuntime) OpenServiceLogs(
	ctx context.Context,
	checkoutRoot string,
	serviceID string,
	tail int,
) (LogFollower, error) {
	if strings.TrimSpace(serviceID) != serviceID || serviceID == "" {
		return nil, errors.New("container runtime service identity must be canonical")
	}
	if tail < 0 || tail > 10_000 {
		return nil, errors.New("container runtime log tail must be between 0 and 10000")
	}
	containers, err := runtime.projectContainers(ctx, checkoutRoot)
	if err != nil {
		return nil, err
	}
	selected := make([]admittedContainer, 0)
	for _, observed := range containers {
		if observed.serviceID == serviceID {
			selected = append(selected, observed)
		}
	}
	if err := validateTTYLogSelection(selected); err != nil {
		return nil, err
	}
	followerContext, cancel := context.WithCancel(ctx)
	follower := &dockerLogFollower{
		runtime:      runtime,
		ctx:          followerContext,
		cancel:       cancel,
		checkoutRoot: checkoutRoot,
		serviceID:    serviceID,
		tail:         tail,
		initial:      selected,
		changed:      make(chan struct{}),
		copyDone:     make(chan struct{}),
	}
	if len(containers) > 0 {
		follower.projectID = containers[0].projectID
	}
	follower.setAvailable(len(selected) > 0)
	return follower, nil
}

// Close releases idle Engine transports after Harbor has retired every log response body.
func (runtime *DockerRuntime) Close() error {
	return runtime.engine.Close()
}

// admittedContainer retains trusted Compose labels beside ephemeral runtime context during grouping.
type admittedContainer struct {
	projectID string
	serviceID string
	container Container
}

// projectContainers lists label-bearing containers and rejects any whose working directory is not the exact checkout.
func (runtime *DockerRuntime) projectContainers(ctx context.Context, checkoutRoot string) ([]admittedContainer, error) {
	return runtime.containers(ctx, checkoutRoot, "", "")
}

// serviceContainers narrows frequent log reconciliation to one exact Compose service before inspect calls.
func (runtime *DockerRuntime) serviceContainers(
	ctx context.Context,
	checkoutRoot string,
	projectID string,
	serviceID string,
) ([]admittedContainer, error) {
	return runtime.containers(ctx, checkoutRoot, projectID, serviceID)
}

// containers uses one bounded working-directory-label discovery so alternate spellings of the same host path cannot be missed.
func (runtime *DockerRuntime) containers(
	ctx context.Context,
	checkoutRoot string,
	projectID string,
	serviceID string,
) ([]admittedContainer, error) {
	canonicalRoot, err := canonicalRuntimeDirectory(checkoutRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize Harbor checkout: %w", err)
	}
	filters := make(client.Filters).Add("label", composeWorkingDirectoryLabel)
	if projectID != "" {
		filters.Add("label", composeProjectLabel+"="+projectID)
	}
	if serviceID != "" {
		filters.Add("label", composeServiceLabel+"="+serviceID)
	}
	listed, err := runtime.engine.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list Compose containers: %w", err)
	}
	if len(listed.Items) > maximumRuntimeCandidates {
		return nil, fmt.Errorf("host reports more than %d Compose container candidates", maximumRuntimeCandidates)
	}
	result := make([]admittedContainer, 0, len(listed.Items))
	projectIdentity := ""
	for _, summary := range listed.Items {
		admitted, ok, err := runtime.admitContainer(ctx, canonicalRoot, summary)
		if err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if !ok {
			continue
		}
		if len(result) >= maximumProjectContainers {
			return nil, fmt.Errorf("checkout owns more than %d Compose containers", maximumProjectContainers)
		}
		if projectIdentity == "" {
			projectIdentity = admitted.projectID
		} else if projectIdentity != admitted.projectID {
			return nil, errors.New("Compose containers for one checkout report multiple project identities")
		}
		result = append(result, admitted)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].serviceID != result[right].serviceID {
			return result[left].serviceID < result[right].serviceID
		}
		if result[left].container.Replica != result[right].container.Replica {
			return result[left].container.Replica < result[right].container.Replica
		}
		return result[left].container.Name < result[right].container.Name
	})
	return result, nil
}

// admitContainer revalidates immutable inspect labels so neighboring Compose projects cannot cross checkout authority.
func (runtime *DockerRuntime) admitContainer(
	ctx context.Context,
	canonicalRoot string,
	summary container.Summary,
) (admittedContainer, bool, error) {
	if summary.ID == "" || !labelsSelectCheckout(summary.Labels, canonicalRoot) {
		return admittedContainer{}, false, nil
	}
	inspected, err := runtime.engine.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
	if err != nil {
		return admittedContainer{}, false, fmt.Errorf("inspect Compose container %q: %w", summary.ID, err)
	}
	if inspected.Container.Config == nil || inspected.Container.State == nil ||
		!labelsSelectCheckout(inspected.Container.Config.Labels, canonicalRoot) {
		return admittedContainer{}, false, nil
	}
	labels := inspected.Container.Config.Labels
	if strings.EqualFold(strings.TrimSpace(labels[composeOneoffLabel]), "true") {
		return admittedContainer{}, false, nil
	}
	projectID := strings.TrimSpace(labels[composeProjectLabel])
	serviceID := strings.TrimSpace(labels[composeServiceLabel])
	if projectID == "" || serviceID == "" || projectID != labels[composeProjectLabel] || serviceID != labels[composeServiceLabel] {
		return admittedContainer{}, false, nil
	}
	replica, err := strconv.Atoi(labels[composeContainerNumberLabel])
	if err != nil || replica <= 0 {
		replica = 1
	}
	name := strings.TrimPrefix(inspected.Container.Name, "/")
	if name == "" && len(summary.Names) > 0 {
		name = strings.TrimPrefix(summary.Names[0], "/")
	}
	health := "none"
	if inspected.Container.State.Health != nil {
		health = string(inspected.Container.State.Health.Status)
	}
	ports := make([]Port, 0, len(summary.Ports))
	for _, port := range summary.Ports {
		address := ""
		if port.IP.IsValid() {
			address = port.IP.String()
		}
		ports = append(ports, Port{
			Address:  address,
			Private:  port.PrivatePort,
			Public:   port.PublicPort,
			Protocol: port.Type,
		})
	}
	return admittedContainer{
		projectID: projectID,
		serviceID: serviceID,
		container: Container{
			ID:       inspected.Container.ID,
			Name:     name,
			Image:    inspected.Container.Config.Image,
			State:    string(inspected.Container.State.Status),
			Health:   health,
			ExitCode: inspected.Container.State.ExitCode,
			Replica:  replica,
			TTY:      inspected.Container.Config.Tty,
			Ports:    ports,
		},
	}, true, nil
}

// labelsSelectCheckout compares canonical host paths rather than trusting a Compose project name alone.
func labelsSelectCheckout(labels map[string]string, canonicalRoot string) bool {
	if labels == nil {
		return false
	}
	workingDirectory := labels[composeWorkingDirectoryLabel]
	if workingDirectory == "" {
		return false
	}
	canonicalLabel, err := canonicalRuntimeDirectory(workingDirectory)
	if err != nil {
		return false
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		return false
	}
	labelInfo, err := os.Stat(canonicalLabel)
	return err == nil && os.SameFile(rootInfo, labelInfo)
}

// canonicalRuntimeDirectory collapses symlinks before a host path can admit container runtime facts.
func canonicalRuntimeDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// groupProjectServices projects ephemeral replicas into stable logical Compose service summaries.
func groupProjectServices(containers []admittedContainer) []Service {
	grouped := make(map[string]*Service)
	for _, observed := range containers {
		service := grouped[observed.serviceID]
		if service == nil {
			service = &Service{
				ID:         observed.serviceID,
				Name:       observed.serviceID,
				Project:    observed.projectID,
				Containers: make([]Container, 0),
			}
			grouped[observed.serviceID] = service
		}
		service.Containers = append(service.Containers, observed.container)
	}
	services := make([]Service, 0, len(grouped))
	for _, service := range grouped {
		service.State, service.Health, service.Active = aggregateServiceState(service.Containers)
		services = append(services, *service)
	}
	sort.Slice(services, func(left, right int) bool { return services[left].ID < services[right].ID })
	return services
}

// aggregateServiceState reports the most actionable replica state without hiding unhealthy peers.
func aggregateServiceState(containers []Container) (string, string, bool) {
	state := "stopped"
	severity := 0
	health := "none"
	active := false
	setState := func(candidate string, candidateSeverity int) {
		if candidateSeverity > severity {
			state = candidate
			severity = candidateSeverity
		}
	}
	for _, observed := range containers {
		switch observed.State {
		case string(container.StateRunning):
			active = true
			setState("ready", 1)
		case string(container.StateCreated), string(container.StateRestarting):
			active = true
			setState("working", 2)
		case string(container.StatePaused):
			active = true
			setState("degraded", 3)
		case string(container.StateDead):
			active = true
			setState("failed", 4)
		case string(container.StateExited):
			if observed.ExitCode != 0 {
				active = true
				setState("failed", 4)
			}
		}
		switch observed.Health {
		case string(container.Unhealthy):
			health = "unhealthy"
			setState("degraded", 3)
		case string(container.Starting):
			if health != "unhealthy" {
				health = "starting"
			}
			setState("working", 2)
		case string(container.Healthy):
			if health == "none" {
				health = "healthy"
			}
		}
	}
	return state, health, active
}

// dockerLogFollower reconciles ephemeral container IDs while preserving one monotonic Harbor transcript.
type dockerLogFollower struct {
	runtime        *DockerRuntime
	ctx            context.Context
	cancel         context.CancelFunc
	checkoutRoot   string
	serviceID      string
	projectID      string
	tail           int
	initial        []admittedContainer
	available      atomic.Bool
	availabilityMu sync.Mutex
	changed        chan struct{}
	copyOnce       sync.Once
	copyDoneOnce   sync.Once
	closeOnce      sync.Once
	copyMu         sync.Mutex
	copyStarted    bool
	closed         bool
	copyDone       chan struct{}
	copyErr        error
}

// Available reports whether the latest canonical label selection contains a current replica.
func (follower *dockerLogFollower) Available() bool {
	return follower.available.Load()
}

// WaitAvailable waits on a generation signal so an initially empty service can become live without polling the UI.
func (follower *dockerLogFollower) WaitAvailable(ctx context.Context) error {
	return follower.waitState(ctx, true)
}

// WaitStateChange waits on a generation signal so held reads wake when containers appear or disappear.
func (follower *dockerLogFollower) WaitStateChange(ctx context.Context, available bool) error {
	for {
		if follower.Available() != available {
			return nil
		}
		follower.availabilityMu.Lock()
		if follower.Available() != available {
			follower.availabilityMu.Unlock()
			return nil
		}
		changed := follower.changed
		follower.availabilityMu.Unlock()
		select {
		case <-changed:
		case <-follower.ctx.Done():
			return follower.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitState waits for one requested availability value without exposing generation channels.
func (follower *dockerLogFollower) waitState(ctx context.Context, available bool) error {
	for {
		if follower.Available() == available {
			return nil
		}
		follower.availabilityMu.Lock()
		if follower.Available() == available {
			follower.availabilityMu.Unlock()
			return nil
		}
		changed := follower.changed
		follower.availabilityMu.Unlock()
		select {
		case <-changed:
		case <-follower.ctx.Done():
			return follower.ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// setAvailable advances the wait generation only when container selection changes.
func (follower *dockerLogFollower) setAvailable(available bool) {
	previous := follower.available.Swap(available)
	if previous == available {
		return
	}
	follower.availabilityMu.Lock()
	close(follower.changed)
	follower.changed = make(chan struct{})
	follower.availabilityMu.Unlock()
}

// CopyTo follows each immutable container ID at most once while polling only the admitted checkout for replacements.
func (follower *dockerLogFollower) CopyTo(destination io.Writer) error {
	follower.copyOnce.Do(func() {
		follower.copyMu.Lock()
		if follower.closed {
			follower.copyMu.Unlock()
			follower.copyDoneOnce.Do(func() { close(follower.copyDone) })
			return
		}
		follower.copyStarted = true
		follower.copyMu.Unlock()
		defer follower.copyDoneOnce.Do(func() { close(follower.copyDone) })
		follower.copyErr = follower.copyTo(destination)
	})
	return follower.copyErr
}

// copyTo owns per-container cancellations so one exited replica cannot block discovery of its replacement.
func (follower *dockerLogFollower) copyTo(destination io.Writer) error {
	destination = &synchronizedLogWriter{destination: destination}
	type sourceResult struct {
		id  string
		err error
	}
	results := make(chan sourceResult, maximumProjectContainers)
	active := make(map[string]context.CancelFunc)
	seen := make(map[string]struct{})
	var sources sync.WaitGroup
	start := func(observed admittedContainer, tail int) error {
		if _, exists := seen[observed.container.ID]; exists {
			return nil
		}
		if len(seen) >= maximumProjectContainers {
			return fmt.Errorf("service log follower observed more than %d container identities", maximumProjectContainers)
		}
		seen[observed.container.ID] = struct{}{}
		sourceContext, cancel := context.WithCancel(follower.ctx)
		active[observed.container.ID] = cancel
		sources.Add(1)
		go func() {
			defer sources.Done()
			err := follower.copyContainerLogs(sourceContext, observed.container, tail, destination)
			if sourceContext.Err() != nil || errdefs.IsNotFound(err) {
				err = nil
			}
			select {
			case results <- sourceResult{id: observed.container.ID, err: err}:
			case <-follower.ctx.Done():
			}
		}()
		return nil
	}
	for _, observed := range follower.initial {
		if err := start(observed, follower.tail); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(follower.runtime.logReconcilePeriod)
	defer ticker.Stop()
	defer func() {
		for _, cancel := range active {
			cancel()
		}
		sources.Wait()
	}()
	for {
		select {
		case <-follower.ctx.Done():
			return nil
		case result := <-results:
			delete(active, result.id)
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				return result.err
			}
		case <-ticker.C:
			containers, err := follower.runtime.serviceContainers(
				follower.ctx,
				follower.checkoutRoot,
				follower.projectID,
				follower.serviceID,
			)
			if err != nil {
				if follower.ctx.Err() != nil {
					return nil
				}
				return err
			}
			next := make(map[string]struct{})
			if err := validateTTYLogSelection(containers); err != nil {
				return err
			}
			for _, observed := range containers {
				if observed.serviceID != follower.serviceID {
					continue
				}
				next[observed.container.ID] = struct{}{}
				if follower.projectID == "" {
					follower.projectID = observed.projectID
				}
				tail := follower.tail
				if tail == 0 {
					tail = replacementContainerLogTail
				}
				if err := start(observed, tail); err != nil {
					return err
				}
			}
			follower.setAvailable(len(next) > 0)
			for id, cancel := range active {
				if _, exists := next[id]; !exists {
					cancel()
					delete(active, id)
				}
			}
		}
	}
}

// copyContainerLogs decodes TTY or multiplexed Engine framing and prefixes every complete line with replica context.
func (follower *dockerLogFollower) copyContainerLogs(
	ctx context.Context,
	observed Container,
	tail int,
	destination io.Writer,
) error {
	logs, err := follower.runtime.engine.ContainerLogs(ctx, observed.ID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return fmt.Errorf("open container %q logs: %w", observed.Name, err)
	}
	defer logs.Close()
	if observed.TTY {
		_, err = io.Copy(destination, logs)
	} else {
		stdout := &containerLineWriter{
			destination:        destination,
			prefix:             containerLogPrefix(observed, "stdout"),
			lineStart:          true,
			attributeFragments: true,
		}
		stderr := &containerLineWriter{
			destination:        destination,
			prefix:             containerLogPrefix(observed, "stderr"),
			lineStart:          true,
			attributeFragments: true,
		}
		_, err = stdcopy.StdCopy(stdout, stderr, logs)
		err = errors.Join(err, stdout.flush(), stderr.flush())
	}
	return err
}

// Close cancels polling and every Engine log request exactly once.
func (follower *dockerLogFollower) Close() error {
	follower.closeOnce.Do(func() {
		follower.copyMu.Lock()
		follower.closed = true
		follower.copyMu.Unlock()
		follower.cancel()
		follower.availabilityMu.Lock()
		close(follower.changed)
		follower.changed = make(chan struct{})
		follower.availabilityMu.Unlock()
	})
	follower.copyMu.Lock()
	started := follower.copyStarted
	follower.copyMu.Unlock()
	if started {
		select {
		case <-follower.copyDone:
		case <-time.After(defaultLogClosePeriod):
			return fmt.Errorf("container log follower did not stop within %s", defaultLogClosePeriod)
		}
	}
	return nil
}

// synchronizedLogWriter serializes independently decoded replica frames before Harbor's transcript writer.
type synchronizedLogWriter struct {
	mu          sync.Mutex
	destination io.Writer
}

// Write keeps frames from concurrent replicas contiguous.
func (writer *synchronizedLogWriter) Write(output []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.destination.Write(output)
}

// containerLineWriter retains incomplete UTF-8 across Engine reads while applying per-line attribution.
type containerLineWriter struct {
	destination        io.Writer
	prefix             []byte
	pending            []byte
	lineStart          bool
	attributeFragments bool
}

// Write formats only complete UTF-8 so transport chunking cannot introduce replacement characters.
func (writer *containerLineWriter) Write(output []byte) (int, error) {
	// Docker stdout and stderr frames may alternate before either emits a newline, so every non-TTY frame retains its own attribution.
	if writer.attributeFragments && len(output) > 0 && !writer.lineStart {
		writer.lineStart = true
	}
	combined := append(append([]byte(nil), writer.pending...), output...)
	complete := completeUTF8LogPrefix(combined)
	writer.pending = append(writer.pending[:0], combined[complete:]...)
	formatted := writer.format(combined[:complete])
	_, err := writer.destination.Write(formatted)
	return len(output), err
}

// flush emits a trailing incomplete encoding as one deliberate replacement at source EOF.
func (writer *containerLineWriter) flush() error {
	if len(writer.pending) == 0 {
		return nil
	}
	formatted := writer.format(bytes.ToValidUTF8(writer.pending, []byte("\uFFFD")))
	writer.pending = nil
	_, err := writer.destination.Write(formatted)
	return err
}

// format inserts attribution only at logical line boundaries.
func (writer *containerLineWriter) format(output []byte) []byte {
	formatted := make([]byte, 0, len(output)+len(writer.prefix))
	for _, character := range output {
		if writer.lineStart {
			formatted = append(formatted, writer.prefix...)
			writer.lineStart = false
		}
		formatted = append(formatted, character)
		if character == '\n' {
			writer.lineStart = true
		}
	}
	return formatted
}

// completeUTF8LogPrefix returns the longest prefix that cannot end inside an encoding.
func completeUTF8LogPrefix(output []byte) int {
	if len(output) == 0 {
		return 0
	}
	start := len(output) - 1
	minimum := len(output) - utf8.UTFMax
	if minimum < 0 {
		minimum = 0
	}
	for start > minimum && !utf8.RuneStart(output[start]) {
		start--
	}
	if utf8.RuneStart(output[start]) && !utf8.FullRune(output[start:]) {
		return start
	}
	return len(output)
}

// validateTTYLogSelection keeps cursor-addressed terminal bytes intact by refusing an inherently ambiguous raw-stream merge.
func validateTTYLogSelection(containers []admittedContainer) error {
	if len(containers) <= 1 {
		return nil
	}
	for _, observed := range containers {
		if observed.container.TTY {
			return errors.New("multiple service replicas cannot share one raw TTY log stream")
		}
	}
	return nil
}

// containerLogName chooses human attribution without exposing an opaque runtime ID.
func containerLogName(observed Container) string {
	if strings.TrimSpace(observed.Name) == observed.Name && observed.Name != "" {
		return observed.Name
	}
	if observed.Replica > 0 {
		return fmt.Sprintf("replica-%d", observed.Replica)
	}
	return "container"
}

// containerLogPrefix retains Docker stream provenance while keeping TTY output's intentionally combined stream concise.
func containerLogPrefix(observed Container, stream string) []byte {
	name := containerLogName(observed)
	if stream == "" {
		return []byte("[" + name + "] ")
	}
	return []byte("[" + name + " " + stream + "] ")
}
