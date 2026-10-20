# Development Handoff

## Current continuation (2026-10-20)

The active goal is to complete and verify the native installer and graphical lifecycle on macOS, Ubuntu, and Windows. Do not claim all three platforms complete, and do not merge, tag, or publish from this work.

The final Unix package producer [39394942316](https://github.com/goforj/harbor/actions/runs/39394942316) passed at product source `c808886`; publication was skipped. Its exact PKG SHA-256 is `5e4313ddc44067de1d72edf84fd353a3f4f10adc06863f93f493f83e0a43e8ad`, and its DEB SHA-256 is `d396e2dcea982bd7f738bd462e479f97cbe253071fe60f22cedb4d4634336139`. macOS installed GUI run [39396200673](https://github.com/goforj/harbor/actions/runs/39396200673) passed cold creation, Ready/trusted HTTPS, hide/reopen, duplicate handling, Quit/relaunch, Stop, fixture unregister, networking release, uninstall, and three nonempty hashes on that exact PKG. The fixture's foreign administrator root could not be separately removed within its timeout; genuine consent, final trust-root removal, full trust-store restoration, and failure recovery are not proved.

Final Ubuntu installed GUI run [39398129238](https://github.com/goforj/harbor/actions/runs/39398129238) is still running after fixes for a networking-state race and native keyboard activation. Do not record it as passing yet. Earlier baseline [39367877717](https://github.com/goforj/harbor/actions/runs/39367877717) remains evidence only for its earlier package and source. Its XFCE/X11-on-Xvfb environment does not establish GNOME Wayland, genuine PolicyKit consent, or failure recovery.

Windows native component gate [39393903198](https://github.com/goforj/harbor/actions/runs/39393903198) and package producer [39394186110](https://github.com/goforj/harbor/actions/runs/39394186110) passed on exact product source `9d704e05d0eab64d58520a9dad40604ff01d109e`. The package installs, handoff and `SystemDrive` environment issues are fixed, and the uninstall receipt is correct. The installed GUI opens and starts an elevation-zero daemon; native UAC approval remains pending. A preauthorized high-controller fixture is being prepared with the unchanged installed signed-helper protocol. This is not genuine GUI-consent evidence. Keep actual product-token facts and protected machine/IPC probes mandatory. The extra cross-high observer is limited to the standalone profile; the product receipt says it was skipped and makes no containment claim. The hosted Server 2025 profile is not Windows client/UAC evidence. Public Windows signing remains gated.

Full root CI [39397089966](https://github.com/goforj/harbor/actions/runs/39397089966) passed all 15 checks at `9023875`. The MySQL Docker lifecycle and cleanup passed for all three projects in [39396134671](https://github.com/goforj/harbor/actions/runs/39396134671). The unreleased GoForj candidate in [PR #253](https://github.com/goforj/goforj/pull/253) addresses true first-boot readiness by waiting for authenticated permanent-server readiness; three fresh native containers passed in [39395622504](https://github.com/goforj/goforj/actions/runs/39395622504). Candidate [`7e38e5f041cc2f449f5ec33baaaa2139ac4b6b50`](https://github.com/goforj/goforj/commit/7e38e5f041cc2f449f5ec33baaaa2139ac4b6b50) is unreleased and is pinned only for this CI acceptance path. No Harbor API, generated project configuration, persisted data, or minimum Go version changed.

## Immediate next goal

Wait for the final Ubuntu GUI run and record its result. Complete the Windows preauthorized controller fixture while preserving the installed signed-helper protocol, then verify the installed lifecycle and cleanup using actual product-token facts and protected machine/IPC probes. Keep the Windows GUI consent claim open unless genuine native UAC approval is exercised. Reconcile the remaining platform evidence against exact source and artifact digests before any release discussion.

Product source admission stays strict at `c808886` for Unix and `9d704e05d0eab64d58520a9dad40604ff01d109e` for Windows. Documentation and test-harness edits are excluded exactly from product-source comparisons. Preserve active developer installations.

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
