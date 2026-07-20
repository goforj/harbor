# Current Implementation State

Status: active development

Last updated: 2026-07-20

This document describes the repository as it works today. The other documents in this directory describe Harbor's intended product and architecture; their phase gates are not claims that the corresponding work is complete.

## Product shape

Harbor is a local development control plane for GoForj projects:

- `harbord` is the sole durable-state writer and runtime reconciler;
- the Go CLI and Wails desktop are clients of the daemon;
- GoForj remains responsible for rendering projects and running their development graph;
- each project owns its own Apps, containers, and data-bearing services;
- Harbor assigns each project a loopback identity so projects can reuse ordinary ports without colliding;
- shared DNS and HTTP/TLS ingress translate stable `.test` names to those project-local runtimes;
- privileged machine changes are delegated to a narrowly scoped, one-shot helper rather than running the daemon or desktop as root.

The repository is a working vertical slice, not a releasable product. One real GoForj project has been registered, assigned a non-default loopback address, started through the desktop, observed as ready, and shown with live `forj dev` output. The current source also observes that checkout's Compose services and streams a selected service's container logs through the desktop without asking the generated App or GoForj CLI to access Docker. Restart recovery, live container topology, resolver parity, trusted HTTPS installation, and packaging are still active work.

The current slice launches one default App at a direct `http://<assigned-loopback>:<project-port>` URL. Once that App is ready, `harbord` uses a local, read-only Docker Engine client to list candidate containers and inspect their immutable facts before projecting active Compose services into the Services UI. Admission requires the exact Compose project, service, and working-directory labels plus the registered canonical checkout; neighboring projects with the same service names are excluded. Harbor separately asks the exact supervising GoForj executable for its bounded resource-only `dev:status` report so framework-owned links can appear beside the directly observed services.

The daemon-owned container adapter currently implements local list, inspect, and log-stream calls. A service-detail request travels through authenticated `harbord` control, the narrow Wails binding, and the typed Vue bridge; the daemon follows admitted current and recreated replicas while preserving a bounded, current-session cursor. This path does not require a GoForj service-log capability, parse Compose YAML, or perform Docker mutations, and neither generated Apps nor frontend code receive Docker access. A Linux-native CI fixture now creates two isolated Compose projects, proves exact checkout admission and neighbor exclusion, follows a recreated target replica, and compares fixture container IDs around Harbor's read-only calls; workflow execution remains required evidence. It is not the required generated-project or macOS/Windows Docker Desktop acceptance proof. Docker event consumption, continuously refreshed service topology, and routing from observed publications remain future work.

## Repository shape

| Path | Role |
|---|---|
| `README.md` | Honest repository entry point for future users and contributors. |
| `AGENTS.md` | Harbor-specific agent routing, invariants, and generated-source rules. |
| `docs` | Approved target design, implementation state, proof contracts, and handoff. |
| `cmd/app` | User-facing `harbor` CLI entrypoint. |
| `cmd/harbord` | Foreground daemon entrypoint used by development and future service installation. |
| `cmd/helper` | Privileged, allowlisted host-mutation helper. |
| `cmd/devbootstrap` | Development-only installer for helper artifacts. |
| `cmd/devartifacts` | Builds the current platform's desktop development helper artifacts. |
| `cmd/platformproof` | Hosted-runner proof executable for native loopback identity behavior. |
| `cmd/launchdrelay` | Darwin launchd relay primitive; not yet a complete low-port product path. |
| `desktop` | Nested Wails v2 Go module. |
| `desktop/frontend` | Vue 3, TypeScript, Vite, Tailwind 4, Pinia, and shadcn/Reka-based SPA. |
| `internal/authority` | Daemon-side authorization boundary for control operations. |
| `internal/control` | Versioned local control protocol and client/server transport. |
| `internal/containerruntime` | Read-only local Docker Engine observation and current-session service-log streams. |
| `internal/host` | Host capability and state observations used by reconciliation. |
| `internal/state` | Durable SQLite-backed desired state, journals, and atomic mutations. |
| `internal/reconcile` | Project, network, approval, and route coordination. |
| `internal/projectprocess` | `forj dev` launch, output, process ownership, stop, and restart recovery. |
| `internal/networksetupapproval`, `internal/networkresolverapproval`, `internal/projectapproval` | Two-step, daemon-bound approval plans for sensitive actions. |
| `internal/network` | DNS, ingress, TCP relay, address identity, and data-plane primitives. |
| `internal/platform` | Native loopback, conflict detection, resolver, user-path, and helper adapters. |
| `internal/trust` | Local CA and certificate material primitives; native trust-store installation is not complete. |
| `internal/testkit/goforjproject` | Headless generated-project fixtures used by native hosted proofs. |
| `migrations/harbord/default` | Embedded migrations for the named `harbord` SQLite connection. |

