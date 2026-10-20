# Development Handoff

## Current continuation (2026-10-20)

The exact-source hosted GUI lifecycle and normal cleanup receipts now pass for macOS, Ubuntu, and Windows at product source `a537b52`. See the evidence matrix and platform limits in [current state](./current-state.md). Three same-package Windows GUI runs passed lifecycle and cleanup. Readiness took about 102.85, 103.23, and 109.30 seconds; the earlier roughly 128-second timeout did not recur, but its exact cause remains unknown. No npm logs met the attribution rules for retention. No product timing, configuration, or cache warmup was changed. Do not infer genuine consent, a supported Windows client/UAC profile, release readiness, or containment from the hosted fixture. No Harbor merge, tag, or publication is authorized by that evidence. The user separately authorized the GoForj merge, release, local installation, and Harbor validation; GoForj v0.35.1 is now released and installed locally.

## Immediate next goal

Review [PR #13](https://github.com/goforj/harbor/pull/13) for integration against the accepted evidence matrix. The earlier Windows timeout remains unexplained. Production signing, genuine interactive consent, additional desktop profiles, and recovery/update proof remain separate work. Harbor now selects released GoForj v0.35.1 across generated fixtures and CLI workflow pins. Complete the fresh Docker and hosted platform tests, retain exact run receipts, and update current state before handing back. Preserve active developer installations.

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
