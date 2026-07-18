# Product Design

Status: proposed

## Product

Harbor is the desktop and command-line home for local GoForj development. It makes a generated project feel installed on the developer's machine:

- the project has memorable local domains;
- HTTPS works without browser warnings;
- its database, cache, mail, and observability services retain their native ports;
- apps and services can be started, stopped, inspected, and repaired from one place;
- the terminal remains a first-class control surface;
- GoForj's API Index, Lighthouse, resource registry, and development watcher remain the underlying framework capabilities.

Harbor is not a general container desktop or another application framework. Its value comes from knowing the GoForj project model and presenting it coherently without taking ownership away from GoForj.

## Problem

A single local project can use familiar ports without difficulty. Several projects cannot all bind the same host address and ports at once. Developers then have to coordinate arbitrary port offsets across applications, databases, caches, mail tools, and dashboards. Those offsets leak into `.env` files, bookmarks, documentation, database clients, and team conventions.

The friction is larger than the port collision itself:

- a project has no stable identity outside its checkout;
- starting the app and starting its infrastructure are separate mental models;
- tool links are rediscovered from terminal output;
- TLS, DNS, Docker, and framework health fail in different places;
- desktop controls can drift from CLI behavior if each implements its own orchestration;
- closing a desktop window can accidentally become coupled to the lifetime of development services.

Harbor provides one machine-local control plane and keeps project-specific knowledge in GoForj.

## Principles

### Stable outside, flexible inside

Developers use stable domains and native public ports. Harbor may assign private high ports to app processes and container publications internally. Those implementation ports are not written into the project and are not part of the developer contract.

### One action, one meaning

The same operation must have the same semantics in the desktop, tray, and CLI because all three call the same daemon API. `Stop project` cannot mean “stop the app” in one surface and “delete containers” in another.

### Project files remain the owner's

Harbor discovers `.goforj.yml` and asks GoForj for a versioned project description. It does not add `harbor.yml`, rewrite `.env`, edit Compose files, change Go dependencies, or run generation during discovery.

### Registration is not execution

Selecting a directory is read-only. Harbor may validate its path and ask `forj` for a static, secret-free description, but it does not run project lifecycle tasks until the user starts the project. `.goforj.yml` can contain trusted shell commands, so scanning must never execute it.

### Removal is not deletion

Removing a project from Harbor unregisters domains and machine-local state. It never deletes the checkout or persistent container volumes. Destructive data removal is not part of the first release.

### The desktop is optional

Projects and the daemon continue to run if the desktop window or tray client exits. Users can perform all essential operations with `harbor` from a terminal. Tray support varies by Linux desktop, so it cannot be the only recovery path.

### Diagnostics before magic

Harbor may repair state it owns, but it does not kill foreign processes, replace unrelated DNS configuration, or guess around an occupied port. It identifies the conflicting owner and explains the next action.

### Compatibility is explicit

Harbor negotiates capabilities with `forj`. An older GoForj version receives a useful compatibility error or a reduced, visibly labeled experience. Harbor does not scrape help text or formatted development output to pretend a stable integration exists.

## User model

| Term | Meaning |
|---|---|
| Project | A registered checkout with a valid GoForj project description. |
| App | The default GoForj App or a named App from the project's app model. |
| Service | Infrastructure whose lifecycle GoForj assigns to the project, normally through Compose. |
| Resource | A launchable or connectable projection attached to an owning App or service, such as an App URL, database endpoint, Mailpit, Lighthouse, Grafana, or API Reference backed by the API Index. |
| Session | One active `forj dev` lifecycle, owned by a terminal or by Harbor. |
| Endpoint | A public Harbor domain and protocol mapped to an internal app or service listener. |
| Project identity | A stable Harbor ID, project slug, and loopback address lease stored outside the checkout. |

The hierarchy shown to users is:

```text
Project
├── Apps
│   ├── app
│   │   └── URLs, API Reference, Lighthouse
│   └── named Apps and their resources
├── Services
│   ├── databases and their native endpoints
│   ├── cache and mail endpoints
│   └── observability services and dashboards
└── Session
    └── lifecycle, watchers, operations, and logs
```

Resources are a searchable action catalog and projection, not a third ownership tree. Their canonical detail belongs to the App or service that owns them. Harbor may aggregate them for quick open/copy actions, but it must not flatten named Apps into unrelated projects, duplicate service state, or present every URL as a container service.

## First run

First run is an explicit setup, not a silent installer side effect.

1. Harbor explains the local changes it needs: a `.test` resolver path, a local certificate authority, low-port ingress, and loopback identities for native services.
2. It verifies the installed Go version, `forj`, Docker/Compose when the project needs containers, and platform prerequisites.
3. The user approves the narrowly scoped privileged setup. A separate one-shot helper runs with root/Administrator authority to install the required loopback, resolver, trust, and low-port state, then exits. Harbor shows the exact categories of host state it will own and how uninstall reverses them. If background reconciliation later needs another approved mutation, it records `requires approval`; only an interactive desktop or CLI action opens the OS consent flow, and cancellation remains safely retryable.
4. Harbor starts the per-user daemon and verifies DNS, HTTP, TLS, and a native TCP loopback probe end to end.
5. The user chooses a project directory or runs `harbor add [path] [--json]`.
6. Harbor records the checkout as a stopped project. App, service, container, and network discovery happen in later lifecycle work rather than being implied by registration.

The setup result has three visible states:

- `ready`: the full stable-domain and native-port contract works;
- `limited`: Harbor can run projects but a named capability, such as trusted HTTPS, is unavailable;
- `blocked`: the core promise cannot be met, with a diagnostic and repair action.

Limited mode must never silently change public ports while showing a healthy state.

## Register a project

Registration deliberately has a smaller boundary than project startup:

1. The desktop opens the operating system's directory picker, or the CLI accepts `harbor add [path] [--json]`.
2. Harbor canonicalizes the selected path, requires a regular `.goforj.yml`, and derives basic presentation metadata without executing project code or applying the project's environment to the daemon.
3. The daemon allocates an opaque project ID and records a stopped project with no Apps, services, resources, or routes.
4. Repeating the same canonical path returns the existing registration instead of creating a duplicate.

Project description, endpoint planning, containers, DNS, certificates, and ingress belong to later lifecycle work. A successful registration does not imply that any of them are ready.

Registration does not start Apps, run migrations, pull images, or update dependencies.

Compatibility classification such as `read-only` or `upgrade required` belongs to later project discovery. Registration does not auto-render a checkout or claim that it already supports Harbor-managed startup.

Harbor supports direct registration, not broad “park every folder” behavior, in the first release. A future directory scan may list directories containing `.goforj.yml`, but it remains read-only until each project is registered.

## Start a project

Starting a project is one operation with observable phases:

1. Revalidate the project descriptor and configuration hash.
2. Reconcile the project's address, DNS, certificate, endpoint, and private-port leases.
3. Start `forj dev` in managed mode and negotiate the active Apps, listener plan, and session-scoped assignments.
4. Let GoForj build Apps, run phased pre-Compose tasks, and start its typed Compose phase.
5. Observe the actual private publications, activate native routes, and acknowledge the route barrier.
6. Let GoForj run post-Compose readiness, database setup/migrations, post-migrate tasks, and its watcher graph.
7. Publish each endpoint only when its own upstream is ready.

The project detail shows progress without translating it into fake certainty. For example, `building app`, `starting mysql`, and `waiting for readiness` are distinct from `ready`.

If a user starts `forj dev` in a terminal, it can attach to Harbor before lifecycle tasks run. The terminal remains the session owner; Harbor supplies endpoints and observes the session. Standalone `forj dev` remains unchanged when Harbor is absent or explicitly disabled.

