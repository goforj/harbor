package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	creationTarget := os.Getenv("HARBOR_NATIVE_CREATION_SMOKE_TARGET")
	probe := nativeSmokeProbe
	timeout := 25 * time.Second
	if creationTarget != "" {
		absolute, err := filepath.Abs(creationTarget)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			fmt.Println(err)
			return 1
		}
		tempRoot, err := filepath.EvalSymlinks("/tmp")
		if err != nil || !strings.HasPrefix(parent, tempRoot+string(filepath.Separator)) {
			fmt.Println("creation proof requires a new checkout under /tmp")
			return 1
		}
		creationTarget = absolute
		client = NewApp()
		encoded, _ := json.Marshal(creationTarget)
		probe = strings.ReplaceAll(nativeCreationSmokeProbe, "__TARGET__", string(encoded))
		timeout = 5 * time.Minute
	}
	options := newApplicationOptions(client, assets)
	options.SingleInstance = nil
	handler := options.Assets.Handler
	var native *application.App
	var passed atomic.Bool
	options.Assets.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/smoke/progress":
			fmt.Println("native creation step:", r.URL.Query().Get("step"))
			w.WriteHeader(http.StatusNoContent)
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
			if creationTarget != "" {
				for _, path := range []string{".goforj.yml", "go.mod", filepath.Join("cmd", "app", "frontend", "package.json")} {
					if _, err := os.Stat(filepath.Join(creationTarget, path)); err != nil {
						fmt.Println("creation output verification:", err)
						os.Exit(1)
					}
				}
				fmt.Println("native creation proof: real CLI files and native frontend registration verified at", creationTarget)
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
					body = bytes.Replace(body, []byte("</body>"), []byte("<script type=\"module\">"+probe+"</script></body>"), 1)
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
		if creationTarget != "" {
			client.startup(native.Context())
		} else {
			client.presentation.startup(native.Context())
		}
	})
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		client.presentation.windowReady()
	})
	timer := time.AfterFunc(timeout, func() { fmt.Println("native desktop smoke: timeout"); os.Exit(1) })
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

// nativeCreationSmokeProbe creates a real Vue project through the production SPA and real Wails bindings.
const nativeCreationSmokeProbe = `(async () => {
 const target = __TARGET__;
 const deadline=Date.now()+240000;
 let phase='bootstrap'; const progress=async step=>{phase=step;await fetch('/smoke/progress?step='+encodeURIComponent(step));};
 const wait=async predicate=>{while(!predicate()){if(Date.now()>deadline)throw new Error('creation UI timeout: '+phase+' '+document.body.textContent.slice(0,900));await new Promise(r=>setTimeout(r,100));}};
 const button=label=>Array.from(document.querySelectorAll('button')).find(node=>node.textContent.trim()===label);
 const fill=(label,value)=>{const input=document.querySelector('[aria-label="'+label+'"]');if(!input)throw new Error('missing '+label);input.value=value;input.dispatchEvent(new Event('input',{bubbles:true}));};
 try {
  await wait(()=>window.go?.main?.App && document.querySelector('.harbor-workspace'));
  await progress('overview'); location.hash='/overview';
  await wait(()=>button('New project')&&!button('New project').disabled);
  await progress('open wizard'); button('New project').click();
  await wait(()=>document.querySelector('[aria-label="Project name"]'));
  await progress('project identity'); fill('Project name','Harbor Creation Proof');fill('Go module','example.com/harbor-creation-proof');fill('Project directory',target);
  await new Promise(r=>setTimeout(r,50));button('Continue').click();
  await wait(()=>document.querySelector('[aria-label="Starter kit"]'));
  await progress('stack'); const kit=document.querySelector('[aria-label="Starter kit"]');kit.value='vue';kit.dispatchEvent(new Event('change',{bubbles:true}));
  await new Promise(r=>setTimeout(r,50));button('Continue').click();
  await wait(()=>document.querySelector('[aria-label="Atlas agent support"]'));
  await progress('options'); const atlas=document.querySelector('[aria-label="Atlas agent support"]');atlas.value='skip';atlas.dispatchEvent(new Event('change',{bubbles:true}));
  await new Promise(r=>setTimeout(r,50));button('Continue').click();
  await wait(()=>button('Create project')&&!button('Create project').disabled);await progress('create');await new Promise(r=>setTimeout(r,50));button('Create project').click();
  await wait(()=>!document.querySelector('[role="dialog"]')||document.querySelector('[role="alert"]'));
  if(document.querySelector('[role="dialog"]'))throw new Error(document.querySelector('[role="alert"]')?.textContent||'creation failed');
  await progress('registration'); const snapshot=await window.go.main.App.Snapshot();
  const project=snapshot.projects.find(project=>project.path===target||project.path==='/private'+target);
  if(!project||project.state!=='stopped')throw new Error('new project not registered in stopped state');
  if(Array.from(document.querySelectorAll('[data-slot=badge]')).some(node=>node.textContent.trim()==='Development fixture'))throw new Error('fixture bridge selected');
  const removal=await window.go.main.App.RemoveProject(project.id,'intent-creation-proof-'+Date.now());
  if(removal.operation&&removal.operation.state!=='succeeded')throw new Error('proof registration cleanup incomplete');
  await fetch('/smoke/result?result=ok');
 }catch(error){await fetch('/smoke/result?result='+encodeURIComponent(String(error)));}
})();`
