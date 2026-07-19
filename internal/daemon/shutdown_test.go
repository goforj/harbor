package daemon

import (
	"sync"
	"testing"
)

// TestShutdownPublishesOneStableSignalUnderConcurrency verifies duplicate administrative requests are harmless.
func TestShutdownPublishesOneStableSignalUnderConcurrency(t *testing.T) {
	shutdown := NewShutdown()
	requested := shutdown.Requested()
	if requested == nil || requested != shutdown.Requested() {
		t.Fatal("Requested() did not return one stable signal")
	}
	select {
	case <-requested:
		t.Fatal("new shutdown signal was already closed")
	default:
	}

	const callers = 64
	ready := make(chan struct{})
	var requests sync.WaitGroup
	requests.Add(callers)
	for range callers {
		go func() {
			defer requests.Done()
			<-ready
			shutdown.Request()
		}()
	}
	close(ready)
	requests.Wait()

	select {
	case <-requested:
	default:
		t.Fatal("concurrent Request calls did not publish shutdown")
	}
	shutdown.Request()
}