## Stop and restart

Operations have precise scopes:

- `Restart App` uses a typed GoForj session action and does not tear down Compose.
- `Restart watcher` restarts only the selected GoForj watcher when the session advertises that capability.
- `Restart project` asks GoForj to restart the managed development graph without treating the transition as a normal exit that runs `down_on_exit` between app restarts.
- `Stop project` gracefully stops the GoForj session and applies phased configured down behavior through the same typed Compose identity/override used at start; it never implies volume deletion.
- `Quit Harbor UI` exits only the desktop client; closing its window normally hides it.
- `Stop Harbor daemon` is a separate administrative action and warns that stable endpoints will be unavailable.

Harbor never finds or kills processes by executable name. It acts only on a session identity containing a nonce, PID, process start evidence, and ownership record.

## Dashboard

The dashboard is a compact view of registered projects, not a second observability product.

The contextual pane uses dense rows, not a card grid. It groups projects as `Attention`, `Running`, and `Stopped`, then orders favorites and recent use within each group. A row shows:

- project name and path;
- aggregate state;
- default application URL;
- active Apps and services;
- a short current activity or most recent failure;
- start, stop, restart, open, and logs actions appropriate to its state.

Favorites may pin projects within a group without changing lifecycle behavior.

Aggregate states are:

| State | Meaning |
|---|---|
| `stopped` | No active project session is expected. |
| `starting` | Required lifecycle work has not reached readiness. |
| `ready` | Required Apps and managed services are reachable. |
| `rebuilding` | The last ready session is processing a source change. |
| `degraded` | The project is usable, but a non-critical watcher, service, or tool is unhealthy. |
| `failed` | A required App, service, or reconciliation step failed. |
| `stopping` | Graceful shutdown is in progress. |
| `unavailable` | The checkout, compatible `forj`, Docker engine, or required host capability is missing. |

The daemon derives these states from typed facts. UI clients do not derive health independently.

## Desktop layout

Harbor should adapt Lerd's strongest visual idea: a compact navigation rail, a contextual list, and a persistent detail pane. This makes a large amount of local state scannable without turning the home screen into a grid of unrelated cards.

The rail is intentionally icon-width. Each icon has an accessible name, tooltip, keyboard shortcut, and visible selected state; it does not expand into a permanent text sidebar. The contextual pane is a dense, grouped list. The detail pane is the only large canvas.

### Implementation anchor

The implementation starts from a complete, pinned import of GoForj's Vue starter. Harbor preserves its app-owned shadcn-vue primitive source and expresses the rail, panes, rows, tabs, menus, confirmations, tooltips, command palette, and responsive surfaces through those components instead of creating a parallel UI kit.

Lerd's density and visual treatment are the initial anchor. Harbor maps selected pane dimensions, spacing, surfaces, borders, and colors into semantic Tailwind tokens and shadcn component composition so the product can evolve without rewriting every view. The detailed ownership and adaptation boundary is defined in [Frontend](./frontend.md).

The destination map is exact: Home opens `Overview`, the project icon opens `Projects`, the service icon opens `Services`, and the gear opens `System`. Settings is a section inside System rather than a fifth rail destination. The Harbor mark is branding, not an action.

### Overview

```text
┌────┬──────────────────────┬──────────────────────────────────────────────┐
│ ◉  │ Overview             │ Today                                      │
│ ⌂  │ Attention · 1        ├──────────────────────────────────────────────┤
│ ▣  │ ! billing     Failed │ 2 projects ready · 1 needs attention       │
│ ▤  │ Running · 2          │                                              │
│ ⚙  │ ● orders-api  Ready  │ Recent resources                            │
│    │ ● storefront  Ready  │ API Reference · orders-api                 │
│    │ Setup                │ MySQL · billing                             │
│    │ ● Host ready         │                                              │
└────┴──────────────────────┴──────────────────────────────────────────────┘
```

