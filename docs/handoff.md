# Development Handoff

## Current continuation (2026-10-20)

The user requested transparent daemon restarts so running environments are retained. Implementation is in draft [PR #18](https://github.com/goforj/harbor/pull/18), branch `feat/transparent-daemon-recovery`, isolated worktree `/tmp/harbor-package-ui-20261019`, based on merged documentation baseline `ffbfecb`. Active developer checkouts, running user projects, and installations remain untouched. All changes in this worktree belong to this milestone; inspect live Git status before editing.

Ready/Degraded sessions reconnect through separately authenticated output brokers and exact native process scope. Clean daemon shutdown detaches supported environments instead of stopping them. Windows broker query handles retain separately owned named jobs; root birth and membership remain mandatory for restored Stop authority. Unix retains its dedicated native session. Interrupted starts settle retryably, explicit Stop still settles the full scope, and ambiguous ownership quarantines only that project. No GoForj CLI integration is restored and no new CLI release is required.

The first packaged candidate was `d2bacd0a59e200876962c64592843a718c8f955f`. Its installed macOS [39510197399](https://github.com/goforj/harbor/actions/runs/39510197399) and Windows [39510197333](https://github.com/goforj/harbor/actions/runs/39510197333) GUI runs passed both daemon crashes, unchanged Ready sessions, fresh live logs, trusted HTTPS, normal Stop, cleanup, uninstall, and user-file preservation. Linux [39510197342](https://github.com/goforj/harbor/actions/runs/39510197342) passed runtime continuity but failed uninstall because the recovered command lacked its checkout directory and its service watcher left an asynchronous shutdown error. These receipts are historical evidence, not acceptance for later product changes.

The current correction restores the observation executable and canonical checkout, derives read-only resource-query networking from the admitted runtime plan, and joins the adoption monitor before releasing its process wrapper. A new native test assertion reproduced the missing checkout before the fix. The expanded resource query exposed the process-wrapper release race under the local race detector; the complete process and GoForj-adapter race suites pass after the monitor owns release. Resource-environment validation and failure paths are covered directly.

Windows repetition [39512162035](https://github.com/goforj/harbor/actions/runs/39512162035) observed a false empty-job result with both exact root and broker still alive. Job-information buffers previously crossed a uintptr-typed wrapper while remaining on movable Go stacks. The new `internal/platform/windowsjob` boundary passes typed pointers directly to the syscall; compiler escape analysis confirms every affected native buffer now escapes to stable storage. Its native round-trip and invalid-handle checks, together with repeated handoff/Stop, remain required on Windows. The throwaway `diag/restart-session-windows` branch runs these focused repetitions through manual CI dispatch; it is not a merge candidate and its temporary workflow must not replace feature CI.

## Exact package and acceptance references

Fresh macOS, Linux, and Windows packages must be built from the corrected product source and admitted by the existing exact-product verifier. The beta.4 producer [39508579934](https://github.com/goforj/harbor/actions/runs/39508579934) and Windows producer [39508583059](https://github.com/goforj/harbor/actions/runs/39508583059) are superseded. Never weaken admission or exclude production paths to reuse them. Final full CI, native repetitions, and installed GUI results belong on [PR #18 checks](https://github.com/goforj/harbor/pull/18/checks) and in the PR body with exact producer IDs and artifacts.

The installed driver requires two real daemon crashes, unchanged Ready-session generations and addresses, fresh live HTTP log markers, trusted HTTPS, safe interrupted-peer retry, peer isolation, final graphical Stop, normal cleanup/uninstall, and preserved user-file hashes. Source-code cross-compiles are not native acceptance. Earlier failed or cancelled checks are not relabeled as passing.

The next action is to complete fresh exact-source acceptance, address actual failures, and mark PR #18 ready only when all required checks pass. Thereafter review/merge requires the user's instruction. Do not merge, tag, publish, or change the user's active installation without that authorization. No public release is included in this milestone.

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
