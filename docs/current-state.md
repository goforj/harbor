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

## Repository shape

| Path | Role |
|---|---|
| `cmd/app` | User-facing `harbor` CLI entrypoint. |
| `cmd/harbord` | Foreground daemon entrypoint used by development and future service installation. |
| `cmd/helper` | Privileged, allowlisted host-mutation helper. |
| `cmd/devbootstrap` | Development-only installer for helper artifacts. |
| `cmd/devartifacts` | Builds the current platform's desktop development helper artifacts. |
| `desktop` | Nested Wails v2 Go module. |
| `desktop/frontend` | Vue 3, TypeScript, Vite, Tailwind 4, Pinia, and shadcn/Reka-based SPA. |
| `internal/control` | Versioned local control protocol and client/server transport. |
| `internal/state` | Durable SQLite-backed desired state, journals, and atomic mutations. |
| `internal/reconcile` | Project, network, approval, and route coordination. |
| `internal/projectprocess` | `forj dev` launch, output, process ownership, stop, and restart recovery. |
| `internal/network` | DNS, ingress, TCP relay, address identity, and data-plane primitives. |
| `internal/platform` | Native loopback, conflict detection, resolver, user-path, and helper adapters. |
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

Do not manually run Wire for ordinary development. Checked-in generated wiring must be regenerated only when a provider graph changes.

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
2. registration reads only bounded presentation/runtime metadata and records a stopped project;
3. network setup selects and installs a machine-owned loopback pool;
4. Harbor gives the project a stable primary loopback lease;
5. Start discovers the default App's HTTP port and checks the exact assigned address and port;
6. Harbor writes its bounded network block to `.env.host` and launches `forj dev` without a shell;
7. the daemon records exact process evidence and waits for the App listener to become ready;
8. ready state publishes the App/resource snapshot and reconciles DNS/ingress routes;
9. current bounded stdout/stderr is streamed to the desktop and ANSI styling is rendered as safe HTML;
10. Stop or daemon shutdown settles the complete Harbor-owned process scope before deleting session evidence.

Start and Stop are currently exposed through the control protocol and desktop, but not as first-class user CLI commands.

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

This is deliberately temporary. It makes existing GoForj projects work without special `forj dev` flags while the semantic managed-session protocol remains future work. Any replacement must preserve ordinary OS-environment precedence and environment reload behavior.

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
- activation of the process-local data plane after setup completes;
- project route replacement after lifecycle changes.

Important incomplete work:

- Linux resolver changes exist locally but do not yet recover interrupted filesystem transactions and use a blocking, non-cancelable mutation lock;
- Windows NRPT work exists locally but is not wired into the helper/daemon and has failing focused tests;
- trust-store installation and complete trusted-HTTPS product proof are absent;
- low-port mechanisms and native-port service relays are not complete product paths;
- the required three-real-project, full-stack acceptance test has not been reached.

## Desktop state

The desktop currently provides:

- the Harbor rail/context/detail layout derived from the GoForj Vue starter and adapted toward Lerd's density;
- daemon connection and snapshot updates through typed Wails bindings;
- project registration and removal;
- network setup approval and helper installation/repair prompts;
- project Start/Stop actions and current failure feedback;
- current project output with ANSI styling;
- dark/light/system themes and themed toasts;
- frontend unit tests, browser fixture tests, Playwright smoke, and native module builds.

Tray integration, notifications, project-removal approval handoff, native accessibility proof, signed installers, and release-grade platform smoke remain incomplete.

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

Cross-compilation proves build compatibility only. Privileged loopback, resolver, certificate, DNS, networking, and desktop behavior require native OS-specific workers. The existing workflow uses Ubuntu 24.04, macOS 14, and Windows 2022 hosted jobs, but release support requires the stronger product environments described in `testing.md`.
