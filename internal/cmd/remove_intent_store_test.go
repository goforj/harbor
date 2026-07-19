package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

const (
	projectRemovalIntentHelperEnvironment    = "HARBOR_TEST_REMOVE_INTENT_HELPER"
	projectRemovalIntentHelperDirectory      = "HARBOR_TEST_REMOVE_INTENT_DIRECTORY"
	projectRemovalIntentHelperGeneratedID    = "HARBOR_TEST_REMOVE_INTENT_ID"
	projectRemovalIntentHelperProcessProject = "project-process"
)

// newProjectRemovalIntentJournalFixture creates one independent journal view over a caller-owned test directory.
func newProjectRemovalIntentJournalFixture(directory string) *filesystemProjectRemovalIntentJournal {
	return &filesystemProjectRemovalIntentJournal{
		dataDirectory: func() (string, error) { return directory, nil },
	}
}

// TestProjectRemovalIntentJournalRetainsAndClearsExactAttempts verifies explicit and generated identities share one durable lifecycle.
func TestProjectRemovalIntentJournalRetainsAndClearsExactAttempts(t *testing.T) {
	directory := t.TempDir()
	first := newProjectRemovalIntentJournalFixture(directory)
	intentID, err := first.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		return "intent-first", nil
	})
	if err != nil || intentID != "intent-first" {
		t.Fatalf("first intent = %q, %v, want intent-first", intentID, err)
	}

	second := newProjectRemovalIntentJournalFixture(directory)
	factoryCalls := 0
	intentID, err = second.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-unexpected", nil
	})
	if err != nil || intentID != "intent-first" || factoryCalls != 0 {
		t.Fatalf("reloaded intent = %q, %v, factory calls %d, want retained intent-first", intentID, err, factoryCalls)
	}
	if _, err := second.LoadOrCreate(t.Context(), "project-orders", "intent-other", nil); err == nil || !strings.Contains(err.Error(), "intent-first") {
		t.Fatalf("conflicting explicit intent error = %v, want retained identity", err)
	}
	if err := second.Clear(t.Context(), "project-orders", "intent-other"); err == nil {
		t.Fatal("mismatched clear error = nil")
	}
	if err := second.Clear(t.Context(), "project-orders", "intent-first"); err != nil {
		t.Fatalf("clear exact intent: %v", err)
	}
	intentID, err = first.LoadOrCreate(t.Context(), "project-orders", "intent-second", nil)
	if err != nil || intentID != "intent-second" {
		t.Fatalf("fresh explicit intent = %q, %v, want intent-second", intentID, err)
	}
}

// TestProjectRemovalIntentJournalSerializesIndependentProcesses verifies file locking converges concurrent CLI launches on one intent.
func TestProjectRemovalIntentJournalSerializesIndependentProcesses(t *testing.T) {
	if runProjectRemovalIntentHelperProcess() {
		return
	}
	directory := t.TempDir()
	type processResult struct {
		output string
		err    error
	}
	results := make(chan processResult, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, generatedID := range []string{"intent-process-a", "intent-process-b"} {
		generatedID := generatedID
		go func() {
			ready.Done()
			<-start
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestProjectRemovalIntentJournalSerializesIndependentProcesses$")
			command.Env = append(
				os.Environ(),
				projectRemovalIntentHelperEnvironment+"=1",
				projectRemovalIntentHelperDirectory+"="+directory,
				projectRemovalIntentHelperGeneratedID+"="+generatedID,
			)
			output, err := command.CombinedOutput()
			results <- processResult{output: strings.TrimSpace(string(output)), err: err}
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	for index, result := range []processResult{first, second} {
		if result.err != nil {
			t.Fatalf("helper %d error = %v:\n%s", index, result.err, result.output)
		}
	}
	if first.output != second.output || first.output != "intent-process-a" && first.output != "intent-process-b" {
		t.Fatalf("helper intents = %q and %q, want one shared generated identity", first.output, second.output)
	}
}

// runProjectRemovalIntentHelperProcess performs one journal transaction in a genuinely independent test process.
func runProjectRemovalIntentHelperProcess() bool {
	if os.Getenv(projectRemovalIntentHelperEnvironment) != "1" {
		return false
	}
	directory := os.Getenv(projectRemovalIntentHelperDirectory)
	generatedID := domain.IntentID(os.Getenv(projectRemovalIntentHelperGeneratedID))
	journal := newProjectRemovalIntentJournalFixture(directory)
	intentID, err := journal.LoadOrCreate(context.Background(), projectRemovalIntentHelperProcessProject, "", func() (domain.IntentID, error) {
		return generatedID, nil
	})
	if err != nil {
		_, _ = fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}
	_, _ = fmt.Fprint(os.Stdout, intentID)
	os.Exit(0)
	return true
}

// TestProjectRemovalIntentJournalRejectsInvalidRetainedState verifies corruption is never silently replaced by a new operation identity.
func TestProjectRemovalIntentJournalRejectsInvalidRetainedState(t *testing.T) {
	directory := t.TempDir()
	journal := newProjectRemovalIntentJournalFixture(directory)
	if _, err := journal.LoadOrCreate(t.Context(), "project-orders", "intent-first", nil); err != nil {
		t.Fatalf("create retained intent: %v", err)
	}
	path := filepath.Join(directory, projectRemovalIntentDirectory, projectRemovalIntentPath("project-orders"))
	if err := os.WriteFile(path, []byte(`{"version":1,"project_id":"project-other","intent_id":"intent-first"}`), projectRemovalIntentFileMode); err != nil {
		t.Fatalf("replace retained state: %v", err)
	}
	factoryCalls := 0
	_, err := journal.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-second", nil
	})
	if err == nil || factoryCalls != 0 {
		t.Fatalf("corrupt retained state error = %v, factory calls %d, want rejection before generation", err, factoryCalls)
	}
}

// TestProjectRemovalIntentJournalHonorsCancelledWaiters verifies in-process contention does not make cancellation unresponsive.
func TestProjectRemovalIntentJournalHonorsCancelledWaiters(t *testing.T) {
	<-projectRemovalIntentProcessGate
	defer releaseProjectRemovalIntentProcessGate()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	journal := newProjectRemovalIntentJournalFixture(t.TempDir())
	_, err := journal.LoadOrCreate(ctx, "project-orders", "intent-first", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}
}
