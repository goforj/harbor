# Current Implementation State

Status: active development

Last updated: 2026-07-21

This document describes the repository as it works today. The other documents in this directory describe Harbor's intended product and architecture; their phase gates are not claims that the corresponding work is complete.

## Product shape

Harbor is a local development control plane for GoForj projects:

- `harbord` is the sole durable-state writer and runtime reconciler;
- the Go CLI and Wails desktop are clients of the daemon;
- GoForj remains responsible for rendering projects and running their development graph;
- each project owns its own Apps, containers, and data-bearing services;
- Harbor assigns each project a loopback identity so projects can reuse ordinary ports without colliding;
- shared DNS and HTTP/TLS ingress translate stable `.test` names to those project-local runtimes;
- the current ingress is loopback-only; LAN, NAT, and Cloudflare-style exposure require a separate explicit App grant and are not enabled by selecting a host interface;
- privileged machine changes are delegated to a narrowly scoped, one-shot helper rather than running the daemon or desktop as root.

The repository is a working vertical slice, not a releasable product. One real GoForj project has been registered, assigned a non-default loopback address, started through the desktop, observed as ready, and shown with live `forj dev` output. The current source also observes that checkout's Compose services, refreshes their durable service projection after local container events, and streams a selected service's container logs through the desktop without asking the generated App or GoForj CLI to access Docker. Reserved-resource route projection now joins descriptor-matched ready resources to exact endpoint reservations and private upstreams; a durable scoped `project.restart` operation now replaces one live session through a fenced stop boundary and the same readiness pipeline. Normal Start is now a convergence action even after a project-local recovery quarantine: an exact retained process receipt is settled through the native supervisor before a replacement session is admitted, while a receipt-free launch row is retired only when the durable row contains no process identity. If a project-owned loopback still has its App port occupied, Start now performs a native same-user inspection and settles either the exact `forj dev` scope or a same-user listener whose working directory is the registered checkout before retrying admission; foreign, ambiguous, unreadable, and unsupported listeners remain fail-closed. The desktop presents this as the ordinary `Start project` action; explicit native inspection remains a separate fallback only when Harbor lacks an exact project lease or checkout-owned candidate. Failed stop, join, and route-publication edges now leave a route-free recovery boundary instead of stranding the project in `stopping`. The desktop lifecycle surface also refuses start, stop, or restart against a stale/disconnected retained snapshot without sending a request or creating a phantom intent; an uncertain first request remains retryable until a baseline snapshot exists. Harbor now has a pure, session-fenced planner, an ephemeral exact-fence publication registry, and a durable boundary that re-reads Harbor-owned reservations, full-network ownership, project readiness, and the exact attached session before returning a route plan; these pieces are deliberately not wired to native relays until the managed-session handshake and real publication observations exist. Managed-session continuity, native service publication, daemon-restart/native recovery proof, resolver parity, trusted HTTPS installation, and packaging are still active work.

The current slice launches one default App at a direct `http://<assigned-loopback>:<project-port>` URL. Before any project-network mutation or process authority is created, `harbord` invokes the exact registered GoForj executable with `project:describe --json`, strictly validates the versioned secret-free schema, and stores its normalized topology digest in the active session. An invalid descriptor therefore fails the queued start without leaving a primary lease or endpoint reservation behind; if registration changes while admission rereads it, Harbor revalidates the admitted checkout before launch. Once that App is ready, `harbord` uses a local, read-only Docker Engine client to list candidate containers and inspect their immutable facts before projecting active Compose services into the Services UI. Admission requires the exact Compose project, service, and working-directory labels plus the registered canonical checkout; neighboring projects with the same service names are excluded. Harbor separately asks the exact supervising GoForj executable for its bounded resource-only `dev:status` report so framework-owned links can appear beside the directly observed services. The product-proof verifier now requires a typed event-refresh record with target/service identity, advancing project revision, replaced target container IDs, and unchanged peer IDs, along with an Engine 28-equivalent version floor and a platform-matched engine kind: Linux evidence must identify Docker Engine, while macOS and Windows evidence must identify Docker Desktop. Cleanup evidence is additionally bound to the exact lifecycle worker identity and artifact digest set. The tagged Linux Docker acceptance job now supplies explicit runner identity, writes the fixed lifecycle/cleanup manifests only after successful assertions and exact cleanup, independently verifies them, and uploads them; macOS and Windows product-worker evidence remains outstanding.

