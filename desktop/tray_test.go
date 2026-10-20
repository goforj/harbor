package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestTrayConnectionCallbacksDeferAllMutationsToMainQueue exercises overlapping events without allowing event goroutines to mutate Wails state.
func TestTrayConnectionCallbacksDeferAllMutationsToMainQueue(t *testing.T) {
	const count = 128
	menu := newTrayMenu(testApp().presentation)
	status := menu.ItemAt(0)
	queue := make(chan func(), count)
	handler := newTrayConnectionHandler(context.Background(), func(fn func()) { queue <- fn }, status)
	var callbacks sync.WaitGroup
	for index := range count {
		callbacks.Go(func() {
			handler(&application.CustomEvent{Data: desktopwire.ConnectionEvent{State: ConnectionState(fmt.Sprintf("state-%d", index))}})
		})
	}
	returned := make(chan struct{})
	go func() { callbacks.Wait(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("event callbacks waited for the main queue")
	}
	if got := status.Label(); got != "Harbor: connecting" {
		t.Fatalf("label mutated before main dispatch: %q", got)
	}
	seen := make(map[string]bool, count)
	for range count {
		(<-queue)()
		seen[status.Label()] = true
	}
	for index := range count {
		if !seen[fmt.Sprintf("Harbor: state-%d", index)] {
			t.Fatalf("queued mutation missing for event %d", index)
		}
	}
	if menu.ItemAt(0) != status || status.Enabled() || menu.ItemAt(2).Label() != "Open Harbor" || menu.ItemAt(3).Label() != "Quit Harbor UI" {
		t.Fatal("status updates replaced the informational item or presentation actions")
	}
}

// TestTrayConnectionCallbacksSkipQueuedAndLateEventsAfterShutdown keeps pending native work out of Wails teardown.
func TestTrayConnectionCallbacksSkipQueuedAndLateEventsAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := newTrayMenu(testApp().presentation).ItemAt(0)
	queue := make(chan func(), 2)
	handler := newTrayConnectionHandler(ctx, func(fn func()) { queue <- fn }, status)
	handler(&application.CustomEvent{Data: desktopwire.ConnectionEvent{State: ConnectionConnected}})
	cancel()
	handler(&application.CustomEvent{Data: desktopwire.ConnectionEvent{State: ConnectionDisconnected}})
	for range 2 {
		(<-queue)()
		if got := status.Label(); got != "Harbor: connecting" {
			t.Fatalf("queued event mutated item after shutdown: %q", got)
		}
	}
}
