package projectprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

const (
	serviceLogTailLines           = 200
	serviceLogStopPeriod          = 2 * time.Second
	serviceLogRetryPeriod         = time.Second
	maximumProjectLogFollowers    = 64
	maximumServiceLogProblemBytes = 4096
	maximumServiceLogCodeBytes    = 64
)

// ServiceLogProblem describes a typed, bounded runtime or stream failure.
type ServiceLogProblem struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Validate reports whether the problem is safe to expose through local control.
func (problem ServiceLogProblem) Validate() error {
	if err := validateServiceLogProblemCode(problem.Code); err != nil {
		return err
	}
	if problem.Message == "" || strings.TrimSpace(problem.Message) != problem.Message {
		return errors.New("service log problem message must be non-empty canonical text")
	}
	if len(problem.Message) > maximumServiceLogProblemBytes || !utf8.ValidString(problem.Message) {
		return fmt.Errorf("service log problem message must be valid UTF-8 within %d bytes", maximumServiceLogProblemBytes)
	}
	for _, character := range problem.Message {
		if unicode.IsControl(character) {
			return errors.New("service log problem message must not contain control characters")
		}
	}
	return nil
}

// ServiceLogSelection is one bounded cursor view of the current logical service follower.
type ServiceLogSelection struct {
	Supported bool
	Available bool
	Problem   *ServiceLogProblem
	Output    OutputChunk
}

// serviceLogKey binds one follower to an exact supervised session and logical service.
type serviceLogKey struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
	serviceID domain.ServiceID
}

// serviceLogStream owns cancellable Engine response bodies and one bounded merged transcript.
type serviceLogStream struct {
	key        serviceLogKey
	checkout   string
	ctx        context.Context
	cancel     context.CancelFunc
	transcript *outputTranscript
	ready      chan struct{}
	done       chan struct{}
	readyOnce  sync.Once
	stopOnce   sync.Once
	mu         sync.Mutex
	follower   *serviceLogFollowerLease
	supported  bool
	available  bool
	retired    bool
	problem    *ServiceLogProblem
	lastAccess time.Time
	closeErr   error
}

// serviceLogFollowerLease gives concurrent run and stop paths one idempotent response-body owner.
type serviceLogFollowerLease struct {
	follower  containerruntime.LogFollower
	closeOnce sync.Once
	mu        sync.Mutex
	closeErr  error
}

// close interrupts and joins one runtime follower exactly once.
func (lease *serviceLogFollowerLease) close() error {
	lease.closeOnce.Do(func() {
		err := lease.follower.Close()
		lease.mu.Lock()
		lease.closeErr = err
		lease.mu.Unlock()
	})
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.closeErr
}

// ReadServiceLogs returns one immediate bounded view, opening Engine followers on first use.
func (supervisor *Supervisor) ReadServiceLogs(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
	cursor uint64,
) (ServiceLogSelection, error) {
	return supervisor.readServiceLogs(ctx, projectID, sessionID, serviceID, cursor, false)
}

// WaitServiceLogs holds one cursor until output advances, the stream ends, or the caller cancels.
func (supervisor *Supervisor) WaitServiceLogs(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
	cursor uint64,
) (ServiceLogSelection, error) {
	return supervisor.readServiceLogs(ctx, projectID, sessionID, serviceID, cursor, true)
}

// readServiceLogs shares one runtime follower independently from any individual desktop request lifetime.
func (supervisor *Supervisor) readServiceLogs(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
	cursor uint64,
	wait bool,
) (ServiceLogSelection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ServiceLogSelection{}, err
	}
	if err := projectID.Validate(); err != nil {
		return ServiceLogSelection{}, err
	}
	if err := sessionID.Validate(); err != nil {
		return ServiceLogSelection{}, err
	}
	if err := serviceID.Validate(); err != nil {
		return ServiceLogSelection{}, err
	}
	if cursor > uint64(domain.MaximumSequence) {
		return ServiceLogSelection{}, fmt.Errorf("service log cursor exceeds %d", domain.MaximumSequence)
	}
	key := serviceLogKey{projectID: projectID, sessionID: sessionID, serviceID: serviceID}
	stream, err := supervisor.currentServiceLogStream(key)
	if err != nil {
		if errors.Is(err, ErrNotRunning) {
			return ServiceLogSelection{Supported: true}, nil
		}
		return ServiceLogSelection{}, err
	}
	stream.touch()
	select {
	case <-stream.ready:
	case <-ctx.Done():
		return ServiceLogSelection{}, ctx.Err()
	}
	selection := stream.selection(cursor)
	if !wait || !selection.Supported || !selection.Available || selection.Problem != nil ||
		selection.Output.Text != "" || selection.Output.Reset || selection.Output.Truncated || selection.Output.HasMore {
		if wait && selection.Supported && !selection.Available && selection.Problem == nil {
			return stream.waitSelection(ctx, cursor)
		}
		return selection, nil
	}
	return stream.waitSelection(ctx, cursor)
}

