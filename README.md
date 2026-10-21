# GoForj Harbor

Status: unsigned development preview; not yet a stable release

GoForj Harbor is the local development control plane for GoForj projects. It is designed to give every project stable `.test` domains, trusted HTTPS, and native service ports without making projects compete for the same host bindings.

Harbor keeps project ownership intact: each project retains its own GoForj Apps, Compose services, versions, credentials, containers, and volumes. A per-user daemon owns Harbor state and reconciliation; the CLI and Wails desktop are clients; narrowly scoped privileged helpers apply only reviewed machine-level networking changes.

## Preview downloads

[Harbor v0.1.0-beta.1](https://github.com/goforj/harbor/releases/tag/v0.1.0-beta.1) includes an Apple-silicon macOS PKG and an Ubuntu 24.04 amd64 DEB, with checksums and package provenance. The macOS package is unsigned and unnotarized. Windows passed its hosted installed-app checks, but public installers remain gated on production code signing. See the release notes for the exact tested profiles and preview limits.

## Target

The first complete release must prove that three ordinary GoForj projects can run concurrently on macOS, Linux, and Windows with:

- stable HTTPS application and tool URLs;
- project-specific MySQL, PostgreSQL, and Redis endpoints on their native ports;
- per-project containers and persistent data;
- DNS, certificates, ingress, and restart recovery managed without rewriting application code or repository port configuration;
- the same core workflows available through the daemon protocol, CLI, and desktop;
- native operating-system CI evidence for every supported networking and privilege claim.

The approved design and its complete release gates are indexed in [Harbor design](./docs/README.md).

## Current state

Harbor provides guided GoForj project creation, dedicated project loopback addresses, trusted HTTPS, development output, terminals, and service inspection. Supported running environments survive daemon restarts and reconnect without rebuilding or restarting Compose. Native installed-app acceptance passes on the named hosted macOS, Ubuntu, and Windows profiles; the preview does not establish every desktop or consent profile.

[Current implementation state](./docs/current-state.md) is the source of truth for what works, temporary compatibility bridges, known gaps, and validation. [Development handoff](./docs/handoff.md) records the exact continuation point and local worktree boundary.

## Develop

From the repository root, run the complete development graph:

```sh
forj dev
```

This builds and watches `harbord`, applies its embedded migrations, runs the daemon in the foreground, and starts the Wails desktop. Do not run `forj dev`, `harbord`, or Wails as root or Administrator; Harbor requests explicit elevation only for a bounded helper operation.

See [source development](./docs/development.md) for current prerequisites, daemon-only and helper-bootstrap workflows, CLI commands, and generated-source maintenance. The root Go module, nested `desktop` Go module, and `desktop/frontend` test/build surface are independent validation boundaries.

## Documentation

- [Current implementation state](./docs/current-state.md): implemented behavior and known gaps.
- [Development handoff](./docs/handoff.md): current worktree boundary, reproduction context, and next goal.
- [Product design](./docs/product-design.md): user model, workflows, UI, and first-release scope.
- [Frontend](./docs/frontend.md): GoForj Vue/shadcn foundation and the bounded Lerd visual adaptation.
- [Illustrations](./docs/illustrations.md): Harbor's handcrafted visual language, maritime metaphors, assets, and placement rules.
- [Architecture](./docs/architecture.md): authority, state, lifecycle, IPC, privilege, and recovery.
- [Networking](./docs/networking.md): loopback identity, DNS, TLS, ingress, relays, and platform proof gates.
- [GoForj integration](./docs/goforj-integration.md): the intended versioned project and managed-session contract.
- [Repository environment overrides](./docs/environment-overrides.md): the optional runtime-neutral mapping from Harbor facts to project environment names.
- [Cross-platform testing](./docs/testing.md): required macOS, Linux, and Windows evidence.
- [Delivery plan](./docs/delivery-plan.md): dependency-ordered milestones and release definition.
- [Research](./docs/research.md): pinned Herd, Yerd, Lerd, Wails, platform, and GoForj findings.
