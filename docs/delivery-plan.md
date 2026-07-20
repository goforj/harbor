# Delivery Plan

Status: approved delivery contract; progress tracked in [Current implementation state](./current-state.md)

## Sequence

Harbor should be delivered as cross-platform vertical proofs, not a desktop mock followed by late networking discovery.

```text
CI product environment
    → platform proof
    → daemon/state/IPC/helper
    → DNS/TLS/HTTP/native ingress
    → GoForj descriptor and managed session
    → three-project acceptance on every OS
    → Wails v2 desktop, Vue/shadcn frontend, and Go tray integration
    → installers, recovery, signed update, release
```

Each phase has a stop condition. If the same-port loopback model or system resolver path cannot be made safe on a target platform, the project changes its support claim before product UI builds assumptions around it.

## Phase -1: test fleet and evidence contract

Goal: make “tested on macOS, Linux, and Windows” an enforceable claim before platform code exists.

Deliver:

- protected-ref GitHub-hosted controller workflows plus one-job just-in-time product workers;
- actual VM/host provisioning, clean-image restoration or destruction, and fail-fast capacity checks;
- repository/workflow-restricted runner groups and exact-SHA admission for privileged public-repository jobs;
- interactive macOS and Ubuntu desktop sessions plus a Windows local-administrator account running Harbor at filtered medium integrity with UAC;
- supported Docker Engine/Desktop installations and reboot-capable guests;
- a versioned capability/evidence manifest with assertion IDs and a required final verifier;
- a hosted provisioning/reservation preflight that fails the exact head SHA and cancels stranded jobs before an unmatched self-hosted label can queue indefinitely;
- named infrastructure ownership, cost budget, image-update policy, and out-of-band job/destruction logs.

### Exit gate

A synthetic workflow proves one trusted head SHA through hosted mutation jobs and all three product profiles, survives a guest reboot, reports zero skipped assertions, cleans its owned namespace, and destroys/reprovisions every worker. Missing capacity fails visibly rather than leaving an optional or indefinitely queued check.

## Phase 0: platform proof

Goal: prove the product's physical network contract on macOS, Linux, and Windows.

Build the smallest headless harness that can:

- create two stable project loopback identities;
- run an authoritative `.test` DNS server;
- connect the OS resolver to it;
- bind the same native TCP port on both identities;
- route both listeners to distinct private high-port upstreams;
- bind/forward local ports 80 and 443 without running the harness as root/Administrator;
- generate, install, use, and remove a local CA;
- expose two HTTPS domains through one Host/SNI router;
- publish two Docker services privately and reach both through the same native public port;
- restart and clean up every owned host change.

Implement the first required GitHub Actions platform-network jobs during this phase. A manual spike is not enough because these mechanisms will regress with OS images and security updates.

In parallel, import a pinned tracked snapshot of GoForj's Vue starter as the non-authoritative frontend baseline. Keep its source-owned shadcn-vue primitives intact, replace the demonstration application with representative Harbor data behind a mock bridge, and adapt Lerd's pinned density, three-pane layout, and initial tokens through those components. This source becomes the Wails frontend rather than a throwaway mock. It has no daemon authority during the prototype phase and cannot allow UI preference to override a failed platform proof.

Also spike the real helper and service packaging shape on every OS: code-signing/admission APIs, one-use ticket transport, service/launch definitions, atomic replacement, and uninstall ownership. Disposable shell probes may explore an OS mechanism in Phase 0, but production phases must use the typed production helper API.

### Platform decisions to close

| Decision | Evidence required |
|---|---|
| macOS loopback persistence | Alias survives or is deterministically restored after reboot without broad privilege. |
| Linux resolver matrix | systemd-resolved and NetworkManager route only `.test`; declared fallback is explicit. |
| Windows project identities | Supported native method creates, persists/restores, and removes distinct loopback addresses. |
| Windows DNS | NRPT reaches Harbor DNS on port 53 without taking over unrelated DNS. |
| Low ports | Each OS has a narrow, update-safe mechanism and detects foreign listeners. |
| Docker binding | Engine/Desktop publishes only private loopback high ports and Harbor reaches those host ports. |
| Multi-user policy | V1 reliably detects and rejects a second active machine profile. |
| Product test profiles | Exact OS build, architecture, resolver/firewall/trust/browser/Wails/Docker stack is pinned and reproducible. |
| Helper packaging | Signed helper admission, consent, update replacement, and uninstall can be implemented without a broad or persistent grant. |