### Projects

```text
┌────┬──────────────────────┬──────────────────────────────────────────────┐
│ ◉  │ Projects        +    │ orders-api                     Ready         │
│ ⌂  │ Attention · 1        │ https://orders.test       Open  Copy  •••   │
│ ▣  │ ! billing     Failed ├──────────────────────────────────────────────┤
│ ▤  │ Running · 2          │ Overview   Logs   Network   Diagnostics     │
│ ⚙  │ ● orders-api  Ready  ├──────────────────────────────────────────────┤
│    │ ● storefront  Ready  │ Apps        app · Ready                    │
│    │ Stopped · 2          │ Services    MySQL · Ready · :3306          │
│    │ ○ worker      Stopped│ Resources   API Reference  Lighthouse      │
│    │ ○ reports     Stopped│ Activity    rebuilding admin               │
└────┴──────────────────────┴──────────────────────────────────────────────┘
```

### Services

```text
┌────┬──────────────────────┬──────────────────────────────────────────────┐
│ ◉  │ Services             │ orders-api / MySQL             Ready        │
│ ⌂  │ Attention · 1        │ mysql.orders.test:3306          Copy  •••   │
│ ▣  │ ! billing / Redis    ├──────────────────────────────────────────────┤
│ ▤  │ Databases · 3        │ Owner       orders-api Compose              │
│ ⚙  │ ● orders / MySQL     │ Private     127.0.0.1:43106                │
│    │ ● billing / Postgres │ Volume      preserved on unregister        │
│    │ Mail & tools · 4     │ Health      accepting connections          │
│    │ ● orders / Mailpit   │ Actions     restart · logs · diagnostics   │
└────┴──────────────────────┴──────────────────────────────────────────────┘
```

### System

```text
┌────┬──────────────────────┬──────────────────────────────────────────────┐
│ ◉  │ System               │ Local networking                Ready       │
│ ⌂  │ ● Harbor daemon      ├──────────────────────────────────────────────┤
│ ▣  │ ● DNS and resolver   │ DNS          .test · healthy                │
│ ▤  │ ● HTTPS and CA       │ HTTPS        trusted · 12 domains          │
│ ⚙  │ ● Ingress            │ Loopback     4 of 8 identities leased      │
│    │ ● Docker             │ Low ports    owned rules verified          │
│    │ ! Updates            │ Actions      recheck · doctor · repair     │
└────┴──────────────────────┴──────────────────────────────────────────────┘
```

### Collapsed

At medium width, selecting a contextual row replaces the list with detail and exposes a Back action. At narrow width, the rail becomes a bottom or compact top destination bar and only one surface is visible:

```text
┌───────────────────────────────────────┐
│ ‹ Projects       orders-api    Ready  │
│ https://orders.test       Open  •••   │
├───────────────────────────────────────┤
│ Overview  Logs  Network  Diagnostics │
│ Apps                                  │
│ app                         Ready     │
│ Services                              │
│ MySQL                  Ready · :3306  │
├───────────────────────────────────────┤
│  ⌂ Home   ▣ Projects   ▤ Services   •••│
└───────────────────────────────────────┘
```

The compact overflow contains System and its Settings section. It cannot hide a unique action; it only changes navigation placement.

The three panes have stable responsibilities:

- the rail switches between Overview, Projects, Services, and System;
- the middle pane lists the entities in the selected area with search, status, and favorites;
- the detail pane owns inspection and actions for the selected entity.

The project header behaves like a small local address bar. It shows the primary domain, TLS state, copy/open actions, project path, aggregate status, and a compact action menu. The selected project remains in place while the four primary views switch between Overview, Logs, Network, and Diagnostics. Apps, services, and their resource actions are ownership sections inside Overview; selecting a row focuses that entity without creating seven competing top-level tabs.

The top-level areas are deliberately limited:

- `Overview` shows aggregate health, recently used projects, current failures, and setup notices;
- `Projects` is the main project list and project detail workspace;
- `Services` gives a cross-project view of databases, caches, mail, and observability services while keeping project ownership visible;
- `System` shows Harbor daemon, DNS, CA, ingress, Docker, GoForj compatibility, updates, machine diagnostics, and Settings.

Global resource search lives in Overview and the `Cmd/Ctrl+K` command palette. Results are GoForj-projected App/service resources; selecting one focuses its owning project and App/service detail before offering open or copy. There is no independent Resources destination.

Services are never presented as a global undifferentiated pool when GoForj owns them per project. A MySQL row is labeled with its project and opens that project's service detail. External/shared resources are visibly distinct from Harbor-managed Compose services.

The cross-project Services area is an aggregated view, not a shared runtime. By default, `orders` MySQL and `billing` MySQL are separate containers and volumes even though both appear at native port `3306` through different domains. This is what allows either project to select a different version, restart independently, and retain or remove its own data without affecting the other.

Visual state must use text and icon shape as well as color. The layout must collapse to two panes and then one pane at narrower window sizes without hiding any operation that exists in the CLI. Harbor uses its own identity and product assets. Its layout and selected initial styling are adapted from Lerd through Harbor-owned Tailwind tokens and shadcn-vue components; Lerd branding, application logic, and product assets are not reused.

## Project detail

Project detail has four primary views.

### Overview

- Apps and their readiness;
- services and their ownership (`managed`, `external`, or `available but not selected`);
- each App/service's resource projections: public domains, API Reference/API Index backing, Lighthouse, dashboards, and native endpoints with copy/open actions;
- latest operation and actionable diagnostics;
- start, stop, and scoped restart controls.

Harbor consumes GoForj's resolved resource projection. It does not hardcode tool ports or build a competing registry. Aggregated resource search and quick-open actions always navigate back to the owning App or service.

### Logs

- one ordered stream with source identity preserved;
- filters for App, watcher, service, and stdout/stderr or PTY/combined provenance;
- live follow, pause, copy, and bounded history;
- raw text retained separately from any presentation formatting;
- explicit gaps when a producer drops data.

Logs are a basic development capability. Harbor does not make live logs conditional on a paid tier or require Lighthouse to be open.

### Network

- each domain, resolved address, public port, internal target, and readiness state;
- certificate expiry and trust status;
- active loopback leases;
- conflicts with foreign listeners;
- direct repair and re-check actions.

### Diagnostics

The project-scoped doctor checks the descriptor, GoForj compatibility, runtime overlay, process/session identity, Compose ownership, endpoint listeners, DNS answers, certificates, Apps, and services. Findings include evidence and a bounded repair when Harbor owns the state.

## Tray

The tray is for quick actions:

```text
GoForj Harbor — 2 ready, 1 degraded

orders-api        Ready
  Open App
  Open Lighthouse
  Restart App
  View Logs

billing           Degraded
  Redis unavailable
  Open Diagnostics

Open Harbor
Start All Favorites
Doctor
Quit Harbor UI
```

It shows running and recently used projects, a short failure reason, and low-risk actions. Rich logs, configuration, and destructive confirmation stay in the main window or terminal.

Closing the window hides it by default and keeps the desktop process alive. `Quit Harbor UI` exits that process only; the daemon and projects continue. Native failure/recovery notifications are best-effort while the UI process is alive and the user has granted permission. Harbor does not add a fifth background notifier merely to make notification delivery unconditional.

`Quit Harbor UI` is also present in the native application menu and bound to `Cmd+Q` on macOS and `Ctrl+Q` on Windows/Linux, so it never depends on a tray. The first close explains once that Harbor remains available in the background and points to both the reopen and explicit Quit paths; this notice does not repeat after acknowledgment.

Linux desktop environments differ in tray and notification support. Harbor must remain fully usable when no tray is available; relaunching the desktop focuses its hidden single instance, and the CLI exposes every essential action.

