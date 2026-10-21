# Current Implementation State

## Beta support guide and integration (2026-10-21)

[Beta support and recovery](./beta-support.md) now separates the public beta.1 downloads from newer source-only features and named native test profiles. It covers verified package/checksum names, setup and tool checks, wildcard conflicts, pending setup and legacy pool expansion, control-plane diagnostics, restart continuity, optional reviewed support reports, and owned removal/uninstall. It names the remaining consent, signing, Docker Desktop, additional desktop/OS, reboot, and signed-update gates rather than turning a hosted result into a wider support promise.

Current Wails references in the documentation index, architecture, test/plan surfaces, and tray design match the pinned v3.0.0-beta.32 source. Historical research remains historical. The CLI catalog and complete doctor checklist are explicitly target design; the user guide uses implemented commands and does not present a control-plane doctor result as host-health proof. The combined source checkout is `/tmp/harbor-support-diagnostics-20261020`; public beta.1 and the active developer installation are unchanged. Main integration and a fresh build-only package rehearsal are the current work, separate from the earlier accepted guide packages.

## Local support diagnostics preview (2026-10-21)

Project details and Settings -> Daemon now offer Support diagnostics. The local report explicitly selects build/protocol, snapshot freshness, networking configuration, project state, and the last five startup observations. It excludes raw snapshots, operations, ownership proofs, helper tickets, resource URLs, checkout paths, and environment files. Development output is off by default. When selected, it uses only the existing authenticated activity feed for the selected session, with an explicit current/retained/unavailable label, at most 40 lines and 6,000 UTF-8 bytes. The entire report is capped at 16 KiB.

The frozen preview is exactly what Copy report sends through the existing clipboard bridge. Live log appends do not silently change the reviewed text. Reconnect, stale snapshot, project/session replacement, output resets, failed activity reads, and output capability/availability changes clear the preview and require explicit refresh. Known checkout/home paths and common credentials are redacted before clipping; arbitrary private values or paths can remain in logs, so the preview asks users to review before sharing. No environment query, file reader, native API, daemon authority, automatic upload, or file-save integration was added.

This frontend increment passes 91 focused tests, the production build/typecheck, and two Chromium flows including exact preview/copy parity, daemon-only scope, actual light/dark colors, and a narrow layout. The source is in `/tmp/harbor-support-diagnostics-20261020` on `feat/support-diagnostics-20261020`, and the nine frontend files were applied with exact byte parity to the active `/Users/cmiles/code/harbor-wails-v3` checkout. Its prior dirty work, daemon, and running projects are preserved. Evidence logs and previews use `/tmp/harbor-support-diagnostics-*`. This is local development work, separate from the installed-onboarding packages and published beta.1 preview. Service-log export and file sharing remain outside this initial preview/copy scope.


## Installed onboarding accepted (2026-10-21)

The integrated desktop checkpoint passes full source CI, the network API check, both exact-source native package producers, and updated installed GUI acceptance on all three named hosted profiles. The packages were built from `2332dd887eb14719f316e0273757a267853ef080`. Source CI and later native-driver revisions match all 2,159 included product files; only admitted test/workflow/documentation paths differ. The feature branch is `feat/project-onboarding-20261020` in `/tmp/harbor-onboarding-integration-20261020`, based on current main `f82be98fe875b03e17271e9e3b5e26ba866ea72e`.

