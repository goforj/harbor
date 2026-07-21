package containerruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	dockercontext "github.com/docker/go-sdk/context"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// fakeDockerEngine supplies deterministic Engine summaries, inspections, and logs.
type fakeDockerEngine struct {
	listed       client.ContainerListResult
	listSequence []client.ContainerListResult
	inspections  map[string]client.ContainerInspectResult
	logs         map[string]client.ContainerLogsResult
	logOutputs   map[string]string
	logFactories map[string]func(context.Context) client.ContainerLogsResult
	logCalls     map[string]int
	listCalls    int
	listOptions  client.ContainerListOptions
	mu           sync.Mutex
	closed       bool
}

// fakeDockerEventEngine adds a deterministic global event stream to the observation fixture.
type fakeDockerEventEngine struct {
	*fakeDockerEngine
	eventResult  client.EventsResult
	eventOptions client.EventsListOptions
	mu           sync.Mutex
}

// Events records the narrow container-event filter and returns the configured stream.
func (engine *fakeDockerEventEngine) Events(_ context.Context, options client.EventsListOptions) client.EventsResult {
	engine.mu.Lock()
	engine.eventOptions = options
	engine.mu.Unlock()
	return engine.eventResult
}

// ContainerList records the reviewed label filter and returns configured summaries.
func (engine *fakeDockerEngine) ContainerList(
	_ context.Context,
	options client.ContainerListOptions,
) (client.ContainerListResult, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.listOptions = options
	engine.listCalls++
	if len(engine.listSequence) > 0 {
		index := engine.listCalls - 1
		if index >= len(engine.listSequence) {
			index = len(engine.listSequence) - 1
		}
		return engine.listSequence[index], nil
	}
	return engine.listed, nil
}

// ContainerInspect returns immutable facts for one configured container ID.
func (engine *fakeDockerEngine) ContainerInspect(
	_ context.Context,
	containerID string,
	_ client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	return engine.inspections[containerID], nil
}

// ContainerLogs returns one configured response body.
func (engine *fakeDockerEngine) ContainerLogs(
	ctx context.Context,
	containerID string,
	_ client.ContainerLogsOptions,
) (client.ContainerLogsResult, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.logCalls == nil {
		engine.logCalls = make(map[string]int)
	}
	engine.logCalls[containerID]++
	if factory := engine.logFactories[containerID]; factory != nil {
		return factory(ctx), nil
	}
	if output, exists := engine.logOutputs[containerID]; exists {
		return &testLogResult{Reader: strings.NewReader(output)}, nil
	}
	return engine.logs[containerID], nil
}

// Close records transport cleanup.
func (engine *fakeDockerEngine) Close() error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.closed = true
	return nil
}

