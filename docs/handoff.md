# Development Handoff

Status: durable scoped project restart, resilient quarantined-project Start convergence, strict GoForj descriptor preflight-before-network/session digest, direct Docker service observation/logs with projection-gap-safe followers, read-only CLI log cursors, control-plane doctor foundation, fenced refresh, session-fenced managed-publication planning and ephemeral observation registry, and desktop project-removal approval handoff committed; native host proof remains

Last updated: 2026-07-21

## Read this first

Harbor is working far enough to register a real GoForj checkout, initialize macOS networking, assign a project-specific loopback, launch `forj dev`, detect App readiness, observe conventional Compose services, and stream current development and selected service output into the Wails desktop. It is not close to release-complete.

The work immediately before this handoff hardened the most painful current failure: `harbord` could disappear while a watcher-created child continued listening on the project's address. The old supervisor owned only the outer process group, so the surviving App looked like a foreign port conflict on restart. New Unix launches use a dedicated session, recovery settles all exact members across watcher-created process groups, and unresolved scope quarantines only the affected project instead of preventing daemon startup.

The regression is intentionally end to end: an outer process and daemon generation are killed, a listener in a separate process group ignores graceful shutdown, replacement recovery removes the complete owned scope, and a second Start reaches Ready on the same address and port.

Retained missing-receipt state now has a separate explicit recovery path. A quarantined project can inspect one bounded macOS candidate, show only reviewed display facts, and ask the user to confirm before the daemon revalidates and signals anything. The plan is caller-bound, short-lived, one-use, and consumed before confirmation; if the confirmed exact scope ignores graceful shutdown, the native backend performs one bounded SIGKILL escalation over that same revalidated scope before reporting failure. Normal Start no longer dead-ends on this state: an exact retained process receipt is settled through the native supervisor before a replacement session is admitted, and a receipt-free planned/awaiting-attach row is retired transactionally only when all process fields are absent. When a retained project-owned loopback has its App port occupied, the primary-lease admission path now performs the same native exact-checkout/same-user scope inspection and automatically confirms an actionable candidate before retrying the port probe; foreign, ambiguous, unreadable, or unsupported listeners still remain fail-closed. The desktop keeps the primary action as ordinary `Start project`; explicit inspection remains a separate fallback for already-retired or unattributed listeners. Failed stop, join, and route-publication edges now retain a route-free recovery boundary so the next Start can retry exact process settlement instead of inheriting `stopping`. The same authenticated repair surface now also checks route-free stopped, failed, or unavailable projects for an already-retired listener and routes confirmable evidence through the no-session inspector; confirmation returns the unchanged retryable project and performs no durable completion mutation. Linux and Windows preserve the same contract but currently return unsupported. The native Darwin implementation has portable and cross-build evidence, not a completed real-host proof.

Commit `b531d7f` replaces the GoForj service-state/log dependency with a daemon-owned, read-only Docker Engine vertical. `harbord` lists local containers, re-inspects candidates, and admits them only when the Compose project, service, and working-directory labels resolve to the registered canonical checkout. A selected service's current-session log stream now crosses authenticated control, Wails, and the typed Vue bridge. The continuation adds a Docker container-event wake hint; event payloads are discarded, exact admission is repeated, and service/resource changes are persisted only behind project-revision and session-generation fences. Supported descriptor resource reports then update loopback HTTP reservations after the durable write; unsupported or failed reports retain the prior resource links. No GoForj log capability is involved, neither the generated App nor Vue can access Docker, and the route reconciler now promotes only explicitly reserved HTTP resources whose owners and private loopback upstreams are ready.

The latest bounded Harbor-only slices add a pure managed-publication planner, an ephemeral exact-fence observation registry, and their durable authority boundary. The registry is process-local and non-durable: it accepts only an already-authenticated attached-session fence, stores a complete replacement set of bounded private publications, returns defensive snapshots, and rejects stale or foreign writes after close or session replacement. The boundary reads only Harbor-owned reservations from a full network aggregate, requires a ready project and exact attached session, validates the observed publication fences, and rechecks the session/reservation authority before returning the planner's deterministic routes. It joins only exact project/session/reservation generations, TCP endpoint reservations, and fresh canonical IPv4 loopback high-port upstreams; it withholds unobserved reservations and rejects duplicate or cross-route sockets before any relay can be constructed. The registry, planner, and boundary have direct tests but are intentionally not wired into the runtime until GoForj supplies the managed-session handshake and Harbor has an actual publication observation.

The service-log follow path now treats event-driven durable projection as a replaceable read model. After a service has been authenticated for the current session, a held read that races with its temporary removal returns a clean unavailable result without replaying output, resetting the cursor, or reading a replacement session. Initial unknown or external service IDs still fail before runtime access, and a simultaneous session change remains fenced.

The native desktop bridge fixture now includes the required `ResourceIconURL` binding. Native selection rejects its absence, while the native bridge test calls it directly; browser fixtures therefore cannot hide this generated-binding drift.

The ready-project Docker watcher now reconnects transient container-event stream failures within a bounded retry budget. Unsupported event sources remain quiet, persistent failures remain visible, and every successful wake still discards the event payload before performing fresh checkout-attributed observation.