### Exit gate

The exact same automated test connects to two named endpoints on `:3306` and receives different project responses on all three operating systems. System resolution, trusted HTTPS, low-port behavior, helper admission, and a reboot/restore cycle also pass. Cleanup removes Harbor's exact owned projections while preserving guarded foreign state.

If one platform fails, choose explicitly:

- solve the platform mechanism;
- label that platform limited/preview with translated ports;
- or remove it from the first full-support release.

Do not hide the result behind a different default port.

## Phase 1: headless control plane

Goal: establish one safe authority before adding project behavior.

Deliver:

- scaffold Harbor as a current-valid GoForj project whose default App builds the `harbor` CLI and whose named `harbord` App owns the daemon;
- add bespoke build-only `harbor-helper` and `harbor-installer` entrypoints with dependency allowlists; neither may use the generated App environment/bootstrap/command dispatcher;
- develop Harbor with ordinary standalone `forj dev`, and land/prove the explicit `--no-harbor` bypass in GoForj before any automatic Harbor attachment ships;
- `harbord` per-user lifecycle and lock;
- `harbor` CLI for status, setup, doctor, and daemon control;
- owner-only Unix socket and Windows named-pipe IPC;
- Hello/Welcome protocol negotiation and golden fixtures;
- SQLite schema, migrations, operation journal, and rollback point;
- pure desired/observed planner and serialized reconciliation scheduler;
- semantic platform interfaces;
- one-shot typed helper with ownership-aware install/repair/remove;
- platform-standard config/data/cache/runtime paths;
- structured snapshots, events, diagnostics, and redacted support artifacts.

Use synthetic projects and upstreams. Do not make the daemon parse GoForj configuration during this phase.

### Exit gate

On every OS, two CLI processes and a synthetic worker can concurrently use the daemon, restart it, reconnect from snapshots, recover an interrupted operation, and remove all host state. IPC admission, helper validation, state races, and cleanup pass the required CI matrix.

## Phase 2: network data plane

Goal: turn registered synthetic endpoints into the full stable local network experience.

Deliver:

- stable project and secondary address leases;
- authoritative exact-name `.test` DNS over UDP and TCP;
- OS resolver adapters and repair;
- local CA, leaf issuance, renewal, trust tracking, and removal;
- exact Host/SNI HTTP router with HTTP/2, WebSocket, SSE, and limits;
- native TCP relays with readiness, bounds, and graceful drain;
- low-port integration;
- private upstream port allocation and observation;
- network-change, sleep/resume, conflict, and daemon-restart reconciliation;
- machine and endpoint doctor checks.

### Exit gate

The Phase 0 harness now uses production daemon APIs. Two synthetic projects retain domains and native endpoints across daemon restart and the supported recovery scenarios on every OS. Unknown hosts, foreign listeners, corrupt certificates, stale aliases, and failed helper operations produce deterministic safe outcomes.

## Phase 3: GoForj contract

Goal: integrate through public, versioned GoForj behavior rather than internal knowledge.

Work in `goforj/goforj`:

- add deterministic `forj project:describe --json` with a pure, value-free resolver for environment-selected topology;
- model service requirements/endpoints and consumer bindings with stable non-secret IDs;
- consolidate the current resource projections and correct known URL/health drift;
- distinguish CLI capabilities from checked-in generated-project capabilities;
- add a configurable bind host for metrics and every auxiliary listener Harbor manages;
- add domain-neutral managed mode to `forj dev`;
- add peer-authenticated terminal attach and the semantic all-listener runtime-plan handshake before lifecycle tasks;
- replace flat generated lifecycle work with stable `pre-compose`, typed Compose, `post-compose`, and `post-migrate` phases plus typed Compose-down with `pre/post-compose-down`; reject ambiguous legacy custom tasks until explicitly phased;
- require arbitrary custom runtimes/watchers to declare enforceable typed endpoints or block full mode;
- split command-local Compose publication assignments from the App-final connection overlay;
- add purpose-specific publication keys such as `REDIS_PUBLISH_PORT` with standalone-compatible fallbacks;
- add the trusted final overlay hook to `forj dev` and generated App `LoadEnv`;
- add the actual-publication/route-ready barrier between typed Compose and post-Compose readiness/database setup/migrations;
- materialize session Compose overrides and metrics targets outside the checkout, with a Docker-gateway-only callback relay proved per OS;
- expose typed watcher/process/resource snapshots, events, logs, and actions;
- add an explicit `.goforj.yml` watcher and keep build/process/runtime/public readiness as distinct facts;
- distinguish scoped managed restart from outer process exit/down behavior;
- discover and adopt existing Compose identity/volumes, use built-in or session-override labels, and define explicit identity migration separately;
- remove current Windows build, shell, signal, terminal, and Compose blockers;
- update authoritative templates/generators and regenerate all checked-in mirrors.

Work in Harbor:

- project registration and descriptor validation;
- GoForj version/capability negotiation;
- managed and terminal-owned session adapters;
- private endpoint plan allocation;
- typed Compose observations and logs received from GoForj, with no Harbor Docker-socket access;
- project/App/service/resource snapshots;
- ordered log ingestion and bounded storage;
- start, stop, scoped restart, open, logs, status, remove, and doctor CLI commands.

### Exit gate

The generated-fixture workflow starts three full GoForj projects concurrently on macOS, Linux, and Windows. Each has trusted HTTPS, MySQL `:3306`, Redis `:6379`, mail, the API Reference backed by its generated API Index, Lighthouse, and selected observability links. A separately seeded existing Compose project is adopted with its data intact. Stopping or restarting one does not affect the others, dirty a checkout, or delete volumes. Standalone `forj dev` remains compatible without Harbor, and an older generated project is honestly read-only/upgrade-required until an explicit render supplies the required hooks.

## Phase 4: desktop experience

Goal: add the Lerd-influenced visual control surface without moving authority into it.

Deliver:

- a nested `desktop/` module wired into the same `.goforj.yml` development graph;
- a pinned stable Wails v2 dependency and native build matrix;
- the recorded GoForj starter source commit, Vue 3, TypeScript, Vite, Tailwind CSS 4, app-owned shadcn-vue components backed by Reka UI, and Lucide icons;
- the Phase 0 production-bound frontend source embedded without replacing its primitive layer;
- WebView-safe hash routing, a typed Wails bridge, a matching mock bridge, and Pinia snapshot/event stores;
- Vitest, Vue Test Utils, Playwright, and a production Vite build;
- a virtualized log stream that preserves source ordering, gaps, follow/pause, and accessibility;
- attributed Lerd styling adaptation through Harbor semantic tokens and component composition;
- first-run explanation, setup progress, and end-to-end verification;
- three-pane shell with four destinations: Overview, Projects, Services, and System (including Settings);
- narrow icon rail, grouped dense contextual lists, and persistent detail pane validated by the Phase 0 prototype;
- project address-bar header with Overview, Logs, Network, and Diagnostics as the four primary views;
- App/service ownership sections and resource actions inside Overview rather than duplicate top-level trees;
- Overview/command-palette resource search that returns to the owning App or service;
- start, stop, scoped restart, open, copy, registration, and removal flows;
- ordered live logs with source filters and gap indicators;
- a proved Go tray integration with aggregate status, recent/running projects, quick actions, doctor, and open-window action;
- a same-process tray/window event-loop decision on all three platforms, with a stateless `harbor-tray` daemon client only if those loops cannot coexist reliably;
- default close-to-hide, native-menu/shortcut UI Quit, first-close explanation, no-tray recovery, and best-effort native notifications while the UI is alive;
- accessible keyboard, screen-reader, contrast, and reduced-motion behavior;
- responsive one/two/three-pane layouts;
- UI/CLI parity tests against the same daemon API.

Pin the exact stable Wails v2 and tray releases. The nested desktop module owns their Go, CGO, WebView, and native runtime requirements so they do not raise the headless binaries' minimum Go version or platform dependency floor.

