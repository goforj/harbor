// App-owned Wire injector. EDIT THIS FILE.
// Add app-wide application service providers here when they do not belong to a narrower injector.

package wire

import (
	"github.com/goforj/wire"

	"github.com/goforj/harbor/app"
	"github.com/goforj/harbor/internal/runtime"
)

// appSet is a wire set that provides application-level services and dependencies.
var appSet = wire.NewSet(
	app.NewLifecycleRegistry,
	runtime.NewTimeouts,
)
