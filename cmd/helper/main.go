// Package main provides Harbor's bespoke one-shot privileged helper entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/loopbackhandler"
	"github.com/goforj/harbor/internal/helper/replaystore"
	"github.com/goforj/harbor/internal/helper/ticketredeemer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// closingTicketRedeemer retains the authenticated spool authority for exactly one helper process.
type closingTicketRedeemer interface {
	helper.TicketRedeemer
	Close() error
}

// closingReplayGuard retains the durable replay authority for exactly one helper process.
type closingReplayGuard interface {
	helper.ReplayGuard
	Close() error
}

// closingResolverHandler retains protected ownership authority for exactly one helper process.
type closingResolverHandler interface {
	helper.ResolverHandler
	Close() error
}

// closingTrustHandler retains the authenticated trust mutation boundary for one helper invocation.
type closingTrustHandler interface {
	helper.TrustHandler
	Close() error
}

// closingLowPortHandler retains the reviewed low-port mutation boundary for one helper invocation.
type closingLowPortHandler interface {
	helper.LowPortHandler
	Close() error
}

// unavailableClosingLowPortHandler keeps unsupported builds resource-free while satisfying helper lifecycle wiring.
type unavailableClosingLowPortHandler struct {
	helper.UnavailableLowPortHandler
}

// Close releases no resources because the unavailable handler owns no native authority.
func (unavailableClosingLowPortHandler) Close() error {
	return nil
}

// runtimeDependencies keeps fixed production authority replaceable without redirectable path arguments.
type runtimeDependencies struct {
	authorizeInvocation                  func() error
	openTicketRedeemer                   func() (closingTicketRedeemer, error)
	openReplayGuard                      func() (closingReplayGuard, error)
	newLoopbackIdentityHandler           func() helper.LoopbackIdentityHandler
	openResolverHandler                  func() (closingResolverHandler, error)
	openTrustHandler                     func() (closingTrustHandler, error)
	openAdministratorTrustHandler        func() (closingTrustHandler, error)
	openLowPortHandler                   func() (closingLowPortHandler, error)
	transitionTrustIdentity              func(string) error
	transitionAdministratorTrustIdentity func(string) error
}

// runtimeAuthorities retains only resources opened for the single admitted helper operation.
type runtimeAuthorities struct {
	redeemer        closingTicketRedeemer
	replayGuard     closingReplayGuard
	resolverHandler closingResolverHandler
	trustHandler    closingTrustHandler
	lowPortHandler  closingLowPortHandler
}

// main runs one request with only the fixed durable stores and reviewed platform mutation authority.
func main() {
	// The privileged helper must not let inherited ambient configuration influence its authority.
	os.Clearenv()
	invocation, err := openPlatformInvocation(os.Args, os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(1)
	}
	runErr := run(context.Background(), invocation.reader, invocation.writer, helper.SystemClock{}, productionDependencies())
	if err := errors.Join(runErr, invocation.close()); err != nil {
		os.Exit(1)
	}
}

// productionDependencies binds the privileged process to Harbor's compiled machine paths and reviewed OS adapter.
func productionDependencies() runtimeDependencies {
	return runtimeDependencies{
		authorizeInvocation: authorizePlatformInvocation,
		openTicketRedeemer: func() (closingTicketRedeemer, error) {
			return ticketredeemer.OpenDefault()
		},
		openReplayGuard: func() (closingReplayGuard, error) {
			return replaystore.OpenDefault()
		},
		newLoopbackIdentityHandler: func() helper.LoopbackIdentityHandler {
			return loopbackhandler.New()
		},
		openResolverHandler:                  openPlatformResolverHandler,
		openTrustHandler:                     openPlatformTrustHandler,
		openAdministratorTrustHandler:        openPlatformAdministratorTrustHandler,
		openLowPortHandler:                   openPlatformLowPortHandler,
		transitionTrustIdentity:              irreversiblyDropTrustIdentity,
		transitionAdministratorTrustIdentity: irreversiblyEnterAdministratorTrustIdentity,
	}
}

