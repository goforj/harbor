# Frontend

Status: proposed

Last updated: 2026-07-18

Harbor does not need a new frontend foundation. The canonical GoForj Vue starter already provides the Vue, TypeScript, Vite, Tailwind, shadcn-vue, routing, theming, command-menu, and application-shell conventions Harbor needs. Harbor should inherit that foundation and spend its design effort on the operational product.

## Decision

The desktop frontend is a plain Vue SPA embedded by Wails. Its initial source is a pinned snapshot of:

```text
goforj/templates/starter-kits/vue/frontend
```

The initial import records the exact GoForj commit. It copies only tracked files so ignored `node_modules` and local build output cannot enter Harbor. Harbor then owns the copied source; it does not depend on a generator or the GoForj repository at build time.

Do not enable the Vue starter in Harbor's root `.goforj.yml` merely to obtain these files. GoForj correctly renders a normal App frontend under `cmd/<app>/frontend`, while Harbor's desktop boundary is `desktop/frontend`. Import the pinned tree with Git archive semantics after preserving the Wails-generated outer application structure; never recursively copy the starter's working directory.

The starter is the structural authority. Lerd is the initial visual anchor. Harbor uses the starter's shadcn-vue components to express Lerd's compact rail, master/detail geometry, dense rows, thin boundaries, restrained surfaces, status treatment, and responsive list-to-detail behavior. This is a starting point, not a permanent constraint on Harbor's visual identity.

## Preserved starter foundation

These files and conventions should remain verbatim until a concrete Harbor requirement forces a change:

- `components.json`, including the `new-york` style, neutral base, CSS variables, Lucide selection, and aliases;
- `src/components/ui/**`, which is the source-owned shadcn-vue primitive layer;
- `src/lib/utils.ts` and its class composition convention;
- the Vue 3, TypeScript, Vite, Tailwind CSS 4, Reka UI, Lucide, and package-lock baseline;
- the theme preference and system-theme behavior;
- the `@` source alias, component organization, and composition style;
- the command-menu and responsive-sidebar patterns where they fit Harbor behavior.

Harbor components live outside `src/components/ui`. They compose the primitive layer instead of editing it for one screen. This keeps the imported foundation recognizable, makes later shadcn updates reviewable, and prevents product-specific behavior from leaking into low-level controls.

The starter's demonstration application is not part of that preservation boundary. Harbor does not inherit its authentication flows, example component pages, starter navigation, GoForj logo, browser session model, or `/api` proxy. Those are examples for a generated web application, not desktop infrastructure.

## Required desktop adaptations

The browser-served starter and an embedded Wails SPA have a small number of intentional seams:

- retain the Wails-generated Go shell, build metadata, asset embedding, and binding workflow;
- adapt `vite.config.ts` to Wails and remove `goforj.env.ts` proxy assumptions;
- use Vue Router hash history so packaged navigation does not depend on a web server fallback;
- replace the starter auth/session layer with a narrow typed Harbor bridge;
- replace starter routes, views, navigation, and branding with Harbor product surfaces;
- add Pinia for immutable daemon snapshots, connection state, ordered events, and optimistic-operation markers;
- add direct log virtualization only when the log surface is introduced;
- add Vitest, Vue Test Utils, and Playwright with the first Harbor-owned frontend behavior rather than carrying unexercised test dependencies.

The frontend must not receive a raw daemon socket, bearer token, Docker socket, command runner, or unrestricted filesystem access. A typed `harborBridge` is its only product boundary. The production adapter calls narrow Wails bindings and subscribes to Wails events; a mock adapter drives browser development, component tests, and deterministic failure states.

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
| Logs | `ScrollArea`, controls, Harbor virtual list | dense monospaced stream with filters, follow/pause, and explicit gaps |

Harbor-owned components should describe the product rather than wrap every primitive mechanically. Expected first-level components include `HarborRail`, `ContextPane`, `ContextGroup`, `ProjectRow`, `ServiceRow`, `DetailHeader`, `StatusBadge`, `ResourceAction`, and `LogStream`.

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

Every reconnect begins with a fresh snapshot and then applies ordered events after that snapshot's sequence. Components render explicit loading, disconnected, stale, empty, partial, and failed states. Product actions return operation identities and reconcile against daemon state rather than treating a button click as proof of success.

## Testing

Frontend confidence is layered:

- TypeScript checking and the production Vite build on every supported desktop OS;
- Vitest and Vue Test Utils for stores, routing, state presentation, keyboard behavior, and product components through the mock bridge;
- accessibility assertions for dialogs, menus, tabs, tooltips, focus restoration, and state labels;
- Playwright for the responsive three-, two-, and one-pane workflows against deterministic fixtures;
- Wails-native smoke for bindings, events, close/hide behavior, relaunch, and platform WebView differences;
- snapshot/event contract fixtures shared with Go tests so the CLI, desktop backend, and frontend agree on protocol meaning.

Browser tests prove the SPA. They do not replace native Wails smoke on macOS, Linux, and Windows.

## Provenance

The first import records the GoForj source commit and the pinned Lerd research commit. The GoForj starter remains identifiable as the foundation even after Harbor replaces its demonstration application.

Lerd is MIT-licensed. If Harbor ports or substantially adapts Lerd source or class compositions, the repository must include the required copyright and permission notice in `THIRD_PARTY_NOTICES.md`. Visual reference alone does not justify copying Lerd branding or assets.
