# Frontend

Status: foundation implemented

Last updated: 2026-07-18

Harbor does not need a new frontend foundation. The canonical GoForj Vue starter provides the Vue, TypeScript, Vite, Tailwind, shadcn-vue, routing, theming, command-menu, and application-shell conventions Harbor needs. Harbor inherits that foundation and spends its design effort on the operational product.

## Decision

The desktop frontend is a plain Vue SPA embedded by Wails. Its initial source is a pinned snapshot of:

```text
goforj/templates/starter-kits/vue/frontend
```

The initial import records the exact GoForj commit. It copies only tracked files so ignored `node_modules` and local build output cannot enter Harbor. Harbor then owns the copied source; it does not depend on a generator or the GoForj repository at build time.

Do not enable the Vue starter in Harbor's root `.goforj.yml` merely to obtain these files. GoForj correctly renders a normal App frontend under `cmd/<app>/frontend`, while Harbor's desktop boundary is `desktop/frontend`. Import the pinned tree with Git archive semantics after preserving the Wails-generated outer application structure; never recursively copy the starter's working directory.

The starter is the structural authority. Lerd is the initial visual anchor. Harbor uses the starter's shadcn-vue components to express Lerd's compact rail, master/detail geometry, dense rows, thin boundaries, restrained surfaces, status treatment, and responsive list-to-detail behavior. This is a starting point, not a permanent constraint on Harbor's visual identity.

## Implementation status

The first desktop foundation is present under `desktop/`:

- stable Wails v2.13 hosts the embedded application in an isolated Go 1.26.1 module, matching the Harbor root packages imported by the desktop while keeping Wails dependencies out of the headless root module;
- the tracked GoForj starter snapshot is recorded at commit `aecc0762f9cfcfc9bfbaad3dc4e215afcf858b43`;
- the complete app-owned shadcn-vue primitive layer remains under `src/components/ui`;
- Harbor owns the rail, contextual browser, detail views, compact navigation, status presentation, and command search under its product component and view directories;
- hash routing, Pinia connection epochs and snapshot ordering, light/dark/system themes, typed bridge adapters, a Go-generated deterministic browser fixture, Vitest, and Playwright are wired and exercised;
- the Overview can open the operating system's directory picker through the narrow `AddProject` binding, register the selected checkout with the daemon, refresh the snapshot, and open the resulting project detail;
- Vite restores the tracked empty embed marker after production builds so the nested Go module also compiles before frontend assets are generated;
- the CI workflow requests root Go validation and nested Wails compilation on Ubuntu, macOS, and Windows, with browser behavior exercised once on Ubuntu before its production assets are reused by the native build matrix.

Frontend-only browser development uses the fixture adapter, with an explicit `Development fixture` marker in the UI. `wails dev` and packaged builds use the native `Status`, `Snapshot`, `AddProject`, and `OpenResource` bindings plus typed `harbor:connection` and `harbor:snapshot` events. `AddProject` returns a stopped registration; container startup and network configuration are separate lifecycle work. Production fails visibly when those bindings or the event runtime are incomplete. A browser production build can request fixture data with `VITE_HARBOR_BROWSER_FIXTURE=true`; the flag is browser-only and cannot override a detected Wails runtime with incomplete bindings. Tray integration and native packaging evidence remain implementation work rather than capabilities implied by the shell.

## Preserved starter foundation

These files and conventions form the preserved starter boundary. Harbor currently has one documented primitive-level exception: `CommandItem` prefers an explicit `value` when indexing search text so the command palette can find relevant metadata without rendering it in the visible label. The remaining primitive source should stay verbatim until a concrete Harbor requirement forces a change:

- `components.json`, including the `new-york` style, neutral base, CSS variables, Lucide selection, and aliases;
- `src/components/ui/**`, which is the source-owned shadcn-vue primitive layer;
- `src/lib/utils.ts` and its class composition convention;
- the Vue 3, TypeScript, Vite, Tailwind CSS 4, Reka UI, Lucide, and package-lock baseline;
- the theme preference and system-theme behavior;
- the `@` source alias, component organization, and composition style;
- the command-menu and responsive-sidebar patterns where they fit Harbor behavior.

Harbor components live outside `src/components/ui`. They compose the primitive layer instead of editing it for one screen. This keeps the imported foundation recognizable, makes later shadcn updates reviewable, and prevents product-specific behavior from leaking into low-level controls.

The starter's demonstration application is not part of that preservation boundary. Harbor does not inherit its authentication flows, example component pages, starter navigation, GoForj logo, browser session model, or `/api` proxy. Those are examples for a generated web application, not desktop infrastructure.

## Desktop adaptations

The browser-served starter and an embedded Wails SPA have a small number of intentional seams:

- retain the Wails-generated Go shell, build metadata, asset embedding, and binding workflow;
- adapt `vite.config.ts` to Wails and remove `goforj.env.ts` proxy assumptions;
- use Vue Router hash history so packaged navigation does not depend on a web server fallback;
- replace the starter auth/session layer with a narrow typed Harbor bridge;
- replace starter routes, views, navigation, and branding with Harbor product surfaces;
- add Pinia for immutable daemon snapshots, connection state, ordered events, and optimistic-operation markers;
- exercise Harbor-owned frontend behavior with Vitest, Vue Test Utils, and Playwright rather than carrying unexercised test dependencies.