// currentServiceLogStream returns or creates the sole follower for an exact current selection.
func (supervisor *Supervisor) currentServiceLogStream(key serviceLogKey) (*serviceLogStream, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrClosed
	}
	projectProcess, projectExists := supervisor.projects[key.projectID]
	sessionProcess, sessionExists := supervisor.sessions[key.sessionID]
	if !projectExists || !sessionExists || projectProcess != sessionProcess ||
		!projectProcess.acceptingStop || projectProcess.stopRequested.Load() {
		return nil, ErrNotRunning
	}
	if stream := supervisor.serviceLogs[key]; stream != nil {
		stream.mu.Lock()
		retired := stream.retired
		stream.mu.Unlock()
		if retired {
			return nil, fmt.Errorf("%w: retired service log follower has not settled", ErrCleanupUncertain)
		}
		return stream, nil
	}
	projectFollowers := 0
	for existing := range supervisor.serviceLogs {
		if existing.projectID == key.projectID && existing.sessionID == key.sessionID {
			projectFollowers++
		}
	}
	if projectFollowers >= maximumProjectLogFollowers {
		return nil, fmt.Errorf("project session exceeds %d service log followers", maximumProjectLogFollowers)
	}
	streamContext, cancel := context.WithCancel(context.Background())
	stream := &serviceLogStream{
		key:        key,
		checkout:   projectProcess.command.Dir,
		ctx:        streamContext,
		cancel:     cancel,
		transcript: newOutputTranscript(outputTranscriptCapacityBytes),
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
		lastAccess: time.Now(),
	}
	supervisor.serviceLogs[key] = stream
	go stream.run(supervisor.containerRuntime)
	go supervisor.monitorServiceLogIdle(key, stream)
	return stream, nil
}

// run preserves one transcript cursor while retrying transient Engine selection and stream failures.
func (stream *serviceLogStream) run(runtime containerruntime.Runtime) {
	defer func() {
		stream.transcript.close()
		stream.signalReady()
		close(stream.done)
	}()
	tail := serviceLogTailLines
	for stream.ctx.Err() == nil {
		follower, err := runtime.OpenServiceLogs(
			stream.ctx,
			stream.checkout,
			string(stream.key.serviceID),
			tail,
		)
		if err != nil {
			stream.publishProblem(false, "runtime_unavailable", err, "The host container runtime could not provide service logs.")
			stream.signalReady()
			if !stream.waitRetry() {
				return
			}
			continue
		}
		lease := &serviceLogFollowerLease{follower: follower}
		if !stream.installFollower(lease) {
			stream.recordCloseError(lease.close())
			return
		}
		// Reopened immutable IDs follow only new bytes; recreated IDs still receive a bounded tail inside the runtime adapter.
		tail = 0
		stream.signalReady()
		copyErr := follower.CopyTo(serviceLogTranscriptWriter{transcript: stream.transcript})
		closeErr := lease.close()
		stream.releaseFollower(lease, closeErr)
		if stream.ctx.Err() != nil {
			return
		}
		cause := errors.Join(copyErr, closeErr)
		if cause == nil {
			cause = errors.New("container log follower ended without cancellation")
		}
		stream.publishProblem(true, "stream_failed", cause, "The container log stream ended unexpectedly.")
		if !stream.waitRetry() {
			return
		}
	}
}

// installFollower atomically rejects a follower that arrived after retirement and publishes a healthy retry generation.
func (stream *serviceLogStream) installFollower(lease *serviceLogFollowerLease) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.retired || stream.ctx.Err() != nil {
		return false
	}
	stream.follower = lease
	stream.supported = true
	stream.available = lease.follower.Available()
	stream.problem = nil
	return true
}

