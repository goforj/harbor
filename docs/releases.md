# Publishing development previews

Harbor uses short-lived feature branches, pull requests into `main`, and immutable version tags. A release rehearsal builds and verifies native packages without publication. A rehearsal does not create a tag or GitHub release.

## Current installed-platform evidence

The final Unix package producer [39394942316](https://github.com/goforj/harbor/actions/runs/39394942316) passed at source `c808886`; publication was skipped. The exact PKG SHA-256 is `5e4313ddc44067de1d72edf84fd353a3f4f10adc06863f93f493f83e0a43e8ad`, and the DEB SHA-256 is `d396e2dcea982bd7f738bd462e479f97cbe253071fe60f22cedb4d4634336139`. The exact-source product guard excludes documentation and test-harness changes while keeping product changes matched to the package source.

macOS installed GUI run [39396200673](https://github.com/goforj/harbor/actions/runs/39396200673) passed on the exact PKG, including cold creation, Ready/trusted HTTPS, hide/reopen, duplicate-instance behavior, Quit/relaunch, Stop, fixture unregister, networking release, uninstall, and three nonempty data hashes. The fixture started with a foreign administrator root; its separate removal timed out. Genuine consent, final trust-root removal, full trust-store restoration, and failure recovery remain unproved.

Ubuntu installed GUI run [39399205567](https://github.com/goforj/harbor/actions/runs/39399205567) passed on the exact DEB from producer 39394942316 at source `c808886`; artifact `12254002247` is retained. Creation, Ready/trusted HTTPS 200, close/hide, original-process reopen, GTK menu Quit with the same daemon and project Ready, new-process relaunch with registration and HTTPS retained, Stop, explicit unregister, networking release, uninstall, and three actual nonempty-content hashes passed. All direct children exited zero and failure recovery was skipped. This is XFCE/X11 on Xvfb, not GNOME Wayland or genuine PolicyKit consent; the pinned GTK runtime does not support Ctrl+Q.

Windows native component gate [39393903198](https://github.com/goforj/harbor/actions/runs/39393903198) and package producer [39394186110](https://github.com/goforj/harbor/actions/runs/39394186110) passed on exact source `9d704e05d0eab64d58520a9dad40604ff01d109e`. The package installs and has a correct uninstall receipt; the handoff and `SystemDrive` environment issues are fixed. Installed GUI run [39399716877](https://github.com/goforj/harbor/actions/runs/39399716877) is active at `ee15868` with the reviewed preauthorized high-controller fixture, exact accepted package, and unchanged product source. The GUI opens a window and starts an elevation-zero daemon. Do not infer GUI consent or lifecycle completion while the run is active. Hosted Server 2025 does not establish Windows client/UAC behavior. Public Windows signing remains gated.

Full root CI [39397089966](https://github.com/goforj/harbor/actions/runs/39397089966) passed all 15 checks at `9023875`. The Docker lifecycle evidence and its unreleased GoForj test candidate are detailed under [Docker project lifecycle and diagnostics](#docker-project-lifecycle-and-diagnostics).

Do not claim all three installed platform lifecycles complete. No merge, tag, or public release is authorized by this work.

## Rehearse without publishing

The `Prerelease artifacts` workflow admits only `vMAJOR.MINOR.PATCH-beta.NUMBER` and `vMAJOR.MINOR.PATCH-rc.NUMBER`. It requires the tagged commit to be on `main` with successful push CI for that exact commit. Stable tags fail preflight while signing and the full product release gates remain unfinished.

To rehearse without publishing, dispatch `Prerelease artifacts` on the implementation branch with a full version such as `0.1.0-beta.1`. The package jobs run and assembly verifies the tested packages and checksums; the publication job is skipped. The `Harbor-prerelease-publication` artifact contains the reviewable result. No tag or public release is created by this rehearsal.

The existing public asset path supports an unsigned, unnotarized Apple-silicon PKG and an unsigned Ubuntu 24.04 amd64 DEB. Windows publication is gated until production signing replaces the disposable CI certificate. Never publish the Windows development workflow's installer: its eight-hour CI signing identity is installed into machine certificate stores for its isolated test.

After approval, merge, and successful main CI, a maintainer can create and push the next unused preview tag:

```sh
git fetch origin main
TESTED_MAIN_COMMIT="$(git rev-parse origin/main)"
git tag -a v0.1.0-beta.1 "$TESTED_MAIN_COMMIT" -m "Harbor 0.1.0-beta.1"
git push origin v0.1.0-beta.1
```

Choose the next unused version rather than copying the example. Repository tag rules prevent deletion or update of `v*` tags. Do not move a published tag to repair a failure. If packaging or validation fails, fix the source through a PR and use a new version tag. Inspect and remove only an incomplete draft if asset attachment failed; publication refuses to overwrite an existing release or its assets.

This path publishes development previews, not a signed product release or automatic update channel. Signing, genuine interactive consent, reboot recovery, and signed update evidence remain governed by [Installation and release](./installation-and-release.md) and [Testing](./testing.md).

## Installed GUI workflow boundaries

The GUI workflows admit exact package bytes from successful package runs, verify checksums and provenance, and require product source to match the package source. Harness and documentation changes may differ. Product changes require a fresh package rehearsal.

The macOS workflow drives the installed Wails window through XCTest in a GitHub-hosted macOS 26 Apple-silicon graphical session. The Ubuntu workflow drives the installed GTK/WebKit executable through AT-SPI and native input in Ubuntu 24.04 XFCE/X11 on Xvfb. Its passing quit action is the native menu; the pinned GTK runtime does not implement Ctrl+Q. These profiles do not prove genuine consent or failure recovery. macOS final trust-root removal and full trust-store restoration remain unproved. Ubuntu X11 evidence does not establish GNOME Wayland behavior.

The Windows gate uses the exact package from the Windows development package workflow. Its hosted Server 2025 profile is a functional fixture, not a supported Windows client/UAC or genuine secure-desktop consent profile. Installed GUI run 39399716877 is active with a reviewed preauthorized high-controller fixture and does not establish genuine GUI consent. Actual product-token facts and protected machine/IPC probes remain mandatory. The extra cross-high observer is limited to the standalone profile; the product receipt explicitly records it as skipped and makes no containment claim. Do not report GUI-consent evidence from the preauthorized fixture.

## Docker project lifecycle and diagnostics

The all-three-project MySQL lifecycle and cleanup passed in [run 39396134671](https://github.com/goforj/harbor/actions/runs/39396134671). The unreleased GoForj candidate in [PR #253](https://github.com/goforj/goforj/pull/253) addresses the true first-boot race by waiting for authenticated permanent-server readiness. Three fresh native containers passed in [run 39395622504](https://github.com/goforj/goforj/actions/runs/39395622504). Candidate [`7e38e5f041cc2f449f5ec33baaaa2139ac4b6b50`](https://github.com/goforj/goforj/commit/7e38e5f041cc2f449f5ec33baaaa2139ac4b6b50) is unreleased and pinned only in this CI acceptance path.

Use `docker-project-diagnostics.yml` to exercise the generated MySQL project acceptance job without rebuilding the desktop matrix. Its canonical source is the `docker-adapter` job in `ci.yml`; regenerate the diagnostic workflow with `python3 scripts/packageui/render_docker_workflow.py` after changing that job. A failed run retains bounded disposable-fixture traces and state/output snapshots under `failure-diagnostics`; these files are diagnostic evidence, not a successful product proof.
