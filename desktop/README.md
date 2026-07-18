# Harbor Desktop

This nested module contains Harbor's replaceable desktop client. It presents daemon state through Wails without owning project lifecycle, networking, or durable runtime state.

The nested module uses Go 1.26.1 because it imports Harbor's root control and domain packages, whose module requires that version. Keeping Wails here still prevents its native desktop dependencies from entering Harbor's headless module.

## Development

Install the [Wails v2 prerequisites](https://wails.io/docs/gettingstarted/installation) for your platform and Wails v2.13, then run:

```sh
cd desktop
wails dev
```

For frontend-only development, run the SPA against its deterministic browser fixture:

```sh
cd desktop/frontend
npm ci
npm run dev
```

`wails dev` uses the native desktop bindings and connects to `harbord`. Browser-only development uses a Go-generated, TypeScript-checked fixture for the exact daemon wire shape and marks the page `Development fixture`. Production fails closed when native bindings or the event runtime are incomplete. A browser production build may opt into fixture data explicitly with `VITE_HARBOR_BROWSER_FIXTURE=true`; the flag is for browser builds and cannot override a detected Wails runtime with incomplete bindings.

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
wails build
```

Ubuntu 24.04 requires GTK3, WebKit2GTK 4.1, and the `webkit2_41` Wails build tag. Native packaging also depends on the platform prerequisites documented by Wails.

The native tray remains later milestone work. The scaffolded Wails icons are development placeholders; Harbor branding, signed installers, dependency notices, and native installation/runtime verification remain release work. The root Harbor module remains independent of Wails and its native dependencies.

## Continuous integration

The repository workflow runs root Go tests on Ubuntu, macOS, and Windows. It separately builds the frontend, runs its browser tests, and then tests, vets, and compiles the nested Wails module on all three operating systems.

These hosted builds prove source compilation and Wails' unsigned application-bundle step. They do not produce release installers or replace interactive native smoke for WebView behavior, close-to-hide, relaunch, tray integration, or platform trust and networking operations.
