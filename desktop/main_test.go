package main

import (
	"context"
	"io/fs"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

// TestApplicationOptionsPinNativeLifecycle verifies close-to-hide, single-instance, assets, bindings, and native menu ownership together.
func TestApplicationOptionsPinNativeLifecycle(t *testing.T) {
	t.Parallel()

	app := testApp()
	appAssets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("Harbor")}}
	applicationOptions := newApplicationOptions(app, appAssets, "linux")

	if applicationOptions.Title != "GoForj Harbor" || applicationOptions.Width != 1280 || applicationOptions.Height != 820 {
		t.Fatalf("window identity and size = (%q, %d, %d)", applicationOptions.Title, applicationOptions.Width, applicationOptions.Height)
	}
	if applicationOptions.MinWidth != 420 || applicationOptions.MinHeight != 520 {
		t.Fatalf("minimum window size = (%d, %d), want (420, 520)", applicationOptions.MinWidth, applicationOptions.MinHeight)
	}
	if !applicationOptions.HideWindowOnClose || applicationOptions.StartHidden {
		t.Fatalf("window visibility options = hide-on-close %t, start-hidden %t", applicationOptions.HideWindowOnClose, applicationOptions.StartHidden)
	}
	if applicationOptions.BackgroundColour == nil || *applicationOptions.BackgroundColour != (options.RGBA{R: 13, G: 13, B: 13, A: 255}) {
		t.Fatalf("background = %+v, want Harbor dark background", applicationOptions.BackgroundColour)
	}
	if applicationOptions.Linux == nil || !reflect.DeepEqual(applicationOptions.Linux.Icon, applicationIcon) || len(applicationOptions.Linux.Icon) == 0 {
		t.Fatalf("Linux icon options = %+v, want embedded Harbor icon", applicationOptions.Linux)
	}
	if applicationOptions.Linux.WebviewGpuPolicy != linux.WebviewGpuPolicyNever {
		t.Fatalf("Linux GPU policy = %v, want preserved software rendering policy", applicationOptions.Linux.WebviewGpuPolicy)
	}
	if applicationOptions.AssetServer == nil {
		t.Fatal("asset server options = nil")
	}
	asset, err := fs.ReadFile(applicationOptions.AssetServer.Assets, "index.html")
	if err != nil || string(asset) != "Harbor" {
		t.Fatalf("embedded asset = (%q, %v), want Harbor", asset, err)
	}
	if applicationOptions.OnStartup == nil || applicationOptions.OnShutdown == nil {
		t.Fatal("Wails lifecycle callbacks are incomplete")
	}
	if applicationOptions.SingleInstanceLock == nil || applicationOptions.SingleInstanceLock.UniqueId != desktopSingleInstanceID || applicationOptions.SingleInstanceLock.OnSecondInstanceLaunch == nil {
		t.Fatalf("single-instance options = %+v, want Harbor callback and stable ID", applicationOptions.SingleInstanceLock)
	}
	if len(applicationOptions.Bind) != 1 || applicationOptions.Bind[0] != app {
		t.Fatalf("bindings = %v, want only the Harbor app", applicationOptions.Bind)
	}
}

