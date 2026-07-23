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

// TestFatalDiagnosticRedactsErrorDetails verifies host diagnostics expose only permitted error details.
func TestFatalDiagnosticRedactsErrorDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantPhase  string
		wantDetail string
	}{
		{
			name:       "configuration",
			err:        errors.New("launchd relay HTTP upstream must be a canonical high 127.0.0.1 socket: 127.0.0.1:12345"),
			wantPhase:  "configuration",
			wantDetail: "configuration-rejected",
		},
		{
			name:       "identity",
			err:        errors.New("determine launchd relay identity: uid unavailable"),
			wantPhase:  "identity",
			wantDetail: "identity-rejected",
		},
		{
			name:       "service identity",
			err:        errors.New("validate launchd relay service identity: foreign service"),
			wantPhase:  "service-identity",
			wantDetail: "service-identity-rejected",
		},
		{
			name:       "runtime construction",
			err:        errors.New("construct launchd ingress relay for policy secret-fingerprint: unavailable"),
			wantPhase:  "runtime-construction",
			wantDetail: "runtime-construction-failed",
		},
		{
			name:       "socket activation",
			err:        errors.New("activate launchd ingress sockets: descriptor 7"),
			wantPhase:  "socket-activation",
			wantDetail: "activate launchd ingress sockets: descriptor 7",
		},
		{
			name:       "environment cleanup",
			err:        errors.New("clear launchd relay ambient environment: inherited-value"),
			wantPhase:  "environment-cleanup",
			wantDetail: "environment-cleanup-failed",
		},
		{
			name:       "relay service",
			err:        errors.New("serve launchd ingress relay: upstream failed"),
			wantPhase:  "relay-service",
			wantDetail: "serve launchd ingress relay: upstream failed",
		},
		{
			name:       "shutdown",
			err:        context.Canceled,
			wantPhase:  "shutdown",
			wantDetail: "context-terminated",
		},
		{
			name:       "unknown",
			err:        errors.New("secret-failure-detail"),
			wantPhase:  "unknown",
			wantDetail: "unclassified-failure",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fatalDiagnostic(test.err)
			if got.phase != test.wantPhase {
				t.Fatalf("fatalDiagnostic(%v).phase = %q, want %q", test.err, got.phase, test.wantPhase)
			}
			if got.detail != test.wantDetail {
				t.Fatalf("fatalDiagnostic(%v).detail = %q, want %q", test.err, got.detail, test.wantDetail)
			}
		})
	}
}