// releaseFollower removes only the generation that actually finished and retains cleanup failures for settlement.
func (stream *serviceLogStream) releaseFollower(lease *serviceLogFollowerLease, closeErr error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.follower == lease {
		stream.follower = nil
		stream.available = false
	}
	stream.closeErr = errors.Join(stream.closeErr, closeErr)
}

// publishProblem exposes a retryable typed state without replacing the transcript or its absolute cursor.
func (stream *serviceLogStream) publishProblem(
	supported bool,
	code string,
	cause error,
	fallback string,
) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.retired {
		return
	}
	stream.supported = supported
	stream.available = false
	stream.problem = &ServiceLogProblem{
		Code:      code,
		Message:   boundedServiceLogError(cause, fallback),
		Retryable: true,
	}
}

// waitRetry bounds retry frequency while allowing session stop and idle retirement to interrupt immediately.
func (stream *serviceLogStream) waitRetry() bool {
	timer := time.NewTimer(serviceLogRetryPeriod)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-stream.ctx.Done():
		return false
	}
}

// serviceLogTranscriptWriter serializes runtime-attributed bytes into the bounded cursor ring.
type serviceLogTranscriptWriter struct {
	transcript *outputTranscript
}

// Write appends bytes after the runtime has decoded framing and retained complete UTF-8 boundaries.
func (writer serviceLogTranscriptWriter) Write(output []byte) (int, error) {
	writer.transcript.append(output)
	return len(output), nil
}

// selection returns a stable public copy without exposing runtime response bodies.
func (stream *serviceLogStream) selection(cursor uint64) ServiceLogSelection {
	stream.mu.Lock()
	supported := stream.supported
	available := stream.available && !stream.retired
	if stream.follower != nil {
		available = stream.follower.follower.Available() && !stream.retired
	}
	problem := cloneServiceLogProblem(stream.problem)
	stream.mu.Unlock()
	selection := ServiceLogSelection{Supported: supported, Available: available, Problem: problem}
	if supported && available {
		selection.Output = stream.transcript.read(cursor)
	}
	return selection
}

// selectionWithOutput combines a waking chunk with terminal state after a held read.
func (stream *serviceLogStream) selectionWithOutput(output OutputChunk) ServiceLogSelection {
	stream.mu.Lock()
	available := stream.available && !stream.retired
	if stream.follower != nil {
		available = stream.follower.follower.Available() && !stream.retired
	}
	selection := ServiceLogSelection{
		Supported: stream.supported,
		Available: available,
		Problem:   cloneServiceLogProblem(stream.problem),
	}
	stream.mu.Unlock()
	if selection.Supported && selection.Available {
		output.Available = true
		selection.Output = output
	}
	return selection
}

// waitSelection wakes on either transcript progress or runtime availability change and joins the losing waiter.
func (stream *serviceLogStream) waitSelection(ctx context.Context, cursor uint64) (ServiceLogSelection, error) {
	stream.mu.Lock()
	lease := stream.follower
	stream.mu.Unlock()
	if lease == nil {
		return stream.selection(cursor), nil
	}
	waitContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type outputResult struct {
		output OutputChunk
		err    error
	}
	outputDone := make(chan outputResult, 1)
	stateDone := make(chan error, 1)
	available := lease.follower.Available()
	go func() {
		output, err := stream.transcript.wait(waitContext, cursor)
		outputDone <- outputResult{output: output, err: err}
	}()
	go func() {
		stateDone <- lease.follower.WaitStateChange(waitContext, available)
	}()
	var output outputResult
	stateWake := false
	select {
	case output = <-outputDone:
	case <-stateDone:
		stateWake = true
	case <-ctx.Done():
		cancel()
		<-outputDone
		<-stateDone
		return ServiceLogSelection{}, ctx.Err()
	}
	cancel()
	if stateWake {
		output = <-outputDone
	} else {
		<-stateDone
	}
	stream.touch()
	if output.err != nil && !errors.Is(output.err, context.Canceled) {
		return ServiceLogSelection{}, output.err
	}
	if !stateWake && output.err == nil {
		return stream.selectionWithOutput(output.output), nil
	}
	return stream.selection(cursor), nil
}

// touch renews the follower lease independently from a held request context.
func (stream *serviceLogStream) touch() {
	stream.mu.Lock()
	if !stream.retired {
		stream.lastAccess = time.Now()
	}
	stream.mu.Unlock()
}