// TestApplicationMenuPinsPlatformShape verifies Harbor actions stay identical while only macOS receives its native edit and window roles.
func TestApplicationMenuPinsPlatformShape(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		platform string
		roles    []menu.Role
	}{
		{platform: "darwin", roles: []menu.Role{menu.EditMenuRole, menu.WindowMenuRole}},
		{platform: "linux", roles: nil},
		{platform: "windows", roles: nil},
	} {
		test := test
		t.Run(test.platform, func(t *testing.T) {
			t.Parallel()

			applicationMenu := newApplicationMenu(testApp().presentation, test.platform)
			if len(applicationMenu.Items) != 1+len(test.roles) {
				t.Fatalf("top-level item count = %d, want %d", len(applicationMenu.Items), 1+len(test.roles))
			}
			harbor := applicationMenu.Items[0]
			if harbor.Label != "Harbor" || harbor.Type != menu.SubmenuType || harbor.SubMenu == nil {
				t.Fatalf("Harbor menu = %+v, want named submenu", harbor)
			}
			if len(harbor.SubMenu.Items) != 3 {
				t.Fatalf("Harbor item count = %d, want 3", len(harbor.SubMenu.Items))
			}
			open := harbor.SubMenu.Items[0]
			separator := harbor.SubMenu.Items[1]
			quit := harbor.SubMenu.Items[2]
			if open.Label != "Open Harbor" || open.Type != menu.TextType || open.Accelerator != nil || open.Click == nil {
				t.Fatalf("open item = %+v, want unaccelerated Open Harbor callback", open)
			}
			if separator.Type != menu.SeparatorType {
				t.Fatalf("separator type = %v, want separator", separator.Type)
			}
			if quit.Label != "Quit Harbor UI" || quit.Type != menu.TextType || quit.Click == nil || !reflect.DeepEqual(quit.Accelerator, keys.CmdOrCtrl("Q")) {
				t.Fatalf("quit item = %+v, want Cmd/Ctrl+Q Quit Harbor UI callback", quit)
			}
			for index, role := range test.roles {
				if applicationMenu.Items[index+1].Role != role {
					t.Fatalf("native role %d = %v, want %v", index, applicationMenu.Items[index+1].Role, role)
				}
			}
		})
	}
}

// TestApplicationMenuAndRelaunchShareActivation proves every native reopen path uses the same restore-and-focus sequence.
func TestApplicationMenuAndRelaunchShareActivation(t *testing.T) {
	t.Parallel()

	var unminimises atomic.Int32
	var shows atomic.Int32
	app := testApp()
	app.presentation = newPresentationController(
		func(context.Context) { unminimises.Add(1) },
		func(context.Context) { shows.Add(1) },
		func(context.Context) {},
	)
	applicationMenu := newApplicationMenu(app.presentation, "linux")

	applicationMenu.Items[0].SubMenu.Items[0].Click(nil)
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if unminimises.Load() != 0 || shows.Load() != 0 {
		t.Fatalf("restore calls before startup = (%d unminimise, %d show), want none", unminimises.Load(), shows.Load())
	}

	app.presentation.startup(context.Background())
	applicationMenu.Items[0].SubMenu.Items[0].Click(nil)
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if unminimises.Load() != 2 || shows.Load() != 2 {
		t.Fatalf("restore calls = (%d unminimise, %d show), want two shared activations", unminimises.Load(), shows.Load())
	}

	app.presentation.shutdown()
	applicationMenu.Items[0].SubMenu.Items[0].Click(nil)
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if unminimises.Load() != 2 || shows.Load() != 2 {
		t.Fatalf("restore calls after shutdown = (%d unminimise, %d show), want no new activations", unminimises.Load(), shows.Load())
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
	applicationOptions := newApplicationOptions(app, fstest.MapFS{}, "linux")
	applicationOptions.OnStartup(context.Background())
	select {
	case <-connected:
	case <-time.After(time.Second):
		applicationOptions.OnShutdown(context.Background())
		t.Fatal("desktop session did not connect")
	}

	applicationOptions.Menu.Items[0].SubMenu.Items[2].Click(nil)
	if quits.Load() != 1 {
		t.Fatalf("Wails quit count = %d, want 1", quits.Load())
	}
	if client.closeCount.Load() != 0 {
		t.Fatalf("desktop session closed before Wails shutdown = %d", client.closeCount.Load())
	}
	if _, _, err := app.currentConnection(); err != nil {
		t.Fatalf("native quit action changed daemon connection: %v", err)
	}

	applicationOptions.OnShutdown(context.Background())
	if client.closeCount.Load() != 1 {
		t.Fatalf("desktop session close count after joined shutdown = %d, want 1", client.closeCount.Load())
	}
	select {
	case <-app.done:
	default:
		t.Fatal("Wails shutdown returned before the desktop owner exited")
	}
}
