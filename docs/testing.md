# Cross-Platform Testing

Status: required release contract

## Rule

Harbor supports macOS, Linux, and Windows only when the same product invariant is exercised on the real operating system in GitHub Actions.

Cross-compiling a binary, mocking a resolver, or running all tests on Linux does not establish platform support. DNS resolver integration, loopback identities, trust stores, low ports, process supervision, Docker Desktop, and installers are operating-system behavior and require OS-native jobs.

A required platform job must fail when its prerequisite is absent. It may not call `t.Skip`, turn a failed full-mode capability into translated-port mode, or mark the check green after testing only a mock.

## Runner strategy

All automation is orchestrated by GitHub Actions. Two worker classes are needed.

### GitHub-hosted workers

Use the current supported Ubuntu, macOS, and Windows images for:

- unit and property tests;
- protocol and golden fixtures;
- direct DNS server UDP/TCP tests on unprivileged ports;
- pure certificate generation and verification;
- HTTP/TLS router and TCP relay tests on high ports;
- state migration and reconciliation tests;
- CLI/daemon IPC and process supervision;
- Wails/frontend builds where the image provides its documented toolchain;
- race-enabled Go tests;
- package construction and non-privileged smoke tests.

These jobs provide fast pull-request feedback and protect portable domain behavior.

### Dedicated ephemeral integration workers

Some crucial tests need administrator access, a clean resolver, actual low ports, Docker Desktop, or reboot control. Provide ephemeral workers registered with GitHub Actions and labeled by operating system, for example:

```text
self-hosted, ephemeral, harbor-integration, linux
self-hosted, ephemeral, harbor-integration, macos
self-hosted, ephemeral, harbor-integration, windows
```

Each worker starts from a versioned clean image, accepts one job, uploads diagnostics, and is destroyed. The image includes the supported Docker engine/Desktop and no user project state.

Using dedicated workers is not permission to move tests outside CI. Their GitHub checks are required for merge or release according to the tiers below.

Privileged jobs never execute unreviewed fork code on a persistent machine. External pull requests run the hosted suite; a maintainer-approved commit runs on an ephemeral privileged worker. `pull_request_target` must not check out and execute untrusted changes.

## Required workflows

| Check | Workers | Trigger | Required for |
|---|---|---|---|
| `core` | hosted Linux, macOS, Windows | every pull request | merge |
| `protocol-compat` | hosted Linux, macOS, Windows | every pull request | merge |
| `platform-smoke` | hosted Linux, macOS, Windows | every pull request | merge |
| `platform-network` | ephemeral integration Linux, macOS, Windows | trusted pull request/head commit | merge |
| `goforj-managed` | ephemeral integration Linux, macOS, Windows | trusted pull request/head commit | merge |
| `docker-projects` | ephemeral Engine/Desktop Linux, macOS, Windows | trusted pull request/head commit | merge |
| `installer-cleanup` | ephemeral integration Linux, macOS, Windows | release candidate | release |
| `reboot-recovery` | disposable OS test machines controlled by Actions | nightly and release candidate | release |
| `signed-update` | native release workers | release candidate | release |

If the project cannot supply a required worker for an operating system, that platform remains preview/unsupported. The workflow is not changed to optional to make a release green.

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
7. publish no AAAA response in the IPv4-only release;
8. atomically swap record snapshots while concurrent queries run;
9. enforce request, connection, and shutdown bounds;
10. verify no packet is accepted from a non-loopback interface.

The privileged platform-network job then proves the real OS path:

1. install the platform resolver integration through `harbor-helper` using a unique CI installation ID;
2. resolve through the operating system API, not a direct query to the DNS port;
3. verify both positive and negative results;
4. verify an unrelated public name still uses the original upstream resolver;
5. simulate or trigger the supported network-change notification and recheck;
6. remove Harbor's integration;
7. prove the `.test` name no longer resolves through Harbor;
8. byte-compare or semantically compare all unrelated resolver state to its pre-test snapshot.

Platform assertions include:

- macOS `/etc/resolver` ownership, custom port behavior, and cleanup;
- Linux systemd-resolved and NetworkManager implementations on separate image jobs, plus the declared non-systemd fallback if supported;
- Windows NRPT, dedicated loopback DNS port 53, rule ownership, and cleanup.

Hosts-file fallback has its own tests on all platforms: preserve BOM/newlines/modes where applicable, retain unrelated entries, replace only the owned marker block, recover an interrupted atomic write, and remove only the exact installation ID.

## Loopback and same-port tests

The crucial address test is identical on every OS:

1. snapshot interfaces, routes, listeners, and Harbor ownership state;
2. create two project loopback identities through the real helper/platform adapter;
3. bind `:3306` on both addresses at the same time;
4. serve a distinct signed test payload from each listener;
5. resolve `mysql.alpha.test` and `mysql.beta.test` through the system resolver;
6. connect by name and native port and verify the matching payload;
7. attempt a duplicate listener and verify Harbor reports the owner instead of shifting ports;
8. restart `harbord` and verify both leases and listeners reconcile;
9. release the identities;
10. prove the original interface and route state is restored.

The same test runs once with a secondary address inside one project to prove two same-port resources can coexist.

No translated-port result satisfies this check.

## HTTP and TLS tests

Every OS proves two projects through one live ingress:

1. create a fresh Harbor CA and two App upstreams;
2. register exact domains and issue leaf certificates;
3. install the CA into the intended user/system trust scope through the real platform adapter;
4. bind the real public ports 80 and 443 through the selected low-port mechanism;
5. use the OS resolver and native trust path to request both HTTPS domains;
6. verify Host/SNI routes to different upstreams;
7. verify HTTP redirects to the correct HTTPS domain;
8. exercise HTTP/2, a streaming response, server-sent events, and a WebSocket upgrade;
9. reject unknown Host and unknown SNI;
10. rotate one expiring leaf without interrupting the other project;
11. restart the daemon and reload the persisted cert/key pair;
12. remove the exact CA fingerprint and prove it is no longer trusted;
13. preserve an unrelated test CA in the same store.

Certificate file tests separately verify first-open permissions, atomic persistence, key/cert matching, corrupt-pair recovery, rollback after a failed second-file write, and no private-key content in diagnostics.

Linux jobs test each supported distribution trust mechanism. Browser/sandbox trust that differs from the OS store is reported and tested in the supported browser packaging matrix rather than assumed.

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

The daemon and Wails process are asserted not to run as root, Administrator, or a system service identity.

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

1. render three projects in isolated temporary directories;
2. give each project an HTTP App, MySQL, Redis, Mailpit, Lighthouse/API Index, and the supported observability selection;
3. register and start all three through Harbor and managed `forj dev`;
4. verify each App through its trusted HTTPS domain;
5. connect to each MySQL at its own domain on `3306` and prove data isolation;
6. connect to each Redis at its own domain on `6379` and prove data isolation;
7. deliver SMTP to each project on native `1025` and open the matching Mailpit UI through HTTPS;
8. verify every Docker publication is `127.0.0.1:<private-port>`, never `0.0.0.0` or `::`;
9. verify containers use Compose service DNS internally and host callbacks use the supported host-gateway path;
10. stop one project and prove the other two remain available;
11. restart its App without restarting or deleting its database volume;
12. stop and start Docker, then verify Harbor reconciles without changing public endpoints;
13. unregister one project and prove its checkout and volumes remain;
14. clean up all test containers, networks, and volumes created by the test installation ID.

The largest supported generated composition is the fixture source. Generator/template changes are regenerated before this workflow and a second generation must be diff-free.

Image versions are pinned by the GoForj fixture. Updating them is a separate reviewed change with data-migration coverage where applicable.

## GoForj managed-session tests

On every OS:

- `forj project:describe --json` is deterministic, secret-free, and non-mutating;
- default and named Apps map to distinct IDs and public domains;
- handshake and runtime plan occur before project pre-tasks;
- `.env`, environment-specific files, and `.env.host` cannot override the final managed endpoint overlay;
- environment reload reapplies that overlay;
- standalone `forj dev` behaves normally with no daemon;
- explicit no-Harbor mode never contacts the daemon;
- terminal-owned and Harbor-owned sessions reconnect safely;
- typed App/watcher restart does not invoke project down behavior;
- ordered events recover through snapshot plus sequence after a forced disconnect;
- log backpressure produces an explicit gap event;
- the API Index, generated examples, Lighthouse, resource URLs, and readiness paths remain present and correct;
- process shutdown uses process groups on Unix and Job Objects on Windows without signaling a recycled PID.

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
- Wails single-instance data is treated as untrusted and cannot invoke an arbitrary daemon action;
- no raw socket, credential, shell, Docker operation, or unrestricted path reaches the frontend bridge.

Frontend component tests exercise the three-pane layout, keyboard navigation, accessible state labels, responsive collapse, operation progress, and parity with daemon snapshots. End-to-end desktop smoke runs on all three native workers.

## Install, uninstall, reboot, and update

Release candidates run from clean OS images:

1. install the signed package;
2. complete setup and register a fixture;
3. snapshot all owned and neighboring host state;
4. restart the UI and daemon independently;
5. reboot the disposable guest and verify daemon, resolver, loopback identities, CA trust, endpoints, and stopped/running policy;
6. simulate sleep/resume and a network-interface change where the platform harness supports it;
7. update to the candidate through the signed updater;
8. verify state migration and project continuity;
9. inject a failed post-update health check and verify rollback;
10. uninstall and prove only the exact Harbor installation's state is removed.

Reboot tests use disposable guests controlled by a GitHub Actions worker so the workflow can survive the guest restart. A manual “tested on my machine” note is not a release gate.

## Cleanup discipline

Every system test has two cleanup paths:

- normal test cleanup registered before the first mutation;
- an unconditional workflow post-step invoking ownership-based cleanup for the unique installation ID.

The job captures a pre-test snapshot and fails if unrelated resolver rules, interfaces, routes, trust anchors, listeners, services, hosts entries, containers, networks, or project files differ afterward.

On failure, upload:

- Harbor operation journal and redacted daemon logs;
- interface, route, resolver, listener, trust, and service observations;
- Docker/Compose state and Harbor ownership labels;
- test certificate fingerprints, never private keys;
- exact OS image and dependency versions.

Artifacts must not contain project environment values, credentials, tokens, or CA private keys.

## Release support matrix

Each release records the exact tested OS versions and mechanisms:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Per-user daemon lifecycle | required | required | required |
| Owner-only IPC | required | required | required |
| Loopback project identities | required | required | required |
| Same native port on two projects | required | required | required |
| System `.test` resolution | required | required | required |
| Trusted HTTPS and CA removal | required | required | required |
| Ports 80/443 and conflict behavior | required | required | required |
| Docker project composition | required | required | required |
| Managed GoForj session | required | required | required |
| Installer, reboot, uninstall | required | required | required |
| Signed update and rollback | required | required | required |

The published support table is generated from successful release-workflow evidence. Documentation cannot claim a green platform cell that the release commit did not exercise.