The daemon-owned container adapter implements local list, inspect, log-stream, and container-event wake calls. Event payloads are discarded: every wake repeats exact canonical checkout admission and reprojects services through a fenced project-revision/session-generation state mutation, pruning stale service-owned resources before route reconciliation. When the descriptor supports framework resources, the same wake also obtains a fresh resource catalog, atomically replaces the service/resource projection behind the same fences, and updates loopback HTTP reservations only after durable resource persistence; selected host-visible TCP requirements similarly refresh their namespaced public reservations, but native routes remain withheld until a private publication is observed; unsupported or failed framework queries retain the last-known resource links while still allowing service refresh. A pure Harbor planner now covers the next publication join boundary without persisting private upstreams or changing runtime topology: it rejects stale session/reservation generations, non-TCP reservations, invalid upstreams, and endpoint/socket collisions, and emits no route for an unobserved reservation. An ephemeral registry now holds only the latest complete observations for exact attached-session fences, atomically replaces them, and rejects stale writes or reads; it has no durable or relay authority of its own. The new durable boundary feeds that planner only Harbor-owned reservations, and revalidates full network ownership, a ready project, and the exact attached session before returning a plan; it also rejects authority drift between its two reads. Portable coordinator tests now cover the complete wake→fresh observation→fenced replacement→route publication edge, including quiet handling when the host event source is unsupported; these tests do not substitute for native Docker Desktop execution. A service-detail request travels through authenticated `harbord` control, the narrow Wails binding, and the typed Vue bridge; the daemon follows admitted current and recreated replicas while preserving a bounded, current-session cursor. If an event refresh briefly removes the selected service row, an already-authenticated same-session held read now returns a clean unavailable result without replaying output or crossing into a replacement session; initial unknown/external service selection remains rejected. This path does not require a GoForj service-log capability, parse Compose YAML, or perform Docker mutations, and neither generated Apps nor frontend clients receive Docker access. The direct two-fixture native Docker proof now builds on Linux and Darwin, proving exact checkout admission and neighbor exclusion, follows a recreated target replica, and compares fixture container IDs around Harbor's read-only calls; it still requires a local Engine/Docker Desktop worker for execution. The tagged three-project generated lifecycle test now performs a test-controlled Compose stop and force-recreate, waits for the event-driven durable service projection to disappear and reappear, proves the replacement container identity changed, and checks neighboring projects remained unchanged. The Linux Docker job is also configured to render and concurrently start three MySQL-enabled generated GoForj projects through Harbor, prove every admitted container ID belongs to one exact checkout and remains unchanged across Harbor service/log reads, open an exact service-log follower for each, stop one project while proving the other two peers remain ready, and restart that exact project; its temporary loopback identities have exact cleanup checks. A separate `productproof` verifier and `platformproof verify-docker-projects` command now fail closed on typed three-project lifecycle and exact-cleanup manifests, binds cleanup to the same worker identity and artifact digest set as lifecycle evidence, and the Linux acceptance job can now emit those two bounded files only after successful lifecycle assertions and cleanup. macOS and Windows product-worker evidence remains outstanding. That generated lifecycle test is Unix-gated and Darwin ARM64/AMD64 cross-buildable for execution on a real local Docker Desktop host, but this is not native proof. Workflow execution remains required evidence. Neither fixture is the required macOS/Windows Docker Desktop acceptance proof. The route reconciler now accepts any explicitly reserved HTTP resource whose owner is ready and whose URL remains the assigned literal loopback; unreserved framework links and observed ports remain private, while descriptor-driven HTTP and namespaced native endpoint reservation plus fenced event-driven refresh now exist at the readiness edge and the pure managed-publication join is tested but intentionally not live until the managed-session protocol supplies verified observations.

