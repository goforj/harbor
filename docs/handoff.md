# Development Handoff

## Current continuation (2026-10-20)

The user authorized integration, and [PR #13](https://github.com/goforj/harbor/pull/13) is merged into `main` at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`. PR #16 was included and is marked merged. PRs #14 and #15 were closed as superseded after verifying that their changes were preserved. Neither Harbor nor GoForj has an open PR at this checkpoint. GoForj v0.35.1 is released and installed locally. Fresh hosted installed GUI lifecycle, cleanup, and uninstall receipts pass for macOS, Ubuntu, and Windows; exact evidence and platform limits are in [current state](./current-state.md). The earlier Windows timeout remains unexplained. These receipts do not establish genuine consent, a supported Windows client/UAC profile, release readiness, or containment. No Harbor tag or publication was requested or created.

## Released CLI validation (2026-10-20)

GoForj PR #253 is merged and released as v0.35.1 at `09b41ca77b523c0724545ecc20b18581d9cae06c`. The canonical local CLI reports v0.35.1 and was installed from the published module without a replacement. [Harbor CI](https://github.com/goforj/harbor/actions/runs/39427059491) passes all 15 jobs. [Windows GUI](https://github.com/goforj/harbor/actions/runs/39427473351), [macOS GUI](https://github.com/goforj/harbor/actions/runs/39423865305), and [Linux GUI](https://github.com/goforj/harbor/actions/runs/39423865351) pass the fresh package lifecycle and retain user data. The earlier Windows failure was a UI Automation selector before GUI Stop, not database readiness or a Windows host-shell requirement.

## Immediate next goal

Confirm the remaining main-branch macOS lifecycle and platform smoke checks for integration commit `e82e189`. [CI](https://github.com/goforj/harbor/actions/runs/39459846653) and the fresh native Windows and Linux lifecycle checks already pass. Production signing, genuine interactive consent, additional desktop profiles, and recovery/update proof remain separate work. Package source is `76662ca`, Windows harness is `006915c`, and the integration introduces no additional product changes. The Windows selector fix is harness-only and preserves ordinal matching and ambiguity rejection. Work is isolated in `/tmp/harbor-package-ui-20261019`; preserve the active developer checkouts and installations.

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