// TestDockerRuntimeWaitProjectChangeUsesEventsOnlyAsAWakeHint verifies the stream is filtered to containers and the
// caller receives no untrusted project identity from the event payload.
func TestDockerRuntimeWaitProjectChangeUsesEventsOnlyAsAWakeHint(t *testing.T) {
	root := t.TempDir()
	messages := make(chan events.Message, 1)
	messages <- events.Message{}
	engine := &fakeDockerEventEngine{
		fakeDockerEngine: &fakeDockerEngine{logs: make(map[string]client.ContainerLogsResult)},
		eventResult: client.EventsResult{
			Messages: messages,
			Err:      make(chan error),
		},
	}

	if err := newDockerRuntime(engine).WaitProjectChange(t.Context(), root); err != nil {
		t.Fatalf("WaitProjectChange() error = %v", err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if !engine.eventOptions.Filters["type"]["container"] {
		t.Fatalf("Events() filters = %#v, want container-only wake filter", engine.eventOptions.Filters)
	}
}

// TestDockerRuntimeWaitProjectChangePropagatesStreamErrors ensures Engine failures do not look like topology changes.
func TestDockerRuntimeWaitProjectChangePropagatesStreamErrors(t *testing.T) {
	root := t.TempDir()
	errorsCh := make(chan error, 1)
	errorsCh <- errors.New("event transport failed")
	engine := &fakeDockerEventEngine{
		fakeDockerEngine: &fakeDockerEngine{logs: make(map[string]client.ContainerLogsResult)},
		eventResult: client.EventsResult{
			Messages: make(chan events.Message),
			Err:      errorsCh,
		},
	}

	err := newDockerRuntime(engine).WaitProjectChange(t.Context(), root)
	if err == nil || !errors.Is(err, ErrProjectChangeTransient) || !strings.Contains(err.Error(), "event transport failed") {
		t.Fatalf("WaitProjectChange() error = %v, want stream failure", err)
	}
}

// TestDockerRuntimeWaitProjectChangeRejectsEnginesWithoutEvents keeps the event capability optional for narrow fakes.
func TestDockerRuntimeWaitProjectChangeRejectsEnginesWithoutEvents(t *testing.T) {
	root := t.TempDir()
	engine := &fakeDockerEngine{logs: make(map[string]client.ContainerLogsResult)}
	if err := newDockerRuntime(engine).WaitProjectChange(t.Context(), root); !errors.Is(err, ErrProjectChangeUnsupported) {
		t.Fatalf("WaitProjectChange() error = %v, want unsupported capability", err)
	}
}

// TestDockerRuntimeAdmitsOnlyTheCanonicalCheckout rejects neighboring projects even when service names collide.
func TestDockerRuntimeAdmitsOnlyTheCanonicalCheckout(t *testing.T) {
	root := t.TempDir()
	neighbor := t.TempDir()
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{
			{ID: "orders-db", Labels: composeLabels(root, "orders", "db", "1")},
			{ID: "neighbor-db", Labels: composeLabels(neighbor, "neighbor", "db", "1")},
			{ID: "foreign", Labels: map[string]string{composeServiceLabel: "db"}},
		}},
		inspections: map[string]client.ContainerInspectResult{
			"orders-db":   inspectResult("orders-db", "orders-db-1", composeLabels(root, "orders", "db", "1"), container.StateRunning, 0),
			"neighbor-db": inspectResult("neighbor-db", "neighbor-db-1", composeLabels(neighbor, "neighbor", "db", "1"), container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}
	runtime := newDockerRuntime(engine)

	observation, err := runtime.ObserveProject(t.Context(), root)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if len(observation.Services) != 1 || observation.Services[0].ID != "db" || len(observation.Services[0].Containers) != 1 {
		t.Fatalf("ObserveProject() = %#v", observation)
	}
	if observation.Services[0].Containers[0].ID != "orders-db" {
		t.Fatalf("admitted container = %#v", observation.Services[0].Containers[0])
	}
	canonicalRoot, err := canonicalRuntimeDirectory(root)
	if err != nil {
		t.Fatalf("canonicalRuntimeDirectory() error = %v", err)
	}
	if !engine.listOptions.All || !engine.listOptions.Filters["label"][composeWorkingDirectoryLabel] {
		t.Fatalf("ContainerList() options = %#v", engine.listOptions)
	}
	if engine.listOptions.Filters["label"][composeWorkingDirectoryLabel+"="+canonicalRoot] {
		t.Fatalf("ContainerList() prematurely narrowed canonical candidates = %#v", engine.listOptions)
	}
}

// TestDockerRuntimeRevalidatesInspectLabels rejects a container whose trusted working-directory label changed between list and inspect.
func TestDockerRuntimeRevalidatesInspectLabels(t *testing.T) {
	root := t.TempDir()
	neighbor := t.TempDir()
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{{
			ID:     "changed",
			Labels: composeLabels(root, "orders", "db", "1"),
		}}},
		inspections: map[string]client.ContainerInspectResult{
			"changed": inspectResult("changed", "changed", composeLabels(neighbor, "neighbor", "db", "1"), container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}

	observation, err := newDockerRuntime(engine).ObserveProject(t.Context(), root)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if observation.Services == nil || len(observation.Services) != 0 {
		t.Fatalf("ObserveProject() = %#v, want initialized empty services", observation)
	}
}

// TestDockerRuntimeAdmitsASymlinkedCheckout proves string label differences cannot defeat canonical host ownership.
func TestDockerRuntimeAdmitsASymlinkedCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating checkout symlinks requires host policy not guaranteed on Windows CI")
	}
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	labels := composeLabels(realRoot, "orders", "db", "1")
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{{ID: "orders-db", Labels: labels}}},
		inspections: map[string]client.ContainerInspectResult{
			"orders-db": inspectResult("orders-db", "orders-db-1", labels, container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}

	observation, err := newDockerRuntime(engine).ObserveProject(t.Context(), linkRoot)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if len(observation.Services) != 1 || observation.Services[0].ID != "db" {
		t.Fatalf("ObserveProject() = %#v", observation)
	}
}

