# Frontend

Status: foundation implemented

Last updated: 2026-07-20

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

- pinned Wails v3.0.0-beta.32 hosts the embedded application in an isolated Go 1.26.1 module, matching the Harbor root packages imported by the desktop while keeping Wails dependencies out of the headless root module;
- the tracked GoForj starter snapshot is recorded at commit `aecc0762f9cfcfc9bfbaad3dc4e215afcf858b43`;
- the complete app-owned shadcn-vue primitive layer remains under `src/components/ui`;
- Harbor owns the rail, contextual browser, detail views, compact navigation, status presentation, and command search under its product component and view directories;
- hash routing, Pinia connection epochs and snapshot ordering, light/dark/system themes, typed bridge adapters, a Go-generated deterministic browser fixture, Vitest, and Playwright are wired and exercised;
- the Overview can open the operating system's directory picker through the narrow `AddProject` binding, register the selected checkout with the daemon, refresh the snapshot, and open the resulting project detail;
- an empty-project Overview can run the idempotent `SetupNetwork` action when the daemon advertises network setup, with native consent, bounded retry guidance, and a fresh authoritative snapshot after the attempt;
- project detail can start, stop, and restart the selected project and follow bounded output from only its current durable `forj dev` session through a wake-driven cursor request, with cursor resets and retained-output truncation shown explicitly;
- service detail can follow bounded logs for one current Compose service through authenticated daemon control and narrow Wails bindings, with explicit connecting, waiting, live, reconnecting, ended, unsupported, and failure states;
- project detail can confirm removal through the narrow `RemoveProject` binding, keep one client-owned intent across an uncertain retry, reconcile it against daemon snapshots, and leave the checkout on disk;
- Vite restores the tracked empty embed marker after production builds so the nested Go module also compiles before frontend assets are generated;
- the CI workflow requests root Go validation and nested Wails compilation on Ubuntu, macOS, and Windows, with browser behavior exercised once on Ubuntu before its production assets are reused by the native build matrix.

Frontend-only browser development uses the fixture adapter, with an explicit `Development fixture` marker in the UI. `wails3 dev` and packaged builds use the native status, snapshot, project lifecycle, current project activity, current service logs, resource, and network setup bindings plus typed `harbor:connection` and `harbor:snapshot` events. `AddProject` returns a stopped registration. `SetupNetwork` keeps the fixed machine-global intent in Go and exposes neither helper tickets nor the three-step approval protocol to TypeScript. Current project activity exposes neither historical sessions nor process ownership; a capability-gated request waits on the current cursor until output arrives, closes, or reaches its bounded timeout. The frontend immediately continues that stream, falls back to 750 ms cursor reads with older daemons, and keeps at most 256 KiB visible. A replacement activity read cancels the prior held desktop request, and an invalid early response without cursor progress backs off instead of creating a request loop. Service logs use an independent capability-gated held cursor so switching a service or reconnecting cannot mix output across project sessions. `harbord`, not GoForj or the frontend, opens the local Docker log streams; Wails transports only the bounded typed result. The shared incremental terminal model handles ANSI presentation, carriage-return loaders, multiline cursor redraws, and split Unicode without interpreting process text as HTML; automatic tailing pauses while the user inspects earlier output. `RemoveProject` can complete an inert registration immediately or display durable queued, running, failed, cancelled, and approval-required operation state; project removal does not implement its interactive approval handoff yet. Production fails visibly when bindings or the event runtime are incomplete. A browser production build can request fixture data with `VITE_HARBOR_BROWSER_FIXTURE=true`; the flag is browser-only and cannot override a detected Wails runtime with incomplete bindings. The native tray projects connection status and exposes only Open Harbor and Quit Harbor UI. Interactive tray behavior on macOS, Windows, and supported Linux desktops remains a release gate.

## First-project guide

`FirstProjectGuide.vue` composes the existing creator, networking controller, guarded Start, prerequisite repair panel, project workspace, and resource opener. `projectGuide.ts` owns only session-local visibility, selected registration, and successful link-open intent. It automatically offers the guide from a fresh empty snapshot; a retained snapshot cannot trigger onboarding. Existing workspaces can opt in from Overview or command search. Active guided creation selects its result and returns to Overview; ordinary creation preserves its project-detail destination.

Progress is derived from current project and networking snapshots, not formatted logs or guessed URLs. Pending setup resumes its retained intent, capabilities determine available setup actions, and stale state disables effects. The guide does not add a second setup or launch implementation. Snapshot summaries and duplicate empty/setup cards give way to the guide in an empty workspace. Dismissal and completion are UI-session preferences, not new database facts. The native creation probe waits for Create to become enabled after prerequisite inspection.

