# Cross-Platform Testing

Status: required release contract

## Rule

Harbor supports macOS, Linux, and Windows only when the same product invariant is exercised on the real operating system in GitHub Actions. Release evidence names exact OS builds, CPU architecture, resolver/trust implementation, browser versions, container engine, and desktop runtime; labels such as “current supported” or `*-latest` are not release evidence.

Cross-compiling a binary, mocking a resolver, or running all tests on Linux does not establish platform support. DNS resolver integration, loopback identities, trust stores, low ports, process supervision, Docker Desktop, and installers are operating-system behavior and require OS-native jobs.

A required platform job must fail when its prerequisite is absent. It may not call `t.Skip`, turn a failed full-mode capability into translated-port mode, or mark the check green after testing only a mock.

Every required job emits a signed or workflow-attested capability manifest containing the tested commit SHA, assertion IDs, exact runner/image and dependency versions, user/elevation mode, skip count, cleanup result, and artifact digests. A final verifier fails the workflow when a required assertion or platform profile is absent, skipped, conditionalized away, marked `continue-on-error`, or reported for another commit.

## Phase -1: CI product environment

Before Phase 0 can claim a platform result, Harbor needs a versioned test fleet rather than labels pointing at unknown machines:

- infrastructure provisions a clean VM or supported dedicated host image, obtains a just-in-time one-job runner registration, and destroys or reprovisions the machine after the job; GitHub's `ephemeral` runner flag alone is not machine destruction;
- runner groups are restricted to this repository and named workflows, and privileged jobs execute only a maintainer-approved exact head SHA;
- workers expose no cloud metadata, internal network access, signing credentials, package-publishing credentials, or reusable GitHub token;
- images, Docker Desktop/Engine versions, browsers, resolver backend, trust store, Wails v2, Node, Vue/Vite, GTK/WebKit, tray dependencies, and interactive-session setup are versioned;
- capacity has a named maintainer, budget, provisioning timeout, and hosted control-plane preflight; a required product job is dispatched only after its exact JIT worker is provisioned and reserved;
- provisioning, job, cleanup, and destruction evidence is retained out of band and tied to the exact head SHA.

For a public repository, fork code never reaches these workers through `pull_request_target`. The privileged workflow definition and provisioning controller run from a protected default-branch ref; after policy validation, the product worker checks out only the approved head commit by SHA. If provisioning or reservation misses its deadline, the hosted controller publishes a required failed check against that exact SHA and cancels any stranded queued job. A job waiting for an unmatched self-hosted label cannot implement this guard itself because GitHub may leave it queued for hours.

## Runner strategy

All automation is orchestrated by GitHub Actions. Two worker classes are needed.

### GitHub-hosted workers

Use versioned Ubuntu, macOS, and Windows labels and record GitHub's attested runtime image version for:

- unit and property tests;
- protocol and golden fixtures;
- direct DNS server UDP/TCP tests on unprivileged ports;
- pure certificate generation and verification;
- HTTP/TLS router and TCP relay tests on high ports;
- state migration and reconciliation tests;
- CLI/daemon IPC and process supervision;
- Wails v2 desktop builds, `vue-tsc`, Vitest/Vue Test Utils, production Vite builds, and Playwright browser runs where the image provides the pinned toolchain;
- race-enabled Go tests;
- package construction and non-privileged smoke tests.

These jobs provide fast pull-request feedback and protect portable domain behavior.

Standard hosted images update in place and are not immutable pins. A capability that requires an immutable environment uses a custom-image larger runner or the dedicated product fleet; release evidence never invents a hosted image digest that GitHub did not provide.

### Current Phase 1 control harness

The implemented `phase1-control` job runs on `ubuntu-24.04`, `macos-14`, and `windows-2022`. It race-builds the production `harbor` and `harbord` entrypoints, then exercises them as separate processes against isolated standard per-user data and runtime paths.

The harness currently proves:

