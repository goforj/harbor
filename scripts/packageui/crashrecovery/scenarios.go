package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/projectapproval"
)

// checkOriginal proves isolation without issuing any lifecycle request to the GUI-owned project.
func (driver *recovery) checkOriginal(ctx context.Context, original projectEvidence, label string) error {
	current, err := driver.healthy(ctx, driver.original)
	driver.evidence.Observations[label] = current
	if err != nil {
		return err
	}
	return continuity(original, current)
}

// settledProject requires affirmative retirement of every process-owned public projection.
func settledProject(project domain.ProjectSnapshot, activity control.ProjectActivity) error {
	if project.State != domain.ProjectFailed || activity.ProjectID != project.ID || activity.Session != nil || len(project.Resources) != 0 {
		return errors.New("crash recovery has not retired the failed project's session and resources")
	}
	for _, app := range project.Apps {
		if app.Active || app.State != domain.EntityStopped {
			return errors.New("failed project retains an active App")
		}
	}
	for _, service := range project.Services {
		if service.State != domain.EntityStopped {
			return errors.New("failed project retains an active service")
		}
	}
	return nil
}

// withdrawnRoute requires Harbor's affirmative unknown-host response rather than accepting TLS or network failure.
func withdrawnRoute(ctx context.Context, host, ingress string) error {
	address, _, err := net.SplitHostPort(ingress)
	if err != nil || !validProjectAddress(address, "") {
		return errors.New("withdrawal probe requires a canonical loopback ingress endpoint")
	}
	status, body, err := probeHostResponse(ctx, "http://"+ingress+"/-/ready", host)
	if err != nil {
		return err
	}
	if status != 421 || body != "Harbor has no route for this host.\n" {
		return fmt.Errorf("public route was not withdrawn: HTTP %d", status)
	}
	return nil
}

// inactiveIdentity preserves durable project identity without requiring withdrawn public DNS.
func inactiveIdentity(before, after projectEvidence) error {
	if before.ID != after.ID || before.Path != after.Path || before.Host != after.Host || before.ProjectAddress != after.ProjectAddress || !validProjectAddress(after.ProjectAddress, before.Address) {
		return errors.New("inactive observation changed project identity or private assignment")
	}
	return nil
}

// settledObservation rejects a restarted or retained session in the final independently read observation.
func settledObservation(project projectEvidence) error {
	if project.State != domain.ProjectFailed || project.SessionID != "" || project.SessionState != "" || project.Generation != 0 {
		return errors.New("final crash settlement observation retains a session or is no longer Failed")
	}
	return nil
}

// settled observes safe failure without treating quarantine or an unavailable ingress as successful cleanup.
func (driver *recovery) settled(ctx context.Context, before projectEvidence, label string) error {
	return waitFor(ctx, 90*time.Second, func(attempt context.Context) (bool, error) {
		project, err := driver.project(attempt, before.ID)
		if err != nil {
			return false, err
		}
		activity, err := driver.client.ProjectActivity(attempt, control.ProjectActivityRequest{ProjectID: before.ID})
		if err != nil {
			return false, err
		}
		if err := settledProject(project, activity); err != nil {
			return false, err
		}
		after, err := driver.observe(attempt, before.ID, false)
		driver.evidence.Observations[label] = after
		if err != nil {
			return false, err
		}
		if err := inactiveIdentity(before, after); err != nil {
			return false, err
		}
		if err := settledObservation(after); err != nil {
			return false, err
		}
		if err := withdrawnRoute(attempt, after.Host, net.JoinHostPort(before.Address, "80")); err != nil {
			return false, err
		}
		after.RouteWithdrawn = true
		driver.evidence.Observations[label] = after
		return true, nil
	})
}

// retry explicitly starts a settled project without claiming its prior process survived the daemon crash.
func (driver *recovery) retry(ctx context.Context, before projectEvidence, label string) (projectEvidence, error) {
	driver.evidence.Stage = label
	if _, err := driver.start(ctx, before.ID); err != nil {
		return projectEvidence{}, err
	}
	after, err := driver.healthy(ctx, before.ID)
	driver.evidence.Observations[label] = after
	if err != nil {
		return after, err
	}
	if err := freshSession(before, after); err != nil {
		return after, err
	}
	if before.ID == driver.original {
		driver.originalCurrent = after
		driver.originalNeedsRetry = false
	}
	return after, nil
}

