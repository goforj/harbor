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
	helperEnabledEnvironment     = "HARBOR_PROJECT_PROCESS_HELPER"
	helperModeEnvironment        = "HARBOR_PROJECT_PROCESS_HELPER_MODE"
	helperPIDFileEnvironment     = "HARBOR_PROJECT_PROCESS_HELPER_PID_FILE"
	helperStartedFileEnvironment = "HARBOR_PROJECT_PROCESS_HELPER_STARTED_FILE"
	helperOverrideEnvironment    = "HARBOR_PROJECT_PROCESS_OVERRIDE"
	helperEmptyEnvironment       = "HARBOR_PROJECT_PROCESS_EMPTY"
	helperUnrelatedEnvironment   = "HARBOR_PROJECT_PROCESS_UNRELATED"
)

// init turns a copied test executable into the exact forj-dev subprocess exercised by integration-style unit tests.
func init() {
	if os.Getenv(helperEnabledEnvironment) != "1" {
		return
	}
	if startedFile := os.Getenv(helperStartedFileEnvironment); startedFile != "" {
		if err := os.WriteFile(startedFile, []byte("started"), 0o600); err != nil {
			os.Exit(89)
		}
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
	fmt.Fprintf(os.Stdout, "app-name=%s\n", os.Getenv("APP_NAME"))
	fmt.Fprintf(os.Stdout, "forj-app=%s\n", os.Getenv("FORJ_APP"))
	fmt.Fprintf(os.Stdout, "dev-service-ip-address=%s\n", os.Getenv("DEV_SERVICE_IP_ADDRESS"))
	fmt.Fprintf(os.Stdout, "ip-address=%s\n", os.Getenv("IP_ADDRESS"))
	fmt.Fprintf(os.Stdout, "api-http-host=%s\n", os.Getenv("API_HTTP_HOST"))
	fmt.Fprintf(os.Stdout, "managed-keys=%s\n", os.Getenv(managedEnvKeysName))
	fmt.Fprintf(os.Stdout, "override=%s\n", os.Getenv(helperOverrideEnvironment))
	emptyValue, emptyPresent := os.LookupEnv(helperEmptyEnvironment)
	fmt.Fprintf(os.Stdout, "empty=%t:%s\n", emptyPresent, emptyValue)
	fmt.Fprintf(os.Stdout, "unrelated=%s\n", os.Getenv(helperUnrelatedEnvironment))
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

// projectProcessTestEnvironment supplies one explicit managed value used by valid launch tests.
func projectProcessTestEnvironment() EnvironmentOverrides {
	return EnvironmentOverrides{helperOverrideEnvironment: "managed"}
}

// newTestSupervisor keeps helper-process tests focused on lifecycle behavior behind an explicit compatible executable seam.
func newTestSupervisor(options Options) *Supervisor {
	return NewWithExecutableVerifier(options, func(string) error { return nil })
}

// canonicalTestPath resolves platform aliases so process expectations use the supervisor's working-directory identity.
func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve canonical test path: %v", err)
	}
	return filepath.Clean(canonical)
}

// TestStartLaunchesExactForjDevelopmentCommand verifies real executable lookup, working directory, environment, output, and evidence.
func TestStartLaunchesExactForjDevelopmentCommand(t *testing.T) {
	checkout := t.TempDir()
	canonicalCheckout := canonicalTestPath(t, checkout)
	stdout := &synchronizedBuffer{}
	stderr := &synchronizedBuffer{}
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-one",
		SessionID:            "session-one",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               stdout,
		Stderr:               stderr,
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
	waitForOutput(t, stdout, "working-directory="+canonicalCheckout)
	waitForOutput(t, stderr, "ready")

	info := handle.Info()
	if info.ProjectID != "project-one" || info.SessionID != "session-one" {
		t.Fatalf("Info() identities = %#v", info)
	}
	if info.CheckoutRoot != canonicalCheckout {
		t.Fatalf("Info().CheckoutRoot = %q, want %q", info.CheckoutRoot, canonicalCheckout)
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

// TestStartUsesCapturedEnvironment prevents Harbor's loaded app identity from selecting an App inside managed projects.
func TestStartUsesCapturedEnvironment(t *testing.T) {
	checkout := t.TempDir()
	stdout := &synchronizedBuffer{}
	installForjHelper(t, "exit")

	captured := CaptureEnvironment()
	filtered := captured[:0]
	for _, entry := range captured {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (environmentNameEqual(name, "APP_NAME") || environmentNameEqual(name, "FORJ_APP")) {
			continue
		}
		filtered = append(filtered, entry)
	}
	supervisor := newTestSupervisor(Options{Environment: filtered})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})
	t.Setenv("APP_NAME", "harbor")
	t.Setenv("FORJ_APP", "harbord")

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-environment",
		SessionID:            "session-environment",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               stdout,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	waitForOutput(t, stdout, "app-name=\n")
	waitForOutput(t, stdout, "forj-app=\n")
	if output := stdout.String(); strings.Contains(output, "harbor") {
		t.Fatalf("managed project inherited Harbor app identity: %q", output)
	}
}