- the embedded migration command, unclaimed endpoint preflight, one foreground daemon, readiness, and negotiated control capabilities;
- concurrent CLI status, snapshot, and natural-identity registration replay while an authenticated desktop-role control connection remains open;
- a deliberate hard daemon termination, restart from durable state, matching CLI and desktop-role snapshots, and continuity of the persisted public CA fingerprint;
- graceful `harbor daemon stop` acknowledgement, foreground-process exit, retained-client disconnect, and joined runtime cleanup;
- restart recovery of a queued unregister operation before readiness, concurrent intent-keyed removal replay, ordered durable operation transitions, and an empty project registry at completion;
- cleanup of the IPC endpoint, reusable process lock, SQLite sidecars, and terminal CLI removal intents;
- a fixed allowlist of bounded, path- and secret-redacted summary and daemon-log artifacts.

This is a headless control-plane acceptance test. The desktop-role observer is a real authenticated control client, not a Wails window, and the project fixtures provide only `.goforj.yml` plus non-secret display metadata. The harness does not synthesize a GoForj managed worker, start Apps or containers, initialize project networking, mutate the system resolver or trust store, exercise interactive approval, or establish native desktop behavior. Those remain covered only when the later dedicated workflows described below are implemented.

Trusted pull requests also run the real production helper/platform API on GitHub-hosted workers to install and remove resolver, loopback, trust, and low-port state. Hosted Linux and macOS workers provide passwordless `sudo`; hosted Windows runs as Administrator with UAC disabled. This is valuable API and cleanup coverage, but it is not evidence for the shipping consent boundary, Windows filtered-token/UAC behavior, Docker Desktop, reboot, or an interactive desktop.

### Dedicated ephemeral integration workers

Some crucial tests need administrator access, a clean resolver, actual low ports, Docker Desktop, or reboot control. Provide ephemeral workers registered with GitHub Actions and labeled by operating system, for example:

```text
self-hosted, ephemeral, harbor-integration, linux
self-hosted, ephemeral, harbor-integration, macos
self-hosted, ephemeral, harbor-integration, windows
```

Each worker starts from a versioned clean image, accepts one job, uploads diagnostics, and is destroyed or returned to a cryptographically/immutably known clean image. The image includes the supported Docker engine/Desktop and no user project state.

Product Windows workers are supported Windows 11 client builds whose interactive local-administrator account runs Harbor under its filtered medium-integrity token, with UAC enabled and Docker Desktop. Elevation uses that account's linked high token, preserving its SID and `CurrentUser\Root`; a true non-admin plus different consenting administrator is a separate preview profile. GitHub-hosted Windows Server/Administrator/UAC-off jobs cannot substitute. Product macOS workers use supported physical hardware or a supported virtualization arrangement with a licensed, initialized Docker Desktop user session; nested Docker Desktop on ordinary hosted macOS is not assumed. Linux desktop workers provide the declared display server, GTK/WebKit runtime, notification service, and tray environment.

Consent tests are driven by an out-of-guest controller that can observe and interact with the genuine Windows secure desktop, macOS authorization UI, and Linux polkit/sudo UI. Administrator credentials and automation channels are held by the harness, never exposed to Harbor, the checked-out commit, process environment, logs, or test artifacts. The suite proves approval, cancellation, timeout, and no-UI `requires approval` behavior.

Using dedicated workers is not permission to move tests outside CI. Their GitHub checks are required for merge or release according to the tiers below.

Privileged jobs never execute unreviewed fork code on a persistent machine. External pull requests run the hosted suite; a maintainer-approved commit runs on an ephemeral privileged worker. `pull_request_target` must not check out and execute untrusted changes.

## Required workflows

| Check | Workers | Trigger | Required for |
|---|---|---|---|
| `core` | hosted Linux, macOS, Windows | every pull request | merge |
| `protocol-compat` | hosted Linux, macOS, Windows | every pull request | merge |
| `platform-smoke` | hosted Linux, macOS, Windows | every pull request | merge |
| `platform-network-hosted` | hosted Linux, macOS, Windows using production helper APIs | trusted pull request/head commit | merge |
| `platform-network-product` | ephemeral product Linux, macOS, Windows | trusted pull request/head commit | merge |
| `goforj-managed` | ephemeral integration Linux, macOS, Windows | trusted pull request/head commit | merge |
| `docker-projects` | ephemeral Engine/Desktop Linux, macOS, Windows | trusted pull request/head commit | merge |
| `installer-cleanup` | ephemeral integration Linux, macOS, Windows | release candidate | release |
| `reboot-recovery` | disposable OS test machines controlled by Actions | Phase 0, nightly, and release candidate | Phase 0 and release |
| `signed-update` | native release workers | release candidate | release |

