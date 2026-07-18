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

Projects and the daemon continue to run if the Wails window or tray exits. Users can perform all essential operations with `harbor` from a terminal. Tray support varies by Linux desktop, so it cannot be the only recovery path.

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
| Resource | A launchable or connectable endpoint exposed by GoForj, such as an app, database, Mailpit, Lighthouse, Grafana, or API Index. |
| Session | One active `forj dev` lifecycle, owned by a terminal or by Harbor. |
| Endpoint | A public Harbor domain and protocol mapped to an internal app or service listener. |
| Project identity | A stable Harbor ID, project slug, and loopback address lease stored outside the checkout. |

The hierarchy shown to users is:

```text
Project
├── Apps
│   ├── app
│   └── named Apps
├── Services
│   ├── databases
│   ├── cache
│   └── mail and observability infrastructure
└── Resources
    ├── application and API links
    ├── API Index and Lighthouse
    └── service dashboards and native connection endpoints
```

Harbor must not flatten named Apps into unrelated projects or present every URL as a container service.

## First run

First run is an explicit setup, not a silent installer side effect.

1. Harbor explains the local changes it needs: a `.test` resolver path, a local certificate authority, low-port ingress, and loopback identities for native services.
2. It verifies the installed Go version, `forj`, Docker/Compose when the project needs containers, and platform prerequisites.
3. The user approves the narrowly scoped privileged setup. Harbor shows the exact categories of host state it will own and how uninstall reverses them.
4. Harbor starts the per-user daemon and verifies DNS, HTTP, TLS, and a native TCP loopback probe end to end.
5. The user chooses a project directory or runs `harbor add <path>`.
6. Harbor displays the discovered Apps, services, and proposed domains before registration is committed.

The setup result has three visible states:

- `ready`: the full stable-domain and native-port contract works;
- `limited`: Harbor can run projects but a named capability, such as trusted HTTPS, is unavailable;
- `blocked`: the core promise cannot be met, with a diagnostic and repair action.

Limited mode must never silently change public ports while showing a healthy state.

## Register a project

Registration performs a read-only discovery followed by an explicit apply:

1. Canonicalize the selected path and reject a duplicate registration or a path outside the user's accessible filesystem.
2. Locate `.goforj.yml` and run the versioned, non-executing GoForj project-description command.
3. Validate the descriptor version, project slug, Apps, required endpoint collisions, Compose capability, and GoForj compatibility.
4. Present the default domains and any conflicts. The user can choose a different project slug before apply.
5. Reserve a stable project ID and loopback identity.
6. Reconcile DNS, certificate, and ingress state.
7. Add the project in the stopped state.

Registration does not start Apps, run migrations, pull images, or update dependencies.

Harbor supports direct registration, not broad “park every folder” behavior, in the first release. A future directory scan may list directories containing `.goforj.yml`, but it remains read-only until each project is registered.

## Start a project

Starting a project is one operation with observable phases:

1. Revalidate the project descriptor and configuration hash.
2. Reconcile the project's address, DNS, certificate, endpoint, and private-port leases.
3. Start `forj dev` in managed mode with a session-scoped runtime overlay.
4. Let GoForj run its existing pre-tasks, database setup, migrations, builds, and watcher graph.
5. Observe GoForj session events and Compose state.
6. Publish endpoints only when their upstreams are ready.

The project detail shows progress without translating it into fake certainty. For example, `building app`, `starting mysql`, and `waiting for readiness` are distinct from `ready`.

If a user starts `forj dev` in a terminal, it can attach to Harbor before lifecycle tasks run. The terminal remains the session owner; Harbor supplies endpoints and observes the session. Standalone `forj dev` remains unchanged when Harbor is absent or explicitly disabled.

## Stop and restart

Operations have precise scopes:

- `Restart App` uses a typed GoForj session action and does not tear down Compose.
- `Restart watcher` restarts only the selected GoForj watcher when the session advertises that capability.
- `Restart project` asks GoForj to restart the managed development graph without treating the transition as a normal exit that runs `down_on_exit` between app restarts.
- `Stop project` gracefully stops the GoForj session and applies the project's configured down behavior.
- `Quit Harbor` closes only the desktop client.
- `Stop Harbor daemon` is a separate administrative action and warns that stable endpoints will be unavailable.

