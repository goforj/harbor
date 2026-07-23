package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/network/ingressrelay"
)

const commandTestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestClearAmbientEnvironmentRetainsOnlyLaunchdServiceIdentity verifies socket activation retains its launchd identity.
func TestClearAmbientEnvironmentRetainsOnlyLaunchdServiceIdentity(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		wantService string
		wantPresent bool
		wantError   bool
	}{
		{
			name: "preserves exact service name",
			environment: map[string]string{
				launchdServiceNameEnvironment: launchdRelayServiceName,
				"HARBOR_INSTALLATION_ID":      "installation-123",
				"UNRELATED_ENVIRONMENT":       "remove-me",
			},
			wantService: launchdRelayServiceName,
			wantPresent: true,
		},
		{
			name: "rejects foreign service name",
			environment: map[string]string{
				launchdServiceNameEnvironment: "com.example.foreign",
				"HARBOR_INSTALLATION_ID":      "installation-123",
				"UNRELATED_ENVIRONMENT":       "remove-me",
			},
			wantError: true,
		},
		{
			name: "missing service name",
			environment: map[string]string{
				"HARBOR_INSTALLATION_ID": "installation-123",
				"UNRELATED_ENVIRONMENT":  "remove-me",
			},
			wantError: true,
		},
		{
			name: "blank service name",
			environment: map[string]string{
				launchdServiceNameEnvironment: "",
				"HARBOR_INSTALLATION_ID":      "installation-123",
				"UNRELATED_ENVIRONMENT":       "remove-me",
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := maps.Clone(test.environment)
			err := clearAmbientEnvironment(commandTestEnvironmentDependencies(environment))
			if (err != nil) != test.wantError {
				t.Fatalf("clearAmbientEnvironment() error = %v, want error %t", err, test.wantError)
			}
			gotService, gotPresent := environment[launchdServiceNameEnvironment]
			if gotService != test.wantService || gotPresent != test.wantPresent {
				t.Fatalf("XPC_SERVICE_NAME = (%q, %t), want (%q, %t)", gotService, gotPresent, test.wantService, test.wantPresent)
			}
			if _, ok := environment["HARBOR_INSTALLATION_ID"]; ok {
				t.Fatal("HARBOR_INSTALLATION_ID remained after clearing environment")
			}
			if _, ok := environment["UNRELATED_ENVIRONMENT"]; ok {
				t.Fatal("UNRELATED_ENVIRONMENT remained after clearing environment")
			}
		})
	}
}

// TestClearAmbientEnvironmentReturnsSetFailure verifies service identity restoration failures stop startup.
func TestClearAmbientEnvironmentReturnsSetFailure(t *testing.T) {
	want := errors.New("set environment failed")
	err := clearAmbientEnvironment(environmentDependencies{
		getenv:   func(string) string { return launchdRelayServiceName },
		clearenv: func() {},
		setenv:   func(string, string) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("clearAmbientEnvironment() error = %v, want %v", err, want)
	}
}

// TestValidateEnvironmentDependenciesRequiresEveryDependency verifies environment cleanup fails before incomplete wiring is used.
func TestValidateEnvironmentDependenciesRequiresEveryDependency(t *testing.T) {
	tests := []struct {
		name         string
		dependencies environmentDependencies
	}{
		{
			name: "missing getenv",
			dependencies: environmentDependencies{
				clearenv: func() {},
				setenv:   func(string, string) error { return nil },
			},
		},
		{
			name: "missing clearenv",
			dependencies: environmentDependencies{
				getenv: func(string) string { return "" },
				setenv: func(string, string) error { return nil },
			},
		},
		{
			name: "missing setenv",
			dependencies: environmentDependencies{
				getenv:   func(string) string { return "" },
				clearenv: func() {},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("validateEnvironmentDependencies() did not panic")
				}
			}()
			validateEnvironmentDependencies(test.dependencies)
		})
	}
}

// commandTestListener records cleanup without acquiring an operating-system port.
type commandTestListener struct {
	address net.Addr
	closed  int
	err     error
}

// Accept is unused because command tests replace the serving runtime.
func (*commandTestListener) Accept() (net.Conn, error) {
	return nil, errors.New("test listener does not accept")
}

// Close records capability release for activation-failure tests.
func (listener *commandTestListener) Close() error {
	listener.closed++
	return listener.err
}

// Addr returns the controlled public socket identity.
func (listener *commandTestListener) Addr() net.Addr {
	return listener.address
}

