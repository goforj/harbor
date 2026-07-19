package daemon

import "sync"

// Shutdown coordinates one process-local daemon shutdown request without performing teardown inline.
type Shutdown struct {
	once      sync.Once
	requested chan struct{}
}

// NewShutdown creates an open shutdown signal shared by control dispatch and the daemon runner.
func NewShutdown() *Shutdown {
	return &Shutdown{requested: make(chan struct{})}
}

// Request publishes shutdown once and returns immediately so response dispatch never owns daemon cleanup.
func (shutdown *Shutdown) Request() {
	shutdown.once.Do(func() {
		close(shutdown.requested)
	})
}

// Requested closes after the first shutdown request and remains closed for every observer.
func (shutdown *Shutdown) Requested() <-chan struct{} {
	return shutdown.requested
}
