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
	// ErrResetInProgress means the canonical checkout still has an owned reset process being reaped.
	ErrResetInProgress = errors.New("project reset is still in progress")
	// ErrOutputBrokerAdoptionUnavailable means no reviewed live-broker adoption boundary is installed.
	ErrOutputBrokerAdoptionUnavailable = errors.New("output broker adoption is unavailable")
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
	// OutputBrokerLauncher optionally transfers child output pipes to a process-surviving broker.
	OutputBrokerLauncher OutputBrokerLauncher
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

// DownRequest identifies the verified registered checkout whose leftover GoForj runtime must be withdrawn.
type DownRequest struct {
	CheckoutRoot     string
	GoForjExecutable string
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
	// OutputBroker identifies the optional process-surviving output broker attached to this session.
	OutputBroker *OutputBrokerPeer
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
	if handle.info.OutputBroker != nil {
		peer := *handle.info.OutputBroker
		info.OutputBroker = &peer
	}
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
	mu                    sync.Mutex
	closed                bool
	gracePeriod           time.Duration
	outputLines           int
	environment           Environment
	verifyExecutable      ExecutableVerifier
	projects              map[domain.ProjectID]*managedProcess
	sessions              map[domain.SessionID]*managedProcess
	checkouts             map[string]*managedProcess
	launches              map[*launchReservation]struct{}
	launchProjects        map[domain.ProjectID]*launchReservation
	launchSessions        map[domain.SessionID]*launchReservation
	launchCheckouts       map[string]*launchReservation
	removeHostEnvironment func(string) error
	containerRuntime      containerruntime.Runtime
	runtimeCloseOnce      sync.Once
	runtimeCloseErr       error
	serviceLogIdle        time.Duration
	serviceLogs           map[serviceLogKey]*serviceLogStream
	outputSpoolDirectory  string
	outputBrokerLauncher  OutputBrokerLauncher
	adoptedOutputs        map[outputBrokerKey]*adoptedOutput
	resets                map[*resetProcess]struct{}
	resetCheckouts        map[string]*resetProcess
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
		gracePeriod:           gracePeriod,
		outputLines:           outputLines,
		environment:           environment,
		verifyExecutable:      verifier,
		projects:              make(map[domain.ProjectID]*managedProcess),
		sessions:              make(map[domain.SessionID]*managedProcess),
		checkouts:             make(map[string]*managedProcess),
		launches:              make(map[*launchReservation]struct{}),
		launchProjects:        make(map[domain.ProjectID]*launchReservation),
		launchSessions:        make(map[domain.SessionID]*launchReservation),
		launchCheckouts:       make(map[string]*launchReservation),
		removeHostEnvironment: removeManagedHostEnvironment,
		containerRuntime:      containerRuntime,
		serviceLogIdle:        serviceLogIdle,
		serviceLogs:           make(map[serviceLogKey]*serviceLogStream),
		outputSpoolDirectory:  outputSpoolDirectory,
		outputBrokerLauncher:  options.OutputBrokerLauncher,
		adoptedOutputs:        make(map[outputBrokerKey]*adoptedOutput),
		resets:                make(map[*resetProcess]struct{}),
		resetCheckouts:        make(map[string]*resetProcess),
	}
}

// launchReservation keeps conflicting starts excluded while one launch performs blocking setup outside Supervisor.mu.
type launchReservation struct {
	projectID    domain.ProjectID
	sessionID    domain.SessionID
	checkoutRoot string
	done         chan struct{}
}

// reserveLaunch atomically admits one launch identity before it performs filesystem or process work.
func (supervisor *Supervisor) reserveLaunch(request StartRequest, checkoutRoot string) (*launchReservation, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrClosed
	}
	if _, resetting := supervisor.resetCheckouts[checkoutRoot]; resetting {
		return nil, fmt.Errorf("%w: %s", ErrResetInProgress, checkoutRoot)
	}
	if _, exists := supervisor.projects[request.ProjectID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrProjectRunning, request.ProjectID)
	}
	if _, exists := supervisor.launchProjects[request.ProjectID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrProjectRunning, request.ProjectID)
	}
	if _, exists := supervisor.sessions[request.SessionID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrSessionRunning, request.SessionID)
	}
	if _, exists := supervisor.launchSessions[request.SessionID]; exists {
		return nil, fmt.Errorf("%w: %s", ErrSessionRunning, request.SessionID)
	}
	if _, exists := supervisor.checkouts[checkoutRoot]; exists {
		return nil, fmt.Errorf("%w: checkout %s", ErrProjectRunning, checkoutRoot)
	}
	if _, exists := supervisor.launchCheckouts[checkoutRoot]; exists {
		return nil, fmt.Errorf("%w: checkout %s", ErrProjectRunning, checkoutRoot)
	}
	reservation := &launchReservation{
		projectID:    request.ProjectID,
		sessionID:    request.SessionID,
		checkoutRoot: checkoutRoot,
		done:         make(chan struct{}),
	}
	supervisor.launches[reservation] = struct{}{}
	supervisor.launchProjects[reservation.projectID] = reservation
	supervisor.launchSessions[reservation.sessionID] = reservation
	supervisor.launchCheckouts[reservation.checkoutRoot] = reservation
	return reservation, nil
}

