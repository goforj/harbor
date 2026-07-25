package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/goforj/harbor/internal/helper"
)

// windowsNativeTransport binds one request to one elevated process and one kernel-authenticated pipe client.
type windowsNativeTransport struct {
	helperExecutable string
	inspectHelper    windowsHelperInspector
	newPipe          windowsPipeFactory
	launchElevated   windowsElevatedLauncher
}

// windowsHelperMetadata records every immutable installation fact checked on a retained file handle.
type windowsHelperMetadata struct {
	regular          bool
	reparse          bool
	linkCount        uint32
	finalPathMatches bool
	ownerTrusted     bool
	daclProtected    bool
	daclRestricted   bool
	signatureTrusted bool
}

// windowsHelperInspection retains the verified executable against replacement through process creation.
type windowsHelperInspection struct {
	metadata windowsHelperMetadata
	close    func() error
}

// windowsHelperInspector validates and retains the fixed installed executable without following its leaf entry.
type windowsHelperInspector func(string) (windowsHelperInspection, error)

// windowsPipeListener owns one random protected route created before native consent begins.
type windowsPipeListener interface {
	path() string
	accept() (windowsPipeConnection, error)
	close() error
}

// windowsPipeConnection exposes only the duplex exchange and kernel client PID.
type windowsPipeConnection interface {
	io.ReadWriteCloser
	clientProcessID() (uint32, error)
}

// windowsPipeFactory creates one owner-and-SYSTEM protected local message pipe.
type windowsPipeFactory func() (windowsPipeListener, error)

// windowsElevatedProcess retains the exact ShellExecuteEx process object through PID correlation and wait.
type windowsElevatedProcess interface {
	processID() (uint32, error)
	wait() (int, error)
	close() error
}

// windowsLaunchOutcome distinguishes a proved pre-child dismissal from every post-creation anomaly.
type windowsLaunchOutcome struct {
	process  windowsElevatedProcess
	created  bool
	declined bool
}

// windowsElevatedLauncher invokes only the fixed helper with one non-authoritative pipe route.
type windowsElevatedLauncher func(string, string) (windowsLaunchOutcome, error)

// windowsAcceptResult carries one listener result without blocking exact-process observation.
type windowsAcceptResult struct {
	connection windowsPipeConnection
	err        error
}

// windowsWaitResult carries the exact process exit observed from the retained native handle.
type windowsWaitResult struct {
	exitCode int
	err      error
}

// newWindowsNativeTransport fails fast when any private native boundary is absent.
func newWindowsNativeTransport(
	helperExecutable string,
	inspectHelper windowsHelperInspector,
	newPipe windowsPipeFactory,
	launchElevated windowsElevatedLauncher,
) *windowsNativeTransport {
	if helperExecutable == "" {
		panic("launcher Windows transport requires a fixed helper executable")
	}
	if inspectHelper == nil {
		panic("launcher Windows transport requires a helper inspector")
	}
	if newPipe == nil {
		panic("launcher Windows transport requires a pipe factory")
	}
	if launchElevated == nil {
		panic("launcher Windows transport requires an elevated launcher")
	}
	return &windowsNativeTransport{
		helperExecutable: helperExecutable,
		inspectHelper:    inspectHelper,
		newPipe:          newPipe,
		launchElevated:   launchElevated,
	}
}