// run opens authentication and replay authority before admitting exactly one lazily selected effect.
func run(ctx context.Context, reader io.Reader, writer io.Writer, clock helper.Clock, dependencies runtimeDependencies) (runErr error) {
	validateRuntimeComposition(clock, dependencies)
	if err := dependencies.authorizeInvocation(); err != nil {
		return fmt.Errorf("authorize helper invocation: %w", err)
	}

	redeemer, err := dependencies.openTicketRedeemer()
	if err != nil {
		return fmt.Errorf("open helper ticket redeemer: %w", err)
	}
	authorities := &runtimeAuthorities{
		redeemer: redeemer,
	}
	defer func() {
		runErr = errors.Join(runErr, authorities.close())
	}()

	replayGuard, err := dependencies.openReplayGuard()
	if err != nil {
		return fmt.Errorf("open helper replay guard: %w", err)
	}
	authorities.replayGuard = replayGuard

	dispatcher := helper.NewOneShotDispatcherWithAdmittedOperationExecutors(
		redeemer,
		clock,
		replayGuard,
		helper.AdmittedOperationExecutors{
			Trust: func(ctx context.Context, admitted helper.AdmittedTrustOperation) (helper.OperationResult, error) {
				return executeAdmittedTrust(ctx, admitted, dependencies, authorities)
			},
			Resolver: func(ctx context.Context, admitted helper.AdmittedResolverOperation) (helper.OperationResult, error) {
				return executeAdmittedResolver(ctx, admitted, dependencies, authorities)
			},
			LowPorts: func(ctx context.Context, admitted helper.AdmittedLowPortOperation) (helper.OperationResult, error) {
				return executeAdmittedLowPorts(ctx, admitted, dependencies, authorities)
			},
			Loopback: func(ctx context.Context, admitted helper.AdmittedLoopbackOperation) (helper.OperationResult, error) {
				return executeAdmittedLoopback(ctx, admitted, dependencies)
			},
		},
	)
	return helper.ServeOnce(ctx, reader, writer, dispatcher)
}

// validateRuntimeComposition rejects incomplete helper wiring before any authority or caller input is reached.
func validateRuntimeComposition(clock helper.Clock, dependencies runtimeDependencies) {
	if clock == nil {
		panic("helper runtime clock is required")
	}
	if dependencies.authorizeInvocation == nil {
		panic("helper invocation authorizer is required")
	}
	if dependencies.openTicketRedeemer == nil {
		panic("helper ticket redeemer factory is required")
	}
	if dependencies.openReplayGuard == nil {
		panic("helper replay guard factory is required")
	}
	if dependencies.newLoopbackIdentityHandler == nil {
		panic("helper loopback handler factory is required")
	}
	if dependencies.openResolverHandler == nil {
		panic("helper resolver handler factory is required")
	}
	if dependencies.openTrustHandler == nil {
		panic("helper trust handler factory is required")
	}
	if dependencies.openAdministratorTrustHandler == nil {
		panic("helper administrator trust handler factory is required")
	}
	if dependencies.openLowPortHandler == nil {
		panic("helper low-port handler factory is required")
	}
	if dependencies.transitionTrustIdentity == nil {
		panic("helper trust identity transition is required")
	}
	if dependencies.transitionAdministratorTrustIdentity == nil {
		panic("helper administrator trust identity transition is required")
	}
}

// executeAdmittedTrust selects the authenticated trust scope before calling its native handler.
func executeAdmittedTrust(
	ctx context.Context,
	admitted helper.AdmittedTrustOperation,
	dependencies runtimeDependencies,
	authorities *runtimeAuthorities,
) (helper.OperationResult, error) {
	switch admitted.TrustMechanism() {
	case networkpolicy.DarwinCurrentUserTrust:
		return executeAdmittedCurrentUserTrust(ctx, admitted, dependencies, authorities)
	case networkpolicy.DarwinAdministratorTrust:
		return executeAdmittedAdministratorTrust(ctx, admitted, dependencies, authorities)
	default:
		return helper.OperationResult{}, fmt.Errorf("admitted trust mechanism is unsupported: %q", admitted.TrustMechanism())
	}
}