| Installed profile | Exact package producer | Full installed onboarding, recovery, owned cleanup, and uninstall |
| --- | --- | --- |
| macOS 26, Apple silicon | [39541535559](https://github.com/goforj/harbor/actions/runs/39541535559), build-only PKG | [39543757063](https://github.com/goforj/harbor/actions/runs/39543757063), artifact `12320529261` |
| Ubuntu 24.04 amd64, XFCE/X11 on Xvfb | [39541535559](https://github.com/goforj/harbor/actions/runs/39541535559), build-only DEB | [39551962656](https://github.com/goforj/harbor/actions/runs/39551962656), artifact `12324620697` |
| Windows Server 2025 amd64, restricted product token | [39541537684](https://github.com/goforj/harbor/actions/runs/39541537684), disposable-signed CI installer | [39542277969](https://github.com/goforj/harbor/actions/runs/39542277969), artifact `12320272474` |

Downloaded receipts record graphical creation, the first-project guide, real native prerequisite recheck, published App opening, system-trusted HTTPS 200, window close/reopen, normal UI Quit/relaunch, two actual daemon crashes with healthy-peer continuity and interrupted-peer retry, final graphical Stop, Harbor-owned cleanup/uninstall, and user-file preservation. Linux additionally records the exact URL read from the real Epiphany browser. Its driver uses native document selection for inline progress text omitted entirely by GTK/WebKit's accessibility tree; no JavaScript injection or fixture-only product replacement is used. The passing run supersedes the earlier driver failures.

[Source CI](https://github.com/goforj/harbor/actions/runs/39545121019) and the [network API check](https://github.com/goforj/harbor/actions/runs/39545120917) pass. Local combined evidence remains 229 focused frontend tests, a production build/typecheck, six Chromium flows, relevant root/nested Go tests, and stable Go-owned fixture regeneration. Follow-up Linux harness checks pass 34 tests. The source integration preserves current main's backend and intentional desktop module replacement; dependencies, minimum Go versions, persisted data, and project configuration are unchanged.

HARBOR-30's hosted onboarding scope is complete. These are named-profile preview receipts, not proof of genuine interactive consent, additional desktops, native Docker Desktop, reboot, signed updates, or production signing. macOS preserved the foreign administrator trust fixture after Harbor uninstall, but cleanup of that disposable foreign fixture timed out; shipping consent/trust remains a separate gate. No tag, public release, merge, or active installation update was performed. Public beta.1 and the user's running environments are unchanged. The newer local HARBOR-31 support-diagnostics frontend increment is separate from these tested package bytes.

## Local first-project guide (2026-10-20)

Overview now offers a four-step guide from the first fresh empty workspace: create/add a project, configure secure local networking, Start, and open the actual published App resource. Established workspaces keep the normal dashboard. Getting started in the Overview header and command menu can reopen the guide. The selected registration and dismissal/open-link intent stay in this UI session; project, networking, lifecycle, and resource facts come from the daemon snapshot. No persisted schema or project manifest was added.

Creation initiated while the guide is active returns to the guide with the successful registration selected. Networking uses the existing explicit setup/resume and old-networking recovery actions, and suppresses the duplicate Overview setup card. Start uses existing admission and the prerequisite repair panel, including explicit Try Start for differing tool environments. Project details remain the place for output and recovery. Workers without an App URL can continue to project details; the guide never invents a URL. Readiness, degraded state, pending networking, stale/disconnected snapshots, and missing setup capabilities have distinct guidance. Dismiss and Finish setup return to the normal workspace.

Validation passes 189 focused frontend tests and the production build. Five Chromium flows cover the complete empty-workspace guide with a missing-tool repair, published-resource opening, light/dark and narrow layouts, command-menu reopening, ordinary creation, CLI recovery, and stale wizard responses. These are fixture-based SPA checks. Existing native creation smoke now waits for the asynchronous prerequisite check to enable Create. This does not claim a new installed consent/GUI rehearsal. Both local product slices remain uncommitted and are absent from the published preview.

## Local provider-aware prerequisites (2026-10-20)

The native desktop now exposes read-only provider prerequisites before schema loading, at creation review, and before a new Start. The GoForj adapter checks canonical CLI identity and creation/descriptor contracts, Go, Node/npm for selected frontend starters, and Docker CLI/Compose v2/engine for selected container workloads. Reports distinguish ready, missing, incompatible, and unavailable, with installation guidance and Recheck. Runtime-only requirements are explicitly labeled and do not block creating a stopped project. The renderer needs Go and forj; Go toolchain selection remains intact.

Existing-project checks derive the checkout from a fresh authenticated snapshot, use GoForj's static service requirements, and inspect bounded generated conventional `frontend/package.json` or root package metadata plus `go.mod`. They do not parse `.goforj.yml` or run project hooks. Custom frontend locations and arbitrary developer hooks may require additional tools outside this initial convention-based check. Queries use fixed argv, bounded output, a neutral working directory, and timeouts. Windows npm is queried through Node's standard npm JS entrypoint rather than a batch shell. Nonstandard shim layouts can report unavailable and retain the ordinary Start path.

Checks describe the desktop environment, which can differ from the daemon's. A deliberate Try Start action uses ordinary daemon admission when an advisory check cannot prove readiness. Already-admitted or uncertain Start intents reconcile without a new preflight. Reconnects discard old observations and late results; creation similarly fences provider, option, dialog, and schema changes. Networking remains its own explicit setup/repair flow. No tool installation, privilege escalation, persisted schema, module dependency/version, or running project was changed.

Validation passes 176 focused frontend tests, seven Chromium creation/recovery flows, the production frontend build, focused nested Go tests and race/vet checks, and root GoForj/creation contract tests. The Go-owned fixture regenerates identically twice. A read-only installed-tool smoke observes GoForj v0.35.1 and Go 1.27.0 with both contracts available. The active development app binary contains the new native methods. Browser tests use fixtures; this is not new native Windows or installer evidence. Work remains local and uncommitted, separate from the published preview.

## Local log search and navigation (2026-10-20)

Project development output and service logs now share a log viewer with scoped Cmd/Ctrl+F, literal case-insensitive search, highlighted matches, next/previous controls, Enter/Shift+Enter navigation, and Escape to close. Search uses rendered terminal text, including phrases across ANSI styles, rather than raw escape sequences or erased progress output. Match selection survives appended output and recalculates when retained output is replaced. Switching projects, services, or runtime sessions clears the prior search.

Opening search or scrolling away pauses automatic scrolling while the transport continues receiving output. Pause scrolling, Resume following, and Jump to latest make tailing explicit. Existing plain-text Copy, service-local Clear, safe URL opening, ANSI colors, retention notices, and authenticated cursor handling remain in place. Search counts all retained matches; highlighting and navigation show the first 5,000 with an explicit limit label for broader queries. The controls respect independent app and terminal appearance settings. No source/severity inference or support-bundle export is added.

Validation passes 53 focused unit/view tests, the production frontend build including TypeScript checking, and three Chromium flows for search/live append/service switching, standalone service logs, and the existing failure/retry panel. Browser checks cover light/dark appearance, an independent dark terminal with a light app, keyboard focus, no matches, paused scroll position, clear, resume, and narrow layout. This is local uncommitted frontend work in `/Users/cmiles/code/harbor-wails-v3`, separate from the published preview and native installer evidence.

## Local failure details (2026-10-20)

Project detail now shows one failure panel above the primary tabs, with the failed or last interrupted startup step, existing port-conflict observations, recovery guidance, and an ordinary Start retry that preserves the existing connection, stale-snapshot, and lifecycle guards. Networking, missing-checkout, and ambiguous-runtime guidance keep their dedicated flows. Disclosure shows a bounded excerpt from the selected session's existing authenticated activity output, including retained output when available. Missing and truncated output are explicit; no arbitrary filesystem read or new daemon authority was added.

Copy failure details includes the same bounded excerpt after common credential patterns and home-directory names are redacted. The panel asks users to review before sharing; this is not the general support-bundle export from HARBOR-31. Raw development output retains its existing behavior. Validation passes 61 focused unit/view tests, the production frontend build, and a Chromium failure/retry flow covering light/dark contrast, narrow layout, retained/truncated output, and redaction. The work is local in `/Users/cmiles/code/harbor-wails-v3`, not part of the published preview.

## Public development preview (2026-10-20)

[Harbor v0.1.0-beta.1](https://github.com/goforj/harbor/releases/tag/v0.1.0-beta.1) is published from immutable tag source `1e71ce47e5aaa07eeeb0f615a822dd49ca46770b`. Exact-source [main CI](https://github.com/goforj/harbor/actions/runs/39519520985) and all five jobs in the [native package release](https://github.com/goforj/harbor/actions/runs/39520200187) passed. Public downloads contain the exact installer bytes that passed installation, three-project trusted HTTPS, desktop daemon activation/reopen, ownership-aware uninstall, and user-data preservation on macOS 26 Apple silicon and Ubuntu 24.04 amd64. The product source also matches the three-platform full installed GUI and transparent-restart acceptance recorded below.

The published PKG SHA-256 is `455bbd6721cd4e16cf36cf0d727e5ecda4274eea95decdaf2f51981a7d51e3e0`; the DEB SHA-256 is `b4f5ffad30cf6fd5696fa386a4611f7cc782c0fd0fe8c70b5a73ac3ed3039344`. Public downloads were fetched and compared byte-for-byte with the sealed runner artifacts. GitHub normalized the Debian asset's tilde to a period, so its checksum references and provenance filenames were corrected after publication without changing either installer or the tag. The release helper now normalizes that filename before sealing future publication sets, with a checksum-resolution regression check.

This is an unsigned development preview. Windows public downloads remain gated on production signing; its disposable CI installer was not published. The user's active installation was not changed. See [publishing previews](./releases.md) for the release record and remaining platform boundaries.

## Transparent daemon restart

[PR #18](https://github.com/goforj/harbor/pull/18) is merged into `main` at `dfaae107a9d1a73ba40bb30ae6653d6cf1221f12`, with a tree identical to tested product source `9700f89e398b025a1f525c882f9875e043d07273`. Ready and Degraded Harbor-owned sessions with a surviving authenticated broker can keep their existing processes, environment, session generation, stable private assignment, and logs across clean daemon shutdown or a crash. Reconnection does not rerun project preparation, GoForj, or Compose. Explicit project Stop still settles the entire owned scope. Interrupted starts remain safely retryable, and ambiguous ownership isolates only that project without signaling an unproved process. Public ingress can be briefly unavailable while the daemon reconnects.

Unix uses the existing dedicated native session. Windows retains a separate named kill-on-last-close job through a query-only broker handle and reopens it only after exact root birth and membership are proved. Broker manifests use protected owner/SYSTEM ACLs on Windows and retained no-follow owner-private reads on Unix. Output authentication remains separate from process-stop authority. The broker drains acknowledgements independently of outbound writes, avoiding a full-duplex pipe deadlock, and closes its journal before publishing normal EOF. Recovered watcher setup stays joined during shutdown; ordinary monitoring checks the exact leader cheaply rather than repeatedly scanning the whole host for every project.

The first packaged product source was `d2bacd0a59e200876962c64592843a718c8f955f`. Its full installed macOS [39510197399](https://github.com/goforj/harbor/actions/runs/39510197399) and Windows [39510197333](https://github.com/goforj/harbor/actions/runs/39510197333) checks passed both crashes, unchanged Ready sessions and generations, fresh HTTP logs, trusted HTTPS, graphical Stop, cleanup, uninstall, and retained user-file hashes. Linux [39510197342](https://github.com/goforj/harbor/actions/runs/39510197342) passed continuity but exposed a recovered service-observation checkout omission during shutdown. A later native Windows repetition also observed an empty accounting result while both exact project and broker processes remained alive. These findings were corrected and fresh corrected-source acceptance passed before merge.

Recovered commands now retain their canonical checkout and executable for service logs and resource observation. Read-only resource queries receive the admitted runtime network assignment without changing the surviving process environment or project files. The adoption monitor owns process-wrapper release and is joined at detachment, fixing a race detected when the regression test also queried real resources after each handoff. Windows job information now crosses a typed-pointer syscall boundary instead of losing Go stack-lifetime tracking in a uintptr wrapper. Compiler escape analysis verifies stable native buffers; native round-trip, invalid-handle, and repeated lifecycle tests remain mandatory.

Corrected-source [CI](https://github.com/goforj/harbor/actions/runs/39513643739) and [security analysis](https://github.com/goforj/harbor/actions/runs/39513638668) pass. [Windows repetition](https://github.com/goforj/harbor/actions/runs/39513522849) passes 100 complete handoff/Stop scenarios and repeated native-buffer and invalid-handle checks without a race report. Fresh beta.5 PKG/DEB producer [39513665783](https://github.com/goforj/harbor/actions/runs/39513665783) and disposable-signed Windows producer [39513668950](https://github.com/goforj/harbor/actions/runs/39513668950) pass; no public release was published.

Full installed GUI acceptance, cleanup, and uninstall pass on [macOS](https://github.com/goforj/harbor/actions/runs/39513643541), [Ubuntu XFCE/X11](https://github.com/goforj/harbor/actions/runs/39513643678), and [Windows Server 2025 with a restricted product token](https://github.com/goforj/harbor/actions/runs/39513643604), all at attempt 2. The downloaded receipts prove two actual daemon crashes, unchanged Ready-session generations, fresh HTTP log markers, trusted HTTPS, interrupted-peer retry, peer isolation, final graphical Stop, authoritative cleanup, uninstall, and three preserved user-file hashes. Exact artifact IDs and package digests are recorded in [the handoff](./handoff.md). Earlier producers and incomplete receipts are superseded.

This slice requires no GoForj CLI update, project configuration change, database migration, or minimum Go increase. Existing unsupported sessions keep their previous recovery path; a fresh Windows Start establishes the retained native job. GoForj's removed managed client is not reinstated. Reboot continuity, unattended service restart, genuine interactive consent, additional desktop profiles, native Docker Desktop integration, signed updates, and public Windows production signing remain separate release concerns. The preview publication above is a separate release action; the user's active installation was not changed.

A full local root run also exposed unmodified macOS canonical-temp/UTC fixture assumptions and a native host-conflict census failure. The path/UTC cases pass with canonical temp paths and UTC; the focused host-conflict census still fails locally. These results are not relabeled as feature acceptance. Hosted checks and exact installed receipts establish only their named profiles.

## Installed crash-recovery acceptance (2026-10-20, hosted profiles passed)

The complete installed GUI recovery scenario passes on macOS, Ubuntu, and Windows at product source `2f3ace995aafbced8cca3568e4a00a4a75686223`. [PR #17](https://github.com/goforj/harbor/pull/17) is merged into `main` at `cbbfd14b674e63a320d3fd0098e2849db044c1d6`; the merge tree matches the independently reviewed PR head. Subsequent documentation-only commits do not change that product source. The three existing platform workflows share `scripts/packageui/crashrecovery`; no new workflow was added.

| Platform | Exact package producer | Full installed GUI, recovery, cleanup, and uninstall |
| --- | --- | --- |
| macOS 26, Apple silicon | [39478906159](https://github.com/goforj/harbor/actions/runs/39478906159), beta.3 PKG, publication skipped | [39480629419](https://github.com/goforj/harbor/actions/runs/39480629419), artifact `12290529180` |
| Ubuntu 24.04, amd64, XFCE/X11 | [39478906159](https://github.com/goforj/harbor/actions/runs/39478906159), beta.3 DEB, publication skipped | [39480706330](https://github.com/goforj/harbor/actions/runs/39480706330), artifact `12292390153` |
| Windows Server 2025, amd64, restricted product token | [39478910510](https://github.com/goforj/harbor/actions/runs/39478910510), disposable-signed dev.29 installer | [39479964857](https://github.com/goforj/harbor/actions/runs/39479964857), artifact `12290218682`; restricted-profile artifact `12290517836` |

The PKG SHA-256 is `18a256fad3117c9c8b009896637241b8502d87424003dbacd13cff2c715489e9`; the DEB SHA-256 is `951107c86e78b2afc23199cead4bd270320864d185750b953c6530195063c53e`. Windows tested-package artifact `12291472007` and producer evidence `12291491865` bind its signed installation to the exact product source. The final Windows installed bundle ID is `80c8f0a635098eaf482749e89936b42c8f3e32661890494cb4cdde9fa4767df1`.

All three receipts prove two actual crashes of the authenticated installed daemon: one while both projects are Ready, another during a blocked peer startup. Recovery retires supervised sessions, withdraws routes, and fails the exact interrupted Start retryably with `project.recovery.process_absent`. The native Go initializer's proof listener must be absent before its gate is released. Explicit retries produce fresh sessions, retain distinct stable backend loopback assignments, restore system DNS, and return trusted HTTPS 200. Public websites intentionally share the ingress address. Ordinary peer Stop and removal preserve the recovered original session; peer source remains on disk. The actual graphical client reconnects and performs the final Stop. Authoritative unregister, networking release, daemon exit, uninstall, and three nonempty preserved user-file hashes pass on each platform.

The tests exposed two product defects. A fresh Windows Vue project completed dependency installation after 97,866 ms, then exceeded the former two-minute startup limit during compilation. The default now permits five minutes for complete startup while retaining per-probe bounds, explicit overrides, cancellation, and terminal cleanup. A stalled project can consequently remain Starting longer. A separate native macOS reproduction exposed a kernel exit race: the session census can become empty just before the exact leader is observable as a zombie. Harbor now re-observes only that mismatch within a 50 ms retry window, repeating complete process-birth, session-census, and leader-state checks. Persistent ambiguity and unreadable facts still fail closed, and incomplete observations never authorize a signal. Focused tests passed three repetitions including 90 native test-owned settlements, with independent review and race checks. The previous hosted quarantine's underlying native error was not retained, so its exact cause remains unproved; the fresh installed macOS run passes with the correction. The peer fixture now uses the existing UUID directory shape without broadening cleanup admission. No public API, configuration key, persisted-data format, or minimum Go version changes.

Final-source [core CI 39478820969](https://github.com/goforj/harbor/actions/runs/39478820969) passed 14 executed jobs, with its conditional Docker job skipped. [Platform network smoke 39478821152](https://github.com/goforj/harbor/actions/runs/39478821152) and [Windows native capacity 39478821340](https://github.com/goforj/harbor/actions/runs/39478821340) passed. A cancelled duplicate smoke run left its always-run verifier red; [its complete rerun 39478811512](https://github.com/goforj/harbor/actions/runs/39478811512) passes. Two unrelated local output-broker tests force a damaged module-cache path; hosted root tests pass with the final source.

These receipts establish the named hosted profiles. GoForj v0.35.1 does not perform authenticated managed attachment, so recovery safely settles projects and requires explicit fresh starts rather than adopting running projects without interruption. The harness explicitly starts the installed manager after a crash and does not establish unattended restart policy. Genuine interactive consent, supported Windows client/UAC behavior, GNOME Wayland, reboot recovery, native Docker Desktop integration, and signed updates remain separate proof. macOS preserves the exact foreign administrator certificate and trust through release/uninstall; removing the fixture's final foreign trust root again timed out, so full fixture trust-store restoration remains unproved. Public Windows installers remain gated on production signing. The reviewed work is merged; no Harbor tag or public release was created.

## Main integration (2026-10-20)

[PR #13](https://github.com/goforj/harbor/pull/13) is merged into `main` at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`, including PR #16. Supporting PRs #14 and #15 were closed as superseded after checking that their work was preserved. Their older Windows failures predate the passing installed Windows GUI run below. Fresh [main CI](https://github.com/goforj/harbor/actions/runs/39459846653), [native Windows three-project lifecycle](https://github.com/goforj/harbor/actions/runs/39459846523), and [native Linux three-project lifecycle](https://github.com/goforj/harbor/actions/runs/39459846560) pass. The merge also started [macOS lifecycle](https://github.com/goforj/harbor/actions/runs/39459846702) and [platform smoke](https://github.com/goforj/harbor/actions/runs/39459846564) checks. No Harbor tag or public release was created.

## Released GoForj baseline (2026-10-20)

GoForj [PR #253](https://github.com/goforj/goforj/pull/253) is merged and published as [v0.35.1](https://github.com/goforj/goforj/releases/tag/v0.35.1), at commit `09b41ca77b523c0724545ecc20b18581d9cae06c`. Harbor's generated-project fixtures, hosted test workflows, and package GUI CLI installs now select that release. This replaces the acceptance-only candidate pin. The published CLI is installed locally with canonical module metadata and no local replacement. Fresh Harbor validation passed against this release: [all 15 CI jobs](https://github.com/goforj/harbor/actions/runs/39427059491), [three-project Docker lifecycle and cleanup](https://github.com/goforj/harbor/actions/runs/39423903409), and [generated-project platform smoke on Windows, macOS, and Linux](https://github.com/goforj/harbor/actions/runs/39423865346). Package source is `76662ca`; subsequent changes are limited to the Windows GUI harness and documentation.


| Platform | Fresh package producer | Installed GUI lifecycle, cleanup, and uninstall |
| --- | --- | --- |
| macOS | [39424253927](https://github.com/goforj/harbor/actions/runs/39424253927), publication skipped | [39423865305](https://github.com/goforj/harbor/actions/runs/39423865305), attempt 2, artifact `12265538384` |
| Ubuntu | [39424253927](https://github.com/goforj/harbor/actions/runs/39424253927), publication skipped | [39423865351](https://github.com/goforj/harbor/actions/runs/39423865351), attempt 2, artifact `12265139712` |
| Windows | [39424257838](https://github.com/goforj/harbor/actions/runs/39424257838) | [39427473351](https://github.com/goforj/harbor/actions/runs/39427473351), artifact `12266847973` |

The exact PKG SHA-256 is `902073ed7565e79539e1bb4ddd0fc6a9715f27658504676367f07ca4488a1ed9`; the DEB SHA-256 is `0525df8cfe47319d4028bd02de43f76cc6a243c2d29a81ab3b9a774fe4bf8f1a`. All three GUI runs preserve three nonempty user-file hashes after uninstall. Windows normal cleanup proves stopped registration, authoritative unregister, network release, and daemon exit before uninstall. The new Windows run uses harness `006915c`; CI uses `9ab1526`. Both admit the exact unchanged package product source through the existing verifier.

Initial GUI attempts correctly rejected older installers after the CLI baseline update. Fresh packages resolved those source mismatches. A later Windows harness failure occurred after Start, HTTPS, native Quit, and reopen, while finding the created project before GUI Stop. The selector now explicitly indexes the native UI Automation collection and tolerates optional names while preserving ordinal matching and multiple-match rejection. The complete rerun passed. This was not GoForj database readiness: Go owns Docker execution, retries, and cancellation; the small SQL client scripts run inside Linux database containers rather than a Windows host shell. Native Windows/macOS Docker-provider acceptance remains distinct from the Linux Docker proof.

The following installed-package entries retain their earlier source and CLI selection.

## Installed platform evidence (2026-10-20)

The earlier accepted product source is `a537b52`. Full CI passed all 15 jobs in [39409654639](https://github.com/goforj/harbor/actions/runs/39409654639). The exact-source Unix package producer [39410128497](https://github.com/goforj/harbor/actions/runs/39410128497) passed with publication skipped. Its PKG SHA-256 is `7c630ee73250873ca5e325c4d4cb6e1a36321aa764a2102c1f4444de64228bf6`; its DEB SHA-256 is `d6547610ec9479ab238316c120b724058f353de4ccc2480221c7d5afd81fb275`. All three exact-package GUI lifecycle and normal cleanup receipts are accepted at this product source.

| Platform | Accepted package and GUI evidence | Boundary |
| --- | --- | --- |
| macOS | GUI [39409562969](https://github.com/goforj/harbor/actions/runs/39409562969), attempt 2, artifact `12260056137`, using the exact PKG from producer [39410128497](https://github.com/goforj/harbor/actions/runs/39410128497) | Full GUI lifecycle, cleanup, uninstall, and three nonempty data hashes passed; failure recovery was skipped. Foreign administrator trust remained preserved. A separate attempt to remove the fixture's final trust root timed out at 30 seconds, so genuine consent and full trust-store restoration remain unproved. |
| Ubuntu | GUI [39409563489](https://github.com/goforj/harbor/actions/runs/39409563489), attempt 2, artifact `12260565110`, using the exact DEB from producer [39410128497](https://github.com/goforj/harbor/actions/runs/39410128497) | Full GUI lifecycle, cleanup, uninstall, and three nonempty data hashes passed; failure recovery was skipped. This is XFCE/X11 on Xvfb, not GNOME Wayland or genuine PolicyKit consent; pinned GTK does not support Ctrl+Q. |
| Windows | Exact-source package producer [39409593732](https://github.com/goforj/harbor/actions/runs/39409593732) and full GUI [39412874662](https://github.com/goforj/harbor/actions/runs/39412874662) passed; GUI receipts include normal cleanup and three nonempty data hashes | Three same-package GUI runs passed full lifecycle, trusted HTTPS, native Quit, Stop, unregister, networking release, uninstall, and three nonempty data hashes: [39412874662](https://github.com/goforj/harbor/actions/runs/39412874662), [39414837174](https://github.com/goforj/harbor/actions/runs/39414837174), and [39414837212](https://github.com/goforj/harbor/actions/runs/39414837212). Readiness took about 102.85, 103.23, and 109.30 seconds. The earlier roughly 128-second timeout did not recur; its exact cause remains unknown, and no npm logs met the attribution rules for retention. No product timing, configuration, or cache warmup was changed. The preauthorized hosted Server 2025 fixture does not prove genuine GUI consent or supported Windows client/UAC behavior; public signing remains gated. |

The source includes three product corrections: machine Root-store snapshots open read-only while helper mutation and CurrentUser access stay unchanged; Wails' native application menu is enabled only on Windows; tray status mutations are serialized on the native main queue without destructive `Menu.Update`. Restricted-token observation passed in [39401125154](https://github.com/goforj/harbor/actions/runs/39401125154), the focused Windows native Quit probe passed in [39407787846](https://github.com/goforj/harbor/actions/runs/39407787846), and the tray fix's overlap/shutdown regression tests passed. Windows evidence includes actual product-token facts and protected machine/IPC probes. The extra cross-high observer belongs only to the standalone profile; the product receipt records it as skipped and makes no containment claim.

The earlier Docker acceptance lifecycle passed all three MySQL projects and cleanup in [run 39396134671](https://github.com/goforj/harbor/actions/runs/39396134671), using candidate `7e38e5f041cc2f449f5ec33baaaa2139ac4b6b50` from GoForj PR #253. The released v0.35.1 implementation additionally passed twelve pinned native MySQL/MariaDB version fixtures in [GoForj CI](https://github.com/goforj/goforj/actions/runs/39421351506). Fresh Harbor evidence for the released CLI is tracked above. This test-baseline update changes no Harbor API, generated configuration, persisted data, or minimum Go version.

These receipts establish hosted functional lifecycles, not release readiness. Genuine interactive consent, platform-specific profiles beyond those named above, recovery, signing, and update proof remain separate gates. Integration was separately authorized and is recorded above; a Harbor tag or publication still requires an explicit request.

## Earlier implementation notes

The following entries record completed feature work and remain historical. They are not a live platform release checklist.

## Command menu parity (2026-10-18)

Visual follow-up: command actions, navigation, and theme choices now use the existing semantic icon colors with tinted badges. Shared icon glyphs explicitly inherit their badge color so command-item defaults cannot mute them. Vendor artwork and status colors retain their roles. The frontend build and focused keyboard/settings browser checks pass; light and dark previews were inspected.

The command menu now reuses the shared service logos, project identity, resource favicons, and settings navigation. Actions include New project, Add existing project, Refresh, direct Appearance/Daemon links, and app theme choices. Creation uses the same dialog as Overview and the sidebar. Resource search includes all current resources, with recent items grouped separately and no duplicates. Search metadata updates when snapshot entries change, and reopening clears the prior query. Connection-sensitive actions remain disabled when disconnected or stale. The frontend production build, two command primitive unit tests, and four focused Chromium checks pass, including keyboard focus, service branding, non-recent resources, shared creation, and settings navigation. Browser previews were inspected. No daemon or project restart was needed.

## Service identity logos (2026-10-18)

Service cards, the service browser, service headers, and per-project service tabs now use a bundled offline vendor-logo catalog. Exact service IDs/names select logos for the current name-only snapshot; the resolver also accepts complete image repositories, tags, and digests for a future image metadata projection. An explicit unknown/private image overrides name guessing and retains the generic semantic icon. Grafana seed helpers share Grafana artwork; VictoriaMetrics helpers share its vendor mark. No Docker observation, daemon contract, dependency, or lifecycle behavior changes in this presentation slice. Asset sources and licenses are recorded beside the SVGs. The frontend build, eight focused lookup tests, and two existing service browser checks pass, with light/dark visual inspection and card/tab interaction proof.

## Network setup prompt (2026-10-18)

The Overview setup prompt now consumes the daemon's retained networking configuration, not just its ability to run setup. The snapshot adds optional `network_setup.configured`, projected from the current durable full networking stage, independently of bounded operation history. Missing metadata from an older daemon is unknown rather than an instruction to set up networking; explicit setup errors retain their recovery path. Completed setup removes the onboarding card, and subsequently removed configuration can show it again. This adds no migration or host mutation. Focused domain/authority tests, 77 store/view tests, the setup browser flow, the frontend build, and repeat-generation parity pass. Other pending appearance and project-creation work remains local and uncommitted.

## Graphical project creation (2026-10-18)

Help-format choices now show a live terminal preview and the selected format description. The optional option-level `preview` text comes from GoForj's real formatters through the schema, so the GUI stays stack-neutral and omits the panel for options without previews. Framework, Guided, and External CLI switching, narrow layouts, the existing creation flow, producer/adapter/wire checks, repeat-generation parity, and the frontend build pass. The installed local CLI includes the previews; the previous binary remains backed up.

The creation dialog now presents the current GoForj iOS logo in a branded stack card with its description and a connected step indicator. Multiple adapters use the shared select primitive with per-stack artwork and a neutral fallback. The first screen includes clearer input hints and a compact directory action. Frontend build, five focused timeline tests, and two relevant browser flows pass, with light/dark and narrow visual inspection.

The wizard now selects a compiled-in project creator. Its shared schema, field conditions, semantic destination role, creation jobs, output cursors, and error presentation are stack-neutral. GoForj is the first available adapter and retains responsibility for its CLI arguments and daemon registration. This creates an extension point for additional stacks; it does not claim other stacks can already run under Harbor.

Creation runs asynchronously with bounded live command output and explicit Create files / Add to Harbor stages. A failed render keeps its directory and offers editable choices. A completed render whose registration fails offers Retry registration using only the retained job's destination; this never invokes the renderer again. Jobs and retry authority are retained for the current desktop process only, with bounded output and job counts. The retry button is available in the creation dialog; after leaving that flow, Add existing project remains the recovery path. After restarting Harbor, Add existing project can register the retained directory. Custom Atlas agent and integration fields now follow producer-supplied conditional metadata.

Overview and the Projects browser can open a New project wizard. The installed, canonical GoForj CLI supplies a version-one `new --schema` document; Harbor renders Project, Stack, Options, and Review stages from that contract, including component dependencies, exclusive database choices, starter-kit availability, component-library selection, CLI help format, Atlas presets and custom choices, and Demo. Native directory selection proposes a child directory, with an editable absolute destination.

Creation passes validated argv directly to `forj new --no-interaction`. GoForj owns validation, resources, dependencies, and rendering. Harbor registers successful output with `harbord` and leaves it stopped. An unavailable CLI schema produces update guidance. A registration failure after successful creation reports the retained path separately, and partial creation files are never automatically deleted. Explicit RGB process output and existing project lifecycles are unaffected.

Local evidence includes a real CLI-only render and Go build, plus a production Wails WebView flow using a local CLI and the real daemon to create a Vue project. The native check verified registration in Stopped, removed only its temporary registration, and retained the generated checkout. The generated Vue Go application and frontend both build. This is local macOS evidence; no cross-platform creation or native directory-picker interaction is claimed.

## Terminal appearance settings (2026-10-18)

Settings → Appearance now groups app themes and System, Dark, and Light terminal choices with a live preview. GoForj's pale light code surface and Harbor's neutral near-black dark surface apply consistently to project output, service logs, and interactive shells. The local preference survives reloads, validates stored values, and remains usable for a session when storage is blocked. System tracks the resolved app theme; pinned choices remain independent. Existing ANSI scrollback recolors through CSS variables and open shells receive an in-place palette update, without replay, reconnect, or project restart.

The appearance menu also anchors directly to its button instead of nesting two trigger wrappers, fixing offscreen placement at the bottom of the desktop rail. The bottom rail cog opens Settings → Appearance; Daemon is a separate section for connection and snapshot details. Legacy `/system` routes redirect to `/settings/daemon`. Appearance stays available before the first daemon snapshot, and app-theme controls share reactive state with desktop and mobile quick menus. The production build, 259 frontend tests, and 28 Chromium checks pass, with dark/light card inspection. Tests cover preference persistence, unsupported storage values, unavailable storage, app-follow behavior, retained preview text, and shell continuity. Native consent, tray, and platform acceptance remain separate gates.

## Embedded development output (2026-10-18)

Development output defaults to one collapsed startup summary naming the current step or reporting completion and elapsed time. A failed step names the failure and expands its details automatically. Overview retains the full startup timeline. These are frontend presentation changes; raw process output and lifecycle authority remain with their existing owners.

The log viewer now uses tight terminal rows instead of the former 1.8 line spacing plus a 20-pixel row minimum, so box-drawing borders join. Streamed logs and interactive shells share the GoForj website code well's deeper near-black background (`#0A080D`) and a softer ANSI palette while preserving explicit RGB/256-color output. The frontend build, 248 unit tests, 26 Chromium checks, focused process-launch tests and race checks, and process-package vet pass. A fresh fixture server was required after branch/HMR changes to keep the existing dynamic bridge-mocking browser test on one module identity.

Harbor also offers `FORJ_DEV_PRESENTATION=embedded` with its private startup destination. Supporting GoForj versions omit the legacy `Dev ready` URL inventory at the producer; ordinary console behavior and raw build/error output remain intact. Companion GoForj PR #249 is merged and published in v0.33.1. After explicit user approval, Harbor stopped the registered Diablo Immortal Tracker project, the installed CLI was updated from v0.30.0 to v0.33.1 with a retained backup, and Harbor started a fresh session. The project returned Ready with five resources; its normal-trust HTTPS endpoint returned 200. Its new output omits the legacy URL inventory, and its private mode-0600 startup stream contains 16 records ending in completed. Existing log history remains unchanged.

## Project identity and semantic icons (2026-10-18)

Project rows, headers, and command results now share the current GoForj iOS icon while offline. They request the primary App favicon only in ready, rebuilding, or degraded states, with GoForj retained for discovery and failure. Tabs, service kinds, and resource kinds use consistent Lucide glyphs with restrained light/dark type colors; health remains explicit in the existing status treatments. This changes presentation only, without daemon contracts, dependencies, or project lifecycle changes.

The production frontend build, 248 unit tests, and 26 Chromium checks pass. Browser inspection covers light/dark desktop and a 430-pixel layout without document overflow. Focused tests cover offline lookup suppression, online discovery, discovery failure, and returning offline. Native tray and onboarding verification remain separate gates.

## Accepted project workspace design (2026-10-18)

Branch `feat/project-workspace-design` contains the accepted visual implementation of
the project detail screen and shared project browser. It prioritizes the project
name, status, and labeled actions; adds snapshot-backed overview shortcuts and
service summaries; gives development output the full working area; and removes
the illustration from project working screens. Shared shadcn primitives and
native/daemon contracts are unchanged. The user accepted this visual direction
on 2026-10-18; PR #6 is merged. Distribution remains pending. Frontend build, 237 unit tests, and 25 Chromium checks pass. Browser
inspection covers dark/light desktop and a 430-pixel layout without horizontal
overflow. The isolated fixture preview is served on `127.0.0.1:39517` while this
session remains running.
## Prerelease publication (2026-10-18)

The tag-driven `Prerelease artifacts` workflow admits beta/RC tags only after successful CI for the exact main commit. It reuses native macOS and Ubuntu package gates, verifies the tested bytes, and publishes them together with checksums and provenance as an unsigned GitHub prerelease. Manual dispatch performs the same build and sealing without publication. Windows public installers remain gated on production signing; its short-lived CI helper certificate must not leave the development acceptance workflow. Stable releases, genuine consent, reboot recovery, signing, and update proof remain separate gates. See [Publishing development previews](./releases.md).

## Structured startup diagnostics (2026-10-18)

The GoForj runtime supplies an optional owner-private startup event file to ordinary `forj dev` without changing its argv or delaying launch for another descriptor command. A supporting CLI emits bounded schema-v1 JSON Lines; older CLIs keep their existing raw output. Harbor validates exact session/sequence, schema, labels, status, file shape, and stream size before projecting a collapsible timeline in Overview and Development output. Quiet startup refresh is bounded; producer completion never marks a project ready. Startup metadata remains outside durable lifecycle state and raw child errors remain in the retained log.

Failed project activity also performs bounded read-only Docker diagnostics. It compares checkout-admitted non-running containers' configured host binds to fresh running-container publications and reports the service, requested endpoint, and competing container. Wildcard IPv4 binds overlap a project loopback; distinct loopbacks and different protocols do not. Harbor performs no foreign-container mutation. Native process-only listeners and unavailable Engine observations retain raw-output troubleshooting rather than guessed owners.

Local validation covers portable runtime/control/reconcile/authority tests, focused race tests and vet, producer task failure without leaked output, strict event admission, wildcard/exact/different-loopback ports, the frontend build, 241 frontend tests, generated-contract parity, and the isolated native WebView focus/hide/restore/binding/event smoke. Native UI automation is disabled on the local Mac, so genuine tray clicks, directory-dialog cancellation, shipping consent, and installed onboarding remain manual/product-worker gates. No installed GoForj CLI has been replaced by this work.

## Wails v3 desktop trial (2026-10-18)

Initial startup now restores and focuses Harbor once both the native application and window are ready, regardless of event ordering. Later frontend reloads do not reactivate a hidden or background window. Lifecycle tests cover ordering and repeated ready events; the native smoke now verifies initial focus before checking hide/restore, bindings, and events.

macOS icon presentation now keeps the application runtime icon unset, preserving the compiled asset catalog used by the Dock rather than replacing it with the opaque square PNG. Its menu bar uses a separate transparent monochrome anchor template generated from the canonical SVG; Windows/Linux retain the existing application and tray artwork. Asset regeneration is documented in `desktop/build/README.md`. Desktop tests/vet, repeat-generation parity, and the local native smoke pass; the running development bundle needs a restart for visual verification.

The development server binds to `127.0.0.1` so Wails' IPv4 asset proxy can connect even when `localhost` resolves to IPv6 first. The frontend install task reruns `npm ci` when either package manifest changes or the installed lockfile is absent; unchanged dependencies are reused. Darwin builds explicitly align cgo, the external linker, and bundle metadata with the compiler's supported minimum: macOS 12 for Go 1.26 and macOS 13 for Go 1.27. This does not increase the module's minimum Go version or change the existing macOS 15 installer baseline. Focused builder tests/vet, helper/relay/desktop native builds without deployment-target warnings, and an isolated IPv4 Vite HTTP probe pass. Restart the development graph to use the updated task.

The migration pins the Go module, CLI, and JavaScript runtime to v3.0.0-beta.32. A native tray projects daemon connection state and offers Open Harbor and Quit Harbor UI. Closing the window hides it; explicit activation restores and focuses it. Desktop shutdown still joins its client goroutine and does not stop projects or the daemon.

The v3 transport is installed before Vue imports select the bridge. It exposes the existing generated method whitelist, unwraps native event envelopes, and preserves clipboard errors. Taskfile-based development and `cmd/desktopbuild` replace `wails.json`, while package outputs retain their existing paths and identities. Linux uses v3's `gtk3` compatibility tag with its existing GTK3/WebKit2GTK 4.1 runtime.

Local macOS native smoke passes with the production SPA and a fake daemon, covering service calls, event delivery, and restoration without host mutations. Interactive tray clicks, directory dialogs, and WebView behavior on every supported desktop remain release gates. This is a beta trial, not a stable-distribution claim. Desktop Go tests/vet, frontend unit tests/build, all 25 Chromium checks, and generated-contract parity pass locally. The full headless suite still encounters existing macOS setgid-mode and host-conflict-observation failures, reproduced on the unmodified base commit; they are not attributed to Wails.

## GoForj tooling baseline (2026-10-18)

Generated-project CI, hosted lifecycle workflows, and development-package workflows now select GoForj `v0.30.0` (release commit `f46e5010b75ebcde3c0251d23ea3650b58ca0922`). The generated-project acceptance fixture records `0.30.0` as well. The isolated fixture explicitly supplies current renderer defaults and compares the resulting single-document YAML by value, allowing formatting and renderer-owned timestamp changes while rejecting added fields, changed values, duplicate keys, and extra documents.

Local validation for this update passes both module vet checks, the complete desktop Go suite, fixture unit/race tests, the real MySQL render acceptance test, and Harbor's read-only schema-v1 project descriptor. Two repeat renders of an isolated generated MySQL project produce no file changes. The root suite is not clean on this host: native host-conflict observation exhausts its transient-race budget, output-broker tests force a damaged shared module cache, and state tests reject non-UTC timestamps. Daemon-launcher and discovery path tests pass when repeated with a canonical temporary directory. No new hosted or privileged lifecycle evidence is claimed.

This replaces the earlier CI baseline; historical run IDs below remain evidence for their original commits, not validation of this update. Runtime executable admission still checks canonical command/module identity rather than an exact release. Harbor's own `goforj_version: 0.19.0` remains historical render metadata: the customized application and frontend have not been regenerated over existing development work.

## Trusted HTTPS happy-path gate

The three-project trusted-HTTPS happy-path gate is met at Harbor commit `e0c9773`. One repeatable acceptance test starts from clean Harbor-owned state, renders three unmodified GoForj projects that all retain App port 3000, runs them concurrently at distinct literal `https://<project>.test` URLs on port 443, and verifies distinct responses through normal system DNS and default trust. The probes use no caller-supplied port, resolver override, custom CA, or TLS bypass.

The same test passed on GitHub-hosted Ubuntu, macOS, and Windows runners in runs `30166963369`, `30166963384`, and `30166963379`. It stops and unregisters all projects, releases Harbor-owned resolver, trust, loopback, ingress, process, and per-user daemon state, preserves foreign state, and leaves all three generated checkouts byte-for-byte unchanged. CI run `30166963374` and hosted platform-network run `30166963368` passed for the same commit. No self-hosted runner or project VM was used.

This is behavioral evidence for the trusted-HTTPS gate, not a complete release claim. Interactive consent under shipping user/elevation profiles, native Docker-provider behavior on macOS and Windows, reboot recovery, installers, signing, updates, tray/desktop smoke, and public exposure remain separate work.

The managed barrier now also repairs missing descriptor-declared host service reservations before it joins fresh Compose port observations. This closes the daemon-restart path for an already-attached GoForj process; commit `bd54830` has a focused regression and passes the full root test suite. Native execution against a user-selected local Docker-compatible Engine is still required for product evidence.

Managed publication observation now distinguishes a retryable pre-full network stage from a genuinely missing durable reservation, so a resolver-stage daemon no longer reports a misleading endpoint defect. GoForj commit `a4b35cd5` bounds each temporary managed Compose barrier wait to 30 seconds when no caller deadline exists; a stuck barrier therefore fails fast while Harbor still has time to settle the child. Harbor launch, attach, stop, and process-join paths also use bounded contexts and quarantine unresolved process scope instead of leaving a project in `starting` indefinitely.

Project admission now distinguishes an uninitialized network from the honest post-DNS boundary: resolver-stage DNS is sufficient to reserve and publish observed native TCP service endpoints, while HTTP resources remain gated by `project.network.full_setup_required` until shared HTTPS/native ingress and trust authority are installed. The desktop explains that DNS is active but secure web ingress is not, and does not offer the resolver-only setup action again. This keeps the service path useful without pretending SSL is ready; full data-plane activation remains the next macOS networking slice.

The production `harbor setup` command now selects the complete pool, resolver, trust, and paired low-port workflow on macOS cgo builds. It validates and correlates every daemon response before opening the next approval boundary, retries only validated cancelled or explicitly retryable terminal operations, and reports completion only after the data-plane operation reaches its terminal `completed` phase. Non-macOS and non-cgo builds deliberately retain the prior pool-only command until the same native boundary is ported. Portable command tests prove the full ordering and failure matrix; native macOS consent, trust, resolver, and public-port execution still require the production-daemon acceptance gate and are not claimed by those tests.

Since the previous handoff, Harbor also has a post-start native-route activator: the managed barrier now plans the complete observed publication set against Harbor-owned reservations, replaces native relays without rebinding shared listeners, and acknowledges only after every relay is live. An inherited GoForj session now sends the complete replacement reset and retries that barrier once its initial watcher graph is online; Harbor re-observes service ports instead of trusting GoForj to invent upstreams. The managed registration path can now reconstruct its ephemeral publication authority from an exact durable attached session after a daemon restart, and GoForj retains a process-local launch context for a fresh authenticated replay connection. GoForj now retries one lost managed-session transport and runs a bounded heartbeat that resets and republishes observations while the dev process remains alive; Harbor also preserves an exact live attached process birth during recovery so replay can reconnect without killing it. Supervisor output now has an owner-private, checksummed session spool outside the checkout, including crash-tail-safe replay and bounded absolute-cursor compaction; it is retained diagnostic history only and does not claim live process availability or reattach pipes. The additive managed-session events schema is now mirrored and strictly validated in both modules but remains capability-off and handler-free; it defines output chunks and explicit gaps without changing live output. Harbor and the dedicated GoForj worktree now mirror a strict, capability-off output-reattach begin/challenge/confirm contract (`e37fa15f`) that binds the exact session process evidence, managed-session fence, owner-private endpoint shape, nonce, and opaque ticket; it remains schema-only and does not advertise or grant stream authority. The runtime-plan schema remains capability-gated and optional: Harbor can derive App binds from the durable project lease and fresh Compose publications for peers that explicitly negotiate it, while peers without that capability continue the existing lifecycle path. Harbor does not require unpublished service-environment assignments. Unsupported runtime kinds and driver-specific URL/DSN shapes remain fail-closed. GoForj now also carries additive stable task IDs and typed lifecycle phases for exact generated Compose, database-readiness, and frontend setup tasks, and managed admission migrates only exact framework-owned legacy forms in memory before rejecting ambiguous custom tasks or cross-scope phase assignments. Standalone execution remains unchanged. The initial process-surviving output broker now launches when the exact sibling artifact is installed, with direct pipes retained when it is absent or its journal cannot be preflighted. First-start attachment persists owner-private broker evidence, and recovery now revalidates the manifest, ticket digest, broker PID birth, executable, and argv before adopting a surviving broker on Linux and macOS; unsupported identity readers fail closed and historical spool output remains the fallback.

The macOS retained-lease repair boundary now correlates every endpoint row back to native socket owners before deciding ambiguity. One same-user process may expose the leased port through multiple exact, wildcard, or dual-stack records and still be settled as one project scope; a process that exits during descriptor census is treated as a bounded transient race when the stable listener rows remain fully accounted for, while unaccounted rows, multiple owners, or foreign evidence remain fail-closed.

Project activity now surfaces the owner-private output spool as explicitly historical output when the live supervisor is unavailable. The retained tail remains read-only diagnostics rather than process authority; malformed or missing history leaves the activity view cleanly unavailable, and a later live stream replaces the historical view at a fresh cursor boundary.

The new `OutputBrokerJournal` boundary reuses that checksummed spool for exact frame replay, idempotent append retries, bounded live subscribers, explicit backpressure gaps, and monotonic acknowledgements. The standalone `cmd/outputbroker` now owns that spool while it adopts inherited stdout/stderr pipes and serves an authenticated local replay/live connection; the broker remains separate from GoForj process stop/reap authority.

The local IPC package now also admits endpoint-specific owner-private Unix sockets and Windows named pipes through the same operating-system peer checks as Harbor's daemon endpoint. `OutputBrokerPeer` binds that transport identity to an exact project/session, endpoint shape, separate broker `ProcessEvidence`, and owner-private manifest/ticket digest; `AuthenticateOutputBrokerPeer` requires the kernel PID and a fresh full-process observation to match field-for-field. The daemon provider discovers only a canonical sibling `outputbroker` executable, and the launcher uses fixed inherited descriptors, retains the owner-private manifest for the broker lifetime, and removes it on normal attachment close. Supervisor hands authenticated records back through its existing relay without transferring child stop/reap authority; the broker path is optional and falls back to direct pipes when its artifact, journal, or startup handshake is unavailable. Successful first-start attachment now persists that exact broker metadata in the same fenced session mutation as the child process, while the raw ticket remains outside SQLite and the checkout. Recovery now re-observes the persisted broker process and adopts its output-only transport after exact manifest and ticket checks; Linux and macOS have native identity readers, while unsupported platforms fail closed and keep the historical spool fallback.

This document describes the repository as it works today. The other documents in this directory describe Harbor's intended product and architecture; their phase gates are not claims that the corresponding work is complete.

## Authoritative production boundary

This section supersedes earlier managed-session implementation claims in this document. Production admission verifies only the canonical embedded GoForj command and module identity; it does not pin a version, revision, or clean working tree. `NewProjectLifecycleCoordinator` does not configure a managed launch factory. Harbor therefore launches ordinary `forj dev`, injects its leased loopback through the marked `.env.host` block, probes readiness itself, and owns process supervision, routes, and TLS. Managed-session launch-context, publication, replay, and runtime-plan code is dormant pending deletion or activation. The reviewed GoForj revision used by generated-project CI is a deterministic source pin, not a runtime compatibility requirement.

Commit `e0c9773` is the three-platform hosted milestone. Runs `30166963369`, `30166963384`, and `30166963379` passed the same native lifecycle on GitHub-hosted Ubuntu, macOS, and Windows: clean Harbor-owned state, full setup, three generated projects retaining port 3000, distinct literal system-resolved and default-trusted HTTPS identities on port 443, production stop/unregister/release, foreign-state preservation, and exact checkout verification. Runs `30166963374` and `30166963368` passed the corresponding CI and hosted cross-platform API smoke.

This is not a three-platform release claim. The hosted jobs deliberately adapt GitHub's available elevation profiles and do not prove genuine polkit, Authorization Services, or UAC interaction, a filtered Windows token, an interactive desktop, native Docker-provider behavior on macOS or Windows, reboot recovery, installers, signing, or updates. Docker provider choice remains outside trusted-HTTPS admission.

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

The repository is a working vertical slice, not a releasable product. One real GoForj project has been registered, assigned a non-default loopback address, started through the desktop, observed as ready, and shown with live `forj dev` output. The current source also observes that checkout's Compose services, refreshes their durable service projection after local container events, and streams a selected service's container logs through the desktop without asking the generated App or GoForj CLI to access Docker. Reserved-resource route projection now joins descriptor-matched ready resources to exact endpoint reservations and private upstreams; a durable scoped `project.restart` operation now replaces one live session through a fenced stop boundary and the same readiness pipeline. Normal Start is now a convergence action even after a project-local recovery quarantine: an exact retained process receipt is settled through the native supervisor before a replacement session is admitted, while a receipt-free planned/awaiting-attach launch first reads its exact project and primary-listener fence, settles a proven same-user listener, and retires the row only after that boundary converges and all process fields remain absent. If PID-only settlement cannot converge but the project still owns its primary lease, Start makes one bounded native same-user listener repair attempt against that exact leased address and App port before surfacing recovery-required. If a project-owned loopback still has its App port occupied, Start now performs a native same-user inspection and settles either the exact `forj dev` scope or the same-user process scope that owns the leased port, including one uniquely correlated wildcard bind, before retrying admission; this same repair runs while selecting a fresh candidate, so a newly allocated address stays stable when its exact listener is proven to belong to the project. Transient native drift is re-probed for a short bounded window so a listener that is already exiting does not become a user-visible conflict. Foreign users, multiple owners, ambiguous native facts, and unsupported platforms remain fail-closed. The desktop presents this as the ordinary `Start project` action; explicit native inspection remains a separate fallback when Harbor cannot prove the leased listener scope. Failed stop, join, and route-publication edges now leave a route-free recovery boundary instead of stranding the project in `stopping`. The desktop lifecycle surface also refuses start, stop, or restart against a stale/disconnected retained snapshot without sending a request or creating a phantom intent; an uncertain first request remains retryable until a baseline snapshot exists. Harbor now has a pure, session-fenced planner, an ephemeral exact-fence publication registry, and a durable boundary that re-reads Harbor-owned reservations, resolver-or-full network ownership, project readiness, and the exact attached session before returning a route plan; the strict managed-session contract and authenticated handler seam now have a matching private GoForj client adapter, and the authenticated control endpoint can isolate a configured GoForj role, complete exact awaiting-session attachment, and accept fenced publication replacements without exposing human methods. Its barrier remains unacknowledged until native route activation and secure launch context are available. Production Start and Restart now create the owner-only launch context before durable launch admission, and GoForj consumes it before project configuration, dials the local Unix endpoint, and retries only the explicit pre-attachment startup race; ordinary `forj dev` argv remains unchanged. The negotiated `managed-session.launch-context.v1` capability carries a bounded launch ticket, and Harbor verifies its SHA-256 against the durable session digest without exposing credential material. Resolver approval now also starts and restarts Harbor's authoritative DNS-only generation immediately; observed native TCP publication can use that DNS stage, while HTTP/HTTPS remains gated on the later full-network and trust proof. Native route activation, publication activation, durable session replay after daemon restart, resolver parity, trusted HTTPS installation, and packaging are still active work.

The managed-session portion of that summary has since advanced: inherited GoForj now resets publications and retries the authenticated barrier from its initial watcher graph, while Harbor replaces the reset with exact observed service ports before activating native relays. A fresh Harbor authority can replay the exact attached fence without a durable generation bump, and GoForj automatically retries one lost transport plus periodic observation republishing; Harbor preserves the live exact attached process during recovery, while the process-surviving event broker and native local-Engine evidence remain active work.

The managed runtime-plan reader remains capability-gated and optional. Harbor does not require the unpublished GoForj service-environment experiments that were removed from the sibling worktree; projects still start without those fields. Resolver-stage DNS now supports observed native TCP service publication, while HTTP/HTTPS resources remain guarded by full network and trust authority. Driver-specific URL/DSN shapes, native relay execution, and native local-Engine evidence remain open; broker output re-adoption now has a fail-closed Linux/macOS implementation with historical spool fallback.

The current managed slice launches each active HTTP App on its Harbor-assigned loopback and keeps the public `.test` URL separate from the private bind. Before any project-network mutation or process authority is created, `harbord` invokes the exact registered GoForj executable with `project:describe --json`, strictly validates the versioned secret-free schema, and stores its normalized topology digest in the active session. An invalid descriptor therefore fails the queued start without leaving a primary lease or endpoint reservation behind; if registration changes while admission rereads it, Harbor revalidates the admitted checkout before launch. Once that App is ready, `harbord` uses a local, read-only Docker Engine client to list candidate containers and inspect their immutable facts before projecting active Compose services into the Services UI. Admission requires the exact Compose project, service, and working-directory labels plus the registered canonical checkout; neighboring projects with the same service names are excluded. Harbor separately asks the exact supervising GoForj executable for its bounded resource-only `dev:status` report so framework-owned links can appear beside the directly observed services. The product-proof verifier requires a typed event-refresh record with target/service identity, advancing project revision, replaced target container IDs, and unchanged peer IDs, along with a local Engine 28-equivalent version floor. It records the selected provider but does not admit or reject by provider name. Cleanup evidence is additionally bound to the exact lifecycle worker identity and artifact digest set. The tagged Linux Docker acceptance job emits this evidence on hosted Linux. The available cross-platform gate is instead the shared three-project trusted-HTTPS lifecycle on GitHub-hosted native operating systems; it does not admit or reject any Docker provider and does not claim interactive desktop or release-worker behavior.

The daemon-owned container adapter implements local list, inspect, log-stream, and container-event wake calls. Every wake repeats exact canonical checkout admission and reprojects services through fenced project and session state, while unsupported or failed framework queries retain the last-known resource links. Portable coordinator tests cover the complete wake, fresh observation, fenced replacement, and route-publication edge, but they do not substitute for native execution against a user-selected local Docker-compatible Engine. The direct fixture proofs exclude neighboring projects, follow recreated replicas, and verify Harbor's reads do not mutate containers. The tagged three-project lifecycle test adds stop, force-recreate, peer-isolation, and exact-cleanup assertions. Linux can emit strict Docker lifecycle and cleanup evidence today; native macOS and Windows Docker product evidence is unavailable without suitable workers. Provider choice is not part of the proof contract: the verifier requires a named provider, a compatible Engine version, exact checkout ownership, and the product behavior, without allowlisting Docker Desktop or any alternative.

The live managed-session edge described above is now wired through the inherited GoForj process: its first watcher graph starts the publication reset/barrier retry, and Harbor performs the authoritative service-port observation and native activation. This does not yet claim durable replay or the desired pre-Compose/post-Compose migration ordering.

The Harbor Compose barrier is now phase-aware: an authenticated request may join Harbor-owned service publications while the project is still `Starting`, but only the exact requested fence receives that allowance and the observer must provide fresh service facts. The default managed publication planner still requires `Ready`; the managed GoForj path uses the early barrier, then requests its runtime plan before post-Compose readiness and migration.

The inherited GoForj startup path now does that ordering: explicit pre-Compose tasks and frontend/App build complete, typed Compose runs, the authenticated Harbor barrier is awaited synchronously, Harbor then returns a capability-gated runtime plan, and only then do post-Compose readiness, migration, and post-migrate tasks run. The plan is applied as a process-local overlay: HTTP listener binds plus explicit database host/port, SMTP host/port, and Redis address consumer assignments are merged after normal project environment loading; the `.env.host` block remains the compatibility bridge when an older Harbor lacks the capability. Harbor-owned `down_on_exit` cleanup now orders typed pre-Compose-down, Compose-down, and post-Compose-down tasks, while standalone `forj down` remains unchanged. Driver-specific URL/DSN formats still fail closed until their value shapes are added to the descriptor contract. The full process/action event contract remains open; standalone `forj dev` is unchanged.

The pure managed-publication observation normalizer now supplies the registry boundary from existing Harbor facts: it selects only descriptor-declared Compose-owned host TCP endpoints, requires one exact native-port observation and matching durable reservation, emits a deterministic complete replacement, withdraws stale publications when facts are incomplete, and rejects malformed candidates without replacing the registry's last good set. It remains a Harbor-only join; it does not add a GoForj wire contract or activate native relays.

The Docker service watcher treats a transient Engine event-stream failure as a bounded reconnect condition. It retries the wake stream without treating the failed connection as topology, then surfaces a persistent failure after the retry budget; unsupported event sources remain quiet. Portable coordinator coverage exercises this reconnect boundary, but it does not substitute for native execution against a user-selected local compatible Engine.

The fresh Docker observation at both the ready edge and after a wake now classifies only Engine transport unavailability, retrying the exact checkout read within the same bounded, cancellation-aware budget. Canonicalization, label admission, malformed facts, and other non-transport failures remain terminal; no partial observation is ever projected as topology. If the ready-edge budget expires, Harbor preserves the healthy App and marks service observation unavailable as before; the event watcher can recover later. Portable coverage exercises both recovery and bounded terminal failure, but native local-Engine execution remains required evidence.

The desktop bridge treats every required generated Wails binding as part of its native capability boundary. Its fixture and selection tests include `ResourceIconURL`, reject native mode when that binding is absent, and exercise it through the authoritative bridge; this prevents browser fixtures from disguising an incomplete native bridge.

The framework-resource process harness canonicalizes both its supplied checkout and the child working directory before comparing them. This retains one exact checkout identity while accepting macOS's equivalent `/var` and `/private/var` spellings; it does not permit a different resolved checkout.

The Darwin host-conflict observer now permits a bounded 10 ms, cancellation-aware settlement delay after an explicitly detected native table-generation race. It still requires two matching complete observations, allows at most 31 retries after the initial pass (32 passes total), and returns no admission result if the tables remain ambiguous.

The hard-restart fixture keeps its deliberately signal-ignoring watcher and listener alive with a sleeping wait rather than an empty `select`, so Go's deadlock detector cannot terminate the fixture before the durable launch boundary. The restart assertion remains a native macOS proof requirement.

The Darwin PCB observer recognizes XNU's documented IPv6-family null-bind form that carries a canonical IPv4 fact. It accepts that form only with `INP_IPV4` and zero IPv4-in-IPv6 padding, projects it as IPv4 for conflict classification, and continues to reject mapped, noncanonical, and contradictory address facts. Its Darwin-native fixture now exercises that admission on a requested port and keeps a non-wildcard dual-stack flag combination as a fail-closed contradiction.

Phase 1 production acceptance now distinguishes active work from the bounded terminal operation history intentionally retained in every authoritative snapshot. Startup recovery and idempotent removal require no nonterminal operations; they do not erase completed operation evidence.

The native Unix IPC integration keeps its client connected until server-side peer admission completes. This preserves Darwin's `LOCAL_PEERCRED` boundary under test rather than converting a deliberately closed socket into a credential retry path.

The Darwin retained-runtime observer now treats a positive `PROC_PIDLISTFDS` size query as bounded descriptor-count evidence rather than a failed empty-buffer call. Its second read reserves twenty fixed descriptor records to distinguish a complete census from a growing one, then rejects a saturated, malformed, or over-limit result without signaling. The final signal gate rereads every captured session member's executable, argv digest/count, cwd, UID, process-group, session, parent, and birth facts, in addition to the exact socket and root checks, so non-root identity drift cannot authorize `SIGTERM`; the Darwin comparator suite now exercises each of those drift dimensions directly. The same listener census also accepts XNU's IPv4-capable AF_INET6 null-bind form, so a dual-stack App cannot strand an otherwise proven project-owned port. Native CI execution remains the required proof.

The Linux `systemd-resolved` recovery path reconciles only bounded, root-owned Harbor transaction names before a public observation. It removes an unpublished canonical stage without restart, recognizes an exact public replacement plus retained owned stage as an exchange crash, restarts `systemd-resolved` while that stage remains as retry evidence, and removes it only after fixed/stage identity and content revalidation. A failed or uncertain restart therefore leaves the exchanged marker for the next recovery attempt. Recovery restores an exact owned removal quarantine only when the fixed artifact is absent, and preserves every foreign, malformed, unsafe, or ambiguous pairing. If more than one Harbor transaction artifact is present, recovery now rejects the ambiguous set before reading or mutating any artifact; entries carrying a Harbor stage or quarantine prefix are also rejected unless they match the exact transaction shape, so malformed crash debris cannot be mistaken for unrelated host state. Its strict `DNSEx` reader treats a zero port as the systemd representation of ordinary DNS's default port 53, while retaining exact address-family and server-name validation. The opt-in root test appends a 4 KiB-capped native cause when its initial observation or mutation fails; production errors remain typed and redacted. The new source behavior still needs a successful privileged Linux CI run before it is native support evidence.

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
| `desktop` | Nested Wails v3.0.0-beta.32 Go module. |
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
2. builds `./cmd/outputbroker` as the optional sibling `./bin/outputbroker`;
3. runs embedded migrations;
4. runs `harbord --foreground`;
5. starts `wails3 dev` in `desktop`;
6. lets Wails build the current macOS development helper artifacts through `cmd/desktopbuild`.

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
wails3 dev

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
15. Restart uses one durable `project.restart` operation: it withdraws routes, settles the exact old session, records a stopped boundary without losing the operation identity, then re-admits the primary lease and launches a replacement session through the normal descriptor/admission/readiness path. Lease admission deliberately occurs after the old listener is gone so the project cannot conflict with its own App port. Restart succeeds only after the replacement is ready; retry and daemon-recovery paths preserve the same operation and process fences.
16. Start and replacement Restart withdraw routes and run the exact verified `forj down` executable in the selected canonical checkout before launching `forj dev`; cleanup is serialized per checkout and bounded, and any failure terminalizes the operation without leaving an active session so the user can retry.

Start, Stop, and scoped Restart are exposed through the control protocol, desktop, and first-class `harbor start <project>`, `harbor stop <project>`, and `harbor restart <project>` commands. The CLI prints the authoritative operation state, offers `--json` for scripts, and includes an explicit retry intent after an indeterminate daemon call. `harbor open <project> [resource]` resolves a fresh project-scoped resource from the same snapshot and launches it through the fixed native browser handler for the current OS; it never accepts a URL from the caller.

`harbor status <project>` selects one project from the same authoritative snapshot used by desktop clients. Its default view is compact and `--json` prints that project object without a command-specific wrapper. `harbor open` defaults to the readiness-proven `app-http` resource and accepts an explicit project-local resource ID for framework links.

`harbor logs <project>` reads bounded current-session GoForj output. `--service <id>` selects one project-scoped Compose service and `--follow` holds an authenticated cursor until new output or interruption; neither path grants the CLI Docker or lifecycle authority.

`harbor doctor [project] [--json]` reports only authenticated daemon status, validated snapshot integrity, observed sequence drift, and raw project state/counts. Its explicit `control-plane` scope does not claim DNS, TLS, Docker, helper, descriptor, process, route, lease, or native-platform health; those require a future versioned diagnostics contract and native evidence.

This is intentionally narrow compatibility code, not a second GoForj parser. Registration reads `APP_NAME` from `.env`, then root `project_name` from `.goforj.yml`, then `APP_NAME` from `.env.example`. Runtime discovery reads only `API_HTTP_PORT` or `PORT` from `.env` and then `.env.example`, and verifies the generated `internal/http/runtime.go` default-host contract through bounded Go AST inspection. `Supervisor.Start` still executes the ordinary `forj dev` argv directly, without a shell; the production coordinator does not supply its optional managed launch context. Compose intent and mutations remain inside GoForj; Harbor's daemon adapter observes only exact checkout-attributed runtime facts. The resource-only GoForj query is optional enrichment, not a service-state or log transport. GoForj now exposes a read-only `forj project:describe --json` schema for static project identity, conventional available Apps, default HTTP runtime intent, a non-secret topology digest, and a pure service-requirement projection derived from project-owned `.env.example`/`.env` layers. Harbor invokes that exact descriptor before production process launch, strictly validates schema v1, and stores its normalized digest in the active session; the validator also admits optional, strictly bounded resource intent and service requirements with stable IDs, explicit App consumers, endpoint protocols/ports/visibility, and no secret values. When those sections are present, ready framework links must match their owner, runtime, and path before Harbor stores them, and Harbor derives exact resource-label `.test` reservations only from those matched ready resources; selected host-visible TCP service requirements also receive `service:`-namespaced native endpoint reservations, while private upstreams remain unassigned and no LAN/public exposure is inferred. The durable lifecycle store now has a fenced, idempotent `awaiting_attach`→`attached` mutation that rechecks exact process evidence and advances the session generation. Harbor's authenticated managed-session handler and GoForj client remain implemented but dormant because production Start and Restart do not issue a launch context or negotiate those capabilities. Generated-project CI uses GoForj `v0.30.0` as a deterministic test input; runtime admission verifies canonical embedded command/module identity rather than an exact version or source revision. Those remaining contracts are described in [GoForj integration](./goforj-integration.md).

## Temporary `.env.host` bridge

The intended GoForj contract uses a typed managed session and a trusted final runtime overlay outside the checkout. The negotiated overlay now carries Harbor's supported HTTP assignments; older Harbor/GoForj pairs continue using the current explicitly accepted bridge, which writes a single replaceable block at the end of `.env.host`:

```dotenv
# harbor managed: begin
API_HTTP_HOST="127.77.0.10"
DEV_SERVICE_IP_ADDRESS="127.77.0.10"
IP_ADDRESS="127.77.0.10"
LIGHTHOUSE_URL="ws://127.77.0.10:3000/lighthouse/ws/agent"
# harbor managed: end
```

Harbor preserves all content outside the marker pair, rejects malformed or repeated markers, updates the file atomically, and rewrites literal local hosts and URLs onto the assigned address. Before launch it removes the file-owned keys plus `APP_NAME`, `FORJ_APP`, `FORJ_BUILD_PROGRESS`, `FORJ_COMMAND_PREFIX`, and `FORJ_DEV_PLAIN` from its captured ambient environment, then sets `FORJ_DEV_PLAIN=1`. Other ambient values remain inherited normally.

This is deliberately temporary, but it is the current production loopback-injection mechanism while the dormant managed-session path is deleted or replaced. After a fully settled Harbor-requested shutdown, the supervisor removes only its exact managed block; malformed or foreign marker ownership is left untouched and prevents a false clean stop. Any replacement must preserve ordinary OS-environment precedence and environment reload behavior.

Each accepted launch also replaces `<checkout>/_data/harbor/forj-dev.log`. The file is owner-private (`0600`), bounded to 4 MiB, and ends with a visible truncation marker when output exceeds that bound. Both this trace and the `.env.host` block are interim checkout mutations that depart from the target outside-checkout managed-session storage model.

## Process ownership and restart recovery

Harbor must never interpret a busy port as process ownership.

Current launches use these ownership scopes:

- macOS and Linux: one dedicated Unix session created before `forj dev` starts;
- Windows: one kill-on-close Job Object.

The durable process receipt includes exact birth identity. Unix recovery walks the complete owned session, crossing watcher-created process groups, and revalidates each member's birth and session before signaling it. Windows retains Job Object behavior during one daemon generation and uses exact creation evidence during restart observation.

Before session evidence is retired, Harbor must prove that the complete owned scope is absent. An unresolved scope makes only that project unavailable and route-free while retaining its evidence; it must not prevent `harbord` or unrelated projects from starting.

Legacy state created before complete scope receipts is now repairable during Start when Harbor still has the exact project primary lease and native evidence ties one same-user listener to the leased port and registered checkout scope. A port, PID, checkout path, `.env.host`, or command line alone still cannot prove ownership. If the durable lease or checkout correlation is absent, the explicit unattributed inspect/confirm path remains the safe fallback and must label the candidate as not proven Harbor-owned.

The retained-session repair is now wired through reconciliation, authenticated control, Wails, and the project detail view. Inspection accepts only a project ID; the daemon derives durable and native facts, retains the native receipt in a short-lived process-local plan bound to the authenticated caller, and returns only a fixed process-shape label (`forj dev` or `project listener`), checkout, endpoint, root PID, member count, expiry, and opaque selectors. Confirmation consumes the plan before revalidating every durable and native fence. Drift, ambiguity, expiry, caller mismatch, replacement, or incomplete settlement cannot signal a process and requires a fresh inspection.

The native implementation is currently macOS-only. Retained-session repair requires one exact same-user listener, a dedicated session rooted at the expected `forj dev` process, stable birth/executable/argv/cwd/socket/tree facts, and complete session plus socket settlement after graceful `SIGTERM`; one exact-session `SIGKILL` escalation is allowed only after the user confirms that candidate and the bounded graceful period does not converge. The durable scoped restart path now handles the normal live-session replacement and retains exact operation/session fences across its stop-to-start boundary, but its daemon-loss and native Docker/managed-session behavior still needs execution on a real macOS host before this becomes a native recovery claim. The already-retired case has a separate inspector and confirmation backend that correlates one same-user listener (exact-address or wildcard) to the leased port, preferring a `forj dev` root and otherwise using the listener process only when the lease provides the project identity; confirmation revalidates the scope, signals only the displayed root, and uses one exact captured-member `SIGKILL` escalation when the bounded graceful period does not settle, while waiting for captured births and the target socket before changing Harbor state. Start uses that same native backend automatically when the project's primary lease is exact; the caller-bound reconcile, authenticated control, Wails, and desktop confirmation path remains available for route-free projects without an automatic lease admission edge. The macOS CI selector now includes the non-dedicated inspection/confirmation/settlement proof alongside the retained-session lifecycle, ambiguity, drift, and checkout-owned-listener cases. Linux, Windows, and other adapters report unsupported through the same portable contract. Darwin AMD64/ARM64 cross-builds and portable tests pass, but the libproc observation and signal path still need execution on a real macOS host before this behavior is a native support claim. No-session confirmation intentionally returns the unchanged route-free retryable projection; it does not manufacture a Harbor session or perform durable completion mutation.

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
- resolver-stage activation of the authoritative DNS-only data-plane generation, including daemon restart reconstruction;
- activation of the full process-local data plane after the later network and trust gates complete;
- an optional Darwin+cgo authenticated global-release control surface with start/read operations and independently negotiated low-port, resolver, trust, and loopback-pool approvals. The daemon assigns the operation ID, rebuilds fresh exact full-stage authority from durable state plus ownership, low-port, resolver, trust, and every pool-loopback observation, then durably stages it before the controller retires the exact full listener generation. At each owner- and checkpoint-bound boundary, prepare lazily issues only the corresponding release helper capability. Resolver prepare uses the release-only plan source and issuer; if publication durability is uncertain, it returns the sole result reference with an explicit indeterminate disposition for caller reconciliation. Resolver confirmation accepts `owned_absent` helper evidence only when an independent fresh native observation validates it, preserving the foreign-resolver rules; its durable receipt advances `resolver` to `trust`, and exact replay cannot issue another capability. Cold recovery orders Arm, zero-listener control-anchor startup, then coordinator Recover, but deliberately never crosses a user approval boundary. Unsupported binaries and Darwin without cgo do not advertise these capabilities;
- a trust-release foundation that keeps setup `ensure_trust` issuance separate from global-release `release_trust` authority. Its owned-only release plan source, disposition-aware durable trust receipt, `trust`-to-`loopbacks` advance rail, coordinator, narrow Darwin+cgo issuer wiring, and authenticated authority/control/RPC surface are implemented; launcher metadata admits `release_trust`. Preparation returns a removal ticket only for exact owned trust, reports indeterminate publication without allowing blind reissue, and returns an explicit no-ticket disposition for a staged pre-existing unowned root. The pre-existing path carries JSON `null` instead of fabricated helper evidence. Confirmation independently re-observes the exact requester-scoped trust state, constructs pre-existing confirmation evidence inside the coordinator, and replays only the exact durable receipt. No product caller drives this checkpoint yet, so the normal product release flow still stops at `trust`;
- a dedicated negotiated authenticated loopback-pool approval capability with prepare and confirm RPC methods. Both bind the authenticated transport user. Prepare publishes exact `release_loopback_pool` authority through the dedicated release-only issuer and narrow state plan source, returning non-secret ticket metadata with an explicit durable or indeterminate publication disposition. Confirm accepts complete ordered absent helper evidence, verifies it against eight fresh native observations through the coordinator, commits both the helper-evidence and owned-absent-observation digests, and advances `loopbacks` to `verify_effects`. Exact receipt replay reuses the committed receipt without reissuing authority or reobserving; Darwin+cgo DI wires the loopback plan source and issuer. Its migration rejects unexplained later state and unsafe rollback;
- daemon-owned effects verification automatically follows loopback-pool confirmation and also runs during recovery at `verify_effects`; it freshly proves the zero-listener runtime anchor, exact retained ownership, absence of the canonical low-port artifacts, absence of Harbor-owned resolver and trust state while preserving foreign and pre-existing state, and absence of all eight loopbacks. The coordinator durably advances `verify_effects` to `ownership` with an effects receipt; it adds no user approval or RPC;
- project route replacement after lifecycle changes.

Important incomplete work:

- Windows NRPT portable tests and cross-compilation pass, and the daemon plus privileged helper now select the reviewed fixed-PowerShell backend. Its opt-in elevated lifecycle test is intended to exercise a fresh rule's observe, CAS add, bounded name-server Set repair, exact verification, and release, but deterministic native precondition drift remains unresolved; native workflow evidence and complete latent-field repair policy remain absent;
- complete release-product trusted-HTTPS proof is absent. The hosted macOS intermediate lifecycle proves the headless current-user trust and cleanup path, but not interactive product consent, concurrency, reboot, installer, or signed-release behavior. After ticket redemption, validation, and replay consumption, the helper's trust branch lazily opens no other effect handler, closes and disarms its root-owned ticket/replay authority, binds the canonical non-root ticket requester to the invoking real UID, and performs one Darwin+cgo `setuid` that verifies both real and effective IDs before constructing only the current-user Security.framework trust adapter. Effect handlers are now selected lazily after replay admission, so an admitted opener, transition, or mutation failure consumes that one-shot ticket and requires fresh capability issuance. Darwin release accepts only an exact owned portable observation, then one native helper rechecks the target certificate's exact Harbor trust shape and ownership marker immediately before certificate-wide removal while preserving native errors. The reviewed Security.framework surface exposes no conditional compare-and-delete primitive here, so this narrows stale windows but is not an atomic CAS. Destructive production release remains gated on the missing product-worker evidence;
- no CLI, desktop, or other product caller drives the loopback approval RPC yet. Durable server-side retry fencing for an indeterminate resolver, trust, or loopback publication remains before an automated caller may retry prepare; ownership release and final projection cleanup/operation retirement remain incomplete, and no native macOS happy-path proof exists. Harbor therefore still cannot complete cleanup end-to-end;
- low-port mechanisms and native-port service relays are not complete product paths;
- the required three-real-project, full-stack acceptance test has not been reached.

Current platform capability is therefore uneven:

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Loopback, host-conflict, process-scope, and local IPC foundations | Implemented with native proof for the exercised paths | Implemented with hosted native proof for exercised paths | Implemented with hosted native proof for exercised paths |
| Resolver backend | Darwin backend integrated and crash-recovery tested; resolver completion now starts the DNS-only Harbor generation | `systemd-resolved` foundation includes cancelable locking plus owned stage/quarantine recovery; its root-only lifecycle and crash-recovery test is required in Linux CI, while broader resolver parity remains | NRPT backend is wired into daemon confirmation and the privileged helper; elevated workflow evidence and complete latent-field repair remain |
| Source helper installation | Automatic Wails development flow | Manual development bootstrap | Not implemented |
| Trusted CA/leaf use | Public material, portable ownership/CAS boundary, and current-user Security.framework adapter with irreversible helper identity transition; real macOS GUI/keychain and native proof remain | Material plus portable ownership/CAS boundary and helper trust protocol; native trust installation absent | Material plus portable ownership/CAS boundary and helper trust protocol; native trust installation absent |
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
- an optional full-Xcode macOS post-build icon catalog with an Icon Composer rendition for macOS 26 and bitmap fallback renditions for earlier releases, while retaining Wails' `.icns` compatibility asset without blocking source development when `actool` is unavailable;
- frontend unit tests, browser fixture tests, Playwright smoke, and native module builds.

Those desktop lifecycle behaviors have unit coverage, but not release-grade native smoke. Tray integration, notifications, native approval/accessibility proof, signed installers, and release-grade platform smoke remain incomplete.

## Delivery status

No phase in `delivery-plan.md` has met its full exit gate.

| Phase | Status |
|---|---|
| Test fleet and evidence | Partial: hosted three-OS CI, bounded evidence, and cross-platform trusted-HTTPS parity pass; interactive product profiles, reboot coverage, and release-signed evidence do not exist. |
| Platform proof | Partial: the hosted three-platform trusted-HTTPS behavior passes, while shipping consent profiles, installer lifecycle, reboot, and broader release evidence remain incomplete. |
| Headless control plane | Partial but substantial: SQLite, authenticated IPC, operations, registration/removal, recovery, and acceptance coverage exist. |
| Network data plane | Partial: servers, planning, setup, activation, and routes exist; full cross-platform host integration is incomplete. |
| GoForj contract | Early/partial: discovery, strict static `forj project:describe --json` validation and session-digest capture, descriptor-constrained framework resource enrichment, readiness-edge and event-driven fenced HTTP endpoint assignment, `forj dev` supervision, direct read-only Docker service observation, daemon-owned service-log streaming, selected service-publication normalization, reserved-resource HTTP route projection, a pure Harbor-owned session/network-fenced managed-publication-to-native-route boundary, a strict transport-neutral managed-session v1 request/response schema, exclusive role-scoped handler routing, exact awaiting-session attachment, capability-gated launch proof and retry, fenced publication replacement authority, exact durable attached-session replay without a generation bump, bounded reconnect/heartbeat republishing for inherited dev sessions, exact live attached-process preservation during daemon recovery, capability-gated runtime-plan production and HTTP overlay consumption, additive stable generated-task IDs and typed lifecycle metadata, and managed admission validation that preserves exact generated legacy tasks while rejecting ambiguous custom lifecycle tasks and cross-scope phase assignments before session registration. Harbor still lacks process/action event delivery and native macOS local-Engine evidence; broker output re-adoption is implemented for Linux/macOS with unsupported platforms failing closed. |
| Desktop experience | Partial: the working Wails/Vue client covers the main development slice; interactive tray verification, signing, and several approvals remain. |
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

The main CI workflow runs root, control, loopback, frontend, and desktop checks on Ubuntu 24.04, macOS 14, and Windows 2022 as applicable, with Node 22 for the frontend. The native macOS desktop build uses macOS 26 and verifies Xcode 26 because its post-build hook compiles an Icon Composer document; the headless macOS jobs retain macOS 14 coverage. The separate hosted platform-network workflow uses Ubuntu 24.04, macOS 15, and Windows 2022. Generated-project and hosted lifecycle jobs build GoForj at pinned release `v0.30.0`, queue and launch all three rendered projects before awaiting readiness, and require them to retain port 3000 concurrently through `internal/testkit/goforjproject`, followed by cleanup/evidence checks. This CI source pin is not a runtime admission requirement. At Harbor commit `e0c9773`, the complete lifecycle passed on hosted Ubuntu (`30166963369`), macOS (`30166963384`), and Windows (`30166963379`); CI (`30166963374`) and platform-network smoke (`30166963368`) also passed.

The separate hosted platform-network workflow is pre-provisioned API proof. The hosted lifecycle workflows add resolver installation, trusted TLS, and the three-project behavioral gate, but still do not prove the shipping consent flow, Compose on every platform, reboot recovery, or release support. Those stronger claims require the product environments described in [Cross-platform testing](./testing.md).
The managed-session event transport now has an authenticated, bounded opt-in sink; normal launch remains capability-off and no durable event projection or producer hook is enabled yet.
Managed Compose route heartbeats now stop after two consecutive bounded barrier failures (a successful refresh resets the budget), so an unavailable Harbor route cannot leave a live GoForj watcher issuing IPC requests forever.

Managed starts now use resolver-stage DNS for observed host-visible native TCP service publications. An already-attached managed barrier repairs the exact durable TCP reservation before joining its fresh private upstream, while shared HTTP/HTTPS resources remain full-stage-only until low-port and current-user trust proof exists. Optional managed runtime-plan assignments are not part of Harbor's lifecycle contract; absent service-environment metadata remains valid and Harbor does not require the unpublished sibling experiments that were removed from GoForj. Resource-specific DSNs, URLs, credentials, and inferred environment shapes remain intentionally unsupported.

The shared Redis driver now follows the same contract across cache, queue, events, and storage: descriptors emit only `REDIS_HOST` and `REDIS_PORT`, and Harbor fills them from the observed project-private publication rather than the Compose-internal hostname.

Credential-free address consumers now use the same boundary: Memcached emits `CACHE_ADDRESSES` and Kafka emits `EVENTS_BROKERS`, both materialized as the exact project-private `host:port` value. URL/DSN-shaped drivers remain withheld until their scheme, database, and credential semantics can be represented without guessing.

The focused Harbor and GoForj unit and race checks for this slice pass. Full GoForj package validation still depends on a local `templates/demo/frontend/node_modules` fixture and a native Docker environment; cross-compilation or a remote Docker endpoint is not macOS product evidence.

## Immediate native-service DNS readiness

Project startup now stages observed native service routes while the durable project is still `Starting`. Harbor probes direct service sockets, publishes names such as `mysql.<project>.test`, and only then commits the project as `Ready`. Route publication failure fails startup instead of exposing a ready project whose service name can first answer `NXDOMAIN`; if the final lifecycle commit fails, Harbor withdraws the staged project routes.

Focused controller, lifecycle, native-route, and DNS tests cover the ordering and the starting-state publication authority. The remaining native check is to restart a real macOS project and query its native service name immediately after Harbor reports `Ready`, without flushing `mDNSResponder`.