// TestDockerRuntimeIncludesEveryCanonicalLabelSpelling prevents a partial exact match from hiding equivalent containers.
func TestDockerRuntimeIncludesEveryCanonicalLabelSpelling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating checkout symlinks requires host policy not guaranteed on Windows CI")
	}
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	realLabels := composeLabels(realRoot, "orders", "db", "1")
	linkLabels := composeLabels(linkRoot, "orders", "db", "2")
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{
			{ID: "orders-db-one", Labels: realLabels},
			{ID: "orders-db-two", Labels: linkLabels},
		}},
		inspections: map[string]client.ContainerInspectResult{
			"orders-db-one": inspectResult("orders-db-one", "orders-db-1", realLabels, container.StateRunning, 0),
			"orders-db-two": inspectResult("orders-db-two", "orders-db-2", linkLabels, container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}

	observation, err := newDockerRuntime(engine).ObserveProject(t.Context(), realRoot)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if len(observation.Services) != 1 || len(observation.Services[0].Containers) != 2 {
		t.Fatalf("ObserveProject() = %#v, want both canonical replicas", observation)
	}
}

// TestDockerRuntimeRejectsAmbiguousComposeProjectIdentity prevents one checkout from blending unrelated Compose lifecycles.
func TestDockerRuntimeRejectsAmbiguousComposeProjectIdentity(t *testing.T) {
	root := t.TempDir()
	first := composeLabels(root, "orders", "db", "1")
	second := composeLabels(root, "orders-next", "cache", "1")
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{
			{ID: "orders-db", Labels: first},
			{ID: "orders-cache", Labels: second},
		}},
		inspections: map[string]client.ContainerInspectResult{
			"orders-db":    inspectResult("orders-db", "orders-db-1", first, container.StateRunning, 0),
			"orders-cache": inspectResult("orders-cache", "orders-cache-1", second, container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}

	_, err := newDockerRuntime(engine).ObserveProject(t.Context(), root)
	if err == nil || !strings.Contains(err.Error(), "multiple project identities") {
		t.Fatalf("ObserveProject() error = %v", err)
	}
}

// TestDockerRuntimeExcludesOneOffContainers keeps transient Compose commands out of durable logical services.
func TestDockerRuntimeExcludesOneOffContainers(t *testing.T) {
	root := t.TempDir()
	labels := composeLabels(root, "orders", "migrate", "1")
	labels[composeOneoffLabel] = "true"
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{{ID: "orders-migrate", Labels: labels}}},
		inspections: map[string]client.ContainerInspectResult{
			"orders-migrate": inspectResult("orders-migrate", "orders-migrate-1", labels, container.StateExited, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
	}

	observation, err := newDockerRuntime(engine).ObserveProject(t.Context(), root)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if len(observation.Services) != 0 {
		t.Fatalf("ObserveProject() = %#v, want no one-off services", observation)
	}
}

