# Development Handoff

Status: direct Docker service observation and logs committed; live topology and native host proof remain

Last updated: 2026-07-20

## Read this first

Harbor is working far enough to register a real GoForj checkout, initialize macOS networking, assign a project-specific loopback, launch `forj dev`, detect App readiness, observe conventional Compose services, and stream current development and selected service output into the Wails desktop. It is not close to release-complete.

The work immediately before this handoff hardened the most painful current failure: `harbord` could disappear while a watcher-created child continued listening on the project's address. The old supervisor owned only the outer process group, so the surviving App looked like a foreign port conflict on restart. New Unix launches use a dedicated session, recovery settles all exact members across watcher-created process groups, and unresolved scope quarantines only the affected project instead of preventing daemon startup.

The regression is intentionally end to end: an outer process and daemon generation are killed, a listener in a separate process group ignores graceful shutdown, replacement recovery removes the complete owned scope, and a second Start reaches Ready on the same address and port.

Retained missing-receipt state now has a separate explicit recovery path. A quarantined project can inspect one bounded macOS candidate, show only reviewed display facts, and ask the user to confirm before the daemon revalidates and signals anything. The plan is caller-bound, short-lived, one-use, and consumed before confirmation. Linux and Windows preserve the same contract but currently return unsupported. The native Darwin implementation has portable and cross-build evidence, not a completed real-host proof.

Commit `b531d7f` replaces the GoForj service-state/log dependency with a daemon-owned, read-only Docker Engine vertical. `harbord` lists local containers, re-inspects candidates, and admits them only when the Compose project, service, and working-directory labels resolve to the registered canonical checkout. A selected service's current-session log stream now crosses authenticated control, Wails, and the typed Vue bridge. No GoForj log capability is involved, and neither the generated App nor Vue can access Docker. Docker events, continuously refreshed topology, and publication-derived routing have not been implemented.

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
- Harbor never parses Compose YAML or `forj dev` prose. GoForj owns Compose intent and every mutation. Only `harbord` receives the read-only local Docker Engine boundary. The current adapter uses list, inspect, and log-stream calls attributed through exact Compose project/service/working-directory labels and canonical checkout ownership; generated Apps and frontend clients never inspect Docker. Engine events, live topology replacement, and publication routing remain the next container-layer capabilities, not current behavior.
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

Do not delete SQLite state, wipe the database, or make Harbor kill whatever happens to own port 3000. New sessions launched after the process-scope commit retain enough evidence for automatic recovery. A retained pre-receipt quarantine can use the explicit legacy repair workflow below. This already-retired remnant cannot: it needs a separate, explicitly unattributed listener-inspection action or manual user action.

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

No push was part of the stopping sequence. Confirm the live relationship rather than relying on a recorded ahead/behind count after more work:

```sh
git status -sb
git rev-list --left-right --count origin/main...HEAD
git log --oneline origin/main..HEAD
```

The resolver source is committed in the two foundation checkpoints above. The unexplained local artifact `desktop/package-lock.json` was deliberately excluded because its ownership or intent was not established; `.tmp/` is also runtime scratch and must remain untracked if it reappears. Live `git status` remains authoritative. Preserve those paths, avoid `git add -A`, stage explicit files, and inspect the cached diff before every commit.

The direct Docker service/log vertical spans `internal/containerruntime`, project supervision and reconciliation, authenticated control, the Wails bridge and generated fixture, and the Vue service-log panel. Continue excluding the unexplained top-level `desktop/package-lock.json` unless its ownership is established.

## Resolver follow-up review

The Linux `systemd-resolved` integration is committed and passed its focused unit, vet, helper, wire, race, and compile checks. It is not release-ready.

Blocking findings:

1. Every observation rejects a retained transaction artifact, but no recovery routine reconciles those artifacts. A crash after stage, exchange, quarantine, or cleanup can permanently disable Observe, Ensure, and Release until manual root cleanup.
2. The mutation `flock` is blocking and does not honor context cancellation or deadlines.
3. The CI reset assumes its early-sorting drop-in produces an empty global resolver even though later drop-ins or imported foreign DNS can repopulate runtime state; the workflow must verify the prerequisite explicitly.
4. Privileged CI proves the happy lifecycle only, not crash-after-stage, exchange, quarantine, restart, or cleanup recovery.

The implementation is also much larger than expected for one fixed `systemd-resolved` drop-in. Simplify where possible while retaining descriptor-bound, no-follow, exact-correlation, rollback, and command-allowlist protections.

The Windows NRPT core is also a committed foundation. Its portable focused/full package tests, vet, and Windows cross-compile pass, but it remains incomplete:

- it is not wired into helper or daemon providers, so Windows still selects the unsupported implementation;
- its native runner now derives the fixed Windows PowerShell executable from the native system-directory API rather than a caller-controlled `PATH`;
- native exactness rejects latent NRPT fields such as IPsec CA restrictions, DirectAccess settings, and DNSSEC encryption. Before any Set operation, a second raw snapshot rejects those fields with zero mutation until a native clearing contract and proof exist;
- helper dependency admission, native add/observe/set/release/CAS/cleanup tests, and Go/PowerShell fingerprint-parity evidence are absent.

Do not claim either platform's resolver as complete merely because its foundation is committed or cross-compiles.

## Current implementation status

No delivery phase has met its complete exit gate.