## Provider prerequisites

`ProjectCreationPrerequisites` and `ProjectPrerequisites` return the Go-owned `projectreadiness.Report` through additive native methods. Creator adapters own policy, while the shared wizard and `PrerequisitePanel.vue` render generic tool states. Creation requires authoring tools and previews runtime-only needs; Start checks before sending a fresh intent. An explicit Try Start delegates to normal daemon admission when desktop and daemon environments differ. In-flight and uncertain lifecycle intents retain their original reconciliation behavior.

The wizard invalidates results on provider, option, dialog, and schema changes. The store scopes project checks to registration path, request generation, and connection epoch, and discards cached observations on reconnect/disposal. Existing-project metadata reads are bounded and remain inside the registered checkout. The initial GoForj convention recognizes generated root/frontend package manifests; arbitrary custom hooks or frontend paths remain the provider's development runtime responsibility. Networking and prerequisite checks are separate product actions.

## Retained log search

`LogViewer.vue` owns the shared search toolbar, keyboard scope, Copy, and explicit pause/resume controls for project output and service logs. `TerminalOutput.vue` retains its incremental, frame-batched model and safe link opening; `terminalSearch.ts` finds literal case-insensitive ranges across styled runs without introducing HTML interpretation. Search includes only currently rendered retained output, counts every match, and bounds highlights/navigation to the first 5,000 with a visible limit label. Enter and Shift+Enter wrap through those results. Escape closes search and restores viewport focus without silently resuming tailing.

Opening Find pauses scrolling, not collection. New output continues through the existing authenticated cursor transport. Append keeps the selected match anchored by terminal row and offset; retention resets recalculate results, while a project/service/session change clears search and resumes following. Scrolling away pauses following; Resume following or Jump to latest resumes it explicitly. Clear remains local to a service's current desktop transcript, and Copy includes the full visible plain text independently of the search query. Controls and highlights use terminal appearance tokens so mixed app/terminal themes remain readable.

## Project workspace design preview

The local project detail preview makes project identity and lifecycle actions the
header's primary hierarchy. The URL and checkout path remain secondary, with
copy and resource-open behavior preserved. Project navigation uses readable
names and accessible status dots rather than repeated boxed status controls.

The overview uses snapshot-backed application and ready-service counts, resource
shortcuts, an update timestamp, and application/service summaries. Development
output fills the workspace below a compact toolbar; its terminal model, copy,
retained-output notices, and scroll-follow behavior remain intact. Background
artwork is restricted to the global overview rather than project working screens.
The neutral surface hierarchy supports light and dark themes and narrow layouts.
The user accepted this visual direction on 2026-10-18. The implementation
is being prepared for merge; acceptance is not a claim that the design has shipped.

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

The frontend must not receive a raw daemon socket, bearer token, Docker socket, command runner, or unrestricted filesystem access. A typed `harborBridge` is its only product boundary. Container observation and log response bodies remain owned by `harbord`; the native adapter carries only bounded service identities, state, problems, cursors, and text through narrow Wails bindings plus connection and snapshot events. A mock adapter drives browser development, component tests, and deterministic failure states.

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

## Project identity and semantic icons

`ProjectIcon` uses the bundled GoForj iOS artwork while a project is stopped, starting, stopping, failed, or unavailable, without requesting its favicon. Ready, rebuilding, and degraded projects can display their primary App favicon through the existing same-origin backend lookup. The GoForj artwork remains the fallback during discovery, after a failed image, or when the project has no primary App resource. Returning offline restores the local icon immediately.

`KindIcon` and `presentationIcons.ts` define one Lucide vocabulary for service and resource types across project summaries, lists, quick access, and the command palette. Command actions, navigation, settings, and theme choices use the same tinted badges. Glyphs explicitly inherit the badge color so primitive menu styles cannot mute their semantic color. Theme tokens give applications, documentation, data, cache, mail, and other services restrained type colors. These colors identify kind; status badges and dots still communicate health. Tab icons supplement visible labels and remain hidden from assistive technology. The local GoForj asset and its canonical source are recorded in `desktop/frontend/src/assets/brand/README.md`.

## Illustration system

Harbor's maritime illustration language is a first-class, original part of the product identity. [Illustration system](./illustrations.md) defines its purpose, visual vocabulary, metaphor mappings, asset and provenance rules, light/dark treatment, placement constraints, and accessibility contract. Reference products inform the desired warmth and craft but do not supply compositions, branding, or artwork.