// TestBoundedSingleLinePreventsDiagnosticFieldInjection verifies public error details stay bounded to one line.
func TestBoundedSingleLinePreventsDiagnosticFieldInjection(t *testing.T) {
	long := strings.Repeat("a", maximumFatalDiagnosticDetailBytes+12)
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "control characters",
			value: "socket\nactivation\rfailed\t\x00",
			want:  "socket activation failed",
		},
		{
			name:  "blank",
			value: "\n\t\r",
			want:  "unavailable",
		},
		{
			name:  "truncated",
			value: long,
			want:  strings.Repeat("a", maximumFatalDiagnosticDetailBytes-3) + "...",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedSingleLine(test.value); got != test.want {
				t.Fatalf("boundedSingleLine(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// TestClearAmbientEnvironmentRemovesEverySetting verifies activation-only context cannot reach the relay runtime.
func TestClearAmbientEnvironmentRemovesEverySetting(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
	}{
		{
			name: "preserves exact service name",
			environment: map[string]string{
				launchdServiceNameEnvironment: launchdRelayServiceName,
				"HARBOR_INSTALLATION_ID":      "installation-123",
				"UNRELATED_ENVIRONMENT":       "remove-me",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := maps.Clone(test.environment)
			err := clearAmbientEnvironment(commandTestEnvironmentDependencies(environment))
			if err != nil {
				t.Fatalf("clearAmbientEnvironment() error = %v", err)
			}
			if _, ok := environment[launchdServiceNameEnvironment]; ok {
				t.Fatal("XPC_SERVICE_NAME remained after clearing environment")
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

// TestValidateLaunchdServiceNameRejectsUntrustedIdentity verifies identity is accepted only before activation.
func TestValidateLaunchdServiceNameRejectsUntrustedIdentity(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		wantError   bool
	}{
		{
			name:        "exact service name",
			environment: map[string]string{launchdServiceNameEnvironment: launchdRelayServiceName},
		},
		{
			name:        "foreign service name",
			environment: map[string]string{launchdServiceNameEnvironment: "com.example.foreign"},
			wantError:   true,
		},
		{
			name:        "missing service name",
			environment: map[string]string{},
			wantError:   true,
		},
		{
			name:        "blank service name",
			environment: map[string]string{launchdServiceNameEnvironment: ""},
			wantError:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceName, err := captureLaunchdServiceName(commandTestEnvironmentDependencies(test.environment))
			if err != nil {
				t.Fatalf("captureLaunchdServiceName() error = %v", err)
			}
			err = validateLaunchdServiceName(serviceName)
			if (err != nil) != test.wantError {
				t.Fatalf("validateLaunchdServiceName(%q) error = %v, want error %t", serviceName, err, test.wantError)
			}
			if !test.wantError && serviceName != launchdRelayServiceName {
				t.Fatalf("service name = %q, want %q", serviceName, launchdRelayServiceName)
			}
		})
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
			},
		},
		{
			name: "missing clearenv",
			dependencies: environmentDependencies{
				getenv: func(string) string { return "" },
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
	onServe   func()
}

// Serve captures listener ownership without starting network goroutines.
func (runtime *commandTestRuntime) Serve(ctx context.Context, listeners ingressrelay.Listeners) error {
	if runtime.onServe != nil {
		runtime.onServe()
	}
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

// TestRunCapturesServiceNameBeforeActivation verifies activation can consume identity before cleanup and serving.
func TestRunCapturesServiceNameBeforeActivation(t *testing.T) {
	http := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")}
	https := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443")}
	events := make([]string, 0, 4)
	environment := map[string]string{
		launchdServiceNameEnvironment: launchdRelayServiceName,
		"LAUNCHD_ACTIVATION_STATE":    "available-until-activation",
	}
	runtime := &commandTestRuntime{onServe: func() {
		events = append(events, "serve")
		if len(environment) != 0 {
			t.Fatalf("runtime environment = %#v, want empty", environment)
		}
	}}
	var receivedConfig ingressrelay.Config
	dependencies := runtimeDependencies{
		effectiveUID: func() (uint32, error) { return 501, nil },
		captureServiceName: func() (string, error) {
			events = append(events, "capture")
			return captureLaunchdServiceName(commandTestEnvironmentDependencies(environment))
		},
		activateIngress: func() (ingressrelay.Listeners, error) {
			events = append(events, "activate")
			if environment[launchdServiceNameEnvironment] != launchdRelayServiceName || environment["LAUNCHD_ACTIVATION_STATE"] != "available-until-activation" {
				t.Fatalf("activation context = %#v, want complete launchd environment", environment)
			}
			delete(environment, launchdServiceNameEnvironment)
			return ingressrelay.Listeners{
				HTTP:  http,
				HTTPS: https,
			}, nil
		},
		clearAmbientEnvironment: func() error {
			events = append(events, "clear")
			return clearAmbientEnvironment(commandTestEnvironmentDependencies(environment))
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
	if got := strings.Join(events, ","); got != "capture,activate,clear,serve" {
		t.Fatalf("startup events = %q, want capture,activate,clear,serve", got)
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
				effectiveUID:       func() (uint32, error) { return test.uid, test.err },
				captureServiceName: func() (string, error) { return launchdRelayServiceName, nil },
				activateIngress: func() (ingressrelay.Listeners, error) {
					capabilityCalls++
					return ingressrelay.Listeners{}, nil
				},
				clearAmbientEnvironment: func() error { return nil },
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

// TestRunRejectsServiceIdentityBeforeCapabilityAcquisition verifies a foreign activation context fails closed.
func TestRunRejectsServiceIdentityBeforeCapabilityAcquisition(t *testing.T) {
	capabilityCalls := 0
	dependencies := validCommandTestDependencies(&commandTestRuntime{})
	dependencies.captureServiceName = func() (string, error) { return "com.example.foreign", nil }
	dependencies.activateIngress = func() (ingressrelay.Listeners, error) {
		capabilityCalls++
		return ingressrelay.Listeners{}, nil
	}

	err := run(context.Background(), validCommandTestArguments(), dependencies)
	if err == nil || !strings.Contains(err.Error(), "launchd service identity does not match Harbor's relay") {
		t.Fatalf("run() error = %v, want foreign identity failure", err)
	}
	if capabilityCalls != 0 {
		t.Fatalf("capability calls = %d, want 0", capabilityCalls)
	}
}

// TestRunStopsWhenServiceNameCaptureFails verifies no privileged capability is used after capture failure.
func TestRunStopsWhenServiceNameCaptureFails(t *testing.T) {
	captureErr := errors.New("service name unavailable")
	runtime := &commandTestRuntime{}
	newRuntimeCalls := 0
	activationCalls := 0
	cleanupCalls := 0
	dependencies := validCommandTestDependencies(runtime)
	dependencies.captureServiceName = func() (string, error) { return "", captureErr }
	dependencies.newRuntime = func(ingressrelay.Config) (relayRuntime, error) {
		newRuntimeCalls++
		return runtime, nil
	}
	dependencies.activateIngress = func() (ingressrelay.Listeners, error) {
		activationCalls++
		return ingressrelay.Listeners{}, nil
	}
	dependencies.clearAmbientEnvironment = func() error {
		cleanupCalls++
		return nil
	}

	err := run(context.Background(), validCommandTestArguments(), dependencies)
	if !errors.Is(err, captureErr) || !strings.Contains(err.Error(), "capture launchd relay service identity") {
		t.Fatalf("run() error = %v, want wrapped capture error", err)
	}
	if newRuntimeCalls != 0 || activationCalls != 0 || cleanupCalls != 0 || runtime.calls != 0 {
		t.Fatalf("lifecycle calls = (new=%d, activate=%d, cleanup=%d, serve=%d), want all zero", newRuntimeCalls, activationCalls, cleanupCalls, runtime.calls)
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

	t.Run("environment cleanup failure", func(t *testing.T) {
		http := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")}
		https := &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443"), err: errors.New("HTTPS close failed")}
		runtime := &commandTestRuntime{}
		dependencies := validCommandTestDependencies(runtime)
		dependencies.activateIngress = func() (ingressrelay.Listeners, error) {
			return ingressrelay.Listeners{HTTP: http, HTTPS: https}, nil
		}
		dependencies.clearAmbientEnvironment = func() error { return errors.New("environment cleanup failed") }
		err := run(context.Background(), validCommandTestArguments(), dependencies)
		if err == nil || !strings.Contains(err.Error(), "environment cleanup failed") || !strings.Contains(err.Error(), "HTTPS close failed") {
			t.Fatalf("run() error = %v, want environment cleanup and listener close failures", err)
		}
		if runtime.calls != 0 || http.closed != 1 || https.closed != 1 {
			t.Fatalf("Serve calls and listener closes = (%d, %d, %d), want (0, 1, 1)", runtime.calls, http.closed, https.closed)
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
		{
			captureServiceName:      func() (string, error) { return launchdRelayServiceName, nil },
			activateIngress:         func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil },
			clearAmbientEnvironment: func() error { return nil },
			newRuntime:              func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil },
		},
		{
			effectiveUID:            func() (uint32, error) { return 501, nil },
			activateIngress:         func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil },
			clearAmbientEnvironment: func() error { return nil },
			newRuntime:              func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil },
		},
		{
			effectiveUID:            func() (uint32, error) { return 501, nil },
			captureServiceName:      func() (string, error) { return launchdRelayServiceName, nil },
			clearAmbientEnvironment: func() error { return nil },
			newRuntime:              func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil },
		},
		{
			effectiveUID:       func() (uint32, error) { return 501, nil },
			captureServiceName: func() (string, error) { return launchdRelayServiceName, nil },
			activateIngress:    func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil },
			newRuntime:         func(ingressrelay.Config) (relayRuntime, error) { return &commandTestRuntime{}, nil },
		},
		{
			effectiveUID:            func() (uint32, error) { return 501, nil },
			captureServiceName:      func() (string, error) { return launchdRelayServiceName, nil },
			activateIngress:         func() (ingressrelay.Listeners, error) { return ingressrelay.Listeners{}, nil },
			clearAmbientEnvironment: func() error { return nil },
		},
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
		effectiveUID:       func() (uint32, error) { return 501, nil },
		captureServiceName: func() (string, error) { return launchdRelayServiceName, nil },
		activateIngress: func() (ingressrelay.Listeners, error) {
			return ingressrelay.Listeners{
				HTTP:  &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:80")},
				HTTPS: &commandTestListener{address: fixedCommandTestAddress("127.0.0.1:443")},
			}, nil
		},
		clearAmbientEnvironment: func() error { return nil },
		newRuntime:              func(ingressrelay.Config) (relayRuntime, error) { return runtime, nil },
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
	}
}

// astPackageName gives malformed package assertions a useful printable value.
func astPackageName(file *ast.File) string {
	if file == nil || file.Name == nil {
		return "<missing>"
	}
	return file.Name.Name
}