// executeAdmittedCurrentUserTrust closes privileged authority before the irreversible requester identity transition.
func executeAdmittedCurrentUserTrust(
	ctx context.Context,
	admitted helper.AdmittedTrustOperation,
	dependencies runtimeDependencies,
	authorities *runtimeAuthorities,
) (helper.OperationResult, error) {
	if err := authorities.closePrivileged(); err != nil {
		return helper.OperationResult{}, err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := dependencies.transitionTrustIdentity(admitted.RequesterIdentity()); err != nil {
		return helper.OperationResult{}, fmt.Errorf("enter helper trust identity: %w", err)
	}
	trustHandler, err := dependencies.openTrustHandler()
	if err != nil {
		return helper.OperationResult{}, fmt.Errorf("open helper trust handler: %w", err)
	}
	if trustHandler == nil {
		panic("helper trust handler factory returned nil")
	}
	authorities.trustHandler = trustHandler
	return admitted.ExecuteTrust(ctx, trustHandler)
}

// executeAdmittedAdministratorTrust restores the real root identity Security.framework requires after admission authority closes.
func executeAdmittedAdministratorTrust(
	ctx context.Context,
	admitted helper.AdmittedTrustOperation,
	dependencies runtimeDependencies,
	authorities *runtimeAuthorities,
) (helper.OperationResult, error) {
	if err := authorities.closePrivileged(); err != nil {
		return helper.OperationResult{}, err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := dependencies.transitionAdministratorTrustIdentity(admitted.RequesterIdentity()); err != nil {
		return helper.OperationResult{}, fmt.Errorf("enter helper administrator trust identity: %w", err)
	}
	trustHandler, err := dependencies.openAdministratorTrustHandler()
	if err != nil {
		return helper.OperationResult{}, fmt.Errorf("open helper administrator trust handler: %w", err)
	}
	if trustHandler == nil {
		panic("helper administrator trust handler factory returned nil")
	}
	authorities.trustHandler = trustHandler
	return admitted.ExecuteTrust(ctx, trustHandler)
}

// executeAdmittedResolver opens only the resolver authority selected by the authenticated ticket.
func executeAdmittedResolver(
	ctx context.Context,
	admitted helper.AdmittedResolverOperation,
	dependencies runtimeDependencies,
	authorities *runtimeAuthorities,
) (helper.OperationResult, error) {
	resolverHandler, err := dependencies.openResolverHandler()
	if err != nil {
		return helper.OperationResult{}, fmt.Errorf("open helper resolver handler: %w", err)
	}
	if resolverHandler == nil {
		panic("helper resolver handler factory returned nil")
	}
	authorities.resolverHandler = resolverHandler
	return admitted.ExecuteResolver(ctx, resolverHandler)
}

// executeAdmittedLowPorts opens only the low-port authority selected by the authenticated ticket.
func executeAdmittedLowPorts(
	ctx context.Context,
	admitted helper.AdmittedLowPortOperation,
	dependencies runtimeDependencies,
	authorities *runtimeAuthorities,
) (helper.OperationResult, error) {
	lowPortHandler, err := dependencies.openLowPortHandler()
	if err != nil {
		return helper.OperationResult{}, fmt.Errorf("open helper low-port handler: %w", err)
	}
	if lowPortHandler == nil {
		panic("helper low-port handler factory returned nil")
	}
	authorities.lowPortHandler = lowPortHandler
	return admitted.ExecuteLowPorts(ctx, lowPortHandler)
}

// executeAdmittedLoopback constructs only the stateless loopback authority selected by the authenticated ticket.
func executeAdmittedLoopback(
	ctx context.Context,
	admitted helper.AdmittedLoopbackOperation,
	dependencies runtimeDependencies,
) (helper.OperationResult, error) {
	handler := dependencies.newLoopbackIdentityHandler()
	if handler == nil {
		panic("helper loopback handler factory returned nil")
	}
	return admitted.ExecuteLoopback(ctx, handler)
}

// close releases every retained authority in reverse construction order and disarms later cleanup.
func (authorities *runtimeAuthorities) close() error {
	return errors.Join(
		authorities.closeTrustHandler(),
		authorities.closeLowPortHandler(),
		authorities.closeResolverHandler(),
		authorities.closeReplayGuard(),
		authorities.closeTicketRedeemer(),
	)
}

// closePrivileged disarms admission authority before the selected trust handler gains mutation authority.
func (authorities *runtimeAuthorities) closePrivileged() error {
	return errors.Join(
		authorities.closeLowPortHandler(),
		authorities.closeResolverHandler(),
		authorities.closeReplayGuard(),
		authorities.closeTicketRedeemer(),
	)
}

// closeTicketRedeemer closes and forgets the root-owned ticket topology.
func (authorities *runtimeAuthorities) closeTicketRedeemer() error {
	if authorities.redeemer == nil {
		return nil
	}
	redeemer := authorities.redeemer
	authorities.redeemer = nil
	if err := redeemer.Close(); err != nil {
		return fmt.Errorf("close helper ticket redeemer: %w", err)
	}
	return nil
}

// closeReplayGuard closes and forgets the root-owned replay directory.
func (authorities *runtimeAuthorities) closeReplayGuard() error {
	if authorities.replayGuard == nil {
		return nil
	}
	replayGuard := authorities.replayGuard
	authorities.replayGuard = nil
	if err := replayGuard.Close(); err != nil {
		return fmt.Errorf("close helper replay guard: %w", err)
	}
	return nil
}

// closeResolverHandler closes and forgets the root-owned resolver boundary.
func (authorities *runtimeAuthorities) closeResolverHandler() error {
	if authorities.resolverHandler == nil {
		return nil
	}
	handler := authorities.resolverHandler
	authorities.resolverHandler = nil
	if err := handler.Close(); err != nil {
		return fmt.Errorf("close helper resolver handler: %w", err)
	}
	return nil
}

// closeTrustHandler closes and forgets the user-scoped trust boundary.
func (authorities *runtimeAuthorities) closeTrustHandler() error {
	if authorities.trustHandler == nil {
		return nil
	}
	handler := authorities.trustHandler
	authorities.trustHandler = nil
	if err := handler.Close(); err != nil {
		return fmt.Errorf("close helper trust handler: %w", err)
	}
	return nil
}

// closeLowPortHandler closes and forgets the root-owned low-port boundary.
func (authorities *runtimeAuthorities) closeLowPortHandler() error {
	if authorities.lowPortHandler == nil {
		return nil
	}
	handler := authorities.lowPortHandler
	authorities.lowPortHandler = nil
	if err := handler.Close(); err != nil {
		return fmt.Errorf("close helper low-port handler: %w", err)
	}
	return nil
}
