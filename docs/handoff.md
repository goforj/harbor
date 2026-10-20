# Development Handoff

## Current continuation (2026-10-20)

The exact-source hosted GUI lifecycle and normal cleanup receipts now pass for macOS, Ubuntu, and Windows at product source `a537b52`. See the evidence matrix and platform limits in [current state](./current-state.md). Three same-package Windows GUI runs passed lifecycle and cleanup. Readiness took about 102.85, 103.23, and 109.30 seconds; the earlier roughly 128-second timeout did not recur, but its exact cause remains unknown. No npm logs met the attribution rules for retention. No product timing, configuration, or cache warmup was changed. Do not infer genuine consent, a supported Windows client/UAC profile, release readiness, or containment from the hosted fixture. No Harbor merge, tag, or publication is authorized by that evidence. The user separately authorized the GoForj merge, release, local installation, and Harbor validation; GoForj v0.35.1 is now released and installed locally.

## Released CLI validation (2026-10-20)

GoForj PR #253 is merged and released as v0.35.1 at `09b41ca77b523c0724545ecc20b18581d9cae06c`. The canonical local CLI reports v0.35.1 and was installed from the published module without a replacement. [Harbor CI](https://github.com/goforj/harbor/actions/runs/39427059491) passes all 15 jobs. [Windows GUI](https://github.com/goforj/harbor/actions/runs/39427473351), [macOS GUI](https://github.com/goforj/harbor/actions/runs/39423865305), and [Linux GUI](https://github.com/goforj/harbor/actions/runs/39423865351) pass the fresh package lifecycle and retain user data. The earlier Windows failure was a UI Automation selector before GUI Stop, not database readiness or a Windows host-shell requirement.

## Immediate next goal

Review [PR #13](https://github.com/goforj/harbor/pull/13) for integration against the accepted evidence matrix. The earlier Windows timeout remains unexplained. Production signing, genuine interactive consent, additional desktop profiles, and recovery/update proof remain separate work. Harbor now selects released GoForj v0.35.1 across generated fixtures and CLI workflow pins. Fresh full CI, Docker lifecycle, native platform smoke, and installed GUI lifecycle/cleanup/uninstall pass on all three hosted profiles; exact receipts are in current state. Package source is `76662ca`, Windows harness is `006915c`, and the remaining changes are documentation. The Windows selector fix is harness-only and preserves ordinal matching and ambiguity rejection. Preserve active developer installations.

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
