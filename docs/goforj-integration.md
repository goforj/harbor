# GoForj Integration

Status: approved target design; implementation tracked in [Current implementation state](./current-state.md)

Original design-research baseline: `goforj/goforj` commit `6422f32eb3013c44ce3b18d236a90158dc8e7f16`

The current tactical launcher has a temporary exact compatibility gate: it accepts the canonical GoForj pseudo-version `v0.21.1-0.20260722203521-55a1e5759956`, or a clean binary built from revision `55a1e57599565c9768627db016fc781e3c705f15`. No released tag currently contains the combined descriptor, managed-session, and conventional-task compatibility contract, so development and CI use that reviewed revision. Other versioned and unversioned builds are rejected until their wire behavior is reviewed. The `goforj_version: 0.19.0` field in Harbor's `.goforj.yml` is render metadata, not the runtime CLI admission rule.

## Boundary

Harbor coordinates projects; GoForj understands them.

| GoForj owns | Harbor owns |
|---|---|
| `.goforj.yml` parsing and compatibility | Registered machine-local project identity |
| default and named App composition | Stable domains and loopback leases |
| component and resource plans | Public endpoint and private upstream allocation |
| environment conventions | Session-scoped endpoint intent |
| build, render, migration, and lifecycle tasks | Multi-project coordination |
| native watcher graph and graceful child lifecycle | Containing managed-process scope and final settlement |
| App/resource discovery semantics | Desktop, tray, CLI, notifications, and log retention |
| project Compose intent, identity selection, and every Compose mutation | Machine-local endpoint coordination, read-only container observation, and observed routing |
| API Index artifacts, API Reference, Lighthouse, and framework tooling | Presenting and opening those resolved resources |

Harbor must not import `goforj` internal packages, parse `.goforj.yml` independently, scrape `forj dev` output, infer resources from Compose YAML, or reproduce the watcher graph. Generated Apps do not inspect Docker. The frontend receives typed Harbor snapshots and bounded log chunks, never a Docker endpoint or generic operation surface.

`harbord` may independently observe the local Docker Engine through a narrow read-only adapter. That adapter may list, inspect, subscribe to events, and stream logs only after exact Compose project/service/working-directory labels and the registered canonical checkout establish ownership. It cannot infer desired composition or invoke any mutation. GoForj remains the sole owner of Compose configuration, profiles, up/down, start/stop/restart, builds, pulls, and destructive actions.

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

The command reads `.goforj.yml`, checked-in generated metadata, and generated conventions. Some effective topology exists only in `.env` or `.env.example` today, including selected drivers, local/external placement, Compose profiles, consumers, and endpoint affinity. GoForj may resolve those files through a pure, read-only topology path because it owns their semantics. It must emit no environment value, must not apply values to the command process, and must digest only the normalized non-secret topology—not the raw environment files.

The command must not:

- execute the generated application;
- run Wire, generation, migrations, shell tasks, or Compose;
- return secret values or derive stable IDs from credential-bearing material;
- pull images or modules;
- mutate the checkout.

The command returns one documented JSON object on stdout and diagnostics on stderr. The schema is versioned independently from the GoForj CLI version.

