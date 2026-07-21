package projectprocess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

const (
	defaultGracePeriod       = 3 * time.Second
	defaultOutputBufferLines = 256
	defaultServiceLogIdle    = 40 * time.Second
	forceSettlementPeriod    = time.Second
	forceSettlementPoll      = 10 * time.Millisecond
	developmentPlainEnvName  = "FORJ_DEV_PLAIN"
)

var developmentLaunchIsolationNames = []string{
	"APP_NAME",
	"FORJ_APP",
	"FORJ_BUILD_PROGRESS",
	"FORJ_COMMAND_PREFIX",
	ManagedLaunchContextEnvironment,
	developmentPlainEnvName,
}

var (
	// ErrClosed means the supervisor no longer accepts new processes.
	ErrClosed = errors.New("project process supervisor is closed")
	// ErrInvalidRequest means the launch identity or checkout root is incomplete.
	ErrInvalidRequest = errors.New("invalid project process request")
	// ErrCleanupUncertain means an accepted process did not prove its complete ownership scope was retired.
	ErrCleanupUncertain = errors.New("project process cleanup is uncertain")
	// ErrProjectRunning means Harbor already owns a process for the requested project.
	ErrProjectRunning = errors.New("project process is already running")
	// ErrSessionRunning means Harbor already owns a process for the requested session.
	ErrSessionRunning = errors.New("project session process is already running")
	// ErrNotRunning means no process matches both requested identities.
	ErrNotRunning = errors.New("project process is not running")
)

// Options controls bounded shutdown and output buffering behavior.
type Options struct {
	GracePeriod time.Duration
	// OutputBufferLines bounds queued output records; its original name remains for API compatibility.
	OutputBufferLines int
	// ContainerRuntime observes host Compose services without transferring their lifecycle authority to Harbor.
	ContainerRuntime containerruntime.Runtime
	// ServiceLogIdlePeriod bounds how long a log follower remains after the desktop stops renewing reads.
	ServiceLogIdlePeriod time.Duration
	// OutputSpoolDirectory overrides the per-user runtime root used for persisted session output history.
	OutputSpoolDirectory string
	// Environment isolates child projects from Harbor's subsequently loaded application configuration.
	Environment Environment
}

// Environment is the ambient user process environment inherited by managed development commands.
type Environment []string

// EnvironmentOverrides contains the explicit network values Harbor owns in one project's host dotenv layer.
type EnvironmentOverrides map[string]string

// CaptureEnvironment snapshots the current process environment before Harbor loads its own application configuration.
func CaptureEnvironment() Environment {
	return append(Environment(nil), os.Environ()...)
}

// StartRequest identifies the registered checkout, managed environment, and best-effort destinations for its development output.
type StartRequest struct {
	ProjectID    domain.ProjectID
	SessionID    domain.SessionID
	CheckoutRoot string
	// GoForjExecutable binds a descriptor preflight to the exact executable image that will be launched.
	GoForjExecutable     string
	EnvironmentOverrides EnvironmentOverrides
	// ManagedLaunch carries one owner-only session credential without changing the ordinary dev argv.
	ManagedLaunch *ManagedLaunchContext
	Stdout        io.Writer
	Stderr        io.Writer
}

// Evidence binds later process actions to one exact executable birth instead of a reusable PID.
type Evidence struct {
	PID                int64
	BirthToken         string
	ExecutableIdentity string
	ArgumentsSHA256    string
}

// Info describes the exact command accepted by the operating system.
type Info struct {
	ProjectID    domain.ProjectID
	SessionID    domain.SessionID
	CheckoutRoot string
	Arguments    []string
	Evidence     Evidence
	StartedAt    time.Time
}

// Exit describes one completed child process and whether Harbor requested its shutdown.
type Exit struct {
	ProjectID          domain.ProjectID
	SessionID          domain.SessionID
	ExitCode           int
	Err                error
	ScopeSettlementErr error
	StopRequested      bool
	// DroppedOutputLines counts dropped output records; its original name remains for API compatibility.
	DroppedOutputLines uint64
	ExitedAt           time.Time
}

// Handle observes one launched process without exposing mutable operating-system handles.
type Handle struct {
	info   Info
	done   chan struct{}
	mu     sync.RWMutex
	result Exit
	exited bool
}