If the project cannot supply a required worker for an operating system, that platform remains preview/unsupported. The hosted provisioning controller fails the required workflow when capacity is unavailable; the workflow is not changed to optional and is not allowed to sit queued until reviewers stop noticing it.

## Core matrix

The ordinary Go suite runs on all three operating systems with:

- unit tests for domain validation, address allocation, routing, reconciliation, state transitions, migrations, certificate policy, helper request validation, and diagnostics;
- race detector for daemon state, event fan-out, proxy routing, DNS snapshot swaps, log buffering, and shutdown;
- deterministic/fake-clock tests for retry budgets, expiry, readiness, reconnect, and operation recovery;
- fuzz/property tests for IPC frames, JSON schemas, DNS names, archive/update parsing, certificate persistence, path canonicalization, hosts/resolver edits, and state migrations;
- golden protocol requests, responses, snapshots, errors, and compatibility fixtures;
- a second migration/generation pass that produces no further diff.

Go commands use isolated caches in CI and discover every module, including the desktop nested module. Each relevant module runs its own test and vet/build passes because a root `./...` does not cross `go.mod` boundaries.

## DNS server tests

Every OS runs direct server tests before changing the system resolver:

1. start Harbor DNS on a loopback address and test port;
2. query over UDP and TCP;
3. resolve a shared HTTP domain to the ingress address;
4. resolve two native resource domains to different project identities;
5. return NXDOMAIN for an unregistered `.test` name;
6. refuse a name outside the Harbor zone;
7. return authoritative `NOERROR/NODATA` for AAAA in the IPv4-only release;
8. atomically swap record snapshots while concurrent queries run;
9. enforce request, connection, and shutdown bounds;
10. verify no packet is accepted from a non-loopback interface.

The hosted and product platform-network jobs then prove the real OS path:

1. install the platform resolver integration through the production `harbor-helper` request API using a unique CI installation ID and per-run DNS names;
2. resolve through the operating system API, not a direct query to the DNS port;
3. verify both positive and negative results;
4. verify an unrelated public name still uses the original upstream resolver;
5. simulate or trigger the supported network-change notification and recheck;
6. remove Harbor's integration;
7. remove or flush the platform DNS cache where supported, wait no longer than the test's bounded TTL, and prove in a fresh process that the unique `.test` name no longer resolves through Harbor;
8. compare-and-swap verify only the resolver namespaces and records Harbor touched, preserving concurrent foreign changes instead of diffing the whole machine.

Platform assertions include:

- macOS `/etc/resolver` ownership, custom port behavior, and cleanup;
- Linux systemd-resolved and NetworkManager adapters in separate image jobs when those standalone paths are claimed; the initial Ubuntu profile also exercises their combined desktop path, and no non-systemd fallback is implied unless it gains its own profile;
- Windows NRPT, dedicated loopback DNS port 53, rule ownership, and cleanup.

Coexistence fixtures install unrelated hosts entries, resolver rules, VPN/split-DNS policy, and a foreign rule claiming `.test`. Harbor must preserve unrelated policy and refuse or explicitly take over the conflicting owner. The suite exercises local-network/browser DNS policy in every browser profile that can block local endpoints independently of the OS resolver.

Hosts-file fallback has its own tests on all platforms: preserve BOM/newlines/modes where applicable, retain unrelated entries, replace only the owned marker block, recover an interrupted atomic write, and remove only the exact installation ID.

## Loopback and same-port tests

The crucial address test is identical on every OS:

1. record compare-and-swap evidence for the candidate address, route, listener, and Harbor ownership namespaces;
2. create three project loopback identities through the real helper/platform adapter;
3. bind `:3306` on all three addresses at the same time;
4. serve a distinct signed test payload from each listener;
5. resolve three project MySQL domains through the system resolver;
6. connect by name and native port and verify the matching payload;
7. attempt a duplicate listener and verify Harbor reports the owner instead of shifting ports;
8. restart `harbord` and verify all three leases and listeners reconcile;
9. release the identities;
10. prove Harbor's owned interface/route projections are gone and every touched foreign value still matches its guarded precondition.