The descriptor provides the `schema_version`, project identity, a non-secret normalized topology digest, CLI/generation metadata, the conventional available-App/HTTP-runtime inventory, and (when the catalog can prove a native endpoint) a deterministic service-requirement projection. The service projection reads only project-owned `.env.example` and `.env` layers in memory; it never applies values to the command process or emits credentials, addresses, or raw endpoint-affinity material. It distinguishes Compose-managed, external, and available requirements and includes stable requirement/endpoint IDs, App consumers, protocols, native ports, visibility, and optional exact generated environment metadata. Each metadata item names its App, generated key, and bounded value shape (`host`, `port`, or `address`); Harbor materializes the value only after observing the matching private publication. The command does not execute generated code or mutate the checkout, and Harbor must not treat the descriptor as a managed-session capability. Harbor's production start path invokes this command through the exact admitted GoForj executable, strictly validates schema v1, and stores the normalized `config_digest` in the active session before launching `forj dev`; when an additive resource section is present, Harbor consumes it only to constrain matching live links and assign exact local reservations. Harbor consumes the App inventory and explicit service metadata for capability-gated runtime-plan assignments; unsupported driver-specific URL formats remain fail-closed until their value shape is added to the contract.

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
    "cli_capabilities": [
      "managed-dev.v1",
      "resource-snapshot.v1"
    ],
    "generated_project": {
      "generation": "...",
      "capabilities": [
        "runtime-overlay-hook.v1",
        "managed-listeners.v1"
      ]
    }
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
  "service_requirements": [
    {
      "id": "requirement.database.primary",
      "service_key": "mysql",
      "kind": "database",
      "driver": "mysql",
      "owner": "compose",
      "lifecycle": "project",
      "consumers": ["app"],
      "endpoints": [
        {
          "id": "endpoint.database.primary.tcp",
          "protocol": "tcp",
          "native_port": 3306,
          "visibility": "host"
        }
      ]
    }
  ],
  "resources": [
    {
      "id": "swagger",
      "name": "API Reference",
      "category": "docs",
      "protocol": "http",
      "owner": "app",
      "app": "app",
      "runtime": "http",
      "path": "/swagger",
      "backing_artifact": "api-index",
      "enabled": true
    }
  ]
}
```

The exact schema should be proved with generated fixtures before it is frozen. Its invariants are more important than the illustrative fields:

- stable IDs, not display names, join static intent to live events;
- default and named Apps use the same shape;
- available Apps are distinct from the active Apps and runtime implementations selected for a live session;
- service requirements distinguish Compose-managed, external, and available-but-not-selected intent;
- every requirement and endpoint has its own stable, non-secret ID, even when several requirements share a service key;
- endpoint-affinity and active-consumer relationships are preserved as explicit mappings rather than collapsed into one `mysql` or `redis` record; when affinity depends on credential-bearing material, GoForj emits only a deterministic equivalence-group label assigned from sorted structural membership after comparing values in memory, never the value or its raw hash;
- endpoints state protocol and native public port without disclosing credentials;
- resources preserve GoForj categories, ownership, App/runtime identity, and ordering;
- paths are project-relative unless a canonical root is required by the invocation contract;
- unknown future capability names are ignored, while an unsupported schema major fails clearly;
- descriptor generation is deterministic and golden-tested.

Harbor stores the schema version and normalized topology digest. A digest change triggers a re-description and a reviewed reconciliation, not an automatic render.

## Managed development session

GoForj should add a managed mode to `forj dev`. The transport remains domain-neutral even though Harbor is its first consumer.

Harbor now has the transport-neutral v1 message contract and authenticated handler seam that this mode uses. GoForj
contains a private transport adapter that mirrors the v1 frame, envelope, handshake, and typed calls because Harbor's
implementation packages are intentionally not importable across modules. Production Start/Restart supplies GoForj an
owner-only inherited context; GoForj consumes it before project environment loading, negotiates
`managed-session.v1` plus `managed-session.launch-context.v1`, and retries only the short planned-to-awaiting process
attachment race. Registration carries the canonical project/session identity, descriptor digest, client nonce, owner,
the negotiated capabilities, and a bounded launch ticket. Harbor hashes that exact ticket against the durable session
digest and never stores the raw value. The publication and Compose-barrier methods are bounded complete replacements
fenced by the exact attached-session generation. In the current vertical slice, GoForj sends an empty replacement and
starts a retrying Compose barrier when the initial watcher graph is online; Harbor ignores invented client ports and
re-observes the supervised Compose services before activating native routes. The contract rejects unknown or duplicate JSON fields, trailing values, unsorted identities, invalid
digests, and cross-session publication facts before an authority handler is called.

Conceptual invocation:

```text
forj dev --managed --control-endpoint <inherited-or-owner-only-reference>
```

The control credential is inherited or stored in an owner-only runtime file. It is not printed or placed in an argument visible to other processes.

Harbor's authenticated barrier now has a live native-route activator: once the inherited GoForj session reaches its
initial watcher graph, GoForj sends a complete empty client replacement and retries the barrier while Harbor waits for
readiness, fresh service-port observations, Harbor-owned TCP reservations, and live native relay evidence. Harbor does
not trust client-supplied ports; it replaces the registry with its own exact observation before acknowledging. The
Compose barrier may now perform that service-only join while the exact project is still `Starting`; public App
readiness and default publication planning remain `Ready`-gated. Managed startup now executes the explicit pre-Compose,
Compose, post-Compose, and post-migrate task buckets around that barrier, and Harbor-owned `down_on_exit` cleanup
executes typed pre-Compose-down, Compose-down, and post-Compose-down buckets. The semantic runtime-plan/overlay
handshake and full process/action event delivery remain later slices, while ordinary standalone
`forj dev` remains unchanged.

Startup ordering matters:

1. acquire the existing project dev lock;
2. load and validate the static project descriptor;
3. capture any inherited control/ticket reference before project environment files can affect process state;
4. load normal environment layers and resolve the active App/runtime set, including `dev.apps`, `FORJ_APP`, build-only Apps, and custom runtime overrides;
5. connect, negotiate CLI and generated-project capabilities, and register the session;
6. receive and validate a semantic plan covering every active listener and service requirement, then materialize the App overlay through an internal trusted handle;
7. build the selected Apps, preserving GoForj's current build-before-setup dependency;
8. run explicitly phased foreground `pre-compose` tasks;
9. start the typed Compose phase with command-local publication assignments;
10. report successful Compose completion and the accepted project identity, then wait for Harbor to observe the actual publications and activate required native routes;
11. after Harbor acknowledges that barrier, run `post-compose` readiness tasks, database setup/migrations with the App connection overlay, then `post-migrate` tasks;
12. start the watcher graph, publish complete snapshots and ordered lifecycle events, and accept only advertised typed actions.

The handshake must happen before lifecycle tasks. Full managed mode replaces the flat `dev.pre` list with stable task IDs and explicit `pre-compose`, `post-compose`, and `post-migrate` phases. GoForj migrates framework-owned generated tasks: Compose becomes the typed phase, database wait belongs after the route barrier, and generated frontend preparation receives its appropriate foreground phase. It does not classify arbitrary legacy/custom commands from their names or shell text. An unphased custom task makes the project upgrade-required until the user assigns a phase; a managed lifecycle task must finish in its process group and leave no detached descendants or listeners. Long-running work belongs in a declared runtime/watcher.

Shutdown receives the same treatment. The generated raw `dev.down` Compose command becomes a typed Compose-down phase that retains the exact implementation, adopted/fresh project identity, profiles, publication environment, and untracked override set selected at startup. Optional custom shutdown tasks must declare `pre-compose-down` or `post-compose-down`; ambiguous legacy commands block managed mode. GoForj keeps session artifacts alive until all configured down behavior finishes and never adds volume deletion implicitly.

The publication barrier prevents a migration configured for `mysql.<project>.test:3306` from running before Harbor can relay that name to the newly started private Compose port.

If Harbor is absent, an ordinary `forj dev` behaves exactly as it does now. An explicit `--no-harbor` remains the future terminal-owned opt-out. An inherited production context is fail-closed: daemon absence, capability mismatch, or attachment rejection stops before lifecycle work rather than switching port policy halfway through startup.

### Terminal-owned attachment

A terminal-launched `forj dev` discovers the owner-only daemon endpoint in the platform-standard per-user runtime location. It connects over peer-authenticated IPC and sends its canonical project root, descriptor digest, client nonce, CLI capabilities, generated-project capabilities, and proposed active App/runtime set. The daemon validates the peer UID/SID, registration, digest, and absence of another active session before returning a short-lived credential bound to that root, peer, nonce, and session.

This exchange completes before any lifecycle task. A Harbor-launched session instead receives an inherited one-use ticket and endpoint reference. Neither credential appears in argv or the project checkout, and neither can attach another root or control machine setup.

## Runtime plan

Harbor sends semantic assignments, not hardcoded environment-key names:

```json
{
  "schema_version": 1,
  "session_id": "...",
  "apps": {
    "app": {
      "active": true,
      "runtimes": {
        "http": {
          "bind_host": "127.0.0.1",
          "bind_port": 43101,
          "public_url": "https://orders.test",
          "routes": {
            "health": "/-/health",
            "ready": "/-/ready",
            "metrics": "/metrics",
            "lighthouse_agent": "/lighthouse/ws/agent"
          }
        }
      }
    }
  },
  "service_endpoints": {
    "endpoint.database.primary.tcp": {
      "requirement_id": "requirement.database.primary",
      "consumers": ["app"],
      "publish_host": "127.0.0.1",
      "publish_port": 43106,
      "public_host": "mysql.orders.test",
      "public_port": 3306,
      "environment": [
        {"app_id": "app", "key": "DB_HOST", "kind": "host"},
        {"app_id": "app", "key": "DB_PORT", "kind": "port"}
      ]
    },
    "endpoint.cache.primary.tcp": {
      "requirement_id": "requirement.cache.primary",
      "consumers": ["app", "worker"],
      "publish_host": "127.0.0.1",
      "publish_port": 43107,
      "public_host": "redis.orders.test",
      "public_port": 6379
    }
  }
}
```

The mirrored managed-session wire contract represents the two assignment collections as sorted arrays rather than
JSON objects so every peer can enforce deterministic IDs and duplicate rejection without relying on map ordering.
The request/response is capability-gated (`managed-session.runtime-plan.v1`) and bound to the exact attached session
fence. Harbor does not advertise the capability until its authority can produce real assignments, and GoForj keeps it
off by default; this contract therefore does not change the current startup path or imply that the overlay exists yet.

GoForj validates every ID and maps the assignments through its current project, App, service-requirement, and resource plans. Stable IDs are assigned from normalized structural identity, not from the current affinity hash when that hash may include a credential-bearing DSN. Harbor does not need to know whether a current template uses `DB_MYSQL_PORT`, an App-prefixed key, or a future typed config field; GoForj emits the exact generated key and Harbor supplies only the value shape it was told to materialize.

The plan covers every listener-bearing active command shape. A generated combined HTTP `run` has one listener whose routes include health, readiness, `/metrics`, and Lighthouse; those routes are not separate ports. Standalone worker or scheduler commands receive their own metrics listener only when that active command actually opens one. SPAs are build nodes, not listeners, unless an explicitly modeled development server exists.

Current custom `run.exec` commands and `dev.watches` have no endpoint or bind metadata. Any arbitrary custom process therefore blocks full mode by default, even if no collision is currently observed. Supporting one requires a future typed endpoint declaration, a Harbor-assigned bind contract, process-group ownership, and post-start observation that GoForj can enforce; Harbor never assumes a shell command is listener-free.

## Runtime assignment layers

A managed endpoint assignment must beat normal project files without modifying them.

GoForj materializes three separate outputs after validating the semantic plan:

1. a command-local Compose publication environment containing private host addresses and private high ports;
2. an App-final connection overlay containing explicit service-consumer values and private App listener assignments;
3. session artifacts such as a Compose override and metrics scrape targets, stored outside the checkout.

The publication environment is applied only to the typed Compose command. It must not leak into App, build, migration, or watcher environments. The App overlay is applied:

- after every `env.Load` or `env.Reload` inside `forj dev`;
- to the runtime and migration child environments that need App connection values;
- after the generated App's existing `.env`, compiled default/override, and named-App overlay sequence;
- again on every managed App process start.

The generated App needs a narrow runtime-overlay hook in its existing `LoadEnv` path. `forj dev` retains the validated overlay handle internally and passes it to each child through an inherited descriptor/handle or another owner-only mechanism that is not rediscovered from project environment. The generated App captures that reference before its own project environment load, validates its version and ownership, and applies direct App-local assignments last. A project file cannot replace the overlay locator. This is a local development/runtime mechanism, not a new committed environment layer.

For the default App, a materialized overlay may include values corresponding to:

```text
APP_URL=https://orders.test
API_HTTP_HOST=127.77.0.10
API_HTTP_PORT=3000
DB_HOST=127.77.0.11
DB_PORT=43106
CACHE_ADDR=127.77.0.12:43107
MAIL_SMTP_HOST=127.77.0.13
MAIL_SMTP_PORT=43108
LIGHTHOUSE_URL=ws://127.77.0.10:3000/lighthouse/ws/agent
```

`LIGHTHOUSE_URL` is the private WebSocket transport used by generated agents and devwatch. The public, user-facing Lighthouse URL is a separate resolved HTTPS resource. It is not written into this transport key.

Private Compose publications use values such as `IP_ADDRESS=127.0.0.1` and explicit `*_PUBLISH_PORT` keys only in the Compose command environment. App connection values use the stable public domain and native port. They are related but are not the same values. Where a current template overloads a key—most notably `REDIS_PORT` for both host publication and App connection—the generated Compose template gains a purpose-specific `REDIS_PUBLISH_PORT` (and analogous keys where needed) with a backward-compatible fallback to the existing key for standalone projects. Managed mode never assigns one process-wide map and hopes both meanings coincide.

Named Apps receive their resolved App-specific overlay after GoForj's existing prefix rules. The session protocol uses App IDs rather than making Harbor reproduce prefix normalization.

Dynamic metrics scrape configuration is mounted from session runtime storage through an untracked Compose override. It must not rewrite or rebuild the checked-in `containers/observability/vmagent/metrics-targets.json`. A container-to-host scrape cannot rely on `host.docker.internal` reaching an App bound to `127.0.0.1`. Managed `forj dev`, which owns Compose and its network intent, hosts a narrow in-process session relay from the platform's container-only host interface to registered loopback metrics targets. Harbor allocates the relay endpoint and can corroborate the resulting container/runtime facts through its read-only Engine adapter. The relay accepts only the session's fixed target set, never binds a LAN interface, and exits with the session. Phase 0 must identify and prove the exact Docker Engine/Desktop interface path per platform; observability is not full-mode capable where no container-only path exists.

The managed overlay contains endpoint and session values, not project secrets. Harbor must not read `.env` to construct it. GoForj can combine the overlay with the normal secret-bearing environment in memory without reporting secrets back to Harbor.

## Session snapshot

### Interim startup observation

Before the authenticated managed session and direct container adapter exist, Harbor uses one intentionally narrow additive bridge: after the default App proves ready, it invokes `forj dev:status --json` through the exact GoForj executable, checkout, and environment already accepted by the process supervisor. GoForj selects and queries Compose, bounds and normalizes machine output, aggregates replicas by Compose service identity, and reports whether the project shape is supported. Harbor validates that versioned report and atomically replaces its service projection with active rows.

The bridge supports only conventional Compose startup identities GoForj can prove. An owner-customized Compose shell task reports `supported: false`; Harbor does not guess its project, files, profiles, or containers. Older accepted GoForj builds keep the historical empty service projection. A valid supported report with no Compose services is a successful empty observation. If a supported observation is malformed or fails, App readiness still wins: Harbor completes startup with an empty service projection and records that service observation was unavailable instead of tearing down a healthy project or publishing invented state.

This report is a point-in-time compatibility observation. It does not provide live events, publications, container authority, actions, or reconnect semantics and must not grow into a polling reimplementation of the managed session below. Container state and logs should not require a new GoForj CLI command: once the direct adapter lands, Harbor observes those runtime facts itself and keeps this bridge only for facts that genuinely belong to GoForj.

GoForj publishes an initial planned snapshot after negotiation, publication intent and phase results at the Compose barrier, and one authoritative project/runtime-plan snapshot after the watcher graph starts:

- project and descriptor identity;
- session owner (`harbor` or `terminal`);
- available and active Apps, runtimes, SPAs, builds, custom watchers, and enforceable/unmanaged bind status;
- graph dependencies and criticality;
- watcher coverage, latest build result, process state, and runtime-probe readiness as distinct facts;
- resolved public resources;
- managed Compose service intent, accepted project identity, and lifecycle phase results;
- supported actions;
- current operation and readiness state.

Snapshots are complete replacements. Events after a snapshot carry a monotonic sequence so Harbor can recover from reconnect without guessing what it missed.

Harbor joins those GoForj facts with its own Docker, DNS, route, relay, TLS, and public-probe facts. Docker observations are current runtime evidence, not project intent; a container, process-start event, or GoForj's current `Dev ready` message cannot by itself mark a public endpoint ready.

## Events

The additive `managed-session.events.v1` capability defines the bounded event shape and now has an authenticated,
bounded transport path on both sides of the managed session. It remains capability-off by default: Harbor advertises
it only when its authority supplies an explicit event sink, and GoForj does not request it from the normal launch
adapter yet. Its first records are `log.chunk` and
`output.gap`: each carries stable project/session identity, a producer-assigned session sequence, an RFC3339 UTC
timestamp, App or watcher source identity, an honest `stdout`, `stderr`, or `pty/combined` stream, and normalized
valid-UTF-8 text. A gap names its inclusive dropped sequence range and count instead of silently claiming continuity.
Attachment generation remains a future batch/append fence, so events can retain their sequence across a Harbor
reconnect. The transport rejects disabled delivery, non-monotonic envelope sequences, and payload/envelope sequence
mismatches. This schema does not make the current supervisor pipe or diagnostic spool re-adoptable and does not claim
durable event replay; producer hooks and process/action state projection remain the next slice.

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
  "previous_state": "stopped",
  "state": "started"
}
```