// Info returns an immutable copy of the accepted launch metadata.
func (handle *Handle) Info() Info {
	info := handle.info
	info.Arguments = append([]string(nil), handle.info.Arguments...)
	return info
}

// Done closes after the process exits and its pipes have been drained into the bounded best-effort output relay.
func (handle *Handle) Done() <-chan struct{} {
	return handle.done
}

// Result returns the exit result after Done closes.
func (handle *Handle) Result() (Exit, bool) {
	handle.mu.RLock()
	defer handle.mu.RUnlock()
	return handle.result, handle.exited
}

// Wait waits for process completion or for the caller to stop waiting.
func (handle *Handle) Wait(ctx context.Context) (Exit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-handle.done:
		result, _ := handle.Result()
		return result, nil
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	}
}

// complete publishes the process result once pipe readers can no longer produce output.
func (handle *Handle) complete(result Exit) {
	handle.mu.Lock()
	handle.result = result
	handle.exited = true
	handle.mu.Unlock()
	close(handle.done)
}

// Supervisor owns every process tree it launches until exit or shutdown.
type Supervisor struct {
	mu                   sync.Mutex
	closed               bool
	gracePeriod          time.Duration
	outputLines          int
	environment          Environment
	verifyExecutable     ExecutableVerifier
	projects             map[domain.ProjectID]*managedProcess
	sessions             map[domain.SessionID]*managedProcess
	containerRuntime     containerruntime.Runtime
	runtimeCloseOnce     sync.Once
	runtimeCloseErr      error
	serviceLogIdle       time.Duration
	serviceLogs          map[serviceLogKey]*serviceLogStream
	outputSpoolDirectory string
}

// New constructs an empty project process supervisor.
func New(options Options) *Supervisor {
	return NewWithExecutableVerifier(options, productionGoForjExecutableVerifier())
}

// NewWithExecutableVerifier constructs a supervisor around an explicit side-effect-free executable compatibility boundary.
func NewWithExecutableVerifier(options Options, verifier ExecutableVerifier) *Supervisor {
	if verifier == nil {
		panic("projectprocess.NewWithExecutableVerifier requires a non-nil executable verifier")
	}
	gracePeriod := options.GracePeriod
	if gracePeriod <= 0 {
		gracePeriod = defaultGracePeriod
	}
	outputLines := options.OutputBufferLines
	if outputLines <= 0 {
		outputLines = defaultOutputBufferLines
	}
	environment := options.Environment
	if environment == nil {
		environment = CaptureEnvironment()
	} else {
		environment = append(Environment(nil), environment...)
	}
	containerRuntime := options.ContainerRuntime
	if containerRuntime == nil {
		configured, err := containerruntime.NewDocker()
		if err != nil {
			containerRuntime = containerruntime.NewUnavailable(err)
		} else {
			containerRuntime = configured
		}
	}
	serviceLogIdle := options.ServiceLogIdlePeriod
	if serviceLogIdle <= 0 {
		serviceLogIdle = defaultServiceLogIdle
	}
	outputSpoolDirectory := resolveOutputSpoolDirectory(options.OutputSpoolDirectory)
	return &Supervisor{
		gracePeriod:          gracePeriod,
		outputLines:          outputLines,
		environment:          environment,
		verifyExecutable:     verifier,
		projects:             make(map[domain.ProjectID]*managedProcess),
		sessions:             make(map[domain.SessionID]*managedProcess),
		containerRuntime:     containerRuntime,
		serviceLogIdle:       serviceLogIdle,
		serviceLogs:          make(map[serviceLogKey]*serviceLogStream),
		outputSpoolDirectory: outputSpoolDirectory,
	}
}

