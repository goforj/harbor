# Installation and release bundle

Status: proposed target design; implementation tracked in [Current implementation state](./current-state.md)

Last updated: 2026-07-24

This document defines how Harbor's desktop, daemon, command-line client, helper, relay, service definitions, and installer ship as one compatible product. It refines the release boundary in [Architecture](./architecture.md), the user-facing update behavior in [Product design](./product-design.md), and the native evidence required by [Cross-platform testing](./testing.md).

The first implementation target is macOS. Windows and Ubuntu layouts are normative contracts for later platform work, not claims that those packages exist today.

## Decision

Harbor is distributed as one signed product release containing multiple executables. The Wails application is the visible desktop client; it is not the product's process boundary and is not responsible for keeping projects alive.

One release binds:

- the Harbor desktop;
- `harbord`;
- the `harbor` CLI;
- `outputbroker`;
- `harbor-helper`;
- `harbor-installer`;
- platform-specific launchers, relays, policies, and service definitions;
- the IPC protocol range and durable-state schema range;
- the release manifest, component digests, channel, and release sequence.

The operating system may install these files in different locations and run them under different identities. They still form one indivisible compatibility unit. Harbor must never independently update only the Wails executable.

## Goals

- Install a complete Harbor release through the platform's normal signed installation mechanism.
- Keep the desktop, daemon, and project processes unprivileged.
- Run `harbord` as the interactive user and independently of the desktop window.
- Install only the narrow privileged components required by the selected platform.
- Prove every executable and service definition belongs to the same release.
- Support first install, repair, update, rollback, and uninstall as explicit transactions.
- Preserve project source, Compose volumes, and foreign host state.
- Build release artifacts once, sign them in isolation, and promote the same digests.
- Require native install and lifecycle evidence before claiming platform support.

## Non-goals

- Shipping Harbor as one monolithic executable.
- Running `harbord` as root, `SYSTEM`, or a Session 0 Windows service.
- Giving the desktop process ambient administrator authority.
- Using a generic shell script, package-manager command, or passwordless grant as the helper API.
- Letting a release manifest choose arbitrary installation destinations.
- Updating GoForj, project dependencies, generated project code, or service images as part of a Harbor update.
- Removing project checkouts or container volumes during Harbor uninstall.
- Treating a DMG, ZIP, AppImage, or unsigned archive as a full Harbor installation when it cannot install and prove the required host integration.

## Product roles

| Component | Runtime identity | Responsibility | Must not own |
|---|---|---|---|
| Harbor desktop | Interactive user | Window, tray, native consent presentation, notifications, and daemon client behavior | Durable state, project supervision, Docker access, or host mutations |
| `harbord` | Interactive user | Sole durable-state writer, reconciler, project supervisor, DNS, ingress, and authenticated control endpoint | Elevation or installer mutation |
| `harbor` | Interactive user | Scriptable daemon client and interactive setup/update entrypoint | Direct database, Docker, or host mutation |
| `outputbroker` | Interactive user | Exact-session output retention and replay | Project lifecycle authority or durable Harbor state |
| `harbor-helper` | One-shot elevated or admitted identity | Apply one ticket-bound, allowlisted host mutation and return typed evidence | Network access, arbitrary commands, project execution, or long-lived state |
| Platform relay | Platform-specific least privilege | Own or receive only the fixed low-port sockets required by the platform | Harbor durable state, project execution, or broad root authority |
| `harbor-installer` | One-shot installer identity | Verify and transact one complete product bundle at fixed destinations | Update download, project execution, Docker, or arbitrary paths |
| Platform launcher | Interactive user or OS launcher | Start the selected installed `harbord` version and report deterministic failure | Product policy or state migration |

Database migrations are embedded in `harbord`. The Vue frontend is embedded in the desktop binary. Private CA keys, leaf certificates, project data, logs, and machine ownership records are generated at runtime and are never release payloads.

## Tray packaging boundary

