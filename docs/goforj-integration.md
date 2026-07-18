# GoForj Integration

Status: proposed

Baseline: `goforj/goforj` commit `6422f32eb3013c44ce3b18d236a90158dc8e7f16`

## Boundary

Harbor coordinates projects; GoForj understands them.

| GoForj owns | Harbor owns |
|---|---|
| `.goforj.yml` parsing and compatibility | Registered machine-local project identity |
| default and named App composition | Stable domains and loopback leases |
| component and resource plans | Public endpoint and private upstream allocation |
| environment conventions | Session-scoped endpoint intent |
| build, render, migration, and lifecycle tasks | Multi-project coordination |
| native watcher graph and child process groups | Outer managed-session ownership |
| App/resource discovery semantics | Desktop, tray, CLI, notifications, and log retention |
| project Compose intent | Deterministic Compose identity and observed endpoint routing |
| API Index, Lighthouse, and framework tooling | Presenting and opening those resolved resources |

Harbor must not import `goforj` internal packages, parse `.goforj.yml` independently, scrape `forj dev` output, infer resources from Compose YAML, or reproduce the watcher graph.

The integration consists of two versioned contracts:

1. a static, secret-free project descriptor;
2. a live managed-development session.

## Current foundation

GoForj already provides most of the project semantics Harbor needs:

- the default App uses `cmd/app`, `app`, and `app/wire`;
- named Apps use `cmd/<app>`, `app/<app>`, and `app/<app>/wire`;
- `dev.apps` can be an authoritative App allowlist;
- structured Apps compile into SPA, build, and runtime watcher dependencies;
- `forj dev` owns environment reload, pre/down tasks, database setup, migrations, builds, restarts, and process-tree shutdown;
- one project lock prevents concurrent `forj dev` sessions;
- generated HTTP runtimes separate `APP_URL` from `API_HTTP_HOST` and `API_HTTP_PORT`;
- generated Compose publications already accept `IP_ADDRESS` and per-service port keys;
- the internal resource registry models URLs, App/runtime ownership, health, auth, and priority;
- generated Apps expose a versioned `resources:describe --json` contract for durable database and storage resources.

The current pieces are not yet an external Harbor contract:

- watcher state and actions are internal;
- Lighthouse receives lossy, presentation-oriented log lines;
- the TUI infers some state from formatted text;
- resource discovery is split between the internal registry, hardcoded dev links, Atlas, and the generated App command;
- tool URLs assume localhost and only the default App receives some links;
- the registry currently uses `/health` while generated Apps expose `/-/health` and `/-/ready`;
- metrics listeners have no bind-host setting and bind all interfaces;
- `.env.host` intentionally sets local service hosts to `localhost`;
- GoForj's env loader lets project files override ambient process values, including on reload;
- generated App `LoadEnv` also loads project files, so an inherited environment value alone is not a final override;
- `down_on_exit` means relaunching the whole outer dev process is not a safe App-restart implementation;
- the current dev orchestration uses Bash and contains Unix-only process/TUI paths, so `forj dev` does not yet build cleanly for Windows;
- generated lifecycle commands still assume `docker-compose` rather than a cross-platform Compose capability abstraction.

The design below closes these gaps without publishing all of GoForj's internal implementation as a permanent Go API.

## Static project descriptor

GoForj should expose:

```text
forj project:describe --json
```

The command reads `.goforj.yml`, the project render/resource plans, and generated conventions. It must not:

- execute the generated application;
- run Wire, generation, migrations, shell tasks, or Compose;
- load or return secret values;
- pull images or modules;
- mutate the checkout.

The command returns one documented JSON object on stdout and diagnostics on stderr. The schema is versioned independently from the GoForj CLI version.

An illustrative v1 shape is:

```json
{
  "schema_version": 1,
  "project": {
    "name": "orders-api",
    "module": "example.com/orders",
    "config_digest": "sha256:..."
  },
  "goforj": {
    "version": "v0.0.0",
    "capabilities": [
      "managed-dev.v1",
      "runtime-overlay.v1",
      "resource-snapshot.v1"
    ]
  },
  "apps": [
    {
      "id": "app",
      "name": "app",
      "entrypoint": "cmd/app/main.go",
      "runtimes": [
        {
          "id": "http",
          "kind": "http",
          "default_port": 3000,
          "public_url": true,
          "readiness_path": "/-/ready"
        }
      ]
    }
  ],
  "services": [
    {
      "id": "mysql",
      "kind": "database",
      "driver": "mysql",
      "owner": "compose",
      "lifecycle": "project",
      "endpoints": [
        {
          "id": "mysql",
          "protocol": "tcp",
          "native_port": 3306,
          "visibility": "host"
        }
      ]
    }
  ],
  "resources": [
    {
      "id": "api-index",
      "name": "API Index",
      "category": "docs",
      "protocol": "http",
      "owner": "app",
      "app": "app",
      "runtime": "http",
      "enabled": true
    }
  ]
}
```

The exact schema should be proved with generated fixtures before it is frozen. Its invariants are more important than the illustrative fields:

- stable IDs, not display names, join static intent to live events;
- default and named Apps use the same shape;
- services distinguish Compose-managed, external, and available-but-not-selected intent;
- endpoints state protocol and native public port without disclosing credentials;
- resources preserve GoForj categories, ownership, App/runtime identity, and ordering;
- paths are project-relative unless a canonical root is required by the invocation contract;
- unknown future capability names are ignored, while an unsupported schema major fails clearly;
- descriptor generation is deterministic and golden-tested.

Harbor stores the schema version and digest. A digest change triggers a re-description and a reviewed reconciliation, not an automatic render.

## Managed development session

GoForj should add a managed mode to `forj dev`. The transport remains domain-neutral even though Harbor is its first consumer.

Conceptual invocation:

```text
forj dev --managed --control-endpoint <inherited-or-owner-only-reference>
```

The control credential is inherited or stored in an owner-only runtime file. It is not printed or placed in an argument visible to other processes.

Startup ordering matters:

1. acquire the existing project dev lock;
2. load and validate the static project descriptor;
3. connect, negotiate protocol/capabilities, and register the session;
4. receive a semantic runtime plan from Harbor;
5. load the normal project environment, then apply the final managed overlay;
6. run current setup, pre-task, Compose, migration, build, and watcher behavior;
7. publish the compiled watcher/resource snapshot and ordered lifecycle events;
8. accept only advertised typed actions.

The handshake must happen before pre-tasks so Compose receives Harbor's private publication assignments.

If Harbor is absent, an ordinary `forj dev` behaves exactly as it does now. An explicit `--no-harbor` or equivalent disables automatic attach. A failed optional attach falls back only before lifecycle work starts and reports the reason; it cannot switch port policy halfway through startup.

## Runtime plan

Harbor sends semantic assignments, not hardcoded environment-key names:

```json
{
  "schema_version": 1,
  "session_id": "...",
  "apps": {
    "app": {
      "runtimes": {
        "http": {
          "bind_host": "127.0.0.1",
          "bind_port": 43101,
          "public_url": "https://orders.test"
        }
      }
    }
  },
  "services": {
    "mysql": {
      "publish_host": "127.0.0.1",
      "publish_port": 43106,
      "public_host": "mysql.orders.test",
      "public_port": 3306
    },
    "redis": {
      "publish_host": "127.0.0.1",
      "publish_port": 43107,
      "public_host": "redis.orders.test",
      "public_port": 6379
    }
  }
}
```

GoForj validates every ID and maps the assignments through its current project, App, and resource plans. Harbor does not need to know whether a current template uses `DB_MYSQL_PORT`, an App-prefixed key, or a future typed config field.

## Final runtime overlay

A managed endpoint assignment must beat normal project files without modifying them.

GoForj materializes a session-scoped, owner-only runtime overlay after validating the semantic plan. It applies that overlay:

- after every `env.Load` or `env.Reload` inside `forj dev`;
- to lifecycle and Compose command environments;
- to build and watcher child environments;
- after the generated App's existing `.env`, compiled default/override, and named-App overlay sequence;
- again on every managed App process start.

The generated App needs a narrow runtime-overlay hook in its existing `LoadEnv` path. The hook reads only the file reference inherited for this process, validates its version and ownership, and applies direct App-local endpoint assignments last. It is a local development/runtime mechanism, not a new committed environment layer.

For the default App, a materialized overlay may include values corresponding to:

```text
APP_URL=https://orders.test
API_HTTP_HOST=127.0.0.1
API_HTTP_PORT=43101
DB_HOST=mysql.orders.test
DB_PORT=3306
REDIS_HOST=redis.orders.test
REDIS_PORT=6379
MAIL_SMTP_HOST=smtp.orders.test
MAIL_SMTP_PORT=1025
IP_ADDRESS=127.0.0.1
```

Private Compose publications use their own private host-port values in the lifecycle command environment. App connection values use the stable public domain and native port. They are related but are not the same values.

Named Apps receive their resolved App-specific overlay after GoForj's existing prefix rules. The session protocol uses App IDs rather than making Harbor reproduce prefix normalization.

The managed overlay contains endpoint and session values, not project secrets. Harbor must not read `.env` to construct it. GoForj can combine the overlay with the normal secret-bearing environment in memory without reporting secrets back to Harbor.

## Session snapshot

After setup, GoForj publishes one authoritative snapshot:

- project and descriptor identity;
- session owner (`harbor` or `terminal`);
- Apps, runtimes, SPAs, builds, and custom watchers;
- graph dependencies and criticality;
- current watcher/process state;
- resolved public resources;
- managed Compose service intent and observations available to GoForj;
- supported actions;
- current operation and readiness state.

Snapshots are complete replacements. Events after a snapshot carry a monotonic sequence so Harbor can recover from reconnect without guessing what it missed.

## Events

The event envelope includes at least:

```json
{
  "schema_version": 1,
  "sequence": 42,
  "timestamp": "2026-07-18T12:00:00Z",
  "project_id": "...",
  "session_id": "...",
  "kind": "watcher.state_changed",
  "app_id": "app",
  "watcher_id": "app.runtime",
  "previous_state": "starting",
  "state": "ready"
}
```

Required event families are:

- session registering, starting, ready, stopping, stopped, and disconnected;
- setup task and migration progress;
- watcher build, start, rebuild, failure, recovery, and stop;
- process start and exit with PID, exit code, timing, and intentional-stop reason;
- resource snapshot change and readiness;
- raw stdout/stderr log chunks with source identity;
- explicit backpressure/drop notification;
- descriptor/configuration change requiring refresh.

Log IDs cannot be based only on millisecond timestamps. Ordering comes from a session sequence assigned before send.

## Actions

The session advertises the actions it supports. Initial bounded actions are:

- restart App runtime;
- rebuild App;
- restart SPA or custom watcher;
- restart all runtime watchers without replaying project down/up tasks;
- render through the existing GoForj flow;
- stop the session gracefully;
- request a fresh snapshot.

Each request uses stable IDs, has an operation ID, and returns a typed result. Actions are serialized or rejected according to GoForj's existing watcher graph rules.

Arbitrary shell command execution is not part of the first Harbor contract. If App command execution is added later, GoForj must provide a versioned command descriptor and accept a command identity plus an argv array. Harbor must not scrape `--help` or concatenate user text into a shell command.

## Restart and ownership

Harbor cannot implement App restart by interrupting and relaunching `forj dev`:

- current `down_on_exit` behavior may tear Compose down;
- the project lock prevents a second outer session;
- terminal ownership and graceful TUI recovery matter;
- restarting the whole graph loses useful ready state.

Scoped restart therefore stays inside GoForj's existing supervisor.

A terminal-owned session remains attached to its terminal. Harbor may send advertised actions, but quitting the UI does nothing to it. Stopping it from Harbor is an explicit, confirmed graceful request.

A Harbor-owned session is started and monitored by `harbord`. Daemon recovery verifies its session nonce and process birth identity before reattaching or signaling it.

## Compose

Managed mode must preserve GoForj's service plan and lifecycle policy while adding machine-local coordination:

- set a deterministic `COMPOSE_PROJECT_NAME` derived from the stable Harbor project ID;
- add stable ownership labels to generated services where Compose supports them;
- apply private loopback publication assignments through the final runtime overlay;
- preserve profiles such as optional Redis;
- observe the actual published host address and port before routing;
- distinguish stop from volume/data destruction;
- keep current sibling/project-relative test behavior unrelated to Harbor intact.

GoForj should own detection and invocation of a supported Compose implementation instead of baking `docker-compose` v1 shell text into the Harbor contract. The cross-platform path must be tested with Docker Engine on Linux and Docker Desktop on macOS and Windows.