// Start launches the checkout's current GoForj development command without a shell or terminal.
func (supervisor *Supervisor) Start(ctx context.Context, request StartRequest) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request.EnvironmentOverrides = cloneEnvironmentOverrides(request.EnvironmentOverrides)
	if request.ManagedLaunch != nil {
		managedLaunch := request.ManagedLaunch.Clone()
		request.ManagedLaunch = &managedLaunch
	}
	if err := validateStartRequest(request); err != nil {
		return nil, err
	}

	checkoutRoot, err := canonicalDirectory(request.CheckoutRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize checkout root: %w", err)
	}
	executable, err := supervisor.acceptedGoForjExecutable(request.GoForjExecutable)
	if err != nil {
		return nil, err
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrClosed
	}
	if _, exists := supervisor.projects[request.ProjectID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrProjectRunning, request.ProjectID)
	}
	if _, exists := supervisor.sessions[request.SessionID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrSessionRunning, request.SessionID)
	}
	spool, spoolErr := openOutputSpool(supervisor.outputSpoolDirectory, request.ProjectID, request.SessionID)
	if spoolErr != nil {
		// Output history is diagnostic state; a corrupt or unavailable spool must never block process ownership.
		spool = nil
	}
	spoolCleanup := spool != nil
	defer func() {
		if spoolCleanup {
			_ = spool.close()
		}
	}()
	managedOverrides, err := writeManagedHostEnvironment(checkoutRoot, request.EnvironmentOverrides)
	if err != nil {
		return nil, fmt.Errorf("apply Harbor managed host environment: %w", err)
	}
	if err := validateEnvironmentOverrides(request.EnvironmentOverrides); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request.ManagedLaunch != nil {
		if request.ManagedLaunch.ProjectID != request.ProjectID || request.ManagedLaunch.SessionID != request.SessionID {
			return nil, fmt.Errorf("%w: managed launch context identity does not match process request", ErrInvalidRequest)
		}
		if request.ManagedLaunch.ProjectRoot != checkoutRoot {
			return nil, fmt.Errorf("%w: managed launch context project root does not match checkout", ErrInvalidRequest)
		}
	}

	command := exec.Command(executable, "dev")
	command.Dir = checkoutRoot
	managedLaunchPath := ""
	if request.ManagedLaunch != nil {
		managedLaunchPath, err = writeManagedLaunchContext(*request.ManagedLaunch)
		if err != nil {
			return nil, err
		}
	}
	managedLaunchCleanup := true
	defer func() {
		if managedLaunchCleanup {
			_ = removeManagedLaunchContext(managedLaunchPath)
		}
	}()
	command.Env = withDevelopmentEnvironment(supervisor.environment, managedOverrides, managedLaunchPath)
	stdout, stdoutChild, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open forj stdout: %w", err)
	}
	stderr, stderrChild, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutChild.Close()
		return nil, fmt.Errorf("open forj stderr: %w", err)
	}
	command.Stdout = stdoutChild
	command.Stderr = stderrChild
	platform, err := preparePlatformProcess(command)
	if err != nil {
		_ = stdout.Close()
		_ = stdoutChild.Close()
		_ = stderr.Close()
		_ = stderrChild.Close()
		return nil, fmt.Errorf("prepare forj process ownership: %w", err)
	}
	startedAt := time.Now().UTC()
	trace, err := openProjectLaunchTrace(checkoutRoot, request.ProjectID, request.SessionID, startedAt)
	if err != nil {
		_ = stdout.Close()
		_ = stdoutChild.Close()
		_ = stderr.Close()
		_ = stderrChild.Close()
		platform.close()
		return nil, err
	}
	relay := newOutputRelayWithTraceAndSpool(request.Stdout, request.Stderr, trace, spool, supervisor.outputLines)
	spoolCleanup = false
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutChild.Close()
		_ = stderr.Close()
		_ = stderrChild.Close()
		platform.close()
		relay.finish()
		return nil, fmt.Errorf("start forj dev: %w", err)
	}

	var pipeReaders sync.WaitGroup
	pipeReaders.Add(2)
	go readOutputStream(stdout, outputStreamStdout, relay, &pipeReaders)
	go readOutputStream(stderr, outputStreamStderr, relay, &pipeReaders)
	// The parent copies must close immediately so EOF represents the complete child tree, not Harbor itself.
	if err := errors.Join(stdoutChild.Close(), stderrChild.Close()); err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		if cleanupErr != nil {
			_ = stdout.Close()
			_ = stderr.Close()
		}
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, acceptedProcessStartError("release parent output pipes", err, cleanupErr)
	}
	birthToken, err := platform.attach(command.Process)
	if err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		if cleanupErr != nil {
			_ = stdout.Close()
			_ = stderr.Close()
		}
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, acceptedProcessStartError("capture forj process ownership", err, cleanupErr)
	}
	if err := ctx.Err(); err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		if cleanupErr != nil {
			_ = stdout.Close()
			_ = stderr.Close()
		}
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, acceptedProcessStartError("cancel accepted forj process", err, cleanupErr)
	}
	if err := platform.resume(command.Process); err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		if cleanupErr != nil {
			_ = stdout.Close()
			_ = stderr.Close()
		}
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, acceptedProcessStartError("resume forj process", err, cleanupErr)
	}

	arguments := append([]string(nil), command.Args...)
	handle := &Handle{
		info: Info{
			ProjectID:    request.ProjectID,
			SessionID:    request.SessionID,
			CheckoutRoot: checkoutRoot,
			Arguments:    arguments,
			Evidence: Evidence{
				PID:                int64(command.Process.Pid),
				BirthToken:         birthToken,
				ExecutableIdentity: executable,
				ArgumentsSHA256:    digestArguments(arguments),
			},
			StartedAt: startedAt,
		},
		done: make(chan struct{}),
	}
	process := &managedProcess{
		command:           command,
		platform:          platform,
		relay:             relay,
		stdout:            stdout,
		stderr:            stderr,
		pipeReaders:       &pipeReaders,
		handle:            handle,
		gracePeriod:       supervisor.gracePeriod,
		acceptingStop:     true,
		forced:            make(chan struct{}),
		signalsDone:       make(chan struct{}),
		stopComplete:      make(chan struct{}),
		managedLaunchPath: managedLaunchPath,
	}
	supervisor.projects[request.ProjectID] = process
	supervisor.sessions[request.SessionID] = process
	managedLaunchCleanup = false
	go supervisor.wait(process)
	return handle, nil
}

