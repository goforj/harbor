package projectdiscovery

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverDefaultRuntimeUsesOnlyTheLocalListener proves public URLs and bind hosts cannot redirect Harbor readiness.
func TestDiscoverDefaultRuntimeUsesOnlyTheLocalListener(t *testing.T) {
	root := runtimeTargetTestProject(t, "APP_URL=https://public.example.test\nAPI_HTTP_HOST=0.0.0.0\nAPI_HTTP_PORT=4317\nSECRET_TOKEN=do-not-retain\n", "API_HTTP_PORT=3000\n")

	target, err := NewDiscoverer().DiscoverDefaultRuntime(t.Context(), root)
	if err != nil {
		t.Fatalf("DiscoverDefaultRuntime() error = %v", err)
	}
	if target.AppID != "app" || target.Name != "App" || target.Port != 4317 {
		t.Fatalf("DiscoverDefaultRuntime() identity = %#v", target)
	}
	if target.Address != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("DiscoverDefaultRuntime() address = %s", target.Address)
	}
	if target.ResourceURL != "http://127.0.0.1:4317" || target.ReadyURL != "http://127.0.0.1:4317/-/ready" {
		t.Fatalf("DiscoverDefaultRuntime() URLs = %#v", target)
	}
	if strings.Contains(target.ResourceURL+target.ReadyURL, "public.example.test") || strings.Contains(target.ResourceURL+target.ReadyURL, "do-not-retain") {
		t.Fatalf("DiscoverDefaultRuntime() retained unrelated project values: %#v", target)
	}
}

// TestDiscoverDefaultRuntimeAtAddressUsesAssignedLoopback proves project configuration cannot replace Harbor's selected identity.
func TestDiscoverDefaultRuntimeAtAddressUsesAssignedLoopback(t *testing.T) {
	root := runtimeTargetTestProject(t, "API_HTTP_HOST=127.0.0.1\nAPI_HTTP_PORT=4317\n", "")
	assigned := netip.MustParseAddr("127.0.0.42")

	target, err := NewDiscoverer().DiscoverDefaultRuntimeAtAddress(t.Context(), root, assigned)
	if err != nil {
		t.Fatalf("DiscoverDefaultRuntimeAtAddress() error = %v", err)
	}
	if target.Address != assigned || target.ResourceURL != "http://127.0.0.42:4317" || target.ReadyURL != "http://127.0.0.42:4317/-/ready" {
		t.Fatalf("DiscoverDefaultRuntimeAtAddress() = %#v", target)
	}
}

// TestDiscoverDefaultRuntimeAppliesGeneratedPortPrecedence covers file, key, duplicate, and fallback ordering.
func TestDiscoverDefaultRuntimeAppliesGeneratedPortPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		dotEnv     string
		dotExample string
		want       uint16
	}{
		{name: "environment API", dotEnv: "PORT=4000\nAPI_HTTP_PORT=4001\n", dotExample: "API_HTTP_PORT=3000\n", want: 4001},
		{name: "environment generic", dotEnv: "PORT='4002'\n", dotExample: "API_HTTP_PORT=3000\n", want: 4002},
		{name: "last assignment", dotEnv: "API_HTTP_PORT=4003\nAPI_HTTP_PORT=4004\n", want: 4004},
		{name: "example API", dotExample: "API_HTTP_PORT=4005\nPORT=4006\n", want: 4005},
		{name: "example generic", dotExample: "export PORT=4007\n", want: 4007},
		{name: "generated default", want: 3000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := runtimeTargetTestProject(t, test.dotEnv, test.dotExample)
			target, err := NewDiscoverer().DiscoverDefaultRuntime(t.Context(), root)
			if err != nil {
				t.Fatalf("DiscoverDefaultRuntime() error = %v", err)
			}
			if target.Port != test.want {
				t.Fatalf("DiscoverDefaultRuntime() port = %d, want %d", target.Port, test.want)
			}
		})
	}
}

// TestDiscoverDefaultRuntimeRejectsInvalidExplicitPorts keeps launch errors attached to the project's actual configuration.
func TestDiscoverDefaultRuntimeRejectsInvalidExplicitPorts(t *testing.T) {
	for _, value := range []string{"", "0", "65536", "3OOO", "3000.5", "${OTHER_PORT}"} {
		t.Run(value, func(t *testing.T) {
			root := runtimeTargetTestProject(t, "API_HTTP_PORT="+value+"\n", "API_HTTP_PORT=3000\n")
			_, err := NewDiscoverer().DiscoverDefaultRuntime(t.Context(), root)
			var invalid *InvalidProjectError
			if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "API_HTTP_PORT") || !strings.Contains(err.Error(), "1 through 65535") {
				t.Fatalf("DiscoverDefaultRuntime() error = %v, want invalid port", err)
			}
		})
	}
}