The pure managed-publication observation normalizer now supplies the registry boundary from existing Harbor facts: it selects only descriptor-declared Compose-owned host TCP endpoints, requires one exact native-port observation and matching durable reservation, emits a deterministic complete replacement, withdraws stale publications when facts are incomplete, and rejects malformed candidates without replacing the registry's last good set. It remains a Harbor-only join; it does not add a GoForj wire contract or activate native relays.

The Docker service watcher treats a transient Engine event-stream failure as a bounded reconnect condition. It retries the wake stream without treating the failed connection as topology, then surfaces a persistent failure after the retry budget; unsupported event sources remain quiet. Portable coordinator coverage exercises this reconnect boundary, but it does not substitute for native Docker Desktop execution.

The fresh Docker observation at both the ready edge and after a wake now classifies only Engine transport unavailability, retrying the exact checkout read within the same bounded, cancellation-aware budget. Canonicalization, label admission, malformed facts, and other non-transport failures remain terminal; no partial observation is ever projected as topology. If the ready-edge budget expires, Harbor preserves the healthy App and marks service observation unavailable as before; the event watcher can recover later. Portable coverage exercises both recovery and bounded terminal failure, but native Docker Desktop execution remains required evidence.

The desktop bridge treats every required generated Wails binding as part of its native capability boundary. Its fixture and selection tests include `ResourceIconURL`, reject native mode when that binding is absent, and exercise it through the authoritative bridge; this prevents browser fixtures from disguising an incomplete native bridge.

The framework-resource process harness canonicalizes both its supplied checkout and the child working directory before comparing them. This retains one exact checkout identity while accepting macOS's equivalent `/var` and `/private/var` spellings; it does not permit a different resolved checkout.

The Darwin host-conflict observer now permits a bounded 10 ms, cancellation-aware settlement delay after an explicitly detected native table-generation race. It still requires two matching complete observations, allows at most 31 retries after the initial pass (32 passes total), and returns no admission result if the tables remain ambiguous.

The hard-restart fixture keeps its deliberately signal-ignoring watcher and listener alive with a sleeping wait rather than an empty `select`, so Go's deadlock detector cannot terminate the fixture before the durable launch boundary. The restart assertion remains a native macOS proof requirement.

The Darwin PCB observer recognizes XNU's documented IPv6-family null-bind form that carries a canonical IPv4 fact. It accepts that form only with `INP_IPV4` and zero IPv4-in-IPv6 padding, projects it as IPv4 for conflict classification, and continues to reject mapped, noncanonical, and contradictory address facts. Its Darwin-native fixture now exercises that admission on a requested port and keeps a non-wildcard dual-stack flag combination as a fail-closed contradiction.

Phase 1 production acceptance now distinguishes active work from the bounded terminal operation history intentionally retained in every authoritative snapshot. Startup recovery and idempotent removal require no nonterminal operations; they do not erase completed operation evidence.

The native Unix IPC integration keeps its client connected until server-side peer admission completes. This preserves Darwin's `LOCAL_PEERCRED` boundary under test rather than converting a deliberately closed socket into a credential retry path.

The Darwin retained-runtime observer now treats a positive `PROC_PIDLISTFDS` size query as bounded descriptor-count evidence rather than a failed empty-buffer call. Its second read reserves twenty fixed descriptor records to distinguish a complete census from a growing one, then rejects a saturated, malformed, or over-limit result without signaling. The final signal gate rereads every captured session member's executable, argv digest/count, cwd, UID, process-group, session, parent, and birth facts, in addition to the exact socket and root checks, so non-root identity drift cannot authorize `SIGTERM`; the Darwin comparator suite now exercises each of those drift dimensions directly. Native CI execution remains the required proof.