### Exit gate

Closing, crashing, updating, and reopening the desktop does not stop projects or corrupt operations. All essential UI operations have a CLI equivalent, Linux remains usable without a tray, and native desktop smoke passes on all three OS workers.

## Phase 5: lifecycle hardening and release

Goal: make host integration safe to install and durable enough to trust daily.

Deliver:

- signed native installers and packages;
- macOS signing/notarization, Windows Authenticode, and supported Linux packages;
- coordinated signed updater for desktop, daemon, CLI, helper, service definitions, and state schema;
- whole-bundle channel/sequence binding, anti-replay/downgrade checks, key rotation/revocation, and mix-and-match rejection;
- build-once/promote-by-digest provenance with isolated short-lived signing identity;
- transaction-scoped rollback capsule and schema snapshot for a failed, still-uncommitted `N-1` to candidate update;
- reboot, login/logout, sleep/resume, VPN/network-change, Docker restart, and partial-operation recovery;
- exact ownership-based uninstall;
- release-generated platform support evidence;
- clear limited-mode and unsupported-platform messaging;
- performance and resource-use budgets for idle daemon, DNS, ingress, event retention, and desktop.

### Exit gate

The release commit passes installer, reboot, cleanup, Docker, managed-GoForj, and signed-update workflows on every claimed OS. Uninstall preserves unrelated host state, checkouts, and project volumes. The support table is generated from those workflow results.

## Critical path

The work that can invalidate the product is intentionally first:

1. reproducible, secure, reboot-capable product workers for all three OS profiles;
2. Windows stable loopback identities and NRPT/port-53 behavior;
3. macOS durable aliases and low-port behavior;
4. Docker Desktop private publications and the container-to-host metrics relay on both desktop platforms;
5. GoForj Windows managed-session readiness;
6. typed Compose publication barrier before migration;
7. split Compose/App assignment precedence across `forj dev` and generated App loading;
8. existing Compose identity/data adoption;
9. one authoritative resource projection;
10. Wails v2 packaging and cross-platform tray integration.

UI polish, notifications, and convenience actions must not pull effort ahead of these proofs.

## Test-first slices

Each capability lands as a vertical slice:

| Slice | Pure tests | Platform integration | Product assertion |
|---|---|---|---|
| Project identity | lease/collision planner | two real loopback identities per OS | same port can bind twice |
| DNS | record snapshot/server protocol | OS resolver path and cleanup | domains resolve correctly |
| TLS | CA/leaf/persistence | native trust store and low ports | trusted HTTPS works |
| Native service | relay state machine | system DNS plus two native listeners | native port remains unchanged |
| GoForj App | descriptor/runtime plan | live managed session | public URL and private bind separate |
| Compose | requirement/consumer and publication/App mapping | Engine/Desktop generated fixtures plus seeded identity adoption | no public/LAN binding or apparent data loss |
| Recovery | operation journal | daemon/reboot/network fault injection | state heals without foreign mutation |
| Desktop | snapshot-driven view model | native Wails smoke | UI exit does not own runtime |

## Release definition

Harbor v1 is not defined by the presence of a window. It is defined by these invariants:

- one daemon owns state;
- three GoForj projects run concurrently;
- their data-bearing Compose services remain independently owned per project;
- public HTTP/TLS and native service endpoints are stable;
- no repository port or environment file is rewritten;
- GoForj, not Harbor, owns project semantics;
- source and volumes survive stop, unregister, update, and uninstall;
- foreign host state is detected and preserved;
- every supported platform has required GitHub Actions evidence.

## Deferred

After v1 evidence exists, separately design and justify:

- multiple active OS users through a machine-level broker;
- IPv6 parity;
- LAN sharing and public tunnels;
- worktree identities;
- arbitrary frameworks;
- signed third-party service/extension manifests;
- backups, restores, and data-version migrations;
- team-synchronized project aliases;
- remote or browser control;
- a service marketplace;
- MCP/AI control.

None of these should enter the first-release daemon API accidentally. New authority, network exposure, or destructive behavior requires its own threat model and migration plan.
