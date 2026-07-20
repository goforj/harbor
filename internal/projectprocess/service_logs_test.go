package projectprocess

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

// scriptedContainerRuntime returns one source set per open so tests can model container recreation.
type scriptedContainerRuntime struct {
	mu      sync.Mutex
	scripts []containerruntime.LogFollower
	err     error
	opens   int
	tails   []int
	closed  bool
}

// ObserveProject returns an initialized empty view because these tests exercise only logs.
func (*scriptedContainerRuntime) ObserveProject(context.Context, string) (containerruntime.ProjectObservation, error) {
	return containerruntime.ProjectObservation{Services: []containerruntime.Service{}}, nil
}

// OpenServiceLogs returns and consumes the next configured replica set.
func (runtime *scriptedContainerRuntime) OpenServiceLogs(
	ctx context.Context,
	_ string,
	_ string,
	tail int,
) (containerruntime.LogFollower, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.opens++
	runtime.tails = append(runtime.tails, tail)
	if runtime.err != nil {
		return nil, runtime.err
	}
	if len(runtime.scripts) == 0 {
		return newBlockingLogFollower(false), nil
	}
	follower := runtime.scripts[0]
	runtime.scripts = runtime.scripts[1:]
	if memory, ok := follower.(*memoryLogFollower); ok {
		memory.ctx = ctx
	}
	return follower, nil
}

// Close records runtime transport cleanup.
func (runtime *scriptedContainerRuntime) Close() error {
	runtime.mu.Lock()
	runtime.closed = true
	runtime.mu.Unlock()
	return nil
}

// openCount returns a race-free count of runtime log selections.
func (runtime *scriptedContainerRuntime) openCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.opens
}

// requestedTails returns a race-free copy of every retry's historical selection.
func (runtime *scriptedContainerRuntime) requestedTails() []int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]int(nil), runtime.tails...)
}

// memoryLogFollower copies one finite already-attributed transcript.
type memoryLogFollower struct {
	output string
	ctx    context.Context
}

// Available reports that the fixture represents one selected container.
func (*memoryLogFollower) Available() bool {
	return true
}

// WaitAvailable returns immediately because the finite fixture is already selected.
func (*memoryLogFollower) WaitAvailable(context.Context) error {
	return nil
}

// WaitStateChange blocks until cancellation because the finite fixture never changes availability.
func (*memoryLogFollower) WaitStateChange(ctx context.Context, available bool) error {
	if !available {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// CopyTo writes the configured transcript once.
func (follower *memoryLogFollower) CopyTo(destination io.Writer) error {
	if _, err := io.Copy(destination, strings.NewReader(follower.output)); err != nil {
		return err
	}
	<-follower.ctx.Done()
	return nil
}

// Close is inert after finite in-memory output.
func (*memoryLogFollower) Close() error {
	return nil
}

// blockingLogFollower remains open until Harbor closes it during cancellation, idle retirement, or session stop.
type blockingLogFollower struct {
	available bool
	reader    *io.PipeReader
	writer    *io.PipeWriter
	closed    chan struct{}
	once      sync.Once
}

// mutableLogFollower models a live runtime whose selected container can disappear while one read is held.
type mutableLogFollower struct {
	mu        sync.Mutex
	available bool
	changed   chan struct{}
	closed    chan struct{}
	once      sync.Once
}

// newMutableLogFollower creates one change-signalled availability fixture.
func newMutableLogFollower(available bool) *mutableLogFollower {
	return &mutableLogFollower{available: available, changed: make(chan struct{}), closed: make(chan struct{})}
}

// Available returns the current fixture state.
func (follower *mutableLogFollower) Available() bool {
	follower.mu.Lock()
	defer follower.mu.Unlock()
	return follower.available
}

// WaitAvailable delegates to the general state transition waiter.
func (follower *mutableLogFollower) WaitAvailable(ctx context.Context) error {
	if follower.Available() {
		return nil
	}
	return follower.waitFor(ctx, true)
}

// WaitStateChange waits until the fixture differs from the supplied state.
func (follower *mutableLogFollower) WaitStateChange(ctx context.Context, available bool) error {
	for {
		follower.mu.Lock()
		if follower.available != available {
			follower.mu.Unlock()
			return nil
		}
		changed := follower.changed
		follower.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-follower.closed:
			return context.Canceled
		}
	}
}

// waitFor waits until the fixture reaches one requested state.
func (follower *mutableLogFollower) waitFor(ctx context.Context, available bool) error {
	for {
		follower.mu.Lock()
		if follower.available == available {
			follower.mu.Unlock()
			return nil
		}
		changed := follower.changed
		follower.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-follower.closed:
			return context.Canceled
		}
	}
}

