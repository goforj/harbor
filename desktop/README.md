# Harbor Desktop

This nested module contains Harbor's replaceable desktop client. It presents daemon state through Wails without owning project lifecycle, networking, or durable runtime state.

The nested module uses Go 1.26.1 because it imports Harbor's root control and domain packages, whose module requires that version. Keeping Wails here still prevents its native desktop dependencies from entering Harbor's headless module.

## Development

Harbor is trialing **Wails v3.0.0-beta.32**, including its native tray. This is a pinned prerelease, not a stable-release claim. Install the [Wails v3 prerequisites](https://v3.wails.io/getting-started/installation/) and the matching CLI:

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.32
```

Linux deliberately uses the supported `gtk3` compatibility tag with GTK3 and WebKit2GTK 4.1. On Linux, install the CLI with `go install -tags gtk3 github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.32` too. Start Harbor's daemon and desktop development loops together from the repository root:

```sh
forj dev
```

GoForj rebuilds and restarts `harbord`, applies its embedded migrations before each start, and runs Wails. `desktop/Taskfile.yml` and `cmd/desktopbuild` replace the v2 CLI hooks. macOS source builds create the adjacent helper artifacts without installing or elevating them. Linux and Windows retain their existing source-bootstrap limitations; the installer workflows build their product components separately. Wails watches root and desktop Go sources; Vite refreshes frontend changes in place.

To run only the desktop while an existing daemon is already running, use `wails3 dev` from `desktop/`.

For frontend-only development, run the SPA against its deterministic browser fixture:

```sh
cd desktop/frontend
npm ci
npm run dev
```

`wails3 dev` uses the native desktop bindings and connects to `harbord`. Browser-only development uses a Go-generated, TypeScript-checked fixture for the exact daemon wire shape and marks the page `Development fixture`. Production fails closed when native bindings or the event runtime are incomplete. A browser production build may opt into fixture data explicitly with `VITE_HARBOR_BROWSER_FIXTURE=true`; the flag is for browser builds and cannot override a detected Wails runtime with incomplete bindings.

The Overview's **Add project** action opens the operating system's directory picker and asks the connected daemon to register the selected GoForj checkout. The CLI equivalent is `harbor add [path] [--json]`. Registration records a stopped project; it does not start containers or configure DNS, certificates, or routing.

## Tests

Validate the frontend independently:

```sh
cd desktop/frontend
npm ci
npm run typecheck
npm test
npx playwright install
npm run test:e2e
npm run build
```

On Linux, use `npx playwright install --with-deps` in place of the browser install command so Playwright's system libraries are present too.

Validate the nested Go module separately from the repository root:

```sh
cd desktop
go test ./...
go vet ./...
```

## Build

Build the desktop application from this module:

```sh
cd desktop
wails3 task frontend
wails3 build
```

Ubuntu 24.04 requires GTK3 and WebKit2GTK 4.1. The build command supplies the v3 `gtk3` tag; use `go test -tags gtk3 ./...` and `go vet -tags gtk3 ./...` on Linux. Native packaging also depends on the platform prerequisites documented by Wails.

The Harbor application mark is used by the window and native tray. The tray shows connection status, opens Harbor, and quits only the UI. Closing the window hides it; reopening the application restores it even when a Linux desktop does not display a tray. Interactive tray behavior on each supported desktop and signed distribution remain release gates. The root Harbor module remains independent of Wails and its native dependencies.

The `macOS development package` workflow assembles the first complete Apple-silicon package payload: the desktop, daemon, CLI, output broker, privileged helper, low-port relay, stable daemon launcher, release manifest, and installation record. It is deliberately unsigned and uploaded as a development artifact; it is not a notarized public release. `wails3 build RELEASE=true` enables the package-only release behavior. Ordinary `wails3 build` and `wails3 dev` do not activate installed services. The release-tagged desktop activates the fixed per-user LaunchAgent on first launch, while ordinary source builds continue to use the daemon started by `forj dev`.

## Continuous integration

The repository workflow runs root Go tests on Ubuntu, macOS, and Windows. It separately builds the frontend, runs its browser tests, and then tests, vets, and compiles the nested Wails module on all three operating systems.

The CI matrix builds the native desktop on all three operating systems. Separate development-package workflows retain the existing `.pkg`, `.deb`, and NSIS payload layouts and installer lifecycle checks. Passing compilation does not replace interactive tray/WebView proof or signed-release verification.

An explicit local native smoke uses the production SPA with a fake daemon and makes no networking, trust, project, or installer changes:

```sh
cd desktop
go test -c -o /tmp/harbor-native-smoke.test .
HARBOR_NATIVE_DESKTOP_SMOKE=1 /tmp/harbor-native-smoke.test
# Expected: native desktop smoke: ok
```

Run this only in an interactive desktop session. It opens and restores a window, checks real bindings and event delivery, and exits automatically.