There are two Go modules and both must be validated: the root module and `desktop`.

## Running from source

From the repository root:

```sh
forj dev
```

The checked-in `.goforj.yml` does the following:

1. builds `./cmd/harbord` as `./bin/harbord`;
2. runs embedded migrations;
3. runs `harbord --foreground`;
4. starts `wails dev` in `desktop`;
5. lets Wails build the current macOS development helper artifacts through its pre-build hook.

The daemon watcher is intentionally non-terminal: a failed daemon run remains visible and waits for the next successful build instead of taking down the entire `forj dev` graph.

The main commands are:

```sh
# Full development graph
forj dev

# Daemon only
go build -o ./bin/harbord ./cmd/harbord
./bin/harbord migrate
./bin/harbord --foreground

# Desktop only, against an existing daemon
cd desktop
wails dev

# User-facing CLI
go run ./cmd/app --help
```

Do not manually run Wire for ordinary development. Checked-in generated wiring must be regenerated only when a provider graph changes. There is no `cmd/installer` yet; development bootstrap code is not a release installer.

## Machine-local state

The default SQLite database locations are:

| Platform | Path |
|---|---|
| macOS | `~/Library/Application Support/GoForj/Harbor/harbor.db` |
| Linux | `${XDG_DATA_HOME}/goforj/harbor/harbor.db`, or `~/.local/share/goforj/harbor/harbor.db` |
| Windows | `%LOCALAPPDATA%\GoForj\Harbor\harbor.db` |

The named database connection is `harbord`. Startup configures SQLite for one writer, WAL, full synchronization, foreign keys, immediate transactions, and a bounded busy timeout. Migrations are embedded and applied by `harbord migrate`; do not add ad hoc startup schema creation.

The current migration stream removes legacy optional derived resource rows, retaining only the readiness-proven `app-http` resource. A future successful project start rebuilds those runtime-only links; no checkout, volume, secret, or operation data is changed.

Harbor also owns platform-specific per-user runtime paths and machine-global privileged paths. The desktop and daemon stay unprivileged. The current macOS/Linux source paths install or repair the privileged helper through explicit operating-system consent; Windows source installation remains incomplete.

## Current project lifecycle

The implemented path is:

1. the desktop or CLI registers a checkout containing `.goforj.yml`;
2. registration reads only bounded presentation metadata and records a stopped project;
3. network setup selects and installs a machine-owned loopback pool;
4. Harbor gives the project a stable primary loopback lease;
5. Start discovers the default App's HTTP port and checks the exact assigned address and port;
6. Harbor writes its bounded network block to `.env.host` and launches `forj dev` without a shell;
7. the daemon records exact process evidence and waits for the App listener to become ready;
8. after App readiness, `harbord` lists local Docker containers and inspects exact candidates; only containers whose Compose project, service, and working-directory labels resolve to the registered canonical checkout become deterministic active service rows;
9. Harbor invokes the exact GoForj executable with `dev:status --json --resources-only` through the checkout and launch environment already owned by the supervisor; supported framework links enrich the project without supplying service state or logs;
10. ready state atomically publishes the default App/resource, admitted framework links, and directly observed services at its direct IP-literal HTTP URL; routing primitives reconcile, but `.test` HTTPS and publication-derived service routes are not yet the complete user path;
11. current bounded stdout/stderr wakes a held, cursor-addressed desktop request as each pipe chunk arrives; the frontend incrementally applies ANSI styling and terminal redraw controls as safe Vue text;
12. selecting a Compose service opens a daemon-owned Docker log follower and streams bounded current-session output through authenticated control, Wails, and Vue; session changes reset the cursor and container recreation is followed without transferring lifecycle authority;
13. Stop or daemon shutdown settles the complete Harbor-owned process scope and its service-log followers before deleting session evidence, then retains observed service identities as stopped.