// releaseLaunchLocked removes exactly one pending launch reservation.
func (supervisor *Supervisor) releaseLaunchLocked(reservation *launchReservation) {
	if _, exists := supervisor.launches[reservation]; !exists {
		return
	}
	delete(supervisor.launches, reservation)
	if supervisor.launchProjects[reservation.projectID] == reservation {
		delete(supervisor.launchProjects, reservation.projectID)
	}
	if supervisor.launchSessions[reservation.sessionID] == reservation {
		delete(supervisor.launchSessions, reservation.sessionID)
	}
	if supervisor.launchCheckouts[reservation.checkoutRoot] == reservation {
		delete(supervisor.launchCheckouts, reservation.checkoutRoot)
	}
	close(reservation.done)
}

// outputBrokerKey selects one live output relay without pretending it is a child-process handle.
type outputBrokerKey struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
}

// adoptedOutput retains a live transcript for a broker that was adopted after daemon restart.
type adoptedOutput struct {
	attachment OutputBrokerAttachment
	relay      *outputRelay
	done       chan struct{}
}

// AdoptOutputBroker reconnects the supervisor's output surface to one exact durable broker session.
//
// This method intentionally does not add a synthetic child to the process maps. Stop and reap authority
// remains on the native recovery boundary for the managed GoForj process; the adopted broker contributes
// only live output, while its checksummed spool remains the fallback if the transport disappears.
func (supervisor *Supervisor) AdoptOutputBroker(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	broker domain.OutputBrokerSession,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if supervisor == nil {
		return ErrOutputBrokerAdoptionUnavailable
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("output broker adoption project ID: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return fmt.Errorf("output broker adoption session ID: %w", err)
	}
	if err := broker.Validate(); err != nil {
		return fmt.Errorf("validate output broker adoption evidence: %w", err)
	}
	launcher, ok := supervisor.outputBrokerLauncher.(OutputBrokerAdopter)
	if !ok {
		return ErrOutputBrokerAdoptionUnavailable
	}
	if supervisor.outputSpoolDirectory == "" {
		return ErrOutputBrokerAdoptionUnavailable
	}
	peer := OutputBrokerPeer{
		ProjectID:         projectID,
		SessionID:         sessionID,
		EndpointReference: broker.EndpointReference,
		Process:           broker.Process,
		ManifestPath:      broker.ManifestPath,
		TicketDigest:      broker.CredentialDigest,
	}
	if err := peer.Validate(); err != nil {
		return fmt.Errorf("validate output broker adoption peer: %w", err)
	}
	key := outputBrokerKey{projectID: projectID, sessionID: sessionID}
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return ErrClosed
	}
	if supervisor.adoptedOutputs == nil {
		supervisor.adoptedOutputs = make(map[outputBrokerKey]*adoptedOutput)
	}
	if existing := supervisor.adoptedOutputs[key]; existing != nil {
		if existing.attachment != nil && existing.attachment.Peer() == peer {
			supervisor.mu.Unlock()
			return nil
		}
		supervisor.mu.Unlock()
		return errors.New("output broker is already adopted for this lifecycle")
	}
	supervisor.mu.Unlock()

	attachment, err := launcher.Adopt(ctx, OutputBrokerAdoptionSpec{
		ProjectID:       projectID,
		SessionID:       sessionID,
		OutputDirectory: supervisor.outputSpoolDirectory,
		Peer:            peer,
	})
	if err != nil {
		return fmt.Errorf("adopt output broker: %w", err)
	}
	if attachment == nil {
		return errors.New("adopt output broker returned a nil attachment")
	}
	if adoptedPeer := attachment.Peer(); adoptedPeer != peer {
		_ = attachment.Close()
		return errors.New("adopted output broker peer differs from durable evidence")
	}
	relay := newOutputRelayWithTraceAndSpool(io.Discard, io.Discard, nil, nil, supervisor.outputLines)
	adopted := &adoptedOutput{attachment: attachment, relay: relay, done: make(chan struct{})}
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		_ = attachment.Close()
		relay.finish()
		return ErrClosed
	}
	if existing := supervisor.adoptedOutputs[key]; existing != nil {
		supervisor.mu.Unlock()
		_ = attachment.Close()
		relay.finish()
		if existing.attachment != nil && existing.attachment.Peer() == peer {
			return nil
		}
		return errors.New("output broker is already adopted for this lifecycle")
	}
	supervisor.adoptedOutputs[key] = adopted
	supervisor.mu.Unlock()
	go supervisor.readAdoptedOutput(key, adopted)
	return nil
}