- Hosted three-OS CI, Phase 1 evidence, and privileged loopback tests exist.
- Durable SQLite state, authenticated local RPC, operation journals, project registration/removal, network setup approvals, and recovery are substantial.
- DNS, HTTP ingress, TCP relay, local CA/certificate primitives, loopback identity, Darwin resolver ownership, Linux resolver integration foundation, Windows NRPT core foundation, and runtime activation exist.
- Wails/Vue can add/remove projects, set up networking, start/stop a project, inspect and explicitly confirm one retained macOS runtime candidate, display actionable errors, stream current ANSI-formatted development output, and follow a selected Compose service's current-session logs through independent held cursors with incremental terminal redraws.
- GoForj now has a pure `forj project:describe --json` v1 starting contract for static project identity, available App inventory, conventional HTTP runtime defaults, and a non-secret topology digest. It does not read dotenv files, mutate the checkout, or execute generated code. Dotenv-derived service/resource intent and the managed-session handshake do not yet exist. Startup service state therefore still comes from `harbord`'s read-only local Docker list/inspect adapter, while the optional GoForj resource-only status query supplies framework links rather than container state or logs. Direct service logs are daemon-owned and do not depend on a GoForj log capability.
- Docker event consumption, continuously refreshed Compose topology, publication-derived routing, terminal-owned attachment, three-real-project acceptance, resolver parity, trust installation, low ports, tray, signed packaging, updates, and release evidence remain incomplete.
- Project Start/Stop exists in control, desktop, and first-class CLI surfaces; the CLI exposes `harbor start <project>` and `harbor stop <project>` with `--json` and an explicit retry intent.
- `harbor status <project>` provides the CLI's read-only view of one project from the authoritative daemon snapshot, with compact human output or the exact typed project object through `--json`.
- The pending resource-projection repair migration retains only the readiness-proven `app-http` resource and removes older optional runtime links that could make the daemon reject its complete snapshot. It is intentionally one-way: the links are derived and are rebuilt on a successful Start; it does not affect project source, volumes, secrets, or operations.
- Project-removal approval handoff is not implemented in the desktop.

[Current implementation state](./current-state.md) contains the fuller matrix and commands.

## Immediate next goal

Prove the committed direct Docker service/log vertical on the macOS development host before opening another product front. Exercise one real multi-container GoForj checkout through service discovery, log output, container recreation, project Stop, and project restart. The proof must show that similarly named neighboring Compose projects are excluded and that Harbor never performs an Engine mutation. Portable tests, both Go modules, frontend typechecking/unit/build, generator parity, and Chromium/Firefox browser suites are green; WebKit still needs an environment with its native runtime dependencies.

After that vertical is stable, add daemon-owned Docker event consumption and live replacement of service topology so container starts, stops, health changes, and recreation appear without restarting the project. Route only explicitly admitted publication facts through Harbor's existing resource/routing policy; observing a port is not itself permission to publish it.

The retained-session repair still needs native execution on a real macOS host: reproduce Start, abrupt daemon loss, explicit inspection, cancellation, confirmation, complete settlement, and a second Start on the same endpoint. The historical already-retired listener remains unattributed and is not covered by this mutation. Project-removal approval, trusted routing, and tray presence remain later bounded desktop slices. Linux resolver crash recovery is now implemented: its cancelable lock and exact owned stage/quarantine recovery have focused tests, and the root-only lifecycle and crash-recovery test is required by Linux CI. That workflow evidence is still required before claiming native resolver support.

## Next-session start checklist

1. Read `AGENTS.md`, [Current implementation state](./current-state.md), and this handoff before changing code.
2. Run `git status -sb`, inspect `origin/main...HEAD`, and preserve unexplained local artifacts rather than sweeping them into a commit.
3. Re-observe the macOS host and Ditracker runtime; paths, PIDs, listeners, leases, and database rows in this document are historical evidence until confirmed.
4. Reproduce a normal Start, abrupt daemon restart, automatic exact-scope recovery, explicit retained-session repair, Stop, and second Start before claiming native recovery complete.
5. Prove and commit the direct Docker service/log slice before adding Engine events, live topology replacement, or publication routing.
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

The desktop replaces the `project.recovery.ambiguous_launch` dead end with an inspect action and explicit confirmation dialog. Its warning is:

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

The retained-runtime slice passed full root and desktop Go tests and vet, focused race tests across `projectprocess`, `reconcile`, `control`, and `authority`, all frontend unit tests, production frontend build, and generator parity. Chromium and Firefox browser suites pass after the Wails fixtures were extended with the new binding surface. WebKit cannot launch in the current Linux workspace because its GTK/GStreamer host libraries are absent. Darwin ARM64 and AMD64 cgo-free project-process test binaries link successfully; that is build evidence only, not native libproc/signaling proof. The required macOS job runs the exact graceful lifecycle, a non-dedicated-session ambiguity case that must leave its listener alive, and a replacement-listener drift case that must receive no signal.

The Linux resolver checkpoint also passed isolated root-module tests and vet, focused resolver/helper/wire tests, and source formatting. Its root-only CI command covers normal lifecycle, owned crash recovery, and preservation of a foreign quarantine artifact; workflow evidence is still required. The Windows checkpoint passed focused and full resolver package tests, vet, and Windows AMD64/ARM64 cross-compiles. Its required elevated CI command creates, verifies, and removes one fresh local NRPT rule; workflow evidence is still required before this becomes native support evidence.

## Things not to do

- Do not infer process ownership from a busy port.
- Do not clear a session row merely to make Start clickable.
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
