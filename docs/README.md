# GoForj Harbor Design

Status: proposed

Last updated: 2026-07-18

GoForj Harbor is a local development control plane for GoForj projects. It gives each project stable local domains, trusted HTTPS, and native service endpoints without forcing developers to edit project ports. A persistent daemon owns runtime state; the CLI, desktop window, and tray are clients of that daemon; GoForj remains the authority for project composition and the development lifecycle.

The first release is successful when three ordinary generated GoForj projects can run concurrently and each can expose, at the same time:

- its application at a stable `https://<project>.test` URL;
- MySQL at `mysql.<project>.test:3306`;
- PostgreSQL at `postgres.<project>.test:5432` when selected;
- Redis at `redis.<project>.test:6379`;
- project tools such as Mailpit, Lighthouse, Grafana, and the API Reference backed by GoForj's API Index and generated examples through stable HTTPS links;
- the same behavior on macOS, Linux, and Windows, proven by required OS-specific GitHub Actions jobs.

No repository port files need to change, and stopping or unregistering a project must not delete its source or container volumes.

## Decisions

| Area | Decision |
|---|---|
| Authority | `harbord` is the sole Harbor state writer and reconciler. |
| Repository | Harbor's CLI and daemon remain GoForj Apps. The desktop is a nested Wails v2 module in the same development graph; privileged helper/installer entrypoints stay bespoke. |
| Desktop | Stable Wails v2 hosts a thin, replaceable client. Tray integration is a separate Go capability proved against the native event loops; closing the UI does not stop projects. |
| Frontend | Harbor starts from GoForj's source-owned Vue/shadcn starter, keeps its primitive layer intact, and builds Harbor-specific views from those components. Lerd is the initial visual anchor for density, layout, and interaction styling. |
| GoForj | GoForj describes and runs projects through versioned contracts; Harbor does not parse terminal output or reproduce `.goforj.yml` semantics. |
| Project intent | `.goforj.yml` remains authoritative. Harbor does not introduce a repository-owned manifest. |
| HTTP | One local HTTP/TLS ingress routes exact domains by Host and SNI. |
| Native ports | Each project receives a stable loopback identity so raw protocols can reuse their native ports across projects. |
| Containers | Harbor exposes native endpoints through loopback-only TCP relays to private high host ports. It never relies on Docker container IPs. |
| Service ownership | Data-bearing services remain per-project Compose resources. Harbor shares its control plane, not one global MySQL/Redis/Mailpit stack. |
| Privilege | The desktop and daemon are always unprivileged. An explicitly elevated, one-shot, allowlisted helper installs or repairs owned loopback, resolver, trust-store, and low-port state, then exits. |
| State | Harbor stores machine-local registration and leases; it never rewrites project `.env` files to make routing work. |
| Scope | Harbor orchestrates GoForj's existing apps, Compose services, resource registry, Lighthouse, and API Index/API Reference instead of replacing them. |
| Platforms | A capability is supported only after required macOS, Linux, and Windows system tests pass. No platform may silently degrade while claiming parity. |

## Documents

- [Source development](./development.md) explains how to build and bootstrap the privileged helper from source on macOS and Linux.
- [Product design](./product-design.md) defines the user model, workflows, UI, lifecycle, and product boundary.
- [Frontend](./frontend.md) defines the inherited GoForj starter foundation, Harbor component boundary, Lerd styling adaptation, state bridge, and UI test strategy.
- [Architecture](./architecture.md) defines processes, ownership, IPC, state, reconciliation, privilege, packaging, and recovery.
- [Networking](./networking.md) defines loopback identities, DNS, TLS, HTTP ingress, native-port relays, and container connectivity.
- [GoForj integration](./goforj-integration.md) defines the project descriptor, managed development session, runtime overlay, resource projection, and ownership split.
- [Cross-platform testing](./testing.md) defines required CI jobs and the macOS, Linux, and Windows acceptance matrix.
- [Delivery plan](./delivery-plan.md) defines proof gates, phases, release criteria, and deferred work.
- [Research](./research.md) records the Herd, Yerd, Lerd, Wails, platform, and current-GoForj findings that informed the design.

## Design rule

Use Yerd as the control-plane reference, Lerd as the operational edge-case, test, and visual-layout reference, and Herd as the product-experience reference. Harbor's frontend begins with GoForj's own Vue/shadcn starter rather than a new scaffold. Lerd's narrow navigation rail, dense contextual list, persistent detail pane, and selected styling are adapted through those source-owned shadcn components; Lerd branding and product-specific assets are not copied. Harbor's framework contract must follow GoForj as it exists today.
