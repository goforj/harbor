package projectprocess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

const (
	serviceObservationHelperModeEnvironment = "GO_HARBOR_SERVICE_OBSERVATION_HELPER_MODE"
	serviceObservationHelperMarkerEnv       = "GO_HARBOR_SERVICE_OBSERVATION_HELPER_MARKER"
)

// TestDecodeServiceObservationProjectsActiveServices verifies deterministic semantic rows without persisting container internals.
func TestDecodeServiceObservationProjectsActiveServices(t *testing.T) {
	report := goForjServiceObservationReport{
		SchemaVersion: serviceObservationSchemaVersion,
		Supported:     true,
		Services: []goForjServiceObservationItem{
			{ID: "redis", Name: "Redis", Kind: "compose", State: domain.EntityWorking, Active: true},
			{ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady, Active: true, Required: true},
			{ID: "old", Name: "Old", Kind: "compose", State: domain.EntityStopped, Active: false},
		},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	observation, err := decodeServiceObservation(encoded)
	if err != nil {
		t.Fatalf("decodeServiceObservation() error = %v", err)
	}
	if !observation.Supported || len(observation.Services) != 2 {
		t.Fatalf("decodeServiceObservation() = %#v", observation)
	}
	if observation.Services[0] != (domain.ServiceSnapshot{
		ID:        "mysql",
		Name:      "MySQL",
		Kind:      "compose",
		State:     domain.EntityReady,
		Owner:     domain.ServiceOwnerCompose,
		Selection: domain.ServiceSelected,
		Required:  true,
	}) {
		t.Fatalf("first service = %#v", observation.Services[0])
	}
	if observation.Services[1].ID != "redis" || observation.Services[1].State != domain.EntityWorking {
		t.Fatalf("second service = %#v", observation.Services[1])
	}
}

// TestDecodeServiceObservationPreservesCompatibility verifies explicit unsupported reports retain the historical empty projection.
func TestDecodeServiceObservationPreservesCompatibility(t *testing.T) {
	observation, err := decodeServiceObservation([]byte(`{"schema_version":1,"supported":false,"problem":"custom Compose task","services":[]}`))
	if err != nil {
		t.Fatalf("decodeServiceObservation() error = %v", err)
	}
	if observation.Supported || observation.Services == nil || len(observation.Services) != 0 {
		t.Fatalf("decodeServiceObservation() = %#v", observation)
	}
}

// TestDecodeServiceObservationRejectsInvalidReports covers every trust-boundary branch added by the machine contract.
func TestDecodeServiceObservationRejectsInvalidReports(t *testing.T) {
	invalidService := goForjServiceObservationItem{
		ID:     "mysql",
		Name:   "MySQL",
		Kind:   "compose",
		State:  domain.EntityReady,
		Active: true,
	}
	tests := []struct {
		name   string
		report goForjServiceObservationReport
		raw    []byte
		want   string
	}{
		{name: "empty", raw: []byte("  "), want: "empty"},
		{name: "malformed", raw: []byte("{"), want: "decode"},
		{name: "schema", report: goForjServiceObservationReport{SchemaVersion: 2, Supported: true, Services: []goForjServiceObservationItem{}}, want: "schema"},
		{name: "null services", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true}, want: "must not be null"},
		{name: "too many services", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Services: make([]goForjServiceObservationItem, maximumObservedProjectServices+1)}, want: "exceeds"},
		{name: "problem whitespace", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Problem: " problem ", Services: []goForjServiceObservationItem{}}, want: "canonical"},
		{name: "problem length", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Problem: strings.Repeat("x", maximumServiceObservationProblem+1), Services: []goForjServiceObservationItem{}}, want: "canonical"},
		{name: "supported problem", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Problem: "compose unavailable", Services: []goForjServiceObservationItem{}}, want: "could not observe"},
		{name: "invalid service", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Services: []goForjServiceObservationItem{{ID: " bad", Name: "Bad", Kind: "compose", State: domain.EntityReady, Active: true}}}, want: "validate"},
		{name: "active stopped", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Services: []goForjServiceObservationItem{{ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityStopped, Active: true}}}, want: "cannot be stopped"},
		{name: "duplicate", report: goForjServiceObservationReport{SchemaVersion: 1, Supported: true, Services: []goForjServiceObservationItem{invalidService, invalidService}}, want: "duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := test.raw
			if encoded == nil {
				var err error
				encoded, err = json.Marshal(test.report)
				if err != nil {
					t.Fatalf("marshal report: %v", err)
				}
			}
			_, err := decodeServiceObservation(encoded)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeServiceObservation() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestBoundedObservationBufferRejectsOversize verifies output beyond the fixed budget is discarded and marked.
func TestBoundedObservationBufferRejectsOversize(t *testing.T) {
	buffer := &boundedObservationBuffer{maximum: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if !buffer.exceeded || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("buffer = exceeded:%t bytes:%q", buffer.exceeded, buffer.Bytes())
	}
	if written, err := buffer.Write([]byte("more")); err != nil || written != 4 || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("second Write() = %d, %v, bytes %q", written, err, buffer.Bytes())
	}
}

// TestExecuteOwnedServiceObservationCommandCancelsDescendants proves a timed-out status query cannot orphan its container-runtime child.
func TestExecuteOwnedServiceObservationCommandCancelsDescendants(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "child.pid")
	environment := append(os.Environ(),
		serviceObservationHelperModeEnvironment+"=root",
		serviceObservationHelperMarkerEnv+"="+marker,
	)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- executeOwnedServiceObservationCommand(
			ctx,
			executable,
			[]string{"-test.run=^TestServiceObservationOwnedCommandHelper$"},
			"",
			environment,
			&boundedObservationBuffer{maximum: 4096},
			&boundedObservationBuffer{maximum: 4096},
		)
	}()
	childPID := waitForServiceObservationHelperPID(t, marker)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executeOwnedServiceObservationCommand() error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned service observation did not return after cancellation")
	}
	assertServiceObservationProcessAbsent(t, childPID)
}

// TestServiceObservationOwnedCommandHelper creates one descendant that ignores a root-only interrupt for ownership tests.
func TestServiceObservationOwnedCommandHelper(t *testing.T) {
	switch os.Getenv(serviceObservationHelperModeEnvironment) {
	case "":
		return
	case "child":
		marker := os.Getenv(serviceObservationHelperMarkerEnv)
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(2)
		}
		signal.Ignore(os.Interrupt)
		select {}
	case "root":
		command := exec.Command(os.Args[0], "-test.run=^TestServiceObservationOwnedCommandHelper$")
		command.Env = append(os.Environ(), serviceObservationHelperModeEnvironment+"=child")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(2)
		}
		select {}
	default:
		os.Exit(2)
	}
}

// waitForServiceObservationHelperPID waits for the descendant to prove it started before cancellation.
func waitForServiceObservationHelperPID(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		encoded, err := os.ReadFile(marker)
		if err == nil {
			pid, conversionErr := strconv.Atoi(strings.TrimSpace(string(encoded)))
			if conversionErr != nil || pid <= 0 {
				t.Fatalf("helper PID = %q, %v", encoded, conversionErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper marker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service observation helper did not create %s", marker)
	return 0
}

// assertServiceObservationProcessAbsent waits until the cancelled descendant is absent from the native process table.
func assertServiceObservationProcessAbsent(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, alive, err := observeProcessBirthToken(pid)
		if err == nil && !alive {
			return
		}
		if err != nil {
			t.Fatalf("observe helper process %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service observation descendant %d remained alive after cancellation", pid)
}
