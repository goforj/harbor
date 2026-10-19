package containerruntime

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/projectruntime"
	"github.com/moby/moby/client"
)

// LaunchDiagnosticObserver optionally diagnoses checkout-owned failed containers without changing their lifecycle.
type LaunchDiagnosticObserver interface {
	ObserveLaunchDiagnostics(context.Context, string) (projectruntime.LaunchDiagnostics, error)
}

// ObserveLaunchDiagnostics compares failed services' requested publications against fresh global Engine observations.
func (runtime *DockerRuntime) ObserveLaunchDiagnostics(ctx context.Context, checkout string) (projectruntime.LaunchDiagnostics, error) {
	owned, err := runtime.projectContainers(ctx, checkout)
	if err != nil {
		return projectruntime.LaunchDiagnostics{}, err
	}
	listed, err := runtime.engine.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return projectruntime.LaunchDiagnostics{}, err
	}
	if len(listed.Items) > maximumRuntimeCandidates {
		return projectruntime.LaunchDiagnostics{}, fmt.Errorf("too many runtime candidates for port diagnostics")
	}
	result := projectruntime.LaunchDiagnostics{PortConflicts: []projectruntime.PortConflict{}}
	seen := map[projectruntime.PortConflict]bool{}
	for _, failed := range owned {
		if failed.container.State == "running" || failed.container.State == "paused" || failed.container.State == "restarting" {
			continue
		}
		for _, wanted := range failed.container.RequestedPorts {
			if err := ctx.Err(); err != nil {
				return projectruntime.LaunchDiagnostics{}, err
			}
			for _, listener := range listed.Items {
				if listener.ID == failed.container.ID {
					continue
				}
				name := listener.ID
				if len(listener.Names) > 0 {
					name = strings.TrimPrefix(listener.Names[0], "/")
				}
				if len(name) > 256 {
					continue
				}
				for _, actual := range listener.Ports {
					if wanted.Public == 0 || wanted.Public != actual.PublicPort || wanted.Protocol != actual.Type || !publicationAddressesOverlap(wanted.Address, actual.IP) {
						continue
					}
					bound := actual.IP.String()
					if !actual.IP.IsValid() {
						bound = "0.0.0.0"
					}
					conflict := projectruntime.PortConflict{Service: failed.serviceID, Address: wanted.Address, Port: wanted.Public, Protocol: wanted.Protocol, Listener: name, Bound: bound}
					if !seen[conflict] && len(result.PortConflicts) < 16 {
						result.PortConflicts = append(result.PortConflicts, conflict)
						seen[conflict] = true
					}
				}
			}
		}
	}
	sort.Slice(result.PortConflicts, func(i, j int) bool { return fmt.Sprint(result.PortConflicts[i]) < fmt.Sprint(result.PortConflicts[j]) })
	return result, nil
}

// publicationAddressesOverlap recognizes wildcard IPv4 binds without conflating distinct loopback identities.
func publicationAddressesOverlap(wanted string, actual netip.Addr) bool {
	requested, err := netip.ParseAddr(wanted)
	if err != nil {
		return false
	}
	if !actual.IsValid() {
		actual = netip.IPv4Unspecified()
	}
	requested, actual = requested.Unmap(), actual.Unmap()
	if requested.Is4() != actual.Is4() {
		return false
	}
	return requested.IsUnspecified() || actual.IsUnspecified() || requested == actual
}

// requestedContainerPorts reads configured host bindings even when Docker could not start the container.
func requestedContainerPorts(inspected client.ContainerInspectResult) []Port {
	result := []Port{}
	if inspected.Container.HostConfig == nil {
		return result
	}
	for port, bindings := range inspected.Container.HostConfig.PortBindings {
		for _, binding := range bindings {
			if len(result) >= 256 {
				return result
			}
			number, err := strconv.ParseUint(binding.HostPort, 10, 16)
			if err != nil || number == 0 {
				continue
			}
			address := "0.0.0.0"
			if binding.HostIP.IsValid() {
				address = binding.HostIP.String()
			}
			result = append(result, Port{Address: address, Private: port.Num(), Public: uint16(number), Protocol: string(port.Proto())})
		}
	}
	return result
}
