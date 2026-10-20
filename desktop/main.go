package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"runtime"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const desktopSingleInstanceID = "582f5d3e-44b0-4f58-9b0b-2dc8fef96515"
const desktopWindowName = "harbor"

// assets keeps bridge-enabled views inside the packaged application.
//
//go:embed all:frontend/dist
var assets embed.FS

// applicationIcon supplies Harbor's identity on platforms without the macOS asset catalog.
//
//go:embed build/appicon.png
var applicationIcon []byte

// trayTemplateIcon lets macOS tint the transparent anchor for the current menu bar appearance.
//
//go:embed build/tray-template.png
var trayTemplateIcon []byte

// main runs the replaceable desktop client while harbord retains durable authority.
func main() {
	client := NewApp()
	native := application.New(newApplicationOptions(client, assets))
	window := newDesktopWindow(native)
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) { client.presentation.windowReady() })
	native.Menu.SetApplicationMenu(newApplicationMenu(client.presentation, runtime.GOOS))
	configureTray(native, client)
	native.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { client.startup(native.Context()) })
	if err := native.Run(); err != nil {
		log.Printf("Harbor desktop stopped: %v", err)
	}
}

// newDesktopWindow keeps close-to-hide identical in production and native smoke verification.
func newDesktopWindow(native *application.App) *application.WebviewWindow {
	window := native.Window.NewWithOptions(newWindowOptions())
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		window.Hide()
		event.Cancel()
	})
	return window
}

// newApplicationOptions binds only Harbor's client and keeps UI shutdown separate from daemon shutdown.
func newApplicationOptions(client *App, appAssets fs.FS) application.Options {
	return application.Options{
		Name: "GoForj Harbor", Icon: nativeApplicationIcon(runtime.GOOS),
		Assets:         application.AssetOptions{Handler: application.AssetFileServerFS(appAssets)},
		Services:       []application.Service{application.NewService(client)},
		SingleInstance: &application.SingleInstanceOptions{UniqueID: nativeSingleInstanceID(runtime.GOOS), OnSecondInstanceLaunch: client.onSecondInstanceLaunch},
		OnShutdown:     func() { client.shutdown(context.Background()) },
		Mac:            application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		Windows:        application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:          application.LinuxOptions{DisableQuitOnLastWindowClosed: true, ProgramName: "goforj-harbor", ApplicationID: "com.goforj.harbor"},
	}
}

// nativeSingleInstanceID preserves established locks while using a valid Linux D-Bus name.
func nativeSingleInstanceID(platform string) string {
	if platform == "linux" {
		return "com.goforj.harbor"
	}
	return desktopSingleInstanceID
}

// nativeApplicationIcon leaves the macOS Dock icon to the compiled bundle catalog instead of overriding it with a square bitmap.
func nativeApplicationIcon(platform string) []byte {
	if platform == "darwin" {
		return nil
	}
	return applicationIcon
}

// newWindowOptions preserves the existing desktop geometry and rendering policy.
func newWindowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name: desktopWindowName, Title: "GoForj Harbor",
		Width: 1280, Height: 820, MinWidth: 420, MinHeight: 520,
		BackgroundColour: application.RGBA{Red: 13, Green: 13, Blue: 13, Alpha: 255},
		Linux:            application.LinuxWindow{WebviewGpuPolicy: application.WebviewGpuPolicyNever},
	}
}

// newApplicationMenu retains native edit/window roles and the same explicit UI-only actions on every platform.
func newApplicationMenu(presentation *presentationController, platform string) *application.Menu {
	result := application.NewMenu()
	harbor := result.AddSubmenu("Harbor")
	harbor.Add("Open Harbor").OnClick(func(*application.Context) { presentation.activate() })
	harbor.AddSeparator()
	harbor.Add("Quit Harbor UI").SetAccelerator("CmdOrCtrl+Q").OnClick(func(*application.Context) { presentation.quitUI() })
	if platform == "darwin" {
		result.AddRole(application.EditMenu)
		result.AddRole(application.WindowMenu)
	}
	return result
}

// newTrayMenu exposes status and presentation actions without project or daemon mutations.
func newTrayMenu(presentation *presentationController) *application.Menu {
	menu := application.NewMenu()
	menu.Add("Harbor: connecting").SetEnabled(false)
	menu.AddSeparator()
	menu.Add("Open Harbor").OnClick(func(*application.Context) { presentation.activate() })
	menu.Add("Quit Harbor UI").OnClick(func(*application.Context) { presentation.quitUI() })
	return menu
}

// configureTray projects validated daemon connection events into the native status menu.
func configureTray(native *application.App, client *App) {
	tray := native.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		tray.SetIcon(applicationIcon)
	}
	tray.SetTooltip("GoForj Harbor")
	tray.OnClick(client.presentation.activate)
	menu := newTrayMenu(client.presentation)
	tray.SetMenu(menu)
	native.Event.On(desktopwire.ConnectionEventName, func(event *application.CustomEvent) {
		connection := event.Data.(desktopwire.ConnectionEvent)
		menu.ItemAt(0).SetLabel("Harbor: " + string(connection.State))
		menu.Update()
	})
}

// nativeWindow resolves the mandatory named window for all presentation callbacks.
func nativeWindow() application.Window {
	window, exists := application.Get().Window.GetByName(desktopWindowName)
	if !exists {
		panic("Harbor native window is not registered")
	}
	return window
}

// emitNativeEvent preserves the Go-owned payload contract while adapting to Wails v3's event manager.
func emitNativeEvent(_ context.Context, name string, values ...interface{}) {
	application.Get().Event.Emit(name, values...)
}

// openNativeURL opens a URL only after Harbor's existing resource policy has approved it.
func openNativeURL(_ context.Context, url string) {
	if err := application.Get().Browser.OpenURL(url); err != nil {
		log.Printf("Open Harbor resource: %v", err)
	}
}

// chooseNativeDirectory attaches the project picker to Harbor's window without exposing generic file operations.
func chooseNativeDirectory(_ context.Context, options directoryDialogOptions) (string, error) {
	return application.Get().Dialog.OpenFile().SetTitle(options.Title).CanChooseFiles(false).CanChooseDirectories(true).CanCreateDirectories(options.CanCreateDirectories).ResolvesAliases(options.ResolvesAliases).AttachToWindow(nativeWindow()).PromptForSingleSelection()
}

// restoreNativeWindow reverses minimization before an explicit activation.
func restoreNativeWindow(context.Context) { nativeWindow().UnMinimise() }

// showNativeWindow raises and focuses Harbor after a tray, menu, or second-instance activation.
func showNativeWindow(context.Context) {
	nativeWindow().Show()
	nativeWindow().Focus()
}

// quitNativeUI releases the presentation lock before v3 synchronously invokes shutdown hooks.
func quitNativeUI(context.Context) {
	native := application.Get()
	go native.Quit()
}