## CLI

Essential GUI behavior has a CLI equivalent and structured output where automation is useful:

```text
harbor add [path] [--json]
harbor list [--json]
harbor status [project] [--json]
harbor daemon status [--json]
harbor daemon start
harbor daemon stop
harbor start <project>
harbor start --favorites
harbor stop <project>
harbor restart <project> [--app <name>] [--watcher <name>]
harbor favorite add <project>
harbor favorite remove <project>
harbor open <project> [resource]
harbor logs <project> [--app <name>] [--service <name>] [--follow]
harbor doctor [project] [--json]
harbor remove <project>
harbor setup
harbor repair
harbor host remove
harbor update status [--json]
harbor update apply
```

The CLI is a daemon client. It does not mutate Harbor's database, resolver files, Docker state, or project files directly. `host remove` asks the daemon to plan ownership-checked host cleanup and launches the one-shot helper only after interactive approval; package removal remains the native installer's responsibility.

## Doctor

`harbor doctor` is one evidence-based diagnostic path used by the CLI and desktop. Machine checks include:

- daemon endpoint, lock, protocol, and version;
- privileged helper integrity and owned host configuration;
- loopback address allocation and same-port bind behavior;
- `.test` resolver path and exact DNS answers;
- CA fingerprint, trust store, leaf expiry, and TLS handshake;
- low-port ingress and foreign listener conflicts;
- Docker/Compose availability and loopback-only publications;
- supported `forj` version and managed-session capabilities;
- registered checkout existence and descriptor validity;
- stale sessions, processes, containers, routes, and leases.

Repairs are ownership-aware. Harbor may restore its marked resolver entry, reissue its certificate, or recreate its missing listener. It may not overwrite a foreign DNS configuration or stop an unrelated process.

## Updates

Harbor presents separate update domains:

- Harbor desktop, daemon, CLI, helper, installer, and service definitions are one signed product release;
- GoForj CLI updates are offered separately;
- project Go dependencies and minimum Go version remain project decisions;
- service image updates are explicit and preserve data-version warnings;
- project generation changes are never applied during Harbor discovery or startup.

An update must coordinate protocol compatibility, daemon restart, state migration, host-service definitions, verification, and rollback. The desktop cannot use a single-binary updater independently of the daemon and helper.

## First-release scope

Included:

- register and unregister existing GoForj projects;
- stable `.test` domains and trusted HTTPS;
- concurrent projects with identical public App and native service ports;
- default and named Apps;
- GoForj-owned Compose services and external-resource presentation;
- start, stop, scoped restart, state, live logs, and doctor;
- window, tray where supported, and CLI clients;
- macOS, Linux, and Windows with required native CI gates.

Not included:

- arbitrary framework drivers;
- native database or cache binary distribution;
- a service or extension marketplace;
- remote control, team sync, LAN exposure, or public tunnels;
- project creation, source deletion, volume deletion, backups, or migrations;
- built-in replacements for Mailpit, Lighthouse, Grafana, the API Index, or database clients;
- environment-file or code editing;
- AI/MCP control;
- worktree orchestration;
- automatic dependency, Go-version, or image upgrades.

These exclusions keep Harbor focused on the system-level problem GoForj cannot solve inside one project: stable, concurrent, cross-platform local environments.

## Success measures

The first release should be evaluated by outcomes:

- a new user can reach the first project's trusted HTTPS URL without manually editing DNS, ports, or certificates;
- three generated projects run concurrently with the same native database and cache ports;
- the desktop, tray, and CLI report identical state and actions;
- a desktop crash does not stop a project;
- restart, sleep, network change, and daemon recovery restore owned state without deleting data;
- unregister and uninstall leave unrelated host configuration, source, and volumes untouched;
- every claimed platform capability is exercised by a required OS-native GitHub Actions job.