// completedStart requires a crash to preserve the historical success of an already completed start.
func (driver *recovery) completedStart(ctx context.Context, id domain.OperationID) error {
	snapshot, err := driver.client.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, operation := range snapshot.Operations {
		if operation.ID == id {
			if operation.State != domain.OperationSucceeded {
				return errors.New("daemon recovery rewrote an already successful start")
			}
			return nil
		}
	}
	return errors.New("completed peer start is absent from bounded operation history")
}

// scenarios proves released supervised-session settlement, explicit retries, and ordinary peer isolation.
func (driver *recovery) scenarios(ctx context.Context, original projectEvidence) error {
	driver.evidence.Stage = "peer_initial_start"
	readyOperation, err := driver.start(ctx, driver.peer)
	if err != nil {
		return err
	}
	peer, err := driver.healthy(ctx, driver.peer)
	driver.evidence.Observations["peer_ready_before_crash"] = peer
	if err != nil {
		return err
	}
	if err := distinctProjects(original, peer); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_before_ready_crash"); err != nil {
		return err
	}
	driver.originalNeedsRetry = true
	driver.evidence.Stage = "ready_daemon_crash"
	if err := driver.crash(ctx); err != nil {
		return fmt.Errorf("ready crash: %w", err)
	}
	driver.evidence.Stage = "ready_crash_settlement"
	if err := driver.settled(ctx, original, "original_settled_after_ready_crash"); err != nil {
		return err
	}
	if err := driver.settled(ctx, peer, "peer_settled_after_ready_crash"); err != nil {
		return err
	}
	if err := driver.completedStart(ctx, readyOperation.ID); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "ready crash settled both supervised sessions to Failed with no active session or routes; completed peer Start remained Succeeded")
	original, err = driver.retry(ctx, original, "original_retry_after_ready_crash")
	if err != nil {
		return err
	}
	peer, err = driver.retry(ctx, peer, "peer_retry_after_ready_crash")
	if err != nil {
		return err
	}
	if err := distinctProjects(original, peer); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_after_ready_crash_retries"); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "explicit retries restored both projects with fresh sessions, stable DNS and distinct project addresses, and trusted HTTPS")
	driver.evidence.Stage = "normal_peer_stop"
	if err := driver.stop(ctx); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_after_normal_peer_stop"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(driver.parent, "gate-enabled"), []byte(driver.marker), 0600); err != nil {
		return err
	}
	driver.evidence.Stage = "gated_peer_start"
	operation, err := driver.start(ctx, driver.peer)
	if err != nil {
		return err
	}
	var starting projectEvidence
	var proof gateProof
	var expectedSession domain.SessionID
	err = waitFor(ctx, 4*time.Minute, func(attempt context.Context) (bool, error) {
		var err error
		proof, err = readGateProof(filepath.Join(driver.parent, "entered.json"), driver.marker)
		if err != nil {
			return false, err
		}
		if err := probe(attempt, "http://"+proof.Address+"/", driver.marker); err != nil {
			return false, err
		}
		starting, err = driver.observe(attempt, driver.peer, false)
		if err != nil {
			return false, err
		}
		if starting.SessionID != "" {
			if expectedSession == "" {
				expectedSession = starting.SessionID
			}
			if expectedSession != starting.SessionID {
				return false, errors.New("gated start session changed while awaiting its process-backed boundary")
			}
		}
		snapshot, err := driver.client.Snapshot(attempt)
		if err != nil {
			return false, err
		}
		for _, current := range snapshot.Operations {
			if current.ID == operation.ID && current.ProjectID == driver.peer && current.IntentID == operation.IntentID && current.State == domain.OperationRunning && starting.State == domain.ProjectStarting && processBackedSession(starting) {
				driver.evidence.InterruptedOperation = &current
				return true, nil
			}
		}
		return false, errors.New("gate has not reached a process-backed Starting session with running operation")
	})
	driver.evidence.Observations["peer_starting_before_crash"] = starting
	if err != nil {
		return err
	}
	driver.evidence.Gate = &proof
	if err := inactiveIdentity(peer, starting); err != nil {
		return err
	}
	if starting.SessionID == peer.SessionID {
		return errors.New("second peer start reused the stopped session")
	}
	if err := distinctProjects(original, starting); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_before_starting_crash"); err != nil {
		return err
	}
	driver.originalNeedsRetry = true
	driver.evidence.Stage = "starting_daemon_crash"
	if err := driver.crash(ctx); err != nil {
		return fmt.Errorf("starting crash: %w", err)
	}
	driver.evidence.Stage = "starting_crash_settlement"
	err = waitFor(ctx, 90*time.Second, func(attempt context.Context) (bool, error) {
		snapshot, err := driver.client.Snapshot(attempt)
		if err != nil {
			return false, err
		}
		for _, current := range snapshot.Operations {
			if current.ID != operation.ID {
				continue
			}
			driver.evidence.InterruptedOperation = &current
			if err := interruptedStart(operation, current); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, errors.New("interrupted start operation is absent")
	})
	if err != nil {
		return err
	}
	if err := driver.settled(ctx, original, "original_settled_after_starting_crash"); err != nil {
		return err
	}
	if err := driver.settled(ctx, peer, "peer_settled_after_starting_crash"); err != nil {
		return err
	}
	driver.evidence.Stage = "gate_absence_before_release"
	if err := listenerAbsent(ctx, proof.Address); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "starting crash failed the exact operation retryably, retired both supervised sessions and routes, and closed the live gate before release")
	if err := os.Remove(filepath.Join(driver.parent, "gate-enabled")); err != nil {
		return err
	}
	original, err = driver.retry(ctx, original, "original_retry_after_starting_crash")
	if err != nil {
		return err
	}
	retry, err := driver.retry(ctx, peer, "peer_retry_after_starting_crash")
	if err != nil {
		return err
	}
	if retry.SessionID == starting.SessionID {
		return errors.New("peer retry reused its interrupted starting session")
	}
	if err := distinctProjects(original, retry); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_after_starting_crash_retries"); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "explicit retries restored both projects with new sessions and stable DNS; subsequent ordinary peer cleanup must preserve the recovered original session")
	return nil
}