The Linux `systemd-resolved` recovery path reconciles only bounded, root-owned Harbor transaction names before a public observation. It removes an unpublished canonical stage without restart, recognizes an exact public replacement plus retained owned stage as an exchange crash, restarts `systemd-resolved` while that stage remains as retry evidence, and removes it only after fixed/stage identity and content revalidation. A failed or uncertain restart therefore leaves the exchanged marker for the next recovery attempt. Recovery restores an exact owned removal quarantine only when the fixed artifact is absent, and preserves every foreign, malformed, unsafe, or ambiguous pairing. If more than one Harbor transaction artifact is present, recovery now rejects the ambiguous set before reading or mutating any artifact. Its strict `DNSEx` reader treats a zero port as the systemd representation of ordinary DNS's default port 53, while retaining exact address-family and server-name validation. The opt-in root test appends a 4 KiB-capped native cause when its initial observation or mutation fails; production errors remain typed and redacted. The new source behavior still needs a successful privileged Linux CI run before it is native support evidence.

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
| `internal/goforj` | Strict static `project:describe --json` observation and topology-digest validation. |
| `internal/projectprocess` | `forj dev` launch, descriptor preflight, output, process ownership, stop, and restart recovery. |
| `internal/networksetupapproval`, `internal/networkresolverapproval`, `internal/projectapproval` | Two-step, daemon-bound approval plans for sensitive actions. |
| `internal/network` | DNS, ingress, TCP relay, address identity, and data-plane primitives. |
| `internal/platform` | Native loopback, conflict detection, resolver, user-path, helper, and portable trust-policy adapters. |
| `internal/trust` | Local CA/certificate material plus the portable trust-store ownership/CAS boundary; native installation is not complete. |
| `internal/testkit/goforjproject` | Headless generated-project fixtures used by native hosted proofs, including an explicit GoForj MySQL/Compose render option included in the required hosted pinned-render check. |
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
4. before any project-network mutation, Start invokes the exact GoForj executable's `project:describe --json` command and rejects invalid static intent;
5. Harbor gives the project a stable primary loopback lease and checks the default App's HTTP port on that exact address;
6. Harbor writes its bounded network block to `.env.host` and launches `forj dev` without a shell;
7. the daemon records exact process evidence and waits for the App listener to become ready;
8. after App readiness, `harbord` lists local Docker containers and inspects exact candidates; only containers whose Compose project, service, and working-directory labels resolve to the registered canonical checkout become deterministic active service rows;
9. Harbor invokes the exact GoForj executable with `dev:status --json --resources-only` through the checkout and launch environment already owned by the supervisor; supported framework links enrich the project without supplying service state or logs;
10. ready state atomically publishes the default App/resource, admitted framework links, and directly observed services at its direct IP-literal HTTP URL; selected descriptor host-visible TCP service requirements now receive stable `<service>.<project>.test:<native-port>` reservations on the project's loopback identity, but no native relay is published until a managed-session or container observation proves its private upstream; later supported container wakes atomically refresh services and descriptor-matched framework links behind project/session fences, then reconcile exact HTTP reservations; the route reconciler can promote an explicitly reserved HTTP resource only after its owner and private upstream are ready, while the control snapshot exposes the corresponding `.test` URL only after that exact route is live;
11. current bounded stdout/stderr wakes a held, cursor-addressed desktop request as each pipe chunk arrives; the frontend incrementally applies ANSI styling and terminal redraw controls as safe Vue text;
12. selecting a Compose service opens a daemon-owned Docker log follower and streams bounded current-session output through authenticated control, Wails, and Vue; session changes reset the cursor and container recreation is followed without transferring lifecycle authority;
13. a local Docker container event wakes a daemon-owned refresh loop; Harbor discards the event payload, repeats exact admission, and atomically replaces the durable service/resource projection only when the fenced observation differs, assigning descriptor-backed HTTP endpoints after the resource write;
14. Stop or daemon shutdown settles the complete Harbor-owned process scope and its service-log followers before deleting session evidence, then retains observed service identities as stopped.
15. Restart uses one durable `project.restart` operation: it withdraws routes, settles the exact old session, records a stopped boundary without losing the operation identity, launches a replacement session through the normal descriptor/admission/readiness path, and succeeds only after the replacement is ready; retry and daemon-recovery paths preserve the same operation and process fences.

