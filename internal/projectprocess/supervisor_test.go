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
	"unicode/utf8"

	"github.com/joho/godotenv"
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
	if err := godotenv.Overload(".env.host"); err != nil {
		os.Exit(101)
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
	fmt.Fprintf(os.Stdout, "managed-context=%s\n", os.Getenv(ManagedLaunchContextEnvironment))
	fmt.Fprintf(os.Stdout, "app-name=%s\n", os.Getenv("APP_NAME"))
	fmt.Fprintf(os.Stdout, "forj-app=%s\n", os.Getenv("FORJ_APP"))
	fmt.Fprintf(os.Stdout, "dev-service-ip-address=%s\n", os.Getenv("DEV_SERVICE_IP_ADDRESS"))
	fmt.Fprintf(os.Stdout, "ip-address=%s\n", os.Getenv("IP_ADDRESS"))
	fmt.Fprintf(os.Stdout, "api-http-host=%s\n", os.Getenv("API_HTTP_HOST"))
	fmt.Fprintf(os.Stdout, "db-host=%s\n", os.Getenv("DB_HOST"))
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
	case "orphan-separate-group":
		runSeparateGroupOrphanParentHelper()
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

// runSeparateGroupOrphanParentHelper models GoForj watchers that move descendants outside their parent's process group.
func runSeparateGroupOrphanParentHelper() {
	command := exec.Command(os.Args[0], "dev")
	command.Env = replaceEnvironment(os.Environ(), helperModeEnvironment, "grandchild-ignore")
	separateHelperProcessGroup(command)
	if err := command.Start(); err != nil {
		os.Exit(102)
	}
	if !waitForPublishedHelperPID(os.Getenv(helperPIDFileEnvironment)) {
		os.Exit(103)
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
	return EnvironmentOverrides{"IP_ADDRESS": "127.77.0.42"}
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

// TestStartRollsBackManagedHostEnvironmentBeforeAcceptingProcess preserves project-owned dotenv content when launch validation fails.
func TestStartRollsBackManagedHostEnvironmentBeforeAcceptingProcess(t *testing.T) {
	checkout := t.TempDir()
	path := filepath.Join(checkout, ".env.host")
	contents := []byte("DB_HOST=127.0.0.1\n")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatalf("write project host environment: %v", err)
	}
	installForjHelper(t, "exit")
	managedContext := validManagedLaunchContext(t)
	managedContext.ProjectID = "project-rollback"
	managedContext.SessionID = "session-rollback"
	managedContext.ProjectRoot = canonicalTestPath(t, t.TempDir())
	supervisor := newTestSupervisor(Options{})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            managedContext.ProjectID,
		SessionID:            managedContext.SessionID,
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		ManagedLaunch:        &managedContext,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() error = %v, want ErrInvalidRequest", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project host environment: %v", err)
	}
	if !bytes.Equal(actual, append(contents, '\n')) {
		t.Fatalf("project host environment = %q, want preserved assignment and separator", actual)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat project host environment: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("project host environment mode = %o, want 640", info.Mode().Perm())
	}
}

// TestStartReportsManagedHostEnvironmentRollbackFailure preserves both launch and cleanup failures when Harbor cannot retire its block.
func TestStartReportsManagedHostEnvironmentRollbackFailure(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "exit")
	managedContext := validManagedLaunchContext(t)
	managedContext.ProjectID = "project-rollback-failure"
	managedContext.SessionID = "session-rollback-failure"
	managedContext.ProjectRoot = canonicalTestPath(t, t.TempDir())
	rollbackErr := errors.New("host environment remains unavailable")
	supervisor := newTestSupervisor(Options{})
	supervisor.removeHostEnvironment = func(string) error { return rollbackErr }
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	_, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            managedContext.ProjectID,
		SessionID:            managedContext.SessionID,
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		ManagedLaunch:        &managedContext,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start() error = %v, want ErrInvalidRequest", err)
	}
	if !errors.Is(err, ErrCleanupUncertain) {
		t.Fatalf("Start() error = %v, want ErrCleanupUncertain", err)
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("Start() error = %v, want rollback error", err)
	}
	contents, readErr := os.ReadFile(filepath.Join(checkout, ".env.host"))
	if readErr != nil {
		t.Fatalf("read managed host environment: %v", readErr)
	}
	if !strings.Contains(string(contents), managedHostEnvironmentBegin) {
		t.Fatalf("managed host environment was unexpectedly removed: %q", contents)
	}
}