The same test runs once with a secondary address inside one project to prove two same-port resources can coexist.

A capacity variant allocates the documented V1 minimum—three project identities plus one secondary identity for each—then proves the next allocation fails with exact required/available counts and no lease reuse. A two-user race starts setup concurrently under distinct UID/SID profiles and proves the machine-global ownership compare-and-swap allows one installation while the other receives immutable owner evidence without changing host state.

No translated-port result satisfies this check.

## HTTP and TLS tests

Every OS proves three projects through one live ingress:

1. create a fresh Harbor CA and three App upstreams;
2. register exact domains and issue leaf certificates;
3. install the CA into the intended user/system trust scope through the real platform adapter;
4. bind the real public ports 80 and 443 through the selected low-port mechanism;
5. use the OS resolver and native trust path to request all three HTTPS domains;
6. verify Host/SNI routes to different upstreams;
7. verify HTTP redirects to the correct HTTPS domain;
8. exercise HTTP/2, a streaming response, server-sent events, and a WebSocket upgrade;
9. reject unknown Host and unknown SNI;
10. rotate one expiring leaf without interrupting the other project;
11. rotate the CA through a dual-root period, proving old and new leaves work during transition and only the owned old root is removed after drain;
12. restart the daemon and reload the persisted cert/key pair;
13. remove the exact CA fingerprint and prove it is no longer trusted;
14. preserve an unrelated test CA and an identical root that pre-existed Harbor in the same store.

Certificate file tests separately verify first-open permissions, atomic persistence, key/cert matching, corrupt-pair recovery, rollback after a failed second-file write, and no private-key content in diagnostics.

Linux jobs test each supported distribution trust mechanism. Browser/sandbox trust that differs from the OS store is reported and tested in the supported browser packaging matrix rather than assumed.

The proxy's adversarial suite covers malformed and duplicate Host headers, absolute-form request targets, conflicting Content-Length/Transfer-Encoding framing, hop-by-hop header stripping, oversized headers and trailers, slowloris deadlines, TLS without SNI, unknown SNI, request cancellation, WebSocket close behavior, and SSE disconnects. A route that passes readiness is then deliberately released and claimed by a foreign process; Harbor must unpublish or degrade it before forwarding a request to the new owner.

## Low-port and conflict tests

Each platform integration image starts clean so ports 53, 80, and 443 are controlled test inputs.

The job proves:

- normal installation and unprivileged daemon use;
- daemon binary replacement/update does not lose the low-port mechanism;
- an unrelated process occupying 80 or 443 produces a precise diagnostic;
- Harbor never kills or reconfigures that process;
- helper repair is idempotent;
- uninstall removes only Harbor's service, capability, redirect, URL reservation, or socket rule;
- a non-Harbor listener remains untouched after cleanup.

Foreign-state fixtures cover macOS launchd service and socket definitions, Linux nftables/iptables rules plus systemd units/capabilities, and Windows firewall/HTTP.sys reservations. Harbor must add and remove only its namespaced rule or service and preserve state outside that namespace.

The daemon and desktop processes are asserted to run unelevated and outside a system service identity. The macOS relay must also run as the owning Harbor user; launchd may own and pass its fixed low sockets but must not leave Harbor code running as root. During ordinary setup/repair on macOS and Linux, only the one-shot helper receives elevation; the installer is exercised separately only for explicit product lifecycle transactions. On Windows the product worker proves a medium-integrity daemon can bind the selected low ports directly. No long-lived elevated networking broker or ambient network capability may appear.

## Privileged helper tests

Pure and native tests exercise the shipping helper boundary, not an equivalent shell script:

- helper and installer are bespoke entrypoints, and `go list -deps`/binary-symbol allowlists prove they do not include generated App bootstrap, project dotenv/compiled-env loading, generic RootCmd dispatch, Docker, desktop, or network clients;
- hostile project environment files and preboot command names have no effect on helper/installer startup;
- valid ticket admission is bound to platform admission evidence, installation ID, caller UID/SID, ownership generation, exact operation, nonce, and deadline;
- malformed, oversized, stale, replayed, already-consumed, wrong-peer, and wrong-generation tickets fail before mutation;
- one installation cannot repair or remove another installation's resolver rule, address, trust anchor, or low-port state;
- concurrent setup/repair/remove requests serialize through the machine ownership compare-and-swap;
- path and link targets are swapped between validation and use to prove descriptor-relative or OS-native operations resist symlink/reparse-point races;
- cancellation of macOS authorization, UAC, or Linux polkit/sudo consent returns `requires approval` without partial state;
- the helper accepts one allowlisted operation, emits bounded typed evidence over the authenticated return channel, has no network access, and exits.

Installer tests verify helper location, owner, mode/ACL, platform admission evidence (Apple/Authenticode signature or Ubuntu package signature plus installed digest), update replacement, downgrade rejection, and uninstall preservation of a mismatched helper or ownership record.

## Native TCP relay tests

Protocol-neutral tests run on all hosted OS workers:

- distinct project addresses with the same public port;
- private high-port upstreams;
- bidirectional streaming and half-close;
- long-lived connection idle policy;
- upstream connect failure and recovery;
- listener swap without routing a connection to the wrong project;
- connection limits and graceful drain;
- no payload content in logs or support bundles.

The platform-network jobs repeat the route through system DNS and real project identities.

## Docker and Compose tests

Dedicated workers run Docker Engine on Linux and Docker Desktop on macOS and Windows. The test uses generated GoForj fixtures, not a Harbor-specific hand-written Compose file alone.

Required scenario:

1. render three projects in isolated operating-system temporary directories, never inside the GoForj or Harbor checkout;
2. give each project an HTTP App, MySQL, Redis, Mailpit, Lighthouse/API Index, and the supported observability selection;
3. register and start all three through Harbor and managed `forj dev`;
4. verify each App through its trusted HTTPS domain;
5. connect to each MySQL at its own domain on `3306` and prove data isolation;
6. connect to each Redis at its own domain on `6379` and prove data isolation;
7. deliver SMTP to each project on native `1025` and open the matching Mailpit UI through HTTPS;
8. verify every Docker publication is `127.0.0.1:<private-port>`, never `0.0.0.0` or `::`;
9. verify containers use Compose service DNS internally and metrics reaches only the session's Docker-gateway relay while Apps remain bound to host loopback;
10. stop one project and prove the other two remain available;
11. restart its App without restarting or deleting its database volume;
12. stop and start Docker, then verify Harbor reconciles without changing public endpoints;
13. unregister one project and prove its checkout and volumes remain;
14. seed a fourth project under its existing directory-derived Compose identity, write distinct database data, register it, and prove Harbor adopts the same containers and named volumes without creating an empty replacement stack;
15. stop and restart the seeded project across the Compose publication/Harbor route barrier and prove its host migration succeeds through the native database domain;
16. prove Docker Engine/Desktop is at least the supported Engine 28-equivalent floor and a peer on the same L2 network cannot reach loopback-published private ports;
17. prove `harbord`'s Docker adapter uses only the allowlisted read-only list/inspect/events/logs surface, attributes containers through exact Compose project/service/working-directory labels and canonical checkout ownership, and rejects foreign or ambiguous containers without mutation;
18. verify the generated project worktrees remain clean, including externally mounted metrics targets and session Compose overrides;
19. clean up all test containers, networks, and volumes created by the test installation ID.

The largest supported generated composition is the fixture source. Generator/template changes are regenerated before this workflow and a second generation must be diff-free.

Image versions are pinned by the GoForj fixture. Updating them is a separate reviewed change with data-migration coverage where applicable.

Adapter-focused tests record every Engine request and fail on any create, start, stop, restart, remove, exec, attach, build, pull, network, volume, or generic pass-through route. They cover container replacement, replica aggregation, event reconnect, bounded log cursors, cancellation, unavailable Engine access, label/path mismatch, symlink/case normalization on supported platforms, and a checkout move. Native jobs prove the selected Unix socket or Docker Desktop endpoint behavior without exposing that endpoint to a generated App, helper, frontend binding, or test fixture process.

