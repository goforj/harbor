# Publishing development previews

Harbor uses short-lived feature branches, pull requests into `main`, and immutable version tags. A long-lived `release/` branch is unnecessary until Harbor maintains multiple supported release lines.

The `Prerelease artifacts` workflow admits only `vMAJOR.MINOR.PATCH-beta.NUMBER` and `vMAJOR.MINOR.PATCH-rc.NUMBER`. It requires the tagged commit to be on `main` with successful push CI for that exact commit. Create the tag only after those checks pass. Stable tags fail preflight while signing and the full product release gates remain unfinished.

The workflow calls the existing macOS and Ubuntu package acceptance jobs, downloads their exact tested packages, verifies their SHA-256 sidecars, and adds `SHA256SUMS`, source/run provenance, and explicit preview limitations. Only the final publication job can write releases. It attaches all assets to a draft before publishing a GitHub prerelease, without rebuilding or marking it as the latest stable release.

Public assets currently cover an unsigned, unnotarized Apple-silicon PKG and an unsigned Ubuntu 24.04 amd64 DEB. Windows publication is gated until production signing replaces its disposable CI certificate. Do not publish the Windows development workflow's installer: it contains an eight-hour CI signing identity and installs that identity into machine certificate stores for its isolated test. That workflow remains available manually for native packaging evidence.

To rehearse without publishing, dispatch `Prerelease artifacts` on the implementation branch with a full version such as `0.1.0-beta.1`. The same package jobs and checksum verification run, but the publication job is skipped. The `Harbor-prerelease-publication` artifact contains the reviewable result. A rehearsal does not create a tag or a GitHub release.

To publish an approved preview after merge and successful main CI:

```sh
git fetch origin main
git tag -a v0.1.0-beta.1 <tested-main-commit> -m "Harbor 0.1.0-beta.1"
git push origin v0.1.0-beta.1
```

Choose the next unused version rather than copying the example blindly. The repository tag ruleset prevents deletion or update of `v*` tags. Repository maintainers can create a new tag normally. Do not move a published tag to repair a failure.

If packaging or validation fails, no public release is created. Fix the source through a PR and use a new version tag. A failure during attachment can retain a draft; inspect and remove only that incomplete draft before rerunning the same workflow. Publication deliberately refuses to overwrite an existing release or its assets. A main CI run still in progress can be allowed to finish before retrying preflight.

This path publishes development previews, not a signed product release or automatic update channel. Signing, genuine interactive consent, reboot recovery, and signed update evidence remain governed by [Installation and release](./installation-and-release.md) and [Testing](./testing.md).

## Unattended installed macOS UI checks

`Installed macOS GUI workflows` installs the exact PKG retained by a successful `Prerelease artifacts` run on a fresh GitHub-hosted macOS 26 Apple-silicon worker. It verifies both package checksums and provenance, then requires the checked-out product code to match the package source. Test harness and documentation changes may differ. Product changes require a new package rehearsal.

Run it after packaging without publishing or using a personal development installation:

```sh
gh workflow run macos-package-ui.yml --repo goforj/harbor \
  --ref main -f package_run_id=<successful-package-run-id>
```

Changes to this harness also trigger a same-repository PR check against the latest successful package run. The source guard rejects a stale package when product code differs. There is no fork-code execution through `pull_request_target`.

XCTest operates the installed Wails window through macOS accessibility, rather than injecting JavaScript or replacing the package executable. Its planned coverage is graphical creation of a minimal Vue/API project, Start, system-trusted HTTPS, close-to-hide, single-instance reopen, Quit Harbor UI without stopping the project, another launch, and Stop. Native launches use the installed bundle through `open`, retaining separate stdout/stderr files for each stage. Screenshots, the `.xcresult`, lifecycle evidence, and original package provenance are uploaded as `Harbor-installed-macos-ui-<run-number>`.

A failed GUI assertion keeps the gate failed and attempts bounded recovery through the normal native quit, project Stop, fixture unregister, networking release, daemon stop, and installer uninstall paths. It refuses registrations outside its disposable fixture and retains `failure-cleanup.json`, including partial failure. The CLI retains one removal intent; a harness-only authenticated desktop client approves only the exact pending fixture operation through the production helper executor. It does not start a replacement daemon or bypass helper admission. Recovery verifies exact service absence and user-file hashes; worker destruction alone is not recorded as successful cleanup.

