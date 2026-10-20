package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
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

// scenarios proves ready-session adoption, interrupted-start settlement, and an independent retry.
func (driver *recovery) scenarios(ctx context.Context, original projectEvidence) error {
	if _, err := driver.start(ctx); err != nil {
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
	if err := driver.crash(ctx); err != nil {
		return fmt.Errorf("ready crash: %w", err)
	}
	after, err := driver.healthy(ctx, driver.peer)
	driver.evidence.Observations["peer_ready_after_crash"] = after
	if err != nil {
		return err
	}
	if err := continuity(peer, after); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_after_ready_crash"); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "ready crash preserved both exact sessions, distinct DNS addresses, and trusted HTTPS")
	if err := driver.stop(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(driver.parent, "gate-enabled"), []byte(driver.marker), 0600); err != nil {
		return err
	}
	operation, err := driver.start(ctx)
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
				return false, errors.New("gated start session changed while waiting for attachment")
			}
		}
		snapshot, err := driver.client.Snapshot(attempt)
		if err != nil {
			return false, err
		}
		for _, current := range snapshot.Operations {
			if current.ID == operation.ID && current.ProjectID == driver.peer && current.IntentID == operation.IntentID && current.State == domain.OperationRunning && starting.State == domain.ProjectStarting && starting.SessionID != "" && starting.SessionState == domain.SessionAttached {
				driver.evidence.InterruptedOperation = &current
				return true, nil
			}
		}
		return false, errors.New("gate has not reached an attached Starting session with running operation")
	})
	driver.evidence.Observations["peer_starting_before_crash"] = starting
	if err != nil {
		return err
	}
	driver.evidence.Gate = &proof
	if starting.Address != peer.Address || starting.Host != peer.Host || starting.SessionID == peer.SessionID {
		return errors.New("second peer start changed DNS identity or reused the stopped session")
	}
	if err := distinctProjects(original, starting); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_before_starting_crash"); err != nil {
		return err
	}
	if err := driver.crash(ctx); err != nil {
		return fmt.Errorf("starting crash: %w", err)
	}
	err = waitFor(ctx, 90*time.Second, func(attempt context.Context) (bool, error) {
		snapshot, err := driver.client.Snapshot(attempt)
		if err != nil {
			return false, err
		}
		for _, current := range snapshot.Operations {
			if current.ID != operation.ID {
				continue
			}
			if current.ProjectID != driver.peer || current.IntentID != operation.IntentID {
				return false, errors.New("interrupted operation correlation changed")
			}
			driver.evidence.InterruptedOperation = &current
			if current.State != domain.OperationFailed || current.Problem == nil {
				return false, errors.New("interrupted start has not failed with a safe problem")
			}
			if err := current.Problem.Validate(); err != nil {
				return false, err
			}
			if current.Problem.Code != "project.recovery.process_absent" || !current.Problem.Retryable {
				return false, errors.New("interrupted start was quarantined or did not settle retryably")
			}
			project, err := driver.project(attempt, driver.peer)
			if err != nil {
				return false, err
			}
			activity, err := driver.client.ProjectActivity(attempt, control.ProjectActivityRequest{ProjectID: driver.peer})
			if err != nil {
				return false, err
			}
			return (project.State == domain.ProjectFailed || project.State == domain.ProjectStopped) && activity.Session == nil, nil
		}
		return false, errors.New("interrupted start operation is absent")
	})
	if err != nil {
		return err
	}
	if err := listenerAbsent(ctx, proof.Address); err != nil {
		return err
	}
	failed, err := driver.observe(ctx, driver.peer, false)
	driver.evidence.Observations["peer_settled_after_crash"] = failed
	if err != nil {
		return err
	}
	if failed.Address != peer.Address || failed.Host != peer.Host || failed.SessionID != "" {
		return errors.New("failed peer retained a session or changed DNS identity")
	}
	if err := driver.checkOriginal(ctx, original, "original_after_starting_crash"); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "starting crash failed the exact operation retryably, retired its session, and closed the live gate before release")
	if err := os.Remove(filepath.Join(driver.parent, "gate-enabled")); err != nil {
		return err
	}
	if _, err := driver.start(ctx); err != nil {
		return err
	}
	retry, err := driver.healthy(ctx, driver.peer)
	driver.evidence.Observations["peer_retry_ready"] = retry
	if err != nil {
		return err
	}
	if retry.Address != peer.Address || retry.Host != peer.Host || retry.SessionID == starting.SessionID || retry.SessionID == peer.SessionID {
		return errors.New("retry did not establish a fresh session with stable DNS")
	}
	if err := distinctProjects(original, retry); err != nil {
		return err
	}
	if err := driver.checkOriginal(ctx, original, "original_after_retry"); err != nil {
		return err
	}
	driver.evidence.Assertions = append(driver.evidence.Assertions, "fresh intent retried peer to Ready with new session, stable DNS and trusted HTTPS; original session remained healthy")
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
		if errors.Is(err, syscall.ECONNREFUSED) {
			return true, nil
		}
		return false, fmt.Errorf("gate absence is ambiguous: %w", err)
	})
}

// cleanup uses only typed production requests to settle and unregister this invocation's exact peer.
func (driver *recovery) cleanup(ctx context.Context) error {
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
	original, ok := driver.evidence.Observations["original_initial"]
	if !ok {
		return errors.New("initial original evidence missing")
	}
	return driver.checkOriginal(ctx, original, "original_after_cleanup")
}