The Docker observation helper now serves both the initial ready edge and post-wake refreshes. It retries only typed Engine transport unavailability, keeping canonical checkout admission and malformed runtime facts terminal. A successful retry still replaces the projection through the existing revision/session fences; an exhausted post-wake budget records a bounded asynchronous failure, while an exhausted ready-edge budget preserves the healthy App with service observation unavailable instead of inventing topology.

The framework-resource observer test harness now resolves checkout aliases before its exact process-context comparison. This unblocks its intended macOS process-context coverage when temporary directories are spelled through `/var` by the parent and `/private/var` by the child, without admitting another checkout.

The Darwin host-conflict observer has a bounded, cancellation-aware 10 ms pause between recognized native table-generation races, with at most 31 retries after the initial pass (32 passes total). It continues to require consecutive complete facts and returns an error rather than an admission result on unresolved churn.

The hard-restart integration helper now waits in a sleeping loop when it deliberately ignores graceful shutdown. Its prior empty `select` allowed Go to terminate the helper as deadlocked before the durable boundary; the actual restart/recovery contract is unchanged and still needs native macOS execution.

The Darwin PCB parser accepts the documented AF_INET6 null-bind record whose `INP_IPV4` fact is stored in the canonical padded IPv4 slot. It validates every padding byte and still fails closed on mapped, noncanonical, or contradictory family/address facts. This needs the next real macOS suite result before it is native proof.

Phase 1 acceptance treats retained terminal operations as durable client-visible history, as the snapshot contract specifies. Its completion boundaries now reject only queued, running, or approval-required operations; terminal evidence remains available for the subsequent durable-history assertions.

The Unix transport integration now waits for authenticated server acceptance before closing its client. This removes a test-only close race with Darwin `LOCAL_PEERCRED`; peer admission itself remains fail-closed. The Darwin PCB fixture follows the canonical IPv4-in-IPv6 acceptance contract: it uses a requested port for the admitted null-bind form and retains a non-wildcard dual-stack assertion as the contradictory case.

The Darwin retained-runtime libproc census now admits only a bounded positive `PROC_PIDLISTFDS` size result when no output buffer is supplied. The subsequent descriptor read adds a twenty-record spare margin and remains unreadable on saturation, malformed stride, or an over-limit census. This fixes the native observer's prior immediate `native_unreadable` classification without widening signal authority.

The Darwin retained-runtime final signal gate now rereads every captured session member's full native identity, not only PID and birth. Executable, argv digest/count, working directory, UID, process-group, session, parent, and birth drift therefore emits zero signals before the exact root `SIGTERM`; native execution remains required to prove the complete lifecycle.

The privileged Linux resolver test preserves its public typed failure contract but adds a 4 KiB-capped unwrapped native cause to its root-only observation and mutation failure reports. Linux recovery now distinguishes unpublished canonical staging from an exchanged old owned artifact: only the latter restarts `systemd-resolved` while the old stage remains as retry evidence, then removes it after fixed/stage identity and content revalidation. A failed or uncertain restart leaves that marker intact for the next attempt. Its `DNSEx` reader normalizes only systemd's zero-port default representation to port 53. Foreign, malformed, unsafe, excess, and ambiguous transaction states remain preserved and fail closed; recovery rejects any multi-artifact transaction set before mutation so one bad remnant cannot cause partial cleanup. Native CI evidence is still required.

Do not expand scope before reading [Current implementation state](./current-state.md), this handoff, and the relevant design document.

## Product decisions that should not be reopened casually

- `harbord` is the only durable writer and runtime reconciler. The CLI and desktop are clients.
- Harbor is per-user. The daemon and desktop do not run as root or Administrator.
- Elevation is explicit and one-shot through an allowlisted helper.
- Each project keeps its own Apps, Compose services, versions, containers, and volumes. Harbor shares control-plane infrastructure, not one global MySQL/Redis/mail stack.
- Projects reuse ordinary ports by receiving distinct loopback identities. Port offsets are not the product model.
- One shared DNS and HTTP/TLS ingress serves stable `.test` names.
- GoForj owns project composition and `forj dev`; Harbor orchestrates it instead of reproducing `.goforj.yml` semantics.
- Wails v2 remains the stable desktop host. Vue 3, TypeScript, Vite, Tailwind 4, Pinia, Reka/shadcn primitives, and Lucide are the frontend foundation.
- The GoForj Vue starter's component bones were preserved; Harbor's information architecture and visual density are adapted toward Lerd, not constrained to the starter's original page layout.
- Current activity matters more than a long operation-history wall. Project detail shows live state, the actionable failure, and current bounded `forj dev` output. Output delivery is wake-driven over the authenticated control connection and terminal redraw controls update existing rows instead of becoming noisy history.
- Harbor never parses Compose YAML or `forj dev` prose. GoForj owns Compose intent and every mutation. Only `harbord` receives the read-only local Docker Engine boundary. The current adapter uses list, inspect, log-stream, and event-wake calls attributed through exact Compose project/service/working-directory labels and canonical checkout ownership; event payloads never become durable facts, generated Apps and frontend clients never inspect Docker. Publication routing requires an explicit durable endpoint reservation plus a matching ready resource; container observation remains a wake/readiness fact, not publication permission.
- Existing generated API Index and example surfaces remain valuable GoForj resources; Harbor should surface them, not replace or remove them.
- Compatibility and cross-platform support are claims only when macOS, Linux, and Windows native tests prove the crucial behavior.