Start, Stop, and scoped Restart are exposed through the control protocol, desktop, and first-class `harbor start <project>`, `harbor stop <project>`, and `harbor restart <project>` commands. The CLI prints the authoritative operation state, offers `--json` for scripts, and includes an explicit retry intent after an indeterminate daemon call. `harbor open <project> [resource]` resolves a fresh project-scoped resource from the same snapshot and launches it through the fixed native browser handler for the current OS; it never accepts a URL from the caller.

`harbor status <project>` selects one project from the same authoritative snapshot used by desktop clients. Its default view is compact and `--json` prints that project object without a command-specific wrapper. `harbor open` defaults to the readiness-proven `app-http` resource and accepts an explicit project-local resource ID for framework links.

`harbor logs <project>` reads bounded current-session GoForj output. `--service <id>` selects one project-scoped Compose service and `--follow` holds an authenticated cursor until new output or interruption; neither path grants the CLI Docker or lifecycle authority.

`harbor doctor [project] [--json]` reports only authenticated daemon status, validated snapshot integrity, observed sequence drift, and raw project state/counts. Its explicit `control-plane` scope does not claim DNS, TLS, Docker, helper, descriptor, process, route, lease, or native-platform health; those require a future versioned diagnostics contract and native evidence.

This is intentionally narrow compatibility code, not a second GoForj parser. Registration reads `APP_NAME` from `.env`, then root `project_name` from `.goforj.yml`, then `APP_NAME` from `.env.example`. Runtime discovery reads only `API_HTTP_PORT` or `PORT` from `.env` and then `.env.example`, and verifies the generated `internal/http/runtime.go` default-host contract through bounded Go AST inspection. `Supervisor.Start` executes ordinary `forj dev` directly, without a shell or a managed-session protocol. Compose intent and mutations remain inside GoForj; Harbor's daemon adapter observes only exact checkout-attributed runtime facts. The resource-only GoForj query is optional enrichment, not a service-state or log transport. GoForj now exposes a read-only `forj project:describe --json` schema for static project identity, conventional available Apps, default HTTP runtime intent, a non-secret normalized topology digest, and a pure service-requirement projection derived from project-owned `.env.example`/`.env` layers. Harbor invokes that exact descriptor before production process launch, strictly validates schema v1, and stores its normalized digest in the active session; the validator also admits optional, strictly bounded resource intent and service requirements with stable IDs, explicit App consumers, endpoint protocols/ports/visibility, and no secret values. When those sections are present, ready framework links must match their owner, runtime, and path before Harbor stores them, and Harbor derives exact resource-label `.test` reservations only from those matched ready resources; selected host-visible TCP requirements also receive `service:`-namespaced native endpoint reservations, while private upstreams remain unassigned and no LAN/public exposure is inferred. The managed-session protocol remains future work. Runtime admission diagnostics now report the exact supported released `v0.20.1+` or clean development revision choices instead of implying that every newer development build is accepted. Those remaining contracts are described in [GoForj integration](./goforj-integration.md).

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

Legacy state created before complete scope receipts is now repairable during Start when Harbor still has the exact project primary lease and native evidence ties one same-user listener to the registered checkout. A port, PID, checkout path, `.env.host`, or command line alone still cannot prove ownership. If the durable lease or checkout correlation is absent, the explicit unattributed inspect/confirm path remains the safe fallback and must label the candidate as not proven Harbor-owned.

