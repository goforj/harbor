package main

import (
	"embed"
	"io/fs"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

const desktopSingleInstanceID = "582f5d3e-44b0-4f58-9b0b-2dc8fef96515"

// assets keeps the desktop self-contained and prevents bridge-enabled views from loading remote application code.
//
//go:embed all:frontend/dist
var assets embed.FS

// applicationIcon keeps Linux window identity aligned with the packaged macOS and Windows applications.
//
//go:embed build/appicon.png
var applicationIcon []byte

// main starts the replaceable desktop client without taking ownership of Harbor runtime state.
func main() {
	app := NewApp()

	err := wails.Run(newApplicationOptions(app, assets, runtime.GOOS))
	if err != nil {
		log.Printf("Harbor desktop stopped: %v", err)
	}
}

// newApplicationOptions keeps the complete native window contract inspectable without starting Wails.
func newApplicationOptions(app *App, appAssets fs.FS, platform string) *options.App {
	return &options.App{
		Title:             "GoForj Harbor",
		Width:             1280,
		Height:            820,
		MinWidth:          420,
		MinHeight:         520,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: appAssets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 13, A: 255},
		Menu:             newApplicationMenu(app.presentation, platform),
		Linux: &linux.Options{
			Icon:             applicationIcon,
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               desktopSingleInstanceID,
			OnSecondInstanceLaunch: app.onSecondInstanceLaunch,
		},
		Bind: []interface{}{
			app,
		},
	}
}

// newApplicationMenu exposes only native UI lifecycle actions and preserves ordinary macOS edit and window behavior.
func newApplicationMenu(presentation *presentationController, platform string) *menu.Menu {
	applicationMenu := menu.NewMenu()
	harborMenu := applicationMenu.AddSubmenu("Harbor")
	harborMenu.AddText("Open Harbor", nil, func(*menu.CallbackData) {
		presentation.activate()
	})
	harborMenu.AddSeparator()
	harborMenu.AddText("Quit Harbor UI", keys.CmdOrCtrl("Q"), func(*menu.CallbackData) {
		presentation.quitUI()
	})

	if platform == "darwin" {
		applicationMenu.Append(menu.EditMenu())
		applicationMenu.Append(menu.WindowMenu())
	}

	return applicationMenu
}