Required event families are:

- session registering, starting, ready, stopping, stopped, and disconnected;
- setup task and migration progress;
- watcher build, start, rebuild, failure, recovery, and stop;
- process start and exit with PID, exit code, timing, and intentional-stop reason;
- resource snapshot change and readiness;
- pre-decoration App/watcher process log chunks with source identity and honest stream provenance (`stdout`, `stderr`, or `pty/combined`);
- explicit backpressure/drop notification;
- descriptor/configuration change requiring refresh.

Log IDs cannot be based only on millisecond timestamps. Ordering comes from a session sequence assigned before send. A Harbor-managed process may use separate pipes; a terminal-owned Unix watcher may retain its PTY and must report `pty/combined` rather than invent stdout/stderr separation. GoForj adds the capture hook before TUI or Lighthouse decoration. Container logs are not part of this GoForj stream; `harbord` reads them from the exact attributed containers and preserves their container/service identity and Docker stream metadata.

## Actions

The session advertises the actions it supports. Initial bounded actions are:

- restart App runtime;
- rebuild App;
- rebuild a SPA or restart a custom watcher;
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

- discover and persist the checkout's existing directory-derived Compose project identity during registration;
- adopt running containers, networks, and named volumes that match that identity before the first managed start;
- use a stable Harbor-derived identity only for a fresh project with no prior Compose state;
- use Compose's built-in project labels and, when needed, inject Harbor session labels through an external untracked override rather than tracked generated output;
- apply private loopback publication assignments through the command-local Compose environment;
- preserve profiles such as optional Redis;
- report Compose completion and the accepted project identity, then wait while Harbor observes the actual published host address and port and returns its route-ready acknowledgement before database setup or migration;
- retain the same Compose implementation, project identity, profiles, command-local environment, and session override for typed shutdown;
- distinguish stop from volume/data destruction;
- keep current sibling/project-relative test behavior unrelated to Harbor intact.

