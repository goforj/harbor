package reconcile

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/projectprocess"
)

// reconcileObservedNativeServiceRoutes publishes Docker-observed TCP services without requiring runtime-specific descriptors.
func (coordinator *ProjectLifecycleCoordinator) reconcileObservedNativeServiceRoutes(
	ctx context.Context,
	project domain.ProjectSnapshot,
	session domain.ProjectSession,
	primaryAddress netip.Addr,
	services []domain.ServiceSnapshot,
	resources []domain.ResourceSnapshot,
) error {
	if coordinator == nil {
		panic("reconcile.ProjectLifecycleCoordinator.reconcileObservedNativeServiceRoutes requires a non-nil receiver")
	}
	ctx = normalizeLifecycleContext(ctx)
	if err := project.Validate(); err != nil {
		return fmt.Errorf("publish observed native services: validate project: %w", err)
	}
	if err := session.Validate(); err != nil {
		return fmt.Errorf("publish observed native services: validate session: %w", err)
	}
	if session.ProjectID != project.ID {
		return fmt.Errorf("publish observed native services: session does not belong to project %q", project.ID)
	}
	primaryAddress = primaryAddress.Unmap()
	if !primaryAddress.IsValid() || !primaryAddress.Is4() || !primaryAddress.IsLoopback() {
		return fmt.Errorf("publish observed native services: project %q primary address %s is not canonical IPv4 loopback", project.ID, primaryAddress)
	}

	portReader, supported := coordinator.projectRuntimeCapabilities().(projectServicePortReader)
	if !supported {
		return coordinator.routes.ReconcileProjectNativeRoutes(
			ctx,
			project.ID,
			[]dataplane.NativeRoute{},
		)
	}
	httpHosts, err := projectHTTPResourceHosts(project.Slug, resources)
	if err != nil {
		return fmt.Errorf("publish observed native services for project %q: %w", project.ID, err)
	}
	routes := make([]dataplane.NativeRoute, 0, len(services))
	for _, service := range services {
		if service.Owner != domain.ServiceOwnerCompose ||
			service.Selection != domain.ServiceSelected ||
			service.State != domain.EntityReady {
			continue
		}
		host, err := projectServiceEndpointHost(project.Slug, string(service.ID))
		if err != nil {
			return fmt.Errorf("publish observed native service %q: %w", service.ID, err)
		}
		if _, claimedByHTTP := httpHosts[host]; claimedByHTTP {
			continue
		}
		observation, err := portReader.ObserveServicePorts(
			ctx,
			project.ID,
			session.ID,
			service.ID,
		)
		if err != nil {
			return fmt.Errorf("observe native service %q ports: %w", service.ID, err)
		}
		if !observation.Supported || !observation.Available {
			continue
		}
		port, address, found := preferredNativeServicePort(observation.Ports, primaryAddress)
		if !found {
			continue
		}
		listen := netip.AddrPortFrom(primaryAddress, port.Private)
		upstream := netip.AddrPortFrom(address, port.Public)
		routes = append(routes, dataplane.NativeRoute{
			ID:       string(project.ID) + ":service:" + string(service.ID),
			Host:     host,
			Listen:   listen,
			Upstream: upstream,
			Direct:   listen == upstream,
		})
	}
	if err := coordinator.routes.ReconcileProjectNativeRoutes(ctx, project.ID, routes); err != nil {
		return fmt.Errorf("publish observed native services for project %q: %w", project.ID, err)
	}
	return nil
}

// projectHTTPResourceHosts returns names already owned by Harbor's shared HTTP proxy.
func projectHTTPResourceHosts(
	slug string,
	resources []domain.ResourceSnapshot,
) (map[string]struct{}, error) {
	hosts := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		host, err := projectResourceEndpointHost(slug, resource.ID)
		if err != nil {
			return nil, fmt.Errorf("derive HTTP resource %q host: %w", resource.ID, err)
		}
		hosts[host] = struct{}{}
	}
	return hosts, nil
}

// preferredNativeServicePort chooses a stable project-address binding before a localhost relay candidate.
func preferredNativeServicePort(
	ports []projectprocess.ServicePort,
	primaryAddress netip.Addr,
) (projectprocess.ServicePort, netip.Addr, bool) {
	var selected projectprocess.ServicePort
	var selectedAddress netip.Addr
	selectedPriority := 0
	for _, port := range ports {
		if !strings.EqualFold(port.Protocol, "tcp") || port.Private == 0 || port.Public == 0 {
			continue
		}
		address, err := netip.ParseAddr(port.Address)
		if err != nil {
			continue
		}
		address = address.Unmap()
		priority := 0
		switch address {
		case primaryAddress:
			priority = 2
		case netip.IPv4Unspecified(), netip.IPv6Unspecified():
			continue
		default:
			if address != netip.MustParseAddr("127.0.0.1") {
				continue
			}
			priority = 1
		}
		if selectedPriority > priority {
			continue
		}
		if selectedPriority == priority && selectedPriority != 0 &&
			(selected.Private < port.Private ||
				selected.Private == port.Private && selected.Public <= port.Public) {
			continue
		}
		selected = port
		selectedAddress = address
		selectedPriority = priority
	}
	return selected, selectedAddress, selectedPriority != 0
}
