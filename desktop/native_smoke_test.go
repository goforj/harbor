package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// TestMain allows an explicit native WebView proof with fake daemon state and no host mutations.
func TestMain(m *testing.M) {
	if os.Getenv("HARBOR_NATIVE_DESKTOP_SMOKE") == "1" {
		os.Exit(runNativeDesktopSmoke())
	}
	os.Exit(m.Run())
}

// runNativeDesktopSmoke exercises the built SPA, real bindings, event transport, and native presentation together.
func runNativeDesktopSmoke() int {
	client, _ := connectedTestApp()
	options := newApplicationOptions(client, assets)
	options.SingleInstance = nil
	handler := options.Assets.Handler
	var native *application.App
	var passed atomic.Bool
	options.Assets.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/smoke/focus":
			if !nativeWindow().IsFocused() {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/smoke/ready":
			nativeWindow().Close()
			client.presentation.activate()
			native.Event.Emit(desktopwire.ConnectionEventName, desktopwire.ConnectionEvent{State: ConnectionConnected})
			w.WriteHeader(http.StatusNoContent)
		case "/smoke/result":
			result := r.URL.Query().Get("result")
			fmt.Println("native desktop smoke:", result)
			// Cocoa may terminate before App.Run returns, so failures must exit explicitly.
			if result != "ok" {
				os.Exit(1)
			}
			passed.Store(true)
			w.WriteHeader(http.StatusNoContent)
			go client.presentation.quitUI()
		default:
			// Go test compiles package main under its module path; release binaries use main.App.
			if strings.HasSuffix(r.URL.Path, ".js") || r.URL.Path == "/" || r.URL.Path == "/index.html" {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				for key, values := range response.Header() {
					w.Header()[key] = values
				}
				w.Header().Del("Content-Length")
				w.WriteHeader(response.Code)
				body := response.Body.Bytes()
				if strings.HasSuffix(r.URL.Path, ".js") {
					body = bytes.ReplaceAll(body, []byte("main.App."), []byte(reflect.TypeFor[App]().PkgPath()+".App."))
				} else {
					body = bytes.Replace(body, []byte("</body>"), []byte("<script type=\"module\">"+nativeSmokeProbe+"</script></body>"), 1)
				}
				_, _ = w.Write(body)
			} else {
				handler.ServeHTTP(w, r)
			}
		}
	})
	native = application.New(options)
	window := newDesktopWindow(native)
	native.Menu.SetApplicationMenu(newApplicationMenu(client.presentation, runtime.GOOS))
	configureTray(native, client)
	native.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		client.presentation.startup(native.Context())
	})
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		client.presentation.windowReady()
	})
	timer := time.AfterFunc(25*time.Second, func() { fmt.Println("native desktop smoke: timeout"); os.Exit(1) })
	defer timer.Stop()
	if err := native.Run(); err != nil {
		fmt.Println(err)
		return 1
	}
	if !passed.Load() {
		return 1
	}
	return 0
}

// nativeSmokeProbe waits for the application in the same document that loads its production modules.
const nativeSmokeProbe = `(async () => {
   const deadline = Date.now() + 15000;
   while (!window.go?.main?.App || !document.querySelector('.harbor-workspace')) {
    if (Date.now() > deadline) { await fetch('/smoke/result?result=bootstrap-timeout'); return; }
    await new Promise(resolve => setTimeout(resolve, 100));
   }
   try {
    while (!(await fetch('/smoke/focus')).ok) {
     if (Date.now() > deadline) throw new Error('initial-window-focus-timeout');
     await new Promise(resolve => setTimeout(resolve, 100));
    }
    const status = await window.go.main.App.Status();
    const snapshot = await window.go.main.App.Snapshot();
    if (status.state !== 'ready' || !snapshot.projects.length) throw new Error('invalid native response');
    if (Array.from(document.querySelectorAll('[data-slot=badge]')).some(node => node.textContent.trim() === 'Development fixture')) throw new Error('native page selected fixture');
    window.runtime.EventsOn('harbor:connection', event => {
     fetch('/smoke/result?result=' + (event.state === 'connected' ? 'ok' : 'invalid-event'));
    });
    await fetch('/smoke/ready');
   } catch (error) { await fetch('/smoke/result?result=' + encodeURIComponent(String(error))); }
  })();`
