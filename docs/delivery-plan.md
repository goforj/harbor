# Delivery Plan

Status: proposed

## Sequence

Harbor should be delivered as cross-platform vertical proofs, not a desktop mock followed by late networking discovery.

```text
platform proof
    → daemon/state/IPC/helper
    → DNS/TLS/HTTP/native ingress
    → GoForj descriptor and managed session
    → three-project acceptance on every OS
    → Wails desktop and tray
    → installers, recovery, signed update, release
```

Each phase has a stop condition. If the same-port loopback model or system resolver path cannot be made safe on a target platform, the project changes its support claim before product UI builds assumptions around it.

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

### Exit gate

The exact same automated test connects to two named endpoints on `:3306` and receives different project responses on all three operating systems. System resolution and trusted HTTPS also pass. Cleanup returns each machine to its baseline.

If one platform fails, choose explicitly:

- solve the platform mechanism;
- label that platform limited/preview with translated ports;
- or remove it from the first full-support release.

Do not hide the result behind a different default port.

## Phase 1: headless control plane

Goal: establish one safe authority before adding project behavior.

Deliver:

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

- add deterministic `forj project:describe --json`;
- consolidate the current resource projections and correct known URL/health drift;
- add a configurable bind host for metrics and any auxiliary listener Harbor manages;
- add domain-neutral managed mode to `forj dev`;
- add the semantic runtime-plan handshake before pre-tasks;
- add the final runtime overlay to `forj dev` and generated App `LoadEnv`;
- expose typed watcher/process/resource snapshots, events, logs, and actions;
- distinguish scoped managed restart from outer process exit/down behavior;
- provide deterministic Compose identity/labels and supported Compose invocation;
- remove current Windows build, shell, signal, terminal, and Compose blockers;
- update authoritative templates/generators and regenerate all checked-in mirrors.

Work in Harbor:

- project registration and descriptor validation;
- GoForj version/capability negotiation;
- managed and terminal-owned session adapters;
- private endpoint plan allocation;
- constrained Docker/Compose observation;
- project/App/service/resource snapshots;
- ordered log ingestion and bounded storage;
- start, stop, scoped restart, open, logs, status, remove, and doctor CLI commands.

### Exit gate

The generated-fixture workflow starts three full GoForj projects concurrently on macOS, Linux, and Windows. Each has trusted HTTPS, MySQL `:3306`, Redis `:6379`, mail, API Index, Lighthouse, and selected observability links. Stopping or restarting one does not affect the others or delete volumes. Standalone `forj dev` remains compatible without Harbor.

## Phase 4: desktop experience

Goal: add the Lerd-influenced visual control surface without moving authority into it.

Deliver:

- pinned Wails v3 desktop module and native build matrix;
- first-run explanation, setup progress, and end-to-end verification;
- three-pane Overview, Projects, Services, and System shell;
- project address-bar header and Overview, Apps, Services, Resources, Logs, Network, and Diagnostics tabs;
- start, stop, scoped restart, open, copy, registration, and removal flows;
- ordered live logs with source filters and gap indicators;
- tray with aggregate status, recent/running projects, quick actions, doctor, and open-window action;
- native notifications for high-value failure/recovery events;
- accessible keyboard, screen-reader, contrast, and reduced-motion behavior;
- responsive one/two/three-pane layouts;
- UI/CLI parity tests against the same daemon API.

Because Wails v3 is alpha as of the design date, pin the exact version and re-evaluate its status before this phase. Wails instability may keep the desktop labeled preview, but it cannot delay or weaken the headless daemon contract.

### Exit gate

Closing, crashing, updating, and reopening the desktop does not stop projects or corrupt operations. All essential UI operations have a CLI equivalent, Linux remains usable without a tray, and native desktop smoke passes on all three OS workers.

## Phase 5: lifecycle hardening and release

Goal: make host integration safe to install and durable enough to trust daily.

Deliver:

- signed native installers and packages;
- macOS signing/notarization, Windows Authenticode, and supported Linux packages;
- coordinated signed updater for desktop, daemon, CLI, helper, service definitions, and state schema;
- verified rollback after a failed post-update check;
- reboot, login/logout, sleep/resume, VPN/network-change, Docker restart, and partial-operation recovery;
- exact ownership-based uninstall;
- release-generated platform support evidence;
- clear limited-mode and unsupported-platform messaging;
- performance and resource-use budgets for idle daemon, DNS, ingress, event retention, and desktop.

### Exit gate

The release commit passes installer, reboot, cleanup, Docker, managed-GoForj, and signed-update workflows on every claimed OS. Uninstall preserves unrelated host state, checkouts, and project volumes. The support table is generated from those workflow results.

## Critical path

The work that can invalidate the product is intentionally first:

1. Windows stable loopback identities and NRPT/port-53 behavior;
2. macOS durable aliases and low-port behavior;
3. Docker Desktop private publications on both desktop platforms;
4. GoForj Windows managed-session readiness;
5. runtime-overlay precedence across both `forj dev` and generated App loading;
6. one authoritative resource projection;
7. Wails v3 stability and packaging.

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
| Compose | service plan mapping | Engine/Desktop generated fixtures | no public/LAN binding |
| Recovery | operation journal | daemon/reboot/network fault injection | state heals without foreign mutation |
| Desktop | snapshot-driven view model | native Wails smoke | UI exit does not own runtime |

## Release definition

Harbor v1 is not defined by the presence of a window. It is defined by these invariants:

- one daemon owns state;
- three GoForj projects run concurrently;
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