// terminateStartedCommand keeps the unreaped root identity reserved while retiring every accepted descendant.
func terminateStartedCommand(command *exec.Cmd, platform *platformProcess) error {
	forceErr := platform.force(command.Process.Pid)
	rootErr := command.Process.Kill()
	if errors.Is(rootErr, os.ErrProcessDone) {
		rootErr = nil
	}
	_ = command.Wait()
	return errors.Join(forceErr, rootErr)
}

// acceptedProcessStartError distinguishes a safe post-acceptance failure from unresolved process authority.
func acceptedProcessStartError(operation string, cause, cleanupErr error) error {
	if cleanupErr == nil {
		return fmt.Errorf("%s: %w", operation, cause)
	}
	return fmt.Errorf("%w: %s: %w", ErrCleanupUncertain, operation, errors.Join(cause, cleanupErr))
}

// Stop gracefully stops the exact project and session process before escalating after the configured grace period.
func (supervisor *Supervisor) Stop(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	supervisor.mu.Lock()
	process, projectExists := supervisor.projects[projectID]
	if !projectExists || supervisor.sessions[sessionID] != process || !process.acceptingStop {
		supervisor.mu.Unlock()
		return fmt.Errorf("%w: project %q session %q", ErrNotRunning, projectID, sessionID)
	}
	process.requestStop()
	supervisor.mu.Unlock()

	select {
	case <-process.stopComplete:
		return process.stopErr
	case <-ctx.Done():
		return errors.Join(ctx.Err(), process.forceStop())
	}
}

// Close rejects new starts, requests every owned process to stop, and joins all process waiters.
func (supervisor *Supervisor) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	supervisor.mu.Lock()
	supervisor.closed = true
	processes := make([]*managedProcess, 0, len(supervisor.projects))
	for _, process := range supervisor.projects {
		processes = append(processes, process)
		if process.acceptingStop {
			process.requestStop()
		}
	}
	supervisor.mu.Unlock()

	deadlineReached := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			for _, process := range processes {
				process.forceStop()
			}
		case <-deadlineReached:
		}
	}()
	var closeErr error
	for _, process := range processes {
		<-process.stopComplete
		closeErr = errors.Join(closeErr, process.stopErr)
	}
	close(deadlineReached)
	supervisor.mu.Lock()
	remainingServiceLogs := make([]*serviceLogStream, 0, len(supervisor.serviceLogs))
	for _, stream := range supervisor.serviceLogs {
		remainingServiceLogs = append(remainingServiceLogs, stream)
	}
	supervisor.mu.Unlock()
	for _, stream := range remainingServiceLogs {
		err := stream.stop()
		closeErr = errors.Join(closeErr, err)
		if err == nil {
			supervisor.removeSettledServiceLogStream(stream.key, stream)
		}
	}
	supervisor.runtimeCloseOnce.Do(func() {
		supervisor.runtimeCloseErr = supervisor.containerRuntime.Close()
	})
	return errors.Join(ctx.Err(), closeErr, supervisor.runtimeCloseErr)
}

