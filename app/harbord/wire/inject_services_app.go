// App-owned Wire injector. EDIT THIS FILE.
// Add app-wide application service providers here when they do not belong to a narrower injector.

package wire

import (
	"github.com/goforj/wire"

	"github.com/goforj/harbor/app/harbord"
	"github.com/goforj/harbor/internal/runtime"
)

// appSet is a wire set that provides application-level services and dependencies.
var appSet = wire.NewSet(
	harbordapp.NewLifecycleRegistry,
	runtime.NewTimeouts,
)