Harbor remains on stable Wails v2 for the first release. [`fyne.io/systray`](https://github.com/fyne-io/systray) is the preferred tray candidate because its external-loop entrypoint is designed to coexist with another GUI toolkit. The exact tray version is not frozen until interactive macOS, Windows, and Linux smoke proves that it can share the desktop process without deadlocks, duplicate native application delegates, broken close-to-hide behavior, or unreliable shutdown.

The first tray contains only aggregate status, `Open Harbor`, and `Quit Harbor UI`. It does not supervise projects, mutate durable state, or become necessary for daemon recovery. Harbor does not adopt alpha Wails v3 solely for tray support and does not maintain a custom Wails, native application-delegate, or tray-library fork to force same-process compatibility.

If the native proof fails, the release gains a stateless `harbor-tray` executable. It is signed, versioned, installed, updated, and removed as part of the same indivisible release, connects through authenticated daemon IPC, and owns no durable state. The release manifest and platform launcher definitions must explicitly include it in that configuration; an independently installed or updated tray is not supported.

## Bundle structure

The release build produces a platform- and architecture-specific component bundle before creating the outer native package:

```text
harbor-release/
  manifest.json
  manifest.signature
  bin/
    harbor-desktop
    harbord
    harbor
    outputbroker
    harbor-helper
    harbor-installer
  platform/
    daemon-launcher
    daemon-service-definition
    helper-admission-definition
    low-port-relay
    low-port-service-definition
  licenses/
  notices/
```

Absent platform components are explicit in the manifest; they are not represented by empty files. For example, the macOS release contains `launchdrelay`, while the initial Windows profile is expected to bind low ports as the medium-integrity user and therefore contains no equivalent long-lived elevated relay.

The native package may rearrange these files into platform-standard locations. The manifest identifies logical component roles, not caller-selected destination paths.

## Release identity and manifest

The manifest is canonical, versioned, size-bounded, and signed. It contains at least:

| Field | Meaning |
|---|---|
| `schema_version` | Manifest parser contract |
| `product_id` | Fixed Harbor product identity |
| `version` | User-facing semantic version |
| `release_sequence` | Monotonically increasing anti-replay value |
| `channel` | Stable, beta, or explicitly named development channel |
| `source_revision` | Exact source commit |
| `target` | Operating system, architecture, package format, and minimum supported OS |
| `components` | Logical role, version, size, digest, signature identity, and fixed destination class |
| `control_protocol` | Minimum and maximum compatible daemon protocol versions |
| `snapshot_schema` | Readable and writable durable-state schema ranges |
| `helper_protocol` | Helper ticket and evidence schema ranges |
| `installation_schema` | Installed bundle and ownership-record schema |
| `signing_policy` | Accepted key identities, threshold, and rotation metadata |
| `bundle_digest` | Digest committing the canonical manifest and every component digest |

The bundle signature and native platform signatures serve different purposes:

- the bundle signature prevents valid components from different releases being mixed together;
- Apple, Authenticode, or Linux package admission proves the executable or package identity expected by the operating system;
- the installer requires both where the platform provides both.

The installer contains a compiled table of fixed destination classes. A manifest cannot add a new privileged path, service label, scheduled task, registry key, or package script.

## Installed-state boundary

Installation state is separate from Harbor's per-user application database.

Machine installation state records:

- installation schema;
- product ID;
- release channel;
- committed release sequence and bundle digest;
- installed component paths and digests;
- installer and signing-key identities;
- active version pointer or package version;
- a bounded update transaction and rollback capsule when one is in progress.

Per-user Harbor state continues to record projects, operations, network leases, sessions, and the user profile's installation ID. The daemon does not write the protected machine installation record.

The native installer is the sole writer of protected installation state. `harbord` remains the sole writer of Harbor's application database.

## Runtime topology

The installed desktop and daemon have independent lifecycles:

```text
OS login
  → per-user launcher starts harbord
  → harbord migrates state, observes owned host state, and opens authenticated IPC

User opens Harbor
  → desktop focuses or starts its one UI instance
  → desktop connects to harbord and negotiates versions

User closes or quits the UI
  → desktop exits or hides according to product behavior
  → harbord and managed projects continue
```

The desktop may request that the platform launcher start a missing daemon. It must not retain the daemon as an ordinary child whose lifetime ends with the UI. The daemon's per-user lock and authenticated endpoint remain the authority for single-instance convergence.

An incompatible desktop and daemon fail at protocol negotiation with an update or repair action. They do not guess compatibility or mutate state.

## Installation transaction

Initial installation follows this order:

1. The operating system verifies the outer package's platform signature.
2. The installer acquires the fixed machine installation lock.
3. The installer verifies its own admission, the bundle signature, every component signature and digest, the target platform, and the release sequence.
4. It rejects a foreign product identity, unsafe existing path, symlink or reparse-point substitution, unexpected owner or ACL, channel mismatch, downgrade, and unrecognized installed state.
5. It stages the complete release without changing the active version.
6. It installs fixed launchers, service definitions, and privileged components using compare-and-swap against their current identities.
7. It atomically selects the staged release or commits the native package transaction.
8. It records the committed release sequence and bundle digest.
9. It records the machine package as ready for per-user activation; it does not guess a GUI user from an elevated context.
10. When an admitted installer is explicitly running in the interactive user's context, it may hand off to that user's activation flow.
11. It performs non-destructive component and signature checks; IPC and daemon health checks follow user activation.

Installing binaries does not silently claim `.test`, trust-store, loopback, or low-port ownership. First-run setup remains an explicit Harbor operation using the one-shot helper and the machine ownership contract. Installation may place dormant helper and service definitions required to perform that later approved setup.

If installation fails before commit, no candidate component becomes active. If native package semantics make a partial write possible, the installer uses its transaction journal to restore the exact pre-install files whose ownership it proved.

## First launch and user activation

The first interactive launch:

1. validates the installed bundle and selected version;
2. installs or enables the current user's daemon launch definition without elevation when the platform allows it;
3. starts `harbord` as that user;
4. negotiates the desktop/daemon protocol and component versions;
5. creates the user's application state only after the daemon is ready;
6. presents secure local networking setup separately;
7. records helper evidence only after the daemon independently verifies the host postcondition.

Machine installation and user activation are deliberately separate. A machine package must not create one arbitrary user's database, trust identity, or project state from an elevated installer context.

## Repair

Repair compares the signed installed manifest to native observations.

Repair may:

- restore a missing or corrupted product component when the current installation identity is exact;
- restore a missing per-user launcher definition;
- replace a Harbor-owned service definition whose current digest matches an allowed prior Harbor version;
- launch ordinary ownership-aware Harbor setup or repair operations for host integration.

Repair must preserve:

- a component at a fixed path whose owner or digest is foreign;
- a machine ownership record for another Harbor installation;
- a trust anchor, resolver rule, listener, scheduled task, service, or launch definition that cannot be proven Harbor-owned;
- user data until its schema and ownership are verified.

An ambiguous repair reports the exact retained artifact and requires a documented manual recovery path. It does not recursively delete the installation root.

## Update transaction

Harbor updates the complete product, not individual executables:

1. The unprivileged daemon downloads a candidate into an owner-only staging directory.
2. The daemon verifies format, channel, sequence, bundle signature, digests, target, and compatibility without granting installation authority.
3. An interactive desktop or CLI action starts the currently trusted `harbor-installer`. The installer has no network client.
4. The installer re-verifies the candidate, current installation, fixed staging root, and installation lock.
5. It creates a protected single-use rollback capsule binding the current and candidate bundle digests, sequence values, operation ID, expiry, and a verified pre-migration state snapshot.
6. The daemon stops accepting mutations, drains clients, checkpoints output, and settles its own process while leaving supervised project processes alone when exact recovery support is available.
7. The installer stages the candidate in a new versioned location and switches the fixed launcher or package transaction.
8. The candidate daemon migrates state and proves IPC, DNS, TLS, helper compatibility, host ownership, and component versions.
9. Success advances the protected sequence high-water mark and destroys the rollback capsule.
10. Failure before commit permits exactly one restoration of the previous bundle and schema snapshot.

If the running Harbor version cannot prove that managed projects will be re-adopted after daemon replacement, update pauses and asks the user to stop those projects. It must not silently orphan or terminate them.

The rollback capsule is not a general downgrade capability. After update commit, restoring an older release requires a separately signed rollback authorization.

## Uninstall transaction

Uninstall separates product removal from project and data ownership:

1. The uninstaller verifies the installed product identity and acquires the installation lock.
2. It asks the current daemon to plan removal of the exact Harbor-owned machine networking state.
3. Interactive approval invokes the one-shot helper for each allowlisted owned removal.
4. The daemon independently verifies resolver, trust, loopback, and low-port postconditions.
5. The daemon stops accepting work, settles its supervised authority, and exits.
6. The installer disables and removes per-user launch definitions that match Harbor's identity.
7. It removes service definitions, helpers, relays, launchers, application files, and installation records only when their observed identity matches the installed manifest or an explicitly allowed Harbor predecessor.
8. It removes Harbor runtime/cache material and offers an explicit choice for retained diagnostic or preference data.

Uninstall always preserves:

- registered project directories;
- project source and generated files;
- container images, named volumes, and databases owned by projects;
- GoForj and its configuration;
- foreign resolver, trust, loopback, firewall, service, task, or launcher state;
- a mismatched Harbor-labeled artifact whose ownership cannot be proved.

If machine cleanup cannot be completed, the uninstaller retains the minimum admitted helper and installation evidence required to retry cleanup and reports that Harbor host integration remains. It must not erase the proof and claim success.

Removing an application by deleting its UI bundle is not equivalent to Harbor uninstall. A later installer or doctor command must detect the retained installation and offer repair or ownership-aware removal.

## macOS package

### Format

The first complete Harbor distribution is a Developer ID-signed and notarized installer package. A notarized DMG may carry that package, but drag-copying an `.app` alone is a limited development distribution because it cannot transact the root-owned helper, relay, and launch definitions.

Every Mach-O executable and nested application component is signed before the outer package is signed and notarized. Notarization is stapled and verified without network access during the install test.

### Layout

The target layout is:

```text
/Applications/Harbor.app
  Contents/MacOS/Harbor
  Contents/Library/Harbor/release-manifest.json

/Library/Application Support/GoForj/Harbor/
  daemon-launcher
  cli-launcher
  releases/<sequence>/
    bin/harbor
    bin/harbord
    bin/outputbroker
    harbor-installer
    platform assets
    release-manifest.json
  current -> releases/<sequence>
  installation.json

/Library/PrivilegedHelperTools/
  com.goforj.harbor.helper
  com.goforj.harbor.launchdrelay

/Library/LaunchDaemons/
  com.goforj.harbor.launchdrelay.plist

~/Library/LaunchAgents/
  com.goforj.harbor.daemon.plist

/usr/local/bin/harbor -> /Library/Application Support/GoForj/Harbor/cli-launcher
```

The exact capitalization of the existing user data directory remains a compatibility constraint and is not changed by packaging:

```text
~/Library/Application Support/goforj/Harbor/
```

The per-user LaunchAgent invokes the stable admitted `daemon-launcher`, which verifies `installation.json`, resolves `current`, and starts that release's `harbord` as the user. The CLI shim follows the same selected release. Neither launcher interprets project configuration or performs update policy. The LaunchAgent is not a root daemon. The launchd low-port relay may receive system-owned sockets, but the relay process runs as the Harbor owner recorded by setup.

The helper and relay use their canonical fixed paths because their tickets, launchd definitions, and platform admission checks bind those paths. Replacing either requires the installer to verify the old and candidate signatures, manifest roles, and exact launchd state.

### macOS lifecycle requirements

- The desktop, daemon, CLI, and output broker run with the interactive user's identity.
- The `.app`, installer, helper, relay, and package identities share one designated signing requirement.
- Hardened-runtime entitlements are minimal and reviewed per executable; helper and installer entitlements are not inherited from Wails.
- First launch creates the user LaunchAgent from an admitted template and bootstraps it in that user's GUI domain.
- Quitting the desktop does not unload the daemon LaunchAgent.
- Package update replaces the application and versioned release assets as one transaction.
- Uninstall unloads only exact Harbor launchd labels and preserves a drifted or foreign plist/binary.

## Windows package

### Format

The Windows package is an Authenticode-signed installer with an explicit per-machine product identity. NSIS may remain the bootstrap format if it can satisfy the transactional, signing, locked-file, repair, and uninstall requirements; otherwise Harbor moves to MSI or MSIX with a separate admitted installer where required. The current generic Wails NSIS template is not the final product installer.

Every PE executable and the outer installer are Authenticode-signed. The installer verifies the expected publisher, bundle manifest, and component digests independently.

### Layout

```text
%ProgramFiles%\GoForj\Harbor\
  desktop-launcher.exe
  daemon-launcher.exe
  cli-launcher.exe
  releases\<sequence>\
    harbor-desktop.exe
    harbord.exe
    harbor.exe
    outputbroker.exe
    harbor-helper.exe
    harbor-installer.exe
    release-manifest.json
  current\
  installation.json

%LocalAppData%\GoForj\Harbor\
  data\
  cache\
  runtime\

Task Scheduler
  GoForj Harbor Daemon — per-user, at logon, medium integrity
```

The fixed desktop shortcut, daemon task, and optional CLI PATH entry use the admitted launchers rather than paths to mutable executables. Each launcher verifies the committed installation record and selects the current release. The per-user daemon task runs only in that user's interactive context. Harbor does not install `harbord` as `LocalSystem`.

The initial Windows full-mode profile uses the same local-administrator account's normal filtered token. `harbor-helper.exe` is invoked through UAC only for one approved mutation and exits. A different consenting administrator identity remains unsupported until split-identity trust and ownership behavior is proved.

### Windows lifecycle requirements

- The installer never overwrites its running executable or an in-use daemon binary.
- Update stages a new versioned directory, stops clients and the old daemon, then switches the launcher/junction transactionally.
- Job Object and named-pipe ownership settle before version switch.
- Scheduled task principal, trigger, action, ACL, and executable signature are compared exactly during repair and uninstall.
- WebView2 availability is checked explicitly; installing or downloading it is an installer policy decision, not an unbounded runtime action.
- Uninstall removes registry keys, shortcuts, tasks, and PATH changes only when they match Harbor's installation identity.

## Linux package

### Format

Ubuntu 24.04 uses a signed native `.deb` package first. Additional distributions require separate package and trust profiles. An AppImage may be offered only as a clearly limited UI/client build; it cannot claim full Harbor support when required helpers, policies, resolver integration, and user services are absent.

Native package-manager updates remain owned by the package manager. Harbor may check availability and launch the approved update flow, but it does not replace dpkg-owned files behind dpkg.

### Layout

```text
/usr/lib/goforj-harbor/
  harbor-desktop
  harbord
  outputbroker
  harbor-helper
  harbor-installer
  release-manifest.json

/usr/bin/harbor

/usr/lib/systemd/user/
  harbord.service

/usr/share/applications/
  harbor.desktop

/usr/share/icons/
  hicolor/.../harbor.png

~/.local/share/goforj/harbor/
~/.cache/goforj/harbor/
${XDG_RUNTIME_DIR}/goforj/harbor/
```

The exact privileged helper admission mechanism and policy location are selected per supported distribution. The package owns only fixed files. Host networking setup remains a separate ticket-bound helper transaction.

### Linux lifecycle requirements

- `harbord` runs as a systemd user service where systemd is part of the supported profile.
- Linger is never enabled silently.
- The package declares exact GTK, WebKit, and desktop-runtime dependencies.
- Package scripts do not run Harbor with project environment loading and do not mutate project state.
- Package upgrade preserves the daemon database until the candidate migration and health transaction commits.
- Package removal and purge remain distinct: ordinary removal preserves recoverable user state, while an explicit Harbor cleanup path removes exact owned host integration.

## Build and signing pipeline

The release workflow has separate trust domains:

1. **Source validation** runs portable tests, both Go modules, frontend tests/build, generators, dependency allowlists, and reproducibility checks.
2. **Native build** compiles every component for one exact platform/architecture and creates the unsigned canonical manifest.
3. **Artifact sealing** records component digests, SBOMs, licenses, source revision, toolchain versions, and provenance.
4. **Isolated signing** signs native components and the bundle manifest using short-lived workload identity. Product test workers never receive release keys.
5. **Package assembly** creates the native installer from those exact signed components without rebuilding.
6. **Native installation proof** installs the package on a clean interactive OS profile and emits attested product evidence.
7. **Promotion** attaches the already-tested package digest to beta or stable without rebuilding or re-signing different content.

Signing order is platform-specific:

- macOS signs nested binaries and helpers inside-out, signs the application and package, notarizes, staples, and verifies;
- Windows signs every PE component, assembles the installer, signs the installer, and verifies publisher and timestamp policy;
- Linux builds the package from sealed components, signs repository/package metadata as required by the supported profile, and verifies installed digests against the embedded Harbor manifest.

Release builds must not depend on mutable `latest` toolchains, package indexes, or Wails versions. The workflow records the exact Go, Node, Wails, native compiler, SDK, package tool, WebView, and signing-tool versions.

## Required release evidence

A platform artifact is publishable only after a clean native worker proves:

- package signature and bundle-manifest verification;
- fixed-path and permission/ACL installation;
- desktop and daemon run unelevated;
- daemon survives desktop exit and returns after login or reboot according to policy;
- first-run helper consent and cancellation;
- trusted DNS, HTTPS, loopback identities, and native service endpoints;
- three projects operating concurrently;
- component-version and IPC compatibility reporting;
- repair of a missing owned component;
- refusal to replace a foreign or drifted component;
- update from signed `N-1`;
- rejection of replay, downgrade, channel mismatch, and mixed components;
- rollback after an injected migration or health-check failure;
- uninstall with exact host cleanup;
- preservation of project source, volumes, and neighboring foreign state;
- no signing credentials or reusable package credentials on the product worker.

Cross-compilation, a successful `wails build`, or installation under an administrative CI account without the shipping consent boundary is not release evidence.

## Failure behavior

| Failure | Required behavior |
|---|---|
| Desktop/daemon protocol mismatch | Refuse mutation and offer update or repair |
| Missing daemon | Ask the admitted per-user launcher to start it; do not spawn an unmanaged child |
| Candidate signature or digest failure | Leave the installed version active and delete only the exact candidate staging data |
| Foreign fixed-path artifact | Preserve it, fail closed, and report exact manual recovery evidence |
| Helper admission mismatch | Issue no helper ticket and perform no host mutation |
| State migration failure | Use the one transaction-bound rollback capsule before commit |
| Candidate daemon health failure | Restore the previous selected version and schema snapshot |
| Running projects cannot be re-adopted | Pause update and require an explicit project stop |
| Uninstall host cleanup ambiguity | Preserve the minimum cleanup authority and report incomplete uninstall |
| Package deleted outside uninstaller | Detect retained installation/host state and offer repair or ownership-aware cleanup |

## Implementation sequence

1. Define the canonical release manifest and deterministic bundle assembler.
2. Add build metadata and dependency allowlists for every component role.
3. Implement `harbor-installer` as a bespoke minimal entrypoint with no network or project dependencies.
4. Build the signed/notarized macOS package and per-user LaunchAgent lifecycle.
5. Prove macOS clean install, first setup, three-project operation, update, rollback, and uninstall.
6. Implement the Windows versioned layout, task lifecycle, signing, and transactional installer.
7. Implement the Ubuntu package, systemd user lifecycle, package admission, and cleanup behavior.
8. Add isolated signing, native product workers, build-once promotion, and release support-table generation.

An unsigned three-platform artifact workflow may be added earlier for developer previews. Its artifacts must be labeled development builds and must not imply that installer, host integration, update, or uninstall support has passed.

## Open decisions

These choices must be closed with native prototypes before their platform package is implemented:

- the exact macOS user-agent installation API and whether release binaries are universal or architecture-specific;
- whether the Windows transactional requirements remain practical in NSIS or require MSI/MSIX plus a separate updater;
- the exact Windows current-version indirection that survives locked executables;
- the Ubuntu helper consent/admission mechanism and the initial repository-signing channel;
- the update metadata transport and channel publication service;
- the minimum retained evidence when uninstall cannot safely remove owned host integration;
- which user preference and diagnostic files ordinary uninstall preserves by default.

Closing one of these decisions must update this document and the corresponding native tests. It must not weaken the product invariants in order to fit a packaging tool.
