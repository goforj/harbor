// Package main provides Harbor's macOS launchd-activated ingress relay.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strconv"

	"github.com/goforj/harbor/internal/network/ingressrelay"
	"github.com/goforj/harbor/internal/platform/launchdsocket"
)

const (
	launchdRelayServiceName       = "com.goforj.harbor.launchdrelay"
	launchdServiceNameEnvironment = "XPC_SERVICE_NAME"
	ownerUIDFlag                  = "--owner-uid"
	policyFingerprintFlag         = "--policy-fingerprint"
	httpUpstreamFlag              = "--http-upstream"
	httpsUpstreamFlag             = "--https-upstream"
	fingerprintLength             = 64
)

// configuration contains only values pinned in the root-owned launchd definition.
type configuration struct {
	ownerUID          uint32
	policyFingerprint string
	httpUpstream      netip.AddrPort
	httpsUpstream     netip.AddrPort
}

// relayRuntime is the fixed paired listener lifecycle used by this process.
type relayRuntime interface {
	// Serve owns both activated listeners until process shutdown.
	Serve(context.Context, ingressrelay.Listeners) error
}

// runtimeDependencies keeps native identity and socket activation deterministic in tests.
type runtimeDependencies struct {
	effectiveUID            func() (uint32, error)
	captureServiceName      func() (string, error)
	activateIngress         func() (ingressrelay.Listeners, error)
	clearAmbientEnvironment func() error
	newRuntime              func(ingressrelay.Config) (relayRuntime, error)
}

// environmentDependencies controls the environment clearing boundary.
type environmentDependencies struct {
	getenv   func(string) string
	clearenv func()
}

// main starts the relay with launchd's activation context available until socket acquisition completes.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	if err := run(ctx, os.Args[1:], productionDependencies()); err != nil {
		reportFatalDiagnostic(fatalDiagnostic(err))
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// productionEnvironmentDependencies binds environment operations to the process environment.
func productionEnvironmentDependencies() environmentDependencies {
	return environmentDependencies{
		getenv:   os.Getenv,
		clearenv: os.Clearenv,
	}
}

// captureLaunchdServiceName records launchd's service identity before activation may consume it.
func captureLaunchdServiceName(dependencies environmentDependencies) (string, error) {
	validateEnvironmentDependencies(dependencies)
	return dependencies.getenv(launchdServiceNameEnvironment), nil
}

// validateLaunchdServiceName rejects activation contexts not owned by Harbor's relay service.
func validateLaunchdServiceName(serviceName string) error {
	if serviceName != launchdRelayServiceName {
		return errors.New("launchd service identity does not match Harbor's relay")
	}
	return nil
}

// clearAmbientEnvironment removes every ambient setting after launchd activation completes.
func clearAmbientEnvironment(dependencies environmentDependencies) error {
	validateEnvironmentDependencies(dependencies)
	dependencies.clearenv()
	return nil
}

// productionDependencies binds the process to its current UID and fixed launchd socket names.
func productionDependencies() runtimeDependencies {
	return runtimeDependencies{
		effectiveUID:            productionEffectiveUID,
		captureServiceName:      func() (string, error) { return captureLaunchdServiceName(productionEnvironmentDependencies()) },
		activateIngress:         launchdsocket.ActivateIngress,
		clearAmbientEnvironment: func() error { return clearAmbientEnvironment(productionEnvironmentDependencies()) },
		newRuntime: func(config ingressrelay.Config) (relayRuntime, error) {
			return ingressrelay.New(config)
		},
	}
}