The frontend must not receive a raw daemon socket, bearer token, Docker socket, command runner, or unrestricted filesystem access. A typed `harborBridge` is its only product boundary. The native adapter is limited to narrow Wails bindings plus connection and snapshot events; a mock adapter drives browser development, component tests, and deterministic failure states.

## Shell and component map

The first Harbor shell keeps the GoForj starter's application composition while replacing its generic dashboard layout with the three-pane operational workspace.

| Harbor surface | GoForj/shadcn foundation | Lerd anchor |
|---|---|---|
| Destination rail | `Button`, `Tooltip`, `Separator`, shell layout CSS | fixed 56px rail, icon-only destinations, selected state, bottom utility actions |
| Context pane | `ScrollArea`, `Input`, `Separator`, `Item`, `Badge` | 224px at medium width, 256px at large width, grouped dense rows |
| Detail pane | `Tabs`, `Button`, `DropdownMenu`, `Breadcrumb`, `Separator` | persistent flexible canvas, compact header, actions adjacent to identity |
| Project/service row | `Item`, `Badge`, `Tooltip` | one-line identity, leading state, restrained metadata, full-row selection |
| Status | `Badge` plus Harbor wrapper | text and shape in addition to green, amber, red, and neutral color |
| Command palette | `CommandDialog` | global resource and action search with keyboard-first operation |
| Narrow layout | `Sheet` or `Drawer` plus router state | one-surface list-to-detail navigation and compact destination bar |
| Destructive action | `AlertDialog` | explicit scope and consequence near the operation |
| Progress and feedback | `Skeleton`, `Progress`, `Sonner` | quiet loading, persistent failure state, transient success feedback |

Harbor-owned components describe the product rather than wrapping every primitive mechanically. The first shell includes `HarborRail`, `HarborMobileNav`, `ContextPane`, `EntityRow`, `HarborCommandMenu`, `ThemeMenu`, and `StatusBadge`; destination views own their project-, service-, and system-specific composition.

## Visual tokens

Lerd's dimensions and contrast are an initial calibration target, but Harbor owns the semantic tokens in `src/style.css`. Product components consume tokens such as background, foreground, card, muted, border, accent, destructive, sidebar, and status states. They should not scatter copied product-name colors or literal gray/red utilities across views.

The first visual prototype maps Lerd's reference values into semantic Harbor names:

| Reference value | Initial Harbor use |
|---|---|
| `#0d0d0d` | dark background |
| `#161616` | dark card, popover, rail, and contextual-pane surface |
| `#262626` | dark border and sidebar border |
| `#404040` | dark muted and inactive-control treatment |
| `#ff2d20` | primary selection and focus accent |
| `#e02419` | primary hover accent |

These values are an honest prototype anchor, not a compatibility promise or a second product's named palette.

The initial theme should preserve:

- a compact 56px destination rail;
- a 224px/256px contextual pane;
- small operational typography and 40–44px dense selectable rows;
- thin neutral separators rather than nested card chrome;
- a restrained accent for selection and action focus;
- explicit ready, working, degraded, failed, stopped, and unavailable treatments;
- equivalent light and dark hierarchy;
- system theme by default, with stored light/dark overrides.

Harbor branding, icons, terminology, and status semantics remain Harbor-owned. Lerd's logo, product icons, copy, and PHP-specific surfaces do not enter the product.

## State flow

Pinia stores consume daemon snapshots and events; they do not become a second authority.

```text
harbord snapshot/event
        ↓
typed Wails service and event adapter
        ↓
harborBridge
        ↓
Pinia snapshot stores
        ↓
Harbor views composed from shadcn-vue primitives
```

Every reconnect starts a new connection epoch. The last replacement snapshot remains visible but explicitly stale until the first validated snapshot from that connection arrives, and sequence suppression applies only within that connection. Components render explicit loading, disconnected, stale, empty, partial, and failed states. Product actions return operation identities and reconcile against daemon state rather than treating a button click as proof of success.

## Testing

Frontend confidence is layered. The implemented browser layer currently includes:

- TypeScript checking and a production Vite build;
- Vitest coverage for bridge selection, connection epochs, snapshot ordering, status request races, recovery, lookup, and failure behavior;
- Playwright coverage for navigation, command metadata search, compact utility access, and the three-, two-, and one-pane workflows against deterministic fixtures;
- a checked-in TypeScript contract artifact generated and validated by Go, consumed by the native bridge, compile-checked with `satisfies`, and exercised by frontend tests; it declares exact Wails method arity and parameter/result types, binds event names to payload types, and covers every connection payload plus active and terminal state examples.

The remaining native layer requires:

- green hosted evidence from the three-platform workflow on each reviewed revision;
- accessibility assertions for dialogs, menus, tabs, tooltips, focus restoration, and state labels;
- Wails-native smoke for bindings, events, close/hide behavior, relaunch, and platform WebView differences.

Browser tests prove the SPA. They do not replace native Wails smoke on macOS, Linux, and Windows.

## Provenance

The first import records the GoForj source commit and the pinned Lerd research commit. The GoForj starter remains identifiable as the foundation even after Harbor replaces its demonstration application.

Lerd is MIT-licensed. If Harbor ports or substantially adapts Lerd source or class compositions, the repository must include the required copyright and permission notice in `THIRD_PARTY_NOTICES.md`. Visual reference alone does not justify copying Lerd branding or assets.