// TestAggregateServiceStatePreservesTheMostActionableReplicaFailure prevents healthy peers from hiding a failed replica.
func TestAggregateServiceStatePreservesTheMostActionableReplicaFailure(t *testing.T) {
	state, health, active := aggregateServiceState([]Container{
		{State: string(container.StateExited), ExitCode: 2, Health: "none"},
		{State: string(container.StateRunning), Health: string(container.Healthy)},
		{State: string(container.StatePaused), Health: "none"},
	})
	if state != "failed" || health != "healthy" || !active {
		t.Fatalf("aggregateServiceState() = %q, %q, %v", state, health, active)
	}
}

// TestDockerLogSourceDecodesMultiplexedStreams keeps one-replica logs free of Engine framing and source decoration.
func TestDockerLogSourceDecodesMultiplexedStreams(t *testing.T) {
	encoded := new(bytes.Buffer)
	writeMultiplexedTestFrame(t, encoded, 1, []byte("database ready\n"))
	writeMultiplexedTestFrame(t, encoded, 2, []byte("slow query\n"))
	engine := &fakeDockerEngine{
		logs: map[string]client.ContainerLogsResult{
			"orders-db": &testLogResult{Reader: bytes.NewReader(encoded.Bytes())},
		},
	}
	output := new(bytes.Buffer)
	follower := &dockerLogFollower{runtime: newDockerRuntime(engine), tail: 200}
	err := follower.copyContainerLogs(t.Context(), Container{
		ID: "orders-db", Name: "orders-db-1", Replica: 1,
	}, 200, output)
	if err != nil {
		t.Fatalf("copyContainerLogs() error = %v", err)
	}
	if output.String() != "database ready\nslow query\n" {
		t.Fatalf("decoded output = %q", output.String())
	}
}

// TestDockerLogSourceAttributesConcurrentReplicas keeps interleaved multi-replica output identifiable.
func TestDockerLogSourceAttributesConcurrentReplicas(t *testing.T) {
	encoded := new(bytes.Buffer)
	writeMultiplexedTestFrame(t, encoded, 1, []byte("database ready\n"))
	writeMultiplexedTestFrame(t, encoded, 2, []byte("slow query\n"))
	engine := &fakeDockerEngine{logs: map[string]client.ContainerLogsResult{
		"orders-db": &testLogResult{Reader: bytes.NewReader(encoded.Bytes())},
	}}
	output := new(bytes.Buffer)
	follower := &dockerLogFollower{runtime: newDockerRuntime(engine), tail: 200}
	follower.attributeSources.Store(true)
	if err := follower.copyContainerLogs(t.Context(), Container{ID: "orders-db", Name: "orders-db-1", Replica: 1}, 200, output); err != nil {
		t.Fatalf("copyContainerLogs() error = %v", err)
	}
	if output.String() != "[orders-db-1 stdout] database ready\n[orders-db-1 stderr] slow query\n" {
		t.Fatalf("attributed output = %q", output.String())
	}
}