// Invoke launches one elevated helper and treats every anomaly after process creation as indeterminate.
func (transport *windowsNativeTransport) Invoke(ctx context.Context, request io.Reader, response io.Writer) TransportResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || requiredInterfaceIsNil(request) || requiredInterfaceIsNil(response) {
		return TransportResult{State: TransportUnavailable}
	}

	inspection, err := transport.inspectHelper(transport.helperExecutable)
	if err != nil || inspection.close == nil || !validInstalledWindowsHelper(inspection.metadata) {
		closeWindowsHelperInspection(inspection)
		return TransportResult{State: TransportUnavailable}
	}
	listener, err := transport.newPipe()
	if err != nil || requiredInterfaceIsNil(listener) || listener.path() == "" || ctx.Err() != nil {
		closeWindowsPipeListener(listener)
		closeWindowsHelperInspection(inspection)
		return TransportResult{State: TransportUnavailable}
	}
	accepts := acceptWindowsPipe(listener)

	launch, launchErr := transport.launchElevated(transport.helperExecutable, listener.path())
	processCreated := launch.created || !requiredInterfaceIsNil(launch.process)
	if !processCreated {
		closeWindowsPendingAccept(listener, accepts)
		closeWindowsHelperInspection(inspection)
		if launch.declined && launchErr == nil {
			return TransportResult{State: TransportDeclined}
		}
		return TransportResult{State: TransportUnavailable}
	}
	if requiredInterfaceIsNil(launch.process) {
		closeWindowsPendingAccept(listener, accepts)
		closeWindowsHelperInspection(inspection)
		return indeterminateWindowsTransport(transportFailureWindowsMissingProcess)
	}

	failure := transportFailureNone
	if launchErr != nil || launch.declined {
		failure = transportFailureWindowsLaunchAmbiguous
	}
	if err := inspection.close(); err != nil {
		failure = transportFailureWindowsInspectionClose
	}
	waits := waitForWindowsProcess(launch.process)
	processID, err := launch.process.processID()
	if err != nil || processID == 0 {
		closeWindowsPendingAccept(listener, accepts)
		return finishIndeterminateWindowsProcess(launch.process, waits, nil, transportFailureWindowsProcessIdentity)
	}

	var accepted windowsAcceptResult
	select {
	case accepted = <-accepts:
	case waitResult := <-waits:
		closeWindowsPendingAccept(listener, accepts)
		failure := windowsChildInvocationFailure(waitResult.exitCode)
		if failure == transportFailureNone {
			failure = transportFailureWindowsProcessExitedBeforePipe
		}
		return finishIndeterminateWindowsProcess(launch.process, waits, &waitResult, failure)
	case <-ctx.Done():
		closeWindowsPendingAccept(listener, accepts)
		return finishIndeterminateWindowsProcess(launch.process, waits, nil, transportFailureWindowsContextBeforePipe)
	}
	if err := listener.close(); err != nil {
		failure = transportFailureWindowsListenerClose
	}
	if accepted.err != nil || requiredInterfaceIsNil(accepted.connection) {
		if !requiredInterfaceIsNil(accepted.connection) {
			_ = accepted.connection.Close()
		}
		return finishIndeterminateWindowsProcess(launch.process, waits, nil, transportFailureWindowsPipeAccept)
	}

	clientProcessID, err := accepted.connection.clientProcessID()
	if err != nil || clientProcessID == 0 || clientProcessID != processID {
		_ = accepted.connection.Close()
		return finishIndeterminateWindowsProcess(launch.process, waits, nil, transportFailureWindowsPipePeerIdentity)
	}

	stopCancellation := context.AfterFunc(ctx, func() {
		_ = accepted.connection.Close()
	})
	capturedResponse, exchangeErr, exchangeFailure := exchangeWindowsHelper(accepted.connection, request)
	connectionCloseErr := accepted.connection.Close()
	stopCancellation()
	waitResult := <-waits
	processCloseErr := launch.process.close()
	switch {
	case failure != transportFailureNone:
		return indeterminateWindowsTransport(failure)
	case exchangeErr != nil:
		return indeterminateWindowsTransport(exchangeFailure)
	case connectionCloseErr != nil:
		return indeterminateWindowsTransport(transportFailureWindowsConnectionClose)
	case waitResult.err != nil:
		return indeterminateWindowsTransport(transportFailureWindowsWait)
	case processCloseErr != nil:
		return indeterminateWindowsTransport(transportFailureWindowsProcessClose)
	case ctx.Err() != nil:
		return indeterminateWindowsTransport(transportFailureWindowsContextAfterExchange)
	}
	if failure := windowsChildInvocationFailure(waitResult.exitCode); failure != transportFailureNone {
		return indeterminateWindowsTransport(failure)
	}
	if waitResult.exitCode != ExitCodeSucceeded && waitResult.exitCode != ExitCodeHelperFailed {
		return indeterminateWindowsTransport(transportFailureWindowsExitCode)
	}
	body := capturedResponse.Bytes()
	if len(body) == 0 {
		return indeterminateWindowsTransport(transportFailureWindowsEmptyResponse)
	}
	written, err := response.Write(body)
	if err != nil || written != len(body) {
		return indeterminateWindowsTransport(transportFailureWindowsResponseWrite)
	}
	return TransportResult{State: TransportCompleted, ExitCode: waitResult.exitCode}
}

