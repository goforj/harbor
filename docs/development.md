# Source development

`harbor-devbootstrap` prepares Harbor's fixed privileged helper layout when running the project from source on macOS or Linux. It is development-only, not a production installer.

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

The explicit UID and GID identify the unelevated developer who will submit helper tickets. Rebuild and rerun the bootstrap after changing `cmd/helper`; it replaces the installed development helper while preserving valid runtime state.

This command is Unix-only. A Windows source bootstrap is not available yet; the Windows release path will install an Authenticode-signed helper.