// TestContainerLineWriterRetainsSplitUTF8AndANSI verifies Engine chunking does not corrupt terminal output.
func TestContainerLineWriterRetainsSplitUTF8AndANSI(t *testing.T) {
	output := new(bytes.Buffer)
	writer := &containerLineWriter{
		destination: output,
		prefix:      []byte("[orders-db-1] "),
		lineStart:   true,
	}
	encoded := []byte("\x1b[32mready ⚓\x1b[0m\n")
	split := bytes.Index(encoded, []byte("⚓")) + 1
	if _, err := writer.Write(encoded[:split]); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if _, err := writer.Write(encoded[split:]); err != nil {
		t.Fatalf("Write(second) error = %v", err)
	}
	if err := writer.flush(); err != nil {
		t.Fatalf("flush() error = %v", err)
	}
	want := "[orders-db-1] " + string(encoded)
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

// TestValidateLocalEngineHostRejectsRemoteTransports preserves local checkout labels as host ownership evidence.
func TestValidateLocalEngineHostRejectsRemoteTransports(t *testing.T) {
	for _, host := range []string{"tcp://127.0.0.1:2375", "tcp://docker.example.com:2376", "ssh://builder.example.com"} {
		if err := validateLocalEngineHost(host); err == nil {
			t.Fatalf("validateLocalEngineHost(%q) error = nil", host)
		}
	}
	local := "unix:///var/run/docker.sock"
	if runtime.GOOS == "windows" {
		local = "npipe:////./pipe/docker_engine"
	}
	if err := validateLocalEngineHost(local); err != nil {
		t.Fatalf("validateLocalEngineHost(%q) error = %v", local, err)
	}
}

// TestNewDockerUsesTheCLISelectedContext prevents Harbor from silently observing a different local Engine than Compose.
func TestNewDockerUsesTheCLISelectedContext(t *testing.T) {
	dockercontext.SetupTestDockerContexts(t, 1, 1)
	t.Setenv("DOCKER_HOST", "")
	_, err := NewDocker()
	if err == nil || !strings.Contains(err.Error(), "local") {
		t.Fatalf("NewDocker() error = %v, want selected TCP context rejection", err)
	}
}

// TestNewDockerGivesDockerHostPrecedenceOverTheCLIContext matches Docker's documented environment selection order.
func TestNewDockerGivesDockerHostPrecedenceOverTheCLIContext(t *testing.T) {
	dockercontext.SetupTestDockerContexts(t, 1, 1)
	host := "unix:///tmp/harbor-docker-test.sock"
	if runtime.GOOS == "windows" {
		host = "npipe:////./pipe/harbor-docker-test"
	}
	t.Setenv("DOCKER_HOST", host)
	configured, err := NewDocker()
	if err != nil {
		t.Fatalf("NewDocker() error = %v", err)
	}
	if err := configured.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestDockerLogFollowerDoesNotReplayTheSameFiniteContainer verifies polling never reopens one ended immutable ID.
func TestDockerLogFollowerDoesNotReplayTheSameFiniteContainer(t *testing.T) {
	root := t.TempDir()
	summary := container.Summary{ID: "db-one", Labels: composeLabels(root, "orders", "db", "1")}
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{summary}},
		inspections: map[string]client.ContainerInspectResult{
			"db-one": inspectResult("db-one", "orders-db-1", summary.Labels, container.StateExited, 1),
		},
		logs:       make(map[string]client.ContainerLogsResult),
		logOutputs: map[string]string{"db-one": rawTTYLog("failed once\n")},
	}
	runtime := newDockerRuntime(engine)
	runtime.logReconcilePeriod = 10 * time.Millisecond
	follower, err := runtime.OpenServiceLogs(t.Context(), root, "db", 200)
	if err != nil {
		t.Fatalf("OpenServiceLogs() error = %v", err)
	}
	output := new(bytes.Buffer)
	done := make(chan error, 1)
	go func() { done <- follower.CopyTo(output) }()
	time.Sleep(60 * time.Millisecond)
	if err := follower.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	engine.mu.Lock()
	calls := engine.logCalls["db-one"]
	engine.mu.Unlock()
	if calls != 1 || strings.Count(output.String(), "failed once") != 1 {
		t.Fatalf("same-ID log calls/output = %d / %q", calls, output.String())
	}
}

