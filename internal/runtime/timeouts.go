package runtime

import (
	"time"

	"github.com/goforj/env/v2"
)

// Timeouts holds app-level lifecycle timeout policy resolved once at startup.
type Timeouts struct {
	shutdownTimeout            time.Duration
	schedulerSubprocessTimeout time.Duration
}

// NewTimeouts resolves lifecycle timeout policy from env once at boot.
func NewTimeouts() *Timeouts {
	appTimeout := env.GetDuration("APP_SHUTDOWN_TIMEOUT", "30s")
	schedulerSubprocessTimeout := env.GetDuration("SCHEDULER_SUBPROCESS_SHUTDOWN_TIMEOUT", appTimeout.String())
	return &Timeouts{
		shutdownTimeout:            appTimeout,
		schedulerSubprocessTimeout: schedulerSubprocessTimeout,
	}
}

// ShutdownTimeout returns the default app shutdown budget.
func (s *Timeouts) ShutdownTimeout() time.Duration {
	if s.shutdownTimeout <= 0 {
		return 30 * time.Second
	}
	return s.shutdownTimeout
}

// SchedulerSubprocessShutdownTimeout returns the shutdown budget for scheduler-owned subprocesses.
func (s *Timeouts) SchedulerSubprocessShutdownTimeout() time.Duration {
	if s.schedulerSubprocessTimeout > 0 {
		return s.schedulerSubprocessTimeout
	}
	return s.ShutdownTimeout()
}
