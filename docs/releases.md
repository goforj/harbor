# Publishing development previews

Harbor uses short-lived feature branches, pull requests into `main`, and immutable version tags. A release rehearsal builds and verifies native packages without publication. A rehearsal does not create a tag or GitHub release.

## Current installed-platform evidence

The installed native lifecycle is still in progress. The latest full macOS GUI proof is [39381799231](https://github.com/goforj/harbor/actions/runs/39381799231), using package run [39373153230](https://github.com/goforj/harbor/actions/runs/39373153230). The latest full Ubuntu GUI proof is [39367877717](https://github.com/goforj/harbor/actions/runs/39367877717), using package run [39363377263](https://github.com/goforj/harbor/actions/runs/39363377263). Each passed its tested GUI and cleanup lifecycle, but both packages predate the final combined source. Their hosted profiles and explicit limits are recorded in [Current implementation state](./current-state.md#installed-platform-evidence-2026-10-20).

The exact fresh Windows package now installs, and native component gate [39389556850](https://github.com/goforj/harbor/actions/runs/39389556850) passes at source `739e9fb`. The uninstall continuation exits before its parent observes the ready-event handoff. Windows installed GUI and uninstall acceptance therefore remain open. Public Windows signing is also gated. The Windows Server 2025 fixture does not establish supported Windows client/UAC behavior. The protected signed maintenance copy and receipt are intentionally retained as an audit cache after uninstall.

Full root CI [39388769856](https://github.com/goforj/harbor/actions/runs/39388769856) passed 14 of 15 checks. Its outstanding Docker-project readiness timeout has a separate pending diagnostic [39391099494](https://github.com/goforj/harbor/actions/runs/39391099494). Do not treat package or component checks as a substitute for the installed GUI gate or full CI.

No public release or tag is authorized by the current work. The final combined source still needs fresh matching package and GUI evidence on macOS, Ubuntu, and Windows.

## Rehearse without publishing

The `Prerelease artifacts` workflow admits only `vMAJOR.MINOR.PATCH-beta.NUMBER` and `vMAJOR.MINOR.PATCH-rc.NUMBER`. It requires the tagged commit to be on `main` with successful push CI for that exact commit. Stable tags fail preflight while signing and the full product release gates remain unfinished.

To rehearse without publishing, dispatch `Prerelease artifacts` on the implementation branch with a full version such as `0.1.0-beta.1`. The package jobs run and assembly verifies the tested packages and checksums; the publication job is skipped. The `Harbor-prerelease-publication` artifact contains the reviewable result. No tag or public release is created by this rehearsal.

The existing public asset path supports an unsigned, unnotarized Apple-silicon PKG and an unsigned Ubuntu 24.04 amd64 DEB. Windows publication is gated until production signing replaces the disposable CI certificate. Never publish the Windows development workflow's installer: its eight-hour CI signing identity is installed into machine certificate stores for its isolated test.

After approval, merge, and successful main CI, a maintainer can create and push the next unused preview tag:

```sh
git fetch origin main
git tag -a v0.1.0-beta.1 <tested-main-commit> -m "Harbor 0.1.0-beta.1"
git push origin v0.1.0-beta.1
```

Choose the next unused version rather than copying the example. Repository tag rules prevent deletion or update of `v*` tags. Do not move a published tag to repair a failure. If packaging or validation fails, fix the source through a PR and use a new version tag. Inspect and remove only an incomplete draft if asset attachment failed; publication refuses to overwrite an existing release or its assets.

This path publishes development previews, not a signed product release or automatic update channel. Signing, genuine interactive consent, reboot recovery, and signed update evidence remain governed by [Installation and release](./installation-and-release.md) and [Testing](./testing.md).

## Installed GUI workflow boundaries

The GUI workflows admit exact package bytes from successful package runs, verify checksums and provenance, and require product source to match the package source. Harness and documentation changes may differ. Product changes require a fresh package rehearsal.

The macOS gate drives the installed Wails window through XCTest in a GitHub-hosted macOS 26 Apple-silicon graphical session. The Ubuntu gate drives the installed GTK/WebKit executable through AT-SPI and native input in Ubuntu 24.04 XFCE/X11 on Xvfb. Its passing quit action is the native menu; the pinned GTK runtime does not implement Ctrl+Q. These runs do not prove genuine consent or failure recovery. macOS final trust-root removal and full trust-store restoration remain unproved. Ubuntu X11 evidence does not establish GNOME Wayland behavior.

The Windows gate accepts only the exact package from the Windows development package workflow. Its hosted Server 2025 profile is a functional fixture, not a supported Windows client/UAC or genuine secure-desktop consent profile. Current native component evidence passes, but the installed uninstall continuation has not completed its parent ready-event handoff. Do not report Windows installed GUI acceptance until a fresh exact package passes the complete lifecycle and cleanup checks.

## Isolated Docker diagnostics

Use `docker-project-diagnostics.yml` to exercise the generated MySQL project acceptance job without rebuilding the desktop matrix. Its canonical source is the `docker-adapter` job in `ci.yml`; regenerate the diagnostic workflow with `python3 scripts/packageui/render_docker_workflow.py` after changing that job. A failed run retains bounded disposable-fixture traces and state/output snapshots under `failure-diagnostics`; these files are diagnostic evidence, not a successful product proof.
