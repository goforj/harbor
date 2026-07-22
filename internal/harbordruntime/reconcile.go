package harbordruntime

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
)

const maximumHTTPReconcileAttempts = 4

const appHTTPResourceID domain.ResourceID = "app-http"

// Reconcile projects the current ready project routes into the live data plane.
func (controller *Controller) Reconcile(ctx context.Context) error {
	if controller == nil || !controller.initialized {
		return ErrNotInitialized
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	controller.mutex.RLock()
	lifecycle := controller.state
	runtime := controller.dataPlane
	authority := controller.certificates
	controller.mutex.RUnlock()
	if lifecycle != controllerStateReady || runtime == nil || authority == nil {
		return fmt.Errorf("reconcile harbord runtime: %w", ErrNotReady)
	}
	return controller.reconcileHTTPRoutes(ctx, controllerStateReady, runtime, authority)
}

// HTTPRouteLive reports whether the ready generation last published one exact host-to-upstream route.
func (controller *Controller) HTTPRouteLive(host string, upstream netip.AddrPort) bool {
	if controller == nil || !controller.initialized {
		return false
	}

	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()
	controller.mutex.RLock()
	defer controller.mutex.RUnlock()
	if controller.state != controllerStateReady || controller.dataPlane == nil {
		return false
	}
	if controller.dataPlane.Snapshot().State != dataplane.StateReady {
		return false
	}
	for _, route := range controller.publishedHTTPRoutes {
		if route.Host == host && route.Upstream == upstream {
			return true
		}
	}
	return false
}

// initializeHTTPReconciliation retains the immutable topology and the routes already owned at startup.
func (controller *Controller) initializeHTTPReconciliation(desired dataplane.DesiredState) {
	controller.reconcileMutex.Lock()
	controller.httpFoundation = desired
	controller.publishedHTTPRoutes = desired.HTTPRoutes()
	controller.reconcileMutex.Unlock()
}

// reconcileHTTPRoutes withdraws obsolete routes before certificate or publication work can fail.
func (controller *Controller) reconcileHTTPRoutes(
	ctx context.Context,
	expected controllerState,
	runtime dataPlane,
	authority certificateAuthority,
) error {
	controller.reconcileMutex.Lock()
	defer controller.reconcileMutex.Unlock()

	for attempt := 0; attempt < maximumHTTPReconcileAttempts; attempt++ {
		if err := controller.requireReconcileLifecycle(ctx, expected); err != nil {
			return err
		}
		runtimeState, err := controller.source.RuntimeState(ctx)
		if err != nil {
			return controller.failClosedHTTPRoutes(ctx, expected, runtime, fmt.Errorf("read durable state: %w", err))
		}
		desired, err := controller.desiredHTTPStateFromRuntimeState(runtimeState)
		if err != nil {
			return controller.failClosedHTTPRoutes(ctx, expected, runtime, err)
		}
		if !sameHTTPFoundation(controller.httpFoundation, desired) {
			return controller.failClosedHTTPRoutes(
				ctx,
				expected,
				runtime,
				errors.New("durable listener or native-route topology changed during a live generation"),
			)
		}

		retained := unchangedHTTPRoutes(controller.publishedHTTPRoutes, desired.HTTPRoutes())
		if err := controller.replaceHTTPRoutes(ctx, expected, runtime, retained); err != nil {
			return fmt.Errorf("withdraw changed HTTP routes: %w", err)
		}
		for _, route := range desired.HTTPRoutes() {
			if err := controller.requireReconcileLifecycle(ctx, expected); err != nil {
				return err
			}
			if _, err := authority.EnsureLeaf(ctx, route.Host); err != nil {
				return fmt.Errorf("ensure certificate for HTTP host %q: %w", route.Host, err)
			}
		}

		if err := controller.requireReconcileLifecycle(ctx, expected); err != nil {
			return err
		}
		confirmedState, err := controller.source.RuntimeState(ctx)
		if err != nil {
			return controller.failClosedHTTPRoutes(ctx, expected, runtime, fmt.Errorf("confirm durable state: %w", err))
		}
		confirmed, err := controller.desiredHTTPStateFromRuntimeState(confirmedState)
		if err != nil {
			return controller.failClosedHTTPRoutes(ctx, expected, runtime, err)
		}
		if !sameHTTPFoundation(controller.httpFoundation, confirmed) {
			return controller.failClosedHTTPRoutes(
				ctx,
				expected,
				runtime,
				errors.New("durable listener or native-route topology changed during a live generation"),
			)
		}
		if !slices.Equal(desired.HTTPRoutes(), confirmed.HTTPRoutes()) {
			continue
		}
		if err := controller.replaceHTTPRoutes(ctx, expected, runtime, confirmed.HTTPRoutes()); err != nil {
			return fmt.Errorf("publish HTTP routes: %w", err)
		}
		return nil
	}

	return controller.failClosedHTTPRoutes(
		ctx,
		expected,
		runtime,
		fmt.Errorf("reconcile HTTP routes: durable state changed across %d attempts", maximumHTTPReconcileAttempts),
	)
}

// desiredHTTPStateFromRuntimeState retains the DNS-only resolver foundation while HTTP remains gated by full setup.
func (controller *Controller) desiredHTTPStateFromRuntimeState(runtimeState state.RuntimeState) (dataplane.DesiredState, error) {
	desired, err := desiredHTTPStateFromRuntimeState(runtimeState)
	if err != nil {
		return dataplane.DesiredState{}, err
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageResolver {
		return desired, nil
	}
	foundation := controller.httpFoundation
	plan := foundation.ListenerPlan()
	if plan.DNS == (netip.AddrPort{}) || plan.HTTP != (netip.AddrPort{}) || plan.HTTPS != (netip.AddrPort{}) || len(foundation.NativeRoutes()) != 0 {
		return desired, nil
	}
	return desiredWithHTTPRoutes(foundation, nil)
}

// requireReconcileLifecycle verifies permission without retaining the lifecycle lock across external I/O.
func (controller *Controller) requireReconcileLifecycle(ctx context.Context, expected controllerState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	controller.mutex.RLock()
	lifecycle := controller.state
	stopCause := controller.stopCause
	controller.mutex.RUnlock()
	if lifecycle != expected {
		if expected == controllerStateStarting && stopCause != nil {
			return stopCause
		}
		return fmt.Errorf("reconcile harbord runtime: %w", ErrNotReady)
	}
	return nil
}

// replaceHTTPRoutes publishes one route set and advances retained state only after success.
func (controller *Controller) replaceHTTPRoutes(
	ctx context.Context,
	expected controllerState,
	runtime dataPlane,
	routes []dataplane.HTTPRoute,
) error {
	if err := controller.requireReconcileLifecycle(ctx, expected); err != nil {
		return err
	}
	desired, err := desiredWithHTTPRoutes(controller.httpFoundation, routes)
	if err != nil {
		return err
	}
	if err := runtime.ReplaceHTTPRoutes(desired); err != nil {
		return err
	}
	controller.publishedHTTPRoutes = desired.HTTPRoutes()
	return nil
}

// failClosedHTTPRoutes withdraws all HTTP routes when durable state can no longer authorize an exact subset.
func (controller *Controller) failClosedHTTPRoutes(
	ctx context.Context,
	expected controllerState,
	runtime dataPlane,
	cause error,
) error {
	if err := controller.replaceHTTPRoutes(ctx, expected, runtime, nil); err != nil {
		return errors.Join(cause, fmt.Errorf("withdraw HTTP routes: %w", err))
	}
	return cause
}

// desiredHTTPStateFromRuntimeState joins ready resources to durable host and address ownership.
func desiredHTTPStateFromRuntimeState(runtimeState state.RuntimeState) (dataplane.DesiredState, error) {
	foundation, err := desiredStateFromRuntimeState(runtimeState)
	if err != nil {
		return dataplane.DesiredState{}, err
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageFull {
		return foundation, nil
	}

	primaryAddresses := make(map[domain.ProjectID]netip.Addr)
	for _, lease := range runtimeState.Network.Leases {
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primaryAddresses[lease.Key.ProjectID] = lease.Address.Unmap()
		}
	}
	reservations := make(map[domain.ProjectID][]state.EndpointReservation)
	for _, reservation := range runtimeState.Network.Reservations.Endpoints {
		if reservation.Protocol == state.EndpointProtocolHTTP {
			reservations[reservation.Key.ProjectID] = append(reservations[reservation.Key.ProjectID], reservation)
		}
	}

	routes := make([]dataplane.HTTPRoute, 0, len(runtimeState.Network.Reservations.Endpoints))
	for _, project := range runtimeState.Snapshot.Projects {
		if project.State != domain.ProjectReady {
			continue
		}
		primary, exists := primaryAddresses[project.ID]
		if !exists {
			return dataplane.DesiredState{}, fmt.Errorf("derive HTTP route for project %q: primary lease is missing", project.ID)
		}
		projectRoutes, err := readyProjectHTTPRoutes(project, reservations[project.ID], primary)
		if err != nil {
			return dataplane.DesiredState{}, fmt.Errorf("derive HTTP route for project %q: %w", project.ID, err)
		}
		routes = append(routes, projectRoutes...)
	}

	desired, err := desiredWithHTTPRoutes(foundation, routes)
	if err != nil {
		return dataplane.DesiredState{}, fmt.Errorf("derive HTTP routes: %w", err)
	}
	return desired, nil
}

// readyProjectHTTPRoutes joins every durable HTTP reservation to one ready resource without treating observation as publication authority.
func readyProjectHTTPRoutes(
	project domain.ProjectSnapshot,
	reservations []state.EndpointReservation,
	primary netip.Addr,
) ([]dataplane.HTTPRoute, error) {
	resources := make(map[domain.ResourceID]domain.ResourceSnapshot, len(project.Resources))
	for _, resource := range project.Resources {
		resources[resource.ID] = resource
	}
	apps := make(map[domain.AppID]domain.AppSnapshot, len(project.Apps))
	for _, app := range project.Apps {
		apps[app.ID] = app
	}
	services := make(map[domain.ServiceID]domain.ServiceSnapshot, len(project.Services))
	for _, service := range project.Services {
		services[service.ID] = service
	}

	appHTTPFound := false
	routes := make([]dataplane.HTTPRoute, 0, len(reservations))
	for _, reservation := range reservations {
		resource, exists := resources[domain.ResourceID(reservation.Key.EndpointID)]
		if !exists {
			return nil, fmt.Errorf("HTTP reservation %q has no matching resource", reservation.Key.EndpointID)
		}
		if reservation.Key.EndpointID == string(appHTTPResourceID) {
			appHTTPFound = true
			if resource.Kind != "application" {
				return nil, fmt.Errorf("resource app-http kind %q must be application", resource.Kind)
			}
			if resource.Owner.Kind != domain.ResourceOwnedByApp {
				return nil, errors.New("resource app-http must be App-owned")
			}
			if reservation.Host != project.Slug+".test" {
				return nil, fmt.Errorf(
					"app-http host %q must equal %q",
					reservation.Host,
					project.Slug+".test",
				)
			}
		}
		if err := readyHTTPResourceOwner(resource, apps, services); err != nil {
			return nil, fmt.Errorf("resource %q: %w", resource.ID, err)
		}
		if reservation.Key.EndpointID == string(appHTTPResourceID) {
			app := apps[resource.Owner.AppID]
			if !app.Required {
				return nil, fmt.Errorf("App %q must be required", app.ID)
			}
		}
		var upstream netip.AddrPort
		var err error
		if reservation.Key.EndpointID == string(appHTTPResourceID) {
			upstream, err = canonicalHTTPUpstream(resource.URL)
		} else {
			upstream, err = resourceHTTPUpstream(resource.URL)
		}
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", resource.ID, err)
		}
		if upstream.Addr() != primary {
			return nil, fmt.Errorf(
				"resource %q upstream address %s does not match primary lease %s",
				resource.ID,
				upstream.Addr(),
				primary,
			)
		}
		routes = append(routes, dataplane.HTTPRoute{
			ID:       project.Slug + ":" + reservation.Key.EndpointID,
			Host:     reservation.Host,
			Upstream: upstream,
		})
	}
	if !appHTTPFound {
		return nil, errors.New("app-http reservation is missing")
	}
	return routes, nil
}

// readyHTTPResourceOwner requires a live lifecycle owner before a reserved resource can enter public ingress.
func readyHTTPResourceOwner(
	resource domain.ResourceSnapshot,
	apps map[domain.AppID]domain.AppSnapshot,
	services map[domain.ServiceID]domain.ServiceSnapshot,
) error {
	switch resource.Owner.Kind {
	case domain.ResourceOwnedByApp:
		app, exists := apps[resource.Owner.AppID]
		if !exists {
			return fmt.Errorf("App owner %q is missing", resource.Owner.AppID)
		}
		if !app.Active || app.State != domain.EntityReady {
			return fmt.Errorf("App %q is not ready and active", app.ID)
		}
	case domain.ResourceOwnedByService:
		service, exists := services[resource.Owner.ServiceID]
		if !exists {
			return fmt.Errorf("service owner %q is missing", resource.Owner.ServiceID)
		}
		if service.Owner != domain.ServiceOwnerCompose || service.Selection != domain.ServiceSelected || service.State != domain.EntityReady {
			return fmt.Errorf("service %q is not a ready selected Compose service", service.ID)
		}
	default:
		return fmt.Errorf("resource owner kind %q is unsupported", resource.Owner.Kind)
	}
	return nil
}

// resourceHTTPUpstream extracts the private origin from a named resource URL while preserving its browser path.
//
// The ingress relay forwards the request path unchanged, so a descriptor resource such as /swagger still needs
// the origin-only upstream shape used by the data plane. Query and fragment-bearing links are not route authority.
func resourceHTTPUpstream(rawURL string) (netip.AddrPort, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return netip.AddrPort{}, fmt.Errorf("URL %q must be a query-free HTTP resource", rawURL)
	}
	upstream, err := netip.ParseAddrPort(parsed.Host)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("URL host %q must be a literal address with an explicit port", parsed.Host)
	}
	upstream = netip.AddrPortFrom(upstream.Addr().Unmap(), upstream.Port())
	canonicalHost := upstream.String()
	if !upstream.Addr().Is4() || !upstream.Addr().IsLoopback() || upstream.Port() == 0 || parsed.Host != canonicalHost {
		return netip.AddrPort{}, fmt.Errorf("URL %q must use a canonical IPv4 loopback HTTP origin", rawURL)
	}
	return upstream, nil
}