Registration never changes an adopted Compose identity during ordinary start. Moving to another identity is a separate, explicit, data-aware migration that inventories volumes and offers rollback; otherwise existing databases can appear empty while their original volumes remain orphaned.

GoForj should own detection and invocation of a supported Compose implementation instead of baking `docker-compose` v1 shell text into the Harbor contract. The cross-platform path must be tested with Docker Engine on Linux and Docker Desktop on macOS and Windows.

Harbor's Docker adapter is observational only. It discovers containers through standard Compose labels, requires exact project, service, and working-directory labels to agree with the registered canonical checkout, and treats ambiguous, missing, stale, or foreign attribution as unavailable. It may list/inspect containers, read health and publication state, subscribe to lifecycle events, and stream bounded logs. It never creates, starts, stops, restarts, removes, executes in, attaches to, builds, pulls, or mutates containers, images, networks, or volumes. All such actions remain typed GoForj/Compose lifecycle requests.

Harbor never runs `docker compose down -v` as part of stop or unregister, and it does not invoke any equivalent Docker API mutation.

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

The API Index and its generated examples are deliberate GoForj value. Today `build/api_index.json` is the backing artifact and the browser-facing resource is the generated API Reference/Swagger route, normally `/swagger`. Harbor opens that existing resolved resource and describes its API Index backing; it must not invent a direct API Index URL or replace it with a desktop-generated route list. If GoForj later exposes the raw index as a stable resource, it enters the same projection. Lighthouse remains the execution-inspection surface; Harbor supplies a stable link and a small session summary rather than duplicating its explorers.