// TestStartRejectsDifferentLifecycleForSameCheckout prevents two lifecycle identities from sharing Harbor's host environment block.
func TestStartRejectsDifferentLifecycleForSameCheckout(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "wait")
	output := &synchronizedBuffer{}
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-checkout-one",
		SessionID:            "session-checkout-one",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		Stdout:               output,
		Stderr:               output,
	})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	waitForOutput(t, output, "ready")
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-checkout-two",
		SessionID:            "session-checkout-two",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrProjectRunning) {
		t.Fatalf("second Start() error = %v, want ErrProjectRunning", err)
	}
	if err := supervisor.Stop(t.Context(), "project-checkout-one", "session-checkout-one"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

// TestCheckoutReservationOutlastsExitUntilManagedHostEnvironmentCleanup finishes cleanup before allowing a new lifecycle for the same checkout.
func TestCheckoutReservationOutlastsExitUntilManagedHostEnvironmentCleanup(t *testing.T) {
	checkout := t.TempDir()
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	removeHostEnvironment := supervisor.removeHostEnvironment
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var cleanupStartOnce sync.Once
	var releaseCleanupOnce sync.Once
	release := func() { releaseCleanupOnce.Do(func() { close(releaseCleanup) }) }
	supervisor.removeHostEnvironment = func(root string) error {
		cleanupStartOnce.Do(func() { close(cleanupStarted) })
		<-releaseCleanup
		return removeHostEnvironment(root)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	defer release()

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-exit-one",
		SessionID:            "session-exit-one",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	select {
	case <-cleanupStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("managed host environment cleanup did not start")
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-exit-two",
		SessionID:            "session-exit-two",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if !errors.Is(err, ErrProjectRunning) {
		t.Fatalf("Start() during cleanup error = %v, want ErrProjectRunning", err)
	}
	release()
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	_, err = supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-exit-two",
		SessionID:            "session-exit-two",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() after cleanup error = %v", err)
	}
}

// TestUnexpectedSettledExitRemovesManagedHostEnvironment removes Harbor's block after an owned child exits without a stop request.
func TestUnexpectedSettledExitRemovesManagedHostEnvironment(t *testing.T) {
	checkout := t.TempDir()
	path := filepath.Join(checkout, ".env.host")
	contents := []byte("DB_HOST=127.0.0.1\n")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatalf("write project host environment: %v", err)
	}
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-unexpected-exit",
		SessionID:            "session-unexpected-exit",
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result, err := handle.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.StopRequested {
		t.Fatalf("Wait() result = %#v, want unexpected exit", result)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project host environment: %v", err)
	}
	if !bytes.Equal(actual, append(contents, '\n')) {
		t.Fatalf("project host environment = %q, want preserved assignment and separator", actual)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat project host environment: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("project host environment mode = %o, want 640", info.Mode().Perm())
	}
}

// TestStartCarriesOwnerOnlyManagedContextWithoutChangingArgv verifies the context crosses only through the reserved file reference.
func TestStartCarriesOwnerOnlyManagedContextWithoutChangingArgv(t *testing.T) {
	checkout := t.TempDir()
	managedContext := validManagedLaunchContext(t)
	managedContext.ProjectRoot = canonicalTestPath(t, checkout)
	stdout := &synchronizedBuffer{}
	installForjHelper(t, "exit")
	supervisor := newTestSupervisor(Options{GracePeriod: 100 * time.Millisecond})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            managedContext.ProjectID,
		SessionID:            managedContext.SessionID,
		CheckoutRoot:         checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(),
		ManagedLaunch:        &managedContext,
		Stdout:               stdout,
		Stderr:               io.Discard,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result, err := handle.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Err == nil {
		t.Fatalf("Wait() result = %#v, want helper exit error", result)
	}
	waitForOutput(t, stdout, "managed-context=")
	info := handle.Info()
	if len(info.Arguments) != 2 || info.Arguments[1] != "dev" {
		t.Fatalf("Info().Arguments = %#v, want exact dev argv", info.Arguments)
	}
	for _, entry := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(entry, "managed-context=") {
			path := strings.TrimPrefix(entry, "managed-context=")
			if path == "" {
				t.Fatal("managed context environment was empty")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("managed context stat after child exit = %v, want not exist", err)
			}
			return
		}
	}
	t.Fatalf("managed context output missing from %q", stdout.String())
}

