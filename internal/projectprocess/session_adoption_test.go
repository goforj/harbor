//go:build darwin || linux || windows

package projectprocess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// sessionForProcessHandle constructs the durable ownership facts returned by a real test-owned launch.
func sessionForProcessHandle(handle *Handle) domain.ProjectSession {
	info := handle.Info()
	peer := info.OutputBroker
	session := domain.ProjectSession{
		ProjectID: info.ProjectID, ID: info.SessionID, Owner: domain.SessionOwnerHarbor,
		State: domain.SessionAwaitingAttach, Generation: 2,
		DescriptorDigest: strings.Repeat("a", 64), CredentialDigest: strings.Repeat("b", 64),
		CreatedAt: info.StartedAt, UpdatedAt: info.StartedAt,
		Process: &domain.ProcessEvidence{PID: info.Evidence.PID, BirthToken: info.Evidence.BirthToken,
			ExecutableIdentity: info.Evidence.ExecutableIdentity, ArgumentDigest: info.Evidence.ArgumentsSHA256},
	}
	if peer != nil {
		session.OutputBroker = &domain.OutputBrokerSession{
			EndpointReference: peer.EndpointReference, Process: peer.Process,
			ManifestPath: peer.ManifestPath, CredentialDigest: peer.TicketDigest,
		}
	}
	return session
}

