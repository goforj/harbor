# Development Handoff

## Current continuation (2026-10-20)

The active goal is a fully working native installer and graphical lifecycle on macOS, Ubuntu, and Windows. This goal is not complete, and no public release or tag is authorized.

macOS and Ubuntu have full installed GUI runs for earlier exact packages. macOS run [39381799231](https://github.com/goforj/harbor/actions/runs/39381799231) used package run [39373153230](https://github.com/goforj/harbor/actions/runs/39373153230), source `5b691b84ca6f2a2104fbd393177ad7ba7b08f46c`. Ubuntu run [39367877717](https://github.com/goforj/harbor/actions/runs/39367877717) used package run [39363377263](https://github.com/goforj/harbor/actions/runs/39363377263), source `1014280f3cbf652fab0d126b14c11257820ae659`. Both passed their declared GUI lifecycle, cleanup, uninstall, and retained-data assertions without invoking failure recovery. These runs predate the final combined source and need fresh matching package and GUI runs after Windows is stable.

Windows now installs the exact fresh package. The original bootstrap parent exits before the copied uninstall continuation finishes authenticating the parent image and token. Change `1cae93e` adds a typed acknowledgement; native verification is pending. Native component gate [39389556850](https://github.com/goforj/harbor/actions/runs/39389556850) passes at source `739e9fb`, but it does not prove installed GUI or uninstall completion. The hosted Server 2025 profile is not Windows client/UAC evidence. Public Windows signing remains gated. Preserve the protected signed maintenance copy and result receipt as the intentional post-uninstall audit cache.

The full root CI run [39388769856](https://github.com/goforj/harbor/actions/runs/39388769856) passed 14 of 15 checks. The remaining Docker-project readiness timeout has an isolated diagnostic [39391099494](https://github.com/goforj/harbor/actions/runs/39391099494), pending. Keep it separate from package and GUI acceptance.

Known evidence limits:

- macOS run 39381799231 proves its hosted graphical lifecycle, but not genuine interactive consent, final fixture trust-root removal, full trust-store restoration, or failure recovery. A separate final-root removal attempt timed out after 30 seconds.
- Ubuntu run 39367877717 is Ubuntu 24.04 XFCE/X11 on Xvfb with AT-SPI, not GNOME Wayland or genuine PolicyKit consent. It proves the native menu quit; the pinned GTK runtime does not implement Ctrl+Q. Failure recovery was skipped.
- Windows hosted Server 2025 does not prove a supported Windows client-desktop, filtered-token/UAC, separate administrator, or genuine secure-desktop consent profile. The installed GUI/uninstall gate remains open while the typed acknowledgement awaits native verification.
- None of these runs proves completion for a later combined source. Do not infer release readiness from component or root CI alone.

## Immediate next goal

Verify the typed acknowledgement from change `1cae93e` on the native Windows package, then rerun the installed GUI lifecycle against the same exact product source, and retain the package, provenance, lifecycle, cleanup, and uninstall evidence. Once Windows is stable, run fresh macOS and Ubuntu package and GUI gates against that combined source. Resolve the pending Docker diagnostic and rerun the required full CI. Update [Current implementation state](./current-state.md) and [Publishing development previews](./releases.md) with final run links, exact source SHAs, retained artifacts, and only the coverage each run actually proves.

Do not publish or create a tag. Public Windows signing remains gated. Preserve the active developer installations.

## Product invariants

- `harbord` is the sole durable-state writer and reconciler. The CLI, desktop, and tray are clients.
- GoForj owns project composition and `forj dev`; Harbor orchestrates it and does not recreate `.goforj.yml` semantics.
- Each project retains its own Apps, Compose services, containers, credentials, versions, and volumes.
- The daemon and desktop remain unprivileged. Machine changes use explicit one-shot allowlisted helper operations.
- A port, PID, path, command line, or environment file is not process-ownership proof. Revalidate exact native process identity and scope before signaling; send no signal when evidence drifts or is ambiguous.
- One failed or quarantined project must not prevent the daemon or unrelated projects from operating.
- Do not wipe durable state, delete a checkout, remove volumes, or mutate foreign host state as ordinary recovery.
- Cross-platform support requires native OS evidence. Cross-compilation and hosted functional fixtures do not prove genuine consent, client-desktop behavior, reboot recovery, Docker-provider behavior, signing, or release readiness.

See [Delivery plan](./delivery-plan.md) for the broader product objective and [Testing](./testing.md) for required native profiles and release proof. Older implementation handoffs were intentionally removed here; the source tree and linked run artifacts are authoritative for present behavior.
