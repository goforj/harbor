package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPresentationCallbacksRespectLifecycle keeps native runtime calls inside the Wails lifecycle that supplies their context.
func TestPresentationCallbacksRespectLifecycle(t *testing.T) {
	t.Parallel()

	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("lifecycle"), "first")
	var mu sync.Mutex
	actions := make([]string, 0, 4)
	contexts := make([]context.Context, 0, 4)
	record := func(action string) presentationAction {
		return func(received context.Context) {
			mu.Lock()
			actions = append(actions, action)
			contexts = append(contexts, received)
			mu.Unlock()
		}
	}
	controller := newPresentationController(record("unminimise"), record("show"), record("quit"))

	controller.activate()
	controller.quitUI()
	if len(actions) != 0 {
		t.Fatalf("actions before startup = %v, want none", actions)
	}

	controller.startup(ctx)
	controller.activate()
	controller.quitUI()
	controller.quitUI()
	controller.shutdown()
	controller.activate()
	controller.quitUI()

	wantActions := []string{"unminimise", "show", "quit"}
	if len(actions) != len(wantActions) {
		t.Fatalf("actions = %v, want %v", actions, wantActions)
	}
	for index, want := range wantActions {
		if actions[index] != want {
			t.Fatalf("action %d = %q, want %q", index, actions[index], want)
		}
		if contexts[index] != ctx {
			t.Fatalf("context %d did not come from startup", index)
		}
	}

	secondContext := context.WithValue(context.Background(), contextKey("lifecycle"), "second")
	controller.startup(secondContext)
	controller.quitUI()
	if len(actions) != 4 || actions[3] != "quit" || contexts[3] != secondContext {
		t.Fatalf("actions after restart = %v, want a quit using the new lifecycle", actions)
	}
}

// TestPresentationConcurrentActivationSerializesRestorePairs prevents native window calls from interleaving across relaunch and menu callbacks.
func TestPresentationConcurrentActivationSerializesRestorePairs(t *testing.T) {
	t.Parallel()

	const activationCount = 32
	var mu sync.Mutex
	active := false
	overlapped := false
	actions := make([]string, 0, activationCount*2)
	controller := newPresentationController(
		func(context.Context) {
			mu.Lock()
			if active {
				overlapped = true
			}
			active = true
			actions = append(actions, "unminimise")
			mu.Unlock()
		},
		func(context.Context) {
			mu.Lock()
			actions = append(actions, "show")
			active = false
			mu.Unlock()
		},
		func(context.Context) {},
	)
	controller.startup(context.Background())

	var group sync.WaitGroup
	group.Add(activationCount)
	for range activationCount {
		go func() {
			defer group.Done()
			controller.activate()
		}()
	}
	group.Wait()
	controller.shutdown()

	if overlapped {
		t.Fatal("concurrent activation interleaved native restore calls")
	}
	if len(actions) != activationCount*2 {
		t.Fatalf("action count = %d, want %d", len(actions), activationCount*2)
	}
	for index := 0; index < len(actions); index += 2 {
		if actions[index] != "unminimise" || actions[index+1] != "show" {
			t.Fatalf("activation pair %d = %v, want unminimise then show", index/2, actions[index:index+2])
		}
	}
}

// TestPresentationShutdownJoinsActivation prevents a native callback from using the Wails context after shutdown returns.
func TestPresentationShutdownJoinsActivation(t *testing.T) {
	t.Parallel()

	showStarted := make(chan struct{})
	releaseShow := make(chan struct{})
	controller := newPresentationController(
		func(context.Context) {},
		func(context.Context) {
			close(showStarted)
			<-releaseShow
		},
		func(context.Context) {},
	)
	controller.startup(context.Background())

	activationDone := make(chan struct{})
	go func() {
		controller.activate()
		close(activationDone)
	}()
	<-showStarted

	shutdownDone := make(chan struct{})
	shutdownStarted := make(chan struct{})
	go func() {
		close(shutdownStarted)
		controller.shutdown()
		close(shutdownDone)
	}()
	<-shutdownStarted
	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned while native activation was still running")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseShow)
	<-activationDone
	<-shutdownDone
	controller.activate()
}

// TestPresentationConcurrentQuitIsIdempotent keeps repeated native shortcuts from racing multiple Wails exits.
func TestPresentationConcurrentQuitIsIdempotent(t *testing.T) {
	t.Parallel()

	var quits atomic.Int32
	controller := newPresentationController(
		func(context.Context) {},
		func(context.Context) {},
		func(context.Context) { quits.Add(1) },
	)
	controller.startup(context.Background())

	var group sync.WaitGroup
	group.Add(32)
	for range 32 {
		go func() {
			defer group.Done()
			controller.quitUI()
		}()
	}
	group.Wait()

	if quits.Load() != 1 {
		t.Fatalf("quit count = %d, want 1", quits.Load())
	}
}

// TestPresentationShutdownJoinsQuit prevents UI teardown from invalidating the context used by a native quit request.
func TestPresentationShutdownJoinsQuit(t *testing.T) {
	t.Parallel()

	quitStarted := make(chan struct{})
	releaseQuit := make(chan struct{})
	quitFinished := make(chan struct{})
	controller := newPresentationController(
		func(context.Context) {},
		func(context.Context) {},
		func(context.Context) {
			close(quitStarted)
			<-releaseQuit
			close(quitFinished)
		},
	)
	controller.startup(context.Background())

	go controller.quitUI()
	<-quitStarted

	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		close(shutdownStarted)
		controller.shutdown()
		close(shutdownDone)
	}()
	<-shutdownStarted
	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned while native quit was still running")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseQuit)
	<-quitFinished
	<-shutdownDone
	controller.quitUI()
}
