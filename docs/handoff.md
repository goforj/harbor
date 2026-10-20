# Development Handoff

## Current continuation (2026-10-20)

The user authorized installed-app crash recovery and peer-isolation checks. Work is on `test/installed-crash-recovery-20261020` in `/tmp/harbor-package-ui-20261019`, isolated from the active developer checkouts and installations. The three existing installed GUI workflows share `scripts/packageui/crashrecovery`; no additional workflow was added. Source review found that GoForj v0.35.1 does not perform managed attachment, so the released contract is safe settlement and explicit fresh retry after a daemon crash. Uninterrupted adoption remains unimplemented for this CLI. The driver asserts retired sessions, withdrawn routes, stable distinct backend assignments, trusted HTTPS after retries, an exact retryable interrupted operation, no orphan gate listener, and isolation during ordinary peer lifecycle actions. Inactive public DNS may be withdrawn; Ready retries must restore it. The latest source and native evidence are recorded in [current state](./current-state.md).

The user authorized integration, and [PR #13](https://github.com/goforj/harbor/pull/13) is merged into `main` at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`. PR #16 was included and is marked merged. PRs #14 and #15 were closed as superseded after verifying that their changes were preserved. The new recovery work is draft [PR #17](https://github.com/goforj/harbor/pull/17). GoForj v0.35.1 is released and installed locally. Earlier hosted installed GUI lifecycle, cleanup, and uninstall receipts pass for macOS, Ubuntu, and Windows; exact evidence and platform limits are in [current state](./current-state.md). The earlier Windows timeout remains unexplained. These receipts do not establish genuine consent, a supported Windows client/UAC profile, release readiness, or containment. No Harbor tag or publication was requested or created.

## Released CLI validation (2026-10-20)

GoForj PR #253 is merged and released as v0.35.1 at `09b41ca77b523c0724545ecc20b18581d9cae06c`. The canonical local CLI reports v0.35.1 and was installed from the published module without a replacement. [Harbor CI](https://github.com/goforj/harbor/actions/runs/39427059491) passes all 15 jobs. [Windows GUI](https://github.com/goforj/harbor/actions/runs/39427473351), [macOS GUI](https://github.com/goforj/harbor/actions/runs/39423865305), and [Linux GUI](https://github.com/goforj/harbor/actions/runs/39423865351) pass the fresh package lifecycle and retain user data. The earlier Windows failure was a UI Automation selector before GUI Stop, not database readiness or a Windows host-shell requirement.

## Immediate next goal

Produce fresh Unix and Windows packages for the bounded native-session observation fix, then run all three installed GUI workflows against those exact packages. Product source `a30d76b` already has fresh package producers `39472788655` and `39472793212`, full Linux installed recovery pass `39474148770`, green core CI/platform smoke/Windows capacity, and Windows helper completion `39475822102`; its corrected graphical navigation is running in `39478111088`. macOS `39474173073` quarantined a peer, with no retained underlying native error. Independently reproduced empty-census/live-leader exit races motivated the narrow 50 ms re-observation fix, with 90 native test-owned settlements and focused independent race tests passing. Do not attribute the old hosted failure conclusively until fresh installed evidence passes. Persistent ambiguity still quarantines, and no process-signaling authority changed. Two unrelated output-broker tests force the damaged shared module cache locally. Recovery starts the installed manager explicitly and does not prove unattended automatic restart. Production signing, genuine interactive consent, supported client profiles, reboot, and update proof remain separate work.

Product-source admission remains strict. Documentation and test-harness edits are excluded from product-source comparisons.

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