// wait reaps one child, drains its pipes, releases platform handles, and publishes one immutable result.
func (supervisor *Supervisor) wait(process *managedProcess) {
	err := process.command.Wait()
	supervisor.mu.Lock()
	process.acceptingStop = false
	stopRequested := process.stopRequested.Load()
	supervisor.mu.Unlock()
	cleanupErr := process.settleTree(stopRequested)
	if cleanupErr != nil {
		_ = process.stdout.Close()
		_ = process.stderr.Close()
	}
	process.pipeReaders.Wait()
	process.relay.finish()
	process.platform.close()

	process.handle.mu.RLock()
	info := process.handle.info
	process.handle.mu.RUnlock()
	supervisor.mu.Lock()
	if supervisor.projects[info.ProjectID] == process {
		delete(supervisor.projects, info.ProjectID)
	}
	if supervisor.sessions[info.SessionID] == process {
		delete(supervisor.sessions, info.SessionID)
	}
	serviceLogs := supervisor.detachServiceLogsLocked(info.ProjectID, info.SessionID)
	supervisor.mu.Unlock()
	for _, stream := range serviceLogs {
		streamErr := stream.stop()
		cleanupErr = errors.Join(cleanupErr, streamErr)
		if streamErr == nil {
			supervisor.removeSettledServiceLogStream(stream.key, stream)
		}
	}
	if stopRequested && cleanupErr == nil {
		cleanupErr = removeManagedHostEnvironment(info.CheckoutRoot)
	}
	cleanupErr = errors.Join(cleanupErr, removeManagedLaunchContext(process.managedLaunchPath))

	exitCode := 0
	if process.command.ProcessState != nil {
		exitCode = process.command.ProcessState.ExitCode()
	}
	process.handle.complete(Exit{
		ProjectID:          info.ProjectID,
		SessionID:          info.SessionID,
		ExitCode:           exitCode,
		Err:                errors.Join(err, cleanupErr),
		ScopeSettlementErr: cleanupErr,
		StopRequested:      stopRequested,
		DroppedOutputLines: process.relay.dropped.Load(),
		ExitedAt:           time.Now().UTC(),
	})
	process.stopErr = cleanupErr
	close(process.stopComplete)
}

// managedProcess retains the private handles needed to stop and reap one process tree.
type managedProcess struct {
	command           *exec.Cmd
	platform          *platformProcess
	relay             *outputRelay
	stdout            *os.File
	stderr            *os.File
	pipeReaders       *sync.WaitGroup
	handle            *Handle
	gracePeriod       time.Duration
	acceptingStop     bool
	stopRequested     atomic.Bool
	stopOnce          sync.Once
	forceOnce         sync.Once
	signalMu          sync.Mutex
	signalsClosed     bool
	treeSettled       bool
	forceErr          error
	forced            chan struct{}
	signalsDone       chan struct{}
	stopErr           error
	stopComplete      chan struct{}
	managedLaunchPath string
}

// requestStop starts the one bounded graceful-shutdown sequence shared by concurrent callers.
func (process *managedProcess) requestStop() {
	process.stopRequested.Store(true)
	process.stopOnce.Do(func() {
		if err := process.gracefulStop(); err != nil {
			_ = process.forceStop()
		}
		go process.enforceStopDeadline()
	})
}

// enforceStopDeadline escalates one requested stop unless the wait owner first settles or visibly relinquishes the tree.
func (process *managedProcess) enforceStopDeadline() {
	timer := time.NewTimer(process.gracePeriod)
	defer timer.Stop()
	select {
	case <-timer.C:
		_ = process.forceStop()
	case <-process.signalsDone:
	}
}