The reference products have deliberate, different roles. Herd is the product-experience reference, Yerd is the closest control-plane reference, and Lerd is the operational edge-case, test, and visual-layout reference. They are research inputs, not Harbor's architecture authority: GoForj remains authoritative for project semantics, and Harbor's per-project loopback identity, native same-port services, ownership model, and cross-platform proof requirements remain distinct. Read [Research](./research.md) for the pinned audits and [Frontend](./frontend.md) before adapting Lerd-influenced UI structure or styling.

## Objective and definition of done

The project goal remains the complete cross-platform Harbor MVP described by [Delivery plan](./delivery-plan.md), not merely the working macOS slice.

Completion requires evidence for the whole product contract:

- `harbord` is a durable per-user daemon and the CLI, desktop, and tray are equivalent clients of its versioned protocol;
- three real generated GoForj projects run concurrently with the same ordinary App and native service ports;
- project-owned Compose services and volumes remain isolated and survive stop, unregister, update, and uninstall;
- stable `.test` DNS, trusted TLS, common HTTP ingress, loopback identity, and native TCP relays work on macOS, Linux, and Windows;
- helper installation, repair, ownership, rollback, and cleanup mutate only exact Harbor-owned host state;
- abrupt daemon, desktop, worker, container-engine, network, sleep, login, and reboot transitions converge without inferred ownership or manual database repair;
- the typed GoForj descriptor and managed-session contract replace the tactical `.env.host` bridge without breaking standalone `forj dev`;
- native OS CI proves the crucial resolver, loopback, certificate, privilege, process, desktop, and cleanup behavior;
- documentation, installers, packaging, and release evidence match the implemented support matrix.

No current phase has passed all of its exit gates. Keep this full objective intact while taking the dependency-ordered next step below.

## Current development workflow

From `/workspace/code/harbor`:

```sh
forj dev
```

That command builds and watches `harbord`, runs migrations and the foreground daemon, and starts Wails. No special environment flag is required.

The equivalent daemon-only loop is:

```sh
go build -o ./bin/harbord ./cmd/harbord
./bin/harbord migrate
./bin/harbord --foreground
```

The desktop can be run separately with `wails dev` from `desktop` when a daemon is already available.

The project contains two Go modules. Root `go test ./...` does not validate `desktop`.

## Host reproduction at handoff

The live development host used during this session was macOS under user `cmiles`.

- Harbor checkout: `/Users/cmiles/code/harbor`
- Real test project: `/Users/cmiles/code/ditracker`
- Display name: `Diablo Immortal Tracker`
- Harbor database: `/Users/cmiles/Library/Application Support/GoForj/Harbor/harbor.db`
- Assigned address observed in the project: `127.77.59.72`
- Default App port: `3000`
- Project launch trace: `/Users/cmiles/code/ditracker/_data/harbor/forj-dev.log`

Network setup eventually completed through the desktop after several Darwin helper/bootstrap fixes. The desktop displayed `Harbor networking is ready`, the project was started, and the App became online on `127.77.59.72:3000`.

The real checkout currently has a Harbor-managed `.env.host` block similar to:

```dotenv
# harbor managed: begin
API_HTTP_HOST="127.77.59.72"
DB_HOST="127.77.59.72"
DEV_SERVICE_IP_ADDRESS="127.77.59.72"
IP_ADDRESS="127.77.59.72"
LIGHTHOUSE_URL="ws://127.77.59.72:3000/lighthouse/ws/agent"
MAIL_SMTP_HOST="127.77.59.72"
RAPIDOCR_URL="http://127.77.59.72:9003/ocr"
REDIS_HOST="127.77.59.72"
# harbor managed: end
```

That block is an accepted tactical bridge. Harbor derives additional rewrites from literal local hosts already present in `.env.host`, preserves content outside the markers, and replaces the block atomically. A fully settled Harbor-requested shutdown removes only the exact managed block; malformed marker ownership remains untouched and makes cleanup fail. Each accepted launch also replaces an owner-private, 4 MiB-bounded `_data/harbor/forj-dev.log`; both checkout mutations are temporary deviations from the target managed-session storage model.

## Existing stale listener on the macOS host

Before the complete-session fix, the user observed:

```text
COMMAND    PID   USER   FD   TYPE  NAME
.app.run- 6275  cmiles 14u  IPv4  TCP 127.77.59.72:3000 (LISTEN)
```

The corresponding durable session evidence had already been retired by older code. The new recovery logic cannot safely claim that already-orphaned PID retroactively. A port and PID are not ownership proof.

If this host remnant is still present, re-observe it and inspect the current owner before signaling. Do not reuse the historical PID blindly:

```sh
lsof -nP -iTCP@127.77.59.72:3000 -sTCP:LISTEN
PID="$(lsof -t -nP -iTCP@127.77.59.72:3000 -sTCP:LISTEN)"
ps -o pid=,ppid=,pgid=,command= -p "$PID"
lsof -a -p "$PID" -d cwd -Fn
```

Any manual signal is a human-owned emergency action outside Harbor's safety guarantees. Re-run the socket observation and confirm that it still names the same PID immediately before using Activity Monitor or `kill`; ordinary macOS PID signaling cannot eliminate the remaining observation-to-signal race.

If it respawns, inspect the replacement and its parent before signaling anything else:

```sh
PID="$(lsof -t -nP -iTCP@127.77.59.72:3000 -sTCP:LISTEN)"
ps -o pid=,ppid=,pgid=,command= -p "$PID"
```

Do not delete SQLite state, wipe the database, or make Harbor kill whatever happens to own port 3000. New sessions launched after the process-scope commit retain enough evidence for automatic recovery. A retained pre-receipt quarantine no longer blocks a normal Start: Harbor retires only the receipt-free row, then lets the normal process admission and native port checks establish the replacement runtime. An already-retired remnant still needs a separate, explicitly unattributed listener-inspection action or manual user action.

## What the process-scope stopping commit changes

The logical commit is `06b542f fix: recover complete project process scopes`.

- Unix starts use `setsid`, making the launch PID the dedicated session ID.
- Durable Unix birth evidence is marked with `harbor-unix-session-v1:` so recovery can distinguish complete-session receipts from legacy process-group evidence.
- Linux and Darwin enumerate live members of that exact session and revalidate birth plus session immediately before signaling.
- Watcher-created process groups remain inside the Harbor-owned session and cannot evade cleanup merely by changing PGID.
- Linux and Darwin zombie processes are distinguished from live members so a zombie-only session can settle without signaling unrelated processes.
- Windows keeps its Job Object ownership model and native creation token.
- Normal child exit errors are separate from `ScopeSettlementErr`; durable evidence is deleted only when the entire scope settled.
- Post-acceptance launch cleanup exposes `ErrCleanupUncertain` instead of pretending that no process escaped.
- Uncertainty during live Start, Stop, daemon shutdown, or restart recovery atomically makes only that project unavailable and route-free, retains its session/evidence, and fails the affected operation.
- A subsequent Start is a convergence edge: exact retained evidence is settled before replacement admission, while a quarantined session with no process receipt is retired only after a fenced all-null receipt check.
- Successful project-local quarantine does not poison coordinator health or abort all of `harbord`.
- The hard-restart integration test proves the same port can be used by a second successful Start after exact cleanup.

## Git and worktree boundary

Work is being committed directly to `main` during rapid development. Normal Git operations must remain under `Chris Miles <chris.miles.e@gmail.com>`.

The stopping baseline contains these logical commits:

- `06b542f fix: recover complete project process scopes`
- `3d6bd7a docs: record Harbor development handoff`
- `cdc496e feat: add Linux resolver integration foundation`
- `c84093f feat: add Windows NRPT resolver backend foundation`

The retained-runtime continuation is split into focused commits:

- `0a3bb00 feat: add retained runtime repair boundary`
- `44a1165 fix: clear completed runtime recovery warning`
- `ac5d508 feat: define runtime repair control contract`
- `4520f61 feat: add macOS runtime repair backend`
- `44dbffe feat: coordinate retained runtime repair`
- `78a35f6 feat: expose project runtime repair control`
- `5615316 feat: add desktop runtime repair flow`
- `b531d7f feat: add direct container service tooling`
- `3e3e2a7 feat: add unattributed runtime inspection boundary`
- `92da708 feat: add read-only unattributed runtime inspector`
- `99bf58b feat: add unattributed runtime confirmation backend`
- `0df5011 feat: surface unattributed runtime repair`

No push was part of the stopping sequence. Confirm the live relationship rather than relying on a recorded ahead/behind count after more work:

```sh
git status -sb
git rev-list --left-right --count origin/main...HEAD
git log --oneline origin/main..HEAD
```

The resolver source is committed in the two foundation checkpoints above. The unexplained local artifact `desktop/package-lock.json` was deliberately excluded because its ownership or intent was not established; `.tmp/` is also runtime scratch and must remain untracked if it reappears. Live `git status` remains authoritative. Preserve those paths, avoid `git add -A`, stage explicit files, and inspect the cached diff before every commit.

The direct Docker service/log vertical spans `internal/containerruntime`, project supervision and reconciliation, authenticated control, the Wails bridge and generated fixture, and the Vue service-log panel. Its two-fixture native adapter proof now builds on both Linux and Darwin; execution still requires the dedicated local Engine/Docker Desktop worker described below. Continue excluding the unexplained top-level `desktop/package-lock.json` unless its ownership is established.

## Resolver follow-up review

The Linux `systemd-resolved` integration is committed and passed its focused unit, vet, helper, wire, race, and compile checks. It is not release-ready.

Current constraints:

1. Recovery has direct portable coverage for its stage decision table and overflow bound, plus opt-in privileged coverage for staged, exchanged, and quarantined crashes and foreign-quarantine preservation. A successful Linux native CI run is still required.
2. The CI reset now uses a late-sorting drop-in to override earlier global resolver settings. The native test's initial observation remains the prerequisite check, and imported foreign DNS must still fail closed with a diagnostic.
3. The implementation is intentionally fail-closed: an unknown transaction name, a foreign or unsafe artifact, an excess transaction set, or an ambiguous stage/fixed pairing blocks observation and mutation rather than cleaning up host state.

The implementation is also much larger than expected for one fixed `systemd-resolved` drop-in. Simplify where possible while retaining descriptor-bound, no-follow, exact-correlation, rollback, and command-allowlist protections.

The Windows NRPT core is now wired through both production resolver providers. Its portable focused/full package tests, vet, and Windows cross-compile pass, but it remains incomplete:

- the daemon confirmation observer and privileged helper now select the same reviewed adapter, whose fixed PowerShell runner is limited to the immutable NRPT program and system-directory executable;
- its native runner now derives the fixed Windows PowerShell executable from the native system-directory API rather than a caller-controlled `PATH`;
- native exactness rejects latent NRPT fields such as IPsec CA restrictions, DirectAccess settings, and DNSSEC encryption. Before any Set operation, a second raw snapshot rejects those fields with zero mutation until a native clearing contract and proof exist;
- the helper dependency guard admits `os/exec` only through the reviewed Windows resolver adapter, while native Go/PowerShell execution parity and workflow evidence remain required. A required elevated CI test covers a fresh rule's observe, CAS add, bounded name-server Set repair, exact verification, and release.

Do not claim either platform's resolver as complete merely because its foundation is committed or cross-compiles.

## Current implementation status

No delivery phase has met its complete exit gate.

- Hosted three-OS CI, Phase 1 evidence, and privileged loopback tests exist.
- The hosted same-port proof now provisions three exact loopback identities, queues and launches all three rendered GoForj projects before awaiting readiness on port 3000, and rejects evidence or cleanup that omits any identity; workflow execution remains required native evidence.
- Durable SQLite state, authenticated local RPC, operation journals, project registration/removal, network setup approvals, and recovery are substantial.
- DNS, HTTP ingress, TCP relay, local CA/certificate primitives, loopback identity, Darwin resolver ownership, Linux resolver integration foundation, the production-wired Windows NRPT backend, and runtime activation exist.
- Wails/Vue can add/remove projects, set up networking, start/stop a project, inspect and explicitly confirm one retained macOS runtime candidate, display actionable errors, stream current ANSI-formatted development output, and follow a selected Compose service's current-session logs through independent held cursors with incremental terminal redraws.
- GoForj now has a pure `forj project:describe --json` v1 starting contract for static project identity, available App inventory, conventional HTTP runtime defaults, and a non-secret topology digest. It does not read dotenv files, mutate the checkout, or execute generated code. Harbor invokes and strictly validates that descriptor before production process launch, persisting only its normalized digest in the active session. The optional resource-intent section now crosses the process boundary, constrains live framework links by stable owner/runtime/path identity, and drives exact resource-label `.test` endpoint reservations after readiness; Harbor still does not infer private ports or external exposure from observation. A pure session-fenced managed-publication planner now validates the next native-route join without making it live, and its ephemeral registry now provides a non-durable handoff for complete, exact-session private publication observations. Dotenv-derived service intent and the managed-session handshake do not yet exist. Startup service state therefore still comes from `harbord`'s read-only local Docker list/inspect adapter, while the optional GoForj resource-only status query supplies framework links rather than container state or logs. Direct service logs are daemon-owned and do not depend on a GoForj log capability.
- GoForj executable admission diagnostics now match the exact policy: a released `v0.20.1+` build or the clean development revision `d8a462840ca2c92a61a105f06408c464fcf53391`; newer unversioned development revisions remain rejected.
- Terminal-owned attachment, three-real-project acceptance, native resolver evidence, trust installation, low ports, tray, signed packaging, updates, and release evidence remain incomplete. The readiness edge and supported event wakes derive optional HTTP reservations from descriptor-matched resources; the durable resource replacement precedes endpoint assignment and route reconciliation, while unsupported or failed framework queries retain their last-known links. Portable coordinator coverage proves the wake hint is followed by fresh service/framework observations, fenced replacement, and route publication; native macOS Docker Desktop execution remains required evidence.
- `internal/platform/trust` now supplies the portable trusted-HTTPS ownership boundary: requests bind one public CA fingerprint and trust mechanism, observations classify only matching or explicitly marked entries, compare-and-swap fingerprints guard effects, unrelated roots are preserved, and pre-existing identical roots remain reusable but unowned. The privileged helper now admits strict public-CA trust tickets and returns bounded CAS postcondition evidence, including the preserved-unowned pre-existing case; the entrypoint still selects an unavailable trust handler because native macOS, Linux, and Windows trust-store backends and product-worker proof remain outstanding.
- Project Start/Stop/Restart exists in control, desktop, and first-class CLI surfaces; the CLI exposes `harbor start <project>`, `harbor stop <project>`, and `harbor restart <project>` with `--json` and an explicit retry intent. Restart keeps one durable `project.restart` operation across the exact stop boundary and replacement readiness path.
- `harbor status <project>` provides the CLI's read-only view of one project from the authoritative daemon snapshot, with compact human output or the exact typed project object through `--json`.
- `harbor open <project> [resource]` now resolves one fresh, project-scoped resource and invokes only the fixed native browser handler; it defaults to `app-http` and never accepts a caller-supplied URL.
- `harbor logs <project>` now exposes the existing bounded current-session project and Compose-service cursors through a read-only CLI surface, with `--follow` using the negotiated held-read capability and resetting on session replacement.
- `harbor doctor [project] [--json]` now emits a versioned, explicitly `control-plane` report from authenticated daemon status and a validated snapshot; it records sequence drift and raw project state without claiming native host health or offering repairs.
- The pending resource-projection repair migration retains only the readiness-proven `app-http` resource and removes older optional runtime links that could make the daemon reject its complete snapshot. It is intentionally one-way: the links are derived and are rebuilt on a successful Start; it does not affect project source, volumes, secrets, or operations.
- The desktop now exposes the typed project-removal approval handoff: an active `requires_approval` removal retains its intent, offers one explicit administrator-approval action, consumes the terminal result, and remains retryable after a declined or unavailable native approval. Native consent execution and release-grade macOS proof remain outstanding.