// TestStartAppliesExplicitEnvironmentOverrides proves managed values win while unrelated captured values remain intact.
func TestStartAppliesExplicitEnvironmentOverrides(t *testing.T) {
	checkout := t.TempDir()
	stdout := &synchronizedBuffer{}
	installForjHelper(t, "exit")

	captured := CaptureEnvironment()
	captured = replaceEnvironment(captured, "DEV_SERVICE_IP_ADDRESS", "127.0.0.7")
	captured = replaceEnvironment(captured, "IP_ADDRESS", "127.0.0.8")
	captured = replaceEnvironment(captured, "API_HTTP_HOST", "127.0.0.9")
	captured = replaceEnvironment(captured, helperOverrideEnvironment, "captured")
	captured = replaceEnvironment(captured, helperUnrelatedEnvironment, "preserved")
	captured = replaceEnvironment(captured, managedEnvKeysName, "STALE")
	supervisor := newTestSupervisor(Options{Environment: captured})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-overrides",
		SessionID:    "session-overrides",
		CheckoutRoot: checkout,
		EnvironmentOverrides: EnvironmentOverrides{
			"DEV_SERVICE_IP_ADDRESS":  "127.0.0.42",
			"IP_ADDRESS":              "127.0.0.42",
			helperOverrideEnvironment: "managed",
			helperEmptyEnvironment:    "",
		},
		Stdout: stdout,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	waitForOutput(t, stdout, "dev-service-ip-address=127.0.0.42")
	waitForOutput(t, stdout, "ip-address=127.0.0.42")
	waitForOutput(t, stdout, "api-http-host=127.0.0.9")
	waitForOutput(t, stdout, "override=managed")
	waitForOutput(t, stdout, "empty=true:")
	waitForOutput(t, stdout, "unrelated=preserved")
	waitForOutput(t, stdout, "managed-keys=DEV_SERVICE_IP_ADDRESS,HARBOR_PROJECT_PROCESS_EMPTY,HARBOR_PROJECT_PROCESS_OVERRIDE,IP_ADDRESS")
	if output := stdout.String(); strings.Contains(output, "127.0.0.7") || strings.Contains(output, "127.0.0.8") || strings.Contains(output, "managed-keys=STALE") {
		t.Fatalf("managed project retained captured override values: %q", output)
	}
}