Harbor never runs `docker compose down -v` as part of stop or unregister.

## Resource projection

GoForj should reconcile its current resource surfaces into one resolved session projection:

- internal project resource registry;
- Atlas inventory;
- `forj dev` startup links;
- generated `resources:describe --json` durable resources;
- named-App base URLs;
- runtime health and actual Harbor public URLs.

The projection should retain the current useful fields—stable ID, name, category, URL, description, priority, source, App, runtime, health, auth hint, and owner—while adding protocol/native endpoint metadata where needed.

Before Harbor consumes it, GoForj should fix known drift:

- use `/-/health` and `/-/ready` consistently;
- describe named-App resources, not only the default App;
- remove localhost assumptions from runtime-resolved URLs;
- define deterministic precedence when static and live resolvers share an ID;
- keep credentials and secret values out of descriptions;
- make `forj dev`, Atlas, Lighthouse, and Harbor consume the same resolved model.

The API Index and its generated examples are deliberate GoForj value. Harbor opens and surfaces that existing resource; it must not replace it with a desktop-generated route list. Lighthouse remains the execution-inspection surface; Harbor supplies a stable link and a small session summary rather than duplicating its explorers.

## Configuration changes

GoForj already watches root `.env*` and App trees. In managed mode:

1. GoForj detects and reloads owner configuration as it does today;
2. it reapplies the managed overlay after the reload;
3. it produces a new descriptor/resource digest when topology changed;
4. Harbor plans any DNS, certificate, listener, or private-port delta;
5. GoForj applies a typed session refresh;
6. both sides publish one new snapshot.

The runtime overlay lives outside the checkout and must not trigger the project's source or environment watcher.

Harbor does not run `forj render` merely because `.goforj.yml` changed. If generated output is stale, GoForj reports an actionable prerequisite and the user chooses whether to render.

## Windows readiness

Harbor's product target is macOS, Linux, and Windows, but current `forj dev` includes Windows build blockers and Bash/PTY assumptions. These are GoForj prerequisites, not issues Harbor should hide:

- move Unix signal calls behind platform files;
- provide Windows-safe terminal recovery;
- define whether project shell tasks require an installed Bash or gain a portable command model;
- test watcher roots, process groups/Job Objects, shutdown, environment reload, and managed IPC on Windows;
- use a supported Compose invocation on Docker Desktop.

Windows cannot be called a full Harbor platform until an ordinary generated project's managed `forj dev` session passes the required Windows integration workflow.

## Extension boundary

GoForj extensions are still a draft design. Harbor must not scan, install, update, or execute extension code during project discovery.

When extensions exist, their routes, commands, runtimes, and resources should flow through the same GoForj project descriptor and live session projection. Harbor remains unaware of extension package APIs. Capability expansion is visible through a descriptor change and follows GoForj's trusted compile-time model.

## Compatibility

Versioning is capability-based:

- descriptor schema version;
- managed-session protocol range;
- runtime-overlay schema version;
- individual capabilities and action versions;
- minimum compatible GoForj version shown in diagnostics.

A new GoForj release may add fields and capabilities without requiring a Harbor release. Removing or changing semantics requires a new protocol/schema major with a migration window.

Harbor does not require projects to raise their Go version. The GoForj integration should use the oldest Go release its implementation and required dependencies actually need, and document an exact constraint before any increase.

## Integration acceptance

The GoForj side is ready when fixtures prove:

- default and multiple named Apps describe deterministically;
- static description executes no project code and contains no secret values;
- standalone `forj dev` is byte-for-byte behaviorally unchanged when Harbor is absent;
- managed startup receives its plan before pre-tasks and Compose;
- project `.env` and `.env.host` cannot overwrite the final managed endpoint overlay;
- an environment reload reapplies the overlay;
- Apps bind private loopback ports while `APP_URL` uses the public HTTPS domain;
- Compose binds only private loopback high ports;
- App restart does not invoke `down_on_exit` or restart unrelated services;
- ordered state and log events recover from disconnect through snapshot plus sequence;
- resource URLs, health paths, API Index, Lighthouse, and named Apps are correct;
- terminal-owned and Harbor-owned session recovery is safe;
- the largest supported generated composition passes on macOS, Linux, and Windows;
- GoForj's generators/templates are changed at their source, regenerated, and verified diff-free.