// interruptedStart distinguishes safe retryable settlement from quarantine, cancellation, or operation drift.
func interruptedStart(before, after domain.Operation) error {
	if after.ID != before.ID || after.ProjectID != before.ProjectID || after.IntentID != before.IntentID || after.Kind != domain.OperationKindProjectStart || after.State != domain.OperationFailed || after.Problem == nil {
		return errors.New("interrupted start has not failed at its exact operation boundary")
	}
	if err := after.Problem.Validate(); err != nil {
		return err
	}
	if after.Problem.Code != "project.recovery.process_absent" || !after.Problem.Retryable {
		return errors.New("interrupted start was quarantined or did not settle retryably")
	}
	return nil
}

// listenerAbsent proves connection refusal before test gate release can hide an orphan.
func listenerAbsent(ctx context.Context, address string) error {
	return waitFor(ctx, 45*time.Second, func(attempt context.Context) (bool, error) {
		dialer := &net.Dialer{Timeout: time.Second}
		connection, err := dialer.DialContext(attempt, "tcp4", address)
		if err == nil {
			connection.Close()
			return false, errors.New("orphan recovery gate listener remains live")
		}
		if connectionRefused(err) {
			return true, nil
		}
		return false, fmt.Errorf("gate absence is ambiguous: %w", err)
	})
}

