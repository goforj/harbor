# Harbor Desktop

This nested module contains Harbor's replaceable desktop client. It presents daemon state through Wails without owning project lifecycle, networking, or durable runtime state.

## Development

Install the [Wails v2 prerequisites](https://wails.io/docs/gettingstarted/installation) for your platform, then run:

```sh
cd desktop
wails dev
```

The frontend can also run against its deterministic browser bridge:

```sh
cd desktop/frontend
npm ci
npm run dev
```

## Build

Build the desktop application from this module:

```sh
cd desktop
wails build
```

The root Harbor module remains independent of Wails and its native desktop dependencies.