The retained-session repair is now wired through reconciliation, authenticated control, Wails, and the project detail view. Inspection accepts only a project ID; the daemon derives durable and native facts, retains the native receipt in a short-lived process-local plan bound to the authenticated caller, and returns only a fixed process-shape label (`forj dev` or `project listener`), checkout, endpoint, root PID, member count, expiry, and opaque selectors. Confirmation consumes the plan before revalidating every durable and native fence. Drift, ambiguity, expiry, caller mismatch, replacement, or incomplete settlement cannot signal a process and requires a fresh inspection.

The native implementation is currently macOS-only. Retained-session repair requires one exact same-user listener, a dedicated session rooted at the expected `forj dev` process, stable birth/executable/argv/cwd/socket/tree facts, and complete session plus socket settlement after graceful `SIGTERM`; one exact-session `SIGKILL` escalation is allowed only after the user confirms that candidate and the bounded graceful period does not converge. The durable scoped restart path now handles the normal live-session replacement and retains exact operation/session fences across its stop-to-start boundary, but its daemon-loss and native Docker/managed-session behavior still needs execution on a real macOS host before this becomes a native recovery claim. The already-retired case has a separate inspector and confirmation backend that correlates one exact listener either to a same-user `forj dev` root or, when that root has exited, to the same-user listener process whose working directory is the registered checkout; confirmation revalidates the scope, signals only the displayed root, and uses one exact captured-member `SIGKILL` escalation when the bounded graceful period does not settle, while waiting for captured births and the target socket before changing Harbor state. Start uses that same native backend automatically when the project's primary lease is exact; the caller-bound reconcile, authenticated control, Wails, and desktop confirmation path remains available for route-free projects without an automatic lease admission edge. The macOS CI selector now includes the non-dedicated inspection/confirmation/settlement proof alongside the retained-session lifecycle, ambiguity, drift, and checkout-owned-listener cases. Linux, Windows, and other adapters report unsupported through the same portable contract. Darwin AMD64/ARM64 cross-builds and portable tests pass, but the libproc observation and signal path still need execution on a real macOS host before this behavior is a native support claim. No-session confirmation intentionally returns the unchanged route-free retryable projection; it does not manufacture a Harbor session or perform durable completion mutation.

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
- a portable trust-store ownership/CAS adapter that validates public CA identity, preserves unrelated and pre-existing roots, and removes only exact Harbor-owned entries;
- activation of the process-local data plane after setup completes;
- project route replacement after lifecycle changes.

Important incomplete work:

- Windows NRPT portable tests and cross-compilation pass, and the daemon plus privileged helper now select the reviewed fixed-PowerShell backend. Its required elevated CI test exercises a fresh rule's observe, CAS add, bounded name-server Set repair, exact verification, and release; native workflow evidence and complete latent-field repair policy remain absent;
- native trust-store installation and complete trusted-HTTPS product proof are absent; the helper now carries strict public-CA trust tickets and bounded CAS evidence into a fail-closed handler boundary, while the portable adapter remains test infrastructure with no native backend yet;
- low-port mechanisms and native-port service relays are not complete product paths;
- the required three-real-project, full-stack acceptance test has not been reached.

Current platform capability is therefore uneven:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Loopback, host-conflict, process-scope, and local IPC foundations | Implemented with native proof for the exercised paths | Implemented with hosted native proof for exercised paths | Implemented with hosted native proof for exercised paths |
| Resolver backend | Darwin backend integrated and crash-recovery tested | `systemd-resolved` foundation includes cancelable locking plus owned stage/quarantine recovery; its root-only lifecycle and crash-recovery test is required in Linux CI, while broader resolver parity remains | NRPT backend is wired into daemon confirmation and the privileged helper; elevated workflow evidence and complete latent-field repair remain |
| Source helper installation | Automatic Wails development flow | Manual development bootstrap | Not implemented |
| Trusted CA/leaf use | Material plus portable ownership/CAS boundary and helper trust protocol; native trust installation absent | Material plus portable ownership/CAS boundary and helper trust protocol; native trust installation absent | Material plus portable ownership/CAS boundary and helper trust protocol; native trust installation absent |
| Low ports and shared public path | Darwin launchd primitives only | Not complete | Not complete |

