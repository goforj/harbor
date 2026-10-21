# Beta support and recovery

Harbor is an unsigned development preview. This guide separates the public download from newer source changes and the exact environments exercised by native tests.

## Choose a build

The current public download is [v0.1.0-beta.1](https://github.com/goforj/harbor/releases/tag/v0.1.0-beta.1), built from `1e71ce47e5aaa07eeeb0f615a822dd49ca46770b`. Its [release workflow](https://github.com/goforj/harbor/actions/runs/39520200187) tested installation, three-project trusted HTTPS, desktop activation/reopen, Harbor-owned uninstall, and user-data preservation before publication.

| Environment exercised by the preview | Public download | Current limit |
| --- | --- | --- |
| macOS 26, Apple silicon | ARM64 PKG | Unsigned and unnotarized. Genuine consent and full trust-store restoration are separate proof gates. |
| Ubuntu 24.04, x86-64 | amd64 DEB | Unsigned preview. Native GUI evidence uses XFCE/X11 on Xvfb, not GNOME Wayland. |
| Windows Server 2025, x86-64, restricted product token | None | Disposable-signed CI installers are test artifacts. Public Windows downloads await production signing and supported client/UAC evidence. |

These are observed preview profiles, not a minimum-OS or stable-support promise. macOS Intel, Linux ARM64, additional distributions/desktops, Windows client editions, and other resolver/trust combinations need their own native evidence. A compiled executable or a lower deployment target does not establish support. The full release matrix remains governed by [cross-platform testing](./testing.md#release-support-matrix).

Native Docker Engine project isolation is exercised by the [Ubuntu generated-project gate](https://github.com/goforj/harbor/actions/runs/39423903409) against released GoForj v0.35.1. Native Docker Desktop workflows on macOS and Windows remain unproved. A Linux Docker result does not establish Desktop integration on those platforms.

### Newer source changes

The first-project guide, provider-aware tool checks, failure excerpts, log search/navigation, and support-report preview are newer than beta.1. Reading their documentation does not update an installed preview.

The guide/tool-check checkpoint at product source `2332dd887eb14719f316e0273757a267853ef080` passed installed GUI workflows on [macOS](https://github.com/goforj/harbor/actions/runs/39543757063), [Ubuntu XFCE/X11](https://github.com/goforj/harbor/actions/runs/39551962656), and [Windows Server](https://github.com/goforj/harbor/actions/runs/39542277969). Those receipts cover creation, tool recheck, App opening, close/reopen/Quit/relaunch, two daemon crashes, final Stop, owned cleanup/uninstall, and preserved files. The separate support-preview increment has focused frontend/build/browser evidence. Exact sources, package producers, artifacts, and any later acceptance are recorded in [current state](./current-state.md).

## Install and open Harbor

Download the package and its checksum sidecar from the same release. Keep their original filenames. On macOS:

```sh
shasum -a 256 -c GoForj-Harbor-macos-arm64-0.1.0-beta.1.pkg.sha256
```

Expect `GoForj-Harbor-macos-arm64-0.1.0-beta.1.pkg: OK`. Open the PKG with the native installer and review its publisher/signing status before continuing.

On Ubuntu:

```sh
sha256sum -c goforj-harbor_0.1.0.beta.1-1_amd64.deb.sha256
```

Expect `goforj-harbor_0.1.0.beta.1-1_amd64.deb: OK`. Install the verified local package:

```sh
sudo apt install ./goforj-harbor_0.1.0.beta.1-1_amd64.deb
```

Expect the package manager to finish successfully. Open Harbor from the application launcher. On macOS, open Harbor from Applications. The desktop and daemon run as your ordinary user; machine-level networking uses a separate, explicit helper approval.

Checksums detect changed downloads. They do not replace publisher signing. For source development instead of a package installation, use [source development](./development.md).

## Get a project running

Use **New project** for a generated GoForj project or **Add project** for an existing checkout. GoForj owns its Apps, services, versions, and Compose graph. Harbor registers and starts that project without taking ownership of unrelated workloads.

On newer source builds, the first-project guide brings these steps together. Existing workspaces can reopen it with **Getting started** on Overview:

1. Create or select the project.
2. Complete secure local networking setup, including its explicit approval or resume step.
3. Start the project. The new checks require canonical GoForj and Go for creation; selected Node/npm and Docker/Compose/Engine requirements are runtime warnings during creation. Install or start the missing tools and use **Recheck**. **Try Start** deliberately delegates to the daemon when its environment differs from the desktop's.
4. Use **Open app** for the App URL actually published by the ready project. CLI-only projects can continue to project details without inventing an App URL.

The current source modules require Go 1.26.1 for development. Native creation acceptance selects GoForj v0.35.1 and Node 22; these are tested toolchain pins, not a claim that every other version is incompatible. Custom project hooks and nonstandard frontend locations can require tools outside the initial convention-based checks.

## Diagnose a problem

First inspect the selected project's state and **Development output**. A Start failure can occur before a runtime session exists, so an empty output panel does not establish that the App ran. On newer source, failure details show the failed/interrupted startup step and a bounded redacted excerpt. Log search works within retained output; it does not search files on disk.

| Symptom | Next action |
| --- | --- |
| A tool is missing or Docker is unavailable | Install/start the indicated tool in the ordinary user environment, then recheck. An installed CLI with an unreachable Engine is a different failure from a missing CLI. |
| A port is allocated already | Inspect the reported listener. A wildcard bind such as `0.0.0.0:1025` can block a project's loopback address too. Stop or reconfigure the competing workload you own, then retry. |
| Networking setup is pending | Use the offered resume/approval flow. Resume a retained setup rather than deleting its state or beginning a competing operation. |
| A legacy network pool needs expansion | Use the expansion action in Settings -> Daemon on newer source. It retains project addresses; do not manually replace loopback identities. |
| The App is ready but its domain or certificate fails | Open the published App resource and record the exact browser error. Check the networking recovery card and daemon connection. A ready control connection alone does not prove system DNS or browser trust. |
| Startup never becomes ready | Inspect the last startup step and App output. Resolve its database, build, hook, or listener error before retrying. Harbor does not fix arbitrary project hooks. |
| Harbor cannot prove ownership of a stale runtime | Follow its bounded inspection/recovery guidance. Ambiguous evidence must remain visible; a PID, command name, port, or path alone does not authorize termination. |

The installed CLI has read-only control-plane diagnostics:

```sh
harbor daemon status --json
```

Expect a JSON response with `"state": "ready"` when connected. Connection or protocol errors mean that observation is unavailable, not that networking was verified.

```sh
harbor daemon snapshot
```

Expect the authoritative snapshot, including registered `projects`. Use the selected project's `id` from this response for project commands; the directory name and URL slug are not substitutes for its ID.

```sh
harbor doctor
```

Expect a report beginning `Harbor doctor (control-plane)` when the daemon is available. The implemented doctor checks authenticated control-plane facts. It is not a full DNS, trust-store, Docker, or host-health verdict. Review raw CLI output before sharing; snapshots and doctor JSON can contain checkout paths.

## Resume after a daemon restart

Closing or quitting the desktop does not stop projects. Reopening Harbor restores the client connection. Supported Ready/Degraded sessions can also survive a daemon restart when their exact native ownership and surviving broker are proved; recovery preserves their environment and does not rerun preparation or Compose.

Wait for a fresh snapshot before issuing lifecycle actions. An interrupted Start may settle as retryable and need a new explicit Start. A quarantined or ambiguous project needs its displayed recovery flow; it must not prevent healthy peers from operating. If the background service is unavailable, reopen the installed desktop and inspect Settings -> Daemon instead of starting a second daemon as another user.

This is daemon-restart behavior. Reboot continuity, unattended service restart, signed updates, and rollback remain separate gates.

## Collect a support report

In newer source builds, open **Support diagnostics** from project details or Settings -> Daemon. The preview contains selected daemon/build, snapshot, networking, and project facts. Recent development output is off by default. When selected, it is limited to the authenticated matching session and at most 40 lines / 6,000 UTF-8 bytes. The entire report is limited to 16 KiB.

Review the frozen preview, then use **Copy report**. A connection, project/session, output reset, failed read, or access change clears the preview and requires refresh. Common credentials and known private paths are redacted before clipping. Arbitrary private values or paths can remain in logs, so inspect the text before sharing. Environment files are not read and nothing is uploaded automatically.

On beta.1, collect only the relevant visible failure and bounded output manually. Raw logs and private `_data/harbor/forj-dev.log` launch traces are not equivalent to the new redacted support preview. Do not attach dotenv files, CA private keys, helper tickets, or ownership manifests. Include the Harbor version, OS/architecture, project state, and the action that failed.

## Remove a project or uninstall

Stop the selected project and let Harbor finish its owned shutdown. **Remove project** unregisters the project and releases its Harbor-owned networking; its source files and persistent project data remain. If removal is waiting for approval or reports incomplete cleanup, retain the installation and resolve that state before continuing.

Before removing the Ubuntu package, remove all project registrations and release Harbor's machine-global networking from the owning user's ordinary shell:

```sh
harbor daemon release
```

Expect the owned networking release to complete, including its explicit helper approval. If it is pending or fails, keep the package installed and resolve that result. Then stop the now-unused daemon:

```sh
harbor daemon stop
```

Expect an acknowledged shutdown. The package's removal guard deliberately refuses to remove active Harbor host integration. Once release and shutdown are complete, use the native package manager:

```sh
sudo apt remove goforj-harbor
```

Expect successful package removal with project files and retained user data preserved. An incomplete ownership-aware cleanup is a reported failure, not permission to delete network or trust files manually.

On macOS, the installed package includes its uninstaller:

```sh
sudo "/Library/Application Support/GoForj/Harbor/current/bin/harbor-installer" uninstall --user-id "$(id -u)" --group-id "$(id -g)"
```

Expect successful Harbor-owned uninstall with project files and retained user data preserved. Run this from the owning user's ordinary shell; the user/group substitutions identify that user before elevation. Keep the error and installation intact if ownership or trust cleanup cannot be proved. Deleting only Harbor.app is not the complete package uninstall.

The native tests check preservation of unrelated host state. They do not establish every consent/trust profile: the macOS hosted fixture preserves foreign administrator trust after Harbor uninstall, but disposal of that separate fixture timed out. Production signing, genuine consent, and broader platform coverage remain tracked independently in [current state](./current-state.md).