// canonicalHTTPUpstream accepts only the canonical raw loopback HTTP origin persisted by project startup.
func canonicalHTTPUpstream(rawURL string) (netip.AddrPort, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("parse URL: %w", err)
	}
	upstream, err := netip.ParseAddrPort(parsed.Host)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("URL host %q must be a literal address with an explicit port", parsed.Host)
	}
	upstream = netip.AddrPortFrom(upstream.Addr().Unmap(), upstream.Port())
	canonical := (&url.URL{Scheme: "http", Host: upstream.String()}).String()
	if rawURL != canonical || !upstream.Addr().Is4() || !upstream.Addr().IsLoopback() || upstream.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("URL %q must be a canonical IPv4 loopback HTTP origin", rawURL)
	}
	return upstream, nil
}

// desiredWithHTTPRoutes preserves immutable listener, native-route, and TTL topology around HTTP routes.
func desiredWithHTTPRoutes(foundation dataplane.DesiredState, routes []dataplane.HTTPRoute) (dataplane.DesiredState, error) {
	return dataplane.NewDesiredState(foundation.ListenerPlan(), routes, foundation.NativeRoutes(), foundation.TTL())
}

// sameHTTPFoundation reports whether a replacement preserves every non-HTTP field.
func sameHTTPFoundation(left dataplane.DesiredState, right dataplane.DesiredState) bool {
	return left.ListenerPlan() == right.ListenerPlan() &&
		left.TTL() == right.TTL() &&
		slices.Equal(left.NativeRoutes(), right.NativeRoutes())
}

// unchangedHTTPRoutes retains only exact routes authorized by both published and desired state.
func unchangedHTTPRoutes(current []dataplane.HTTPRoute, desired []dataplane.HTTPRoute) []dataplane.HTTPRoute {
	authorized := make(map[dataplane.HTTPRoute]struct{}, len(desired))
	for _, route := range desired {
		authorized[route] = struct{}{}
	}
	retained := make([]dataplane.HTTPRoute, 0, len(current))
	for _, route := range current {
		if _, exists := authorized[route]; exists {
			retained = append(retained, route)
		}
	}
	return retained
}
