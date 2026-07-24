# Repository environment overrides

Harbor can translate its runtime facts into project-specific environment variable names without requiring the project or its runtime provider to understand Harbor.

A project opts in with a repository-owned `.harbor.yml`. The Environment tab can create and edit the same contract through **Environment overrides → Project mappings**:

```yaml
version: 1

environment:
  MEILISEARCH_HOST:
    from: project.address
```

Harbor resolves these bindings after it assigns the project runtime and supplies the resulting values after the project's dotenv files load. In this example, `MEILISEARCH_HOST` receives the project's dedicated loopback address.

The mappings are editable because the repository owns the application variable names. Harbor saves them with revision checks so it cannot overwrite a `.harbor.yml` that changed outside the app.

The same view shows the resolved values and their sources under **Effective read-only values**. These values are read-only because Harbor owns the facts they represent; developers choose only which project environment names consume them.

## Version 1 contract

Version 1 intentionally supports one source:

| Source | Value |
|---|---|
| `project.address` | The stable loopback address Harbor assigned to the project. |

The file is optional. Harbor rejects unknown fields, unsupported versions and sources, non-portable environment names, symlinks, oversized files, and more than 128 bindings. The contract does not execute commands, interpolate templates, or store secrets.

Repository bindings are runtime-neutral. Harbor resolves them before invoking a runtime provider. A provider may still reserve and overwrite values it requires for its own correct operation.