## Configuration changes

GoForj already watches root `.env*` and App trees, but not `.goforj.yml`. Managed mode adds an explicit owner-config watcher:

1. GoForj detects `.env*`, App-tree, and `.goforj.yml` changes through their appropriate watcher paths;
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
- GoForj CLI capabilities;
- checked-in generated-project capabilities, including the runtime-overlay hook and bind controls;
- runtime-overlay and session-artifact schema versions;
- individual capabilities and action versions;
- minimum compatible GoForj version shown in diagnostics.

A new GoForj release may add fields and capabilities without requiring a Harbor release. Removing or changing semantics requires a new protocol/schema major with a migration window.

A current `forj` binary may statically describe an older checkout when its checked-in format is understood. That does not make the project fully manageable. Full mode requires both the CLI/session capabilities and the generated-project hooks used by that project. Registration records one of these compatibility states:

- `managed`: all required capabilities are present;
- `read-only`: Harbor can describe and open the checkout/resources but cannot start it safely;
- `upgrade-required`: an explicit GoForj update and/or render is needed;
- `unavailable`: the descriptor cannot be understood safely.

Read-only and upgrade-required projects can still open their checkout and run standalone `forj dev --no-harbor`; Harbor does not fake full mode by scraping output. It also never renders during discovery or start. An explicit user-selected `forj render`—or a future reviewable plan/apply command—must list affected generated files before apply, preserve normal compatibility policy, regenerate every mirror from its authoritative source, and require review. The implementing releases must publish an exact compatible CLI and generated-project version matrix; this design does not invent version numbers before those capabilities exist.

