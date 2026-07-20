# Harbor Illustration Assets

Runtime illustration files in this directory follow the repository's `docs/illustrations.md` standard and are imported through Vite rather than fetched remotely.

## Provenance

| Asset | Role | Source | Editable source | Attribution |
|---|---|---|---|---|
| `harbor-background.png` | Global atmospheric harbor scene | Provided by project owner Chris Miles for inclusion in Harbor on 2026-07-20 | Not provided | No third-party attribution was supplied. |

The runtime asset is a 1536×1024 RGBA PNG. Preserve its alpha channel; apply opacity, theme treatment, edge fading, and responsive placement through `HarborIllustration.vue` and CSS rather than baking those effects into the file.
