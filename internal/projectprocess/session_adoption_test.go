//go:build darwin || linux || windows

package projectprocess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// sessionTestBrokerLauncher preserves a native handoff error rather than hiding the optional fallback in assertions.
type sessionTestBrokerLauncher struct {
	*OutputBrokerProcessLauncher
	launchErr error
}

// newSessionTestBrokerLauncher retains production launch behavior and records only its diagnostic error.
func newSessionTestBrokerLauncher(path string) (*sessionTestBrokerLauncher, error) {
	launcher, err := NewOutputBrokerProcessLauncher(path)
	if err != nil {
		return nil, err
	}
	return &sessionTestBrokerLauncher{OutputBrokerProcessLauncher: launcher}, nil
}

// Launch records the handoff result without exposing ticket material.
func (launcher *sessionTestBrokerLauncher) Launch(ctx context.Context, spec OutputBrokerLaunchSpec) (OutputBrokerAttachment, error) {
	attachment, err := launcher.OutputBrokerProcessLauncher.Launch(ctx, spec)
	launcher.launchErr = err
	return attachment, err
}

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
	launcher, err := newSessionTestBrokerLauncher(buildOutputBrokerTestBinary(t))
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
		t.Fatalf("durable broker handoff failed: %v", launcher.launchErr)
	}
	waitForOutput(t, output, "stream=")
	for replacement := 0; replacement < 2; replacement++ {
		if err := owner.DetachSession(t.Context(), session.ProjectID, session.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Start(t.Context(), StartRequest{ProjectID: session.ProjectID, SessionID: "session-forbidden-after-detach", CheckoutRoot: checkout, EnvironmentOverrides: projectProcessTestEnvironment()}); !errors.Is(err, ErrClosed) {
			t.Fatalf("detached owner admitted another launch: %v", err)
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
		if root, found := owner.serviceObservationCheckout(session.ProjectID, session.ID); !found || root != checkout {
			t.Fatalf("adopted service observation checkout = %q, %t; want %q", root, found, checkout)
		}
		resources, err := owner.ObserveFrameworkResources(t.Context(), session.ProjectID, session.ID, EnvironmentOverrides{"IP_ADDRESS": "127.77.9.8"})
		if err != nil || !resources.Supported || len(resources.Resources) != 1 || resources.Resources[0].URL != "http://127.77.9.8:8025" {
			t.Fatalf("adopted resource observation = %#v, %v", resources, err)
		}
		waitForFreshSessionOutput(t, owner, session)
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
	launcher, err := newSessionTestBrokerLauncher(buildOutputBrokerTestBinary(t))
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
		t.Fatalf("durable broker handoff failed: %v", launcher.launchErr)
	}
	drifts := []string{"root birth", "broker birth", "broker ticket", "missing broker", "broker arguments", "broker image", "wrong project", "wrong session", "noncanonical checkout", "file checkout", "cancelled", "closed"}
	if runtime.GOOS == "windows" {
		drifts = append(drifts, "legacy lifetime metadata", "foreign job member")
	}
	for _, drift := range drifts {
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
			case "broker arguments":
				broker.Process.ArgumentDigest = strings.Repeat("d", 64)
			case "broker image":
				broker.Process.ExecutableIdentity = filepath.Join(checkout, "another-broker")
			case "wrong project":
				candidate.ProjectID = "project-foreign"
			case "wrong session":
				candidate.ID = "session-foreign"
			}
			replacement := newTestSupervisor(options)
			defer replacement.Close(t.Context())
			adoptionCheckout := checkout
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch drift {
			case "noncanonical checkout":
				adoptionCheckout = checkout + string(os.PathSeparator) + "."
			case "file checkout":
				adoptionCheckout = filepath.Join(checkout, "not-a-directory")
				if err := os.WriteFile(adoptionCheckout, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "closed":
				if err := replacement.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "foreign job member":
				current, err := CaptureCurrentProcessEvidence()
				if err != nil {
					t.Fatal(err)
				}
				*candidate.Process = current
			case "legacy lifetime metadata":
				original, err := os.ReadFile(broker.ManifestPath)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.WriteFile(broker.ManifestPath, original, 0600); err != nil {
						t.Error(err)
					}
				}()
				config, err := ReadOutputBrokerLaunchConfig(broker.ManifestPath)
				if err != nil {
					t.Fatal(err)
				}
				config.RetainProcessScope = false
				data, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(broker.ManifestPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := replacement.AdoptSession(ctx, candidate, adoptionCheckout); err == nil {
				t.Fatal("changed ownership proof was accepted")
			}
			if drift == "legacy lifetime metadata" {
				if _, err := replacement.SettleSession(t.Context(), candidate); err == nil {
					t.Fatal("missing retained-job metadata downgraded to parent-only settlement")
				}
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
	launcher, err := newSessionTestBrokerLauncher(os.Getenv("HARBOR_CONTINUITY_BROKER"))
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
		t.Fatalf("owner broker handoff failed: %v", launcher.launchErr)
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
	launcher, err := newSessionTestBrokerLauncher(broker)
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
	waitForFreshSessionOutput(t, replacement, session)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := replacement.Stop(ctx, session.ProjectID, session.ID); err != nil {
		t.Fatal(err)
	}
	result, err := handle.Wait(ctx)
	if err != nil || result.ScopeSettlementErr != nil || !result.StopRequested {
		t.Fatalf("Stop after actual owner crash: %#v, %v", result, err)
	}
}

// waitForFreshSessionOutput excludes replayed history by requiring a helper timestamp after this observation began.
func waitForFreshSessionOutput(t *testing.T, supervisor *Supervisor, session domain.ProjectSession) {
	t.Helper()
	notBefore := time.Now().UnixNano()
	cursor := supervisor.ReadOutput(session.ProjectID, session.ID, 0).NextCursor
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		chunk, err := supervisor.WaitOutput(ctx, session.ProjectID, session.ID, cursor)
		if err != nil {
			t.Fatalf("fresh live output after adoption: %#v, %v", chunk, err)
		}
		cursor = chunk.NextCursor
		for _, line := range strings.Split(chunk.Text, "\n") {
			if !strings.HasPrefix(line, "stream=") {
				continue
			}
			timestamp, err := strconv.ParseInt(strings.TrimPrefix(line, "stream="), 10, 64)
			if err == nil && timestamp >= notBefore {
				return
			}
		}
		if err := ctx.Err(); err != nil {
			t.Fatal("reconnected output contains only history")
		}
	}
}

// TestSessionAdoptionSettlesDescendantsAfterUnexpectedLeaderExit proves cheap leader monitoring still retires the complete native scope.
func TestSessionAdoptionSettlesDescendantsAfterUnexpectedLeaderExit(t *testing.T) {
	installForjHelper(t, "tree")
	childReceipt := filepath.Join(canonicalTestPath(t, t.TempDir()), "child.pid")
	t.Setenv(helperPIDFileEnvironment, childReceipt)
	launcher, err := newSessionTestBrokerLauncher(buildOutputBrokerTestBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	checkout := canonicalTestPath(t, t.TempDir())
	options := Options{OutputSpoolDirectory: canonicalTestPath(t, t.TempDir()), OutputBrokerLauncher: launcher, GracePeriod: 500 * time.Millisecond}
	owner := newTestSupervisor(options)
	handle, err := owner.Start(t.Context(), StartRequest{ProjectID: "project-unexpected-exit", SessionID: "session-unexpected-exit", CheckoutRoot: checkout, EnvironmentOverrides: projectProcessTestEnvironment()})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionForProcessHandle(handle)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = owner.Close(ctx)
		_, _ = owner.SettleSession(ctx, session)
	})
	if session.OutputBroker == nil {
		t.Fatalf("durable broker handoff failed: %v", launcher.launchErr)
	}
	if !waitForPublishedHelperPID(childReceipt) {
		t.Fatal("owned descendant did not start")
	}
	owner.mu.Lock()
	originalRoot := owner.projects[session.ProjectID].command.Process
	owner.mu.Unlock()
	if err := owner.DetachSession(t.Context(), session.ProjectID, session.ID); err != nil {
		t.Fatal(err)
	}
	replacement := newTestSupervisor(options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = replacement.Close(ctx)
	})
	handle, err = replacement.AdoptSession(t.Context(), session, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := originalRoot.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := handle.Wait(ctx)
	if err != nil || result.StopRequested || result.ScopeSettlementErr != nil {
		t.Fatalf("unexpected root exit did not settle its ignoring descendant: %#v, %v", result, err)
	}
}
