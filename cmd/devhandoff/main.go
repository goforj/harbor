// Command devhandoff transfers same-user Harbor daemon authority to a source-development watcher.
package main

import (
	"context"
	"time"

	"github.com/goforj/harbor/internal/cmd"
)

// main keeps the source-only handoff out of the production CLI surface.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cmd.NewDaemonClient().Stop(ctx)
}
