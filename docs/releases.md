# Publishing development previews

Harbor uses short-lived feature branches, pull requests into `main`, and immutable version tags. A release rehearsal builds and verifies native packages without publication. A rehearsal does not create a tag or GitHub release.

For public downloads, tested profiles, setup, and practical troubleshooting, start with [Beta support and recovery](./beta-support.md).

## Published preview

[v0.1.0-beta.1](https://github.com/goforj/harbor/releases/tag/v0.1.0-beta.1) is Harbor's first public unsigned development preview. Its immutable tag points to `1e71ce47e5aaa07eeeb0f615a822dd49ca46770b`, which passed [main push CI](https://github.com/goforj/harbor/actions/runs/39519520985). [Release run 39520200187](https://github.com/goforj/harbor/actions/runs/39520200187) passed preflight, both native package gates, assembly, and publication.

| Download | Tested SHA-256 |
| --- | --- |
| `GoForj-Harbor-macos-arm64-0.1.0-beta.1.pkg` | `455bbd6721cd4e16cf36cf0d727e5ecda4274eea95decdaf2f51981a7d51e3e0` |
| `goforj-harbor_0.1.0.beta.1-1_amd64.deb` | `b4f5ffad30cf6fd5696fa386a4611f7cc782c0fd0fe8c70b5a73ac3ed3039344` |

The published downloads match the exact tested runner packages byte-for-byte. Both native gates prove package installation, three-project trusted HTTPS, desktop daemon activation/reopen, ownership-aware uninstall, and preserved user data. The same product source passed the full installed GUI and daemon-crash continuity profiles on macOS, Ubuntu, and Windows listed in [current state](./current-state.md#transparent-daemon-restart). Public Windows installers remain gated on production signing; no disposable-signed Windows artifact was published.

GitHub replaces `~` in release asset names with `.`. The Debian package's internal version retains its Debian prerelease ordering, while its public download uses the period spelling above. First-publication verification caught stale filename references in the checksum sidecar, `SHA256SUMS`, and provenance. Those metadata files were corrected without changing package bytes or moving the tag. The release helper now normalizes publication filenames before sealing, and the regression test resolves every checksum and provenance entry against the actual downloaded filename.

## Earlier installed-platform evidence

At product source `a537b52`, full CI passed all 15 jobs, the exact-source Unix package producer passed with publication skipped, and hosted GUI lifecycle plus normal cleanup receipts passed for macOS, Ubuntu, and Windows. The Windows GUI completed normal cleanup and preserved three nonempty data hashes in [run 39412874662](https://github.com/goforj/harbor/actions/runs/39412874662). HTTP readiness took about 102.85 seconds in [39412874662](https://github.com/goforj/harbor/actions/runs/39412874662), 103.23 seconds in [39414837174](https://github.com/goforj/harbor/actions/runs/39414837174), and 109.30 seconds in [39414837212](https://github.com/goforj/harbor/actions/runs/39414837212). All three GUI lifecycles and cleanup receipts passed. An earlier attempt timed out at about 128 seconds; it did not recur, but its exact cause remains unknown. No npm logs met the attribution rules for retention. No product timing, configuration, or cache warmup was changed. See [current state](./current-state.md) for exact artifact provenance and profile limits.

The source includes read-only machine Root-store snapshots, Windows-only Wails application-menu registration, and tray status updates serialized on the native main queue without destructive `Menu.Update`. Hosted Server 2025 evidence does not prove genuine Windows GUI consent or supported client/UAC behavior, and public signing remains gated. macOS genuine consent and full trust-store restoration remain unproved; Ubuntu evidence uses XFCE/X11 on Xvfb, not GNOME Wayland or genuine PolicyKit consent. Failure recovery and update proof remain separate gates. Docker acceptance now selects released [GoForj v0.35.1](https://github.com/goforj/goforj/releases/tag/v0.35.1), which includes the first-boot readiness fix. Fresh full CI, native platform smoke, Docker lifecycle, and installed GUI lifecycle plus normal cleanup/uninstall pass against that release; exact runs and package digests are recorded in current state. The installed GUI receipts above retain their original CLI baseline. Details are under [Docker project lifecycle and diagnostics](#docker-project-lifecycle-and-diagnostics).

The user separately authorized integration, and [PR #13](https://github.com/goforj/harbor/pull/13) is merged into `main` at `e82e189`. PR #16 was included; PRs #14 and #15 were closed as superseded with their work preserved. No Harbor tag or public release was requested or created at that integration step. See [current state](./current-state.md#main-integration-2026-10-20) for the main-branch checks.

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

The macOS workflow drives the installed Wails window through XCTest in a GitHub-hosted macOS 26 Apple-silicon graphical session. The Ubuntu workflow drives the installed GTK/WebKit executable through AT-SPI and native input in Ubuntu 24.04 XFCE/X11 on Xvfb. Its passing quit action is the native menu; the pinned GTK runtime does not implement Ctrl+Q. Current full installed receipts additionally prove the specific daemon-crash continuity and interrupted-start recovery in [current state](./current-state.md#installed-onboarding-accepted-2026-10-21). They do not prove genuine consent, reboot continuity, or every failure mode. macOS final trust-root removal and full trust-store restoration remain unproved. Ubuntu X11 evidence does not establish GNOME Wayland behavior.

The Windows gate uses the exact package from the Windows development package workflow. Its hosted Server 2025 profile is a functional fixture, not a supported Windows client/UAC or genuine secure-desktop consent profile. The focused native menu probe [39407787846](https://github.com/goforj/harbor/actions/runs/39407787846) verifies real Ctrl+Q, UI-process exit, daemon survival, and cleanup. The accepted full GUI run completes normal cleanup, but does not establish a supported Windows client/UAC profile or genuine GUI consent. Actual product-token facts and protected machine/IPC probes remain mandatory. The extra cross-high observer is limited to the standalone profile; the product receipt explicitly records it as skipped and makes no containment claim. Do not report GUI-consent evidence from the preauthorized fixture.

## Docker project lifecycle and diagnostics

The earlier three-project MySQL lifecycle and cleanup passed in [run 39396134671](https://github.com/goforj/harbor/actions/runs/39396134671), using the unreleased candidate from [GoForj PR #253](https://github.com/goforj/goforj/pull/253). That PR is now merged and published in v0.35.1. Harbor's acceptance fixtures and workflow CLI pins select the released version, whose twelve-version native MySQL/MariaDB matrix passed in [GoForj CI](https://github.com/goforj/goforj/actions/runs/39421351506). Fresh [Harbor Docker validation](https://github.com/goforj/harbor/actions/runs/39423903409) passed all three projects, service refresh, stop/restart, peer isolation, and exact cleanup against the released version. [All 15 CI jobs](https://github.com/goforj/harbor/actions/runs/39427059491) and [native generated-project smoke on all three platforms](https://github.com/goforj/harbor/actions/runs/39423865346) also pass. Fresh packages and installed GUI receipts for v0.35.1 are listed in [current state](./current-state.md#released-goforj-baseline-2026-10-20).

Use `docker-project-diagnostics.yml` to exercise the generated MySQL project acceptance job without rebuilding the desktop matrix. Its canonical source is the `docker-adapter` job in `ci.yml`; regenerate the diagnostic workflow with `python3 scripts/packageui/render_docker_workflow.py` after changing that job. A failed run retains bounded disposable-fixture traces and state/output snapshots under `failure-diagnostics`; these files are diagnostic evidence, not a successful product proof.
