# Harbor Desktop

This nested module contains Harbor's replaceable desktop client. It presents daemon state through Wails without owning project lifecycle, networking, or durable runtime state.

Wails v2.13 requires Go 1.25, so the nested module keeps its toolchain and native desktop dependencies out of Harbor's headless module.

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

The fixture is selected only in a normal browser. A Wails runtime without Harbor's native daemon bindings reports an unavailable state instead of presenting fixture data as real machine state.

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

The daemon bindings, native tray, and platform packaging remain later milestone work. The root Harbor module remains independent of Wails and its native dependencies.

## Continuous integration

The repository workflow runs root Go tests on Ubuntu, macOS, and Windows. It separately builds the frontend, runs its browser tests, and then tests, vets, and compiles the nested Wails module on all three operating systems.

These hosted builds prove source and packaging compilation. They do not replace interactive native smoke for WebView behavior, close-to-hide, relaunch, tray integration, or platform trust and networking operations.