// TestDockerLogFollowerAddsAReplacementIDOnce verifies one replica EOF cannot block a recreated container.
func TestDockerLogFollowerAddsAReplacementIDOnce(t *testing.T) {
	root := t.TempDir()
	first := container.Summary{ID: "db-one", Labels: composeLabels(root, "orders", "db", "1")}
	second := container.Summary{ID: "db-two", Labels: composeLabels(root, "orders", "db", "1")}
	engine := &fakeDockerEngine{
		listSequence: []client.ContainerListResult{
			{Items: []container.Summary{first}},
			{Items: []container.Summary{first}},
			{Items: []container.Summary{second}},
		},
		inspections: map[string]client.ContainerInspectResult{
			"db-one": inspectResult("db-one", "orders-db-1", first.Labels, container.StateExited, 1),
			"db-two": inspectResult("db-two", "orders-db-1", second.Labels, container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
		logOutputs: map[string]string{
			"db-one": rawTTYLog("old\n"),
			"db-two": rawTTYLog("replacement\n"),
		},
	}
	runtime := newDockerRuntime(engine)
	runtime.logReconcilePeriod = 10 * time.Millisecond
	follower, err := runtime.OpenServiceLogs(t.Context(), root, "db", 200)
	if err != nil {
		t.Fatalf("OpenServiceLogs() error = %v", err)
	}
	output := new(synchronizedTestBuffer)
	done := make(chan error, 1)
	go func() { done <- follower.CopyTo(output) }()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), "replacement") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = follower.Close()
	if err := <-done; err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	engine.mu.Lock()
	firstCalls, secondCalls := engine.logCalls["db-one"], engine.logCalls["db-two"]
	engine.mu.Unlock()
	if firstCalls != 1 || secondCalls != 1 || !strings.Contains(output.String(), "replacement") {
		t.Fatalf("replacement calls/output = %d/%d / %q", firstCalls, secondCalls, output.String())
	}
}

// TestDockerLogFollowerCloseJoinsActiveReaders proves daemon shutdown cannot leave an Engine body behind.
func TestDockerLogFollowerCloseJoinsActiveReaders(t *testing.T) {
	root := t.TempDir()
	summary := container.Summary{ID: "db-one", Labels: composeLabels(root, "orders", "db", "1")}
	opened := make(chan *cancellableTestLogResult, 1)
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{summary}},
		inspections: map[string]client.ContainerInspectResult{
			"db-one": inspectResult("db-one", "orders-db-1", summary.Labels, container.StateRunning, 0),
		},
		logs: make(map[string]client.ContainerLogsResult),
		logFactories: map[string]func(context.Context) client.ContainerLogsResult{
			"db-one": func(ctx context.Context) client.ContainerLogsResult {
				result := newCancellableTestLogResult(ctx)
				opened <- result
				return result
			},
		},
	}
	follower, err := newDockerRuntime(engine).OpenServiceLogs(t.Context(), root, "db", 200)
	if err != nil {
		t.Fatalf("OpenServiceLogs() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- follower.CopyTo(io.Discard) }()
	result := <-opened
	if err := follower.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CopyTo() error = %v", err)
		}
	default:
		t.Fatal("Close() returned before joining the active Engine reader")
	}
	select {
	case <-result.closed:
	default:
		t.Fatal("Close() returned before closing the Engine response body")
	}
}

// TestDockerLogFollowerCloseBeforeCopyPreventsLateEngineReaders closes the ready-to-copy race during route teardown.
func TestDockerLogFollowerCloseBeforeCopyPreventsLateEngineReaders(t *testing.T) {
	root := t.TempDir()
	summary := container.Summary{ID: "db-one", Labels: composeLabels(root, "orders", "db", "1")}
	engine := &fakeDockerEngine{
		listed: client.ContainerListResult{Items: []container.Summary{summary}},
		inspections: map[string]client.ContainerInspectResult{
			"db-one": inspectResult("db-one", "orders-db-1", summary.Labels, container.StateRunning, 0),
		},
		logs:       make(map[string]client.ContainerLogsResult),
		logOutputs: map[string]string{"db-one": "must not open\n"},
	}
	follower, err := newDockerRuntime(engine).OpenServiceLogs(t.Context(), root, "db", 200)
	if err != nil {
		t.Fatalf("OpenServiceLogs() error = %v", err)
	}
	if err := follower.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := follower.CopyTo(io.Discard); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	engine.mu.Lock()
	calls := engine.logCalls["db-one"]
	engine.mu.Unlock()
	if calls != 0 {
		t.Fatalf("late Engine log opens = %d, want 0", calls)
	}
}