The product-proof verifier additionally requires a non-skipped event-refresh assertion and rejects Engine versions below the supported 28-equivalent floor before accepting uploaded lifecycle evidence.

## GoForj managed-session tests

On every OS:

- `forj project:describe --json` is deterministic, non-mutating, emits no environment values, and digests normalized non-secret topology rather than raw `.env` files;
- Harbor invokes the exact admitted executable for that descriptor before creating process authority, rejects unknown or malformed schema data, and persists only the validated normalized digest in the session;
- multiple requirements sharing one service key retain distinct non-secret endpoint IDs, affinity, and consumer mappings;
- default, named, selected, and build-only Apps map to distinct available/active states and public domains; an undeclared custom runtime/watcher blocks full mode;
- CLI capabilities and checked-in generated-project capabilities negotiate independently; an older generated App is read-only/upgrade-required before lifecycle work;
- terminal attach authenticates the peer UID/SID, canonical root, descriptor digest, nonce, and active-session exclusivity before receiving a scoped credential;
- ordinary daemon absence and an unregistered project preserve standalone behavior, a registered-project rejection fails before lifecycle, and explicit no-Harbor mode never contacts the daemon;
- handshake and a plan for every listener occur before phased lifecycle tasks;
- GoForj preserves build-before-setup ordering, runs `pre-compose`, starts typed Compose, reports completion and the accepted identity, waits for Harbor to observe the actual publications and acknowledge routes, then runs `post-compose`, host migration, and `post-migrate` in order;
- framework-owned legacy tasks migrate to stable phased IDs, while ambiguous unphased custom `dev.pre` tasks fail managed admission instead of being guessed from names or shell text;
- framework-owned raw `dev.down` migrates to typed Compose-down, retains the startup implementation/identity/profiles/override, and rejects ambiguous unphased custom down tasks;
- managed tasks leave no detached descendants/listeners, and a typed custom endpoint fixture proves assignment, enforcement, observation, and process-group ownership before custom processes can enter full mode;
- `.env`, environment-specific files, and `.env.host` cannot override the final managed endpoint overlay;
- each generated App captures its inherited trusted overlay handle/reference before its environment load, and project configuration cannot replace it;
- environment reload reapplies that overlay;
- private Compose publication assignments and App connection assignments are separate; `REDIS_PUBLISH_PORT` never changes the App's native `REDIS_PORT`;
- Lighthouse's private agent WebSocket transport and public HTTPS resource URL both resolve correctly;
- combined App metrics/Lighthouse remain routes on its single HTTP listener, standalone command metrics are allocated only when active, and SPAs remain build nodes;
- dynamic scrape targets and Compose overrides live outside the checkout, and the controlled Docker-gateway relay works while no App/metrics listener binds a LAN address;
- `.goforj.yml` has an explicit watcher and a change produces a reviewed topology refresh rather than an automatic render;
- terminal-owned and Harbor-owned sessions reconnect safely;
- typed App/watcher restart does not invoke project down behavior;
- managed stop targets the seeded adopted Compose identity, runs phased down behavior, leaves no managed container accidentally running, and preserves every named volume;
- ordered events recover through snapshot plus sequence after a forced disconnect;
- build success, process start, runtime probe, public endpoint readiness, and watcher coverage remain separate facts;
- App/watcher log backpressure produces an explicit gap event and terminal PTY output remains labeled `pty/combined` while managed pipes retain stdout/stderr; container log ordering, gaps, and replacement are proved by the Docker adapter tests instead of requiring a generated App capability;
- the API Index artifact, generated examples, exposed API Reference/Swagger resource, Lighthouse, resource URLs, and readiness paths remain present and correct;
- process shutdown settles a dedicated Unix session across watcher-created process groups and uses Job Objects on Windows without signaling a recycled PID; explicit macOS stale-runtime confirmation also proves one bounded exact-scope SIGKILL escalation when a root ignores graceful shutdown, while uncertainty quarantines only the affected project and retains its evidence.

