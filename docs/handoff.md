# Development Handoff

## Current continuation (2026-10-20)

The user requested transparent daemon restarts so running environments are retained. Implementation is in draft [PR #18](https://github.com/goforj/harbor/pull/18), branch `feat/transparent-daemon-recovery`, isolated worktree `/tmp/harbor-package-ui-20261019`, based on merged documentation baseline `ffbfecb`. Active developer checkouts, running user projects, and installations remain untouched. All changes in this worktree belong to this milestone; inspect live Git status before editing.

The product source is `d2bacd0a59e200876962c64592843a718c8f955f`. Subsequent changes are limited to an unrelated unit-fixture race, its exact test-only admission entry/negative tests, and documentation. Product parity with that source has been verified through the package verifier. No further product change is planned. Rebuild packages if product parity changes; never weaken admission to reuse stale binaries.

Ready/Degraded sessions reconnect through separately authenticated output brokers and exact native process scope. Clean daemon shutdown detaches supported environments instead of stopping them. Windows broker query handles retain separately owned named jobs; root birth and membership remain mandatory for restored Stop authority. Unix retains its dedicated native session. Interrupted starts settle retryably, explicit Stop still settles the full scope, and ambiguous ownership quarantines only that project. No GoForj CLI integration is restored and no new CLI release is required.

Native Windows continuity and all ten repeated adoption/Stop lifecycles pass at the product source in [CI 39508253651](https://github.com/goforj/harbor/actions/runs/39508253651); macOS native passes too. Its unrelated Linux root failure came from the EOF test closing its server at the client's exact deadline. Twenty local race-enabled repetitions pass after the fixture is held open until receive returns. The full local process race suite and ten unbuffered broker-flow repetitions pass. [Current state](./current-state.md) records the full behavior and boundaries.

## Exact package and acceptance references

- Unix producer [39508579934](https://github.com/goforj/harbor/actions/runs/39508579934) succeeds for the beta.4 macOS PKG and Ubuntu DEB; publication is skipped.
- Windows producer [39508583059](https://github.com/goforj/harbor/actions/runs/39508583059) succeeds for the disposable-signed installed foundation.
- Current full CI and installed macOS/Linux/Windows GUI outcomes are linked from [PR #18 checks](https://github.com/goforj/harbor/pull/18/checks). Confirm each selected package producer matches the IDs above. Failed or cancelled auto GUI runs before matching packages existed are not acceptance; rerun the current head's checks after producer availability.
- The installed driver requires two real daemon crashes, unchanged Ready-session generations and addresses, fresh live HTTP log markers, trusted HTTPS, safe interrupted-peer retry, peer isolation, final graphical Stop, normal cleanup/uninstall, and preserved user-file hashes.
- Earlier producers `39503700892`, `39503704067`, and `39506409177` were cancelled or superseded. Windows producer `39506412675` succeeded at older product source `0badda5`, but cannot establish acceptance for the corrected broker flow control.

The next action is to finish any outstanding exact-source acceptance checks on PR #18, address actual failures, and mark the PR ready when all required checks pass. Thereafter the next action is review/merge when the user requests it. Do not merge, tag, publish, or change the user's active installation without that authorization. No public release is included in this milestone.

## Important boundaries

- `harbord` remains the sole durable-state writer and reconciler. GoForj owns the inner development graph and every Compose mutation.
- Process ownership requires exact native birth and complete scope. A PID, path, port, command line, or environment file is insufficient. Output authentication alone grants no Stop authority.
- Preserve all project checkouts, volumes, credentials, versions, and stable network assignments. One failed project must not block the daemon or healthy peers.
- The mandatory runtime interface is unchanged; adoption, detachment, and scope settlement are optional capabilities. Existing unsupported sessions retain their previous bounded recovery behavior.
- Windows sessions need a fresh supported Start to establish the retained native job. Reboot and unattended service restart are separate capabilities. Public ingress can briefly drop while the daemon reconnects.
- Hosted proof is limited to its named OS/desktop profiles. Genuine consent, native Docker Desktop, supported Windows clients/UAC, GNOME Wayland, reboot, signed update, and production Windows signing remain separate gates.
- Local full-root validation has unmodified macOS temp-path/UTC assumptions and a host-conflict census failure. The normalized path/UTC cases pass; the focused host census still fails locally. Do not call that local full suite green.
- Use `GOCACHE=/tmp/gocache`, `GOMODCACHE=/tmp/harbor-goforj-modcache`, and `GOWORK=off` for local Go work. The unrelated `/tmp/gomodcache` is damaged. Keep these agent cache overrides out of user-facing commands.

## Prior merged baseline

[PR #17](https://github.com/goforj/harbor/pull/17) is merged at `cbbfd14b674e63a320d3fd0098e2849db044c1d6`, with unchanged product source `2f3ace995aafbced8cca3568e4a00a4a75686223`. Its independent correctness/security reviews found no blockers. The named installed GUI crash-and-retry profiles passed on macOS `39480629419`, Linux `39480706330`, and Windows `39479964857`. Those receipts prove safe settlement/retry, not uninterrupted adoption. Earlier PR #13 includes #16 at `e82e189b236bc56c51d357cf3a500e9f4d7773b7`; #14/#15 were superseded. GoForj v0.35.1 is released and installed locally. No new release or user-installation update has been performed by this feature task.