Harbor does not require projects to raise their Go version. The GoForj integration should use the oldest Go release its implementation and required dependencies actually need, and document an exact constraint before any increase.

## Integration acceptance

The GoForj side is ready when fixtures prove:

- default and multiple named Apps describe deterministically;
- static description executes no project code, emits no secret values, and hashes normalized non-secret topology rather than raw environment files;
- standalone `forj dev` is byte-for-byte behaviorally unchanged when Harbor is absent;
- terminal attach validates peer/root/session binding before lifecycle work, while explicit `--no-harbor` bypasses attachment;
- managed startup receives its plan before phased lifecycle work, reports Compose completion and accepted identity, and waits for Harbor's independently observed publication barrier before post-Compose readiness and migration;
- project `.env` and `.env.host` cannot overwrite the final managed endpoint overlay;
- a generated App's project environment cannot replace the trusted overlay reference it captures before load;
- an environment reload reapplies the overlay;
- command-local Compose publication values never become App connection values, including Redis;
- Apps bind private loopback ports while `APP_URL` uses the public HTTPS domain;
- combined HTTP routes are not modeled as extra listeners, standalone auxiliary listeners are planned, and any undeclared custom process blocks full mode; Lighthouse's private transport remains distinct from its public resource URL;
- Compose binds only private loopback high ports;
- a seeded existing Compose project is adopted with its data intact and no replacement volume set;
- managed stop targets that exact adopted/fresh Compose identity through the retained override set, phases custom down work explicitly, and never deletes volumes;
- dynamic metrics targets live outside the checkout and container scraping reaches the narrow host relay on every OS;
- start, reload, and stop leave the generated checkout clean;
- App restart does not invoke `down_on_exit` or restart unrelated services;
- ordered state and log events recover from disconnect through snapshot plus sequence;
- PTY/combined and separate stdout/stderr process streams retain honest provenance before presentation decoration;
- direct container observation and log streaming accept only exact Compose project/service/working-directory labels and canonical-checkout attribution, reconnect across container replacement, and expose no mutation path;
- generated Apps, helper processes, CLI clients, and frontend bindings receive neither a Docker endpoint nor a generic Docker operation;
- resource URLs, health paths, API Reference/API Index backing, Lighthouse, and named Apps are correct;
- an older generated project is visibly read-only or upgrade-required until its required hooks are rendered explicitly;
- `.goforj.yml` changes are detected by a dedicated watcher and never trigger an automatic render;
- terminal-owned and Harbor-owned session recovery is safe;
- the largest supported generated composition passes on macOS, Linux, and Windows;
- GoForj's generators/templates are changed at their source, regenerated, and verified diff-free.