Legacy and unattributed-runtime tests prove every inspect/confirm safety fence: caller and plan binding, expiry, durable revision drift, PID reuse, birth/executable/arguments/working-directory/socket/parent/scope drift, multiple owners, cross-user candidates, unreadable facts, respawn, and cancellation. Every rejected branch must prove zero signals. A retained legacy session mutates durable ownership only after exact scope and socket absence; an already-retired listener is labeled unattributed, while Start may automatically confirm it only when an exact project lease and one same-user process scope owns that exact leased socket. Explicit confirmation still mutates no Harbor session.

The Windows job is required to catch Bash, signal, PTY, and Compose assumptions. A cross-compiled binary without a live managed session is insufficient.

## IPC and desktop tests

IPC platform tests verify:

- Unix runtime directory and socket modes plus peer UID checks;
- Windows named-pipe ACL limited to the expected SID;
- one daemon lock per user;
- Hello/Welcome version negotiation;
- oversized, partial, pipelined, malformed, cancelled, and idle frames;
- bounded connections and request queues;
- a session credential cannot control another project or machine setup;
- desktop close/crash/relaunch does not stop the daemon or project;
- default close hides the window, the one-time first-close explanation is accessible, native-menu `Cmd/Ctrl+Q` exits only the UI, and relaunch focuses the hidden single instance even when no tray is available;
- desktop single-instance data is treated as untrusted and cannot invoke an arbitrary daemon action;
- project/API Reference/Lighthouse URLs open in an instrumented system browser and never navigate the bridge-enabled WebView;
- unexpected main-frame origins, raw-message origins, and frontend-supplied resource URLs are rejected;
- no raw socket, credential, shell, Docker operation, or unrestricted path reaches the frontend bridge.

Frontend tests run the same view model against the mock and Wails-backed bridge contracts. Vitest and Vue Test Utils exercise Pinia snapshot/event ordering, the exact four-destination rail, grouped dense list, detail pane, resource command palette, System/Settings compact overflow, shadcn keyboard/focus behavior, accessible state labels, operation progress, and parity with daemon snapshots and CLI actions. Playwright covers light/dark themes and the 56px rail, contextual pane, detail pane, and three-to-two-to-one-pane transitions against deterministic fixtures.

Large-stream tests prove log virtualization preserves ordering, source identity, gap markers, follow/pause behavior, keyboard access, and screen-reader context without rendering the entire retained history.

End-to-end desktop smoke runs inside real interactive sessions: a per-user LaunchAgent/login session on macOS hardware, an interactive Windows local-administrator account running Harbor at medium integrity with UAC, and the pinned Ubuntu desktop display server with the declared GTK/WebKit and tray/notification implementation. It covers first-run consent handoff, window/tray behavior, system-browser opening, notification permission denial/acceptance, and UI restart. Headless service sessions, Windows Server, Xvfb-only rendering, or a successful Wails compile do not substitute for this product check.

## Install, uninstall, reboot, and update

Release candidates run from clean OS images:

1. install the signed `N-1` package and record its committed sequence/bundle digest;
2. complete setup and register a fixture;
3. record compare-and-swap evidence for all Harbor-owned and directly neighboring host namespaces;
4. restart the UI and daemon independently;
5. reboot the disposable guest and verify daemon, resolver, loopback identities, CA trust, endpoints, and stopped/running policy;
6. drive a real sleep/wake cycle and a real supported network-interface change through the out-of-guest harness on every full product profile;
7. update from `N-1` to the candidate through the one-shot signed installer;
8. verify channel binding, release sequence, whole-bundle digest, and component-version compatibility;
9. reject a replayed manifest, downgrade, revoked/expired signing key, wrong channel, and mix of otherwise valid components from two releases;
10. verify state migration and project continuity;
11. inject a failed post-update health check and verify the transaction-scoped capsule restores the exact `N-1` bundle and pre-migration schema snapshot without lowering the committed high-water mark;
12. uninstall and prove only the exact Harbor installation's state is removed.

Reboot tests use disposable guests controlled by a GitHub Actions worker so the workflow can survive the guest restart. The full reboot path is first proved during Phase 0, then remains nightly and release-required. A manual “tested on my machine” note is not a phase or release gate.