// TestDockerLogFollowerPreservesSingleTTYBytes keeps carriage returns and cursor controls untouched.
func TestDockerLogFollowerPreservesSingleTTYBytes(t *testing.T) {
	encoded := "\x1b[2K\rloading\rready\n"
	engine := &fakeDockerEngine{
		logs: map[string]client.ContainerLogsResult{
			"orders-worker": &testLogResult{Reader: strings.NewReader(encoded)},
		},
	}
	follower := &dockerLogFollower{runtime: newDockerRuntime(engine), tail: 200}
	output := new(bytes.Buffer)
	err := follower.copyContainerLogs(t.Context(), Container{
		ID: "orders-worker", Name: "orders-worker-1", Replica: 1, TTY: true,
	}, 200, output)
	if err != nil {
		t.Fatalf("copyContainerLogs() error = %v", err)
	}
	if output.String() != encoded {
		t.Fatalf("TTY output = %q, want exact %q", output.String(), encoded)
	}
}

// TestValidateTTYLogSelectionRejectsAmbiguousRawMerges keeps replica bytes from corrupting each other's terminal state.
func TestValidateTTYLogSelectionRejectsAmbiguousRawMerges(t *testing.T) {
	selection := []admittedContainer{
		{container: Container{ID: "worker-one", TTY: true}},
		{container: Container{ID: "worker-two", TTY: true}},
	}
	if err := validateTTYLogSelection(selection); err == nil {
		t.Fatal("validateTTYLogSelection() error = nil")
	}
}

// synchronizedTestBuffer makes asynchronous follower assertions race-free.
type synchronizedTestBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write appends one follower fragment under the test lock.
func (buffer *synchronizedTestBuffer) Write(output []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(output)
}

// String returns one race-free follower transcript copy.
func (buffer *synchronizedTestBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

// rawTTYLog returns bytes directly because these fixtures mark inspected containers as TTY below.
func rawTTYLog(output string) string {
	return output
}

// testLogResult adapts an in-memory stream to Docker's log result interface.
type testLogResult struct {
	io.Reader
}

// Close is inert for an in-memory log response.
func (*testLogResult) Close() error {
	return nil
}

// cancellableTestLogResult models an HTTP response body whose request context owns an interrupted read.
type cancellableTestLogResult struct {
	ctx       context.Context
	closed    chan struct{}
	closeOnce sync.Once
}

// newCancellableTestLogResult creates one blocking response body bound to its Engine request context.
func newCancellableTestLogResult(ctx context.Context) *cancellableTestLogResult {
	return &cancellableTestLogResult{ctx: ctx, closed: make(chan struct{})}
}

// Read blocks until the Engine request is cancelled or the body is closed.
func (result *cancellableTestLogResult) Read([]byte) (int, error) {
	select {
	case <-result.ctx.Done():
		return 0, result.ctx.Err()
	case <-result.closed:
		return 0, io.EOF
	}
}

// Close records deterministic response-body retirement.
func (result *cancellableTestLogResult) Close() error {
	result.closeOnce.Do(func() { close(result.closed) })
	return nil
}

// composeLabels returns the exact immutable Compose ownership labels admitted by Harbor.
func composeLabels(root, project, service, replica string) map[string]string {
	return map[string]string{
		composeWorkingDirectoryLabel: filepath.Clean(root),
		composeProjectLabel:          project,
		composeServiceLabel:          service,
		composeContainerNumberLabel:  replica,
	}
}

// inspectResult returns one fully labeled Engine inspection fixture.
func inspectResult(
	id string,
	name string,
	labels map[string]string,
	state container.ContainerState,
	exitCode int,
) client.ContainerInspectResult {
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID:   id,
		Name: "/" + name,
		State: &container.State{
			Status:   state,
			Running:  state == container.StateRunning,
			ExitCode: exitCode,
		},
		Config: &container.Config{Image: "mysql:8", Labels: labels, Tty: true},
	}}
}

// writeMultiplexedTestFrame creates one Engine log frame without relying on deprecated writer helpers.
func writeMultiplexedTestFrame(t *testing.T, destination io.Writer, stream byte, payload []byte) {
	t.Helper()
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := destination.Write(append(header, payload...)); err != nil {
		t.Fatalf("write multiplexed test frame: %v", err)
	}
}