// readAdoptedOutput feeds one adopted broker into the same bounded transcript used by live processes.
func (supervisor *Supervisor) readAdoptedOutput(key outputBrokerKey, adopted *adoptedOutput) {
	defer close(adopted.done)
	var readers sync.WaitGroup
	readers.Add(1)
	readOutputBrokerAttachment(context.Background(), adopted.attachment, adopted.relay, &readers, nil)
	readers.Wait()
	adopted.relay.finish()
	supervisor.mu.Lock()
	if supervisor.adoptedOutputs[key] == adopted {
		delete(supervisor.adoptedOutputs, key)
	}
	supervisor.mu.Unlock()
}

// Start launches the checkout's current GoForj development command without a shell or terminal.
func (supervisor *Supervisor) Start(ctx context.Context, request StartRequest) (handle *Handle, startErr error) {
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

	reservation, err := supervisor.reserveLaunch(request, checkoutRoot)
	if err != nil {
		return nil, err
	}
	promoted := false
	defer func() {
		if promoted {
			return
		}
		supervisor.mu.Lock()
		supervisor.releaseLaunchLocked(reservation)
		supervisor.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	brokerLauncher := supervisor.outputBrokerLauncher
	if brokerLauncher != nil {
		// Probe the broker's journal before child start so a corrupt diagnostic spool disables only the
		// optional broker path; ordinary direct-pipe output remains available for the lifecycle.
		if supervisor.outputSpoolDirectory == "" {
			brokerLauncher = nil
		} else if _, _, probeErr := readOutputSpool(supervisor.outputSpoolDirectory, request.ProjectID, request.SessionID); probeErr != nil {
			brokerLauncher = nil
		}
	}
	var spool *outputSpool
	var spoolErr error
	// When a broker is enabled it is the sole writer for this session's spool; two appenders would race cursor ownership.
	if brokerLauncher == nil {
		spool, spoolErr = openOutputSpool(supervisor.outputSpoolDirectory, request.ProjectID, request.SessionID)
		if spoolErr != nil {
			// Output history is diagnostic state; a corrupt or unavailable spool must never block process ownership.
			spool = nil
		}
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
	managedHostEnvironmentCleanup := len(managedOverrides) > 0
	defer func() {
		if managedHostEnvironmentCleanup {
			cleanupErr := supervisor.removeHostEnvironment(checkoutRoot)
			if cleanupErr != nil {
				startErr = managedHostEnvironmentRollbackError(startErr, cleanupErr)
			}
		}
	}()
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
	command.Env = withDevelopmentEnvironment(supervisor.environment, requestedManagedEnvironmentOverrides(request.EnvironmentOverrides, managedOverrides), managedLaunchPath)
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
	traceCleanup := true
	defer func() {
		if traceCleanup {
			_ = removeProjectLaunchTrace(checkoutRoot)
		}
	}()
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
	managedHostEnvironmentCleanup = false

	var brokerAttachment OutputBrokerAttachment
	var brokerPeer *OutputBrokerPeer
	fallbackBrokerToDirect := func() {
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
			brokerAttachment = nil
		}
		brokerPeer = nil
		if relay.spool == nil && supervisor.outputSpoolDirectory != "" {
			if fallbackSpool, fallbackErr := openOutputSpool(supervisor.outputSpoolDirectory, request.ProjectID, request.SessionID); fallbackErr == nil {
				relay.spool = fallbackSpool
			}
		}
	}
	if brokerLauncher != nil {
		brokerAttachment, err = brokerLauncher.Launch(ctx, OutputBrokerLaunchSpec{
			ProjectID:       request.ProjectID,
			SessionID:       request.SessionID,
			OutputDirectory: supervisor.outputSpoolDirectory,
			Stdout:          stdout,
			Stderr:          stderr,
		})
		if err != nil {
			fallbackBrokerToDirect()
		} else if brokerAttachment == nil {
			fallbackBrokerToDirect()
		} else {
			peer := brokerAttachment.Peer()
			if peerErr := peer.Validate(); peerErr != nil {
				fallbackBrokerToDirect()
			} else if peer.ProjectID != request.ProjectID || peer.SessionID != request.SessionID {
				fallbackBrokerToDirect()
			} else {
				brokerPeer = &peer
			}
		}
	}

	var pipeReaders sync.WaitGroup
	if brokerAttachment == nil {
		pipeReaders.Add(2)
		go readOutputStream(stdout, outputStreamStdout, relay, &pipeReaders)
		go readOutputStream(stderr, outputStreamStderr, relay, &pipeReaders)
	}
	// The parent copies must close immediately so EOF represents the complete child tree, not Harbor itself.
	if err := errors.Join(stdoutChild.Close(), stderrChild.Close()); err != nil {
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
		}
		cleanupErr := terminateStartedCommand(command, platform)
		managedHostEnvironmentCleanup = len(managedOverrides) > 0 && cleanupErr == nil
		traceCleanup = cleanupErr == nil
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
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
		}
		cleanupErr := terminateStartedCommand(command, platform)
		managedHostEnvironmentCleanup = len(managedOverrides) > 0 && cleanupErr == nil
		traceCleanup = cleanupErr == nil
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
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
		}
		cleanupErr := terminateStartedCommand(command, platform)
		managedHostEnvironmentCleanup = len(managedOverrides) > 0 && cleanupErr == nil
		traceCleanup = cleanupErr == nil
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
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
		}
		cleanupErr := terminateStartedCommand(command, platform)
		managedHostEnvironmentCleanup = len(managedOverrides) > 0 && cleanupErr == nil
		traceCleanup = cleanupErr == nil
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
	handle = &Handle{
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
			OutputBroker: brokerPeer,
			StartedAt:    startedAt,
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
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		if brokerAttachment != nil {
			_ = brokerAttachment.Close()
		}
		cleanupErr := terminateStartedCommand(command, platform)
		managedHostEnvironmentCleanup = len(managedOverrides) > 0 && cleanupErr == nil
		traceCleanup = cleanupErr == nil
		if cleanupErr != nil {
			_ = stdout.Close()
			_ = stderr.Close()
		}
		pipeReaders.Wait()
		relay.finish()
		platform.close()
		return nil, acceptedProcessStartError("cancel accepted forj process", ErrClosed, cleanupErr)
	}
	supervisor.projects[request.ProjectID] = process
	supervisor.sessions[request.SessionID] = process
	supervisor.checkouts[checkoutRoot] = process
	supervisor.releaseLaunchLocked(reservation)
	promoted = true
	supervisor.mu.Unlock()
	traceCleanup = false
	if brokerAttachment != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		pipeReaders.Add(1)
		// The request context ends with the Start RPC; broker observation must instead last until the
		// inherited child pipes reach EOF so a normal client disconnect cannot retire live output.
		go readOutputBrokerAttachment(context.Background(), brokerAttachment, relay, &pipeReaders, func() {
			alive, observeErr := process.observeTree()
			if observeErr == nil && alive {
				process.requestStop()
			}
		})
	}
	managedLaunchCleanup = false
	go supervisor.wait(process)
	return handle, nil
}

