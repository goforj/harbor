package trustedhttpsharness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// recordingRunner captures exact curl authority while returning endpoint-bound documents.
type recordingRunner struct {
	commands []Command
	titles   map[string]string
	err      error
}

// Run records one command and derives the selected project from its literal URL.
func (runner *recordingRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	runner.commands = append(runner.commands, command)
	if runner.err != nil {
		return CommandResult{StandardError: []byte("native trust rejected certificate")}, runner.err
	}
	url := command.Arguments[len(command.Arguments)-1]
	domain := strings.TrimSuffix(strings.TrimPrefix(url, "https://"), "/swagger/doc.json")
	title := runner.titles[domain]
	return CommandResult{StandardOutput: []byte(`{"openapi":"3.0.3","info":{"title":"` + title + `"}}`)}, nil
}

// TestProbeUsesOnlyLiteralSystemHTTPS proves no curl option can inject DNS, a port, trust, or TLS bypass.
func TestProbeUsesOnlyLiteralSystemHTTPS(t *testing.T) {
	t.Setenv("HOME", "/Users/harbor-acceptance")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid")
	t.Setenv("CURL_CA_BUNDLE", "/tmp/alternate-ca.pem")
	endpoints := testEndpoints()
	runner := &recordingRunner{titles: map[string]string{
		"orders.test":    "Harbor Orders",
		"billing.test":   "Harbor Billing",
		"inventory.test": "Harbor Inventory",
	}}

	results, err := Probe(t.Context(), runner, endpoints)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if len(results) != 3 || len(runner.commands) != 3 {
		t.Fatalf("results = %#v, commands = %#v", results, runner.commands)
	}
	for index, command := range runner.commands {
		if command.Path != systemCurlPath || command.Arguments[0] != "--disable" {
			t.Fatalf("command %d = %#v", index, command)
		}
		wantURL := "https://" + endpoints[index].Domain + "/swagger/doc.json"
		if command.Arguments[len(command.Arguments)-1] != wantURL {
			t.Fatalf("command %d URL = %q, want %q", index, command.Arguments[len(command.Arguments)-1], wantURL)
		}
		joined := strings.Join(command.Arguments, " ")
		for _, forbidden := range []string{"--resolve", "--connect-to", "--cacert", "--capath", "--insecure", "-k", ":443"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("command %d contains forbidden %q: %#v", index, forbidden, command.Arguments)
			}
		}
		if !reflect.DeepEqual(command.Environment, []string{
			"LANG=C",
			"LC_ALL=C",
			"PATH=/usr/bin:/bin",
			"HOME=/Users/harbor-acceptance",
		}) {
			t.Fatalf("command %d environment = %#v", index, command.Environment)
		}
	}
}

// TestProbeRejectsCrossRoutedAndFailedResponses proves all three exact identities are required.
func TestProbeRejectsCrossRoutedAndFailedResponses(t *testing.T) {
	t.Run("cross routed", func(t *testing.T) {
		runner := &recordingRunner{titles: map[string]string{
			"orders.test":    "Harbor Billing",
			"billing.test":   "Harbor Billing",
			"inventory.test": "Harbor Inventory",
		}}
		_, err := Probe(t.Context(), runner, testEndpoints())
		if err == nil || !strings.Contains(err.Error(), `orders.test returned OpenAPI title "Harbor Billing"`) {
			t.Fatalf("Probe() error = %v", err)
		}
	})

	t.Run("native failure", func(t *testing.T) {
		runner := &recordingRunner{err: errors.New("exit status 60")}
		_, err := Probe(t.Context(), runner, testEndpoints())
		if err == nil || !strings.Contains(err.Error(), "system DNS and trust") || !strings.Contains(err.Error(), "native trust rejected certificate") {
			t.Fatalf("Probe() error = %v", err)
		}
	})
}

