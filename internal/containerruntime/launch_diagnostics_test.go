package containerruntime

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// TestLaunchDiagnosticsIdentifiesWildcardDockerPublication proves a neighboring listener can block an isolated loopback.
func TestLaunchDiagnosticsIdentifiesWildcardDockerPublication(t *testing.T) {
	for _, test := range []struct {
		name, bind, protocol string
		conflict             bool
	}{
		{"wildcard", "0.0.0.0", "tcp", true},
		{"exact", "127.77.59.72", "tcp", true},
		{"other-loopback", "127.77.59.73", "tcp", false},
		{"other-protocol", "0.0.0.0", "udp", false},
		{"ipv6-only", "::1", "tcp", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			failed := inspectResult("failed", "app-mailpit-1", composeLabels(root, "app", "mailpit", "1"), container.StateCreated, 0)
			failed.Container.HostConfig = &container.HostConfig{PortBindings: network.PortMap{
				network.MustParsePort("1025/tcp"): {{HostIP: netip.MustParseAddr("127.77.59.72"), HostPort: "1025"}},
			}}
			engine := &fakeDockerEngine{
				listed: client.ContainerListResult{Items: []container.Summary{
					{ID: "failed", Labels: composeLabels(root, "app", "mailpit", "1")},
					{ID: "neighbor", Names: []string{"/other-mailpit-1"}, Ports: []container.PortSummary{{IP: netip.MustParseAddr(test.bind), PublicPort: 1025, PrivatePort: 1025, Type: test.protocol}}},
				}},
				inspections: map[string]client.ContainerInspectResult{"failed": failed},
			}
			diagnostics, err := newDockerRuntime(engine).ObserveLaunchDiagnostics(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if (len(diagnostics.PortConflicts) == 1) != test.conflict {
				t.Fatalf("unexpected diagnostics: %#v", diagnostics)
			}
			if test.conflict {
				conflict := diagnostics.PortConflicts[0]
				if conflict.Service != "mailpit" || conflict.Listener != "other-mailpit-1" || conflict.Bound != test.bind {
					t.Fatalf("wrong owner facts: %#v", conflict)
				}
				if err := diagnostics.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// TestRequestedContainerPortsSkipsUnresolvedAndDynamicBindings prevents invented endpoint numbers in failure advice.
func TestRequestedContainerPortsSkipsUnresolvedAndDynamicBindings(t *testing.T) {
	inspected := inspectResult("failed", "failed", nil, container.StateCreated, 0)
	if ports := requestedContainerPorts(inspected); len(ports) != 0 {
		t.Fatal("nil host config produced bindings")
	}
	inspected.Container.HostConfig = &container.HostConfig{PortBindings: network.PortMap{
		network.MustParsePort("80/tcp"): {{HostPort: ""}, {HostPort: "0"}, {HostPort: "bad"}, {HostPort: "70000"}, {HostPort: "8080"}},
	}}
	ports := requestedContainerPorts(inspected)
	if len(ports) != 1 || ports[0].Public != 8080 || ports[0].Address != "0.0.0.0" {
		t.Fatalf("ports: %#v", ports)
	}
}