[Current implementation state](./current-state.md) contains the fuller matrix and commands.

## Immediate next goal

The durable scoped project-restart slice is now implemented and covered by state, coordinator, control, CLI, Wails, desktop-store, and desktop-view tests. Native macOS remains the next evidence target: prove the direct Docker service/log vertical and event-driven refresh, then exercise daemon-restart/retained-runtime recovery and a second Start on the same endpoint. The existing generated three-project lifecycle proof remains harness coverage until a protected product worker executes it.

Prove the committed direct Docker service/log vertical and its event-driven service refresh on the macOS development host before opening another product front. Exercise one real multi-container GoForj checkout through service discovery, log output, container recreation, an event-triggered topology refresh, project Stop, and project restart. The tagged three-project generated lifecycle test now performs the test-controlled stop/force-recreate sequence, waits for the durable service projection to disappear and reappear, compares replacement container IDs, and checks neighboring projects remain unchanged; this is harness coverage, not native evidence until a worker executes it. The proof must show that similarly named neighboring Compose projects are excluded and that Harbor never performs an Engine mutation. The hosted pinned-render check renders two generated fixtures with GoForj's `docker` and `database_mysql` components. The focused Linux Docker gate is configured to render and concurrently launch three MySQL-enabled generated projects through Harbor, prove each admitted container ID belongs to one checkout and remains stable across Harbor service/log reads, open one exact service-log follower per project, stop and restart one project while proving its two peers remain ready, and clean up exact temporary loopbacks; its workflow now supplies explicit runner identity, writes/verifies/uploads the fixed productproof manifests after successful cleanup, and remains required evidence rather than macOS Docker Desktop evidence. A separate typed `productproof` verifier and `platformproof verify-docker-projects` command now reject incomplete, cross-commit, cross-platform, translated-port, shared-container, dependency-identity, assertion, and cleanup evidence; cleanup is bound to the exact lifecycle worker identity and artifact digest set, and macOS and Windows product-worker manifests remain outstanding. The verifier now requires a typed event-refresh record containing target/service identity, an advancing project revision, disjoint replacement IDs, and unchanged peer IDs, in addition to the Docker Engine 28-equivalent version floor; it also binds Linux manifests to Docker Engine and macOS/Windows manifests to Docker Desktop. The same generated lifecycle test is now Unix-gated for manual execution on a real macOS Docker Desktop host, with Darwin ARM64/AMD64 compile checks only; it needs a protected product-worker gate and native execution before it becomes evidence. Its separate two-fixture adapter test exercises exact admission, neighbor exclusion, log following, and target recreation, and verifies that the adapter did not change either fixture before the test-controlled recreation. Portable tests, both Go modules, frontend typechecking/unit/build, generator parity, and Chromium/Firefox browser suites are green; WebKit still needs an environment with its native runtime dependencies.

The route reconciler now implements the publication-derived join through Harbor's existing resource/routing policy: an explicit HTTP endpoint reservation, a matching resource, a ready owner, and an exact assigned loopback upstream are all required. When a descriptor reports resources, Harbor replaces optional HTTP reservations with the enabled, descriptor-matched ready resources and names them `<resource-id>.<project>.test` at readiness and after supported event-driven refresh; selected host-visible TCP service requirements now receive `service:<endpoint-id>` reservations at `<service>.<project>.test:<native-port>`, while the private upstream and native relay remain withheld until managed-session or container evidence exists. The pure planner and its new durable boundary validate this future native join against exact project/session and reservation generations, require full-network ownership and an attached ready session, reject authority drift and collisions, and emit no route for an unobserved endpoint; they do not persist private upstreams or mutate the live data plane. Harbor preserves unrelated native TCP reservations and the default `app-http` authority, and removes only its namespaced service reservations on a fresh supported descriptor. Observing a port is not permission to publish it. Keep the event payload as a wake hint only and preserve the existing exact admission boundary. Managed-session negotiation remains the next contract needed to turn these reservations into live native relays.