// managedHostEnvironmentRollbackError retains the launch failure while marking failed cleanup as unsafe ownership release.
func managedHostEnvironmentRollbackError(startErr error, cleanupErr error) error {
	return fmt.Errorf("%w: %w", ErrCleanupUncertain, errors.Join(startErr, fmt.Errorf("remove Harbor managed host environment: %w", cleanupErr)))
}

// resetProcess retains shutdown authority until the reset command has been reaped.
type resetProcess struct {
	command      *exec.Cmd
	platform     *platformProcess
	checkoutRoot string
	done         chan struct{}
	waitDone     chan struct{}

	// The fields below are protected by Supervisor.mu. A reset remains fenced until
	// its root has been reaped and one settlement attempt has proved its scope gone.
	waitErr        error
	rootReaped     bool
	settling       bool
	settlementDone chan struct{}
	settlementErr  error
	finished       bool
}

// Down withdraws any runtime left by a prior GoForj invocation before Harbor starts a new development session.
func (supervisor *Supervisor) Down(ctx context.Context, request DownRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(request.CheckoutRoot) == "" {
		return fmt.Errorf("%w: checkout root must not be empty", ErrInvalidRequest)
	}
	checkoutRoot, err := canonicalDirectory(request.CheckoutRoot)
	if err != nil {
		return fmt.Errorf("canonicalize checkout root: %w", err)
	}
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return ErrClosed
	}
	if reset := supervisor.resetCheckouts[checkoutRoot]; reset != nil {
		supervisor.mu.Unlock()
		return supervisor.settleReset(ctx, reset)
	}
	if _, launching := supervisor.launchCheckouts[checkoutRoot]; launching {
		supervisor.mu.Unlock()
		return fmt.Errorf("%w: checkout %s", ErrProjectRunning, checkoutRoot)
	}
	for _, process := range supervisor.projects {
		if process.handle.info.CheckoutRoot == checkoutRoot {
			supervisor.mu.Unlock()
			return fmt.Errorf("%w: checkout %s", ErrProjectRunning, checkoutRoot)
		}
	}
	executable, err := supervisor.acceptedGoForjExecutable(request.GoForjExecutable)
	if err != nil {
		supervisor.mu.Unlock()
		return err
	}
	command := exec.Command(executable, "down")
	command.Dir = checkoutRoot
	// Reset must never inherit the one-use launch credential of a different session.
	command.Env = withDevelopmentEnvironment(supervisor.environment, EnvironmentOverrides{})
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	platform, err := preparePlatformProcess(command)
	if err != nil {
		supervisor.mu.Unlock()
		return fmt.Errorf("prepare forj reset ownership: %w", err)
	}
	reset := &resetProcess{
		command:      command,
		platform:     platform,
		checkoutRoot: checkoutRoot,
		done:         make(chan struct{}),
		waitDone:     make(chan struct{}),
	}
	if err := command.Start(); err != nil {
		supervisor.mu.Unlock()
		platform.close()
		return fmt.Errorf("start forj down: %w", err)
	}
	// Admit the fence before attaching or resuming. A partial Windows attachment can otherwise
	// leave a launched process outside both the Job Object and the checkout admission boundary.
	supervisor.resets[reset] = struct{}{}
	supervisor.resetCheckouts[checkoutRoot] = reset
	// Exactly one owner reaps every accepted reset, including reset commands whose caller times out.
	go supervisor.waitReset(reset)
	if _, err := platform.attach(command.Process); err != nil {
		supervisor.mu.Unlock()
		return errors.Join(fmt.Errorf("%w: attach forj reset ownership: %v", ErrCleanupUncertain, err), platform.force(command.Process.Pid))
	}
	if err := platform.resume(command.Process); err != nil {
		supervisor.mu.Unlock()
		return errors.Join(fmt.Errorf("%w: resume forj reset: %v", ErrCleanupUncertain, err), platform.force(command.Process.Pid))
	}
	supervisor.mu.Unlock()
	return supervisor.settleReset(ctx, reset)
}