## Desktop state

The desktop currently provides:

- the Harbor rail/context/detail layout derived from the GoForj Vue starter and adapted toward Lerd's density;
- daemon connection and snapshot updates through typed Wails bindings;
- project registration and removal;
- the typed project-removal approval handoff, including a retryable administrator-approval action for an active removal;
- network setup approval and helper installation/repair prompts;
- project Start/Stop/Restart actions and current failure feedback;
- an explicit stale-runtime inspection and destructive confirmation dialog for quarantined or route-free macOS projects, with one-use plans discarded on cancellation, reconnect, navigation, expiry, or any confirmation attempt; the dialog labels the process as a candidate rather than proven Harbor ownership;
- active conventional Compose services presented from `harbord`'s exact checkout-attributed Docker list/inspect observation after readiness, with observed service identities retained as stopped after shutdown;
- current-session Compose service logs streamed from the local Docker Engine through authenticated daemon control and narrow Wails bindings, with bounded cursors, reconnect/session resets, replica recreation handling, and no Docker access in Vue or generated Apps;
- wake-driven current project output with ANSI styling, carriage-return updates, and multiline terminal redraws;
- dark/light/system themes and themed toasts;
- a reusable, theme-aware Harbor illustration layer with responsive placement, bounded opacity, CSS edge fading, and non-interactive semantics;
- close-to-hide, single-instance relaunch focus, and native `Open Harbor`/`Quit Harbor UI` menu actions;
- frontend unit tests, browser fixture tests, Playwright smoke, and native module builds.

Those desktop lifecycle behaviors have unit coverage, but not release-grade native smoke. Tray integration, notifications, native approval/accessibility proof, signed installers, and release-grade platform smoke remain incomplete.

## Delivery status

No phase in `delivery-plan.md` has met its full exit gate.

| Phase | Status |
|---|---|
| Test fleet and evidence | Partial: hosted three-OS CI and bounded evidence exist; protected product workers, reboot coverage, and signed evidence do not. |
| Platform proof | Partial: loopbacks, helper, local CA primitives, and Darwin resolver exist; trust, low ports, and resolver parity do not. |
| Headless control plane | Partial but substantial: SQLite, authenticated IPC, operations, registration/removal, recovery, and acceptance coverage exist. |
| Network data plane | Partial: servers, planning, setup, activation, and routes exist; full cross-platform host integration is incomplete. |
| GoForj contract | Early/partial: discovery, strict static `forj project:describe --json` validation and session-digest capture, descriptor-constrained framework resource enrichment, readiness-edge and event-driven fenced HTTP endpoint assignment, `forj dev` supervision, direct read-only Docker service observation, daemon-owned service-log streaming, selected service-publication normalization, reserved-resource HTTP route projection, and a pure Harbor-owned session/network-fenced managed-publication-to-native-route boundary. Harbor still lacks dotenv-derived service intent, the live managed-session Compose contract, and native publication routing. |
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

The main CI workflow runs root, control, loopback, frontend, and desktop checks on Ubuntu 24.04, macOS 14, and Windows 2022 as applicable, with Node 22 for the frontend. The separate hosted platform-network workflow uses Ubuntu 24.04, macOS 15, and Windows 2022. It builds GoForj at pinned revision `d8a462840ca2c92a61a105f06408c464fcf53391`, provisions `127.77.254.10`, `127.77.254.11`, and `127.77.254.12`, queues and launches all three rendered projects before awaiting readiness, and requires them to use port 3000 concurrently through `internal/testkit/goforjproject`, followed by cleanup/evidence checks. Workflow execution is still required before this becomes native proof.

That hosted workflow is pre-provisioned API proof. It does not prove the shipping helper/consent flow, resolver installation, trusted TLS, Compose, reboot recovery, or the three-project product gate. Release support requires the stronger product environments described in [Cross-platform testing](./testing.md).