// CopyTo remains live until Harbor retires the fixture.
func (follower *mutableLogFollower) CopyTo(io.Writer) error {
	<-follower.closed
	return nil
}

// Close retires every fixture waiter once.
func (follower *mutableLogFollower) Close() error {
	follower.once.Do(func() { close(follower.closed) })
	return nil
}

// setAvailable advances the availability generation.
func (follower *mutableLogFollower) setAvailable(available bool) {
	follower.mu.Lock()
	if follower.available != available {
		follower.available = available
		close(follower.changed)
		follower.changed = make(chan struct{})
	}
	follower.mu.Unlock()
}

// failingLogFollower writes one fragment before simulating a retryable Engine stream failure.
type failingLogFollower struct {
	output string
}

// Available reports one selected container before the simulated failure.
func (*failingLogFollower) Available() bool { return true }

// WaitAvailable returns immediately for the selected fixture.
func (*failingLogFollower) WaitAvailable(context.Context) error { return nil }

// WaitStateChange blocks until cancellation because failure retirement provides the wake.
func (*failingLogFollower) WaitStateChange(ctx context.Context, available bool) error {
	if !available {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// CopyTo writes once before returning the simulated stream failure.
func (follower *failingLogFollower) CopyTo(destination io.Writer) error {
	if _, err := io.WriteString(destination, follower.output); err != nil {
		return err
	}
	return errors.New("simulated Engine disconnect")
}

// Close is inert after the finite simulated failure.
func (*failingLogFollower) Close() error { return nil }

// newBlockingLogSource creates one cancellable in-memory follower.
func newBlockingLogFollower(available bool) *blockingLogFollower {
	reader, writer := io.Pipe()
	return &blockingLogFollower{
		available: available,
		reader:    reader,
		writer:    writer,
		closed:    make(chan struct{}),
	}
}

// Available returns the configured current-selection state.
func (follower *blockingLogFollower) Available() bool {
	return follower.available
}

// WaitAvailable blocks for unavailable fixtures until cancellation or follower retirement.
func (follower *blockingLogFollower) WaitAvailable(ctx context.Context) error {
	if follower.available {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-follower.closed:
		return context.Canceled
	}
}

// WaitStateChange blocks until cancellation or fixture retirement because its configured state is immutable.
func (follower *blockingLogFollower) WaitStateChange(ctx context.Context, available bool) error {
	if follower.available != available {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-follower.closed:
		return context.Canceled
	}
}

// CopyTo blocks while relaying writes until Close interrupts the pipe.
func (follower *blockingLogFollower) CopyTo(destination io.Writer) error {
	_, err := io.Copy(destination, follower.reader)
	if errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

// Close interrupts the pipe once and reports the follower retirement to tests.
func (follower *blockingLogFollower) Close() error {
	var err error
	follower.once.Do(func() {
		err = errors.Join(follower.reader.Close(), follower.writer.Close())
		close(follower.closed)
	})
	return err
}

// write appends one live fragment to the blocking source.
func (follower *blockingLogFollower) write(t *testing.T, output string) {
	t.Helper()
	if _, err := follower.writer.Write([]byte(output)); err != nil {
		t.Fatalf("write service log source: %v", err)
	}
}

// TestServiceLogsMergeReplicaOutputWithAttribution verifies concurrent sources cannot interleave Docker framing or lose identity.
func TestServiceLogsMergeReplicaOutputWithAttribution(t *testing.T) {
	runtime := &scriptedContainerRuntime{scripts: []containerruntime.LogFollower{
		&memoryLogFollower{output: "[orders-db-1] primary ready\n[orders-db-2] replica ready\n"},
	}}
	supervisor := startServiceLogTestProject(t, runtime, time.Second)

	selection, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
	if err != nil {
		t.Fatalf("ReadServiceLogs() error = %v", err)
	}
	if !selection.Supported || !selection.Available || selection.Problem != nil {
		t.Fatalf("ReadServiceLogs() = %#v", selection)
	}
	output := selection.Output.Text
	cursor := selection.Output.NextCursor
	deadline := time.Now().Add(2 * time.Second)
	for (!strings.Contains(output, "primary ready") || !strings.Contains(output, "replica ready")) && time.Now().Before(deadline) {
		waitContext, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		next, waitErr := supervisor.WaitServiceLogs(waitContext, "project-logs", "session-logs", "db", cursor)
		cancel()
		if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("WaitServiceLogs() error = %v", waitErr)
		}
		output += next.Output.Text
		cursor = next.Output.NextCursor
	}
	for _, expected := range []string{"[orders-db-1] primary ready", "[orders-db-2] replica ready"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("service log output = %q, want %q", output, expected)
		}
	}
}

// TestServiceLogsReturnCleanWaitingAndTypedRuntimeUnavailable distinguishes absence from infrastructure failure.
func TestServiceLogsReturnCleanWaitingAndTypedRuntimeUnavailable(t *testing.T) {
	t.Run("waiting", func(t *testing.T) {
		supervisor := startServiceLogTestProject(t, &scriptedContainerRuntime{scripts: []containerruntime.LogFollower{newBlockingLogFollower(false)}}, time.Second)
		selection, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
		if err != nil {
			t.Fatalf("ReadServiceLogs() error = %v", err)
		}
		if !selection.Supported || selection.Available || selection.Problem != nil {
			t.Fatalf("ReadServiceLogs() = %#v", selection)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		supervisor := startServiceLogTestProject(t, &scriptedContainerRuntime{err: errors.New("engine unavailable")}, time.Second)
		selection, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
		if err != nil {
			t.Fatalf("ReadServiceLogs() error = %v", err)
		}
		if selection.Supported || selection.Available || selection.Problem == nil || selection.Problem.Code != "runtime_unavailable" {
			t.Fatalf("ReadServiceLogs() = %#v", selection)
		}
	})
}

// TestServiceLogHeldReadCancellationAllowsIdleRetirement verifies abandoned UI routes cannot leave Engine followers alive.
func TestServiceLogHeldReadCancellationAllowsIdleRetirement(t *testing.T) {
	source := newBlockingLogFollower(true)
	runtime := &scriptedContainerRuntime{scripts: []containerruntime.LogFollower{source}}
	supervisor := startServiceLogTestProject(t, runtime, 50*time.Millisecond)
	initial, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
	if err != nil || !initial.Available {
		t.Fatalf("ReadServiceLogs() = %#v, %v", initial, err)
	}

	waitContext, cancel := context.WithCancel(t.Context())
	waitResult := make(chan error, 1)
	go func() {
		_, waitErr := supervisor.WaitServiceLogs(waitContext, "project-logs", "session-logs", "db", 0)
		waitResult <- waitErr
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-waitResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitServiceLogs() error = %v, want cancellation", err)
	}
	select {
	case <-source.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle service log follower remained open")
	}
}

// TestServiceLogSessionStopClosesFollowersBeforeReturning verifies project teardown deterministically retires log bodies.
func TestServiceLogSessionStopClosesFollowersBeforeReturning(t *testing.T) {
	source := newBlockingLogFollower(true)
	runtime := &scriptedContainerRuntime{scripts: []containerruntime.LogFollower{source}}
	supervisor := startServiceLogTestProject(t, runtime, time.Minute)
	if _, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0); err != nil {
		t.Fatalf("ReadServiceLogs() error = %v", err)
	}
	stopContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := supervisor.Stop(stopContext, "project-logs", "session-logs"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("Stop() returned before closing service log follower")
	}
}

// TestServiceLogAvailabilityChangeWakesHeldRead verifies container disappearance does not wait for the RPC deadline.
func TestServiceLogAvailabilityChangeWakesHeldRead(t *testing.T) {
	follower := newMutableLogFollower(true)
	supervisor := startServiceLogTestProject(
		t,
		&scriptedContainerRuntime{scripts: []containerruntime.LogFollower{follower}},
		time.Minute,
	)
	initial, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
	if err != nil || !initial.Available {
		t.Fatalf("ReadServiceLogs() = %#v, %v", initial, err)
	}
	done := make(chan ServiceLogSelection, 1)
	errs := make(chan error, 1)
	go func() {
		selection, waitErr := supervisor.WaitServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
		done <- selection
		errs <- waitErr
	}()
	time.Sleep(20 * time.Millisecond)
	follower.setAvailable(false)
	select {
	case selection := <-done:
		if err := <-errs; err != nil {
			t.Fatalf("WaitServiceLogs() error = %v", err)
		}
		if selection.Available {
			t.Fatalf("WaitServiceLogs() = %#v, want unavailable wake", selection)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitServiceLogs() did not wake after container disappearance")
	}
}

// TestServiceLogRetryPreservesCursorWithoutReplayingTail verifies one logical transcript survives Engine reconnection.
func TestServiceLogRetryPreservesCursorWithoutReplayingTail(t *testing.T) {
	replacement := newBlockingLogFollower(true)
	runtime := &scriptedContainerRuntime{scripts: []containerruntime.LogFollower{
		&failingLogFollower{output: "[orders-db-1 stdout] before\n"},
		replacement,
	}}
	supervisor := startServiceLogTestProject(t, runtime, time.Minute)
	if _, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0); err != nil {
		t.Fatalf("initial ReadServiceLogs() error = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.openCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.openCount() != 2 {
		t.Fatalf("runtime open count = %d, want retry", runtime.openCount())
	}
	replacement.write(t, "[orders-db-1 stdout] after\n")
	selection, err := supervisor.ReadServiceLogs(t.Context(), "project-logs", "session-logs", "db", 0)
	if err != nil {
		t.Fatalf("ReadServiceLogs() error = %v", err)
	}
	want := "[orders-db-1 stdout] before\n[orders-db-1 stdout] after\n"
	if selection.Output.Text != want || selection.Output.NextCursor != uint64(len(want)) || selection.Output.Reset {
		t.Fatalf("ReadServiceLogs() output = %#v, want %q", selection.Output, want)
	}
	tails := runtime.requestedTails()
	if len(tails) != 2 || tails[0] != serviceLogTailLines || tails[1] != 0 {
		t.Fatalf("runtime tails = %v, want [%d 0]", tails, serviceLogTailLines)
	}
}

// startServiceLogTestProject launches the existing process helper around one injected runtime.
func startServiceLogTestProject(
	t *testing.T,
	runtime containerruntime.Runtime,
	idle time.Duration,
) *Supervisor {
	t.Helper()
	installForjHelper(t, "wait")
	output := new(synchronizedBuffer)
	supervisor := newTestSupervisor(Options{
		GracePeriod:          100 * time.Millisecond,
		ContainerRuntime:     runtime,
		ServiceLogIdlePeriod: idle,
	})
	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            domain.ProjectID("project-logs"),
		SessionID:            domain.SessionID("session-logs"),
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               output,
		Stderr:               output,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForOutput(t, output, "ready")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := supervisor.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Close() error = %v", err)
		}
	})
	return supervisor
}