// TestDiscoverDefaultRuntimeHonorsCancellationAndProjectValidation keeps filesystem work behind the established discovery boundary.
func TestDiscoverDefaultRuntimeHonorsCancellationAndProjectValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDiscoverer().DiscoverDefaultRuntime(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverDefaultRuntime(cancelled) error = %v", err)
	}

	missing := t.TempDir()
	_, err := NewDiscoverer().DiscoverDefaultRuntime(t.Context(), missing)
	var invalid *InvalidProjectError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), ".goforj.yml") {
		t.Fatalf("DiscoverDefaultRuntime(missing marker) error = %v", err)
	}
}

// TestRuntimeTargetValidateRejectsDriftedURLs prevents callers from substituting public or differently ported targets.
func TestRuntimeTargetValidateRejectsDriftedURLs(t *testing.T) {
	valid := RuntimeTarget{AppID: "app", Name: "App", Address: netip.MustParseAddr("127.0.0.1"), Port: 3000, ResourceURL: "http://127.0.0.1:3000", ReadyURL: "http://127.0.0.1:3000/-/ready"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("RuntimeTarget.Validate() error = %v", err)
	}
	for _, mutate := range []func(*RuntimeTarget){
		func(target *RuntimeTarget) { target.AppID = " bad " },
		func(target *RuntimeTarget) { target.Name = "" },
		func(target *RuntimeTarget) { target.Port = 0 },
		func(target *RuntimeTarget) { target.Address = netip.MustParseAddr("192.0.2.10") },
		func(target *RuntimeTarget) { target.ResourceURL = "https://public.example.test" },
		func(target *RuntimeTarget) { target.ReadyURL = "http://127.0.0.1:3000/-/health" },
	} {
		target := valid
		mutate(&target)
		if err := target.Validate(); err == nil {
			t.Fatalf("RuntimeTarget.Validate(%#v) error = nil", target)
		}
	}
}

// TestNewRuntimeTargetConstructsAssignedIPv4URL covers typed target construction without hostname inference.
func TestNewRuntimeTargetConstructsAssignedIPv4URL(t *testing.T) {
	address := netip.MustParseAddr("127.0.0.43")
	target, err := NewRuntimeTarget("app", "App", address, 8080)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	if target.Address != address || target.ResourceURL != "http://127.0.0.43:8080" || target.ReadyURL != "http://127.0.0.43:8080/-/ready" {
		t.Fatalf("NewRuntimeTarget() = %#v", target)
	}
}

// TestAssignedRuntimeAddressRejectsNonLocalAuthority prevents readiness from following invalid or externally routable assignments.
func TestAssignedRuntimeAddressRejectsNonLocalAuthority(t *testing.T) {
	root := runtimeTargetTestProject(t, "API_HTTP_PORT=4317\n", "")
	for _, address := range []netip.Addr{
		{},
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("::ffff:127.0.0.42"),
		netip.MustParseAddr("::1"),
		netip.MustParseAddr("::1%lo0"),
	} {
		_, err := NewDiscoverer().DiscoverDefaultRuntimeAtAddress(t.Context(), root, address)
		if err == nil || !strings.Contains(err.Error(), "runtime target address") {
			t.Fatalf("DiscoverDefaultRuntimeAtAddress(%s) error = %v", address, err)
		}
	}
}

// runtimeTargetTestProject writes only the marker and optional metadata needed by one discovery case.
func runtimeTargetTestProject(t *testing.T, dotEnv string, dotExample string) string {
	t.Helper()
	root := managedRuntimeContractTestProject(t, managedRuntimeContractTestSource)
	if err := os.WriteFile(filepath.Join(root, ".goforj.yml"), []byte("module_name: example.test/runtime\n"), 0o600); err != nil {
		t.Fatalf("write project marker: %v", err)
	}
	if dotEnv != "" {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(dotEnv), 0o600); err != nil {
			t.Fatalf("write project environment: %v", err)
		}
	}
	if dotExample != "" {
		if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte(dotExample), 0o600); err != nil {
			t.Fatalf("write project example environment: %v", err)
		}
	}
	return root
}