// settleTree keeps numeric signaling authority serialized until the owned process tree is absent or cleanup visibly fails.
func (process *managedProcess) settleTree(stopRequested bool) error {
	defer process.closeSignals()

	alive, err := process.observeTree()
	if err != nil {
		return fmt.Errorf("observe forj process tree: %w", err)
	}
	if !alive {
		return nil
	}
	if !stopRequested {
		if err := process.forceStop(); err != nil {
			return fmt.Errorf("terminate unexpected forj process tree: %w", err)
		}
	}

	var forceDeadline time.Time
	for {
		alive, err = process.observeTree()
		if err != nil {
			return fmt.Errorf("observe forj process tree: %w", err)
		}
		if !alive {
			return nil
		}
		if forceDeadline.IsZero() {
			select {
			case <-process.forced:
				if process.forceErr != nil {
					return fmt.Errorf("terminate forj process tree: %w", process.forceErr)
				}
				forceDeadline = time.Now().Add(forceSettlementPeriod)
			default:
			}
		} else if time.Now().After(forceDeadline) {
			return fmt.Errorf("forj process tree remained active %s after forceful termination", forceSettlementPeriod)
		}
		time.Sleep(forceSettlementPoll)
	}
}

// observeTree marks the numeric process identity settled while holding the same lock used by every later signal.
func (process *managedProcess) observeTree() (bool, error) {
	process.signalMu.Lock()
	defer process.signalMu.Unlock()
	if process.signalsClosed || process.treeSettled {
		return false, nil
	}
	alive, err := process.platform.treeAlive(process.command.Process.Pid)
	if err == nil && !alive {
		process.treeSettled = true
	}
	return alive, err
}

// gracefulStop signals the owned tree only while its numeric identity remains under Harbor's serialized authority.
func (process *managedProcess) gracefulStop() error {
	process.signalMu.Lock()
	defer process.signalMu.Unlock()
	if process.signalsClosed || process.treeSettled {
		return nil
	}
	return process.platform.graceful(process.command.Process.Pid)
}

// closeSignals cancels deadline escalation before platform handles or numeric ownership can be released.
func (process *managedProcess) closeSignals() {
	process.signalMu.Lock()
	defer process.signalMu.Unlock()
	if process.signalsClosed {
		return
	}
	process.signalsClosed = true
	close(process.signalsDone)
}

// forceStop escalates at most once while the serialized process identity remains owned by Harbor.
func (process *managedProcess) forceStop() error {
	process.forceOnce.Do(func() {
		process.signalMu.Lock()
		if !process.signalsClosed && !process.treeSettled {
			process.forceErr = process.platform.force(process.command.Process.Pid)
			if process.forceErr != nil {
				rootErr := process.command.Process.Kill()
				if errors.Is(rootErr, os.ErrProcessDone) {
					rootErr = nil
				}
				process.forceErr = errors.Join(process.forceErr, rootErr)
			}
		}
		process.signalMu.Unlock()
		close(process.forced)
	})
	<-process.forced
	return process.forceErr
}

// validateStartRequest excludes ambiguous process-map identities before any operating-system action.
func validateStartRequest(request StartRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := request.SessionID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if strings.TrimSpace(request.CheckoutRoot) == "" {
		return fmt.Errorf("%w: checkout root must not be empty", ErrInvalidRequest)
	}
	if err := validateEnvironmentOverrides(request.EnvironmentOverrides); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if request.ManagedLaunch != nil {
		if err := request.ManagedLaunch.Validate(); err != nil {
			return fmt.Errorf("%w: managed launch context: %v", ErrInvalidRequest, err)
		}
	}
	return nil
}

// validateEnvironmentOverrides keeps managed values portable and unambiguous across supported operating systems.
func validateEnvironmentOverrides(overrides EnvironmentOverrides) error {
	if len(overrides) == 0 {
		return errors.New("at least one environment override is required")
	}
	names := sortedEnvironmentOverrideNames(overrides)
	seenNames := make(map[string]string, len(names))
	for _, name := range names {
		if err := validateEnvironmentOverrideName(name); err != nil {
			return err
		}
		folded := strings.ToUpper(name)
		if previous, duplicate := seenNames[folded]; duplicate {
			return fmt.Errorf("environment override names %q and %q differ only by case", previous, name)
		}
		seenNames[folded] = name
		if _, supported := managedHostEnvironmentKeys[name]; !supported {
			return fmt.Errorf("environment override name %q is not a managed project network setting", name)
		}
		if strings.IndexByte(overrides[name], 0) >= 0 {
			return fmt.Errorf("environment override %q contains NUL in its value", name)
		}
	}
	return nil
}

