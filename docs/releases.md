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

XCTest operates the installed Wails window through macOS accessibility, rather than injecting JavaScript or replacing the package executable. Its planned coverage is graphical creation of a minimal Vue/API project, Start, system-trusted HTTPS, close-to-hide, single-instance reopen, Quit Harbor UI without stopping the project, another launch, and Stop. The job then removes owned networking, uninstalls, and checks retained database and project-file hashes. Screenshots, the `.xcresult`, lifecycle evidence, and original package provenance are uploaded as `Harbor-installed-macos-ui-<run-number>`.

A failed GUI assertion keeps the gate failed and attempts bounded recovery through the normal native quit, project Stop, networking release, daemon stop, and installer uninstall paths. It refuses registrations outside its disposable fixture and retains `failure-cleanup.json`, including partial failure. Recovery verifies exact service absence and user-file hashes; worker destruction alone is not recorded as successful cleanup.

This is unattended GUI automation in a real graphical session. It installs the released GoForj CLI and Wire, and temporarily exposes that toolchain through the worker's graphical-session `PATH`. Administrator approval is preauthorized on the disposable worker and restored afterward. Genuine consent prompts, default desktop toolchain discovery, Docker-backed projects, tray menu interactions, reboot, signed updates, and installed Linux/Windows GUI workflows remain separate coverage. A hosted result is still pending for this harness.

## Latest package rehearsal (2026-10-19)

[Build-only run 39354344032](https://github.com/goforj/harbor/actions/runs/39354344032) passed on product source `a689e093ce3c8906f15344fd7b062f2b324bff3b`, now merged through PR #12 as `74adee22d2be81ee3699fa245b7269f4d6c49733`. Both native packages use released GoForj v0.34.1 and retained complete fresh-database, installed daemon adapter reopen/restart, trusted HTTPS, user-data preservation, and uninstall receipts. Assembly passed; publication was skipped. This is the package set selected for the first installed GUI rehearsal.

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
