package main

import (
	"context"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App owns the narrow Wails lifecycle boundary for the desktop daemon client.
type App struct {
	mu  sync.RWMutex
	ctx context.Context
}

// NewApp creates an empty desktop client whose runtime context arrives during Wails startup.
func NewApp() *App {
	return &App{}
}

// startup retains the Wails context because native window operations require the runtime-owned value.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
}

// shutdown clears the runtime context so a late native callback cannot act on a destroyed window.
func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = nil
}

// onSecondInstanceLaunch restores the existing client because a second desktop must not become another runtime authority.
func (a *App) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return
	}
	runtime.Show(ctx)
}