// TestSessionSurvivesRepeatedSupervisorDetachAndNativeAdoption proves same-process output continuity and Stop after two replacements.
func TestSessionSurvivesRepeatedSupervisorDetachAndNativeAdoption(t *testing.T) {
	installForjHelper(t, "stream-wait")
	launcher, err := NewOutputBrokerProcessLauncher(buildOutputBrokerTestBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := canonicalTestPath(t, t.TempDir())
	checkout := canonicalTestPath(t, t.TempDir())
	sentinel := filepath.Join(checkout, "preserved-source.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged project source"), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{OutputSpoolDirectory: outputRoot, OutputBrokerLauncher: launcher, GracePeriod: 500 * time.Millisecond}
	owner := newTestSupervisor(options)
	output := &synchronizedBuffer{}
	handle, err := owner.Start(t.Context(), StartRequest{
		ProjectID: "project-continuity", SessionID: "session-continuity", CheckoutRoot: checkout,
		EnvironmentOverrides: projectProcessTestEnvironment(), Stdout: output, Stderr: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionForProcessHandle(handle)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = owner.SettlePriorProcess(ctx, *session.Process)
		_ = owner.Close(ctx)
	})
	if session.OutputBroker == nil {
		t.Fatal("launch fell back to pipes rather than establishing a real durable broker")
	}
	waitForOutput(t, output, "stream=")
	for replacement := 0; replacement < 2; replacement++ {
		if err := owner.DetachSession(t.Context(), session.ProjectID, session.ID); err != nil {
			t.Fatal(err)
		}
		if err := owner.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		owner = newTestSupervisor(options)
		handle, err = owner.AdoptSession(t.Context(), session, checkout)
		if err != nil {
			t.Fatal(err)
		}
		if info := handle.Info(); info.Evidence.PID != session.Process.PID || info.Evidence.BirthToken != session.Process.BirthToken || info.SessionID != session.ID {
			t.Fatalf("adoption changed the process or session: %#v", info)
		}
		cursor := owner.ReadOutput(session.ProjectID, session.ID, 0).NextCursor
		deadline, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		chunk, readErr := owner.WaitOutput(deadline, session.ProjectID, session.ID, cursor)
		cancel()
		if readErr != nil || !chunk.Available || !strings.Contains(chunk.Text, "stream=") {
			t.Fatalf("fresh live output after adoption: %#v, %v", chunk, readErr)
		}
		if _, err := owner.Start(t.Context(), StartRequest{ProjectID: session.ProjectID, SessionID: "session-duplicate", CheckoutRoot: checkout, EnvironmentOverrides: projectProcessTestEnvironment()}); !errors.Is(err, ErrProjectRunning) {
			t.Fatalf("adopted scope did not fence a duplicate launch: %v", err)
		}
	}
	if err := owner.Stop(t.Context(), session.ProjectID, session.ID); err != nil {
		t.Fatal(err)
	}
	result, err := handle.Wait(t.Context())
	if err != nil || !result.StopRequested || result.ScopeSettlementErr != nil {
		t.Fatalf("adopted Stop: %#v, %v", result, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "unchanged project source" {
		t.Fatalf("project source was changed: %q, %v", data, err)
	}
}

// TestSessionAdoptionRejectsDriftWithoutStoppingTheOriginal preserves the process when broker or birth proof changes.
func TestSessionAdoptionRejectsDriftWithoutStoppingTheOriginal(t *testing.T) {
	installForjHelper(t, "wait")
	launcher, err := NewOutputBrokerProcessLauncher(buildOutputBrokerTestBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	options := Options{OutputSpoolDirectory: canonicalTestPath(t, t.TempDir()), OutputBrokerLauncher: launcher, GracePeriod: 500 * time.Millisecond}
	owner := newTestSupervisor(options)
	defer owner.Close(t.Context())
	checkout := canonicalTestPath(t, t.TempDir())
	handle, err := owner.Start(t.Context(), StartRequest{ProjectID: "project-proof", SessionID: "session-proof", CheckoutRoot: checkout, EnvironmentOverrides: projectProcessTestEnvironment()})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionForProcessHandle(handle)
	if session.OutputBroker == nil {
		t.Fatal("test requires its real broker")
	}
	for _, drift := range []string{"root birth", "broker birth", "broker ticket", "missing broker"} {
		t.Run(drift, func(t *testing.T) {
			candidate := session
			process, broker := *session.Process, *session.OutputBroker
			candidate.Process, candidate.OutputBroker = &process, &broker
			switch drift {
			case "root birth":
				process.BirthToken += "-replacement"
			case "broker birth":
				broker.Process.BirthToken += "-replacement"
			case "broker ticket":
				broker.CredentialDigest = strings.Repeat("c", 64)
			case "missing broker":
				candidate.OutputBroker = nil
			}
			replacement := newTestSupervisor(options)
			defer replacement.Close(t.Context())
			if _, err := replacement.AdoptSession(t.Context(), candidate, checkout); err == nil {
				t.Fatal("changed ownership proof was accepted")
			}
			observation, err := owner.ObservePriorProcess(t.Context(), *session.Process)
			if err != nil || observation.State != PriorProcessPresent {
				t.Fatalf("failed adoption affected the original process: %#v, %v", observation, err)
			}
		})
	}
}

// TestSessionOwnerSubprocess supplies an actual supervisor process that the crash test can kill without cleanup.
func TestSessionOwnerSubprocess(t *testing.T) {
	if os.Getenv("HARBOR_CONTINUITY_OWNER") != "1" {
		return
	}
	launcher, err := NewOutputBrokerProcessLauncher(os.Getenv("HARBOR_CONTINUITY_BROKER"))
	if err != nil {
		t.Fatal(err)
	}
	environment := replaceEnvironment(os.Environ(), helperEnabledEnvironment, "1")
	environment = replaceEnvironment(environment, helperModeEnvironment, "stream-wait")
	supervisor := newTestSupervisor(Options{OutputSpoolDirectory: os.Getenv("HARBOR_CONTINUITY_OUTPUT"), OutputBrokerLauncher: launcher, Environment: environment})
	handle, err := supervisor.Start(t.Context(), StartRequest{ProjectID: "project-crash", SessionID: "session-crash", CheckoutRoot: os.Getenv("HARBOR_CONTINUITY_CHECKOUT"), EnvironmentOverrides: projectProcessTestEnvironment()})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionForProcessHandle(handle)
	if session.OutputBroker == nil {
		t.Fatal("owner did not establish a durable broker")
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	receipt := os.Getenv("HARBOR_CONTINUITY_RECEIPT")
	if err := os.WriteFile(receipt+".tmp", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(receipt+".tmp", receipt); err != nil {
		t.Fatal(err)
	}
	waitForTerminationSignal()
}

// TestSessionSurvivesHardSupervisorCrash proves native scope lifetime and fresh logs without executing old-owner shutdown.
func TestSessionSurvivesHardSupervisorCrash(t *testing.T) {
	installForjHelper(t, "stream-wait")
	broker := buildOutputBrokerTestBinary(t)
	outputRoot, checkout := canonicalTestPath(t, t.TempDir()), canonicalTestPath(t, t.TempDir())
	receipt := filepath.Join(t.TempDir(), "session.json")
	owner := exec.Command(os.Args[0], "-test.run=^TestSessionOwnerSubprocess$")
	owner.Env = replaceEnvironment(os.Environ(), helperEnabledEnvironment, "0")
	owner.Env = append(owner.Env, "HARBOR_CONTINUITY_OWNER=1", "HARBOR_CONTINUITY_BROKER="+broker, "HARBOR_CONTINUITY_OUTPUT="+outputRoot, "HARBOR_CONTINUITY_CHECKOUT="+checkout, "HARBOR_CONTINUITY_RECEIPT="+receipt)
	output := &synchronizedBuffer{}
	owner.Stdout, owner.Stderr = output, output
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})
	var session domain.ProjectSession
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(receipt); err == nil {
			if err := json.Unmarshal(data, &session); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("supervisor did not persist its live session: %s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	launcher, err := NewOutputBrokerProcessLauncher(broker)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newTestSupervisor(Options{OutputSpoolDirectory: outputRoot, OutputBrokerLauncher: launcher, GracePeriod: 500 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = replacement.Close(ctx)
		_, _ = replacement.SettleSession(ctx, session)
	})
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("supervisor exited cleanly instead of being crashed")
	}
	handle, err := replacement.AdoptSession(t.Context(), session, checkout)
	if err != nil {
		t.Fatalf("native adoption after hard supervisor crash: %v", err)
	}
	if info := handle.Info(); info.Evidence.PID != session.Process.PID || info.Evidence.BirthToken != session.Process.BirthToken {
		t.Fatalf("crash recovery replaced the original process: %#v", info)
	}
	cursor := replacement.ReadOutput(session.ProjectID, session.ID, 0).NextCursor
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	chunk, err := replacement.WaitOutput(ctx, session.ProjectID, session.ID, cursor)
	if err != nil || !strings.Contains(chunk.Text, "stream=") {
		t.Fatalf("live output after owner crash: %#v, %v", chunk, err)
	}
	if err := replacement.Stop(ctx, session.ProjectID, session.ID); err != nil {
		t.Fatal(err)
	}
	result, err := handle.Wait(ctx)
	if err != nil || result.ScopeSettlementErr != nil || !result.StopRequested {
		t.Fatalf("Stop after actual owner crash: %#v, %v", result, err)
	}
}
