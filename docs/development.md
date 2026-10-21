# Source development

## Prerequisites

The current checkout requires:

- Go 1.26.1 for both Go modules;
- Node.js 22 for the frontend;
- Wails v3.0.0-beta.32 and its native platform prerequisites;
- a GoForj executable whose embedded build information identifies the canonical `github.com/goforj/goforj/cmd/forj` command and `github.com/goforj/goforj` module.

Runtime admission does not pin a GoForj version, revision, replacement, or working-tree state. Harbor rejects a binary whose embedded command or main-module identity is not canonical (including a replaced main module). The reviewed GoForj revision used by generated-project CI is a deterministic test input, not a runtime compatibility requirement. Harbor's `.goforj.yml` still records `goforj_version: 0.19.0` as render metadata; it does not override runtime executable admission.

CI and development packaging select GoForj `v0.35.1`. To use the same released CLI locally:

```sh
go install github.com/goforj/goforj/cmd/forj@v0.35.1
forj --version
# v0.35.1
```

Run the complete development graph from the repository root:

```sh
forj dev
```

GoForj builds and watches `harbord`, builds the optional sibling `outputbroker`, applies its embedded migrations, runs the daemon in the foreground, and starts Wails. The daemon uses the broker when that exact sibling artifact is available and keeps direct pipes when it is not. The Wails desktop builder runs `cmd/devartifacts` on macOS, which places architecture-specific development helper and broker binaries beneath `desktop/build/bin/devtools`. The desktop uses those artifacts when network setup needs to install or repair privileged support.

The frontend dev server binds to `127.0.0.1` for Wails' IPv4 proxy. Its install task reuses unchanged dependencies, running `npm ci` when `package.json` or `package-lock.json` changes or the installed lockfile is missing. Restart `forj dev` after changing its Taskfile.

On macOS, the desktop builder aligns native compilation, linking, and bundle metadata with the toolchain: macOS 12 for Go 1.26, or macOS 13 for Go 1.27. The packaged installer retains its macOS 15 minimum.

Harbor launches registered projects with GoForj's standard checkout-local development layout. Generated runtime binaries and readiness state remain beneath each project's ignored `./bin` directory; Harbor does not redirect them into a separate runtime artifact tree.

Harbor's durable SQLite state is outside the checkout. On macOS it is `~/Library/Application Support/GoForj/Harbor/harbor.db`; see [Current implementation state](./current-state.md) for all platform paths and validation commands.

## Current CLI surface

The user-facing CLI is available from source as `go run ./cmd/app`. Its Harbor-specific commands are:

```text
setup [--expand-pool] [--resume]
add [path] [--json]
remove <project> [--json] [--intent <intent>]
daemon status [--json]
daemon snapshot
daemon stop
start <project> [--json] [--intent <intent>]
stop <project> [--json] [--intent <intent>]
restart <project> [--json] [--intent <intent>]
status <project> [--json]
open <project> [resource]
logs <project> [--service <id>] [--follow]
doctor [project] [--json]
```

Use `harbor setup --expand-pool` to approve growth from a legacy eight-identity pool to its containing 256-identity pool. Existing project addresses and network policy stay the same. Repeating the command resumes a pending setup approval. The desktop exposes the same action in Settings, Daemon.

Use `harbor setup --resume` to continue the existing pending loopback operation. When the daemon retained independently verified helper effects, it rechecks them and finishes confirmation without requesting another loopback helper approval. Otherwise Resume uses the normal explicit approval flow. Overview and Settings, Daemon expose the same pending action.

Project lifecycle commands return the daemon-authoritative operation and preserve the supplied intent for safe retries.

## Manual helper bootstrap

`harbor-devbootstrap` prepares Harbor's fixed privileged helper layout when the desktop installation flow is unavailable. It is development-only, not a production installer.

On macOS, build the helper, relay, and bootstrap as your normal user, then elevate only the already-built bootstrap:

```sh
mkdir -p ./bin
go build -o ./bin/harbor-helper ./cmd/helper
go build -o ./bin/harbor-devbootstrap ./cmd/devbootstrap
go build -o ./bin/harbor-launchdrelay ./cmd/launchdrelay

sudo ./bin/harbor-devbootstrap \
  --helper "$(pwd -P)/bin/harbor-helper" \
  --launchd-relay "$(pwd -P)/bin/harbor-launchdrelay" \
  --user-id "$(id -u)" \
  --group-id "$(id -g)"
# Harbor development bootstrap complete.
```

The explicit UID and GID identify the unelevated developer who will submit helper tickets. Rebuild and rerun the bootstrap after changing `cmd/helper`; it replaces the installed development helper while preserving valid runtime state. Do not run `harbord`, Wails, or `forj dev` as root.

On Linux, omit the relay build and `--launchd-relay` argument. This command is Unix-only. A Windows source bootstrap is not available yet; the Windows release path will install an Authenticode-signed helper.

The implementation reserves these machine-global path contracts:

| Platform | Privileged state root | Helper executable |
|---|---|---|
| macOS | `/Library/Application Support/GoForj/Harbor/Privileged` | `/Library/PrivilegedHelperTools/com.goforj.harbor.helper` |
| Linux | `/var/lib/goforj/harbor` | `/usr/libexec/harbor-helper` |
| Windows | `%ProgramData%\GoForj\Harbor\Privileged` | `%ProgramFiles%\GoForj\Harbor\harbor-helper.exe` |

Windows resolves ProgramData and Program Files through native Known Folder APIs rather than environment variables. These are security and ownership contracts in source; they are not proof that production installation exists on every platform.

## Native cgo generation

Wire's cached loader can omit a constructor defined in a local cgo file, producing `undefined: trust.New` while direct Go builds succeed. [Wire PR #31](https://github.com/goforj/wire/pull/31) fixes the loader; it is not yet a published tool release. Native maintainers exercising the affected source can install its exact reviewed source temporarily:

```sh
go install github.com/goforj/wire/cmd/wire@5027efedad598786df3012ad311f30cb26fd224b
forj harbord make:migration example_change --connection default --no-open
```

Expect both migration directions beneath `migrations/harbord/default`. Use a new migration name and keep only intentional migrations. This is a generator repair, not a reason to change Harbor's trust providers, hand-edit Wire output, or add persistent loader environment flags. The affected provider graph regenerated twice with no changes using that source. The pinned release commands below still describe the repository tool pin; use the repaired installed `wire` for this native cgo case until the fix is released.

## Generated sources

Generated files must move with their authoritative source. Run the applicable generator after a source change, commit the result together, then run the same generator again and require a clean diff.

| Generated artifact | Authoritative source | Regenerate |
|---|---|---|
| `internal/database/connections_gen.go` | GoForj database configuration and selected drivers | `forj generate --db` from the repository root |
| `app/wire/wire_gen.go` | `app/wire/wire.go` and editable provider sets | `go run github.com/goforj/wire/cmd/wire@v1.2.0` from `app/wire` |
| `app/harbord/wire/wire_gen.go` | `app/harbord/wire/wire.go` and editable provider sets | `go run github.com/goforj/wire/cmd/wire@v1.2.0` from `app/harbord/wire` |
| `desktop/frontend/src/bridge/harbor.fixture.ts` | `desktop/internal/desktopwire`, `desktop/internal/wirefixture`, and `desktop/generate.go` | `go generate ./...` from `desktop` |
| `desktop/frontend/dist` | `desktop/frontend` source | `npm ci && npm run build` from `desktop/frontend` |
| GoForj files marked `Code generated by GoForj CLI` | `.goforj.yml`, GoForj templates, and editable app-owned extension files | `forj render` from the repository root, only after reviewing the render plan and expected overwrite set |

Files such as `inject_*_app.go` are Harbor-owned provider extensions even when adjacent `inject_*.go` files are generated. Do not edit `wire_gen.go`, generated database accessors, the frontend fixture, or hashed distribution files by hand. Do not run a broad render merely to repair one generated mirror; identify and change the authoritative input first.

## Local project creation validation

The creation adapter registry is compiled into the desktop. A new stack supplies its own metadata, schema, renderer, and registration adapter; generic fields use `role`, `depends_on`, and `when` rather than GoForj-specific keys. GoForj is the only shipping adapter in this local implementation. The desktop creation wizard consumes the installed CLI's `forj new --schema` contract and submits explicit `--no-interaction` options. An older CLI returns update guidance. Normal `forj new` stays interactive.

Selectable options can expose optional `preview` terminal text. The shared wizard shows it for the selected option without knowing its provider or field name; missing metadata simply omits the panel. GoForj help previews use the actual formatters rather than copied frontend examples.

The browser contract capture is owned by GoForj. Refresh `desktop/internal/wirefixture/project-creation.schema.json` using the reviewed CLI's `forj new --schema`, then run `go generate ./...` from `desktop` twice and verify the second pass is unchanged. Do not edit the generated TypeScript fixture.

Focused checks live in `internal/projectcreation`, `desktop/project_creation_test.go`, and the schema-driven creation Playwright case. The native WebView smoke has an explicit `HARBOR_NATIVE_CREATION_SMOKE_TARGET` mode in addition to `HARBOR_NATIVE_DESKTOP_SMOKE=1`. Its target must be a new directory under `/tmp`; it uses a local GoForj executable from that process's PATH, the production SPA, real Wails bindings, and the real local daemon. It creates a Vue project, verifies Stopped registration, removes only that proof registration, and retains all generated files. Use a test binary built after the frontend production build. This mode never starts the project or drives network setup. Directory-picker interaction remains a separate native check.

Creation progress is desktop-owned and ephemeral: `StartProjectCreation` returns a job ID, `ProjectCreationProgress` returns bounded cursor-relative output, and `RetryProjectRegistration` accepts only a retained completed job. It never accepts a replacement path or invokes the renderer. Renderer failures retain files and require a new or empty destination for another creation attempt. Use the inline retry while the result dialog is open. After leaving that flow or restarting the desktop, use Add existing project.