// validateEnvironmentOverrideName keeps host dotenv assignments portable and excludes private launcher controls.
func validateEnvironmentOverrideName(name string) error {
	if name == "" {
		return errors.New("environment override name is required")
	}
	first := name[0]
	firstIsLetter := first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
	if !firstIsLetter && first != '_' {
		return fmt.Errorf("environment override name %q must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		letter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !letter && !digit && character != '_' {
			return fmt.Errorf("environment override name %q must match [A-Za-z_][A-Za-z0-9_]*", name)
		}
	}
	upperName := strings.ToUpper(name)
	if strings.HasPrefix(upperName, "FORJ_INTERNAL_") {
		return fmt.Errorf("environment override name %q is reserved by the managed project launcher", name)
	}
	switch upperName {
	case developmentPlainEnvName, "APP_ENV", "FORJ_APP", "FORJ_COMMAND_PREFIX", "FORJ_BUILD_PROGRESS":
		return fmt.Errorf("environment override name %q is reserved by the managed project launcher", name)
	}
	return nil
}

// cloneEnvironmentOverrides prevents caller mutation from changing an accepted launch while executable identity is resolved.
func cloneEnvironmentOverrides(overrides EnvironmentOverrides) EnvironmentOverrides {
	result := make(EnvironmentOverrides, len(overrides))
	for name, value := range overrides {
		result[name] = value
	}
	return result
}

// canonicalDirectory resolves aliases before the path becomes a process working-directory identity.
func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}

// canonicalExecutable resolves path aliases so persisted evidence names the executable image, not its launcher symlink.
func canonicalExecutable(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}

// digestArguments uses NUL separators because operating systems reject NUL inside individual arguments.
func digestArguments(arguments []string) string {
	hash := sha256.New()
	for _, argument := range arguments {
		_, _ = io.WriteString(hash, argument)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// environmentAssignment keeps managed values ordered while building the operating-system environment slice.
type environmentAssignment struct {
	name  string
	value string
}

// withDevelopmentEnvironment removes file-owned values from the ambient launch and selects non-interactive output.
func withDevelopmentEnvironment(environment []string, fileValues EnvironmentOverrides, managedLaunchPath ...string) []string {
	names := sortedEnvironmentOverrideNames(fileValues)
	replacedNames := append(append([]string(nil), names...), developmentLaunchIsolationNames...)
	assignments := []environmentAssignment{{name: developmentPlainEnvName, value: "1"}}
	if len(managedLaunchPath) > 0 && managedLaunchPath[0] != "" {
		assignments = append(assignments, environmentAssignment{name: ManagedLaunchContextEnvironment, value: managedLaunchPath[0]})
	}
	return mergeEnvironmentAssignments(environment, replacedNames, assignments)
}

// sortedEnvironmentOverrideNames makes file-owned environment removal independent of map iteration order.
func sortedEnvironmentOverrideNames(overrides EnvironmentOverrides) []string {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Slice(names, func(left int, right int) bool {
		leftFolded := strings.ToUpper(names[left])
		rightFolded := strings.ToUpper(names[right])
		if leftFolded != rightFolded {
			return leftFolded < rightFolded
		}
		return names[left] < names[right]
	})
	return names
}

// mergeEnvironmentAssignments removes every platform-equivalent base key before appending one deterministic final value.
func mergeEnvironmentAssignments(environment []string, replacedNames []string, assignments []environmentAssignment) []string {
	result := make([]string, 0, len(environment)+len(assignments))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && environmentNameReplaced(name, replacedNames) {
			continue
		}
		result = append(result, entry)
	}
	for _, assignment := range assignments {
		result = append(result, assignment.name+"="+assignment.value)
	}
	return result
}

// environmentNameReplaced applies the operating system's environment-key equality rules.
func environmentNameReplaced(name string, replacedNames []string) bool {
	for _, replacedName := range replacedNames {
		if environmentNameEqual(name, replacedName) {
			return true
		}
	}
	return false
}
