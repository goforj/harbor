package projectprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	helperEnabledEnvironment = "HARBOR_PROJECT_PROCESS_HELPER"
	helperModeEnvironment    = "HARBOR_PROJECT_PROCESS_HELPER_MODE"
	helperPIDFileEnvironment = "HARBOR_PROJECT_PROCESS_HELPER_PID_FILE"
)

// init turns a copied test executable into the exact forj-dev subprocess exercised by integration-style unit tests.
func init() {
	if os.Getenv(helperEnabledEnvironment) != "1" {
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "dev" {
		os.Exit(90)
	}
	mode := os.Getenv(helperModeEnvironment)
	if mode == "grandchild" {
		runGrandchildHelper()
		os.Exit(0)
	}
	if mode == "grandchild-ignore" {
		runIgnoringGrandchildHelper()
		os.Exit(0)
	}
	if mode == "ignore" {
		signalIgnoreTermination()
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		os.Exit(91)
	}
	fmt.Fprintf(os.Stdout, "argument=%s\n", os.Args[1])
	fmt.Fprintf(os.Stdout, "plain=%s\n", os.Getenv("FORJ_DEV_PLAIN"))
	fmt.Fprintf(os.Stdout, "working-directory=%s\n", workingDirectory)
	fmt.Fprintln(os.Stderr, "ready")
	switch mode {
	case "exit":
		os.Exit(17)
	case "burst":
		for index := 0; index < 4096; index++ {
			fmt.Fprintf(os.Stdout, "line-%04d\n", index)
		}
		os.Exit(0)
	case "tree":
		runTreeParentHelper()
	case "orphan":
		runOrphanParentHelper()
	case "wait", "ignore":
		waitForTerminationSignal()
	default:
		os.Exit(92)
	}
	os.Exit(0)
}

// runTreeParentHelper creates a descendant in the inherited ownership boundary before waiting for shutdown.
func runTreeParentHelper() {
	command := exec.Command(os.Args[0], "dev")
	command.Env = replaceEnvironment(os.Environ(), helperModeEnvironment, "grandchild-ignore")
	if err := command.Start(); err != nil {
		os.Exit(93)
	}
	waitForTerminationSignal()
}

// runOrphanParentHelper exits abnormally only after its ignoring descendant has entered the same process group.
func runOrphanParentHelper() {
	command := exec.Command(os.Args[0], "dev")
	command.Env = replaceEnvironment(os.Environ(), helperModeEnvironment, "grandchild-ignore")
	if err := command.Start(); err != nil {
		os.Exit(99)
	}
	if !waitForPublishedHelperPID(os.Getenv(helperPIDFileEnvironment)) {
		os.Exit(100)
	}
	os.Exit(23)
}

// runGrandchildHelper publishes its PID before waiting so Unix tests can prove group-wide termination.
func runGrandchildHelper() {
	pidFile := os.Getenv(helperPIDFileEnvironment)
	if pidFile == "" {
		os.Exit(95)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(96)
	}
	waitForTerminationSignal()
}

// runIgnoringGrandchildHelper proves bounded escalation reaches descendants that reject graceful shutdown.
func runIgnoringGrandchildHelper() {
	signalIgnoreTermination()
	pidFile := os.Getenv(helperPIDFileEnvironment)
	if pidFile == "" {
		os.Exit(97)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(98)
	}
	select {}
}

// waitForPublishedHelperPID keeps the helper parent alive until its descendant is observable to the test.
func waitForPublishedHelperPID(path string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(contents)) != "" {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// replaceEnvironment gives helper descendants one deterministic mode without changing unrelated inherited values.
func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, prefix+value)
}

