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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

const (
	defaultGracePeriod       = 3 * time.Second
	defaultOutputBufferLines = 256
	forceSettlementPeriod    = time.Second
	forceSettlementPoll      = 10 * time.Millisecond
)

var (
	// ErrClosed means the supervisor no longer accepts new processes.
	ErrClosed = errors.New("project process supervisor is closed")
	// ErrInvalidRequest means the launch identity or checkout root is incomplete.
	ErrInvalidRequest = errors.New("invalid project process request")
	// ErrProjectRunning means Harbor already owns a process for the requested project.
	ErrProjectRunning = errors.New("project process is already running")
	// ErrSessionRunning means Harbor already owns a process for the requested session.
	ErrSessionRunning = errors.New("project session process is already running")
	// ErrNotRunning means no process matches both requested identities.
	ErrNotRunning = errors.New("project process is not running")
)

// Options controls bounded shutdown and output buffering behavior.
type Options struct {
	GracePeriod       time.Duration
	OutputBufferLines int
	// Environment isolates child projects from Harbor's subsequently loaded application configuration.
	Environment Environment
}

// Environment is the ambient user process environment inherited by managed development commands.
type Environment []string

// CaptureEnvironment snapshots the current process environment before Harbor loads its own application configuration.
func CaptureEnvironment() Environment {
	return append(Environment(nil), os.Environ()...)
}

// StartRequest identifies the registered checkout and best-effort line destinations for its development output.
type StartRequest struct {
	ProjectID    domain.ProjectID
	SessionID    domain.SessionID
	CheckoutRoot string
	Stdout       io.Writer
	Stderr       io.Writer
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
	StopRequested      bool
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
	mu          sync.Mutex
	closed      bool
	gracePeriod time.Duration
	outputLines int
	environment Environment
	projects    map[domain.ProjectID]*managedProcess
	sessions    map[domain.SessionID]*managedProcess
}

// New constructs an empty project process supervisor.
func New(options Options) *Supervisor {
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
	return &Supervisor{
		gracePeriod: gracePeriod,
		outputLines: outputLines,
		environment: environment,
		projects:    make(map[domain.ProjectID]*managedProcess),
		sessions:    make(map[domain.SessionID]*managedProcess),
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
	if err := validateStartRequest(request); err != nil {
		return nil, err
	}

	checkoutRoot, err := canonicalDirectory(request.CheckoutRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize checkout root: %w", err)
	}
	executable, err := exec.LookPath("forj")
	if err != nil {
		return nil, fmt.Errorf("resolve forj executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("make forj executable absolute: %w", err)
	}
	executableIdentity, err := canonicalExecutable(executable)
	if err != nil {
		return nil, fmt.Errorf("canonicalize forj executable: %w", err)
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

	command := exec.Command(executable, "dev")
	command.Dir = checkoutRoot
	command.Env = withDevelopmentEnvironment(supervisor.environment)
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
	relay := newOutputRelay(request.Stdout, request.Stderr, supervisor.outputLines)
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
	go readOutputLines(stdout, outputStreamStdout, relay, &pipeReaders)
	go readOutputLines(stderr, outputStreamStderr, relay, &pipeReaders)
	// The parent copies must close immediately so EOF represents the complete child tree, not Harbor itself.
	if err := errors.Join(stdoutChild.Close(), stderrChild.Close()); err != nil {
		terminateStartedCommand(command, platform)
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, fmt.Errorf("release parent output pipes: %w", err)
	}
	birthToken, err := platform.attach(command.Process)
	if err != nil {
		terminateStartedCommand(command, platform)
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, fmt.Errorf("capture forj process ownership: %w", err)
	}
	if err := ctx.Err(); err != nil {
		terminateStartedCommand(command, platform)
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, err
	}
	if err := platform.resume(command.Process); err != nil {
		terminateStartedCommand(command, platform)
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, fmt.Errorf("resume forj process: %w", err)
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
				ExecutableIdentity: executableIdentity,
				ArgumentsSHA256:    digestArguments(arguments),
			},
			StartedAt: time.Now().UTC(),
		},
		done: make(chan struct{}),
	}
	process := &managedProcess{
		command:       command,
		platform:      platform,
		relay:         relay,
		stdout:        stdout,
		stderr:        stderr,
		pipeReaders:   &pipeReaders,
		handle:        handle,
		gracePeriod:   supervisor.gracePeriod,
		acceptingStop: true,
		forced:        make(chan struct{}),
		signalsDone:   make(chan struct{}),
		stopComplete:  make(chan struct{}),
	}
	supervisor.projects[request.ProjectID] = process
	supervisor.sessions[request.SessionID] = process
	go supervisor.wait(process)
	return handle, nil
}

// terminateStartedCommand kills both the owned tree and its root because Windows attachment can fail before Job ownership exists.
func terminateStartedCommand(command *exec.Cmd, platform *platformProcess) {
	_ = platform.force(command.Process.Pid)
	_ = command.Process.Kill()
	_ = command.Wait()
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
	return errors.Join(ctx.Err(), closeErr)
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
	supervisor.mu.Unlock()

	exitCode := 0
	if process.command.ProcessState != nil {
		exitCode = process.command.ProcessState.ExitCode()
	}
	process.handle.complete(Exit{
		ProjectID:          info.ProjectID,
		SessionID:          info.SessionID,
		ExitCode:           exitCode,
		Err:                errors.Join(err, cleanupErr),
		StopRequested:      stopRequested,
		DroppedOutputLines: process.relay.dropped.Load(),
		ExitedAt:           time.Now().UTC(),
	})
	process.stopErr = cleanupErr
	close(process.stopComplete)
}

// managedProcess retains the private handles needed to stop and reap one process tree.
type managedProcess struct {
	command       *exec.Cmd
	platform      *platformProcess
	relay         *outputRelay
	stdout        *os.File
	stderr        *os.File
	pipeReaders   *sync.WaitGroup
	handle        *Handle
	gracePeriod   time.Duration
	acceptingStop bool
	stopRequested atomic.Bool
	stopOnce      sync.Once
	forceOnce     sync.Once
	signalMu      sync.Mutex
	signalsClosed bool
	treeSettled   bool
	forceErr      error
	forced        chan struct{}
	signalsDone   chan struct{}
	stopErr       error
	stopComplete  chan struct{}
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
	return nil
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

// withDevelopmentEnvironment replaces Harbor's plain-mode switch while preserving the user's inherited environment.
func withDevelopmentEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	found := false
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && environmentNameEqual(name, "FORJ_DEV_PLAIN") {
			if !found {
				result = append(result, "FORJ_DEV_PLAIN=1")
				found = true
			}
			continue
		}
		result = append(result, entry)
	}
	if !found {
		result = append(result, "FORJ_DEV_PLAIN=1")
	}
	return result
}