Atmospheric scenes are local transparent assets under `src/assets/illustrations/` and enter the shell through the Harbor-owned `HarborIllustration` component, outside the preserved shadcn primitive layer. Its typed image, placement, opacity, size, and fade options keep swapping and composing artwork centralized. The component owns fixed background layering, responsive aspect-preserving sizing, CSS masking, theme-aware blending, the 4–10% opacity boundary, non-interaction, and forced-color suppression. Screens do not reproduce those styles or place decorative artwork over important content.

## Terminal presentation

`terminalTheme.ts` centralizes the neutral near-black surface and vivid ANSI palette shared by streamed logs and the interactive shell. The neutral `#0B0B0B` ground and `#171717` toolbar fit Harbor's charcoal workspace without the former purple cast. Saturated blue, cyan, green, amber, red, and violet ANSI tokens provide contrast while ordinary text and timestamps remain neutral. Explicit 256-color and RGB process output remains faithful. `TerminalOutput` uses 13-pixel Menlo with platform monospace fallbacks, a 1.2 line height, and rows with exactly the same minimum height; do not add independent row padding or a larger minimum height, which separates vertical box-drawing glyphs. Wrapping and safe links still use the bounded incremental terminal model, and copied text remains unstyled.

## Support diagnostics preview

`SupportDiagnostics` is shared by project details and Daemon settings. It assembles an allowlisted local summary, not a serialized snapshot. Optional development output comes only from the selected project's existing authenticated activity feed. `useProjectActivity.fresh` is reset on connection/source invalidation and failed reads, preventing retained UI text from being treated as newly authorized diagnostic output. Exact project/session identity, capability, availability, and output-reset changes invalidate an open preview. Unknown fields and environment/editor sources are excluded.

The report is frozen for review and Copy exports exactly that text. New live output requires Refresh preview to update the report. Redaction precedes Unicode-safe clipping; summaries omit checkout paths, and output redacts known checkout/home paths and common credential patterns. The 40-line/6,000-byte output excerpt and 16-KiB report remain bounded. Review-before-sharing wording describes the limits of pattern-based redaction. Clipboard failures remain local and explicit; no automatic upload, arbitrary file read, support endpoint, or native binding is introduced.

## Project failure details

`ProjectFailurePanel` keeps failure context above the project tabs. It uses the selected session's existing startup events, port-conflict observations, and bounded output, preserving the separate network setup, checkout, and runtime recovery flows. Retry delegates to the same guarded Start action as the header. The disclosed excerpt shows at most 40 lines and 6,000 characters, distinguishes retained output and truncation, and offers navigation to Development output. Copy failure details uses the same excerpt after common credential and home-path redaction, with an explicit review-before-sharing note. Missing output stays unavailable rather than implying a file was read. This presentation adds no daemon capability or unrestricted file access.

## Terminal appearance preferences

The bottom navigation cog opens Settings, with Appearance and Daemon sections. Appearance contains both app and terminal preferences; daemon status and snapshot facts remain under Daemon. Existing `/system` links redirect to `/settings/daemon`. Settings has no entity search or count, and appearance remains available before a daemon snapshot arrives.

Settings → Appearance includes separate app-theme controls and a Terminal appearance card with a live preview and System, Dark, and Light choices. The terminal System option follows Harbor's app appearance, with a split light/dark thumbnail distinguishing it from pinned modes. Both app and terminal choices use System, Dark, Light order. System is the default and tracks Harbor's resolved light/dark appearance, including system changes when the app follows the operating system. An explicit terminal choice remains independent. The preference is stored locally as `harbor.terminal-appearance`; unsupported stored values fall back to System, and unavailable storage still permits a session-local change.

The dark palette uses a neutral `#0B0B0B` well with charcoal chrome. Light mode follows GoForj code styling with `#F6F8FA` with `#1F2328` text and white chrome. Standard ANSI colors and inverse defaults reference shared CSS variables so retained output updates without replay or cursor changes. Explicit extended RGB/color-cube values remain intact. The interactive terminal updates xterm's theme in place, preserving its live shell and scrollback. Appearance never changes a daemon setting or restarts a project.

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
- Vitest coverage for bridge selection, connection epochs, snapshot ordering, status request races, recovery, lookup, failure behavior, wake-driven current-output and service-log streaming, session resets, compatibility polling, and terminal redraw behavior;
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
