# Repository environment overrides

Harbor can translate its runtime facts into project-specific environment variable names without requiring the project or its runtime provider to understand Harbor.

A project opts in with a repository-owned `.harbor.yml`:

```yaml
version: 1

environment:
  MEILISEARCH_HOST:
    from: project.address
```

Harbor resolves these bindings after it assigns the project runtime and supplies the resulting values after the project's dotenv files load. In this example, `MEILISEARCH_HOST` receives the project's dedicated loopback address.

The Environment tab shows each resolved value and its source under **Environment overrides**. These values are read-only because Harbor owns the facts they represent; developers choose only which project environment names consume them.

## Version 1 contract

Version 1 intentionally supports one source:

| Source | Value |
|---|---|
| `project.address` | The stable loopback address Harbor assigned to the project. |

The file is optional. Harbor rejects unknown fields, unsupported versions and sources, non-portable environment names, symlinks, oversized files, and more than 128 bindings. The contract does not execute commands, interpolate templates, or store secrets.

Repository bindings are runtime-neutral. Harbor resolves them before invoking a runtime provider. A provider may still reserve and overwrite values it requires for its own correct operation.