// run verifies the launchd-selected owner before acquiring either low-port capability.
func run(ctx context.Context, arguments []string, dependencies runtimeDependencies) error {
	validateDependencies(dependencies)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	config, err := parseConfiguration(arguments)
	if err != nil {
		return err
	}
	uid, err := dependencies.effectiveUID()
	if err != nil {
		return fmt.Errorf("determine launchd relay identity: %w", err)
	}
	if uid == 0 || uid != config.ownerUID {
		return fmt.Errorf("launchd relay effective UID %d does not match non-root owner UID %d", uid, config.ownerUID)
	}
	serviceName, err := dependencies.captureServiceName()
	if err != nil {
		return fmt.Errorf("capture launchd relay service identity: %w", err)
	}
	if err := validateLaunchdServiceName(serviceName); err != nil {
		return fmt.Errorf("validate launchd relay service identity: %w", err)
	}

	runtime, err := dependencies.newRuntime(ingressrelay.Config{
		HTTPUpstream:  config.httpUpstream,
		HTTPSUpstream: config.httpsUpstream,
	})
	if err != nil {
		return fmt.Errorf("construct launchd ingress relay for policy %s: %w", config.policyFingerprint, err)
	}
	if runtime == nil {
		return errors.New("construct launchd ingress relay: runtime is missing")
	}
	listeners, err := dependencies.activateIngress()
	if err != nil {
		return errors.Join(fmt.Errorf("activate launchd ingress sockets: %w", err), closeListeners(listeners))
	}
	if err := dependencies.clearAmbientEnvironment(); err != nil {
		return errors.Join(fmt.Errorf("clear launchd relay ambient environment: %w", err), closeListeners(listeners))
	}
	if err := runtime.Serve(ctx, listeners); err != nil {
		return fmt.Errorf("serve launchd ingress relay: %w", err)
	}
	return nil
}

// parseConfiguration accepts one exact argument vector emitted by Harbor's owned plist.
func parseConfiguration(arguments []string) (configuration, error) {
	if len(arguments) != 8 ||
		arguments[0] != ownerUIDFlag ||
		arguments[2] != policyFingerprintFlag ||
		arguments[4] != httpUpstreamFlag ||
		arguments[6] != httpsUpstreamFlag {
		return configuration{}, errors.New("launchd relay requires the exact owned argument vector")
	}
	ownerUID, err := parseOwnerUID(arguments[1])
	if err != nil {
		return configuration{}, err
	}
	if err := validateFingerprint(arguments[3]); err != nil {
		return configuration{}, err
	}
	httpUpstream, err := parseUpstream("HTTP", arguments[5])
	if err != nil {
		return configuration{}, err
	}
	httpsUpstream, err := parseUpstream("HTTPS", arguments[7])
	if err != nil {
		return configuration{}, err
	}
	if httpUpstream == httpsUpstream {
		return configuration{}, errors.New("launchd relay upstreams must be distinct")
	}
	return configuration{
		ownerUID:          ownerUID,
		policyFingerprint: arguments[3],
		httpUpstream:      httpUpstream,
		httpsUpstream:     httpsUpstream,
	}, nil
}

// parseOwnerUID requires one canonical non-root unsigned host identity.
func parseOwnerUID(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("launchd relay owner UID must be a canonical non-root uint32")
	}
	return uint32(parsed), nil
}

// validateFingerprint accepts only the canonical policy digest pinned by machine ownership.
func validateFingerprint(value string) error {
	if len(value) != fingerprintLength {
		return errors.New("launchd relay policy fingerprint must contain 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return errors.New("launchd relay policy fingerprint must contain 64 lowercase hexadecimal characters")
	}
	return nil
}

// parseUpstream requires one canonical unprivileged listener on exact localhost.
func parseUpstream(name string, value string) (netip.AddrPort, error) {
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil || endpoint.String() != value || endpoint.Addr() != endpoint.Addr().Unmap() ||
		endpoint.Addr() != netip.MustParseAddr("127.0.0.1") || endpoint.Port() < 1024 {
		return netip.AddrPort{}, fmt.Errorf("launchd relay %s upstream must be a canonical high 127.0.0.1 socket", name)
	}
	return endpoint, nil
}

// closeListeners releases partial activation results that failed before Runtime accepted ownership.
func closeListeners(listeners ingressrelay.Listeners) error {
	var result error
	if listeners.HTTP != nil {
		result = errors.Join(result, listeners.HTTP.Close())
	}
	if listeners.HTTPS != nil {
		result = errors.Join(result, listeners.HTTPS.Close())
	}
	return result
}

// validateDependencies fails fast before process identity or socket authority is consulted.
func validateDependencies(dependencies runtimeDependencies) {
	if dependencies.effectiveUID == nil || dependencies.captureServiceName == nil || dependencies.activateIngress == nil || dependencies.clearAmbientEnvironment == nil || dependencies.newRuntime == nil {
		panic("launchd relay requires every runtime dependency")
	}
}

// validateEnvironmentDependencies fails fast when environment cleanup is not fully wired.
func validateEnvironmentDependencies(dependencies environmentDependencies) {
	if dependencies.getenv == nil || dependencies.clearenv == nil {
		panic("launchd relay requires every environment dependency")
	}
}