Start and Stop are exposed through the control protocol, desktop, and first-class `harbor start <project>` / `harbor stop <project>` commands. The CLI prints the authoritative operation state, offers `--json` for scripts, and includes an explicit retry intent after an indeterminate daemon call.

`harbor status <project>` selects one project from the same authoritative snapshot used by desktop clients. Its default view is compact and `--json` prints that project object without a command-specific wrapper.

This is intentionally narrow compatibility code, not a second GoForj parser. Registration reads `APP_NAME` from `.env`, then root `project_name` from `.goforj.yml`, then `APP_NAME` from `.env.example`. Runtime discovery reads only `API_HTTP_PORT` or `PORT` from `.env` and then `.env.example`, and verifies the generated `internal/http/runtime.go` default-host contract through bounded Go AST inspection. `Supervisor.Start` executes ordinary `forj dev` directly, without a shell or a managed-session protocol. Compose intent and mutations remain inside GoForj; Harbor's daemon adapter observes only exact checkout-attributed runtime facts. The resource-only GoForj query is optional enrichment, not a service-state or log transport. GoForj now exposes an initial read-only `forj project:describe --json` schema for static project identity, conventional available Apps, default HTTP runtime intent, and a non-secret normalized topology digest. It does not yet describe dotenv-derived service requirements/resources or provide a managed-session protocol, so Harbor does not consume it yet. Those remaining contracts are described in [GoForj integration](./goforj-integration.md).

## Temporary `.env.host` bridge

The intended GoForj contract uses a typed managed session and a trusted final runtime overlay outside the checkout. That contract is not implemented yet. The current, explicitly accepted bridge writes a single replaceable block at the end of `.env.host`:

```dotenv
# harbor managed: begin
API_HTTP_HOST="127.77.0.10"
DEV_SERVICE_IP_ADDRESS="127.77.0.10"
IP_ADDRESS="127.77.0.10"
LIGHTHOUSE_URL="ws://127.77.0.10:3000/lighthouse/ws/agent"
# harbor managed: end
```

Harbor preserves all content outside the marker pair, rejects malformed or repeated markers, updates the file atomically, and rewrites literal local hosts and URLs onto the assigned address. Before launch it removes the file-owned keys plus `APP_NAME`, `FORJ_APP`, `FORJ_BUILD_PROGRESS`, `FORJ_COMMAND_PREFIX`, and `FORJ_DEV_PLAIN` from its captured ambient environment, then sets `FORJ_DEV_PLAIN=1`. Other ambient values remain inherited normally.

This is deliberately temporary. It makes existing GoForj projects work without special `forj dev` flags while the semantic managed-session protocol remains future work. After a fully settled Harbor-requested shutdown, the supervisor removes only its exact managed block; malformed or foreign marker ownership is left untouched and prevents a false clean stop. Any replacement must preserve ordinary OS-environment precedence and environment reload behavior.

Each accepted launch also replaces `<checkout>/_data/harbor/forj-dev.log`. The file is owner-private (`0600`), bounded to 4 MiB, and ends with a visible truncation marker when output exceeds that bound. Both this trace and the `.env.host` block are interim checkout mutations that depart from the target outside-checkout managed-session storage model.

## Process ownership and restart recovery

Harbor must never interpret a busy port as process ownership.

Current launches use these ownership scopes:

