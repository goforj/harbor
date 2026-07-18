// Package main provides Harbor's bespoke one-shot privileged helper entrypoint.
package main

import (
	"context"
	"io"
	"os"

	"github.com/goforj/harbor/internal/helper"
)

// main fails closed until durable replay storage and platform mutation handlers are composed here.
func main() {
	// The privileged helper must not let inherited ambient configuration influence its authority.
	os.Clearenv()
	if err := run(context.Background(), os.Stdin, os.Stdout, helper.SystemClock{}); err != nil {
		os.Exit(1)
	}
}

// run composes only fail-closed adapters until durable admission and OS handlers are available.
func run(ctx context.Context, reader io.Reader, writer io.Writer, clock helper.Clock) error {
	dispatcher := helper.NewDispatcher(
		helper.UnavailableTicketRedeemer{},
		clock,
		helper.UnavailableReplayGuard{},
		helper.UnavailableLoopbackIdentityHandler{},
	)
	return helper.ServeOnce(ctx, reader, writer, dispatcher)
}
