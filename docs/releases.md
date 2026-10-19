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
