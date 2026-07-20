# Harbor Agent Instructions

These instructions extend the workspace-level `AGENTS.md` for work in this repository.

## Read Before Changing Code

Use this order:

1. Inspect `git status` and preserve every change you did not create.
2. Read `docs/current-state.md` for implemented behavior and known gaps.
3. Read `docs/handoff.md` for the current continuation point, dirty-worktree boundary, and immediate goal.
4. Read the relevant approved target document linked from `docs/README.md`.
5. Use `docs/delivery-plan.md` and `docs/testing.md` for milestone and proof requirements.

For product or frontend work, also read `docs/research.md`, `docs/frontend.md`, and `docs/illustrations.md`: Herd is the product-experience reference, Yerd the control-plane reference, and Lerd the operational/test/visual-layout reference. These are research inputs, not substitutes for Harbor's approved design, illustration language, or GoForj's project semantics.

Code and tests are authoritative for present behavior. The target design is authoritative for the intended product. A design statement is not evidence that a capability is implemented, and a temporary implementation bridge does not silently replace the target design.

## Product Invariants

- `harbord` is the sole durable-state writer and reconciler. CLI, desktop, and tray surfaces remain clients.
- GoForj owns project composition and the inner development graph. Harbor orchestrates `forj dev`; it does not reimplement `.goforj.yml` semantics.
- Projects retain their own Apps, Compose services, containers, credentials, versions, and volumes.
- The daemon and desktop remain unprivileged. Machine changes use explicit, one-shot, allowlisted helper operations.
- A port, PID, path, command line, or environment file is not process-ownership proof. Revalidate exact native birth and scope evidence before signaling, and send no signal when identity drifts or is ambiguous.
- One failed or quarantined project must not prevent the daemon or unrelated projects from operating.
- Do not wipe durable state, delete a checkout, remove volumes, or mutate foreign host state as ordinary recovery.
- Do not claim macOS, Linux, or Windows support from a cross-compile. Crucial networking, resolver, certificate, privilege, desktop, and cleanup behavior needs native OS evidence.
- The bounded `.env.host` managed block is an accepted temporary bridge. Preserve it until the typed GoForj managed-session overlay is implemented and proved; do not broaden it into a second project manifest.

## Repository Boundaries

Harbor has two Go modules:

- the root headless module;
- `desktop`, which contains Wails and imports the root through its intentional relative `replace`.

The Vue frontend under `desktop/frontend` is a third independent build and test surface. Root `go test ./...` does not validate either nested surface.

Use focused tests while iterating, then validate every affected boundary. The complete commands are maintained in `docs/current-state.md`. Cross-platform adapters require focused native or CI coverage in addition to portable unit tests.

The ordinary source-development entrypoint is `forj dev`. Do not add special environment flags to make Harbor assignment work, and do not run the development graph elevated.

## Worktree Discipline

- Treat the dirty boundary in `docs/handoff.md` and live `git status` as authoritative. Uncommitted work may belong to another active task even when it appears related.
- Check status before editing, before staging, and after committing. Stage explicit paths, inspect the cached diff, and never use `git add -A` in a mixed worktree.
- Keep logical semantic commits under `Chris Miles <chris.miles.e@gmail.com>`. Do not push or rewrite unrelated commits unless the user explicitly asks.

## Generated And Persisted Sources

The exact source-to-generator map and commands are maintained in `docs/development.md`.

- Migrations are embedded through GoForj's migration stream. Add migrations through the established generator/layout; do not add ad hoc startup schema creation.
- Editable `*_app.go` Wire providers are app-owned; checked-in `wire_gen.go` files are generated. Do not confuse those ownership boundaries.
- Desktop wire fixtures are Go-owned and must be regenerated with their authoritative Go contract.
- Frontend distribution assets are build output. Never hand-edit hashed assets.
- GoForj-generated files marked `DO NOT EDIT` remain generator-owned. Change their source or template and regenerate rather than patching only the mirror.
- Run every applicable generator twice and verify that the second run produces no diff.

## Documentation And Handoffs

- Update `docs/current-state.md` when implemented behavior, validation, or known gaps change.
- Update target design documents only for an intentional product decision, and keep the implementation/design distinction visible.
- Update `docs/handoff.md` at a stopping point with the exact worktree boundary, evidence, unresolved risks, and one concrete next goal.
- Never present a historical PID, address, path, or database row as current without re-observing it.
- Keep root `README.md` suitable for future users: describe release status honestly and link to detail instead of implying unfinished cross-platform behavior is available.