// commandTestRuntime records the immutable configuration and listener pair handed to Serve.
type commandTestRuntime struct {
	context   context.Context
	listeners ingressrelay.Listeners
	serveErr  error
	calls     int
}

// Serve captures listener ownership without starting network goroutines.
func (runtime *commandTestRuntime) Serve(ctx context.Context, listeners ingressrelay.Listeners) error {
	runtime.context = ctx
	runtime.listeners = listeners
	runtime.calls++
	return runtime.serveErr
}

// TestParseConfigurationAcceptsExactOwnedArguments verifies the canonical root-plist argument contract.
func TestParseConfigurationAcceptsExactOwnedArguments(t *testing.T) {
	arguments := validCommandTestArguments()
	config, err := parseConfiguration(arguments)
	if err != nil {
		t.Fatalf("parseConfiguration() error = %v", err)
	}
	if config.ownerUID != 501 || config.policyFingerprint != commandTestFingerprint {
		t.Fatalf("identity = (%d, %q), want (501, fingerprint)", config.ownerUID, config.policyFingerprint)
	}
	if config.httpUpstream != netip.MustParseAddrPort("127.0.0.1:18080") ||
		config.httpsUpstream != netip.MustParseAddrPort("127.0.0.1:18443") {
		t.Fatalf("upstreams = (%s, %s), want fixed high localhost sockets", config.httpUpstream, config.httpsUpstream)
	}
}

