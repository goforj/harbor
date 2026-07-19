// Package main provides Harbor's bespoke one-shot privileged helper entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/loopbackhandler"
	"github.com/goforj/harbor/internal/helper/replaystore"
	"github.com/goforj/harbor/internal/helper/ticketredeemer"
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

// runtimeDependencies keeps fixed production authority replaceable without redirectable path arguments.
type runtimeDependencies struct {
	authorizeInvocation        func() error
	openTicketRedeemer         func() (closingTicketRedeemer, error)
	openReplayGuard            func() (closingReplayGuard, error)
	newLoopbackIdentityHandler func() helper.LoopbackIdentityHandler
}

// main runs one request with only the fixed durable stores and reviewed platform mutation authority.
func main() {
	// The privileged helper must not let inherited ambient configuration influence its authority.
	os.Clearenv()
	if err := run(context.Background(), os.Stdin, os.Stdout, helper.SystemClock{}, productionDependencies()); err != nil {
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
	}
}

// run opens every durable authority boundary before accepting one request from the caller.
func run(ctx context.Context, reader io.Reader, writer io.Writer, clock helper.Clock, dependencies runtimeDependencies) (runErr error) {
	if err := dependencies.authorizeInvocation(); err != nil {
		return fmt.Errorf("authorize helper invocation: %w", err)
	}

	redeemer, err := dependencies.openTicketRedeemer()
	if err != nil {
		return fmt.Errorf("open helper ticket redeemer: %w", err)
	}
	defer func() {
		if err := redeemer.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close helper ticket redeemer: %w", err))
		}
	}()

	replayGuard, err := dependencies.openReplayGuard()
	if err != nil {
		return fmt.Errorf("open helper replay guard: %w", err)
	}
	defer func() {
		if err := replayGuard.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close helper replay guard: %w", err))
		}
	}()

	dispatcher := helper.NewDispatcher(
		redeemer,
		clock,
		replayGuard,
		dependencies.newLoopbackIdentityHandler(),
	)
	return helper.ServeOnce(ctx, reader, writer, dispatcher)
}
