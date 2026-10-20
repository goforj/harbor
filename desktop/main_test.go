package main

import (
	"bytes"
	"context"
	"image/png"
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
	if options.Name != "GoForj Harbor" || len(options.Services) != 1 || options.Assets.Handler == nil || options.OnShutdown == nil {
		t.Fatal("incomplete native application contract")
	}
	if options.SingleInstance == nil || options.SingleInstance.UniqueID != nativeSingleInstanceID(runtime.GOOS) || options.SingleInstance.OnSecondInstanceLaunch == nil {
		t.Fatal("single-instance identity or activation missing")
	}
	if options.Mac.ApplicationShouldTerminateAfterLastWindowClosed || !options.Windows.DisableQuitOnLastWindowClosed || !options.Linux.DisableQuitOnLastWindowClosed {
		t.Fatal("closing a window may terminate the desktop")
	}
	window := newWindowOptions(runtime.GOOS)
	if window.Name != desktopWindowName || window.Title != "GoForj Harbor" || window.Width != 1280 || window.Height != 820 || window.MinWidth != 420 || window.MinHeight != 520 || window.Hidden {
		t.Fatalf("unexpected window geometry: %+v", window)
	}
	if window.Linux.WebviewGpuPolicy != application.WebviewGpuPolicyNever {
		t.Fatal("Linux rendering policy changed")
	}
}

// TestWindowApplicationMenuPinsWindowsAccelerators keeps the native Quit shortcut attached without changing Unix presentation.
func TestWindowApplicationMenuPinsWindowsAccelerators(t *testing.T) {
	for _, platform := range []string{"windows", "darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			if got := newWindowOptions(platform).UseApplicationMenu; got != (platform == "windows") {
				t.Fatalf("application menu attached on %s: %t", platform, got)
			}
		})
	}
}

// TestNativeApplicationIconPreservesBundleArtwork prevents the raw tile from replacing macOS catalog rendering.
func TestNativeApplicationIconPreservesBundleArtwork(t *testing.T) {
	for _, platform := range []string{"darwin", "windows", "linux"} {
		icon := nativeApplicationIcon(platform)
		if platform == "darwin" {
			if icon != nil {
				t.Fatal("macOS must retain the bundle icon")
			}
		} else if len(icon) == 0 || !bytes.Equal(icon, applicationIcon) {
			t.Fatalf("missing application artwork on %s", platform)
		}
	}
	if !bytes.Equal(newApplicationOptions(testApp(), fstest.MapFS{}).Icon, nativeApplicationIcon(runtime.GOOS)) {
		t.Fatal("native application options bypassed platform icon policy")
	}
}

// TestTrayTemplateHasTransparentMonochromeArtwork rejects opaque application tiles in the macOS menu bar.
func TestTrayTemplateHasTransparentMonochromeArtwork(t *testing.T) {
	icon, err := png.Decode(bytes.NewReader(trayTemplateIcon))
	if err != nil {
		t.Fatal(err)
	}
	if icon.Bounds().Dx() != 48 || icon.Bounds().Dy() != 48 {
		t.Fatal("template must retain its high-density menu bar dimensions")
	}
	var transparent, opaque int
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			r, g, b, a := icon.At(x, y).RGBA()
			if r != 0 || g != 0 || b != 0 {
				t.Fatal("template must contain only black alpha-mask artwork")
			}
			if a == 0 {
				transparent++
			} else if a == 65535 {
				opaque++
			}
		}
	}
	if transparent < 48*48/2 || opaque == 0 {
		t.Fatal("template needs a transparent surround and a visible anchor")
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

// TestNativeSingleInstanceIDUsesValidLinuxBusName prevents Wails from exiting before its first window.
func TestNativeSingleInstanceIDUsesValidLinuxBusName(t *testing.T) {
	if actual := nativeSingleInstanceID("linux"); actual != "com.goforj.harbor" {
		t.Fatalf("Linux single-instance identity = %q, want domain-style D-Bus name", actual)
	}
	for _, platform := range []string{"darwin", "windows"} {
		if actual := nativeSingleInstanceID(platform); actual != desktopSingleInstanceID {
			t.Fatalf("%s established single-instance identity changed to %q", platform, actual)
		}
	}
}