// TestParseConfigurationRejectsUnownedShapes covers argument order and every security-sensitive value boundary.
func TestParseConfigurationRejectsUnownedShapes(t *testing.T) {
	tests := []struct {
		name      string
		arguments func() []string
		want      string
	}{
		{name: "missing argument", arguments: func() []string { return validCommandTestArguments()[:7] }, want: "exact owned argument vector"},
		{name: "extra argument", arguments: func() []string { return append(validCommandTestArguments(), "extra") }, want: "exact owned argument vector"},
		{name: "reordered flag", arguments: mutateCommandTestArgument(0, "--http-upstream"), want: "exact owned argument vector"},
		{name: "root owner", arguments: mutateCommandTestArgument(1, "0"), want: "canonical non-root uint32"},
		{name: "signed owner", arguments: mutateCommandTestArgument(1, "+501"), want: "canonical non-root uint32"},
		{name: "padded owner", arguments: mutateCommandTestArgument(1, "0501"), want: "canonical non-root uint32"},
		{name: "owner overflow", arguments: mutateCommandTestArgument(1, "4294967296"), want: "canonical non-root uint32"},
		{name: "short fingerprint", arguments: mutateCommandTestArgument(3, strings.Repeat("a", 63)), want: "64 lowercase hexadecimal"},
		{name: "uppercase fingerprint", arguments: mutateCommandTestArgument(3, strings.ToUpper(commandTestFingerprint)), want: "64 lowercase hexadecimal"},
		{name: "non-hex fingerprint", arguments: mutateCommandTestArgument(3, strings.Repeat("z", 64)), want: "64 lowercase hexadecimal"},
		{name: "HTTP hostname", arguments: mutateCommandTestArgument(5, "localhost:18080"), want: "canonical high 127.0.0.1"},
		{name: "HTTP alternate loopback", arguments: mutateCommandTestArgument(5, "127.0.0.2:18080"), want: "canonical high 127.0.0.1"},
		{name: "HTTP mapped address", arguments: mutateCommandTestArgument(5, "[::ffff:127.0.0.1]:18080"), want: "canonical high 127.0.0.1"},
		{name: "HTTP low port", arguments: mutateCommandTestArgument(5, "127.0.0.1:80"), want: "canonical high 127.0.0.1"},
		{name: "HTTPS invalid", arguments: mutateCommandTestArgument(7, "not-an-endpoint"), want: "canonical high 127.0.0.1"},
		{name: "same upstream", arguments: mutateCommandTestArgument(7, "127.0.0.1:18080"), want: "upstreams must be distinct"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseConfiguration(test.arguments())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseConfiguration() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestRunTransfersOnlyValidatedCapabilities verifies identity, construction, activation, and Serve ordering.
func TestRunTransfersOnlyValidatedCapabilities(t *testing.T) {
	http := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")}
	https := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443")}
	runtime := &commandTestRuntime{}
	var receivedConfig ingressrelay.Config
	dependencies := runtimeDependencies{
		effectiveUID: func() (uint32, error) { return 501, nil },
		activateIngress: func() (ingressrelay.Listeners, error) {
			return ingressrelay.Listeners{HTTP: http, HTTPS: https}, nil
		},
		newRuntime: func(config ingressrelay.Config) (relayRuntime, error) {
			receivedConfig = config
			return runtime, nil
		},
	}
	ctx := context.Background()

	if err := run(ctx, validCommandTestArguments(), dependencies); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if receivedConfig.HTTPUpstream != netip.MustParseAddrPort("127.0.0.1:18080") ||
		receivedConfig.HTTPSUpstream != netip.MustParseAddrPort("127.0.0.1:18443") {
		t.Fatalf("runtime config = %#v, want validated upstreams", receivedConfig)
	}
	if runtime.calls != 1 || runtime.context != ctx || runtime.listeners.HTTP != http || runtime.listeners.HTTPS != https {
		t.Fatalf("Serve capture = %#v, want exact context and listener pair", runtime)
	}
	if http.closed != 0 || https.closed != 0 {
		t.Fatalf("command closed transferred listeners: HTTP=%d HTTPS=%d", http.closed, https.closed)
	}
}

// TestRunRejectsIdentityBeforeCapabilityAcquisition proves the plist owner cannot be bypassed or executed as root.
func TestRunRejectsIdentityBeforeCapabilityAcquisition(t *testing.T) {
	tests := []struct {
		name string
		uid  uint32
		err  error
		want string
	}{
		{name: "identity lookup failure", err: errors.New("uid unavailable"), want: "uid unavailable"},
		{name: "root", uid: 0, want: "does not match non-root owner UID"},
		{name: "wrong owner", uid: 502, want: "does not match non-root owner UID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilityCalls := 0
			dependencies := runtimeDependencies{
				effectiveUID: func() (uint32, error) { return test.uid, test.err },
				activateIngress: func() (ingressrelay.Listeners, error) {
					capabilityCalls++
					return ingressrelay.Listeners{}, nil
				},
				newRuntime: func(ingressrelay.Config) (relayRuntime, error) {
					capabilityCalls++
					return &commandTestRuntime{}, nil
				},
			}
			err := run(context.Background(), validCommandTestArguments(), dependencies)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run() error = %v, want substring %q", err, test.want)
			}
			if capabilityCalls != 0 {
				t.Fatalf("capability calls = %d, want 0", capabilityCalls)
			}
		})
	}
}

// TestRunCleansPartialActivationAndPropagatesRuntimeFailures verifies every pre-transfer failure is bounded.
func TestRunCleansPartialActivationAndPropagatesRuntimeFailures(t *testing.T) {
	t.Run("constructor failure", func(t *testing.T) {
		activationCalls := 0
		dependencies := validCommandTestDependencies(&commandTestRuntime{})
		dependencies.newRuntime = func(ingressrelay.Config) (relayRuntime, error) {
			return nil, errors.New("constructor failed")
		}
		dependencies.activateIngress = func() (ingressrelay.Listeners, error) {
			activationCalls++
			return ingressrelay.Listeners{}, nil
		}
		err := run(context.Background(), validCommandTestArguments(), dependencies)
		if err == nil || !strings.Contains(err.Error(), "constructor failed") || activationCalls != 0 {
			t.Fatalf("run() = %v with %d activations, want constructor failure before activation", err, activationCalls)
		}
	})

	t.Run("nil runtime", func(t *testing.T) {
		dependencies := validCommandTestDependencies(&commandTestRuntime{})
		dependencies.newRuntime = func(ingressrelay.Config) (relayRuntime, error) { return nil, nil }
		err := run(context.Background(), validCommandTestArguments(), dependencies)
		if err == nil || !strings.Contains(err.Error(), "runtime is missing") {
			t.Fatalf("run() error = %v, want missing runtime", err)
		}
	})

	t.Run("partial activation", func(t *testing.T) {
		http := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")}
		https := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443"), err: errors.New("HTTPS close failed")}
		dependencies := validCommandTestDependencies(&commandTestRuntime{})
		dependencies.activateIngress = func() (ingressrelay.Listeners, error) {
			return ingressrelay.Listeners{HTTP: http, HTTPS: https}, errors.New("activation failed")
		}
		err := run(context.Background(), validCommandTestArguments(), dependencies)
		if err == nil || !strings.Contains(err.Error(), "activation failed") || !strings.Contains(err.Error(), "HTTPS close failed") {
			t.Fatalf("run() error = %v, want activation and cleanup failures", err)
		}
		if http.closed != 1 || https.closed != 1 {
			t.Fatalf("listener closes = (%d, %d), want (1, 1)", http.closed, https.closed)
		}
	})

	t.Run("serve failure", func(t *testing.T) {
		runtime := &commandTestRuntime{serveErr: errors.New("relay failed")}
		err := run(context.Background(), validCommandTestArguments(), validCommandTestDependencies(runtime))
		if err == nil || !strings.Contains(err.Error(), "serve launchd ingress relay: relay failed") {
			t.Fatalf("run() error = %v, want wrapped Serve failure", err)
		}
	})

	t.Run("pre-canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		identityCalls := 0
		dependencies := validCommandTestDependencies(&commandTestRuntime{})
		dependencies.effectiveUID = func() (uint32, error) {
			identityCalls++
			return 501, nil
		}
		if err := run(ctx, validCommandTestArguments(), dependencies); !errors.Is(err, context.Canceled) {
			t.Fatalf("run() error = %v, want context.Canceled", err)
		}
		if identityCalls != 0 {
			t.Fatalf("identity calls = %d, want 0", identityCalls)
		}
	})
}