// windowsChildInvocationFailure converts only the helper's reviewed pre-dispatch statuses into safe stages.
func windowsChildInvocationFailure(exitCode int) transportFailureStage {
	switch exitCode {
	case helper.WindowsInvocationExitPipeConnection:
		return transportFailureWindowsChildPipeConnection
	case helper.WindowsInvocationExitMessageMode:
		return transportFailureWindowsChildMessageMode
	case helper.WindowsInvocationExitTokenIdentity:
		return transportFailureWindowsChildTokenIdentity
	case helper.WindowsInvocationExitPipeSecurity:
		return transportFailureWindowsChildPipeSecurity
	case helper.WindowsInvocationExitServerIdentity:
		return transportFailureWindowsChildServerIdentity
	case helper.WindowsInvocationExitRetainConnection:
		return transportFailureWindowsChildRetainConnection
	default:
		return transportFailureNone
	}
}

// validInstalledWindowsHelper accepts only a trusted, immutable, direct installation object.
func validInstalledWindowsHelper(metadata windowsHelperMetadata) bool {
	return metadata.regular && !metadata.reparse && metadata.linkCount == 1 && metadata.finalPathMatches &&
		metadata.ownerTrusted && metadata.daclProtected && metadata.daclRestricted && metadata.signatureTrusted
}

// acceptWindowsPipe observes one client without hiding an early process exit or cancellation.
func acceptWindowsPipe(listener windowsPipeListener) <-chan windowsAcceptResult {
	results := make(chan windowsAcceptResult, 1)
	go func() {
		connection, err := listener.accept()
		results <- windowsAcceptResult{connection: connection, err: err}
	}()
	return results
}

// waitForWindowsProcess begins exact-handle observation before waiting for the one expected pipe client.
func waitForWindowsProcess(process windowsElevatedProcess) <-chan windowsWaitResult {
	results := make(chan windowsWaitResult, 1)
	go func() {
		exitCode, err := process.wait()
		results <- windowsWaitResult{exitCode: exitCode, err: err}
	}()
	return results
}

// closeWindowsPendingAccept releases any connection that raced listener cancellation before exact wait.
func closeWindowsPendingAccept(listener windowsPipeListener, accepts <-chan windowsAcceptResult) {
	_ = listener.close()
	accepted := <-accepts
	if !requiredInterfaceIsNil(accepted.connection) {
		_ = accepted.connection.Close()
	}
}

// finishIndeterminateWindowsProcess waits and releases one created process after communication certainty is lost.
func finishIndeterminateWindowsProcess(
	process windowsElevatedProcess,
	waits <-chan windowsWaitResult,
	observed *windowsWaitResult,
	failure transportFailureStage,
) TransportResult {
	if observed == nil {
		<-waits
	}
	_ = process.close()
	return indeterminateWindowsTransport(failure)
}

// indeterminateWindowsTransport preserves one reviewed stage without exposing native error contents.
func indeterminateWindowsTransport(failure transportFailureStage) TransportResult {
	return TransportResult{State: TransportIndeterminate, failure: failure}
}

// exchangeWindowsHelper sends one bounded request message and captures one bounded response stream.
func exchangeWindowsHelper(connection windowsPipeConnection, request io.Reader) (*boundedResponseWriter, error, transportFailureStage) {
	requestBody, err := io.ReadAll(io.LimitReader(request, helper.MaxRequestBytes+1))
	if err != nil {
		return &boundedResponseWriter{}, fmt.Errorf("read Windows helper request: %w", err), transportFailureWindowsRequestRead
	}
	if len(requestBody) > helper.MaxRequestBytes {
		return &boundedResponseWriter{}, errors.New("Windows helper request exceeds the protocol bound"), transportFailureWindowsRequestRead
	}
	written, err := connection.Write(requestBody)
	if err != nil || written != len(requestBody) {
		return &boundedResponseWriter{}, errors.Join(err, io.ErrShortWrite), transportFailureWindowsRequestWrite
	}
	capturedResponse := &boundedResponseWriter{}
	if _, err := io.Copy(capturedResponse, io.LimitReader(connection, helper.MaxResponseBytes+1)); err != nil {
		return capturedResponse, fmt.Errorf("read Windows helper response: %w", err), transportFailureWindowsResponseRead
	}
	return capturedResponse, nil, transportFailureNone
}

// closeWindowsHelperInspection releases a retained pre-consent executable when no child exists.
func closeWindowsHelperInspection(inspection windowsHelperInspection) {
	if inspection.close != nil {
		_ = inspection.close()
	}
}

// closeWindowsPipeListener releases a pre-consent route when no child exists.
func closeWindowsPipeListener(listener windowsPipeListener) {
	if !requiredInterfaceIsNil(listener) {
		_ = listener.close()
	}
}