// TestStartLaunchesExactForjDevelopmentCommand verifies real executable lookup, working directory, environment, output, and evidence.
func TestStartLaunchesExactForjDevelopmentCommand(t *testing.T) {
	checkout := t.TempDir()
	stdout := &synchronizedBuffer{}
	stderr := &synchronizedBuffer{}
	installForjHelper(t, "exit")
	supervisor := New(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-one",
		SessionID:    "session-one",
		CheckoutRoot: checkout,
		Stdout:       stdout,
		Stderr:       stderr,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result, err := handle.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.ExitCode != 17 || result.Err == nil || result.StopRequested {
		t.Fatalf("Wait() result = %#v", result)
	}
	waitForOutput(t, stdout, "argument=dev")
	waitForOutput(t, stdout, "plain=1")
	waitForOutput(t, stdout, "working-directory="+checkout)
	waitForOutput(t, stderr, "ready")

	info := handle.Info()
	if info.ProjectID != "project-one" || info.SessionID != "session-one" {
		t.Fatalf("Info() identities = %#v", info)
	}
	if info.CheckoutRoot != checkout {
		t.Fatalf("Info().CheckoutRoot = %q, want %q", info.CheckoutRoot, checkout)
	}
	if len(info.Arguments) != 2 || info.Arguments[1] != "dev" {
		t.Fatalf("Info().Arguments = %#v", info.Arguments)
	}
	if info.Evidence.PID <= 0 || info.Evidence.BirthToken == "" || !filepath.IsAbs(info.Evidence.ExecutableIdentity) {
		t.Fatalf("Info().Evidence = %#v", info.Evidence)
	}
	if info.Evidence.ArgumentsSHA256 != digestArguments(info.Arguments) {
		t.Fatalf("Info().Evidence.ArgumentsSHA256 = %q", info.Evidence.ArgumentsSHA256)
	}
	if info.StartedAt.Location() != time.UTC || result.ExitedAt.Location() != time.UTC {
		t.Fatalf("timestamps are not UTC: started %v, exited %v", info.StartedAt, result.ExitedAt)
	}
	info.Arguments[1] = "changed"
	if handle.Info().Arguments[1] != "dev" {
		t.Fatal("Info() returned mutable arguments")
	}
}

// TestStopGracefullyStopsRealProcess verifies an explicit stop is distinguished from an unexpected exit.
func TestStopGracefullyStopsRealProcess(t *testing.T) {
	installForjHelper(t, "wait")
	output := &synchronizedBuffer{}
	supervisor := New(Options{GracePeriod: 500 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-stop",
		SessionID:    "session-stop",
		CheckoutRoot: t.TempDir(),
		Stdout:       output,
		Stderr:       output,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForOutput(t, output, "ready")
	if err := supervisor.Stop(t.Context(), "project-stop", "session-stop"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	result, ok := handle.Result()
	if !ok || !result.StopRequested {
		t.Fatalf("Result() = %#v, %t", result, ok)
	}
	if err := supervisor.Stop(t.Context(), "project-stop", "session-stop"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Stop() error = %v, want ErrNotRunning", err)
	}
}

// TestStopCancellationForcesProcess verifies a caller deadline cannot leave the owned process tree running.
func TestStopCancellationForcesProcess(t *testing.T) {
	installForjHelper(t, "ignore")
	output := &synchronizedBuffer{}
	supervisor := New(Options{GracePeriod: time.Minute})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-force",
		SessionID:    "session-force",
		CheckoutRoot: t.TempDir(),
		Stdout:       output,
		Stderr:       output,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForOutput(t, output, "ready")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := supervisor.Stop(ctx, "project-force", "session-force"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context cancellation", err)
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer waitCancel()
	result, err := handle.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !result.StopRequested {
		t.Fatalf("Wait() result = %#v", result)
	}
}

// TestConcurrentDuplicateStartAllowsOneOwner verifies project and session reservations are atomic under contention.
func TestConcurrentDuplicateStartAllowsOneOwner(t *testing.T) {
	installForjHelper(t, "wait")
	supervisor := New(Options{GracePeriod: 100 * time.Millisecond})
	const contenders = 8
	results := make(chan startResult, contenders)
	start := make(chan struct{})
	var contendersDone sync.WaitGroup
	for index := 0; index < contenders; index++ {
		contendersDone.Add(1)
		go func() {
			defer contendersDone.Done()
			<-start
			handle, err := supervisor.Start(t.Context(), StartRequest{
				ProjectID:    "project-duplicate",
				SessionID:    "session-duplicate",
				CheckoutRoot: t.TempDir(),
			})
			results <- startResult{handle: handle, err: err}
		}()
	}
	close(start)
	contendersDone.Wait()
	close(results)

	var winner *Handle
	for result := range results {
		if result.err == nil {
			if winner != nil {
				t.Fatal("more than one concurrent Start() succeeded")
			}
			winner = result.handle
			continue
		}
		if !errors.Is(result.err, ErrProjectRunning) && !errors.Is(result.err, ErrSessionRunning) {
			t.Fatalf("Start() error = %v", result.err)
		}
	}
	if winner == nil {
		t.Fatal("no concurrent Start() succeeded")
	}
	if err := supervisor.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestProjectAndSessionReservationsAreIndependent verifies neither identity can own two simultaneous processes.
func TestProjectAndSessionReservationsAreIndependent(t *testing.T) {
	installForjHelper(t, "wait")
	supervisor := New(Options{GracePeriod: 100 * time.Millisecond})
	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-reserved",
		SessionID:    "session-reserved",
		CheckoutRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-reserved",
		SessionID:    "session-other",
		CheckoutRoot: t.TempDir(),
	})
	if !errors.Is(err, ErrProjectRunning) {
		t.Fatalf("same-project Start() error = %v, want ErrProjectRunning", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-other",
		SessionID:    "session-reserved",
		CheckoutRoot: t.TempDir(),
	})
	if !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("same-session Start() error = %v, want ErrSessionRunning", err)
	}
	if err := supervisor.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestBlockedWriterCannotBackpressureChild verifies bounded line dropping keeps process exit and Close independent of UI consumers.
func TestBlockedWriterCannotBackpressureChild(t *testing.T) {
	installForjHelper(t, "burst")
	writer := newBlockingWriter()
	defer close(writer.release)
	supervisor := New(Options{OutputBufferLines: 2, GracePeriod: 100 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-output",
		SessionID:    "session-output",
		CheckoutRoot: t.TempDir(),
		Stdout:       writer,
		Stderr:       writer,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("output writer was not called")
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := handle.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.DroppedOutputLines == 0 {
		t.Fatalf("DroppedOutputLines = %d, want a bounded-queue drop", result.DroppedOutputLines)
	}
	if err := supervisor.Close(waitCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestCloseStopsAllProcessesAndRejectsNewStarts verifies shutdown joins every owned child and remains idempotent.
func TestCloseStopsAllProcessesAndRejectsNewStarts(t *testing.T) {
	installForjHelper(t, "wait")
	supervisor := New(Options{GracePeriod: 100 * time.Millisecond})
	first, err := supervisor.Start(t.Context(), StartRequest{ProjectID: "project-a", SessionID: "session-a", CheckoutRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	second, err := supervisor.Start(t.Context(), StartRequest{ProjectID: "project-b", SessionID: "session-b", CheckoutRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := supervisor.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for _, handle := range []*Handle{first, second} {
		result, ok := handle.Result()
		if !ok || !result.StopRequested {
			t.Fatalf("Result() = %#v, %t", result, ok)
		}
	}
	if err := supervisor.Close(t.Context()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{ProjectID: "project-c", SessionID: "session-c", CheckoutRoot: t.TempDir()})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() after Close() error = %v, want ErrClosed", err)
	}
}

// TestStartRejectsCanceledContextAndInvalidCheckout verifies no child is launched for invalid preconditions.
func TestStartRejectsCanceledContextAndInvalidCheckout(t *testing.T) {
	installForjHelper(t, "wait")
	supervisor := New(Options{})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := supervisor.Start(canceled, StartRequest{ProjectID: "project", SessionID: "session", CheckoutRoot: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() canceled error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{ProjectID: "", SessionID: "session", CheckoutRoot: t.TempDir()})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() invalid identity error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{ProjectID: "project", SessionID: "session", CheckoutRoot: file})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Start() file checkout error = %v", err)
	}
}

// TestEnvironmentReplacementPreservesUnrelatedValues verifies plain mode appears exactly once.
func TestEnvironmentReplacementPreservesUnrelatedValues(t *testing.T) {
	result := withDevelopmentEnvironment([]string{"HOME=/tmp/home", "FORJ_DEV_PLAIN=0", "PATH=/bin", "FORJ_DEV_PLAIN=2"})
	if strings.Join(result, "|") != "HOME=/tmp/home|FORJ_DEV_PLAIN=1|PATH=/bin" {
		t.Fatalf("withDevelopmentEnvironment() = %#v", result)
	}
}

// TestOutputRelayWritesWholeLinesSerially verifies shared destinations never receive interleaved partial lines.
func TestOutputRelayWritesWholeLinesSerially(t *testing.T) {
	writes := &recordingWriter{}
	relay := newOutputRelay(writes, writes, 8)
	var readers sync.WaitGroup
	readers.Add(2)
	go readOutputLines(strings.NewReader("one\ntwo\n"), outputStreamStdout, relay, &readers)
	go readOutputLines(strings.NewReader("error\n"), outputStreamStderr, relay, &readers)
	readers.Wait()
	relay.finish()
	writes.waitForCount(t, 3)
	for _, write := range writes.snapshot() {
		if !strings.HasSuffix(write, "\n") || strings.Count(write, "\n") != 1 {
			t.Fatalf("writer call = %q, want one complete line", write)
		}
	}
}

// TestOutputRelayBoundsNewlineFreeOutput proves one child write cannot grow Harbor memory without limit.
func TestOutputRelayBoundsNewlineFreeOutput(t *testing.T) {
	writer := &recordingWriter{}
	relay := newOutputRelay(writer, io.Discard, 2)
	var readers sync.WaitGroup
	readers.Add(1)
	input := strings.NewReader(strings.Repeat("x", maximumOutputLineBytes*3))
	go readOutputLines(input, outputStreamStdout, relay, &readers)
	readers.Wait()
	relay.finish()
	writer.waitForCount(t, 1)

	writes := writer.snapshot()
	if len(writes) != 1 {
		t.Fatalf("output writes = %d, want 1", len(writes))
	}
	if len(writes[0]) > maximumOutputLineBytes || !strings.HasSuffix(writes[0], string(outputTruncationMarker)) {
		t.Fatalf("bounded output length = %d, suffix present = %t", len(writes[0]), strings.HasSuffix(writes[0], string(outputTruncationMarker)))
	}
	if relay.dropped.Load() != 1 {
		t.Fatalf("dropped lines = %d, want 1", relay.dropped.Load())
	}
}

// startResult carries one concurrent launch attempt back to the coordinating test goroutine.
type startResult struct {
	handle *Handle
	err    error
}

// synchronizedBuffer makes output polling race-free while the relay remains asynchronous.
type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write appends one relay write under the polling lock.
func (buffer *synchronizedBuffer) Write(bytes []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(bytes)
}

// String returns a stable snapshot of accumulated process output.
func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

// blockingWriter blocks its first and only active delivery until the test releases the caller-owned writer.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

// newBlockingWriter constructs a writer with observable backpressure.
func newBlockingWriter() *blockingWriter {
	return &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
}

// Write exposes the blocked call while keeping later writes behind the relay's single delivery goroutine.
func (writer *blockingWriter) Write(bytes []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return len(bytes), nil
}

// recordingWriter retains each Write call separately so tests can assert line atomicity.
type recordingWriter struct {
	mu     sync.Mutex
	writes []string
}

// Write records one caller-visible relay operation.
func (writer *recordingWriter) Write(bytes []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.writes = append(writer.writes, string(bytes))
	return len(bytes), nil
}

// snapshot returns an isolated copy of recorded calls.
func (writer *recordingWriter) snapshot() []string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]string(nil), writer.writes...)
}

// waitForCount waits for asynchronous relay delivery without racing the writer.
func (writer *recordingWriter) waitForCount(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(writer.snapshot()) >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recorded %d writes, want %d", len(writer.snapshot()), count)
}

// installForjHelper places a platform-named test executable first on PATH for real exec.LookPath resolution.
func installForjHelper(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(helperEnabledEnvironment, "1")
	t.Setenv(helperModeEnvironment, mode)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}
	directory := t.TempDir()
	name := "forj"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(directory, name)
	if err := installExecutable(executable, target); err != nil {
		t.Fatalf("install helper executable: %v", err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installExecutable uses a symlink where available and a copy where Windows executable locking requires it.
func installExecutable(source, target string) error {
	if runtime.GOOS != "windows" {
		return os.Symlink(source, target)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// waitForOutput waits for an asynchronous line destination to observe expected text.
func waitForOutput(t *testing.T, output *synchronizedBuffer, expected string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), expected) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("output %q does not contain %q", output.String(), expected)
}