The ingress remains loopback-only in the current product slice. LAN sharing, user-owned NAT, and Cloudflare-style public reachability are deliberately a separate future exposure grant: a named App, an explicitly selected concrete host address, exact external Host/SNI names, and revalidation on interface or edge drift. Do not broaden the proxy to wildcard binds or treat a public DNS record as Harbor ownership; the detailed contract is in [Networking](./networking.md).

The retained-session repair still needs native execution on a real macOS host: reproduce Start, abrupt daemon loss, automatic retry, cancellation, confirmation, complete settlement, and a second Start on the same endpoint. The historical already-retired listener now reaches the same caller-bound reconcile/control/desktop flow through its read-only durable no-session/lease boundary and separate macOS inspector/confirmation backend; the desktop exposes a read-only check for route-free stopped, failed, or unavailable projects, and confirmation performs no no-session durable mutation. Native Darwin execution proof remains. The desktop project-removal approval handoff is now wired; native consent execution/proof, trusted routing, and tray presence remain later bounded desktop slices. Linux resolver crash recovery now has focused stage/exchange/quarantine/foreign-state coverage and a root-only lifecycle command required by Linux CI. That workflow evidence is still required before claiming native resolver support.

The production lifecycle path now invokes the exact registered GoForj executable with `project:describe --json` before any project-network mutation or process authority. Harbor strictly admits descriptor schema v1, rejects unknown or unsafe fields, and stores the descriptor's normalized topology digest as the active session digest; an invalid descriptor leaves no primary lease or endpoint reservation, and a registration path change is revalidated after lease admission before launch. The validator carries optional resource intent and service requirements through process admission: service requirements retain stable IDs, explicit App consumers, and distinct endpoint IDs/protocols/ports/visibility without exposing values. At readiness and on supported Docker event wakes, Harbor joins enabled descriptor resources to live owner/runtime/path facts and persists exact loopback HTTP reservations, while selected host-visible TCP requirements receive stable `service:<endpoint-id>` reservations on `<service>.<project>.test:<native-port>`; stale Harbor service reservations are removed only from that namespace, and unrelated HTTP/TCP authority is retained. Native relay publication remains withheld until a managed-session or container observation proves a private upstream. Unsupported or failed framework queries keep the prior resource links while service refresh continues. The desktop lifecycle surface now refuses start/stop/restart while a retained snapshot is stale or the daemon is disconnected, without creating a new client intent; it still permits a retry of an uncertain first request before any baseline snapshot exists. Portable subprocess, schema, timeout, ordering, resource-validation, service-requirement, endpoint-assignment, lifecycle, and desktop-guard coverage is green. The current GoForj producer still does not emit service requirements, and the managed-session handshake remains future work.

## Next-session start checklist

1. Read `AGENTS.md`, [Current implementation state](./current-state.md), and this handoff before changing code.
2. Run `git status -sb`, inspect `origin/main...HEAD`, and preserve unexplained local artifacts rather than sweeping them into a commit.
3. Re-observe the macOS host and Ditracker runtime; paths, PIDs, listeners, leases, and database rows in this document are historical evidence until confirmed.
4. Reproduce a normal Start, abrupt daemon restart, automatic exact-scope recovery, replacement Start from both exact and receipt-free quarantine, explicit retained-session repair, Stop, and second Start before claiming native recovery complete.
5. Prove the direct Docker service/log slice plus event-driven topology refresh on native macOS before adding publication routing.
6. Validate both Go modules and any affected frontend or native OS surface.
7. Commit explicit paths as `Chris Miles <chris.miles.e@gmail.com>` and update `current-state.md` plus this handoff when the continuation point changes.

## Retained stale-runtime repair implementation

The retained-session case uses a two-call, user-confirmed, process-local inspection plan. The already-retired listener is a separate case and must not be smuggled through the retained-session mutation.

### Inspect

The `control.project-runtime-repair.v1` capability exposes an inspect method taking only `ProjectID`. The daemon, not the client, derives the project revision, unresolved session, network/lease revision, assigned address, target port, and native candidate.

Inspection is eligible for repair only when all of these correlate:

- project is route-free and unavailable;
- latest lifecycle marker is `project.recovery.ambiguous_launch`;
- a retained Harbor-owned `awaiting_attach` session has the expected legacy missing-evidence shape;
- the primary network lease is exact and current;
- default runtime discovery derives the same address and port;
- one same-user process uniquely correlates by native socket owner, immutable birth, executable, working directory, sanitized command identity, and stable parent/descendant facts.

Inspection returns bounded display facts and an opaque inspection ID/fingerprint. It never returns raw birth tokens, argv, or environment values. Zero, multiple, cross-user, unreadable, or partially correlated candidates are diagnostic only and cannot be confirmed.

### Confirm

Confirmation supplies only project ID, opaque inspection ID, and candidate fingerprint. The server binds the plan to the authenticated caller, project/session/network generations, exact target, and a short expiry.

