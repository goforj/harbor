package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// assets keeps the desktop self-contained and prevents bridge-enabled views from loading remote application code.
//
//go:embed all:frontend/dist
var assets embed.FS

// main starts the replaceable desktop client without taking ownership of Harbor runtime state.
func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:             "GoForj Harbor",
		Width:             1280,
		Height:            820,
		MinWidth:          420,
		MinHeight:         520,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 13, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "582f5d3e-44b0-4f58-9b0b-2dc8fef96515",
			OnSecondInstanceLaunch: app.onSecondInstanceLaunch,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Printf("Harbor desktop stopped: %v", err)
	}
}
