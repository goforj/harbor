package main

import (
	"context"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// newTrayConnectionHandler confines the entire native item mutation to the main queue without blocking shutdown.
func newTrayConnectionHandler(ctx context.Context, dispatch func(func()), status *application.MenuItem) func(*application.CustomEvent) {
	return func(event *application.CustomEvent) {
		connection := event.Data.(desktopwire.ConnectionEvent)
		dispatch(func() {
			// Wails cancels its context before destroying trays, so pending events must not touch released items.
			if ctx.Err() != nil {
				return
			}
			// SetLabel updates the native item directly; Menu.Update would destroy and rebuild its implementation.
			status.SetLabel("Harbor: " + string(connection.State))
		})
	}
}