// TestStartUsesCapturedEnvironment prevents Harbor's loaded app identity from selecting an App inside managed projects.
func TestStartUsesCapturedEnvironment(t *testing.T) {
	checkout := t.TempDir()
	stdout := &synchronizedBuffer{}
	installForjHelper(t, "exit")

	captured := CaptureEnvironment()
	captured = replaceEnvironment(captured, "APP_NAME", "harbor")
	captured = replaceEnvironment(captured, "FORJ_APP", "harbord")
	captured = replaceEnvironment(captured, "FORJ_BUILD_PROGRESS", "harbor-progress")
	captured = replaceEnvironment(captured, "FORJ_COMMAND_PREFIX", "harbor harbord")
	supervisor := newTestSupervisor(Options{Environment: captured})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

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

// TestStartLoadsManagedValuesFromHostEnvironment proves the child reads Harbor values from the final dotenv layer.
func TestStartLoadsManagedValuesFromHostEnvironment(t *testing.T) {
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, ".env.host"), []byte("DB_HOST=127.0.0.1\n"), 0o600); err != nil {
		t.Fatalf("write project host environment: %v", err)
	}
	stdout := &synchronizedBuffer{}
	installForjHelper(t, "exit")

	captured := CaptureEnvironment()
	captured = replaceEnvironment(captured, "DEV_SERVICE_IP_ADDRESS", "127.0.0.7")
	captured = replaceEnvironment(captured, "IP_ADDRESS", "127.0.0.8")
	captured = replaceEnvironment(captured, "API_HTTP_HOST", "127.0.0.9")
	captured = replaceEnvironment(captured, helperOverrideEnvironment, "captured")
	captured = replaceEnvironment(captured, helperUnrelatedEnvironment, "preserved")
	supervisor := newTestSupervisor(Options{Environment: captured})
	t.Cleanup(func() {
		_ = supervisor.Close(context.Background())
	})

	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:    "project-overrides",
		SessionID:    "session-overrides",
		CheckoutRoot: checkout,
		EnvironmentOverrides: EnvironmentOverrides{
			"DEV_SERVICE_IP_ADDRESS": "127.77.0.42",
			"IP_ADDRESS":             "127.77.0.42",
		},
		Stdout: stdout,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	waitForOutput(t, stdout, "dev-service-ip-address=127.77.0.42")
	waitForOutput(t, stdout, "ip-address=127.77.0.42")
	waitForOutput(t, stdout, "api-http-host=127.77.0.42")
	waitForOutput(t, stdout, "db-host=127.77.0.42")
	waitForOutput(t, stdout, "override=captured")
	waitForOutput(t, stdout, "empty=false:")
	waitForOutput(t, stdout, "unrelated=preserved")
	if output := stdout.String(); strings.Contains(output, "127.0.0.7") || strings.Contains(output, "127.0.0.8") {
		t.Fatalf("managed project retained ambient network values: %q", output)
	}
}

