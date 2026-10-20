package projectprocess

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// ErrSessionAdoptionUnsupported leaves sessions without a durable ownership boundary on the existing recovery path.
var ErrSessionAdoptionUnsupported = errors.New("process session adoption is unsupported")

// SettleSession includes a retained native job when retiring an interrupted durable lifecycle.
func (supervisor *Supervisor) SettleSession(ctx context.Context, session domain.ProjectSession) (PriorProcessSettlement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.Validate(); err != nil {
		return PriorProcessSettlement{}, err
	}
	if session.Owner != domain.SessionOwnerHarbor || session.Process == nil {
		return PriorProcessSettlement{}, ErrSessionAdoptionUnsupported
	}
	if result, supported, err := settleRetainedSessionScope(ctx, session, supervisor.outputSpoolDirectory); supported || err != nil {
		return result, err
	}
	return supervisor.SettlePriorProcess(ctx, *session.Process)
}

// AdoptSession restores one exact process scope after independently authenticating its surviving output broker.
func (supervisor *Supervisor) AdoptSession(ctx context.Context, session domain.ProjectSession, checkoutRoot string) (*Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	if session.Owner != domain.SessionOwnerHarbor || session.Process == nil || session.OutputBroker == nil ||
		(session.State != domain.SessionAwaitingAttach && session.State != domain.SessionAttached && session.State != domain.SessionStopping) {
		return nil, ErrSessionAdoptionUnsupported
	}
	canonical, err := canonicalDirectory(checkoutRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve session adoption checkout: %w", err)
	}
	if canonical != checkoutRoot {
		return nil, errors.New("session adoption requires its existing canonical checkout")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reservation, err := supervisor.reserveLaunch(StartRequest{ProjectID: session.ProjectID, SessionID: session.ID}, checkoutRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		supervisor.mu.Lock()
		supervisor.releaseLaunchLocked(reservation)
		supervisor.mu.Unlock()
	}()
	platform, child, err := adoptPlatformProcess(ctx, session, supervisor.outputSpoolDirectory)
	if err != nil {
		return nil, err
	}
	admitted := false
	defer func() {
		if !admitted {
			platform.close()
			_ = child.Release()
		}
	}()
	if err := supervisor.AdoptOutputBroker(ctx, session.ProjectID, session.ID, *session.OutputBroker); err != nil {
		return nil, err
	}
	key := outputBrokerKey{projectID: session.ProjectID, sessionID: session.ID}
	supervisor.mu.Lock()
	output := supervisor.adoptedOutputs[key]
	if supervisor.closed || output == nil {
		supervisor.mu.Unlock()
		return nil, errors.New("session output ended before process adoption completed")
	}
	if alive, err := platform.treeAlive(child.Pid); err != nil || !alive {
		supervisor.mu.Unlock()
		return nil, errors.Join(errors.New("session scope changed during adoption"), err)
	}
	broker := output.attachment.Peer()
	handle := &Handle{info: Info{
		ProjectID: session.ProjectID, SessionID: session.ID, CheckoutRoot: checkoutRoot,
		Arguments: []string{session.Process.ExecutableIdentity, "dev"},
		Evidence: Evidence{PID: session.Process.PID, BirthToken: session.Process.BirthToken,
			ExecutableIdentity: session.Process.ExecutableIdentity, ArgumentsSHA256: session.Process.ArgumentDigest},
		OutputBroker: &broker, StartedAt: session.CreatedAt,
	}, done: make(chan struct{})}
	readers := &sync.WaitGroup{}
	readers.Add(1)
	go func() { defer readers.Done(); <-output.done }()
	process := &managedProcess{
		adopted: true, detachDone: make(chan struct{}), brokerAttachment: output.attachment,
		command: &exec.Cmd{Process: child}, platform: platform, relay: output.relay, pipeReaders: readers,
		handle: handle, gracePeriod: supervisor.gracePeriod, acceptingStop: true,
		forced: make(chan struct{}), signalsDone: make(chan struct{}), stopComplete: make(chan struct{}),
	}
	supervisor.projects[session.ProjectID] = process
	supervisor.sessions[session.ID] = process
	supervisor.checkouts[checkoutRoot] = process
	supervisor.mu.Unlock()
	admitted = true
	go supervisor.waitAdoptedSession(process)
	return handle, nil
}

// waitAdoptedSession observes the native scope without trying to reap a process created by the previous daemon.
func (supervisor *Supervisor) waitAdoptedSession(process *managedProcess) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-process.detachDone:
			return
		case <-ticker.C:
			alive, err := process.observeTree()
			rootAlive := false
			if err == nil && alive {
				rootAlive, err = process.platform.rootAlive(process.command.Process.Pid)
			}
			if err != nil || !alive || !rootAlive {
				if err != nil {
					process.closeOutputReaders()
				}
				supervisor.finishProcess(process, err, -1)
				_ = process.command.Process.Release()
				return
			}
		}
	}
}

// DetachSession relinquishes only daemon-local handles after proving a live exact scope and authenticated broker.
func (supervisor *Supervisor) DetachSession(ctx context.Context, projectID domain.ProjectID, sessionID domain.SessionID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	supervisor.mu.Lock()
	process := supervisor.projects[projectID]
	if process == nil || supervisor.sessions[sessionID] != process || !process.acceptingStop || process.stopRequested.Load() || process.brokerAttachment == nil {
		supervisor.mu.Unlock()
		return ErrSessionAdoptionUnsupported
	}
	if err := platformProcessCanDetach(process.platform, process.handle.Info()); err != nil {
		supervisor.mu.Unlock()
		return err
	}
	if alive, err := process.observeTree(); err != nil || !alive {
		supervisor.mu.Unlock()
		return errors.Join(errors.New("session scope is not live at detach"), err)
	}
	if alive, err := process.platform.rootAlive(process.command.Process.Pid); err != nil || !alive {
		supervisor.mu.Unlock()
		return errors.Join(errors.New("session leader is not live at detach"), err)
	}
	// Detachment is a shutdown boundary; this owner must never admit a duplicate after relinquishing its maps.
	supervisor.closed = true
	process.acceptingStop = false
	process.detached.Store(true)
	close(process.detachDone)
	delete(supervisor.projects, projectID)
	delete(supervisor.sessions, sessionID)
	if supervisor.checkouts[process.handle.info.CheckoutRoot] == process {
		delete(supervisor.checkouts, process.handle.info.CheckoutRoot)
	}
	supervisor.mu.Unlock()
	process.closeSignals()
	process.closeOutputReaders()
	process.pipeReaders.Wait()
	process.relay.finish()
	process.platform.close()
	if process.adopted {
		_ = process.command.Process.Release()
	}
	return nil
}

// closeOutputReaders handles the optional pipes of adopted sessions without closing the broker's inherited copies.
func (process *managedProcess) closeOutputReaders() {
	if process.brokerAttachment != nil {
		_ = process.brokerAttachment.Close()
	}
	if process.stdout != nil {
		_ = process.stdout.Close()
	}
	if process.stderr != nil {
		_ = process.stderr.Close()
	}
}
