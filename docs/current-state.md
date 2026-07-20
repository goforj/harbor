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

The repository is a working vertical slice, not a releasable product. One real GoForj project has been registered, assigned a non-default loopback address, started through the desktop, observed as ready, and shown with live `forj dev` output. Restart recovery, resolver parity, trusted HTTPS installation, and packaging are still active work.

The current slice launches one default App at a direct `http://<assigned-loopback>:<project-port>` URL. Stable `.test` HTTPS, the common ingress path, named Apps, Compose topology projection, private service publications, and native service relays remain approved target design rather than current user-facing behavior.

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
8. ready state publishes the default App/resource at its direct IP-literal HTTP URL; routing primitives reconcile, but `.test` HTTPS is not yet the complete user path;
9. current bounded stdout/stderr is streamed to the desktop and ANSI styling is rendered as safe HTML;
10. Stop or daemon shutdown settles the complete Harbor-owned process scope before deleting session evidence.

Start and Stop are currently exposed through the control protocol and desktop, but not as first-class user CLI commands.

This is intentionally narrow compatibility code, not a second GoForj parser. Registration reads `APP_NAME` from `.env`, then root `project_name` from `.goforj.yml`, then `APP_NAME` from `.env.example`. Runtime discovery reads only `API_HTTP_PORT` or `PORT` from `.env` and then `.env.example`, and verifies the generated `internal/http/runtime.go` default-host contract through bounded Go AST inspection. `Supervisor.Start` executes ordinary `forj dev` directly, without a shell or a managed-session protocol. Do not expand this interim discovery model; replace it with the typed GoForj descriptor and managed-session contracts in [GoForj integration](./goforj-integration.md).

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

Harbor preserves all content outside the marker pair, rejects malformed or repeated markers, updates the file atomically, and rewrites literal local hosts and URLs onto the assigned address. Before launch it removes the file-owned managed keys plus `APP_NAME`, `FORJ_APP`, `FORJ_BUILD_PROGRESS`, `FORJ_COMMAND_PREFIX`, `FORJ_DEV_PLAIN`, and `FORJ_INTERNAL_MANAGED_ENV_KEYS` from its captured ambient environment, then sets `FORJ_DEV_PLAIN=1`. Other ambient values remain inherited normally.

This is deliberately temporary. It makes existing GoForj projects work without special `forj dev` flags while the semantic managed-session protocol remains future work. The managed block currently remains after Stop and unregister; there is no cleanup path yet. Any replacement must preserve ordinary OS-environment precedence and environment reload behavior.

Each accepted launch also replaces `<checkout>/_data/harbor/forj-dev.log`. The file is owner-private (`0600`), bounded to 4 MiB, and ends with a visible truncation marker when output exceeds that bound. Both this trace and the `.env.host` block are interim checkout mutations that depart from the target outside-checkout managed-session storage model.

## Process ownership and restart recovery

Harbor must never interpret a busy port as process ownership.

Current launches use these ownership scopes:

- macOS and Linux: one dedicated Unix session created before `forj dev` starts;
- Windows: one kill-on-close Job Object.

The durable process receipt includes exact birth identity. Unix recovery walks the complete owned session, crossing watcher-created process groups, and revalidates each member's birth and session before signaling it. Windows retains Job Object behavior during one daemon generation and uses exact creation evidence during restart observation.

Before session evidence is retired, Harbor must prove that the complete owned scope is absent. An unresolved scope makes only that project unavailable and route-free while retaining its evidence; it must not prevent `harbord` or unrelated projects from starting.

Legacy state created before complete scope receipts cannot be killed automatically. A port, PID, checkout path, `.env.host`, or command line can corroborate a candidate but cannot independently prove ownership. A retained quarantined session can support an explicit inspect/confirm repair that retires the session only after exact process and socket postconditions. An older listener whose session was already deleted is an unattributed host process; an in-app action for that case must label it as such and cannot pretend to repair durable Harbor ownership.

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

- Linux resolver mutation still uses a blocking, non-cancelable `flock`, and retained stage/quarantine artifacts have no crash-recovery routine; the privileged CI happy path does not prove interrupted-transaction recovery;
- Windows NRPT portable tests and cross-compilation pass, but production helper/daemon providers still select the unsupported implementation; fixed-path PowerShell execution, complete native-field repair policy, helper dependencies, and native lifecycle/cleanup proof remain absent;
- trust-store installation and complete trusted-HTTPS product proof are absent;
- low-port mechanisms and native-port service relays are not complete product paths;
- the required three-real-project, full-stack acceptance test has not been reached.

Current platform capability is therefore uneven:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Loopback, host-conflict, process-scope, and local IPC foundations | Implemented with native proof for the exercised paths | Implemented with hosted native proof for exercised paths | Implemented with hosted native proof for exercised paths |
| Resolver backend | Darwin backend integrated and crash-recovery tested | `systemd-resolved` foundation integrated; crash recovery and stronger native proof remain | NRPT core committed but not wired into production |
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
- current project output with ANSI styling;
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
| GoForj contract | Early/partial: discovery and `forj dev` supervision work; the typed descriptor/session/Compose contract does not. |
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