// TestStopGracefullyStopsRealProcess verifies an explicit stop is distinguished from an unexpected exit.
func TestStopGracefullyStopsRealProcess(t *testing.T) {
	installForjHelper(t, "wait")
	output := &synchronizedBuffer{}
	supervisor := newTestSupervisor(Options{GracePeriod: 500 * time.Millisecond})
	checkout := t.TempDir()
	path := filepath.Join(checkout, ".env.host")
	contents := []byte("DB_HOST=127.0.0.1\n")
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatalf("write project host environment: %v", err)
	}
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-stop",
		SessionID:            "session-stop",
		CheckoutRoot:         checkout,
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
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project host environment: %v", err)
	}
	if !bytes.Equal(actual, append(contents, '\n')) {
		t.Fatalf("project host environment = %q, want preserved assignment and separator", actual)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat project host environment: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("project host environment mode = %o, want 640", info.Mode().Perm())
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

// TestSupervisorStartIgnoresOutputSpoolFailure keeps diagnostic history from becoming a lifecycle prerequisite.
func TestSupervisorStartIgnoresOutputSpoolFailure(t *testing.T) {
	installForjHelper(t, "exit")
	root := t.TempDir()
	spoolPath := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(spoolPath, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create invalid spool path: %v", err)
	}
	supervisor := newTestSupervisor(Options{
		OutputSpoolDirectory: spoolPath,
		GracePeriod:          100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	handle, err := supervisor.Start(t.Context(), StartRequest{
		ProjectID:            "project-spool-failure",
		SessionID:            "session-spool-failure",
		CheckoutRoot:         t.TempDir(),
		EnvironmentOverrides: projectProcessTestEnvironment(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v, want diagnostic spool failure to be ignored", err)
	}
	if _, err := handle.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
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
		{name: "private GoForj name", overrides: EnvironmentOverrides{"FORJ_INTERNAL_OTHER": "value"}},
		{name: "dotenv selector", overrides: EnvironmentOverrides{"APP_ENV": "testing"}},
		{name: "GoForj app selector", overrides: EnvironmentOverrides{"FORJ_APP": "value"}},
		{name: "plain launcher mode", overrides: EnvironmentOverrides{developmentPlainEnvName: "0"}},
		{name: "unsupported setting", overrides: EnvironmentOverrides{"PROJECT_VALUE": "value"}},
		{name: "portable case collision", overrides: EnvironmentOverrides{"IP_ADDRESS": "127.77.0.8", "ip_address": "127.77.0.9"}},
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

// TestStartAcceptsPresentEmptyNetworkOverride proves host-layer values retain ordinary dotenv semantics.
func TestStartAcceptsPresentEmptyNetworkOverride(t *testing.T) {
	err := validateStartRequest(StartRequest{
		ProjectID:    "project-empty-override",
		SessionID:    "session-empty-override",
		CheckoutRoot: t.TempDir(),
		EnvironmentOverrides: EnvironmentOverrides{
			"LIGHTHOUSE_URL": "",
		},
	})
	if err != nil {
		t.Fatalf("validateStartRequest() error = %v", err)
	}
}

// TestEnvironmentReplacementPreservesUnrelatedValues verifies deterministic explicit values are appended once without mutating the captured base.
func TestEnvironmentReplacementPreservesUnrelatedValues(t *testing.T) {
	base := []string{
		"APP_NAME=harbor",
		"HOME=/tmp/home",
		"FORJ_DEV_PLAIN=0",
		"FORJ_APP=harbord",
		"FORJ_BUILD_PROGRESS=harbor-progress",
		"FORJ_COMMAND_PREFIX=harbor harbord",
		"PATH=/bin",
		"FORJ_DEV_PLAIN=2",
		"DEV_SERVICE_IP_ADDRESS=127.0.0.7",
		"IP_ADDRESS=127.0.0.8",
		"API_HTTP_HOST=127.0.0.9",
		"ip_address=127.0.0.10",
		"UNRELATED=preserved",
	}
	before := append([]string(nil), base...)
	result := withDevelopmentEnvironment(base, EnvironmentOverrides{
		"API_HTTP_HOST":          "127.77.0.42",
		"DEV_SERVICE_IP_ADDRESS": "127.0.0.42",
		"IP_ADDRESS":             "127.0.0.42",
	})
	want := "HOME=/tmp/home|PATH=/bin"
	if runtime.GOOS != "windows" {
		want += "|ip_address=127.0.0.10"
	}
	want += "|UNRELATED=preserved|FORJ_DEV_PLAIN=1"
	if strings.Join(result, "|") != want {
		t.Fatalf("withDevelopmentEnvironment() = %q, want %q", strings.Join(result, "|"), want)
	}
	if strings.Join(base, "|") != strings.Join(before, "|") {
		t.Fatalf("withDevelopmentEnvironment() mutated base = %#v, want %#v", base, before)
	}
}

// TestOutputRelaySerializesReadableChunks verifies shared destinations never receive byte-interleaved pipe reads.
func TestOutputRelaySerializesReadableChunks(t *testing.T) {
	trace := &recordingWriteCloser{}
	relay := newOutputRelayWithTrace(io.Discard, io.Discard, trace, 8)
	var readers sync.WaitGroup
	readers.Add(2)
	go readOutputStream(strings.NewReader("one\ntwo\n"), outputStreamStdout, relay, &readers)
	go readOutputStream(strings.NewReader("error\n"), outputStreamStderr, relay, &readers)
	readers.Wait()
	relay.finish()
	writes := trace.snapshot()
	if len(writes) != 2 {
		t.Fatalf("trace writes = %#v, want two serialized reads", writes)
	}
	seen := map[string]bool{}
	for _, write := range writes {
		seen[write] = true
	}
	if !seen["one\ntwo\n"] || !seen["error\n"] {
		t.Fatalf("trace writes = %#v, want both intact stream chunks", writes)
	}
}

// TestOutputRelayBoundsNewlineFreeReads proves arbitrary output is offered in small fixed-size records without waiting for a newline.
func TestOutputRelayBoundsNewlineFreeReads(t *testing.T) {
	trace := &recordingWriteCloser{}
	relay := newOutputRelayWithTrace(io.Discard, io.Discard, trace, 8)
	want := strings.Repeat("x", outputReadBufferBytes*3+17)
	readOutputChunks(strings.NewReader(want), outputStreamStdout, relay)
	relay.finish()

	writes := trace.snapshot()
	if len(writes) != 4 {
		t.Fatalf("output writes = %d, want 4", len(writes))
	}
	for index, write := range writes {
		if len(write) > outputReadBufferBytes {
			t.Fatalf("output write %d bytes = %d, want <= %d", index, len(write), outputReadBufferBytes)
		}
	}
	if got := strings.Join(writes, ""); got != want {
		t.Fatalf("joined newline-free output bytes = %d, want %d", len(got), len(want))
	}
}

// TestOutputRelayPublishesReadableChunkBeforeEOF proves a blocked child pipe does not hide newline-free progress.
func TestOutputRelayPublishesReadableChunkBeforeEOF(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	reader := newStagedOutputReader([]byte("compiling"))
	done := make(chan struct{})
	go func() {
		readOutputChunks(reader, outputStreamStdout, relay)
		close(done)
	}()

	<-reader.blocked
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	chunk, err := relay.transcript.wait(ctx, 0)
	if err != nil || chunk.Text != "compiling" {
		t.Fatalf("immediate output = %#v, %v", chunk, err)
	}

	close(reader.release)
	<-done
	relay.finish()
}

// TestOutputChunksRetainIncompleteUTF8AcrossReads keeps pipe boundaries from corrupting transcript cursors.
func TestOutputChunksRetainIncompleteUTF8AcrossReads(t *testing.T) {
	relay := newOutputRelay(io.Discard, io.Discard, 2)
	encoded := []byte("界")
	reader := &segmentedOutputReader{segments: [][]byte{encoded[:2], encoded[2:]}}
	readOutputChunks(reader, outputStreamStdout, relay)
	relay.finish()

	chunk := relay.transcript.read(0)
	if chunk.Text != "界" || !utf8.ValidString(chunk.Text) || chunk.NextCursor != uint64(len(encoded)) {
		t.Fatalf("split UTF-8 output = %#v", chunk)
	}
}

// startResult carries one concurrent launch attempt back to the coordinating test goroutine.
type startResult struct {
	handle *Handle
	err    error
}

// stagedOutputReader exposes when the output loop has relayed its first fragment and blocked for more.
type stagedOutputReader struct {
	first   []byte
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

// newStagedOutputReader constructs one reader whose EOF is controlled by the test.
func newStagedOutputReader(first []byte) *stagedOutputReader {
	return &stagedOutputReader{
		first:   append([]byte(nil), first...),
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
}

// Read returns the staged fragment before blocking so the test can inspect output before EOF.
func (reader *stagedOutputReader) Read(output []byte) (int, error) {
	if len(reader.first) > 0 {
		count := copy(output, reader.first)
		reader.first = reader.first[count:]
		return count, nil
	}
	reader.once.Do(func() { close(reader.blocked) })
	<-reader.release
	return 0, io.EOF
}

// segmentedOutputReader returns caller-selected pipe boundaries before EOF.
type segmentedOutputReader struct {
	segments [][]byte
}

// Read copies exactly one remaining segment so UTF-8 boundary behavior stays deterministic.
func (reader *segmentedOutputReader) Read(output []byte) (int, error) {
	if len(reader.segments) == 0 {
		return 0, io.EOF
	}
	segment := reader.segments[0]
	reader.segments = reader.segments[1:]
	count := copy(output, segment)
	if count != len(segment) {
		return count, io.ErrShortBuffer
	}
	return count, nil
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

// recordingWriteCloser makes deterministic relay trace assertions without filesystem I/O.
type recordingWriteCloser struct {
	recordingWriter
}

// Close completes the in-memory trace contract.
func (writer *recordingWriteCloser) Close() error {
	return nil
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