// waitReset is the sole Cmd.Wait owner for an accepted reset command.
func (supervisor *Supervisor) waitReset(reset *resetProcess) {
	err := reset.command.Wait()
	supervisor.mu.Lock()
	reset.waitErr = err
	reset.rootReaped = true
	close(reset.waitDone)
	supervisor.mu.Unlock()
	// The initiating caller may reach its deadline just before the operating system reports
	// the reset root as waitable. The sole waiter performs one bounded settlement attempt so
	// ordinary completion does not require a second user action; uncertainty remains fenced.
	settlementContext, cancelSettlement := context.WithTimeout(context.Background(), forceSettlementPeriod)
	defer cancelSettlement()
	_ = supervisor.settleReset(settlementContext, reset)
}

// settleReset joins the root reaper and performs at most one serialized, caller-bounded scope settlement attempt.
func (supervisor *Supervisor) settleReset(ctx context.Context, reset *resetProcess) error {
	select {
	case <-reset.waitDone:
	case <-ctx.Done():
		return errors.Join(ErrCleanupUncertain, ctx.Err(), reset.platform.force(reset.command.Process.Pid))
	}

	supervisor.mu.Lock()
	if reset.finished {
		waitErr := reset.waitErr
		supervisor.mu.Unlock()
		if waitErr != nil {
			return fmt.Errorf("forj down: %w", waitErr)
		}
		return nil
	}
	if reset.settling {
		done := reset.settlementDone
		supervisor.mu.Unlock()
		select {
		case <-done:
			supervisor.mu.Lock()
			err := reset.settlementErr
			waitErr := reset.waitErr
			finished := reset.finished
			supervisor.mu.Unlock()
			if err != nil {
				return err
			}
			if finished && waitErr != nil {
				return fmt.Errorf("forj down: %w", waitErr)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	reset.settling = true
	reset.settlementDone = make(chan struct{})
	supervisor.mu.Unlock()

	forceErr := reset.platform.force(reset.command.Process.Pid)
	settled, observeErr := waitForPlatformSettlementContext(ctx, reset.platform, reset.command.Process.Pid)
	settlementErr := errors.Join(forceErr, observeErr)
	if !settled {
		if settlementErr == nil {
			settlementErr = fmt.Errorf("%w: forj reset process tree did not settle", ErrCleanupUncertain)
		} else {
			settlementErr = fmt.Errorf("%w: forj reset process tree did not settle: %w", ErrCleanupUncertain, settlementErr)
		}
	}

	if !settled {
		supervisor.mu.Lock()
		reset.settling = false
		reset.settlementErr = settlementErr
		close(reset.settlementDone)
		supervisor.mu.Unlock()
		return settlementErr
	}
	supervisor.mu.Lock()
	waitErr := reset.waitErr
	supervisor.mu.Unlock()
	if waitErr != nil {
		supervisor.finishReset(reset)
		supervisor.mu.Lock()
		reset.settling = false
		reset.settlementErr = nil
		close(reset.settlementDone)
		supervisor.mu.Unlock()
		return fmt.Errorf("forj down: %w", waitErr)
	}
	if err := removeProjectLaunchTrace(reset.checkoutRoot); err != nil {
		supervisor.mu.Lock()
		reset.settling = false
		reset.settlementErr = err
		close(reset.settlementDone)
		supervisor.mu.Unlock()
		return err
	}
	supervisor.finishReset(reset)
	supervisor.mu.Lock()
	reset.settling = false
	reset.settlementErr = nil
	close(reset.settlementDone)
	supervisor.mu.Unlock()
	return nil
}

// finishReset releases one reset command only after its operating-system process has been reaped.
func (supervisor *Supervisor) finishReset(reset *resetProcess) {
	supervisor.mu.Lock()
	if reset.finished || !reset.rootReaped {
		supervisor.mu.Unlock()
		return
	}
	reset.finished = true
	delete(supervisor.resets, reset)
	if supervisor.resetCheckouts[reset.checkoutRoot] == reset {
		delete(supervisor.resetCheckouts, reset.checkoutRoot)
	}
	supervisor.mu.Unlock()
	reset.platform.close()
	close(reset.done)
}

// waitForPlatformSettlement proves the entire owned process scope is gone after the reset root exits.
func waitForPlatformSettlement(platform *platformProcess, pid int) (bool, error) {
	return waitForPlatformSettlementContext(context.Background(), platform, pid)
}

// waitForPlatformSettlementContext proves an owned process scope is absent without exceeding the caller's cleanup budget.
func waitForPlatformSettlementContext(ctx context.Context, platform *platformProcess, pid int) (bool, error) {
	deadline := time.Now().Add(forceSettlementPeriod)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		alive, err := platform.treeAlive(pid)
		if err != nil || !alive {
			return !alive, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		timer := time.NewTimer(forceSettlementPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
	}
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
	launches := make([]*launchReservation, 0, len(supervisor.launches))
	for reservation := range supervisor.launches {
		launches = append(launches, reservation)
	}
	resets := make([]*resetProcess, 0, len(supervisor.resets))
	for reset := range supervisor.resets {
		resets = append(resets, reset)
	}
	adoptedOutputs := make([]*adoptedOutput, 0, len(supervisor.adoptedOutputs))
	for _, adopted := range supervisor.adoptedOutputs {
		adoptedOutputs = append(adoptedOutputs, adopted)
	}
	supervisor.mu.Unlock()

	deadlineReached := make(chan struct{})
	defer close(deadlineReached)
	var closeErr error
	// Reset commands have no durable session row to recover after shutdown. Close requests termination and
	// settles within the caller's budget; the sole waiter retains ownership if the root becomes waitable later.
	for _, reset := range resets {
		if reset == nil || reset.command.Process == nil {
			continue
		}
		if err := reset.platform.force(reset.command.Process.Pid); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("terminate forj reset: %w", err))
		}
	}
	for _, reservation := range launches {
		select {
		case <-reservation.done:
		case <-ctx.Done():
			closeErr = errors.Join(closeErr, ctx.Err())
		}
	}
	go func() {
		select {
		case <-ctx.Done():
			for _, process := range processes {
				process.forceStop()
			}
		case <-deadlineReached:
		}
	}()
	for _, process := range processes {
		select {
		case <-process.stopComplete:
			closeErr = errors.Join(closeErr, process.stopErr)
		case <-ctx.Done():
			closeErr = errors.Join(closeErr, ctx.Err(), process.forceStop())
		}
	}
	for _, reset := range resets {
		if reset == nil {
			continue
		}
		if err := supervisor.settleReset(ctx, reset); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("settle forj reset: %w", err))
		}
	}
	for _, adopted := range adoptedOutputs {
		if adopted == nil {
			continue
		}
		if adopted.attachment != nil {
			closeErr = errors.Join(closeErr, adopted.attachment.Close())
		}
		select {
		case <-adopted.done:
		case <-ctx.Done():
			closeErr = errors.Join(closeErr, ctx.Err())
		}
	}
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
	treeSettlementErr := process.settleTree(stopRequested)
	cleanupErr := treeSettlementErr
	if cleanupErr != nil {
		_ = process.stdout.Close()
		_ = process.stderr.Close()
	}
	process.pipeReaders.Wait()
	_ = process.stdout.Close()
	_ = process.stderr.Close()
	process.relay.finish()
	process.platform.close()

	process.handle.mu.RLock()
	info := process.handle.info
	process.handle.mu.RUnlock()
	supervisor.mu.Lock()
	serviceLogs := supervisor.detachServiceLogsLocked(info.ProjectID, info.SessionID)
	supervisor.mu.Unlock()
	for _, stream := range serviceLogs {
		streamErr := stream.stop()
		cleanupErr = errors.Join(cleanupErr, streamErr)
		if streamErr == nil {
			supervisor.removeSettledServiceLogStream(stream.key, stream)
		}
	}
	if treeSettlementErr == nil {
		cleanupErr = errors.Join(cleanupErr, supervisor.removeHostEnvironment(info.CheckoutRoot))
		if stopRequested {
			cleanupErr = errors.Join(cleanupErr, removeProjectLaunchTrace(info.CheckoutRoot))
		}
	}
	cleanupErr = errors.Join(cleanupErr, removeManagedLaunchContext(process.managedLaunchPath))

	supervisor.mu.Lock()
	if supervisor.projects[info.ProjectID] == process {
		delete(supervisor.projects, info.ProjectID)
	}
	if supervisor.sessions[info.SessionID] == process {
		delete(supervisor.sessions, info.SessionID)
	}
	if supervisor.checkouts[info.CheckoutRoot] == process {
		delete(supervisor.checkouts, info.CheckoutRoot)
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

// requestedManagedEnvironmentOverrides keeps the child environment limited to caller-requested keys while using the persisted normalized values.
func requestedManagedEnvironmentOverrides(requested EnvironmentOverrides, managed EnvironmentOverrides) EnvironmentOverrides {
	result := make(EnvironmentOverrides, len(requested))
	for name := range requested {
		if value, present := managed[name]; present {
			result[name] = value
		}
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

// withDevelopmentEnvironment replaces ambient managed values with Harbor's validated assignments and selects non-interactive output.
func withDevelopmentEnvironment(environment []string, overrides EnvironmentOverrides, managedLaunchPath ...string) []string {
	names := sortedEnvironmentOverrideNames(overrides)
	replacedNames := append(append([]string(nil), names...), developmentLaunchIsolationNames...)
	assignments := []environmentAssignment{{name: developmentPlainEnvName, value: "1"}}
	for _, name := range names {
		assignments = append(assignments, environmentAssignment{
			name:  name,
			value: overrides[name],
		})
	}
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
