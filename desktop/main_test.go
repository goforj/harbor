package main

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestApplicationOptionsPinNativeLifecycle keeps services, identity, and joined UI shutdown independent of the daemon.
func TestApplicationOptionsPinNativeLifecycle(t *testing.T) {
	t.Parallel()
	app := testApp()
	options := newApplicationOptions(app, fstest.MapFS{})
	if options.Name != "GoForj Harbor" || len(options.Icon) == 0 || len(options.Services) != 1 || options.Assets.Handler == nil || options.OnShutdown == nil {
		t.Fatal("incomplete native application contract")
	}
	if options.SingleInstance == nil || options.SingleInstance.UniqueID != desktopSingleInstanceID || options.SingleInstance.OnSecondInstanceLaunch == nil {
		t.Fatal("single-instance identity or activation missing")
	}
	if options.Mac.ApplicationShouldTerminateAfterLastWindowClosed || !options.Windows.DisableQuitOnLastWindowClosed || !options.Linux.DisableQuitOnLastWindowClosed {
		t.Fatal("closing a window may terminate the desktop")
	}
	window := newWindowOptions()
	if window.Name != desktopWindowName || window.Title != "GoForj Harbor" || window.Width != 1280 || window.Height != 820 || window.MinWidth != 420 || window.MinHeight != 520 || window.Hidden {
		t.Fatalf("unexpected window geometry: %+v", window)
	}
	if window.Linux.WebviewGpuPolicy != application.WebviewGpuPolicyNever {
		t.Fatal("Linux rendering policy changed")
	}
}

// TestNativeMenusKeepProjectMutationsOutOfTheTray verifies the bounded status and presentation-only menu contract.
func TestNativeMenusKeepProjectMutationsOutOfTheTray(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"darwin", "linux", "windows"} {
		menu := newApplicationMenu(testApp().presentation, platform)
		if menu.FindByLabel("Open Harbor") == nil || menu.FindByLabel("Quit Harbor UI") == nil {
			t.Fatalf("missing native actions on %s", platform)
		}
		expectedShortcut := "Ctrl+Q"
		if runtime.GOOS == "darwin" {
			expectedShortcut = "Cmd+Q"
		}
		if menu.FindByLabel("Quit Harbor UI").GetAccelerator() != expectedShortcut {
			t.Fatal("quit accelerator changed")
		}
		if platform == "darwin" && (menu.FindByRole(application.EditMenu) == nil || menu.FindByRole(application.WindowMenu) == nil) {
			t.Fatal("macOS native menu roles missing")
		}
	}
	tray := newTrayMenu(testApp().presentation)
	if tray.ItemAt(0).Label() != "Harbor: connecting" || tray.ItemAt(0).Enabled() {
		t.Fatal("tray status must be informational")
	}
	if !tray.ItemAt(1).IsSeparator() || tray.ItemAt(2).Label() != "Open Harbor" || tray.ItemAt(3).Label() != "Quit Harbor UI" || tray.ItemAt(4) != nil {
		t.Fatal("tray must contain only status and UI actions")
	}
}

// TestSecondInstanceUsesTheExistingPresentationLifecycle keeps duplicate processes from creating another control client.
func TestSecondInstanceUsesTheExistingPresentationLifecycle(t *testing.T) {
	t.Parallel()
	var activations atomic.Int32
	app := testApp()
	app.presentation = newPresentationController(func(context.Context) {}, func(context.Context) { activations.Add(1) }, func(context.Context) {})
	options := newApplicationOptions(app, fstest.MapFS{})
	options.SingleInstance.OnSecondInstanceLaunch(application.SecondInstanceData{})
	if activations.Load() != 0 {
		t.Fatal("activated before native startup")
	}
	app.presentation.startup(context.Background())
	options.SingleInstance.OnSecondInstanceLaunch(application.SecondInstanceData{})
	if activations.Load() != 1 {
		t.Fatal("second instance did not activate the existing window")
	}
	app.presentation.shutdown()
	options.SingleInstance.OnSecondInstanceLaunch(application.SecondInstanceData{})
	if activations.Load() != 1 {
		t.Fatal("activated after native shutdown")
	}
}

// TestQuitMenuLeavesDaemonSessionUntilJoinedWailsShutdown proves the native quit action exits only the UI and lets OnShutdown join its client owner.
func TestQuitMenuLeavesDaemonSessionUntilJoinedWailsShutdown(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	connected := make(chan struct{}, 1)
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
		func(_ context.Context, event string, values ...interface{}) {
			if event == connectionEventName && values[0].(ConnectionEvent).State == ConnectionConnected {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	)
	var quits atomic.Int32
	app.presentation = newPresentationController(
		func(context.Context) {},
		func(context.Context) {},
		func(context.Context) { quits.Add(1) },
	)
	applicationOptions := newApplicationOptions(app, fstest.MapFS{})
	app.startup(context.Background())
	select {
	case <-connected:
	case <-time.After(time.Second):
		applicationOptions.OnShutdown()
		t.Fatal("desktop session did not connect")
	}

	app.presentation.quitUI()
	if quits.Load() != 1 {
		t.Fatalf("Wails quit count = %d, want 1", quits.Load())
	}
	if client.closeCount.Load() != 0 {
		t.Fatalf("desktop session closed before Wails shutdown = %d", client.closeCount.Load())
	}
	if _, _, err := app.currentConnection(); err != nil {
		t.Fatalf("native quit action changed daemon connection: %v", err)
	}

	applicationOptions.OnShutdown()
	if client.closeCount.Load() != 1 {
		t.Fatalf("desktop session close count after joined shutdown = %d, want 1", client.closeCount.Load())
	}
	select {
	case <-app.done:
	default:
		t.Fatal("Wails shutdown returned before the desktop owner exited")
	}
}