// TestStopGracefullyStopsRealProcess verifies an explicit stop is distinguished from an unexpected exit.
func TestStopGracefullyStopsRealProcess(t *testing.T) {
	installForjHelper(t, "wait")
	output := &synchronizedBuffer{}
	supervisor := newTestSupervisor(Options{GracePeriod: 500 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-stop",
		SessionID:            "session-stop",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               output,
		Stderr:               output,
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
	supervisor := newTestSupervisor(Options{GracePeriod: time.Minute})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-force",
		SessionID:            "session-force",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               output,
		Stderr:               output,
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
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
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
				ProjectID:            "project-duplicate",
				SessionID:            "session-duplicate",
				CheckoutRoot:         t.TempDir(),
				EnvironmentOverrides: projectProcessTestEnvironment(),
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
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-reserved",
		SessionID:            "session-reserved",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-reserved",
		SessionID:            "session-other",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrProjectRunning) {
		t.Fatalf("same-project Start() error = %v, want ErrProjectRunning", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-other",
		SessionID:            "session-reserved",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
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
	supervisor := newTestSupervisor(Options{OutputBufferLines: 2, GracePeriod: 100 * time.Millisecond})
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-output",
		SessionID:            "session-output",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               writer,
		Stderr:               writer,
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
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	first, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-a",
		SessionID:            "session-a",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	second, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-b",
		SessionID:            "session-b",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
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
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-c",
		SessionID:            "session-c",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Start() after Close() error = %v, want ErrClosed", err)
	}
}

// TestStartRejectsCanceledContextAndInvalidCheckout verifies no child is launched for invalid preconditions.
func TestStartRejectsCanceledContextAndInvalidCheckout(t *testing.T) {
	installForjHelper(t, "wait")
	supervisor := newTestSupervisor(Options{})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := supervisor.Start(canceled, StartRequest{
		ProjectID:            "project",
		SessionID:            "session",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() canceled error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "",
		SessionID:            "session",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() invalid identity error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project",
		SessionID:            "session",
		CheckoutRoot:         file,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Start() file checkout error = %v", err)
	}
}

// TestStartRejectsInvalidEnvironmentOverrides keeps malformed, ambiguous, and private launcher controls out of child processes.
func TestStartRejectsInvalidEnvironmentOverrides(t *testing.T) {
	tests := []struct {
		name      string
		overrides EnvironmentOverrides
	}{
		{name: "missing overrides", overrides: nil},
		{name: "empty name", overrides: EnvironmentOverrides{"": "value"}},
		{name: "digit prefix", overrides: EnvironmentOverrides{"1_VALUE": "value"}},
		{name: "punctuation", overrides: EnvironmentOverrides{"BAD-NAME": "value"}},
		{name: "non ASCII", overrides: EnvironmentOverrides{"CAFÉ": "value"}},
		{name: "NUL name", overrides: EnvironmentOverrides{"BAD\x00NAME": "value"}},
		{name: "NUL value", overrides: EnvironmentOverrides{"GOOD_NAME": "bad\x00value"}},
		{name: "managed marker", overrides: EnvironmentOverrides{managedEnvKeysName: "value"}},
		{name: "private GoForj name", overrides: EnvironmentOverrides{"FORJ_INTERNAL_OTHER": "value"}},
		{name: "dotenv selector", overrides: EnvironmentOverrides{"APP_ENV": "testing"}},
		{name: "GoForj app selector", overrides: EnvironmentOverrides{"FORJ_APP": "value"}},
		{name: "plain launcher mode", overrides: EnvironmentOverrides{developmentPlainEnvName: "0"}},
		{name: "portable case collision", overrides: EnvironmentOverrides{"PROJECT_VALUE": "one", "project_value": "two"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateStartRequest(StartRequest{
				ProjectID:            "project-overrides-invalid",
				SessionID:            "session-overrides-invalid",
				CheckoutRoot:         t.TempDir(),
				EnvironmentOverrides: test.overrides,
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("validateStartRequest() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestStartAcceptsPresentEmptyEnvironmentOverride proves values retain ordinary environment semantics.
func TestStartAcceptsPresentEmptyEnvironmentOverride(t *testing.T) {
	err := validateStartRequest(StartRequest{
		ProjectID:    "project-empty-override",
		SessionID:    "session-empty-override",
		CheckoutRoot: t.TempDir(),
		EnvironmentOverrides: EnvironmentOverrides{
			"EMPTY_VALUE": "",
			"lower_value": "",
		},
	})
	if err != nil {
		t.Fatalf("validateStartRequest() error = %v", err)
	}
}

// TestEnvironmentReplacementPreservesUnrelatedValues verifies deterministic explicit values are appended once without mutating the captured base.
func TestEnvironmentReplacementPreservesUnrelatedValues(t *testing.T) {
	base := []string{
		"HOME=/tmp/home",
		"FORJ_DEV_PLAIN=0",
		"PATH=/bin",
		"FORJ_DEV_PLAIN=2",
		"DEV_SERVICE_IP_ADDRESS=127.0.0.7",
		"IP_ADDRESS=127.0.0.8",
		"API_HTTP_HOST=127.0.0.9",
		"ip_address=127.0.0.10",
		"FORJ_INTERNAL_MANAGED_ENV_KEYS=STALE",
		"UNRELATED=preserved",
	}
	before := append([]string(nil), base...)
	result := withDevelopmentEnvironment(base, EnvironmentOverrides{
		"Z_VALUE":                "last",
		"DEV_SERVICE_IP_ADDRESS": "127.0.0.42",
		"IP_ADDRESS":             "127.0.0.42",
		"b_value":                "middle",
		"A_VALUE":                "",
	})
	want := "HOME=/tmp/home|PATH=/bin|API_HTTP_HOST=127.0.0.9"
	if runtime.GOOS != "windows" {
		want += "|ip_address=127.0.0.10"
	}
	want += "|UNRELATED=preserved|A_VALUE=|b_value=middle|DEV_SERVICE_IP_ADDRESS=127.0.0.42|IP_ADDRESS=127.0.0.42|Z_VALUE=last|FORJ_DEV_PLAIN=1|FORJ_INTERNAL_MANAGED_ENV_KEYS=A_VALUE,b_value,DEV_SERVICE_IP_ADDRESS,IP_ADDRESS,Z_VALUE"
	if strings.Join(result, "|") != want {
		t.Fatalf("withDevelopmentEnvironment() = %q, want %q", strings.Join(result, "|"), want)
	}
	if strings.Join(base, "|") != strings.Join(before, "|") {
		t.Fatalf("withDevelopmentEnvironment() mutated base = %#v, want %#v", base, before)
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
