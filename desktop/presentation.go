package main

import (
	"context"
	"sync"
)

// presentationAction adapts one context-bound Wails presentation operation for deterministic tests.
type presentationAction func(context.Context)

// presentationController owns only the native window and UI-process lifecycle.
type presentationController struct {
	actionMu   sync.Mutex
	mu         sync.Mutex
	ctx        context.Context
	quitting   bool
	unminimise presentationAction
	show       presentationAction
	quit       presentationAction
}

// newPresentationController creates the narrow native boundary shared by menus and relaunch handling.
func newPresentationController(unminimise presentationAction, show presentationAction, quit presentationAction) *presentationController {
	return &presentationController{
		unminimise: unminimise,
		show:       show,
		quit:       quit,
	}
}

// startup publishes the Wails lifecycle context only when native runtime calls are valid.
func (controller *presentationController) startup(ctx context.Context) {
	controller.actionMu.Lock()
	defer controller.actionMu.Unlock()

	controller.mu.Lock()
	controller.ctx = ctx
	controller.quitting = false
	controller.mu.Unlock()
}

// shutdown withdraws the Wails context after every in-flight native presentation action has completed.
func (controller *presentationController) shutdown() {
	controller.actionMu.Lock()
	defer controller.actionMu.Unlock()

	controller.mu.Lock()
	controller.ctx = nil
	controller.quitting = false
	controller.mu.Unlock()
}

// activate restores minimized state before showing the application so every supported platform also raises and focuses Harbor.
func (controller *presentationController) activate() {
	controller.actionMu.Lock()
	defer controller.actionMu.Unlock()

	controller.mu.Lock()
	ctx := controller.ctx
	controller.mu.Unlock()
	if ctx == nil {
		return
	}

	controller.unminimise(ctx)
	controller.show(ctx)
}

// quitUI requests one Wails process exit per active lifecycle without issuing a daemon control operation.
func (controller *presentationController) quitUI() {
	controller.actionMu.Lock()
	defer controller.actionMu.Unlock()

	controller.mu.Lock()
	if controller.ctx == nil || controller.quitting {
		controller.mu.Unlock()
		return
	}
	ctx := controller.ctx
	controller.quitting = true
	controller.mu.Unlock()

	controller.quit(ctx)
}