Release provenance proves each platform artifact was built once, promoted by digest, and signed in an isolated job using short-lived identity. Tests cover signing-key overlap/rotation and an emergency revocation fixture without exposing production signing material to product integration workers.

Installer boundary tests reject arbitrary destinations, mutable/symlink-swapped staging roots, unsigned service assets, wrong installation IDs, concurrent transactions, and bundles whose individually valid components do not match the whole-bundle manifest. Windows tests keep the old daemon/UI binaries open until the installer proves it can stop them and switch versioned launch targets without attempting to overwrite its running executable. Crash injection covers every switch, schema, high-water-mark, and rollback-capsule boundary.

## Cleanup discipline

Every system test has three cleanup paths:

- normal test cleanup registered before the first mutation;
- a workflow post-step invoking ownership-based cleanup for the unique installation ID when the runner is still reachable;
- controller-side finalization that collects external evidence and destroys/reimages the guest when the runner dies, disconnects, is cancelled, or never returns after reboot.

The job records compare-and-swap preconditions for the exact resolver entries, address aliases, trust fingerprints, firewall/low-port namespaces, services, containers, networks, and runtime paths Harbor will touch. Cleanup fails if one of those owned projections remains or if Harbor overwrote a concurrently changed foreign value. It does not broadly byte-compare the whole machine, which would be both flaky and unsafe on shared operating systems.

Infrastructure destruction makes the fleet safe; it does not turn a missing product cleanup assertion green. The evidence manifest reports `product_cleanup` and `infrastructure_finalization` separately, and the release verifier requires both.

On failure, upload:

- Harbor operation journal and redacted daemon logs;
- interface, route, resolver, listener, trust, and service observations;
- Docker/Compose state and Harbor ownership labels;
- test certificate fingerprints, never private keys;
- exact OS image and dependency versions.

Artifacts must not contain project environment values, credentials, tokens, or CA private keys.

## Release support matrix

The initial full-mode product profiles are deliberately narrow enough to prove:

| Profile | Initial target family | Required product environment |
|---|---|---|
| macOS | macOS 15 on Apple silicon | `/etc/resolver`, login-keychain trust, launchd socket activation and unprivileged relay lifecycle, system WebKit, Safari plus pinned Chrome/Firefox, Docker Desktop, interactive login session. |
| Linux | Ubuntu 24.04 LTS on x86-64 | NetworkManager with systemd-resolved, nftables, system CA integration, GNOME Wayland, GTK3 and WebKit2GTK 4.1 with Wails v2's `webkit2_41` build tag, pinned Chrome/Firefox, rootful Docker Engine 28+, systemd user service. |
| Windows | Windows 11 24H2 on x86-64 | NRPT, `CurrentUser\Root`, WebView2, pinned Edge/Chrome/Firefox, Docker Desktop with WSL2, interactive local-administrator account running Harbor at medium integrity with UAC. |

Intel macOS, Linux ARM64, and other distributions, desktops, resolver stacks, or Wails runtime combinations remain preview until equivalent dedicated evidence exists. This is an architecture/support statement, not a cross-compilation limitation.

Each release replaces each family label with the exact tested OS point release/build, architecture, GitHub-hosted runtime image version or custom image digest, resolver and firewall backend, trust scope, browser/runtime versions, Wails commit and GTK/WebKit/WebView version, Docker version, and package format. A moving family label alone never turns a cell green.

Each exact profile records these mechanisms:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Per-user daemon lifecycle | required | required | required |
| Owner-only IPC | required | required | required |
| Loopback project identities | required | required | required |
| Same native port on three projects | required | required | required |
| System `.test` resolution | required | required | required |
| Trusted HTTPS and CA removal | required | required | required |
| Ports 80/443 and conflict behavior | required | required | required |
| Docker project composition | required | required | required |
| Managed GoForj session | required | required | required |
| Installer, reboot, uninstall | required | required | required |
| Signed update and rollback | required | required | required |

The published support table is generated from successful release-workflow evidence. Documentation cannot claim a green platform cell that the release commit did not exercise.
