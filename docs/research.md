# Research

Research date: 2026-07-18

## Scope

This design was informed by:

- the public Laravel Herd product and documentation;
- a local, pinned audit of Yerd;
- a local, pinned audit of Lerd;
- current official Wails v3 and operating-system documentation;
- a clean worktree of current `goforj/goforj` main.

The repositories were studied for architecture, process ownership, IPC, privilege, DNS, TLS, proxying, service orchestration, persistence, UI, updates, tests, and cross-platform behavior. No Yerd or Lerd code or visual asset was copied into Harbor.

## Synthesis

The design conclusion is:

> Use Yerd as Harbor's control-plane reference, Lerd as its operational edge-case, test, and visual-layout reference, and Herd as its product-experience reference. Copy none wholesale. Keep GoForj as the authority for project semantics.

The references solve related PHP development problems. Harbor has a distinct requirement: several host-run Go Apps and project-owned Compose stacks must coexist while raw services retain their native ports. That makes stable per-project loopback identity a core Harbor capability rather than an optional web-domain convenience.

## Laravel Herd

Sources reviewed include the official [product site](https://herd.laravel.com/), [documentation index](https://herd.laravel.com/docs/llms.txt), [macOS installation](https://herd.laravel.com/docs/macos/getting-started/installation), [macOS sites](https://herd.laravel.com/docs/macos/getting-started/sites), [site management](https://herd.laravel.com/docs/macos/sites/managing-sites), [securing sites](https://herd.laravel.com/docs/macos/sites/securing-sites), [Herd.yml](https://herd.laravel.com/docs/macos/sites/herd-yaml), [CLI](https://herd.laravel.com/docs/macos/advanced-usage/herd-cli), [services](https://herd.laravel.com/docs/macos/herd-pro-services/services), [mail](https://herd.laravel.com/docs/macos/herd-pro-services/mail), [Windows sites](https://herd.laravel.com/docs/windows/getting-started/sites), [Windows services](https://herd.laravel.com/docs/windows/herd-pro-services/services), and [Windows installation](https://herd.laravel.com/docs/windows/getting-started/installation).

### What Herd gets right

- It presents one continuous workflow: install, discover/create, open a stable URL, start dependencies, inspect the project, and return through the tray or CLI.
- Parked folders and linked sites remove repeated setup, while favorites keep a large list usable.
- `.test` domains and per-site HTTPS make a project feel installed rather than temporarily bound to a port.
- GUI and CLI cover the same important site and service operations.
- Site detail combines open-in-browser, IDE/terminal shortcuts, environment information, database access, logs, TLS, and service state around one selected project.
- Service management treats databases, caches, search, storage, and mail as part of the development environment rather than unrelated containers.
- The onboarding describes installation and host changes rather than assuming they already exist.

### What Harbor adopts

- a first-run setup and verification path;
- stable `.test` identity and trusted HTTPS;
- registration, favorites, quick open actions, tray access, and CLI parity;
- project-centered Apps, services, tools, logs, and diagnostics;
- explicit update and uninstall behavior;
- a fast path from a project to its browser, editor, terminal, database client, and framework tools.

### What Harbor does not adopt

- Herd's shared PHP/nginx serving model: GoForj Apps are compiled processes with their own lifecycle;
- broad parked-folder execution: Harbor discovers `.goforj.yml` candidates but requires explicit registration;
- a second repository manifest such as `herd.yml`: `.goforj.yml` already owns project intent;
- a native database/cache binary catalog: GoForj already selects Compose infrastructure;
- framework-specific debugging duplicated in Harbor: API Index, Lighthouse, Mailpit, and observability remain GoForj resources;
- unlink/delete ambiguity: unregister never deletes project source;
- force-stopping processes by executable name;
- a product tier that withholds correctness-critical logs or diagnostics.

Herd currently targets macOS and Windows. Harbor's Linux requirement is first-class rather than a later port.

## Yerd

Repository: [`forjedio/yerd`](https://github.com/forjedio/yerd)

Pinned audit commit: [`082958a3f80a7bc087d53f2e931e38e25f32eeb9`](https://github.com/forjedio/yerd/tree/082958a3f80a7bc087d53f2e931e38e25f32eeb9)

License: [MIT](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/LICENSE.md)

Primary sources include [architecture](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/developer/architecture.md), [IPC protocol](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/developer/ipc-protocol.md), [cross-platform design](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/developer/cross-platform.md), [GUI boundary](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/developer/gui.md), [daemon](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/guide/daemon.md), [elevation](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/guide/elevation.md), [DNS](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/guide/dns.md), and [proxying](https://github.com/forjedio/yerd/blob/082958a3f80a7bc087d53f2e931e38e25f32eeb9/docs/guide/proxies.md).

### Strong patterns

- One unprivileged per-user daemon is authoritative; CLI and Tauri desktop are thin clients.
- A one-shot privileged helper accepts typed allowlisted operations rather than arbitrary shell commands.
- Domain logic and I/O are separated behind narrow boundaries.
- IPC is framed, versioned JSON over a local socket with golden wire tests.
- Configuration is strict and versioned, mutations build and validate a candidate before publication, and one writer prevents lost updates.
- Routing is typed, collision-checked, exact-first, and has no implicit catch-all.
- Process supervision is a state machine with readiness windows, backoff, restart budgets, and graceful-stop escalation.
- The updater stages artifacts and verifies checksums and signatures before apply.

### Gaps Harbor must close

- Negotiate protocol versions in the first frame rather than failing after an incompatible request.
- Verify peer identity and bound connection/request concurrency, deadlines, frame size, and idle lifetime.
- Tighten helper path validation against time-of-check/time-of-use replacement.
- Create CA and leaf private keys with restrictive permissions on first open and persist cert/key pairs transactionally.
- Put explicit time, size, and concurrency limits around TLS and proxy connections.
- Sign every executable/service manifest and artifact; a checksum inside an unsigned manifest is not an independent trust root.
- Treat source/documentation drift as a test failure.
- Add actual Windows support rather than unsupported stubs.

Yerd is the closest architectural reference for Harbor, but its one-address web model does not solve Harbor's raw same-port multi-project requirement.

## Lerd

Repository: [`lerd-env/lerd`](https://github.com/lerd-env/lerd)

Pinned audit commit: [`57641f87ed6969a578f1dc5328d873284cc270c8`](https://github.com/lerd-env/lerd/tree/57641f87ed6969a578f1dc5328d873284cc270c8)

License: [MIT](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/LICENSE)

Primary sources include [architecture](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/reference/architecture.md), [configuration](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/configuration.md), [DNS](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/features/dns.md), [HTTPS](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/features/https.md), [system tray](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/features/system-tray.md), [web UI](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/features/web-ui.md), and [logs](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/features/logs.md).

### Strong patterns

- Rootless containers keep ordinary service processes out of root authority.
- Its DNS implementation covers practical NetworkManager, systemd-resolved, macOS resolver, VPN, route-change, dual-stack, and stale-gateway cases.
- It has a broad real-world repair corpus for service, network, worktree, and host changes.
- Service definitions and platform lifecycle management expose many useful behavioral cases.
- TLS renewal and cert/key rollback show careful attention to partial persistence.
- The tray, web dashboard, CLI, and TUI make project and service state highly visible.
- Unlink behavior preserves source files.
- Its test investment covers a large operational surface.

### Visual direction Harbor adopts

Lerd's clearest product contribution is its information architecture:

- a compact left navigation rail;
- a contextual list of sites or services;
- a persistent detail pane for the selected entity;
- an address-style project header with open and quick actions;
- clear Overview, Logs, Services, and System contexts;
- live status in lists without requiring a detail page;
- project-scoped service cards and a cross-project system view;
- tray state that leads back to the richer dashboard.

Harbor adapts that three-pane discipline and density to Projects, Apps, Services, their resource actions, Network, and Diagnostics. It uses its own visual identity and code.

The early Harbor prototype should use the pinned [dashboard](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/assets/screenshots/dashboard.png), [sites list](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/assets/screenshots/sites-list.png), [site detail](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/assets/screenshots/site-detail-overview.png), [services list](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/assets/screenshots/services-list.png), and [system view](https://github.com/lerd-env/lerd/blob/57641f87ed6969a578f1dc5328d873284cc270c8/docs/assets/screenshots/system.png) only as annotated layout references. Harbor does not reuse their assets.

### Architecture Harbor does not adopt

- Several peer processes directly mutating YAML, generated files, Podman, systemd/launchd, and cached state.
- A loopback/LAN HTTP mutation API as the control-plane boundary.
- Passwordless grants for broad host commands.
- Binding the whole home directory into shared containers.
- A second project manifest and automatic `.env` rewriting.
- Automatic public-port shifts, which contradict Harbor's native-port promise.
- Mutable unsigned framework/service recipes that can declare commands and proxy configuration.
- An unsigned installer/updater path.
- A general framework/service store, environment editor, profiler, dumps system, worktree manager, MCP server, and every other feature in one first release.

Lerd is more valuable to Harbor as a catalog of platform failures, tests, and good visual layout than as a control-plane blueprint.

## Wails

Official sources include the Wails v3 [status](https://v3.wails.io/status/), [application lifecycle](https://v3.wails.io/concepts/lifecycle/), [window behavior](https://v3.wails.io/features/windows/basics/), [system tray](https://v3.wails.io/features/menus/systray/), [single instance](https://v3.wails.io/guides/single-instance/), [services/bindings](https://v3.wails.io/features/bindings/services/), [bridge](https://v3.wails.io/concepts/bridge/), and [releases](https://github.com/wailsapp/wails/releases).

As of the research date:

- Wails v3 is marked alpha; v2 is the stable line.
- The observed v3 release was `v3.0.0-alpha2.117`, dated 2026-07-08.
- The current v3 installation guide requires Go 1.25+, and its default Linux path uses GTK4 with WebKitGTK 6.0 on Ubuntu 24.04+; the GTK3/WebKit2GTK 4.1 path is explicitly legacy and scheduled for removal in v3.1.
- v3 provides the window, menu, tray, single-instance, and Go/frontend binding primitives Harbor wants.
- Tray support varies by Linux desktop and cannot be reliably detected as a universal capability.
- Wails services live inside the desktop process and are not OS services.
- Wails' updater does not coordinate a desktop, daemon, helper, service definitions, schema migration, and rollback as one product update.
- The fast-moving alpha documentation and toolchain requirements reinforce the need to pin and derive the actual module/toolchain constraint.

Decision: use a pinned Wails v3 only as the unprivileged desktop client. First prove it as a GoForj named App; move it to a nested module if its Go/CGO/frontend toolchain would otherwise raise or contaminate headless binary requirements. Keep Harbor preview/beta until the desktop and packaging matrix is credible. The headless control plane must survive a Wails replacement.

## Platform sources

The platform design was checked against the following primary sources and one clearly labeled mirror:

- `.test` special use: [RFC 6761](https://www.rfc-editor.org/info/rfc6761/) and [IANA registry](https://www.iana.org/assignments/special-use-domain-names/special-use-domain-names.xhtml);
- macOS resolver files: third-party mirror of Apple-shipped [`resolver(5)`](https://keith.github.io/xcode-man-pages/resolver.5.html); every supported macOS image must also verify the locally installed `man 5 resolver` behavior;
- macOS service lifecycle: [`SMAppService`](https://developer.apple.com/documentation/servicemanagement/smappservice) and [launchd socket activation](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html);
- macOS certificate trust: [Keychain certificate installation](https://support.apple.com/guide/keychain-access/add-certificates-to-a-keychain-kyca2431/mac) and [trust settings](https://support.apple.com/guide/keychain-access/change-the-trust-settings-of-a-certificate-kyca11871/mac);
- Linux resolver control: [`resolvectl`](https://www.freedesktop.org/software/systemd/man/devel/resolvectl.html) and [NetworkManager route-only domains](https://www.networkmanager.dev/docs/api/latest/nm-settings-nmcli.html);
- Linux user services: [`loginctl`](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html) and [`pam_systemd`](https://www.freedesktop.org/software/systemd/man/latest/pam_systemd.html);
- Linux CA stores: [Ubuntu](https://ubuntu.com/server/docs/how-to/security/install-a-root-ca-certificate-in-the-trust-store/) and [Red Hat](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/7/html/security_guide/sec-shared-system-certificates);
- Windows DNS policy: [`Add-DnsClientNrptRule`](https://learn.microsoft.com/en-us/powershell/module/dnsclient/add-dnsclientnrptrule);
- Windows services and UI separation: [services](https://learn.microsoft.com/en-us/windows/win32/services/services), [Session 0 isolation](https://learn.microsoft.com/en-us/windows/win32/services/service-changes-for-windows-vista), and [interactive-service guidance](https://learn.microsoft.com/en-us/windows/win32/services/interactive-services);
- Windows certificate stores: [certificate stores](https://learn.microsoft.com/en-us/windows/win32/secauthn/certificate-stores);
- Windows HTTP URL reservations if HTTP.sys is selected: [`netsh http`](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/netsh-http);
- Docker publications: [port publishing](https://docs.docker.com/engine/network/port-publishing/), [Compose networking](https://docs.docker.com/compose/how-tos/networking/), and [Docker Desktop host access](https://docs.docker.com/desktop/features/networking/networking-how-tos/);
- GitHub Actions execution: [hosted-runner privileges](https://docs.github.com/en/actions/reference/runners/github-hosted-runners), [secure use of self-hosted runners](https://docs.github.com/en/actions/reference/security/secure-use), [ephemeral self-hosted behavior](https://docs.github.com/en/actions/reference/runners/self-hosted-runners), and the [hosted image matrix](https://github.com/actions/runner-images);
- Docker Desktop product workers: [Windows client requirements](https://docs.docker.com/desktop/setup/install/windows-install/) and [GitHub-hosted macOS virtualization limits](https://docs.github.com/en/enterprise-cloud@latest/actions/reference/runners/larger-runners).

Several operating-system mechanisms remain candidates until the Phase 0 CI proof. The documents intentionally distinguish a sourced platform capability from Harbor's unproven use of that capability.

## Current GoForj

Repository: [`goforj/goforj`](https://github.com/goforj/goforj)

Pinned audit commit: [`6422f32eb3013c44ce3b18d236a90158dc8e7f16`](https://github.com/goforj/goforj/tree/6422f32eb3013c44ce3b18d236a90158dc8e7f16)

Primary design/context sources include [App structure](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/context/app-structure.md), [runtime architecture](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/context/runtime-architecture.md), [repo ownership](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/context/repo-boundaries-and-ownership.md), [native watcher design](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/designs/completed/forj-dev-native-watcher-design.md), [TUI design](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/designs/completed/forj-dev-tui-design.md), [resource registry design](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/designs/resource-registry-design.md), and [systray sketch](https://github.com/goforj/goforj/blob/6422f32eb3013c44ce3b18d236a90158dc8e7f16/docs/designs/forj-systray-design.md).

### Findings that shape Harbor

- GoForj already owns the generator, default/named App model, combined runtime, environment policy, lifecycle tasks, native watcher graph, TUI, process groups, and Compose intent.
- Current rendering always includes the default `cmd/app`, `app`, and `app/wire` boundary; Harbor therefore uses it for the user-facing CLI rather than inventing a named-App-only project.
- Generated App mains load project environment, run shared preboot behavior, initialize the full generated application, and expose generic command dispatch. That is useful for the CLI/daemon but disqualifies standard App scaffolding for privileged helper/installer entrypoints.
- `forj dev` has internal process and watcher identities but no stable external session protocol.
- The current flat `dev.pre` list contains generated Compose and database-readiness work in order. Managed Harbor startup needs explicit pre-Compose, typed Compose, post-Compose, and post-migrate phases instead of moving or guessing arbitrary shell tasks.
- A project lock already makes one dev session authoritative inside a checkout.
- `down_on_exit` means Harbor needs an in-session restart action rather than outer relaunch.
- The internal resource registry exists and should be consolidated, not reimplemented in Harbor.
- The API Index and generated examples are important framework UX; Harbor should surface them intact.
- Generated Apps expose `resources:describe --json`, but it covers a different durable-resource purpose and is not a complete project descriptor.
- Compose templates already provide `IP_ADDRESS` and per-service host-port controls, making private publication feasible without rewriting Compose.
- `.env.host` uses localhost service hosts, and the env package makes files win over ambient values, so managed endpoint values need a true final overlay in both the dev tool and generated App.
- HTTP App bind host and public URL are already separate, but metrics needs a bind-host contract.
- Combined HTTP Apps expose health, metrics, and Lighthouse as routes on one listener; standalone worker/scheduler commands may open separate metrics listeners, while SPAs are build nodes. Harbor plans the active command shape rather than assigning a port to every feature label.
- Arbitrary custom App commands and watchers currently have no typed endpoint metadata, so they must block full mode until GoForj can declare and enforce their listener contract.
- Current Windows blockers in `forj dev` must be fixed before Harbor can claim Windows-managed project parity.
- The old systray sketch's lasting principles are useful—keep desktop dependencies out of `forj`, keep the tray thin, consume the resource registry—but its proposed in-memory tray session manager is superseded by Harbor's persistent daemon.

Harbor should not depend on the proposed extension system. Future extensions can enter Harbor through the same static descriptor and live resource/session projection after GoForj implements them.

## License and attribution boundary

Yerd and Lerd are MIT-licensed, but this phase copied no source code. If a later implementation ports substantial code rather than independently applying an architectural pattern, maintainers must review the pinned repository license and preserve the required copyright and permission notice.

Product layouts, behaviors, and platform cases are design inputs. Harbor uses its own code, brand, frontend assets, protocol, and GoForj-specific model.