Harbor never finds or kills processes by executable name. It acts only on a session identity containing a nonce, PID, process start evidence, and ownership record.

## Dashboard

The dashboard is a compact view of registered projects, not a second observability product.

Each project row or card shows:

- project name and path;
- aggregate state;
- default application URL;
- active Apps and services;
- a short current activity or most recent failure;
- start, stop, restart, open, and logs actions appropriate to its state.

The default ordering is running projects, recently used stopped projects, then the rest alphabetically. Favorites may pin projects without changing lifecycle behavior.

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

```text
┌──────────────┬────────────────────────┬─────────────────────────────────────────────┐
│ Harbor       │ Projects               │ orders-api                    Ready          │
│              │                        │ https://orders-api.test        Open  •••     │
│ Overview     │ ● orders-api           ├─────────────────────────────────────────────┤
│ Projects     │ ● billing              │ Overview  Apps  Services  Resources  Logs   │
│ Services     │ ○ storefront           │ Network  Diagnostics                          │
│ System       │                        ├─────────────────────────────────────────────┤
│              │ + Add project          │ Apps                                        │
│              │                        │ app     Ready     https://orders-api.test    │
│              │                        │ worker  Ready     background runtime         │
│              │                        │                                             │
│ Settings     │                        │ Services                                    │
└──────────────┴────────────────────────┴─────────────────────────────────────────────┘
```

The three areas have stable responsibilities:

- the rail switches between Overview, Projects, Services, and System, with Settings anchored separately;
- the middle pane lists the entities in the selected area with search, status, and favorites;
- the detail pane owns inspection and actions for the selected entity.

The project header behaves like a small local address bar. It shows the primary domain, TLS state, copy/open actions, project path, aggregate status, and a compact action menu. The selected project remains in place while tabs switch between Overview, Apps, Services, Resources, Logs, Network, and Diagnostics.

The top-level areas are deliberately limited:

- `Overview` shows aggregate health, recently used projects, current failures, and setup notices;
- `Projects` is the main project list and project detail workspace;
- `Services` gives a cross-project view of databases, caches, mail, and observability services while keeping project ownership visible;
- `System` shows Harbor daemon, DNS, CA, ingress, Docker, GoForj compatibility, updates, and machine diagnostics.

Services are never presented as a global undifferentiated pool when GoForj owns them per project. A MySQL row is labeled with its project and opens that project's service detail. External/shared resources are visibly distinct from Harbor-managed Compose services.

Visual state must use text and icon shape as well as color. The layout must collapse to two panes and then one pane at narrower window sizes without hiding any operation that exists in the CLI. Harbor should use its own identity and assets; the Lerd influence is information architecture, not copied frontend code or visual assets.

## Project detail

Project detail has five primary views.

### Overview

- Apps and their readiness;
- services and their ownership (`managed`, `external`, or `available but not selected`);
- public domains and native endpoints with copy/open actions;
- latest operation and actionable diagnostics;
- start, stop, and scoped restart controls.

### Resources

- application and API URLs;
- API Index, OpenAPI/Swagger, and Lighthouse;
- Mailpit, Grafana, VictoriaMetrics, and other GoForj-resolved tools;
- database, cache, storage, and mail connection endpoints without secret values;
- health and authentication hints supplied by GoForj.

Harbor consumes GoForj's resolved projection. It does not hardcode tool ports or build a competing registry.

### Logs

- one ordered stream with source identity preserved;
- filters for App, watcher, service, and stdout/stderr;
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

Linux desktop environments differ in tray support. Harbor must remain fully usable when no tray is available.

## CLI

Essential GUI behavior has a CLI equivalent and structured output where automation is useful:

```text
harbor add [path]
harbor list [--json]
harbor status [project] [--json]
harbor start <project>
harbor stop <project>
harbor restart <project> [--app <name>] [--watcher <name>]
harbor open <project> [resource]
harbor logs <project> [--app <name>] [--service <name>] [--follow]
harbor doctor [project] [--json]
harbor remove <project>
harbor setup
harbor repair
```

The CLI is a daemon client. It does not mutate Harbor's database, resolver files, Docker state, or project files directly.

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

- Harbor desktop, daemon, helper, and service definitions are one signed product release;
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