// signalReady releases initial readers after runtime selection succeeds or fails.
func (stream *serviceLogStream) signalReady() {
	stream.readyOnce.Do(func() { close(stream.ready) })
}

// stop cancels Engine requests, closes every response body, and joins all readers.
func (stream *serviceLogStream) stop() error {
	stream.mu.Lock()
	stream.retired = true
	stream.available = false
	lease := stream.follower
	stream.mu.Unlock()
	stream.stopOnce.Do(func() {
		stream.cancel()
	})
	if lease != nil {
		stream.recordCloseError(lease.close())
	}
	select {
	case <-stream.done:
		stream.mu.Lock()
		err := stream.closeErr
		stream.mu.Unlock()
		return err
	case <-time.After(serviceLogStopPeriod):
		return fmt.Errorf("%w: container log follower did not stop within %s", ErrCleanupUncertain, serviceLogStopPeriod)
	}
}

// monitorServiceLogIdle retires followers after route changes, window exits, or abandoned held reads.
func (supervisor *Supervisor) monitorServiceLogIdle(key serviceLogKey, stream *serviceLogStream) {
	timer := time.NewTimer(supervisor.serviceLogIdle)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			stream.mu.Lock()
			idle := time.Since(stream.lastAccess)
			retired := stream.retired
			stream.mu.Unlock()
			if retired {
				return
			}
			if idle < supervisor.serviceLogIdle {
				timer.Reset(supervisor.serviceLogIdle - idle)
				continue
			}
			if supervisor.currentServiceLogStreamMatches(key, stream) {
				if err := stream.stop(); err == nil {
					supervisor.removeSettledServiceLogStream(key, stream)
				}
			}
			return
		}
	}
}

// currentServiceLogStreamMatches prevents a superseded idle monitor from retiring another stream.
func (supervisor *Supervisor) currentServiceLogStreamMatches(key serviceLogKey, stream *serviceLogStream) bool {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.serviceLogs[key] == stream
}

// removeSettledServiceLogStream forgets only a follower whose response bodies have been joined successfully.
func (supervisor *Supervisor) removeSettledServiceLogStream(key serviceLogKey, stream *serviceLogStream) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.serviceLogs[key] == stream {
		delete(supervisor.serviceLogs, key)
	}
}

// detachServiceLogsLocked selects all followers tied to an ending session without losing uncertain cleanup handles.
func (supervisor *Supervisor) detachServiceLogsLocked(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) []*serviceLogStream {
	streams := make([]*serviceLogStream, 0)
	for key, stream := range supervisor.serviceLogs {
		if key.projectID != projectID || key.sessionID != sessionID {
			continue
		}
		streams = append(streams, stream)
	}
	return streams
}

// recordCloseError retains cleanup uncertainty until process or daemon settlement reports it.
func (stream *serviceLogStream) recordCloseError(err error) {
	if err == nil {
		return
	}
	stream.mu.Lock()
	stream.closeErr = errors.Join(stream.closeErr, err)
	stream.mu.Unlock()
}

// boundedServiceLogError preserves actionable single-line Engine diagnostics within the wire budget.
func boundedServiceLogError(cause error, fallback string) string {
	if cause == nil {
		return fallback
	}
	message := strings.TrimSpace(string(bytes.ToValidUTF8([]byte(cause.Error()), []byte("\uFFFD"))))
	if message == "" || len(message) > maximumServiceLogProblemBytes {
		return fallback
	}
	for _, character := range message {
		if unicode.IsControl(character) {
			return fallback
		}
	}
	return message
}

// cloneServiceLogProblem prevents callers from mutating shared follower state.
func cloneServiceLogProblem(problem *ServiceLogProblem) *ServiceLogProblem {
	if problem == nil {
		return nil
	}
	clone := *problem
	return &clone
}

// validateServiceLogProblemCode keeps terminal classifications stable for UI branching.
func validateServiceLogProblemCode(code string) error {
	if code == "" || len(code) > maximumServiceLogCodeBytes {
		return fmt.Errorf("service log problem code must be between 1 and %d bytes", maximumServiceLogCodeBytes)
	}
	for index, character := range code {
		if (character >= 'a' && character <= 'z') || (index > 0 && character >= '0' && character <= '9') || (index > 0 && character == '_') {
			continue
		}
		return errors.New("service log problem code must use lowercase identifier characters")
	}
	return nil
}

var _ io.Writer = serviceLogTranscriptWriter{}