Stopped projects intentionally retain endpoint leases, so both Unix GUI gates explicitly unregister their disposable fixture before global networking release. Registration must survive the earlier UI-only quit, relaunch, and Stop checks. Uninstall then verifies the post-unregister database file and generated source files; these hashes do not prove that a registered project survives uninstall. This cleanup boundary preserves the production release admission rule.

This is unattended GUI automation in a real graphical session. It installs the released GoForj CLI and Wire, and temporarily exposes that toolchain through the worker's graphical-session `PATH`. Administrator approval is preauthorized on the disposable worker and restored afterward. Genuine consent prompts, default desktop toolchain discovery, Docker-backed projects, tray menu interactions, reboot, and signed updates remain separate coverage. The complete macOS GUI result is still pending: run 39364852659 stalled installing frontend dependencies, and run 39365852011 exited before its first window with native exit status 2 but no retained internal error.

## Parallel Ubuntu and Windows GUI checks

`Installed Linux GUI workflows` admits the retained DEB from the same successful `Prerelease artifacts` run, using `package_run_id` on dispatch. The exact installed GTK/WebKit executable is driven through AT-SPI and native keyboard events in an Ubuntu 24.04 XFCE X11 session on Xvfb. [Run 39366148978](https://github.com/goforj/harbor/actions/runs/39366148978) passed creation, Start, trusted HTTPS, close/reopen, native menu quit, retained daemon/project state, relaunch, and Stop using the fresh package with the valid Linux D-Bus identifier. Cleanup then exposed the fixture-unregister requirement above, so the entire workflow did not pass. The pinned Wails GTK3 runtime leaves Ctrl+Q unimplemented; successful quit evidence names the actual native menu action. This is a supplementary fixture, not GNOME Wayland, genuine PolicyKit consent, tray, reboot, or notification proof.

`Installed Windows GUI workflows` separately accepts `package_run_id` from a successful `Windows development package` run. It requires that workflow's exact NSIS installer, checksum, and provenance; macOS/Ubuntu release artifacts cannot substitute. It verifies the actual child token is the same interactive user at medium integrity, with administrator rights denied. Machine helper admission and the disposable signer are test prerequisites. No UAC policy is changed and no production signing claim is made.

Windows clean-installed onboarding is currently blocked by missing product activation, not by a harness setup permission. The installed desktop has an unavailable daemon adapter, and the package lacks an admitted launcher, Windows migration preparation, committed installation selection, and exact per-user scheduled-task lifecycle. The safe implementation order is installation identity and signed component admission, launcher and singleton-guarded preparation, task admission, ownership-aware repair/uninstall, then fresh installed GUI proof. The workflow must expose this gap and retain partial cleanup evidence; a manually prestarted daemon does not prove it closed. The hosted Windows Server interactive fixture is also separate from the supported Windows client-desktop profile.

`Windows native capacity diagnostics` isolates repeated `/24` creation, exact replay, same-port listeners, and ownership cleanup. It retains direct/table address readbacks and socket observations on failure. It preserves strict native attribute checks; the intermittent flag drift and socket reuse failures are unresolved release evidence gaps.

[Windows GUI run 39364806331](https://github.com/goforj/harbor/actions/runs/39364806331) rejected the actual suspended installed child before execution: its medium-integrity restricted token still reported elevation with no linked token on the hosted Administrator profile. The exact child was retired and a partial receipt retained. [Capacity run 39364811878](https://github.com/goforj/harbor/actions/runs/39364811878) independently confirmed the unexpected `SkipAsSource=0` through both IP Helper read APIs. Neither result is a Windows GUI success, a resolved native flag issue, or permission to weaken admission.

## Latest package rehearsal (2026-10-19)

[Build-only run 39363377263](https://github.com/goforj/harbor/actions/runs/39363377263) passed on product source `1014280f3cbf652fab0d126b14c11257820ae659`, including the bounded total readiness deadline, close-before-proof socket ordering, and valid Linux D-Bus name. Both native packages use released GoForj v0.34.1 and retained complete fresh-database, installed daemon adapter reopen/restart, trusted HTTPS, user-data preservation, and uninstall receipts. Assembly passed; publication was skipped. This package set is the current Unix GUI input.

The earlier [39354344032](https://github.com/goforj/harbor/actions/runs/39354344032) passed at `a689e093ce3c8906f15344fd7b062f2b324bff3b`, now merged through PR #12 as `74adee22d2be81ee3699fa245b7269f4d6c49733`. It predates the three product corrections above and was the first installed GUI input.

### Earlier rehearsal

[Build-only run 39339451091](https://github.com/goforj/harbor/actions/runs/39339451091) passed on merged main commit `03e6c52759f65178cdcfb0bc103ac5e6cedb2a7a`, using rehearsal version `0.1.0-beta.1`. The macOS 26 Apple-silicon PKG and Ubuntu 24.04 amd64 DEB both passed clean native installation, packaged daemon/CLI networking setup, three generated GoForj v0.34.0 projects with distinct system-trusted HTTPS responses, terminal networking cleanup, and native package removal. Ubuntu preserved a foreign package sentinel. macOS additionally preserved a mismatched fixed helper and foreign administrator trust, and restored its temporary authorization policy.

Asset assembly verified the exact tested package digests and retained `Harbor-prerelease-publication` with both packages, their checksums, `SHA256SUMS`, source/run provenance, and preview notes. The publish job was skipped. No Harbor tag or release was created. Artifacts have finite Actions retention; the run remains the source of evidence rather than a public download channel.

Artifact review found that macOS retained its lifecycle summary but omitted `package-uninstall.txt`: its uninstall step referenced an environment variable scoped to an earlier step, and the job log contains a failed write to `/package-uninstall.txt` despite the green step. The workflow now writes that receipt through `GITHUB_WORKSPACE`, enables strict unset-variable handling, and explicitly rejects a receipt-write failure. Shell syntax, successful writing with the earlier variable absent, and failure rejection with an unavailable destination were checked using an isolated package fixture. The downloaded run predates that correction; its uninstall assertions are recorded in the job log, not in a retained uninstall receipt. A future native package run must verify receipt retention before publication.

The same main commit passed [push CI](https://github.com/goforj/harbor/actions/runs/39338522521), including root-run macOS and Linux bootstrap idempotency/runtime-preservation tests. Focused local installer, release-bundle, install-path, bootstrap, and release-mode macOS desktop daemon-prerequisite tests also passed. These results do not close the installed desktop gates below.

## Installed desktop gates before a public beta

The `fix/installed-daemon-lifecycle` implementation addresses the two audited gaps below and adds fresh-database startup, installed daemon adapter reopen/restart, and post-uninstall data-preservation checks to both native package workflows. The canonical macOS LaunchAgent is unchanged; the stable launcher now supplies migrations and foreground startup under the existing singleton lock. [Build-only run 39343779111](https://github.com/goforj/harbor/actions/runs/39343779111) passed on product source `86e2121941facd864e0dab819ec5275c7f33ce26`. Both native platforms retained complete installed-daemon evidence and uninstall receipts proving user database/project files preserved and the user service absent. Assembly passed and publication was skipped. The earlier rehearsal is not evidence for this new behavior.

The package acceptance jobs exercise both direct packaged CLI operation and the production daemon-activation adapter used by the desktop. They do not open a Wails window or operate its tray. A green job does not establish the complete graphical install, open-app, create, reopen, and uninstall experience.

The 2026-10-19 source audit found two concrete gaps:

- Ubuntu has a desktop shortcut, but `desktop/internal/daemonprerequisite/platform_other.go` returns an unavailable adapter on Linux. The installed desktop cannot start its daemon on a clean machine through that boundary.
- A release macOS desktop creates `~/Library/LaunchAgents/com.goforj.harbor.daemon.plist`. The current `cmd/installer` uninstall transaction verifies and removes system package paths, but does not stop or remove that per-user daemon registration. The package test starts the daemon directly and therefore does not exercise this path.

Those daemon activation and ownership-aware cleanup paths are implemented and natively verified by the adapter gate above. Before advertising an end-to-end installed desktop beta, verify the actual installed graphical onboarding path as well. Preserve the user's durable data, checkouts, container volumes, and foreign service definitions. Do not install or uninstall the package over an active source-development installation to substitute for a disposable-machine proof.

The privileged bootstrap tests already run on macOS and Linux CI and verify repeated helper installation preserves fixture runtime data. This is narrower than repairing a complete installed product with active projects. The package lifecycle verifies checkout preservation before its own explicit test-workspace cleanup; it does not exercise real Docker-volume preservation across uninstall. Genuine interactive consent, installed GUI onboarding, reboot, and signed updates remain separate gates.