// cleanup uses only typed production requests to settle and unregister this invocation's exact peer.
func (driver *recovery) cleanup(ctx context.Context) (result error) {
	stage := driver.evidence.Stage
	defer func() { driver.evidence.Stage = stage }()
	if driver.client != nil {
		driver.client.Close()
		driver.client = nil
	}
	if err := waitFor(ctx, 30*time.Second, func(attempt context.Context) (bool, error) {
		if err := ensureInstalledDaemon(attempt); err != nil {
			return false, err
		}
		client, err := connect(attempt)
		if err != nil {
			return false, err
		}
		if _, err := client.Status(attempt); err != nil {
			client.Close()
			return false, err
		}
		driver.client = client
		return true, nil
	}); err != nil {
		return err
	}
	defer func() {
		if driver.originalNeedsRetry {
			result = errors.Join(result, driver.restoreOriginal(ctx))
		}
	}()
	if driver.peer == "" {
		snapshot, err := driver.client.Snapshot(ctx)
		if err != nil {
			return err
		}
		for _, candidate := range snapshot.Projects {
			if candidate.Path != driver.peerPath {
				continue
			}
			if driver.peer != "" || candidate.ID == driver.original {
				return errors.New("ambiguous peer registration during cleanup")
			}
			driver.peer = candidate.ID
		}
		if driver.peer == "" {
			return nil
		}
	}
	project, err := driver.project(ctx, driver.peer)
	if err != nil {
		return err
	}
	if project.Path != driver.peerPath {
		return errors.New("refusing cleanup after peer path drift")
	}
	if _, err := fixtureRoot(project.Path); err != nil {
		return err
	}
	if err := driver.stop(ctx); err != nil {
		return err
	}
	if proof, err := readGateProof(filepath.Join(driver.parent, "entered.json"), driver.marker); err == nil {
		if err := listenerAbsent(ctx, proof.Address); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(driver.parent, "gate-enabled")); err != nil && !os.IsNotExist(err) {
		return err
	}
	intent := domain.IntentID("recovery-unregister-" + nonce())
	pending, err := driver.client.UnregisterProject(ctx, control.UnregisterProjectRequest{ProjectID: driver.peer, IntentID: intent})
	if err != nil {
		return err
	}
	if pending.Operation.ProjectID != driver.peer || pending.Operation.IntentID != intent {
		return errors.New("unregister correlation changed")
	}
	if pending.Operation.State == domain.OperationRequiresApproval {
		runner := projectapproval.New(driver.client, launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}))
		outcome, err := runner.Execute(ctx, projectapproval.Request{OperationID: pending.Operation.ID, ExpectedOperationRevision: pending.Revision})
		if err != nil {
			return err
		}
		if outcome.State != projectapproval.Succeeded || outcome.Confirmation == nil || outcome.HelperFailure != nil {
			return fmt.Errorf("native peer cleanup approval ended %s", outcome.State)
		}
		confirmation := outcome.Confirmation
		if err := confirmation.Validate(); err != nil {
			return err
		}
		if confirmation.Operation.ID != pending.Operation.ID || confirmation.Operation.ProjectID != driver.peer || confirmation.Operation.IntentID != intent || confirmation.Operation.State != domain.OperationSucceeded || confirmation.Revision <= pending.Revision {
			return errors.New("native peer cleanup confirmation differs from selected operation")
		}
	}
	if err := waitFor(ctx, 45*time.Second, func(attempt context.Context) (bool, error) {
		snapshot, err := driver.client.Snapshot(attempt)
		if err != nil {
			return false, err
		}
		for _, project := range snapshot.Projects {
			if project.ID == driver.peer {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	if driver.originalCurrent.SessionID == "" {
		return errors.New("original session evidence missing")
	}
	if driver.originalNeedsRetry {
		return nil
	}
	return driver.checkOriginal(ctx, driver.originalCurrent, "original_after_cleanup")
}

// restoreOriginal records an explicit failure-cleanup retry without masking the failed acceptance assertion.
func (driver *recovery) restoreOriginal(ctx context.Context) error {
	if driver.originalNeedsRetry {
		current, err := driver.project(ctx, driver.original)
		if err != nil {
			return err
		}
		if current.State == domain.ProjectFailed {
			if err := driver.settled(ctx, driver.originalCurrent, "original_settled_during_failure_cleanup"); err != nil {
				return err
			}
			if _, err := driver.retry(ctx, driver.originalCurrent, "original_restored_during_failure_cleanup"); err != nil {
				return err
			}
		} else if current.State == domain.ProjectReady {
			if err := driver.checkOriginal(ctx, driver.originalCurrent, "original_unchanged_during_failure_cleanup"); err != nil {
				return err
			}
			driver.originalNeedsRetry = false
		} else {
			return errors.New("original cannot be safely restored from an unexpected recovery state")
		}
	}
	return nil
}
