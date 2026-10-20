# Development Handoff

Status: PR #12 merged; installed GUI gate exposed a readiness timeout bug, with a focused fix ready for fresh packaging

## Startup and native-gate continuation (2026-10-19)

Harbor PR #13 is https://github.com/goforj/harbor/pull/13 on `test/installed-package-ui`, worktree `/tmp/harbor-package-ui-20261019`. Initial GUI harness input issues are corrected: the test runner has its own unsandboxed entitlements, WebKit controls use native keyboard navigation with the worker preference restored afterward, checkboxes accept numeric accessibility values, native select menus use keyboard selection, and close-to-hide uses the actual close button. Product source comparison reads Git objects without checking out artifact-selected code; shared Go cache writes are disabled. CodeQL marks the original cache-poisoning alert fixed.

[GUI run 39360249869](https://github.com/goforj/harbor/actions/runs/39360249869) used the exact native-tested package from run 39354344032, completed secure networking and graphical project creation, then failed Start. Its retained snapshot reports `project.readiness.invalid` and `context deadline exceeded`. This confirmed that one HTTP probe deadline incorrectly terminated startup. The four-line coordinator fix preserves the overall startup timer and retries only deadline errors while the daemon context remains active. New regression cases fail before and pass after the change. Logs are `/tmp/harbor-readiness-attempt-{before,after,race}.log`; downloaded native results are under `/tmp/harbor-installed-ui-results-39360249869` and the earlier screenshot-rich run under `/tmp/harbor-installed-ui-results-39358917330`.

The Windows capacity gate also intermittently fails at native assignment, most recently in run 39360245273. The fixture now reports the wrapped cause and JSON observation, and its Windows-only capacity invocation repeats ten times to expose the failure. Do not call this fixed or weaken native ownership checks without those facts.

Focused lifecycle tests, all four readiness policy cases under the race detector, bounded capacity-range validation, and workflow YAML/Bash syntax pass locally. One next goal: rebuild the native package rehearsal on the committed fix, then rerun the GUI gate against those new package bytes and resolve the remaining failures. Its automatic first attempt can reject the stale package source before a new rehearsal completes; rerun it after the new successful package run exists. Preserve all active user installations. These rehearsals do not publish a Harbor release/tag or install over the developer's machine.

## Installed UI continuation (2026-10-19)

PR #12 merged as `74adee22d2be81ee3699fa245b7269f4d6c49733` after all 41 checks passed. Build-only native package run [39354344032](https://github.com/goforj/harbor/actions/runs/39354344032) passed at `a689e093ce3c8906f15344fd7b062f2b324bff3b` with released GoForj v0.34.1. Both packages retained complete uninstall receipts, preserved user database/project files, and passed installed daemon activation, reopen/restart, and trusted HTTPS. Publication was skipped.

The user has no disposable personal Mac and requested one-off GitHub-worker installation and critical-workflow tests. `/tmp/harbor-package-ui-20261019`, branch `test/installed-package-ui`, adds an XCTest host/harness, a manually dispatchable and paths-scoped PR workflow, package/source verification, and release documentation. The original harness used unchanged product bytes; the readiness fix above now requires fresh packaging. The harness launches `/Applications/Harbor.app` and operates its real accessibility controls. No source-development installation is used or restarted. Local native compilation, package digest/provenance verification, verifier tests, and workflow syntax pass; hosted progress and remaining failures are recorded above.

One next goal: run the installed GUI workflow, correct concrete failures, retain its native screenshots/results and uninstall receipt, and record the honest coverage boundary. Preauthorized CI privileges do not prove consent prompts; this first minimal Vue/API project does not prove Docker volume preservation, tray interactions, reboot, signed updates, or Linux/Windows GUI behavior. Preserve all active user checkouts and projects.

Last updated: 2026-10-19

## CLI pin integration continuation (2026-10-19)

The user authorized merging Harbor PR #12, updating its packaged/CI CLI pins, and verifying the clean installed graphical lifecycle. Work remains isolated in `/tmp/harbor-installed-daemon-20261019` on `fix/installed-daemon-lifecycle`; the active source-development checkout and running projects are preserved.

All executable CLI source pins and expected acceptance versions now select GoForj v0.34.1. Existing v0.34.0 evidence below is historical and is not rewritten. Root/desktop framework module dependencies and the desktop relative replacement remain unchanged. All nine workflow files parse, all fourteen executable CLI pins and version expectations align, the three-project MySQL render check passes with the published CLI, and focused macOS release-adapter checks pass. Refreshed PR checks and a new native package rehearsal are pending. Installed GUI proof requires an actual disposable interactive session; the headless daemon adapter gate does not establish it, and package installation/removal must not replace the user's active source-development installation.

## Installed daemon implementation continuation (2026-10-19)

The accepted next slice is isolated in `/tmp/harbor-installed-daemon-20261019`, branch `fix/installed-daemon-lifecycle`, based on merged main plus the receipt correction from local `c4fb533`. The active `/Users/cmiles/code/harbor-wails-v3` checkout is preserved until integration. No project restart or privileged change on the developer's machine is required.

Linux release activation admits the exact root-owned packaged `harbord.service`, rejects shadow units and drop-ins, and starts it through the existing user manager without enabling linger. The unit applies embedded migrations before foreground startup. macOS retains its canonical LaunchAgent bytes; the stable launcher's no-argument path now runs migrations and selects `--foreground`, while explicit maintenance arguments remain unchanged. The old no-argument launcher exited without running the daemon, and the package test previously hid fresh-database startup by running migrations explicitly.

Final preparation uses a small Unix launcher on both platforms and holds the daemon's existing singleton lock while its selected `harbord migrate` runs. An active daemon is preserved without migration or a replacement Start. The shared lock filename remains `harbord.lock`; no persisted path or configuration changes. Root-owned executable admission excludes links, mutable files, and set-ID modes. The unit remains static rather than silently enabling linger or login autostart.

macOS uninstall now checks the OS-resolved user's saved agent and loaded job before bootout, confirms absence, and uses no-follow descriptors plus atomic retirement/inode revalidation to remove only the admitted file. Ambiguous files, native observations, and foreign service definitions stop product removal. Focused regression tests cover replacement, symlink/hardlink, owner/mode, service drift, and native failures. The canonical definition lives in `internal/platform/daemonservice` for desktop/installer agreement.

Both package workflows now run `TestInstalledDaemonLifecycle` through the production release adapter on a fresh user database, retain a stopped registration across client and daemon reopen, and verify user database/project-file hashes after package removal. The test leaves its explicitly selected disposable files for uninstall verification. It proves the daemon adapter used by the desktop, not a Wails window/tray or reboot. Linux test-worker user-manager startup is explicit and does not enable linger.

Local evidence: `/tmp/harbor-installed-daemon-root.log`, `/tmp/harbor-installed-daemon-desktop.log`, `/tmp/harbor-installed-daemon-linux-build.log`, `/tmp/harbor-installed-daemon-guard.log`, `/tmp/harbor-installed-daemon-admission.log`, and `/tmp/harbor-installed-daemon-race.log`. Final [build-only run 39343779111](https://github.com/goforj/harbor/actions/runs/39343779111) passed on product source `86e2121941facd864e0dab819ec5275c7f33ce26`, including the singleton guard. Native macOS and Ubuntu both proved fresh-database migration, client independence, reopen, daemon restart, retained registration, and data-preserving service/package removal. Downloaded evidence is under `/tmp/harbor-installed-daemon-final-39343779111`. Assembly passed and publish was skipped. No Harbor release or tag was created.

PR #12's Linux root job failed because `TestRuntimeReplaceNativeRoutesPublishesAndWithdrawsManagedRelays` independently allocated a native listener after releasing upstream reservations, allowing endpoint reuse. All public TCP endpoints are now reserved together before selecting distinct upstreams. The focused test passes 100 repetitions and the data-plane package passes. The follow-up changes tests and evidence documentation only; packaged product source remains identical to the native proof. The current product-head platform jobs pass on all three operating systems. Fresh PR CI after the fixture correction remains required. Keep the historical rehearsal below distinct from this implementation.

The next CI run exposed two more fixture races: a topology-admission test's fixed upstream ports could collide with its ephemeral public listeners, and a service-log retry test read before its pipe's consumed bytes had reached the transcript. The topology fixture now selects distinct upstreams, and the log test waits through the public cursor API before checking the full transcript. The two route cases pass 100 repetitions, the log case passes 20 repetitions and five race-enabled repetitions. Product routing and log behavior are unchanged.

## Package rehearsal continuation (2026-10-19)

[Build-only run 39339451091](https://github.com/goforj/harbor/actions/runs/39339451091) passed on merged main `03e6c52759f65178cdcfb0bc103ac5e6cedb2a7a`. Both native packages passed installation, three-project trusted HTTPS using released GoForj v0.34.0, ownership-aware host cleanup, and uninstall. Assembly retained the tested bytes, checksums, provenance, and preview notes; publication was skipped. No Harbor tag/release or installation over the developer's active machine was performed.

Evidence and package downloads are under `/tmp/harbor-package-rehearsal-39339451091`. Local logs are `/tmp/harbor-beta-package-{macos,ubuntu}.log`, `/tmp/harbor-beta-main-{macos,linux}.log`, `/tmp/harbor-beta-installer-focused.log`, and `/tmp/harbor-beta-installed-startup.log`. Exact-main CI ran privileged bootstrap repair/idempotency and runtime-data preservation on both Unix platforms. This is fixture repair evidence, not full installed repair with active Docker projects.

The macOS evidence artifact contained only `summary.json`; the uninstall receipt used an environment variable scoped to an earlier step. The original job log confirms a failed write to `/package-uninstall.txt` despite the green step. `.github/workflows/macos-development-package.yml` now writes the receipt through `GITHUB_WORKSPACE`, enables strict unset-variable handling, and explicitly rejects write failure. Focused shell syntax, successful isolated writing with the earlier variable absent, and rejection of an unavailable destination pass. The hosted run predates that correction, so do not claim its missing receipt was uploaded. The original job log records the native uninstall assertions. Rerun the corrected native workflow before publishing.

The active `feat/terminal-appearance` checkout initially matched merged main by tree. This continuation changes only that workflow plus `docs/current-state.md`, `docs/handoff.md`, and `docs/releases.md`; preserve any later unrelated changes. The two Go modules and frontend implementation are unchanged.

One next goal: close the installed daemon lifecycle. Linux's desktop daemon activation adapter is unavailable, so a clean installed UI cannot start its daemon. macOS first launch creates a per-user LaunchAgent that the system-path uninstaller does not remove. Add native installed open-app/reopen/cleanup proof on disposable machines while preserving user data, checkouts, and foreign service definitions. Actual Docker-volume preservation, genuine interactive consent, reboot, and signed updates remain unproved; successful headless package jobs do not substitute for those gates.

## Final Harbor integration (2026-10-19)

Harbor PR #11 merged as `03e6c52759f65178cdcfb0bc103ac5e6cedb2a7a` after 41 successful checks. Its complete scope includes graphical creation, resilient 256-identity networking, resumable setup, appearance, command-menu parity, and service artwork. Push CI, CodeQL, platform-network API smoke, and all three hosted trusted-HTTPS workflows passed for that exact main commit. GoForj PR #251 is merged, released as v0.34.0 at `4553ef30`, and installed at `/Users/cmiles/go/bin/forj` from the published module. Its schema matches Harbor's fixture. All external CLI source pins and expected generated-project versions are moved to 0.34.0; the relative desktop module replacement and framework dependency pins are unchanged.

The native capacity step passed on hosted Linux, macOS, and Windows in run `39333621961` at `79e441d`. General CI on that head exposed two remaining test issues: the pure `internal/network/poolpolicy` dependency was not in the helper's reviewed graph, and the desktop creator fixture used a Unix-only absolute path. The focused fixes pass locally in `/tmp/harbor-integration-helper-boundary.log` and `/tmp/harbor-integration-creation-tests.log`, and are included in the merged commit. A subsequent Linux generated-project run found a stale service-change wake after normal process exit. The watcher now treats only typed runtime-not-running as terminal, matching its wait boundary, while retaining genuine observation errors. A deterministic wake-then-exit regression and focused race checks cover it.

A real macOS native lifecycle proof passed in `/tmp/harbor-integration-native-create-start-stop.log` and `/tmp/harbor-integration-native-reopen-start-stop.log`: GUI creation through the released CLI, Ready, trusted HTTPS 200, Stop, native process exit, a fresh process finding the same stopped registration, another GUI Start, trusted HTTPS 200, and Stop. It used the production SPA and real Wails bindings against the existing daemon, with a minimal Vue/API project. The isolated probe checkout is recorded in `/tmp/harbor-integration-verification-directory`; its test-only probe is not a product change. Generated project files remain beneath `/tmp/harbor-lifecycle-verify-cbk_d1ye/project`. Fixture state, installer consent, daemon restart, and OS reboot are not substituted for this evidence.

The disposable proof registration was removed through the ordinary native project-removal approval flow, verified in `/tmp/harbor-integration-cleanup.log`. All generated files were preserved. No direct database write or host-state bypass was used. The watcher regression/race evidence is `/tmp/harbor-integration-teardown-race.log`.

The earlier continuation sections are historical. In particular, their pending-publication and uncommitted-work notes are superseded by this section and the current Git state.

## Publication and native gate continuation (2026-10-19)

GoForj's schema/argument creation contract is committed as `dab970eb` in `/Users/cmiles/code/goforj-harbor-events` and published for review at https://github.com/goforj/goforj/pull/251. Focused project-creation tests pass in `/tmp/goforj-creation-contract-final.log`. The built CLI emits one schema JSON object, and every field matches Harbor's committed fixture. The contract is now merged and published as v0.34.0; see the final integration section above.

The hosted platform-network workflow adds a `/24` adapter acceptance case with partial assignment replay, retained fingerprints, 100 simultaneous same-port listeners, and exact cleanup. It deliberately uses `127.77.253.0/24`, separate from the existing eight-address and three-project proof ranges, and refuses any pre-existing assignment before mutation. Portable proof and range-bound tests pass in `/tmp/harbor-native-capacity-{portable,bounds}.log`. The native capacity step passed on all three hosted platforms in run `39333621961`. Installer consent, protected ownership growth, and reboot acceptance remain separate.

## Interrupted setup continuation (2026-10-19)

The accepted next slice adds durable loopback setup recovery. Work was isolated in `/tmp/harbor-setup-recovery-20261019` on `fix/setup-recovery`, seeded from the user's full active dirty checkout. `/tmp/harbor-setup-recovery-baseline.json` records source hashes so integration copies only this task's changes and preserves unrelated appearance, creator, icon, and capacity work. No source checkout was reset or stashed.

Recovery adds optional snapshot pending metadata, the negotiated `control.network-setup-recovery.v1` capability, `setup --resume`, desktop `ResumeNetworkSetup`, and a shared Resume card in Overview and Settings, Daemon. The embedded `2026_10_19_020000_create_network_setup_confirmation_receipts` migration stores one bounded receipt against the exact operation and approval revision. Independently verified effects persist before final completion, survive a failed transaction and reopen, and replay without preparing or launching another loopback helper. Fresh readback remains mandatory. Proven host drift retires only that exact pending receipt and restores approval; transient or invalid observations preserve evidence and fail closed. Initial secure setup continues through existing resolver/trust/ingress approvals.

Evidence: `/tmp/harbor-setup-recovery-validation.log` (affected root suites), `/tmp/harbor-setup-recovery-drift.log` (persistence, rollback, and native-readback failures), `/tmp/harbor-setup-recovery-final-core.log`, `/tmp/harbor-setup-recovery-capability.log`, `/tmp/harbor-setup-recovery-final-race.log`, `/tmp/harbor-setup-recovery-final-vet.log`, `/tmp/harbor-setup-recovery-native-client.log` (nested desktop selection/contracts), `/tmp/harbor-setup-recovery-ui-new.log` (89 focused store/mock checks), `/tmp/harbor-setup-recovery-ui-build.log`, and `/tmp/harbor-setup-recovery-browser.log`. Browser screenshots are `/tmp/harbor-setup-recovery-{light,dark,mobile}.png`. Fixture generation ran twice without drift. No new native host mutation was required for this slice.

Integration is complete in `/Users/cmiles/code/harbor-wails-v3` on `feat/terminal-appearance`. The daemon applied the embedded migration normally during its development reload. Fresh readback in `/tmp/harbor-setup-recovery-final-live.json` reports 256 identities, no pending setup, Test2 Ready, and the other two projects stopped. Normal-trust `https://test2.test` returns 200. `/tmp/harbor-setup-recovery-active-desktop.log` confirms the active checkout's native binding and generated contract checks. No privileged setup rerun, alias removal, or direct live database write was used.

Accumulated source work is now checkpointed in three local commits: `8f52dd7` (capacity and address lifecycle), `bb6516f` (guided creation and workspace presentation), and `87be96c` (durable setup recovery). The following documentation checkpoint records the remaining local workflow changes. No Harbor PR, push, tag, or release was created for these checkpoints. Historical references below to uncommitted Harbor work describe the state at those earlier stopping points. The separate GoForj contract checkout still needs publication.

The pending receipt covers loopback effects only. A helper result lost before daemon retention still takes the ordinary idempotent approval path. Linux/Windows native consent, installer/reboot acceptance, and GoForj contract publication remain separate release work. Keep those claims distinct from portable and browser proof.

## Project capacity continuation (2026-10-19)

The user's 100-project request is implemented in the active dirty `/Users/cmiles/code/harbor-wails-v3` checkout and the isolated `fix/address-lifecycle` worktree `/tmp/harbor-address-lifecycle-20261019`, based on `2111d38`. The active checkout retains additional appearance, creator-schema, icon, networking-prompt, and help-preview work. Preserve it and the dirty GoForj checkout `/Users/cmiles/code/goforj-harbor-events`. Integration used three-way source merges, and active generated fixtures were regenerated rather than copied. Those changes are now checkpointed in `8f52dd7`; no PR was opened.

Capacity policy is `internal/network/poolpolicy`: a full `/24`, 256 identities, with `/29` compatibility. Explicit growth changes only the prefix to the containing `/24`. Ownership CAS, signed PreviousPool and exact source fingerprint, native preconditions, resumable helper mutation, aggregate protocol bounds, setup staging/completion, the embedded `2026_10_19_010000_expand_loopback_capacity` migration, and complete-pool release all move together. Existing leases and policy stay unchanged. The UI upgrade lives in Settings, Daemon; CLI uses `setup --expand-pool` and resumes a pending intent.

The first native upgrade exposed a real integration gap: unrelated daemon updates advanced the global sequence during consent. The old completion proof required its running edge to immediately follow approval. The corrected state result and control receipt carry the exact approved journal revision, distinguish expansion from ordinary repair, and allow unrelated prior updates without relaxing the contiguous completion transaction. A copied-state diagnostic reproduced the native failure and passed with the correction; that temporary diagnostic is removed from source. The durable regression registers an unrelated project during approval. Retrying resumes the original operation rather than creating competing authority.

Evidence: `/tmp/harbor-capacity-validation.log`, `/tmp/harbor-capacity-race.log`, `/tmp/harbor-capacity-interleaved-final.log`, `/tmp/harbor-capacity-active-final-go.log`, `/tmp/harbor-capacity-active-final-vet.log`, `/tmp/harbor-capacity-active-ui-build.log`, `/tmp/harbor-capacity-active-ui-tests.log`, `/tmp/harbor-capacity-active-final-desktop.log`, and `/tmp/harbor-capacity-browser.log`. The browser proof covers expansion from 8 to 256 and hides the completed upgrade action. The native update logs are `/tmp/harbor-capacity-native-upgrade.log` and `/tmp/harbor-capacity-native-upgrade-retry.log`. Source backups and merge manifests are under `/tmp/harbor-capacity-preapply` and `/tmp/harbor-capacity-pre-confirmation-fix`.

Native expansion is complete. The first approved helper execution widened protected ownership and ensured all 256 aliases; its daemon confirmation exposed the sequence-gap bug and rolled back safely. After applying the fix, the CLI retry selected the same pending intent, but its extra consent ticket expired. The already-approved, already-applied effects were freshly read through the native loopback adapter and confirmed through the ordinary authenticated daemon API for the retained operation. No privileged bypass, direct live SQLite write, lease rewrite, or alias deletion was used. The temporary confirmation client source and copied diagnostic database were removed.

Final evidence: `/tmp/harbor-capacity-native-confirm.log` records approval 4845, network commit 4852, and terminal operation 4853 for `operation-bed2a84d8fe4a3f3b3f07c3b78228257`. `/tmp/harbor-capacity-native-bind.json` records 100 simultaneous same-port listeners and 100 accepted connections, with all retained lease records unchanged. `/tmp/harbor-capacity-final-snapshot.json` reports 256 identities in `127.77.59.0/24`, Test2 Ready, and both other projects stopped. Test2 retained `127.77.59.73`, lease generation 4, ownership generation 1; a normal-trust HTTPS probe returned 200. `/tmp/harbor-capacity-final-race.log` covers the final interleaved upgrade and 100-project allocation tests. Re-observe these values before further action. The new growth and reboot acceptance gates on Linux and Windows remain separate from portable tests.

Next goal: carry the same expansion, unrelated-update-during-consent, retry, and cleanup cases into native cross-platform release acceptance. No 100-Compose-stack resource benchmark or reboot was run.

## Command menu parity (2026-10-18)

Visual follow-up: command actions, navigation, and theme choices now use the existing semantic icon colors with tinted badges. Shared icon glyphs explicitly inherit their badge color so command-item defaults cannot mute them. Vendor artwork and status colors retain their roles. The frontend build and focused keyboard/settings browser checks pass; light and dark previews were inspected.

The command menu now reuses the shared service logos, project identity, resource favicons, and settings navigation. Actions include New project, Add existing project, Refresh, direct Appearance/Daemon links, and app theme choices. Creation uses the same dialog as Overview and the sidebar. Resource search includes all current resources, with recent items grouped separately and no duplicates. Search metadata updates when snapshot entries change, and reopening clears the prior query. Connection-sensitive actions remain disabled when disconnected or stale. The frontend production build, two command primitive unit tests, and four focused Chromium checks pass, including keyboard focus, service branding, non-recent resources, shared creation, and settings navigation. Browser previews were inspected. No daemon or project restart was needed.

## Service identity logos (2026-10-18)

Service cards, the service browser, service headers, and per-project service tabs now use a bundled offline vendor-logo catalog. Exact service IDs/names select logos for the current name-only snapshot; the resolver also accepts complete image repositories, tags, and digests for a future image metadata projection. An explicit unknown/private image overrides name guessing and retains the generic semantic icon. Grafana seed helpers share Grafana artwork; VictoriaMetrics helpers share its vendor mark. No Docker observation, daemon contract, dependency, or lifecycle behavior changes in this presentation slice. Asset sources and licenses are recorded beside the SVGs. The frontend build, eight focused lookup tests, and two existing service browser checks pass, with light/dark visual inspection and card/tab interaction proof.

## Network setup prompt (2026-10-18)

The Overview setup prompt now consumes the daemon's retained networking configuration, not just its ability to run setup. The snapshot adds optional `network_setup.configured`, projected from the current durable full networking stage, independently of bounded operation history. Missing metadata from an older daemon is unknown rather than an instruction to set up networking; explicit setup errors retain their recovery path. Completed setup removes the onboarding card, and subsequently removed configuration can show it again. This adds no migration or host mutation. Focused domain/authority tests, 77 store/view tests, the setup browser flow, the frontend build, and repeat-generation parity pass. Other pending appearance and project-creation work remains local and uncommitted.

## Project creation continuation (2026-10-18)

Help-format preview follow-up (2026-10-19): optional option-level `preview` text is produced by the existing real GoForj help formatters, passed through the stack-neutral schema, and displayed by `CreationOptionPreview.vue`. Switching formats replaces the output, and choices without metadata show no preview. The installed local CLI was refreshed with the previous binary retained at the path recorded in `/tmp/harbor-forj-help-preview-backup-path`. Focused producer/adapter/wire checks, two creation/preview browser flows, the frontend build, and repeat-generation parity pass. Light/dark and narrow previews were inspected. No project was created or restarted for this follow-up.

Startup disclosure follow-up: expansion is explicitly controlled by Vue, and collapsed rows are removed from the DOM. Completing startup resets an expanded output view to its compact summary. Six focused component tests, the existing startup browser flow, and the frontend build pass. No backend changes or project restart are required.

Latest visual follow-up: the wizard uses the current GoForj iOS logo, a branded stack card, connected step indicators, and clearer input treatment. Development output now defaults to a compact current-step summary; Overview retains the full timeline, and failures expand automatically. The frontend production build, five focused timeline unit tests, and the two relevant browser flows pass. Visual previews were inspected in light/dark and narrow layouts. No backend rebuild or broad validation is needed for this presentation-only change.

Follow-up completed locally: the shared wizard uses a creator registry and stack-neutral fields; GoForj is the first adapter. Creation now streams bounded cursor-based output, retains failed files, and separates registration retry from rendering. Custom Atlas agents and integrations are available. The local CLI was refreshed in `~/go/bin/forj`, preserving the original released-binary backup. Focused adapter and desktop race checks, desktop/binding/fixture tests, 15 frontend unit checks, 3 creation browser flows, the frontend production build, and repeat-generation parity pass. A fresh real native production-SPA creation also passed at `/tmp/harbor-progress-native-w5y2sns6/vue-project`, then removed only its stopped proof registration. The native log is `/tmp/harbor-creation-progress-native.log`. No new PR, commit, or release was created. The next step is user review of the working local wizard; do not repeat broad validation for visual iteration.

The active Harbor checkout remains `harbor-wails-v3`; preserve its earlier appearance edits. The GoForj changes are isolated in `goforj-harbor-events` on `feat/project-creation-contract`, preserving the dirty main GoForj checkout. The user requested a working local CLI and frontend validation before a PR. No new PR has been opened.

`forj new --schema` and `--no-interaction` share the existing catalogs and creation path. Harbor's wizard consumes that schema, uses bounded verified CLI execution, and registers only completed output through the daemon. The explicit native creation smoke mode uses the real daemon and local CLI, creates only a `/tmp` checkout, removes its own stopped registration, and keeps the generated files for build validation. Custom Atlas choices are now exposed through conditional schema fields. The follow-up adds a stack-neutral creator registry, live cursor-based creation output, and retained-job registration retry. GoForj is the first adapter; other stack lifecycles remain future work.

The native production-SPA proof passed, as did real generated CLI and Vue Go builds and the generated Vue frontend build. The local CLI is installed as `v0.33.1-local.project-creation` in `~/go/bin/forj`; its released predecessor is backed up at the path recorded in `/tmp/harbor-forj-creation-backup-path`. The existing Diablo Immortal Tracker project was observed Stopped before and after installation and was left in that state. No new PR has been opened. The final installed-binary proof passed through the production Wails UI and real daemon. All 29 Chromium checks, 259 frontend unit tests, both frontend production builds (Harbor and the generated Vue project), the desktop Go suite, focused GoForj authoring tests/vet, and creation adapter race checks pass. Contract generation is stable on repeat. The next step is the user's review of Overview → New project.

## Terminal appearance continuation (2026-10-18)

The user requested a terminal appearance setting and a GoForj-style light option. The active checkout is `harbor-wails-v3` on `feat/terminal-appearance`. The bottom rail cog now opens Settings → Appearance, grouping app themes and the terminal preview with System/Dark/Light choices. Daemon diagnostics have a separate section. Legacy `/system` links redirect to `/settings/daemon`, and local appearance works before the first snapshot. Preferences are local; the daemon and project state do not change. CSS-variable ANSI palettes recolor retained text, while xterm updates its palette without opening another shell. The application theme menu now uses a direct menu trigger to keep its popup within the viewport. Preserve the running Diablo Immortal Tracker project. The production build, 259 unit tests, and 28 Chromium checks pass, including persisted light/dark choices and independent app-theme changes. Dark/light preview cards were inspected. The user prefers direct local iteration for small frontend changes, focused checks, and no automatic PR per change. The latest palette refinement uses neutral near-black (`#0B0B0B`) and charcoal chrome (`#171717`) to remove the purple cast that conflicted with Harbor. The follow-up ANSI palette increases color saturation for token visibility while keeping ordinary text, timestamps, and surfaces neutral.

## Development output continuation (2026-10-18)

The active user checkout is `harbor-wails-v3` on `fix/development-output`. Semantic icons are merged through PR #9 and present locally. The output refinement removes excess terminal leading, centralizes the neutral surface/soft ANSI palette, and offers an embedded-presentation selector alongside private startup diagnostics. GoForj PR #249 consumes that independent selector and omits only the redundant resource inventory. GoForj PR #249 is merged and released as v0.33.1. The user explicitly approved updating the installed CLI and restarting Diablo Immortal Tracker. Harbor stopped it before the atomic CLI replacement, then started a fresh session: Ready, five resources, normal-trust HTTPS 200, no legacy readiness URL inventory, and 16 private startup events ending in completed. The previous CLI backup path is recorded locally in `/tmp/harbor-forj-output-fix-backup-path`. The active project is running; do not stop it for further visual checks. Harbor PR #10 is merged as `47f17ba`; continue with the appearance settings slice before moving to the remaining native interaction gates.

## Semantic icon continuation (2026-10-18)

`feat/semantic-ui-icons` adds shared project identity and semantic tab, resource, and service icons. Offline projects use the bundled current GoForj iOS asset without favicon calls; online projects can use their primary App favicon with the same local fallback. The frontend build, 248 unit tests, and 26 Chromium checks pass, with light/dark desktop and 430-pixel browser inspection. No daemon, lifecycle, or dependency changes belong to this slice. The isolated browser fixture on `127.0.0.1:39519` contains fake projects only.

## Prerelease pipeline continuation

`feat/prerelease-artifacts` adds beta/RC tag publication from tested main commits, reusable native macOS/Ubuntu package gates, strict asset verification, build-only dispatch, and public preview limitations. Windows publication is intentionally gated until production signing is configured; its disposable certificate installer remains manual CI evidence only. PR #7 is merged. The complete build-only rehearsal passed in GitHub Actions run `39178991761`, including both native package gates and asset assembly; publication was correctly skipped. No Harbor release tag has been created and no public artifact publication is claimed. See `docs/releases.md` for operation and retry behavior.
## Startup diagnostics continuation

`feat/startup-diagnostics` contains the additive startup file consumer, control activity metadata, Vue timeline, and read-only Docker publication conflict diagnostics. Companion GoForj branch `feat/dev-progress-events` emits v1 startup records and advertises its capability. The installed global CLI remains unchanged and older CLIs use raw output. No lifecycle or readiness authority is inferred from diagnostics. Generated wire fixtures must move with their Go source. Native UI automation is disabled here; tray/dialog/consent/onboarding verification is not claimed by a fake-daemon smoke. Harbor PR #8 and GoForj PR #248 are merged. The producer is published in GoForj v0.33.0; the published CLI was installed into an isolated temporary directory and verified to emit failure events with a private file mode. The global installed CLI was not changed.


The opening implementation notes below are retained as historical context. The authoritative continuation and evidence boundary begins at [Read this first](#read-this-first).

The latest trust foundation separates setup `ensure_trust` issuance from global-release `release_trust` authority. It now supplies the owned-only release plan source, disposition-aware durable receipt, `trust`-to-`loopbacks` advance rail, coordinator, narrow Darwin+cgo issuer wiring, and an independently negotiated authenticated authority/control/RPC surface. Owned preparation can publish one exact `release_trust` ticket and reports durable or indeterminate publication explicitly; a staged pre-existing unowned root instead returns a tagged no-ticket preparation and never opens the issuer. The pre-existing confirmation carries JSON `null` rather than fabricated helper evidence. Confirmation independently re-observes the requester-scoped native state. The owned path correlates helper `owned_absent` evidence, while the pre-existing path accepts no helper evidence, requires the exact staged observation fingerprint, and constructs its canonical receipt evidence inside the coordinator. Exact replay retains the original verification time and receipt. The helper routes a trust operation only after ticket redemption, validation, and replay consumption; that branch lazily opens no other effect handler, closes and disarms root-owned ticket/replay authority, binds the canonical non-root ticket requester to the invoking real UID, performs one irreversible Darwin+cgo `setuid` that verifies real and effective IDs, then constructs and uses only the current-user Security.framework trust adapter. Effect handlers are selected lazily after replay admission, so an admitted opener, transition, or mutation failure consumes that one-shot ticket and requires fresh capability issuance. Darwin release now requires exact owned portable facts and performs target-settings plus marker revalidation in the native removal helper; the reviewed Security.framework surface provides no conditional compare-and-delete primitive, so the check is intentionally documented as optimistic rather than atomic. Real macOS GUI/current-user keychain install, release, default-trust, concurrency, and cleanup proof remains absent, and destructive production release stays gated on that evidence. The prior Darwin+cgo low-port and resolver approval surfaces remain as described: an uncertain resolver or trust publication can return its sole reference with an indeterminate disposition, and durable server-side retry fencing is still required before an automated caller retries prepare.

The loopback-pool release unit now has its own negotiated authenticated approval capability and prepare/confirm RPC methods. Both methods bind the authenticated transport user. Prepare publishes exact `release_loopback_pool` authority through its dedicated release-only issuer and narrow state plan source, returning durable or indeterminate non-secret ticket metadata; before publication, the issuer revalidates the active global checkpoint, exact ownership, pinned key, and all eight retained exact loopback observations twice, and an indeterminate result yields only the sole reconcilable reference. Confirm accepts complete ordered absent helper evidence against eight fresh native observations through the coordinator, commits the helper-evidence and independent owned-absent-observation digests, and advances `loopbacks` to `verify_effects`. Exact receipt replay returns the committed receipt without reissuing authority or repeating the destructive confirmation observation. Darwin+cgo DI wires the loopback plan source and issuer. The migration rejects unexplained later state and unsafe rollback. Harbor then automatically performs a separate fresh daemon-owned verification, including during recovery at `verify_effects`: it proves the zero-listener runtime anchor, exact retained ownership, absence of the canonical low-port artifacts, absence of Harbor-owned resolver and trust state without removing foreign or pre-existing state, and absence of all eight loopbacks. The coordinator records an effects receipt and advances `verify_effects` to `ownership`; this adds no user approval or RPC. No CLI, desktop, or other product caller drives this checkpoint, and no native macOS happy-path proof exists, so Harbor still cannot complete cleanup end-to-end; ownership release, final projection cleanup/operation retirement, and the three-project trusted-HTTPS gate remain open.

The root and nested desktop Go modules pass their complete tests and vet checks, and focused authority/control race tests pass. Independent correctness and security reviews found no control-surface blocker; this remains portable evidence, not a real macOS trust-store exercise. The coordinator owns the durable `loopbacks`-to-`verify_effects` and `verify_effects`-to-`ownership` rails with exact replay, while CLI or desktop product callers remain deferred until every cleanup phase can complete without stranding an active release.

Commit `bd54830` closes a managed-session restart gap: the authenticated Compose barrier now idempotently reconciles descriptor-declared host service reservations before joining observed ports. An already-attached GoForj process therefore converges after a daemon restart even when its original Start admission did not write the reservation; the focused regression and full root test suite pass. Native local-Docker proof remains outstanding.

The follow-up barrier hardening reports resolver-stage network ownership as a retryable readiness condition rather than “no exact durable reservation.” GoForj commit `a4b35cd5` bounds an un-deadlined managed Compose barrier to 30 seconds, failing fast while Harbor still has time to observe and settle the child inside its two-minute readiness budget. Harbor's launch, attachment, stop, and join calls are bounded as well; an unresolved process scope enters the existing unavailable/quarantine boundary instead of leaving a durable project in `starting` forever. Focused and full Harbor tests pass; native local-Docker proof remains outstanding.

## Harbor project tracking

[Harbor in Plane](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/) uses identifier `HARBOR` and
mirrors Ship's product-area modules, separate Working Now focus, parent/subtasks,
and type/area/stage labels. Reconciliation on 2026-10-19 verified 9 modules and
46 tasks: 15 Done, 5 In Progress, 3 Todo, and 23 Backlog. New completed local
implementation tasks carry `Evidence: Local verified`; they do not imply
integration or release. Plane tracks prioritization; repository source and
proof gates remain authoritative for behavior and support claims.

HARBOR-39 tracks shipping schema-driven creation, with locally verified CLI and
GUI work in HARBOR-40 and HARBOR-41, and publication still open in HARBOR-42.
HARBOR-43 through HARBOR-45 record local appearance, logos/command-menu, and
networking-prompt completion. HARBOR-46 retains Harbor integration as open work.
Existing rollout, resource-address, accessibility, native, and onboarding tasks
now describe the latest evidence without closing their remaining gates. The
remote task states, local-evidence labels, parents, and module assignments were
read back and verified. Dates and assignees remain unset for the new tasks.

The current Working Now shortlist is:

- [HARBOR-1](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/ee891c06-3968-429b-bb4f-9a34272c4afb): Carry the accepted Harbor desktop design through the product.
- [HARBOR-5](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/8b747b2f-8561-4f2a-b802-af4661c14273): Expose structured GoForj startup progress to Harbor.
- [HARBOR-17](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/e459f1af-47e4-457a-ac76-9ac188b0d4dd): Verify native desktop interactions on macOS.
- [HARBOR-39](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/b6e36d85-04fc-4bfc-9242-3315a5bde505): Ship schema-driven project creation with a stack-neutral creator registry.
- [HARBOR-46](https://plane.goforj.dev/goforj/projects/093ece1d-aa7b-4c5c-a77f-b5774f898111/issues/78b82cef-da26-45e0-a956-7a5802f58cde): Integrate the locally validated Harbor UI and contract changes.

The accepted UI implementation is merged through PR #6 and HARBOR-2 is Done. Structured startup events and Docker port diagnostics are implemented and merged through PR #8, with the producer released in GoForj v0.33.0. Completed package work covers unsigned development artifacts,
not signed stable installers or the full native consent matrix. Leave task
dates and assignees unset until an actual commitment is made.

## Local project workspace design preview (2026-10-18)

Branch `feat/project-workspace-design` contains the accepted visual implementation of
the project detail screen and shared project browser. It prioritizes the project
name, status, and labeled actions; adds snapshot-backed overview shortcuts and
service summaries; gives development output the full working area; and removes
the illustration from project working screens. Shared shadcn primitives and
native/daemon contracts are unchanged. The user accepted this visual direction
on 2026-10-18; PR #6 is merged and distribution remains pending. Frontend build, 237 unit tests, and 25 Chromium checks pass. Browser
inspection covers dark/light desktop and a 430-pixel layout without horizontal
overflow. The isolated fixture preview is served on `127.0.0.1:39517` while this
session remains running.

## Wails v3 desktop trial (2026-10-18)

Initial startup now restores and focuses Harbor once both the native application and window are ready, regardless of event ordering. Later frontend reloads do not reactivate a hidden or background window. Lifecycle tests cover ordering and repeated ready events; the native smoke now verifies initial focus before checking hide/restore, bindings, and events.

macOS icon presentation now keeps the application runtime icon unset, preserving the compiled asset catalog used by the Dock rather than replacing it with the opaque square PNG. Its menu bar uses a separate transparent monochrome anchor template generated from the canonical SVG; Windows/Linux retain the existing application and tray artwork. Asset regeneration is documented in `desktop/build/README.md`. Desktop tests/vet, repeat-generation parity, and the local native smoke pass; the running development bundle needs a restart for visual verification.

The development server binds to `127.0.0.1` so Wails' IPv4 asset proxy can connect even when `localhost` resolves to IPv6 first. The frontend install task reruns `npm ci` when either package manifest changes or the installed lockfile is absent; unchanged dependencies are reused. Darwin builds explicitly align cgo, the external linker, and bundle metadata with the compiler's supported minimum: macOS 12 for Go 1.26 and macOS 13 for Go 1.27. This does not increase the module's minimum Go version or change the existing macOS 15 installer baseline. Focused builder tests/vet, helper/relay/desktop native builds without deployment-target warnings, and an isolated IPv4 Vite HTTP probe pass. Restart the development graph to use the updated task.

The migration pins the Go module, CLI, and JavaScript runtime to v3.0.0-beta.32. A native tray projects daemon connection state and offers Open Harbor and Quit Harbor UI. Closing the window hides it; explicit activation restores and focuses it. Desktop shutdown still joins its client goroutine and does not stop projects or the daemon.

The v3 transport is installed before Vue imports select the bridge. It exposes the existing generated method whitelist, unwraps native event envelopes, and preserves clipboard errors. Taskfile-based development and `cmd/desktopbuild` replace `wails.json`, while package outputs retain their existing paths and identities. Linux uses v3's `gtk3` compatibility tag with its existing GTK3/WebKit2GTK 4.1 runtime.

Local macOS native smoke passes with the production SPA and a fake daemon, covering service calls, event delivery, and restoration without host mutations. Interactive tray clicks, directory dialogs, and WebView behavior on every supported desktop remain release gates. This is a beta trial, not a stable-distribution claim. Desktop Go tests/vet, frontend unit tests/build, all 25 Chromium checks, and generated-contract parity pass locally. The full headless suite still encounters existing macOS setgid-mode and host-conflict-observation failures, reproduced on the unmodified base commit; they are not attributed to Wails.

## GoForj update (2026-10-18)

The active CI and development-package baseline is now GoForj `v0.30.0`, with matching generated-project fixture metadata. Earlier source revisions in this handoff describe historical validation. The fixture now supplies the renderer's explicit defaults and validates YAML values instead of byte formatting, without accepting additional fields or multiple documents.

The installed local `forj` is now the published `v0.30.0` binary; its prior `v0.28.0` executable is backed up outside the checkout. Both module vet checks, the desktop suite, fixture unit/race checks, real generated MySQL rendering, descriptor inspection, and repeat-render idempotence pass. The full root suite still reports native host-observation, hardcoded shared-cache, and non-UTC fixture failures; canonical temporary paths resolve its daemon-launcher and discovery path failures. The next goal is to resolve those test-environment issues and run the hosted lifecycle checks against this updated baseline before making new platform-support claims.

This update covers the eight workflow pins, the acceptance fixture version, isolated render fixture compatibility and regression cases, and integration notes. Harbor's own render provenance stays at `0.19.0`; do not rerender the customized app or frontend to change that field. No privileged lifecycle, native project start, or hosted cross-platform proof has been run for this update.

## Read this first

The managed runtime-plan reader remains capability-gated and optional, but the production launcher does not activate it: `NewProjectLifecycleCoordinator` supplies no managed launch context. Harbor verifies only canonical GoForj command/module identity (not an exact version, revision, or clean tree), writes the leased loopback through the marked `.env.host` block, launches ordinary `forj dev`, probes readiness itself, and owns process supervision, routes, and TLS. The reviewed GoForj revision used by generated-project CI is a deterministic source pin, not a runtime compatibility rule. Managed-session code remains dormant pending deletion or activation.

Commit `e0c9773` is the hosted three-platform milestone. GitHub runs `30166963369`, `30166963384`, and `30166963379` passed the same lifecycle on Ubuntu, macOS, and Windows from clean Harbor-owned state: production full setup, three generated projects retaining port 3000, distinct literal system-resolved and default-trusted HTTPS identities on port 443, production stop/unregister/release, foreign-state preservation, and byte-for-byte checkout verification. Runs `30166963374` and `30166963368` passed CI and hosted cross-platform API smoke for the same commit.

The available GitHub-hosted fleet was sufficient for the original observable trusted-HTTPS gate; no self-hosted runner or project VM was required. Harbor retains GoForj's standard checkout-local `./bin` development outputs and uses reviewed GoForj revision `32c1a06d45a92a3de6021ef2db5723e343268489`. Acceptance verification permits content refreshes only for the pre-existing generated runtime binary, build-cache target, and readiness marker, plus direct regular Windows execution snapshots matching `bin/.app.run-<decimal>`; it continues to reject every other path, type, permission, source, configuration, and environment drift.

Hosted Linux and macOS use GitHub's passwordless `sudo`; the Linux workflow adds a temporary helper-specific polkit rule and the macOS workflow temporarily allows the Authorization Services execute right, restoring that policy afterward. Hosted Windows runs as Administrator with UAC disabled, so its workflow provisions a disposable locally signed helper and exact installer-style ACL topology before running the production launcher. These adapters are CI prerequisites, not product claims.

The hosted gate does not prove genuine polkit, Authorization Services, or UAC interaction, a filtered Windows token, an interactive desktop, installer/package behavior, reboot recovery, signing with release identity, or a local Docker-compatible provider on macOS or Windows. Docker provider choice remains outside trusted-HTTPS admission. Those stronger release claims still require infrastructure that the project does not currently have and must not be inferred from hosted green checks.

Harbor is working far enough to register a real GoForj checkout, initialize macOS networking, assign a project-specific loopback, launch `forj dev`, detect App readiness, observe conventional Compose services, and stream current development and selected service output into the Wails desktop. It is not close to release-complete.

The work immediately before this handoff hardened the most painful current failure: `harbord` could disappear while a watcher-created child continued listening on the project's address. The old supervisor owned only the outer process group, so the surviving App looked like a foreign port conflict on restart. New Unix launches use a dedicated session, recovery settles all exact members across watcher-created process groups, and unresolved scope quarantines only the affected project instead of preventing daemon startup. Fresh lease admission now uses the same exact project-scope repair before replanning, so a newly selected Harbor address is not abandoned when its listener is proven to belong to that checkout.

The regression is intentionally end to end: an outer process and daemon generation are killed, a listener in a separate process group ignores graceful shutdown, replacement recovery removes the complete owned scope, and a second Start reaches Ready on the same address and port. If PID-only settlement cannot converge but the project still owns its primary lease, replacement Start now gets one bounded same-user listener repair attempt against that exact leased address and App port before Harbor presents recovery-required.

Retained missing-receipt state now has a separate explicit recovery path. A quarantined project can inspect one bounded macOS candidate, show only reviewed display facts, and ask the user to confirm before the daemon revalidates and signals anything. The plan is caller-bound, short-lived, one-use, and consumed before confirmation; if the confirmed exact scope ignores graceful shutdown, the native backend performs one bounded SIGKILL escalation over that same revalidated scope before reporting failure. Normal Start no longer dead-ends on this state: an exact retained process receipt is settled through the native supervisor before a replacement session is admitted, while a receipt-free planned/awaiting-attach row first reads its exact project and primary-listener fence, automatically settles a same-user listener when native evidence proves it, and is retired transactionally only after that boundary converges and all process fields remain absent. When a project-owned loopback has its App port occupied, the primary-lease admission path now performs the same native same-user inspection and automatically confirms either an exact `forj dev` scope or the same-user process scope that owns the leased port, including one uniquely correlated wildcard bind, before retrying the port probe; a short bounded re-probe absorbs transient native drift while that owned listener exits. Foreign users, multiple owners, ambiguous native facts, and unsupported listeners still remain fail-closed. The desktop keeps the primary action as ordinary `Start project`; explicit inspection remains a separate fallback when Harbor cannot prove the leased listener scope. Failed stop, join, and route-publication edges now retain a route-free recovery boundary so the next Start can retry exact process settlement instead of inheriting `stopping`. The same authenticated repair surface now also checks route-free stopped, failed, or unavailable projects for an already-retired listener and routes confirmable evidence through the no-session inspector; confirmation returns the unchanged retryable project and performs no durable completion mutation. Linux and Windows preserve the same contract but currently return unsupported. The native Darwin implementation has portable and cross-build evidence, not a completed real-host proof.

Commit `b531d7f` replaces the GoForj service-state/log dependency with a daemon-owned, read-only Docker Engine vertical. `harbord` lists local containers, re-inspects candidates, and admits them only when the Compose project, service, and working-directory labels resolve to the registered canonical checkout. A selected service's current-session log stream now crosses authenticated control, Wails, and the typed Vue bridge. The continuation adds a Docker container-event wake hint; event payloads are discarded, exact admission is repeated, and service/resource changes are persisted only behind project-revision and session-generation fences. Supported descriptor resource reports then update loopback HTTP reservations after the durable write; unsupported or failed reports retain the prior resource links. No GoForj log capability is involved, neither the generated App nor Vue can access Docker, and the route reconciler now promotes only explicitly reserved HTTP resources whose owners and private loopback upstreams are ready.

The latest bounded cross-tree slices add a pure managed-publication planner, an ephemeral exact-fence observation registry, a durable authority boundary, a transport-neutral managed-session v1 message contract, and a matching private GoForj client adapter. These are dormant in the production launcher: Start and Restart do not derive or pass a launch context, and ordinary `forj dev` does not register, publish, replay, or negotiate a runtime plan through Harbor. The output-reattach schema is likewise capability-off and handler-free. Driver-specific URL/DSN shapes, native relay evidence, and a decision to delete or activate the managed-session path remain open.

The retained-lease listener repair now resolves endpoint rows to native owners before classifying ambiguity. A single same-user owner may hold multiple exact, wildcard, or dual-stack records for the leased port; a short-lived PID race during descriptor census no longer turns an otherwise complete owner set into a native-read failure, while unaccounted rows, multiple owners, foreign evidence, and incomplete snapshots still require the explicit fallback. The Darwin libproc descriptor parser now recognizes the corresponding IPv4-capable AF_INET6 listener records before owner correlation.

The pure managed-publication observation normalizer now supplies the ephemeral registry from Harbor's existing read-only service facts. It selects only descriptor-declared Compose-owned host TCP endpoints, joins one exact native-port observation to the matching durable reservation generation, emits deterministic complete replacements, withdraws stale publications on incomplete reads, and preserves the last good registry set when a matching fact is malformed. This is deliberately a Harbor-only seam; it does not invent a GoForj wire contract or activate a native relay.

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

The privileged Linux resolver test preserves its public typed failure contract but adds a 4 KiB-capped unwrapped native cause to its root-only observation and mutation failure reports. Linux recovery now distinguishes unpublished canonical staging from an exchanged old owned artifact: only the latter restarts `systemd-resolved` while the old stage remains as retry evidence, then removes it after fixed/stage identity and content revalidation. A failed or uncertain restart leaves that marker intact for the next attempt. Its `DNSEx` reader normalizes only systemd's zero-port default representation to port 53. Foreign, malformed, unsafe, excess, and ambiguous transaction states remain preserved and fail closed; recovery rejects any multi-artifact transaction set before mutation so one bad remnant cannot cause partial cleanup, and malformed entries carrying Harbor's reserved stage/quarantine prefixes are rejected before scanning or mutation. Native CI evidence is still required.

Do not expand scope before reading [Current implementation state](./current-state.md), this handoff, and the relevant design document.

## Product decisions that should not be reopened casually

- `harbord` is the only durable writer and runtime reconciler. The CLI and desktop are clients.
- Harbor is per-user. The daemon and desktop do not run as root or Administrator.
- Elevation is explicit and one-shot through an allowlisted helper.
- Each project keeps its own Apps, Compose services, versions, containers, and volumes. Harbor shares control-plane infrastructure, not one global MySQL/Redis/mail stack.
- Projects reuse ordinary ports by receiving distinct loopback identities. Port offsets are not the product model.
- One shared DNS and HTTP/TLS ingress serves stable `.test` names.
- GoForj owns project composition and `forj dev`; Harbor orchestrates it instead of reproducing `.goforj.yml` semantics.
- Wails v3.0.0-beta.32 is the pinned desktop trial with an integrated native tray. Vue 3, TypeScript, Vite, Tailwind 4, Pinia, Reka/shadcn primitives, and Lucide are the frontend foundation.
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

The desktop can be run separately with `wails3 dev` from `desktop` when a daemon is already available.

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

Do not delete SQLite state, wipe the database, or make Harbor kill an arbitrary listener. New sessions launched after the process-scope commit retain enough evidence for automatic recovery. A retained pre-receipt quarantine no longer blocks a normal Start: Harbor retires only the receipt-free row, then lets the normal process admission and native port checks establish the replacement runtime. If a persisted process-backed quarantine cannot settle by PID/session evidence, replacement Start now gets one bounded same-user repair attempt against the project's exact primary lease and App port before Harbor presents recovery-required. An already-retired remnant on an exact project lease is now settled automatically when one same-user process scope owns the leased port, even if its working directory no longer identifies the checkout or the process used a wildcard bind; only a missing lease, foreign user, multiple owners, or ambiguous native scope needs the explicit listener-inspection action or manual user action.

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

The direct Docker service/log vertical spans `internal/containerruntime`, project supervision and reconciliation, authenticated control, the Wails bridge and generated fixture, and the Vue service-log panel. Its two-fixture native adapter proof now builds on both Linux and Darwin; execution still requires the dedicated worker with a user-selected local Docker-compatible Engine described below. Continue excluding the unexplained top-level `desktop/package-lock.json` unless its ownership is established.

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
- the helper dependency guard admits `os/exec` only through the reviewed Windows resolver adapter, while native Go/PowerShell execution parity and workflow evidence remain required. An opt-in elevated test is intended to cover a fresh rule's observe, CAS add, bounded name-server Set repair, exact verification, and release once its native precondition contract is stable.

Do not claim either platform's resolver as complete merely because its foundation is committed or cross-compiles.

## Current implementation status

No delivery phase has met its complete exit gate.

- Hosted three-OS CI, Phase 1 evidence, and privileged loopback tests exist.
- The hosted same-port proof provisions three exact loopback identities, queues and launches all three rendered GoForj projects before awaiting readiness on port 3000, and rejects evidence or cleanup that omits any identity. The same test passes on hosted Ubuntu, macOS, and Windows.
- Durable SQLite state, authenticated local RPC, operation journals, project registration/removal, network setup approvals, and recovery are substantial.
- DNS, HTTP ingress, TCP relay, local CA/certificate primitives, loopback identity, Darwin resolver ownership, Linux resolver integration foundation, the production-wired Windows NRPT backend, and runtime activation exist. Resolver approval now replaces the empty generation with the authoritative DNS-only listener and daemon restart reconstructs that generation; observed native TCP service publication can use that DNS stage, while HTTP/HTTPS remains intentionally gated on full network and trust proof.
- Wails/Vue can add/remove projects, set up networking, start/stop a project, inspect and explicitly confirm one retained macOS runtime candidate, display actionable errors, stream current ANSI-formatted development output, show an explicitly historical retained tail when the live supervisor is unavailable, and follow a selected Compose service's current-session logs through independent held cursors with incremental terminal redraws.
- GoForj now has a pure `forj project:describe --json` v1 starting contract for static project identity, available App inventory, conventional HTTP runtime defaults, a non-secret topology digest, and deterministic service requirements derived in memory from `.env.example`/`.env`. It does not execute generated code or mutate the checkout. Harbor invokes and strictly validates that descriptor before production process launch, persisting only its normalized digest in the active session. The optional resource-intent and service-requirement sections now cross the process boundary, constrain live framework links by stable owner/runtime/path identity, and drive exact resource-label `.test` endpoint reservations after readiness. Harbor's managed-session v1 contract and authenticated handler seam now have a matching private GoForj frame/handshake/client adapter with strict typed request/response validation; production Start/Restart supply the owner-only launch context, and the client attaches the exact admitted process with a capability-gated ticket and bounded startup retry. The optional runtime-plan handler returns lease-derived App binds and fresh Compose service publications only when the peer negotiates it; unsupported runtime kinds and absent service-environment assignments remain non-blocking. Startup service state still comes from `harbord`'s read-only local Docker list/inspect adapter, while the optional GoForj resource-only status query supplies framework links rather than container state or logs. Direct service logs are daemon-owned and do not depend on a GoForj log capability.
- The managed-session claims in the preceding historical summary are superseded by the current production boundary above: the handler and adapter exist, but production Start/Restart do not supply a launch context or use that path.
- Generated-project development and CI use reviewed source revision `32c1a06d45a92a3de6021ef2db5723e343268489` for deterministic builds. Runtime admission does not pin that revision or any GoForj version; it verifies canonical embedded command/module identity only.
- The hosted macOS, Linux, and Windows three-project trusted-HTTPS lifecycles pass. Interactive product consent, native local-Docker execution on macOS and Windows, reboot, tray, signed packaging, updates, and release evidence remain incomplete. The readiness edge and supported event wakes derive optional HTTP reservations from descriptor-matched resources; the durable resource replacement precedes endpoint assignment and route reconciliation, while unsupported or failed framework queries retain their last-known links. Portable coordinator coverage proves the wake hint is followed by fresh service/framework observations, fenced replacement, and route publication; native macOS execution against a user-selected local provider remains required evidence.
- `internal/platform/trust` now supplies the portable trusted-HTTPS ownership boundary: requests bind one public CA fingerprint and trust mechanism, observations classify only matching or explicitly marked entries, compare-and-swap fingerprints guard effects, unrelated roots are preserved, and pre-existing identical roots remain reusable but unowned. The privileged helper admits strict public-CA trust tickets and returns bounded CAS postcondition evidence, including the preserved-unowned pre-existing case. On Darwin+cgo, an admitted trust ticket now closes/disarms root-owned ticket and replay authority, irreversibly binds the process to the authenticated current user, and then opens only the current-user Security.framework adapter. Release requires exact owned facts and optimistically revalidates the exact target settings plus marker immediately before removal, while preserving Security/keychain errors; the reviewed certificate-wide API offers no atomic conditional delete. Hosted native trust installation, default verification, and cleanup pass on all three operating systems under their CI elevation profiles; destructive shipping release remains gated on interactive product evidence.
- Project Start/Stop/Restart exists in control, desktop, and first-class CLI surfaces; the CLI exposes `harbor start <project>`, `harbor stop <project>`, and `harbor restart <project>` with `--json` and an explicit retry intent. Restart keeps one durable `project.restart` operation across the exact stop boundary and replacement readiness path.
- `harbor status <project>` provides the CLI's read-only view of one project from the authoritative daemon snapshot, with compact human output or the exact typed project object through `--json`.
- `harbor open <project> [resource]` now resolves one fresh, project-scoped resource and invokes only the fixed native browser handler; it defaults to `app-http` and never accepts a caller-supplied URL.
- `harbor logs <project>` now exposes the existing bounded current-session project and Compose-service cursors through a read-only CLI surface, with `--follow` using the negotiated held-read capability and resetting on session replacement.
- `harbor doctor [project] [--json]` now emits a versioned, explicitly `control-plane` report from authenticated daemon status and a validated snapshot; it records sequence drift and raw project state without claiming native host health or offering repairs.
- The pending resource-projection repair migration retains only the readiness-proven `app-http` resource and removes older optional runtime links that could make the daemon reject its complete snapshot. It is intentionally one-way: the links are derived and are rebuilt on a successful Start; it does not affect project source, volumes, secrets, or operations.
- The desktop now exposes the typed project-removal approval handoff: an active `requires_approval` removal retains its intent, offers one explicit administrator-approval action, consumes the terminal result, and remains retryable after a declined or unavailable native approval. Native consent execution and release-grade macOS proof remain outstanding.

[Current implementation state](./current-state.md) contains the fuller matrix and commands.

## Immediate next goal

The active goal is now the three-project trusted-HTTPS happy path recorded in [Current implementation state](./current-state.md#active-happy-path-goal). The critical order is:

1. complete one macOS setup flow that proves resolver ownership, current-user CA trust, and Harbor ingress on public ports 80 and 443;
2. promote the live resolver-only data plane to full HTTP/TLS authority and backfill exact `app-http` reservations for already admitted projects;
3. run three generated GoForj projects concurrently on their unchanged configured App port and verify their distinct literal `https://<project>.test` URLs through system DNS and default trust;
4. stop them, prove their source and configuration are unchanged while permitting only GoForj-owned ignored `./bin` content refreshes, and remove only Harbor-owned host state;
5. freeze feature work after the macOS gate passes, then move that same acceptance test into the claimed GitHub Actions platform matrix.

Do not substitute direct-IP requests, custom root pools, TLS bypasses, explicit resolver sockets, translated public ports, synthetic Store setup, or a second platform-specific test. A pinned mkcert-backed trust adapter is allowed only if it is the shortest safe way to produce the required observable trust behavior.

The remainder of this section records the previous continuation context. It is useful implementation history, but it does not supersede the active happy-path order above.

The current project activity path now exposes the owner-private output spool as explicitly historical output when the live supervisor is unavailable. This is a diagnostic continuity improvement only: it does not reattach pipes or grant process authority, and the broker path preserves a fresh cursor boundary when live output returns.

`OutputBrokerJournal` now supplies the transport-neutral append-before-notify core over the existing checksummed spool: exact replay, idempotent cursor retries, bounded subscriber queues with explicit gaps, and monotonic acknowledgements are covered directly. The standalone `cmd/outputbroker` now owns that spool while it adopts inherited stdout/stderr pipes and serves an authenticated local replay/live connection; the broker remains separate from GoForj process stop/reap authority.

Endpoint-specific authenticated local transport is now available for the broker boundary: Unix sockets and Windows named pipes use the existing owner and kernel-peer admission rules, while `OutputBrokerPeer` requires exact project/session, endpoint, broker process evidence, kernel PID, and fresh full-process correlation. The daemon provider discovers only a canonical sibling `outputbroker` executable, and the launcher uses fixed inherited descriptors, an owner-private manifest retained for the broker lifetime, and a bounded challenge handshake; direct tests plus Darwin/Windows compile checks cover it. Successful first-start attachment now persists the broker endpoint, exact broker process evidence, owner-private manifest path, and ticket digest in the same fenced session mutation as the child process; the raw ticket never enters SQLite, argv, the checkout, or the ordinary environment. Supervisor feeds authenticated records through its existing relay without transferring child stop/reap authority, preflights the journal read-only, and falls back to direct pipes if the optional artifact or startup handoff is unavailable. Restart-time native broker observation and output-only pipe re-adoption are now implemented for Linux/macOS, with strict manifest/ticket/process checks and historical spool fallback; native execution evidence remains outstanding.

The Harbor side of managed publication activation is now implemented: an authenticated barrier joins the latest complete observation to Harbor-owned TCP reservations, replaces native relays behind a route-free withdrawal boundary, and acknowledges only after live relay evidence. Inherited GoForj sessions now send the complete publication reset and retry that barrier from the initial watcher graph; Harbor recovery preserves the exact live attached process before a replay connection is admitted. The supervisor now writes owner-private, checksummed per-session output history outside the checkout and can replay complete frames after a crash without changing the exact-live `ReadOutput`/`WaitOutput` contract. That spool is diagnostic staging, not stream authority: recovery now attempts output-only pipe re-adoption only after process-bound broker evidence, exact manifest ownership, and ticket-digest checks; failure leaves the historical spool available. The additive `managed-session.events.v1` schema is now mirrored and strictly validated in Harbor and GoForj, but remains unadvertised and unhandled while process/action event delivery is still being phased in; the process-surviving broker and idempotent journal boundary are now available underneath it. Harbor and the dedicated GoForj worktree now mirror a separate, capability-off output-reattach begin/challenge/confirm schema (`e37fa15f`) bound to exact session process evidence, endpoint shape, fence, nonce, and opaque ticket; it is not advertised, handled, or treated as live-stream authority. GoForj now also gives exact generated lifecycle tasks stable IDs and additive typed phase metadata; managed admission migrates only exact framework-owned legacy forms in memory and rejects ambiguous custom tasks or cross-scope phase assignments before Harbor session registration, while current standalone ordering remains preserved. The next evidence edge is native macOS local-Engine execution, followed by the remaining lifecycle/event contract. Durable broker evidence is now persisted with first-start attachment, and recovery revalidates and adopts the surviving broker on Linux/macOS; unsupported readers fail closed to historical spool output.

The Harbor side of the typed Compose boundary is now explicit: the authenticated Compose barrier can observe Harbor-owned service publications during `Starting` for the exact requested session fence, while ordinary route planning and all non-Compose paths retain the `Ready` requirement. GoForj calls the barrier from its initial watcher graph, and the dedicated managed lifecycle work now places post-Compose readiness and migration behind the barrier with the supported overlay applied to every managed setup child.

That lifecycle slice is now wired in GoForj: managed startup runs explicit pre-Compose work and builds, executes typed Compose, waits synchronously for Harbor's barrier, requests the negotiated runtime plan, then runs post-Compose readiness, migration, and post-migrate tasks with the supported overlay installed. Harbor-owned `down_on_exit` cleanup now orders typed pre-Compose-down, Compose-down, and post-Compose-down tasks. The remaining managed contract edges are driver-specific URL/DSN mapping and full process/action event delivery; standalone `forj dev` and standalone `forj down` remain unchanged. Supervisor output-only re-adoption is now wired to the persisted broker evidence with a historical-spool fallback.

The durable scoped project-restart slice is now implemented and covered by state, coordinator, control, CLI, Wails, desktop-store, and desktop-view tests. Native macOS Docker service/log and event-driven-refresh evidence still requires infrastructure beyond GitHub's standard hosted macOS runner. The hosted three-project trusted-HTTPS lifecycle is a separate behavioral gate and does not substitute for that Docker evidence.

When suitable infrastructure exists, prove the committed direct Docker service/log vertical and event-driven service refresh on a macOS product worker. Exercise a real multi-container GoForj checkout through service discovery, log output, container recreation, event-triggered topology refresh, project Stop, and project restart. The proof must exclude similarly named neighboring Compose projects and show that Harbor never performs an Engine mutation. The existing strict verifier binds lifecycle and cleanup evidence to one worker, commit, platform, and artifact set, and requires the Engine 28-equivalent floor, but deliberately does not bind macOS or Windows to Docker Desktop. Any user-selected local Docker-compatible provider is valid when its Engine is reachable and the behavioral assertions pass.

The route reconciler now implements the publication-derived join through Harbor's existing resource/routing policy: an explicit HTTP endpoint reservation, a matching resource, a ready owner, and an exact assigned loopback upstream are all required. When a descriptor reports resources, Harbor replaces optional HTTP reservations with the enabled, descriptor-matched ready resources and names them `<resource-id>.<project>.test` at readiness and after supported event-driven refresh; selected host-visible TCP service requirements now receive `service:<endpoint-id>` reservations at `<service>.<project>.test:<native-port>` as soon as resolver-stage DNS authority is active, while the private upstream and native relay remain withheld until managed-session or container evidence exists. The pure planner and its durable boundary validate this join against exact project/session and reservation generations, accept resolver-or-full network ownership for native TCP routes, reject authority drift and collisions, and emit no route for an unobserved endpoint; they do not persist private upstreams or mutate the live data plane. HTTP resources remain full-stage-only because they require shared HTTPS ingress. Harbor preserves unrelated native TCP reservations and the default `app-http` authority, and removes only its namespaced service reservations on a fresh supported descriptor. Observing a port is not permission to publish it. The managed-session client now negotiates and attaches through the production local IPC launch context, and inherited GoForj now resets publications, retries one lost transport, and periodically republishes observations. A restarted Harbor authority can replay the exact attached fence without a durable generation bump, preserve the exact live attached process before replay, and attempt output-only broker re-adoption from the persisted owner-private manifest; failed or unsupported adoption leaves the historical spool fallback.

Resolver-stage DNS is sufficient for observed native TCP service routes; the managed-publication planner accepts resolver-or-full network ownership for those routes and continues to reject unobserved or unreserved sockets. Shared HTTP/HTTPS resources remain full-stage-only until low-port and current-user trust proof is complete.

The ingress remains loopback-only in the current product slice. LAN sharing, user-owned NAT, and Cloudflare-style public reachability are deliberately a separate future exposure grant: a named App, an explicitly selected concrete host address, exact external Host/SNI names, and revalidation on interface or edge drift. Do not broaden the proxy to wildcard binds or treat a public DNS record as Harbor ownership; the detailed contract is in [Networking](./networking.md).

The retained-session repair still needs native execution on a real macOS host: reproduce Start, abrupt daemon loss, automatic retry, cancellation, confirmation, complete settlement, and a second Start on the same endpoint. The historical already-retired listener now reaches the same caller-bound reconcile/control/desktop flow through its read-only durable no-session/lease boundary and separate macOS inspector/confirmation backend; the desktop exposes a read-only check for route-free stopped, failed, or unavailable projects, and confirmation performs no no-session durable mutation. Native Darwin execution proof remains. The desktop project-removal approval handoff is now wired; native consent execution/proof, trusted routing, and tray presence remain later bounded desktop slices. Linux resolver crash recovery now has focused stage/exchange/quarantine/foreign-state coverage and a root-only lifecycle command required by Linux CI. That workflow evidence is still required before claiming native resolver support.

The production lifecycle path now invokes the exact registered GoForj executable with `project:describe --json` before any project-network mutation or process authority. Harbor strictly admits descriptor schema v1, rejects unknown or unsafe fields, and stores the descriptor's normalized topology digest as the active session digest; an invalid descriptor leaves no primary lease or endpoint reservation, and a registration path change is revalidated after lease admission before launch. The validator carries optional resource intent and service requirements through process admission: service requirements retain stable IDs, explicit App consumers, and distinct endpoint IDs/protocols/ports/visibility without exposing values. GoForj now emits that service intent from a pure `.env.example`/`.env` topology read for cataloged endpoints; the producer never emits credentials, addresses, or raw endpoint-affinity material. At readiness and on supported Docker event wakes, Harbor joins enabled descriptor resources to live owner/runtime/path facts and persists exact loopback HTTP reservations, while selected host-visible TCP requirements receive stable `service:<endpoint-id>` reservations on `<service>.<project>.test:<native-port>`; stale Harbor service reservations are removed only from that namespace, and unrelated HTTP/TCP authority is retained. Native relay publication remains withheld until a managed-session or container observation proves a private upstream. Unsupported or failed framework queries keep the prior resource links while service refresh continues. The durable lifecycle store now has a fenced, idempotent `awaiting_attach`→`attached` mutation that rechecks exact process evidence and advances the session generation. Harbor's authenticated handler seam and GoForj's private client adapter now exist, and the production control endpoint can isolate a configured GoForj role and accept exact process-bound publication replacements; production Start and Restart now derive the final descriptor-bound launch context before `BeginProjectStart`, pass it through a one-use runtime file, and GoForj consumes it before project config/env loading. The negotiated `managed-session.launch-context.v1` capability carries the launch ticket only for that inherited path; Harbor advertises it, hashes the exact ticket string against the durable digest, and maps only the pre-attachment state to a retryable response. GoForj opens the local Unix transport and retries that narrow startup race, then resets publications, retries the authenticated barrier from the initial watcher graph, and replays that exact identity once if the local stream disappears, while never placing the ticket in the checkout, argv, or ordinary environment. A restarted Harbor authority can now reconstruct the exact attached fence without a durable generation bump, preserve its live process, and attempt strict output-broker re-adoption; fully phased Compose ordering and process/action event delivery remain open. The desktop lifecycle surface now refuses start/stop/restart while a retained snapshot is stale or the daemon is disconnected, without creating a new client intent; it still permits a retry of an uncertain first request before any baseline snapshot exists. Portable subprocess, schema, timeout, ordering, resource-validation, service-requirement, endpoint-assignment, lifecycle, desktop-guard, and managed-session protocol coverage is green.

## Next-session start checklist

1. Read `AGENTS.md`, [Current implementation state](./current-state.md), and this handoff before changing code.
2. Run `git status -sb`, inspect `origin/main...HEAD`, and preserve unexplained local artifacts rather than sweeping them into a commit.
3. Treat paths, PIDs, listeners, leases, and database rows in this document as historical evidence until re-observed.
4. Work only on a blocker named by the active three-project trusted-HTTPS acceptance goal.
5. Run the macOS acceptance gate before opening cross-platform adapters or another product front.
6. Validate both Go modules and any affected frontend or native OS surface.
7. Commit explicit paths as `Chris Miles <chris.miles.e@gmail.com>` and update `current-state.md` plus this handoff when the continuation point changes.

## 2026-07-24 native-service DNS readiness handoff

The macOS reproduction was: start `test`, immediately query `mysql.test.test`, receive `Unknown host`, flush `dscacheutil` and `mDNSResponder`, then resolve the expected project loopback address. The cause was lifecycle ordering, not the resolver file: Harbor committed `Ready` before publishing observed native service routes, allowing macOS to retain the transitional `NXDOMAIN`.

Startup now stages project-scoped native routes under the durable `Starting` authority and commits `Ready` only after that publication succeeds. The controller still requires `Ready` for ordinary steady-state reconciliation, and an empty replacement remains available as a fail-closed withdrawal during rollback. Focused root-module tests pass. The next native proof is one immediate post-ready lookup of `mysql.<project>.test` on macOS without a cache flush; do not add another TTL or cache workaround unless that ordered publication still reproduces a miss.

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

Inspection returns bounded display facts and an opaque inspection ID/fingerprint. Its process-shape label is fixed to either `forj dev` or `project listener`; it never returns raw birth tokens, argv, or environment values. Zero, multiple, cross-user, unreadable, or partially correlated candidates are diagnostic only and cannot be confirmed. Automatic Start may additionally correlate one uniquely owned wildcard bind when the exact project lease identifies its port; that bind still must pass the same native birth, parent, and process-scope checks.

### Confirm

Confirmation supplies only project ID, opaque inspection ID, and candidate fingerprint. The server binds the plan to the authenticated caller, project/session/network generations, exact target, and a short expiry.

Before signaling, re-read and compare every durable fence and every native process/socket fact. Any drift emits zero signals and expires the plan. Stop only the exact candidate scope shown to the user; never accept a PID or address from the client and never kill “whatever owns this port.”

The first version uses graceful exact termination and polls exact birth absence, complete session absence, and socket release. A watcher respawn or identity change fails confirmation and requires a fresh inspection. Only after every postcondition succeeds does one atomic state mutation retire the retained session and project the route-free project to stopped.

The desktop keeps an explicit inspection action for the cases that still need native user confirmation, but it is not part of the ordinary recovery action. Start first attempts automatic convergence and is no longer a `project.recovery.ambiguous_launch` dead end; the primary button remains `Start project`. Its warning is:

> Harbor no longer has its launch receipt. This process is a candidate, not proven Harbor-owned. Continue only if you recognize it as this project.

The confirmation label should describe the effect, such as `Stop this process and reset project`, rather than generic `Repair`.

Portable tests cover caller mismatch, expiry, revision drift, PID reuse, birth/executable/argv/cwd/socket/parent drift, multiple owners, respawn, cancellation, one-use consumption, and zero-signal failed branches. Native macOS execution is still required; Linux and Windows currently prove only the explicit unsupported seam.

### Already-retired listener

When Start reports `project.network.port_unavailable` and no session remains, Harbor may offer `Inspect listener`. The result must say that Harbor has no launch receipt and does not own the candidate. It is actionable only when native inspection finds one same-user `forj dev` scope, or one same-user listener rooted at the registered checkout, whose process births, parent/descendant relationships, and exact or wildcard socket on the leased port all correlate. A lone child listener without a stable root is diagnostic only.

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

The Linux resolver checkpoint passed isolated root-module tests and vet, focused resolver/helper/wire tests, and source formatting, but its hosted `systemd-resolved` lifecycle does not yet produce the required exact runtime route. The Windows checkpoint covers daemon/helper provider wiring, focused resolver tests, vet, and Windows cross-compiles, but its elevated NRPT lifecycle still encounters a deterministic native precondition mismatch. Both native executions are outside required CI until their platform claims are completed; neither platform is supported from build evidence alone.

## 2026-07-26 project restart admission correction

Project Restart now crosses the exact stop boundary before asking the primary-lease coordinator to re-admit the App port. The previous ordering ran retained-lease admission while the active project still owned its assigned listener, allowing Harbor to report the project's own port as a foreign conflict even though separate Stop then Start succeeded. The framework-neutral lifecycle regression keeps the port unavailable for as long as the old runtime handle exists and proves Restart still reaches a replacement Ready session.

## 2026-07-26 macOS 26 app-icon packaging

Wails v2's generated `.icns` is a legacy icon on macOS 26, which causes the system to shrink Harbor's artwork inside an additional system-colored frame. When full Xcode's `actool` is available, the Darwin post-build hook compiles a temporary Icon Composer document in an isolated staging directory, installs only `Assets.car`, removes the raw document and every `.icns` fallback, deletes `CFBundleIconFile`, selects only the catalog's `AppIcon` through `CFBundleIconName`, and ad-hoc re-signs the bundle. This exact catalog-only runtime shape is required because retaining the raw or legacy resources can make Tahoe choose the framed bitmap despite a valid `IconImageStack`. A missing `actool` leaves Wails' legacy icon in place without failing source development, and development uses the distinct `com.goforj.harbor.dev` bundle identity.

## Things not to do

- Do not infer process ownership from a busy port.
- Do not clear a session row that retains process evidence; receipt-free quarantines may only be retired through the fenced lifecycle release path.
- Do not wipe the user's database as ordinary recovery.
- Do not let one quarantined project prevent the daemon or other projects from starting.
- Do not pass Harbor's assignment through new special `forj dev` flags.
- Do not make generated Apps or the frontend inspect Docker, and do not let Harbor's read-only adapter perform Compose or Docker mutations.
- Keep the accepted `.env.host` bridge for older Harbor/GoForj pairs; the negotiated overlay is the preferred path when both sides advertise it.
- Do not run `forj dev`, Wails, or `harbord` with elevation.
- Do not hand-write startup schema creation; use embedded GoForj migrations.
- Do not stage `.tmp/` or an unexplained lockfile with unrelated work.
- Do not claim Linux/Windows resolver support from cross-compilation alone.
- Do not start tray, packaging, or updater work before the core restart loop is dependable.
The managed-session transport now has a bounded, authenticated opt-in event sink on both Harbor and GoForj. Normal launch still leaves `managed-session.events.v1` unadvertised, and process/action producer hooks plus durable event projection remain open.
The managed Compose barrier is bounded to 30 seconds, and its steady-state heartbeat now stops after two consecutive failed windows; a successful refresh resets the counter. A route/readiness outage therefore produces bounded diagnostics instead of an unending heartbeat loop, while the watcher process remains available for a fresh lifecycle attempt.

## Managed publication admission and service environment

The resolver-stage managed-publication hang is closed by making the DNS boundary useful instead of extending another retry loop. Resolver approval starts the authoritative DNS-only generation and restart reconstructs it; selected host-visible native TCP service reservations and observed relays may now use that stage. Shared HTTP/HTTPS resources still require `project.network.full_setup_required` and the later low-port/trust proof, while an uninitialized network still uses `project.network.setup_required`. The desktop explains that DNS is active but secure web ingress is not, and does not send a resolver-only retry for the SSL path. If a child is attached before its native service reservation exists, the managed-session barrier repairs the durable TCP reservation and then joins the fresh observation; missing or stale observations remain bounded barrier errors rather than a project stuck in `Starting`.

The sibling GoForj service-environment experiments were local and unpublished and are intentionally not part of Harbor's dependency or startup contract. Harbor continues to validate and ignore absent optional assignments; no project lifecycle path assumes MySQL, Redis, Memcached, or Kafka environment metadata.

The Ditracker descriptor exposed one additional compatibility edge on 2026-07-22: GoForj derived `diclan` as a service consumer from deployment variables such as `DICLAN_STORAGE_SYNC_*`, even though the descriptor declared only the `app` App. `diclan` is not an App process and must not receive a service overlay. Harbor commit `eb71199` now drops the entire optional `service_requirements` projection when a consumer cannot be joined to a declared App, preserving the core descriptor and allowing project Start without guessing or injecting values into the wrong process. This is a Harbor resilience mitigation, not the producer-side fix; GoForj should restrict environment-prefix inference to configured App scopes and stop emitting deployment prefixes as App consumers.

Focused package and race checks pass for the new admission, terminal-barrier, descriptor, and overlay paths. Full GoForj package tests remain environment-dependent here because the demo frontend `node_modules` fixture is absent and Docker is not a native macOS Desktop engine. Native macOS execution is still required before claiming resolver, Docker, or desktop lifecycle evidence.