// TestValidateEndpointsRequiresThreeDistinctExactNames covers every authority-bearing endpoint constraint.
func TestValidateEndpointsRequiresThreeDistinctExactNames(t *testing.T) {
	valid := testEndpoints()
	tests := []struct {
		name   string
		mutate func([]Endpoint) []Endpoint
	}{
		{name: "too few", mutate: func(endpoints []Endpoint) []Endpoint { return endpoints[:2] }},
		{name: "duplicate domain", mutate: func(endpoints []Endpoint) []Endpoint { endpoints[1].Domain = endpoints[0].Domain; return endpoints }},
		{name: "duplicate title", mutate: func(endpoints []Endpoint) []Endpoint {
			endpoints[1].OpenAPITitle = endpoints[0].OpenAPITitle
			return endpoints
		}},
		{name: "blank title", mutate: func(endpoints []Endpoint) []Endpoint { endpoints[0].OpenAPITitle = " "; return endpoints }},
	}
	for _, domain := range []string{
		"orders.test:443",
		"orders.example",
		"orders.dev.test",
		"Orders.test",
		"-orders.test",
		"orders-.test",
		"orders_.test",
		"orders.test.",
		"*.test",
		"https://orders.test",
	} {
		domain := domain
		tests = append(tests, struct {
			name   string
			mutate func([]Endpoint) []Endpoint
		}{
			name: "invalid domain " + domain,
			mutate: func(endpoints []Endpoint) []Endpoint {
				endpoints[0].Domain = domain
				return endpoints
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := append([]Endpoint(nil), valid...)
			if err := validateEndpoints(test.mutate(candidate)); err == nil {
				t.Fatal("validateEndpoints() error = nil")
			}
		})
	}
}

// TestDecodeOpenAPITitleRejectsMalformedAndAmbiguousDocuments covers the bounded response parser.
func TestDecodeOpenAPITitleRejectsMalformedAndAmbiguousDocuments(t *testing.T) {
	valid := []byte(`{"openapi":"3.0.3","info":{"title":"Harbor Orders"}}`)
	title, err := decodeOpenAPITitle(valid)
	if err != nil || title != "Harbor Orders" {
		t.Fatalf("decodeOpenAPITitle(valid) = %q, %v", title, err)
	}
	for _, body := range [][]byte{
		nil,
		[]byte(`{"info":{"title":"Harbor Orders"}}`),
		[]byte(`{"openapi":"3.0.3","info":{}}`),
		[]byte(`{"openapi":"3.0.3","info":{"title":" Harbor Orders"}}`),
		[]byte(`{"openapi":"3.0.3","info":{"title":"Harbor Orders"}} {}`),
		[]byte(`not-json`),
	} {
		if _, err := decodeOpenAPITitle(body); err == nil {
			t.Fatalf("decodeOpenAPITitle(%q) error = nil", body)
		}
	}
	oversized := make([]byte, maximumProbeOutputBytes+1)
	if _, err := decodeOpenAPITitle(oversized); err == nil {
		t.Fatal("decodeOpenAPITitle(oversized) error = nil")
	}
}

// TestBoundedBufferDetectsOverflowWithoutShortWrites proves child pipes remain drained after the evidence limit.
func TestBoundedBufferDetectsOverflowWithoutShortWrites(t *testing.T) {
	buffer := &boundedBuffer{maximum: 4}
	if written, err := buffer.Write([]byte("abc")); err != nil || written != 3 {
		t.Fatalf("first Write() = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte("def")); err != nil || written != 3 {
		t.Fatalf("second Write() = %d, %v", written, err)
	}
	if string(buffer.Bytes()) != "abcd" || !buffer.overflow {
		t.Fatalf("buffer = %q, overflow = %t", buffer.Bytes(), buffer.overflow)
	}
	if written, err := buffer.Write([]byte("ghi")); err != nil || written != 3 || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("third Write() = %d, %v, buffer = %q", written, err, buffer.Bytes())
	}
}

// testEndpoints returns the exact identity set shared by the probe tests.
func testEndpoints() []Endpoint {
	return []Endpoint{
		{Domain: "orders.test", OpenAPITitle: "Harbor Orders"},
		{Domain: "billing.test", OpenAPITitle: "Harbor Billing"},
		{Domain: "inventory.test", OpenAPITitle: "Harbor Inventory"},
	}
}