// TestRunRequiresEveryDependency verifies bad process wiring fails before interpreting arguments.
func TestRunRequiresEveryDependency(t *testing.T) {
	tests := []runtimeDependencies{
		{activateIngress: func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil }, newRuntime: func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil }},
		{effectiveUID: func() (uint32, error) { return 501, nil }, newRuntime: func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil }},
		{effectiveUID: func() (uint32, error) { return 501, nil }, activateIngress: func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil }},
	}
	for index, dependencies := range tests {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("run() did not panic for missing dependency")
				}
			}()
			_ = run(context.Background(), nil, dependencies)
		})
	}
}

// TestCommandDependencyBoundary prevents the privileged relay executable from growing application or Docker dependencies.
func TestCommandDependencyBoundary(t *testing.T) {
	allowed := map[string]struct{}{
		"github.com/goforj/harbor/internal/network/ingressrelay":   {},
		"github.com/goforj/harbor/internal/platform/launchdsocket": {},
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read command source directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s in %s: %v", imported.Path.Value, entry.Name(), err)
			}
			if !strings.HasPrefix(path, "github.com/goforj/harbor/") {
				continue
			}
			if _, ok := allowed[path]; !ok {
				t.Errorf("%s imports forbidden Harbor package %q", entry.Name(), path)
			}
		}
		if file.Name == nil || file.Name.Name != "main" {
			t.Errorf("%s package = %v, want main", entry.Name(), astPackageName(file))
		}
	}
}

// fixedCommandTestAddress provides a stable net.Addr without binding low ports.
type fixedCommandTestAddress string

// Network identifies the controlled address as TCP.
func (fixedCommandTestAddress) Network() string {
	return "tcp"
}

// String returns the exact address consumed by validation paths.
func (address fixedCommandTestAddress) String() string {
	return string(address)
}

// validCommandTestArguments returns one canonical root-owned plist argument vector.
func validCommandTestArguments() []string {
	return []string{
		ownerUIDFlag, "501",
		policyFingerprintFlag, commandTestFingerprint,
		httpUpstreamFlag, "127.0.0.1:18080",
		httpsUpstreamFlag, "127.0.0.1:18443",
	}
}

// mutateCommandTestArgument returns a fresh argument vector with one controlled invalid value.
func mutateCommandTestArgument(index int, value string) func() []string {
	return func() []string {
		arguments := validCommandTestArguments()
		arguments[index] = value
		return arguments
	}
}

// validCommandTestDependencies returns the smallest successful runtime seam.
func validCommandTestDependencies(runtime relayRuntime) runtimeDependencies {
	return runtimeDependencies{
		effectiveUID: func() (uint32, error) { return 501, nil },
		activateIngress: func() (ingressrelay.Listeners, error) {
			return ingressrelay.Listeners{
				HTTP:  &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")},
				HTTPS: &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443")},
			}, nil
		},
		newRuntime: func(ingressrelay.Config) (relayRuntime, error) { return runtime, nil },
	}
}

// commandTestEnvironmentDependencies simulates process environment operations without changing test state outside one case.
func commandTestEnvironmentDependencies(environment map[string]string) environmentDependencies {
	return environmentDependencies{
		getenv: func(key string) string {
			return environment[key]
		},
		clearenv: func() {
			for key := range environment {
				delete(environment, key)
			}
		},
		setenv: func(key string, value string) error {
			environment[key] = value
			return nil
		},
	}
}

// astPackageName gives malformed package assertions a useful printable value.
func astPackageName(file *ast.File) string {
	if file == nil || file.Name == nil {
		return "<missing>"
	}
	return file.Name.Name
}
