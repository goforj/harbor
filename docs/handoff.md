# Development Handoff

## Current continuation (2026-10-20)

The user authorized a preview release using hosted-runner acceptance. [Harbor v0.1.0-beta.1](https://github.com/goforj/harbor/releases/tag/v0.1.0-beta.1) is published at tag source `1e71ce47e5aaa07eeeb0f615a822dd49ca46770b`, after successful exact-source [main CI](https://github.com/goforj/harbor/actions/runs/39519520985) and [release run 39520200187](https://github.com/goforj/harbor/actions/runs/39520200187). The macOS Apple-silicon PKG and Ubuntu amd64 DEB are unsigned previews; Windows publication remains gated on production signing. No active developer installation was changed.

The public PKG digest is `455bbd6721cd4e16cf36cf0d727e5ecda4274eea95decdaf2f51981a7d51e3e0`; the DEB digest is `b4f5ffad30cf6fd5696fa386a4611f7cc782c0fd0fe8c70b5a73ac3ed3039344`. Both downloaded installers match the sealed tested bytes. GitHub changed the Debian asset's tilde to a period; only checksum/provenance filename references were corrected publicly. The release helper now makes that normalization before sealing, and its four focused tests pass. Local release receipts and downloads are under `/tmp/harbor-preview-v0.1.0-beta.1`. The immutable tag and installer payloads were not changed.

Transparent daemon restart continuity is merged in [PR #18](https://github.com/goforj/harbor/pull/18) at `dfaae107a9d1a73ba40bb30ae6653d6cf1221f12`. The merge tree exactly matches tested product source `9700f89e398b025a1f525c882f9875e043d07273`. Its earlier documentation follow-up records acceptance without changing product code. The release worktree is `/tmp/harbor-package-ui-20261019` on `docs/transparent-restart-acceptance`, based on the tagged main commit. Active developer checkouts, running user projects, and installations remain untouched.

Ready/Degraded sessions reconnect through separately authenticated output brokers and exact native process scope. Clean daemon shutdown detaches supported environments instead of stopping them. Windows broker query handles retain separately owned named jobs; root birth and membership remain mandatory for restored Stop authority. Unix retains its dedicated native session. Interrupted starts settle retryably, explicit Stop still settles the full scope, and ambiguous ownership quarantines only that project. No GoForj CLI integration is restored and no new CLI release is required.

All 50 executed pre-merge checks passed, with two conditional Docker checks skipped and none pending or failed. [Source CI](https://github.com/goforj/harbor/actions/runs/39513643739) and [security analysis](https://github.com/goforj/harbor/actions/runs/39513638668) passed. [Native Windows repetition](https://github.com/goforj/harbor/actions/runs/39513522849), artifact `12307686308`, passed 100 complete handoff/Stop scenarios, 100 fresh-stack job-buffer round trips, and 100 invalid-handle checks without a race report. That diagnostic branch differs only in tests, documentation, and its temporary workflow; production source parity was checked. Do not merge its temporary workflow.

## Exact package and acceptance references

| Installed profile | Exact producer | Full installed GUI, cleanup, and uninstall |
| --- | --- | --- |
| macOS 26, Apple silicon | [39513665783](https://github.com/goforj/harbor/actions/runs/39513665783), beta.5 PKG, publication skipped | [39513643541](https://github.com/goforj/harbor/actions/runs/39513643541), attempt 2, artifact `12307528910` |
| Ubuntu 24.04, amd64, XFCE/X11 | [39513665783](https://github.com/goforj/harbor/actions/runs/39513665783), beta.5 DEB, publication skipped | [39513643678](https://github.com/goforj/harbor/actions/runs/39513643678), attempt 2, artifact `12306877946` |
| Windows Server 2025, amd64, restricted product token | [39513668950](https://github.com/goforj/harbor/actions/runs/39513668950), disposable-signed dev.33 installer | [39513643604](https://github.com/goforj/harbor/actions/runs/39513643604), attempt 2, artifact `12307268077`; restricted-profile artifact `12306564448` |

All three downloaded receipts were checked for two real daemon crashes, unchanged Ready-session generations, fresh live HTTP logs, trusted HTTPS, safe interrupted-peer retry, peer isolation, graphical Stop, cleanup/uninstall, and three preserved user-file hashes. Only the gated incomplete peer requires a new Start. The PKG SHA-256 is `5e8e4b5f5852ac6b35f1b669583101e365fc6e03fa8b8a6be6c5c42175bd37bf`; the DEB SHA-256 is `dab57d258c9e8266a155e0c000f761d9a39560662ea0096c4dc47297053f2a28`. The Windows installed bundle digest is `e00c4212f61337216fe0d1fb36b9b56a7b0187c7f9d3fee2c87f1f62489bc347`.

Earlier receipts and producers are superseded. Failures exposed recovered-command checkout/executable omissions, a process-wrapper release race, unsafe native job-buffer lifetime, and full-duplex broker acknowledgement blocking. Those defects were corrected rather than disabled. Fresh exact-source acceptance passed before the user authorized merge.

Preview publication is complete. The next release goal is production signing before enabling Windows public downloads. Any local installation update requires its own user request. The release follow-up changes only publication filename handling, its focused test, and release documentation in `/tmp/harbor-package-ui-20261019`; it does not change product binaries. Automatic CI for that follow-up is separate from the successful exact-tag release evidence above.

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
