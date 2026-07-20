package goforj

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const descriptorHelperEnvironment = "HARBOR_GOFORJ_DESCRIPTOR_HELPER"

// TestMain turns this package's test binary into a bounded fake GoForj command for subprocess tests.
func TestMain(main *testing.M) {
	if os.Getenv(descriptorHelperEnvironment) == "1" {
		runDescriptorHelper()
		return
	}
	os.Exit(main.Run())
}

// TestObserveAdmitsDeterministicDescriptor verifies the exact command boundary and value-free projection.
func TestObserveAdmitsDeterministicDescriptor(t *testing.T) {
	query := descriptorHelperQuery(t, "valid")
	first, err := Observe(t.Context(), query)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	second, err := Observe(t.Context(), query)
	if err != nil {
		t.Fatalf("second Observe() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("descriptor changed between identical reads\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if first.SchemaVersion != DescriptorSchemaVersion || first.Project.Name != "orders" || first.Project.Module != "example.com/orders" {
		t.Fatalf("descriptor identity = %#v", first)
	}
	if first.TopologyDigest != strings.Repeat("a", 64) || first.Project.ConfigDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("descriptor digest = %#v", first)
	}
	if len(first.Apps) != 1 || len(first.Apps[0].Runtimes) != 1 || first.Apps[0].Runtimes[0].DefaultPort != 3000 {
		t.Fatalf("descriptor Apps = %#v", first.Apps)
	}
}

// TestObserveRejectsUnsafeReports verifies unknown fields, schema drift, and secret-like fields fail closed.
func TestObserveRejectsUnsafeReports(t *testing.T) {
	for _, mode := range []string{"unknown-field", "future-schema", "missing-capability", "invalid-digest", "duplicate-app", "multiple-documents", "secret-field", "unsafe-path"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := Observe(t.Context(), descriptorHelperQuery(t, mode)); err == nil {
				t.Fatal("Observe() error = nil")
			}
		})
	}
}

// TestObserveBoundsOutputAndCancellation verifies bounded child output and caller cancellation.
func TestObserveBoundsOutputAndCancellation(t *testing.T) {
	if _, err := Observe(t.Context(), descriptorHelperQuery(t, "oversize")); !errorsContain(err, ErrReportTooLarge) {
		t.Fatalf("oversize error = %v, want ErrReportTooLarge", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := observe(ctx, descriptorHelperQuery(t, "sleep"), time.Second); err == nil || !errorsContain(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v, want deadline", err)
	}
}

// TestValidateQueryRejectsAmbientlyAmbiguousInputs keeps descriptor execution tied to exact paths and env entries.
func TestValidateQueryRejectsAmbientlyAmbiguousInputs(t *testing.T) {
	for _, query := range []Query{
		{Executable: "forj", Checkout: "/tmp/project"},
		{Executable: "/tmp/forj", Checkout: "."},
		{Executable: "/tmp/forj", Checkout: "/tmp/project", Environment: []string{"INVALID"}},
		{Executable: "/tmp/forj", Checkout: "/tmp/project", Environment: []string{"VALUE=bad\x00value"}},
	} {
		if err := validateQuery(query); err == nil {
			t.Fatalf("validateQuery(%#v) error = nil", query)
		}
	}
}

// descriptorHelperQuery returns one exact fake-GoForj invocation rooted in a temporary checkout.
func descriptorHelperQuery(t *testing.T, mode string) Query {
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
			descriptorHelperEnvironment + "=1",
			"HARBOR_GOFORJ_DESCRIPTOR_MODE=" + mode,
		},
	}
}

// runDescriptorHelper emulates only the exact machine command used by Observe.
func runDescriptorHelper() {
	if !reflect.DeepEqual(os.Args[1:], []string{"project:describe", "--json"}) {
		_, _ = fmt.Fprintf(os.Stderr, "unexpected arguments: %#v", os.Args[1:])
		os.Exit(90)
	}
	if _, err := os.Getwd(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "unexpected checkout")
		os.Exit(91)
	}
	switch os.Getenv("HARBOR_GOFORJ_DESCRIPTOR_MODE") {
	case "valid":
		_, _ = fmt.Fprint(os.Stdout, validDescriptorJSON())
	case "unknown-field":
		_, _ = fmt.Fprint(os.Stdout, strings.TrimSuffix(validDescriptorJSON(), "\n")[:len(strings.TrimSuffix(validDescriptorJSON(), "\n"))-1]+`,"secret":"value"}`+"\n")
	case "secret-field":
		_, _ = fmt.Fprint(os.Stdout, `{"schema_version":1,"project":{"name":"orders","module":"example.com/orders","config_digest":"sha256:`+strings.Repeat("a", 64)+`","credentials":"secret"},"goforj":{},"apps":[]}`+"\n")
	case "future-schema":
		_, _ = fmt.Fprint(os.Stdout, strings.Replace(validDescriptorJSON(), `"schema_version":1`, `"schema_version":2`, 1))
	case "missing-capability":
		_, _ = fmt.Fprint(os.Stdout, strings.Replace(validDescriptorJSON(), `"project-descriptor.v1"`, "", 1))
	case "invalid-digest":
		_, _ = fmt.Fprint(os.Stdout, strings.Replace(validDescriptorJSON(), strings.Repeat("a", 64), "not-a-digest", 1))
	case "unsafe-path":
		_, _ = fmt.Fprint(os.Stdout, strings.Replace(validDescriptorJSON(), "cmd/app/main.go", "cmd/../../secret/main.go", 1))
	case "duplicate-app":
		payload := strings.TrimSuffix(validDescriptorJSON(), "\n")
		payload = strings.Replace(payload, `"apps":[{`, `"apps":[{"id":"app","name":"app","entrypoint":"cmd/app/main.go","runtimes":[]},{`, 1)
		_, _ = fmt.Fprintln(os.Stdout, payload)
	case "multiple-documents":
		_, _ = fmt.Fprint(os.Stdout, validDescriptorJSON()+validDescriptorJSON())
	case "oversize":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", maximumReportBytes+1))
	case "sleep":
		for {
			time.Sleep(time.Second)
		}
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown descriptor helper mode")
		os.Exit(92)
	}
}

// validDescriptorJSON returns a deterministic schema-v1 descriptor with no environment values.
func validDescriptorJSON() string {
	return `{"schema_version":1,"project":{"name":"orders","module":"example.com/orders","config_digest":"sha256:` + strings.Repeat("a", 64) + `"},"goforj":{"version":"v0.20.1","cli_capabilities":["project-descriptor.v1"],"generated_project":{"generation":"v0.20.1","capabilities":[]}},"apps":[{"id":"app","name":"app","entrypoint":"cmd/app/main.go","runtimes":[{"id":"http","kind":"http","default_port":3000,"public_url":true,"readiness_path":"/-/ready"}]}]}` + "\n"
}

// errorsContain keeps the test independent of a concrete wrapped error type.
func errorsContain(err, target error) bool {
	return err != nil && (errors.Is(err, target) || strings.Contains(err.Error(), target.Error()))
}