Before signaling, re-read and compare every durable fence and every native process/socket fact. Any drift emits zero signals and expires the plan. Stop only the exact candidate scope shown to the user; never accept a PID or address from the client and never kill “whatever owns this port.”

The first version uses graceful exact termination and polls exact birth absence, complete session absence, and socket release. A watcher respawn or identity change fails confirmation and requires a fresh inspection. Only after every postcondition succeeds does one atomic state mutation retire the retained session and project the route-free project to stopped.

The desktop keeps an explicit inspection action for the cases that still need native user confirmation, but it is not part of the ordinary recovery action. Start first attempts automatic convergence and is no longer a `project.recovery.ambiguous_launch` dead end; the primary button remains `Start project`. Its warning is:

> Harbor no longer has its launch receipt. This process is a candidate, not proven Harbor-owned. Continue only if you recognize it as this project.

The confirmation label should describe the effect, such as `Stop this process and reset project`, rather than generic `Repair`.

Portable tests cover caller mismatch, expiry, revision drift, PID reuse, birth/executable/argv/cwd/socket/parent drift, multiple owners, respawn, cancellation, one-use consumption, and zero-signal failed branches. Native macOS execution is still required; Linux and Windows currently prove only the explicit unsupported seam.

### Already-retired listener

When Start reports `project.network.port_unavailable` and no session remains, Harbor may offer `Inspect listener`. The result must say that Harbor has no launch receipt and does not own the candidate. It is actionable only when native inspection finds one same-user `forj dev` scope whose checkout, command identity, process births, parent/descendant relationships, and exact socket all correlate. A lone child listener without a stable root is diagnostic only.

If the user explicitly confirms, revalidate the entire displayed scope immediately before signaling, stop only that exact scope, and prove both birth absence and socket release. Do not mutate a session because none exists. Retry ordinary Start from its existing stopped/failed projection only after the host postcondition. Any drift, replacement, respawn, ambiguity, or unreadable fact sends no signal and requires a new inspection.

## Validation at the stopping point

The process-scope patch was validated with:

```sh
go test ./internal/projectprocess ./internal/reconcile -count=1

go test ./internal/state \
  -run 'Quarantine(ProjectProcessScope|TerminalProjectSession)|RecordUnexpectedProjectExit' -count=1

go test ./internal/reconcile \
  -run '^TestProjectLifecycleHardRestartConvergesManagedProcess$' -count=3

go vet ./internal/projectprocess ./internal/state ./internal/reconcile
```

Darwin, Windows, and FreeBSD project-process cross-compiles and Darwin/Windows lifecycle cross-compiles also passed. The final zombie and failed-project adjustments have focused tests and must remain covered by the committed suite.

The retained-runtime slice passed full root and desktop Go tests and vet, focused race tests across `projectprocess`, `reconcile`, `control`, and `authority`, all frontend unit tests, production frontend build, and generator parity. Chromium and Firefox browser suites pass after the Wails fixtures were extended with the new binding surface. WebKit cannot launch in the current Linux workspace because its GTK/GStreamer host libraries are absent. Darwin ARM64 and AMD64 cgo-free project-process test binaries link successfully; that is build evidence only, not native libproc/signaling proof. The Darwin comparator suite now covers birth, parent, process-group, session, UID, executable, argv digest/count, command exactness, and working-directory drift for non-root members. The required macOS job runs the exact graceful lifecycle, a non-dedicated-session ambiguity case that must leave its listener alive, a replacement-listener drift case that must receive no signal, and the newly wired non-dedicated inspection/confirmation/settlement case; workflow execution remains the native evidence gate.

The Linux resolver checkpoint also passed isolated root-module tests and vet, focused resolver/helper/wire tests, and source formatting. Its root-only CI command covers normal lifecycle, owned crash recovery, and preservation of a foreign quarantine artifact; workflow evidence is still required. The Windows checkpoint now covers daemon/helper provider wiring in addition to focused and full resolver package tests, vet, and Windows AMD64/ARM64 cross-compiles. Its required elevated CI command creates, verifies, and removes one fresh local NRPT rule; workflow evidence is still required before this becomes native support evidence.

## Things not to do

- Do not infer process ownership from a busy port.
- Do not clear a session row that retains process evidence; receipt-free quarantines may only be retired through the fenced lifecycle release path.
- Do not wipe the user's database as ordinary recovery.
- Do not let one quarantined project prevent the daemon or other projects from starting.
- Do not pass Harbor's assignment through new special `forj dev` flags.
- Do not make generated Apps or the frontend inspect Docker, and do not let Harbor's read-only adapter perform Compose or Docker mutations.
- Do not replace the accepted `.env.host` bridge until a working GoForj managed-session overlay exists.
- Do not run `forj dev`, Wails, or `harbord` with elevation.
- Do not hand-write startup schema creation; use embedded GoForj migrations.
- Do not stage `.tmp/` or an unexplained lockfile with unrelated work.
- Do not claim Linux/Windows resolver support from cross-compilation alone.
- Do not start tray, packaging, or updater work before the core restart loop is dependable.