- macOS and Linux: one dedicated Unix session created before `forj dev` starts;
- Windows: one kill-on-close Job Object.

The durable process receipt includes exact birth identity. Unix recovery walks the complete owned session, crossing watcher-created process groups, and revalidates each member's birth and session before signaling it. Windows retains Job Object behavior during one daemon generation and uses exact creation evidence during restart observation.

Before session evidence is retired, Harbor must prove that the complete owned scope is absent. An unresolved scope makes only that project unavailable and route-free while retaining its evidence; it must not prevent `harbord` or unrelated projects from starting.

Legacy state created before complete scope receipts cannot be killed automatically. A port, PID, checkout path, `.env.host`, or command line can corroborate a candidate but cannot independently prove ownership. A retained quarantined session can support an explicit inspect/confirm repair that retires the session only after exact process and socket postconditions. An older listener whose session was already deleted is an unattributed host process; an in-app action for that case must label it as such and cannot pretend to repair durable Harbor ownership.

The retained-session repair is now wired through reconciliation, authenticated control, Wails, and the project detail view. Inspection accepts only a project ID; the daemon derives durable and native facts, retains the native receipt in a short-lived process-local plan bound to the authenticated caller, and returns only `forj dev`, checkout, endpoint, root PID, member count, expiry, and opaque selectors. Confirmation consumes the plan before revalidating every durable and native fence. Drift, ambiguity, expiry, caller mismatch, replacement, or incomplete settlement cannot signal a process and requires a fresh inspection.

The native implementation is currently macOS-only. It requires one exact same-user listener, a dedicated session rooted at the expected `forj dev` process, stable birth/executable/argv/cwd/socket/tree facts, and complete session plus socket settlement after root-only `SIGTERM`; it never escalates to `SIGKILL`. Linux, Windows, and other adapters report unsupported through the same portable contract. Darwin AMD64/ARM64 cross-builds and portable tests pass, but the libproc and signaling path still needs execution on a real macOS host before this behavior is a native support claim. Repair of an already-retired unattributed listener is not implemented.

## Networking state

Implemented foundations include:

- stable project loopback leases and cross-platform loopback adapters;
- host conflict observation and same-port project isolation tests;
- a privileged helper ticket protocol with replay protection and ownership records;
- DNS, HTTP/HTTPS ingress, native TCP relay, local CA, and certificate primitives;
- a durable network setup and resolver approval workflow;
- Darwin resolver ownership and crash-recovery tests;
- a committed Linux `systemd-resolved` drop-in integration foundation;
- a committed, portable Windows NRPT ownership/transaction foundation;
- activation of the process-local data plane after setup completes;
- project route replacement after lifecycle changes.

Important incomplete work:

- Windows NRPT portable tests and cross-compilation pass, but production helper/daemon providers still select the unsupported implementation; complete native-field repair policy, helper dependencies, and native lifecycle/cleanup proof remain absent;
- trust-store installation and complete trusted-HTTPS product proof are absent;
- low-port mechanisms and native-port service relays are not complete product paths;
- the required three-real-project, full-stack acceptance test has not been reached.

Current platform capability is therefore uneven:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Loopback, host-conflict, process-scope, and local IPC foundations | Implemented with native proof for the exercised paths | Implemented with hosted native proof for exercised paths | Implemented with hosted native proof for exercised paths |
| Resolver backend | Darwin backend integrated and crash-recovery tested | `systemd-resolved` foundation includes cancelable locking plus owned stage/quarantine recovery; its root-only lifecycle and crash-recovery test is required in Linux CI, while broader resolver parity remains | NRPT core committed but not wired into production |
| Source helper installation | Automatic Wails development flow | Manual development bootstrap | Not implemented |
| Trusted CA/leaf use | Material primitives only; native trust installation absent | Material primitives only; native trust installation absent | Material primitives only; native trust installation absent |
| Low ports and shared public path | Darwin launchd primitives only | Not complete | Not complete |

## Desktop state

The desktop currently provides:

- the Harbor rail/context/detail layout derived from the GoForj Vue starter and adapted toward Lerd's density;
- daemon connection and snapshot updates through typed Wails bindings;
- project registration and removal;
- network setup approval and helper installation/repair prompts;
- project Start/Stop actions and current failure feedback;
- an explicit retained-runtime inspection and destructive confirmation dialog for quarantined macOS projects, with one-use plans discarded on cancellation, reconnect, navigation, expiry, or any confirmation attempt;
- active conventional Compose services presented from `harbord`'s exact checkout-attributed Docker list/inspect observation after readiness, with observed service identities retained as stopped after shutdown;
- current-session Compose service logs streamed from the local Docker Engine through authenticated daemon control and narrow Wails bindings, with bounded cursors, reconnect/session resets, replica recreation handling, and no Docker access in Vue or generated Apps;
- wake-driven current project output with ANSI styling, carriage-return updates, and multiline terminal redraws;
- dark/light/system themes and themed toasts;
- a reusable, theme-aware Harbor illustration layer with responsive placement, bounded opacity, CSS edge fading, and non-interactive semantics;
- close-to-hide, single-instance relaunch focus, and native `Open Harbor`/`Quit Harbor UI` menu actions;
- frontend unit tests, browser fixture tests, Playwright smoke, and native module builds.

Those desktop lifecycle behaviors have unit coverage, but not release-grade native smoke. Tray integration, notifications, project-removal approval handoff, native accessibility proof, signed installers, and release-grade platform smoke remain incomplete.

## Delivery status

No phase in `delivery-plan.md` has met its full exit gate.

| Phase | Status |
|---|---|
| Test fleet and evidence | Partial: hosted three-OS CI and bounded evidence exist; protected product workers, reboot coverage, and signed evidence do not. |
| Platform proof | Partial: loopbacks, helper, local CA primitives, and Darwin resolver exist; trust, low ports, and resolver parity do not. |
| Headless control plane | Partial but substantial: SQLite, authenticated IPC, operations, registration/removal, recovery, and acceptance coverage exist. |
| Network data plane | Partial: servers, planning, setup, activation, and routes exist; full cross-platform host integration is incomplete. |
| GoForj contract | Early/partial: discovery, `forj dev` supervision, optional framework resource enrichment, direct read-only Docker service observation, daemon-owned service-log streaming, and a static `forj project:describe --json` identity/App descriptor work. Dotenv-derived service/resource intent and the live managed-session Compose contract do not. Docker events, continuously refreshed topology, and publication routing remain future work. |
| Desktop experience | Partial: the working Wails/Vue client covers the main development slice; tray, packaging, and several approvals remain. |
| Release | Not started. |

## Validation

Validate both Go modules and the frontend independently:

```sh
# Root module
go test ./...
go vet ./...

# Nested desktop module
cd desktop
go test ./...
go vet ./...

# Frontend
cd desktop/frontend
npm ci
npm run typecheck
npm test
npx playwright install
npm run test:e2e
npm run build
```

Linux Playwright setup normally uses `npx playwright install --with-deps chromium`.

Cross-compilation proves build compatibility only. Privileged loopback, resolver, certificate, DNS, networking, and desktop behavior require native OS-specific workers.

The main CI workflow runs root, control, loopback, frontend, and desktop checks on Ubuntu 24.04, macOS 14, and Windows 2022 as applicable, with Node 22 for the frontend. The separate hosted platform-network workflow uses Ubuntu 24.04, macOS 15, and Windows 2022. It builds GoForj at pinned revision `bf5f5e65ab64ba25cbd2fe53e42014bff1115a81`, provisions `127.77.254.10` and `127.77.254.11`, and proves that two headlessly generated projects can use port 3000 concurrently through `internal/testkit/goforjproject`, followed by cleanup/evidence checks.

That hosted workflow is pre-provisioned API proof. It does not prove the shipping helper/consent flow, resolver installation, trusted TLS, Compose, reboot recovery, or the three-project product gate. Release support requires the stronger product environments described in [Cross-platform testing](./testing.md).
