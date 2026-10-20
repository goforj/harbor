# Development Handoff

## Current continuation (2026-10-20)

Installed crash recovery and peer-isolation acceptance is complete for the named hosted macOS, Linux, and Windows GUI profiles at product source `2f3ace995aafbced8cca3568e4a00a4a75686223`. The user authorized merging [PR #17](https://github.com/goforj/harbor/pull/17); it is merged into `main` at `cbbfd14b674e63a320d3fd0098e2849db044c1d6`. The integration checkout is `/tmp/harbor-package-ui-20261019`, isolated from active developer checkouts and installations. Its current branch records documentation after the merge. The three existing GUI workflows share one driver; no new workflow was added. Product fixes allow bounded five-minute cold startup and re-observe a narrowly identified Unix exit race without widening signaling authority. Documentation-only follow-ups preserve the tested product tree.

All final installed GUI runs pass: macOS `39480629419` (artifact `12290529180`), Linux `39480706330` (artifact `12292390153`), and Windows `39479964857` (artifact `12290218682`). Their package producers are Unix `39478906159` and Windows `39478910510`. Core CI `39478820969`, platform smoke `39478821152`, Windows capacity `39478821340`, and the cancelled duplicate smoke's full rerun `39478811512` pass. [Current state](./current-state.md) records hashes, exact evidence, source attribution, and platform boundaries.

Independent correctness and security reviews of PR #17 found no merge blockers. Their focused Go/race and 30 Python checks passed, and the security review independently verified the native artifact receipts. Optional follow-ups are direct peer-source hashing across unregister and generation-bound Unix service crash injection. Earlier [PR #13](https://github.com/goforj/harbor/pull/13) is merged at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`, including PR #16; PRs #14 and #15 were closed as superseded. GoForj v0.35.1 is released and installed locally. PR #17 was merged at the user's request. No new Harbor tag, public release, or change to the user's active installation was performed.

## Released CLI validation (2026-10-20)

GoForj PR #253 is merged and released as v0.35.1 at `09b41ca77b523c0724545ecc20b18581d9cae06c`. The canonical local CLI reports v0.35.1 and was installed from the published module without a replacement. [Harbor CI](https://github.com/goforj/harbor/actions/runs/39427059491) passes all 15 jobs. [Windows GUI](https://github.com/goforj/harbor/actions/runs/39427473351), [macOS GUI](https://github.com/goforj/harbor/actions/runs/39423865305), and [Linux GUI](https://github.com/goforj/harbor/actions/runs/39423865351) pass the fresh package lifecycle and retain user data. The earlier Windows failure was a UI Automation selector before GUI Stop, not database readiness or a Windows host-shell requirement.

## Immediate next goal

The proposed next milestone is authenticated managed-session attachment in GoForj and Harbor: running projects should reconnect after a daemon restart without being stopped, while preserving logs, addresses, and ownership checks. The existing scaffolding is dormant in production and needs a bounded activation slice plus native recovery evidence. The user has requested a recommendation, not implementation of this milestone. The installed crash/retry scenario is already proven on all three named hosted profiles; further speculative reruns are unnecessary without a new failure or product change. The released GoForj CLI still does not implement authenticated managed attachment, so daemon recovery settles supervised projects and explicit fresh starts restore them. Uninterrupted adoption and unattended service restart remain separate work. Genuine consent, supported client profiles, reboot, update proof, and public Windows production signing are still release concerns. macOS foreign trust preservation passes, but fixture last-root removal remains unproved after a bounded timeout.

Product-source admission remains strict. Documentation and test-harness edits are excluded from product-source comparisons. Both local output-broker cache failures are pre-existing; final hosted root tests pass. Preserve the user's active developer checkouts, running projects, and installed CLI.

## Product invariants

- `harbord` is the sole durable-state writer and reconciler. The CLI, desktop, and tray are clients.
- GoForj owns project composition and `forj dev`; Harbor orchestrates it and does not recreate `.goforj.yml` semantics.
- Each project retains its own Apps, Compose services, containers, credentials, versions, and volumes.
- The daemon and desktop remain unprivileged. Machine changes use explicit one-shot allowlisted helper operations.
- A port, PID, path, command line, or environment file is not process-ownership proof. Revalidate exact native process identity and scope before signaling; send no signal when evidence drifts or is ambiguous.
- One failed or quarantined project must not prevent the daemon or unrelated projects from operating.
- Do not wipe durable state, delete a checkout, remove volumes, or mutate foreign host state as ordinary recovery.
- Cross-platform support requires native OS evidence. Cross-compilation and hosted functional fixtures do not prove genuine consent, client-desktop behavior, reboot recovery, Docker-provider behavior, signing, or release readiness.

See [Delivery plan](./delivery-plan.md) for the broader product objective and [Testing](./testing.md) for required native profiles and release proof. The source tree and linked run artifacts are authoritative for present behavior.
