# Development Handoff

## Current continuation (2026-10-20)

Installed crash recovery and peer-isolation acceptance is complete for the named hosted macOS, Linux, and Windows GUI profiles at product source `2f3ace995aafbced8cca3568e4a00a4a75686223`. The user authorized merging [PR #17](https://github.com/goforj/harbor/pull/17); it is merged into `main` at `cbbfd14b674e63a320d3fd0098e2849db044c1d6`. The integration checkout is `/tmp/harbor-package-ui-20261019`, isolated from active developer checkouts and installations. The subsequent documentation baseline is `ffbfecb`, with the active feature branch described below. The three existing GUI workflows share one driver; no new workflow was added. Product fixes allow bounded five-minute cold startup and re-observe a narrowly identified Unix exit race without widening signaling authority. Documentation-only follow-ups preserve the tested product tree.

All final installed GUI runs pass: macOS `39480629419` (artifact `12290529180`), Linux `39480706330` (artifact `12292390153`), and Windows `39479964857` (artifact `12290218682`). Their package producers are Unix `39478906159` and Windows `39478910510`. Core CI `39478820969`, platform smoke `39478821152`, Windows capacity `39478821340`, and the cancelled duplicate smoke's full rerun `39478811512` pass. [Current state](./current-state.md) records hashes, exact evidence, source attribution, and platform boundaries.

Independent correctness and security reviews of PR #17 found no merge blockers. Their focused Go/race and 30 Python checks passed, and the security review independently verified the native artifact receipts. Optional follow-ups are direct peer-source hashing across unregister and generation-bound Unix service crash injection. Earlier [PR #13](https://github.com/goforj/harbor/pull/13) is merged at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`, including PR #16; PRs #14 and #15 were closed as superseded. GoForj v0.35.1 is released and installed locally. PR #17 was merged at the user's request. No new Harbor tag, public release, or change to the user's active installation was performed.

## Released CLI validation (2026-10-20)

GoForj PR #253 is merged and released as v0.35.1 at `09b41ca77b523c0724545ecc20b18581d9cae06c`. The canonical local CLI reports v0.35.1 and was installed from the published module without a replacement. [Harbor CI](https://github.com/goforj/harbor/actions/runs/39427059491) passes all 15 jobs. [Windows GUI](https://github.com/goforj/harbor/actions/runs/39427473351), [macOS GUI](https://github.com/goforj/harbor/actions/runs/39423865305), and [Linux GUI](https://github.com/goforj/harbor/actions/runs/39423865351) pass the fresh package lifecycle and retain user data. The earlier Windows failure was a UI Automation selector before GUI Stop, not database readiness or a Windows host-shell requirement.

## Active milestone: transparent daemon restart

The user authorized implementation of restart continuity. Work is isolated on `feat/transparent-daemon-recovery` in `/tmp/harbor-package-ui-20261019`, based on `ffbfecb`. All current uncommitted changes there belong to this milestone. The first feature commit is `97763ee`; subsequent corrections tighten replay freshness, respect restrictive outer Windows jobs, fence detached owners against new launches, and change installed recovery from explicit retries to exact session continuity. Active developer checkouts, installations, and projects remain untouched. Nothing from this feature has been released or merged.

Ready environments should survive clean daemon shutdown and hard daemon loss, then reconnect with the same process birth, session generation, retained logs, and stable network assignments. Explicit Stop must still settle the complete owned scope. Interrupted starts remain safely retryable. Reboot continuity is a different capability.

GoForj removed its earlier managed integration in `4676161`; the released CLI has no managed client. This implementation uses Harbor's authenticated surviving output broker and separately proven native session/job ownership. It does not restore that removed integration, rewrite project configuration, or require a new CLI release. Unix native continuity and race tests pass locally. Windows work adds a broker-held query handle to a separately owned named Job Object; Windows runtime proof is still pending. Cross-compilation is not acceptance.

The second native CI run `39503589558` passes macOS continuity but Windows falls back from broker handoff. The manifest reader used Unix permissions on Windows; native private ACL creation/admission and Windows failure diagnostics are now added, with native proof pending. A Resume/Close watcher-setup race and retained-job metadata downgrade were corrected. Build-only producers `39503700892` and `39503704067` were cancelled before acceptance because those changes alter product source. No PR exists for this feature yet. Source `0f36e50` gets through Windows broker privacy, native ownership, hard crash, live output, and unexpected leader cleanup. Its only native failure is sharing violation at Stop: journal EOF was visible before its file handle closed. The next correction orders journal closure before subscriber notification and normal EOF cancellation, with ten repetitions of that Windows lifecycle in CI. Linux/macOS native and all desktop checks pass; hosted API smoke `39505134301` passes on all three systems.

Next: finish native Windows ownership and interrupted-operation settlement checks, verify coordinator recovery/close behavior, then run the existing native CI and fresh installed GUI recovery scenarios. Preserve strict package-source admission. The completed PR #17 receipts above remain evidence for the previous crash-and-retry behavior, not this new feature.

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

## Latest continuation: broker flow control

Draft [PR #18](https://github.com/goforj/harbor/pull/18) tracks `feat/transparent-daemon-recovery`. At `0badda5`, Windows passes the initial native continuity suite but the ten-run repeat exposes a startup log stall. A deterministic unbuffered-transport test reproduces the broker command-queue deadlock: acknowledgements cannot drain while the sender blocks on output. The correction moves acknowledgement processing to the independent reader and allows client Close to interrupt an outbound write. Ten local race-enabled repetitions and the full local process race suite pass. The latest product correction still needs native Windows repetition and fresh packages.

Windows producer `39506412675` succeeded at `0badda5`, but do not use it for the corrected feature. Unix producer `39506409177` was cancelled. Auto GUI runs for PR #18 were cancelled or failed admission while fresh matching packages were unavailable; rerun their newest head-specific runs once exact fresh producers succeed. Do not weaken product-source admission. No public release, merge, or user-installation update has occurred.

At `d2bacd0`, Windows native and all ten repeated adoption/Stop lifecycles pass in push CI `39508253651`; macOS native passes too. Fresh build-only producers are Unix `39508579934` and Windows `39508583059`. Keep these running. The current follow-up changes only an unrelated platform-proof unit fixture, its explicit test-only source-admission entry/negative tests, and documentation, so verify product parity with `d2bacd0` before using those packages. The fixture formerly sent EOF at the client's exact cancellation edge; it now stays open until the receiver has returned. Twenty race-enabled local repetitions pass. The latest auto GUI runs for this source must be rerun after the new exact producers succeed. No product changes are planned.
