# Source development

Run the complete development graph from the repository root:

```sh
forj dev
```

GoForj builds and watches `harbord`, applies its embedded migrations, runs the daemon in the foreground, and starts Wails. The Wails macOS pre-build hook runs `cmd/devartifacts`, which places architecture-specific development helper binaries beneath `desktop/build/bin/devtools`. The desktop uses those artifacts when network setup needs to install or repair privileged support.

Harbor's durable SQLite state is outside the checkout. On macOS it is `~/Library/Application Support/GoForj/Harbor/harbor.db`; see [Current implementation state](./current-state.md) for all platform paths and validation commands.

## Manual helper bootstrap

`harbor-devbootstrap` prepares Harbor's fixed privileged helper layout when the desktop installation flow is unavailable. It is development-only, not a production installer.

From the repository root, build both binaries as your normal user, then elevate only the already-built bootstrap:

```sh
mkdir -p ./bin
go build -o ./bin/harbor-helper ./cmd/helper
go build -o ./bin/harbor-devbootstrap ./cmd/devbootstrap

sudo ./bin/harbor-devbootstrap \
  --helper "$(pwd -P)/bin/harbor-helper" \
  --user-id "$(id -u)" \
  --group-id "$(id -g)"
# Harbor development bootstrap complete.
```

The explicit UID and GID identify the unelevated developer who will submit helper tickets. Rebuild and rerun the bootstrap after changing `cmd/helper`; it replaces the installed development helper while preserving valid runtime state. Do not run `harbord`, Wails, or `forj dev` as root.

This command is Unix-only. A Windows source bootstrap is not available yet; the Windows release path will install an Authenticode-signed helper.
