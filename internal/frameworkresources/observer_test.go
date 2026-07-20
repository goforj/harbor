package frameworkresources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const frameworkResourceHelperEnvironment = "GO_WANT_FRAMEWORK_RESOURCE_HELPER"

// TestMain turns this package's test executable into an exact fake GoForj process when requested.
func TestMain(m *testing.M) {
	if os.Getenv(frameworkResourceHelperEnvironment) == "1" {
		runFrameworkResourceHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestObserveUsesExactProcessContext verifies direct argument, directory, and environment propagation.
func TestObserveUsesExactProcessContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	checkout := t.TempDir()
	observation, err := Observe(t.Context(), Query{
		Executable: executable,
		Checkout:   checkout,
		Environment: []string{
			frameworkResourceHelperEnvironment + "=1",
			"FRAMEWORK_RESOURCE_HELPER_MODE=valid",
			"FRAMEWORK_RESOURCE_HELPER_CHECKOUT=" + checkout,
			"FRAMEWORK_RESOURCE_HELPER_ADDRESS=127.77.21.16",
		},
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if !observation.Supported || observation.UnsupportedReason != "" || len(observation.Problems) != 0 {
		t.Fatalf("observation capability = %#v", observation)
	}
	want := []Resource{{
		ID:          "app",
		Name:        "App",
		Kind:        "app",
		URL:         "http://127.77.21.16:3000",
		Description: "Primary local app URL.",
		App:         "app",
		Runtime:     "http",
		Health:      "http://127.77.21.16:3000/health",
		Owner:       "goforj",
	}}
	if !reflect.DeepEqual(observation.Resources, want) {
		t.Fatalf("Observe() resources = %#v, want %#v", observation.Resources, want)
	}
}

// TestObserveTreatsOlderGoForjAsOptional verifies both missing command surfaces degrade to empty enrichment.
func TestObserveTreatsOlderGoForjAsOptional(t *testing.T) {
	for _, mode := range []string{"unknown-command", "unknown-flag"} {
		t.Run(mode, func(t *testing.T) {
			observation, err := observeHelper(t, mode, t.Context())
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			assertUnsupportedObservation(t, observation, UnsupportedCommand)
		})
	}
}

// TestObserveRejectsOrdinaryCommandFailures prevents real execution failures from masquerading as compatibility.
func TestObserveRejectsOrdinaryCommandFailures(t *testing.T) {
	observation, err := observeHelper(t, "failure", t.Context())
	if err == nil || !strings.Contains(err.Error(), "resource registry failed") {
		t.Fatalf("Observe() = (%#v, %v), want bounded command failure", observation, err)
	}
	if strings.Contains(err.Error(), frameworkResourceHelperEnvironment) {
		t.Fatalf("Observe() error reflected environment: %v", err)
	}
}

// TestObserveBoundsProcessOutput verifies a child cannot grow the report buffer without limit.
func TestObserveBoundsProcessOutput(t *testing.T) {
	_, err := observeHelper(t, "oversize", t.Context())
	if !errors.Is(err, ErrReportTooLarge) {
		t.Fatalf("Observe() error = %v, want ErrReportTooLarge", err)
	}
}

// TestObserveHonorsCallerCancellation verifies cancellation interrupts the direct process promptly.
func TestObserveHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := observeHelper(t, "sleep", ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want caller deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Observe() cancellation took %s", elapsed)
	}
}

// TestObserveEnforcesItsOwnTimeout distinguishes Harbor's bounded window from caller cancellation.
func TestObserveEnforcesItsOwnTimeout(t *testing.T) {
	query := helperQuery(t, "sleep")
	started := time.Now()
	_, err := observe(t.Context(), query, 40*time.Millisecond)
	if !errors.Is(err, ErrObservationTimedOut) {
		t.Fatalf("observe() error = %v, want ErrObservationTimedOut", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("observe() timeout took %s", elapsed)
	}
}

// TestObserveRejectsInvalidQueries verifies execution identity cannot fall back to PATH or an ambient checkout.
func TestObserveRejectsInvalidQueries(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	checkout := t.TempDir()
	for _, test := range []struct {
		name  string
		query Query
	}{
		{name: "empty executable", query: Query{Checkout: checkout}},
		{name: "PATH executable", query: Query{Executable: "forj", Checkout: checkout}},
		{name: "empty checkout", query: Query{Executable: executable}},
		{name: "relative checkout", query: Query{Executable: executable, Checkout: "."}},
		{name: "missing environment separator", query: Query{Executable: executable, Checkout: checkout, Environment: []string{"VALUE"}}},
		{name: "NUL environment", query: Query{Executable: executable, Checkout: checkout, Environment: []string{"VALUE=bad\x00value"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Observe(t.Context(), test.query); err == nil {
				t.Fatal("Observe() error = nil")
			}
		})
	}
}

// TestDecodeReportProjectsLaunchableResources verifies filtering, deterministic order, ownership, and diagnostics.
func TestDecodeReportProjectsLaunchableResources(t *testing.T) {
	payload := []byte(`{
		"schema_version":1,
		"supported":true,
		"problem":"project status is partial",
		"resource_problem":"one optional resolver is unavailable",
		"project":"demo",
		"services":[],
		"resources":[
			{"id":"mailpit","name":"Mailpit","kind":"mail","url":"http://127.77.20.16:8025","description":"Local inbox.","service":"mailpit","runtime":"http","owner":"goforj"},
			{"id":"database","name":"Database","kind":"database","service":"mysql","owner":"goforj"},
			{"id":"app","name":"App","kind":"app","url":"https://demo.test","description":"Primary app.","app":"app","runtime":"http","health":"https://demo.test/health","owner":"goforj"}
		]
	}`)
	observation, err := decodeReport(payload)
	if err != nil {
		t.Fatalf("decodeReport() error = %v", err)
	}
	if !observation.Supported || len(observation.Resources) != 2 {
		t.Fatalf("decodeReport() = %#v", observation)
	}
	if observation.Resources[0].ID != "app" || observation.Resources[1].ID != "mailpit" {
		t.Fatalf("resource order = %#v", observation.Resources)
	}
	wantProblems := []Problem{
		{Code: ProblemStatus, Message: "project status is partial"},
		{Code: ProblemResources, Message: "one optional resolver is unavailable"},
	}
	if !reflect.DeepEqual(observation.Problems, wantProblems) {
		t.Fatalf("problems = %#v, want %#v", observation.Problems, wantProblems)
	}
}

// TestDecodeReportRecognizesMissingAdditiveCapability verifies older schema-v1 status output remains compatible.
func TestDecodeReportRecognizesMissingAdditiveCapability(t *testing.T) {
	for _, payload := range []string{
		`{"schema_version":1,"supported":true,"services":[]}`,
		`{"schema_version":1,"supported":true,"services":[],"resources":null}`,
		`{"schema_version":1,"supported":false,"problem":"unsupported","services":[],"resources":[]}`,
	} {
		observation, err := decodeReport([]byte(payload))
		if err != nil {
			t.Fatalf("decodeReport(%s) error = %v", payload, err)
		}
		assertUnsupportedObservation(t, observation, UnsupportedReport)
	}
}

// TestDecodeReportRejectsInvalidDocuments verifies the schema and secret-free allowlist at the process boundary.
func TestDecodeReportRejectsInvalidDocuments(t *testing.T) {
	tooManyResources := make([]wireResource, maximumResources+1)
	tooManyReport := struct {
		SchemaVersion int            `json:"schema_version"`
		Supported     bool           `json:"supported"`
		Services      []any          `json:"services"`
		Resources     []wireResource `json:"resources"`
	}{SchemaVersion: 1, Supported: true, Services: []any{}, Resources: tooManyResources}
	tooManyPayload, err := json.Marshal(tooManyReport)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	valid := `"schema_version":1,"supported":true,"services":[],"resources":[]`
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "empty", payload: ``},
		{name: "malformed", payload: `{`},
		{name: "multiple documents", payload: `{` + valid + `}{` + valid + `}`},
		{name: "missing schema", payload: `{"supported":true,"services":[],"resources":[]}`},
		{name: "future schema", payload: `{"schema_version":2,"supported":true,"services":[],"resources":[]}`},
		{name: "missing supported", payload: `{"schema_version":1,"services":[],"resources":[]}`},
		{name: "missing services", payload: `{"schema_version":1,"supported":true,"resources":[]}`},
		{name: "null services", payload: `{"schema_version":1,"supported":true,"services":null,"resources":[]}`},
		{name: "populated services", payload: `{"schema_version":1,"supported":true,"services":[{}],"resources":[]}`},
		{name: "unknown report field", payload: `{"schema_version":1,"supported":true,"services":[],"resources":[],"credentials":"secret"}`},
		{name: "unknown resource field", payload: `{"schema_version":1,"supported":true,"services":[],"resources":[{"id":"app","name":"App","kind":"app","url":"http://localhost:3000","app":"app","auth":"secret"}]}`},
		{name: "unsafe project", payload: `{"schema_version":1,"supported":true,"project":"bad\nproject","services":[],"resources":[]}`},
		{name: "oversized problem", payload: `{"schema_version":1,"supported":true,"resource_problem":"` + strings.Repeat("x", maximumProblemBytes+1) + `","services":[],"resources":[]}`},
		{name: "too many resources", payload: string(tooManyPayload)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeReport([]byte(test.payload)); err == nil {
				t.Fatal("decodeReport() error = nil")
			}
		})
	}
}

// TestDecodeReportRejectsUnsafeLaunchableResources verifies every copied resource field is bounded and canonical.
func TestDecodeReportRejectsUnsafeLaunchableResources(t *testing.T) {
	base := wireResource{
		ID:      "app",
		Name:    "App",
		Kind:    "app",
		URL:     "http://localhost:3000",
		App:     "app",
		Runtime: "http",
		Owner:   "goforj",
	}
	tests := []struct {
		name   string
		mutate func(*wireResource)
	}{
		{name: "missing ID", mutate: func(resource *wireResource) { resource.ID = "" }},
		{name: "unsafe ID", mutate: func(resource *wireResource) { resource.ID = "app/id" }},
		{name: "unsafe name", mutate: func(resource *wireResource) { resource.Name = "App\nsecret" }},
		{name: "missing kind", mutate: func(resource *wireResource) { resource.Kind = "" }},
		{name: "relative URL", mutate: func(resource *wireResource) { resource.URL = "/app" }},
		{name: "unsupported URL", mutate: func(resource *wireResource) { resource.URL = "file:///tmp/app" }},
		{name: "credential URL", mutate: func(resource *wireResource) { resource.URL = "http://user:password@localhost:3000" }},
		{name: "oversized URL", mutate: func(resource *wireResource) {
			resource.URL = "http://localhost/" + strings.Repeat("x", maximumURLBytes)
		}},
		{name: "missing owner", mutate: func(resource *wireResource) { resource.App = "" }},
		{name: "dual owner", mutate: func(resource *wireResource) { resource.Service = "web" }},
		{name: "unsafe App", mutate: func(resource *wireResource) { resource.App = "bad App" }},
		{name: "unsafe runtime", mutate: func(resource *wireResource) { resource.Runtime = "bad runtime" }},
		{name: "unsafe owner", mutate: func(resource *wireResource) { resource.Owner = "bad owner" }},
		{name: "unsafe health", mutate: func(resource *wireResource) { resource.Health = "ws://localhost/socket" }},
		{name: "unsafe description", mutate: func(resource *wireResource) { resource.Description = "line one\nline two" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := base
			test.mutate(&resource)
			payload := marshalWireReport(t, []wireResource{resource})
			if _, err := decodeReport(payload); err == nil {
				t.Fatal("decodeReport() error = nil")
			}
		})
	}

	duplicate := marshalWireReport(t, []wireResource{base, base})
	if _, err := decodeReport(duplicate); err == nil {
		t.Fatal("decodeReport() duplicate error = nil")
	}
}

// TestDecodeReportIgnoresNonURLResources verifies logical infrastructure never becomes launchable UI content by inference.
func TestDecodeReportIgnoresNonURLResources(t *testing.T) {
	payload := marshalWireReport(t, []wireResource{
		{ID: "database", Name: "Database", Kind: "database", Service: "mysql"},
		{ID: "queue", Name: "Queue", Kind: "queue", App: "jobs"},
	})
	observation, err := decodeReport(payload)
	if err != nil {
		t.Fatalf("decodeReport() error = %v", err)
	}
	if observation.Resources == nil || len(observation.Resources) != 0 {
		t.Fatalf("resources = %#v, want non-nil empty", observation.Resources)
	}
}

// observeHelper invokes this package's test executable through the production runner.
func observeHelper(t *testing.T, mode string, ctx context.Context) (Observation, error) {
	t.Helper()
	return Observe(ctx, helperQuery(t, mode))
}

// helperQuery returns one exact fake-GoForj invocation rooted in a temporary checkout.
func helperQuery(t *testing.T, mode string) Query {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	checkout := t.TempDir()
	return Query{
		Executable: executable,
		Checkout:   checkout,
		Environment: []string{
			frameworkResourceHelperEnvironment + "=1",
			"FRAMEWORK_RESOURCE_HELPER_MODE=" + mode,
			"FRAMEWORK_RESOURCE_HELPER_CHECKOUT=" + checkout,
		},
	}
}

// assertUnsupportedObservation verifies compatibility fallback is typed, empty, and non-nil.
func assertUnsupportedObservation(t *testing.T, observation Observation, reason UnsupportedReason) {
	t.Helper()
	if observation.Supported || observation.UnsupportedReason != reason || observation.Resources == nil || len(observation.Resources) != 0 || observation.Problems == nil || len(observation.Problems) != 0 {
		t.Fatalf("unsupported observation = %#v, want reason %q and initialized empty slices", observation, reason)
	}
}

// marshalWireReport produces a valid schema-v1 resources-only envelope for decoder tests.
func marshalWireReport(t *testing.T, resources []wireResource) []byte {
	t.Helper()
	supported := true
	schema := frameworkResourceSchemaVersion
	services := make([]json.RawMessage, 0)
	report := wireReport{
		SchemaVersion: &schema,
		Supported:     &supported,
		Services:      &services,
		Resources:     &resources,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return payload
}

// runFrameworkResourceHelper emulates only the exact machine contract used by Observe.
func runFrameworkResourceHelper() {
	if !reflect.DeepEqual(os.Args[1:], []string{"dev:status", "--json", "--resources-only"}) {
		_, _ = fmt.Fprintf(os.Stderr, "unexpected arguments: %#v", os.Args[1:])
		os.Exit(90)
	}
	checkout, err := os.Getwd()
	if err != nil || filepath.Clean(checkout) != filepath.Clean(os.Getenv("FRAMEWORK_RESOURCE_HELPER_CHECKOUT")) {
		_, _ = fmt.Fprintf(os.Stderr, "unexpected checkout: %q", checkout)
		os.Exit(91)
	}
	switch os.Getenv("FRAMEWORK_RESOURCE_HELPER_MODE") {
	case "valid":
		address := os.Getenv("FRAMEWORK_RESOURCE_HELPER_ADDRESS")
		_, _ = fmt.Fprintf(os.Stdout, `{"schema_version":1,"supported":true,"services":[],"resources":[{"id":"app","name":"App","kind":"app","url":"http://%s:3000","description":"Primary local app URL.","app":"app","runtime":"http","health":"http://%s:3000/health","owner":"goforj"}]}`+"\n", address, address)
	case "unknown-command":
		_, _ = fmt.Fprintln(os.Stderr, "forj: error: unexpected argument dev:status")
		os.Exit(2)
	case "unknown-flag":
		_, _ = fmt.Fprintln(os.Stderr, "forj: error: unknown flag --resources-only")
		os.Exit(2)
	case "failure":
		_, _ = fmt.Fprintln(os.Stderr, "resource registry failed")
		os.Exit(23)
	case "oversize":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", maximumReportBytes+1024))
	case "sleep":
		for {
			time.Sleep(time.Second)
		}
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(92)
	}
}
