# Harbor Illustration System

Status: approved design standard

Last updated: 2026-07-20

Harbor's illustrations give the product a recognizable emotional setting: a calm, capable harbor where developers build and manage local environments. They are part of Harbor's design language, not miscellaneous decoration. Used consistently, they make an operational desktop tool feel warm and thoughtfully made without weakening its clarity.

This standard governs both atmospheric background artwork and future contextual illustrations. It complements the compact, information-dense application shell defined in [Frontend](./frontend.md); it does not change that shell's hierarchy or turn operational state into scenery.

## Purpose

The illustration system should make Harbor feel:

- calm rather than urgent;
- welcoming rather than clinical;
- handcrafted rather than mechanically generic;
- professional rather than childish;
- grounded in local development rather than abstract cloud infrastructure.

Dropbox, Fly.io, Basecamp, Stripe, and Linear are useful references for warmth, restraint, and editorial craft. They are not source material. Harbor must not trace, reproduce, or closely imitate another product's compositions, characters, icons, palette, or distinctive visual devices. Every finished scene must be original and recognizably Harbor's.

Illustration supports product meaning but never carries it alone. Accurate labels, statuses, controls, and diagnostics remain primary; a metaphor may make a concept memorable, but it cannot replace the concept's real name.

## Design principles

1. **Harbor-native.** Start with the harbor world and the software concept being explained. Do not add nautical objects merely to decorate an otherwise unrelated scene.
2. **Quietly discoverable.** Background artwork should reward attention over time. It must not become the first thing a user notices on a working screen.
3. **Editorial, not diagrammatic.** Favor one clear visual idea, asymmetry, and generous negative space over exhaustive technical representation.
4. **Crafted with control.** Curves and alignments may be slightly imperfect, but silhouettes, spacing, and line weight still need deliberate consistency.
5. **Operational clarity wins.** Move, reduce, mask, or remove an illustration whenever it competes with content. Lower opacity is not permission to place art beneath important text or controls.
6. **One world, many scenes.** Reuse the same shape language, perspective, palette roles, and level of detail so new artwork feels like another view of Harbor rather than a new campaign.
7. **Theme and accessibility are part of the asset.** An illustration is not complete until it has been reviewed in light, dark, system, narrow, zoomed, and forced-color contexts.

## Visual language

Harbor artwork uses flat vector-like forms with soft organic curves. Shapes are rounded, proportions are friendly, and geometry has small human irregularities. Outlines are minimal and slightly softer than the filled forms they define. A scene should remain legible from its silhouette after it has been faded into the application.

Use:

- broad, flat shapes and a small number of overlapping planes;
- rounded corners, rolling shorelines, soft clouds, and calm water;
- restrained outlines with a consistent visual weight;
- simple side-on or lightly elevated views rather than dramatic perspective;
- muted colors with one sparing warm accent;
- subtle paper or grain texture only when it adds tactility at normal size;
- whitespace as an active part of the composition;
- mild asymmetry and imperfect repetition to avoid sterile geometry.

Avoid:

- photorealism, 3D rendering, glossy materials, or lens effects;
- hard shadows, neon bloom, harsh gradients, or high-contrast vignettes;
- busy panoramas, tiny ornamental detail, or technical cutaway diagrams;
- cartoon exaggeration, mascot behavior, or facial expressions that trivialize failures;
- text, UI screenshots, status labels, or logos baked into an asset;
- visual noise presented as handcrafted texture;
- arbitrary cloud, server-rack, hexagon, or circuit-board imagery that abandons the Harbor metaphor.

Texture should be quiet enough to disappear when artwork is rendered at background opacity. Prefer CSS masking, blending, and a very small blur for integration with the shell; do not bake a theme background, edge fade, glow, or global opacity into the source image.

## Harbor metaphor

The harbor world gives software concepts a stable visual vocabulary. These mappings guide illustration concepts; they do not rename the product model.

| Harbor concept | Software concept | Recommended visual treatment |
|---|---|---|
| Harbor or sheltered bay | Harbor control plane | Calm water connecting several independently owned places and vessels. |
| Cargo ship or grouped cargo | Containers and a project runtime | A purposeful vessel carrying distinct loads, not the Docker logo or a literal container diagram. |
| Harbor building | Local App or service | A small, identifiable building connected to the same shoreline while retaining its own function. |
| Lighthouse or beacon | DNS and local discovery | A steady signal guiding traffic to the correct place; use beams sparingly at low contrast. |
| Lit harbor entrance | Trusted HTTPS and host readiness | A safe, clearly marked approach rather than a padlock enlarged into the scene. |
| Warehouse | Storage and durable project data | Sheltered goods that remain in place while ships arrive and leave. |
| Loading dock | Queues and scheduled work | Ordered cargo waiting for deliberate movement rather than a literal list of messages. |
| Waterways and marked channels | Networks and routing | Calm paths between destinations, with buoys or shore markers showing bounded routes. |
| Tugboat | Background worker | A small working vessel helping a larger operation progress without becoming the focal point. |
| Crane | Orchestration and lifecycle actions | Measured movement of cargo; avoid depicting destructive actions as playful machinery. |
| Buoy | Health check, boundary, or endpoint | A small visible marker in the water, useful as a secondary detail rather than a status icon. |
| Island or dock district | Project boundary | A distinct place within one harbor, visibly separate without feeling isolated. |
| Shipping manifest or harbor log | Logs and activity | A contextual prop only; real logs remain readable interface content. |
| Seagull, cloud, or ripple | Atmosphere and continuity | Sparse accents that soften a scene without representing operational state. |

Do not force a metaphor where it makes the interface less clear. Errors, permissions, data loss risk, and security decisions should use direct language and established UI semantics even when nearby artwork belongs to the Harbor world.

## Asset standard

### Runtime format and location

Ship illustration assets locally from `desktop/frontend/src/assets/illustrations/` and import them through the frontend build. A bridge-enabled Wails view must never fetch decorative code or images from a remote URL.

Transparent PNG is the delivery format for raster artwork. Preserve a full alpha channel and export in sRGB. Global background scenes use a consistent 3:2 transparent canvas so the responsive component can swap them without distortion; keep intentional empty canvas around the subject for masks and edge crops. Retain the editable vector or layered source outside the runtime bundle when one exists.

Do not add a solid application-colored backdrop, a baked edge fade, or reduced whole-image opacity. Do not flatten useful transparency. Export clean edges at a resolution suitable for the largest intended CSS size, then optimize losslessly and verify that texture and outlines survive both scaling and low-opacity rendering.

Use lowercase kebab-case names that describe role and subject. Examples include:

- `harbor-background.png` for the global atmospheric scene;
- `empty-projects-dock.png` for a project empty state;
- `network-setup-lighthouse.png` for a contextual setup scene;
- `storage-warehouse-dark.png` only when a tested theme-specific source is genuinely necessary.

Prefer one theme-neutral source adjusted by CSS. When separate light and dark exports are unavoidable, keep their composition, dimensions, crop, and semantic content identical and use the `-light` and `-dark` suffixes.

### Provenance

Artwork must be original, commissioned for Harbor, or used under terms that permit its distribution and modification. The change introducing an asset must record its creator or source, creation method, editable-source location when applicable, usage rights, and any incorporated third-party material. Generated artwork should also record the tool and enough source context to reproduce or deliberately revise it.

Third-party notices and attribution belong in the repository and packaged application when the license requires them. Product references are mood and quality references only; their names do not establish acceptable provenance. Do not trace screenshots, reuse brand assets, or make small alterations to somebody else's illustration and call it Harbor artwork.

## Palette and composition

The palette should feel like a quiet working waterfront. Build most scenes from desaturated harbor blue, blue-gray, fog, sea-glass green, sand, and warm off-white. Clay, coral, or signal orange may provide one small point of warmth. Dark outlines should be softened toward slate or blue-gray rather than pure black.

These are illustration roles, not a second set of application status tokens. Do not use ready, degraded, or failed colors decoratively near status-bearing UI, and do not rely on the artwork's colors to communicate state. Review every asset against Harbor's semantic light and dark backgrounds rather than copying literal colors from a reference brand.

A strong composition normally has:

- one readable idea or focal cluster;
- a few large forms instead of many equally weighted objects;
- open water, sky, or transparent canvas around the subject;
- an asymmetric balance that leaves room for the interface;
- calm horizontal movement and stable silhouettes;
- enough margin for responsive cropping without cutting through the focal object.

Background scenes should carry less local contrast and detail than contextual scenes. Repeated containers, windows, waves, or birds should vary slightly, but the variation must look intentional. Avoid filling every open area simply because the canvas permits it.

## Placement model

Harbor has two illustration roles with different constraints.

| Role | Purpose | Placement | Presentation |
|---|---|---|---|
| Atmospheric background | Establish identity and mood behind a working screen. | Fixed to the shell, normally bottom-right, in verified negative space. | Always decorative, masked, and rendered at 4–10% opacity. |
| Contextual illustration | Support onboarding, an empty state, or a focused explanation. | Contained in that state's layout, adjacent to its copy and action. | May be more visible, but must not replace text, status, or instructions. |

Use at most one atmospheric illustration on a screen. Bottom-right is the default because it balances Harbor's left-side navigation and contextual pane. Top-right, bottom-left, and top-left are available only when the screen's negative space makes them safer. A placement decision belongs to the screen composition, not to the subject's direction alone.

An atmospheric illustration must sit behind every interactive layer and outside the document's reading order. It must not create a visual collision with headings, addresses, logs, tables, actions, focus rings, toasts, dialogs, or the compact navigation. At each responsive layout, first reposition it, then choose a smaller size or stronger fade, and finally omit it if no safe region remains. Do not solve a collision by making content translucent.

Contextual illustrations should be reserved for states that benefit from warmth or orientation, such as first-run setup and genuinely empty collections. Dense project details, logs, diagnostics, permission prompts, destructive confirmations, and active failure states generally do not need a contextual scene. If a contextual illustration conveys information, that information must also appear in accessible text.

## Background behavior

Atmospheric artwork should feel integrated with the application background rather than pasted onto it:

- preserve source transparency;
- use a CSS mask or restrained gradient to fade the inner and outer edges naturally;
- prefer a small CSS blur and theme-aware filter or blend mode over modifying the source PNG;
- keep effective opacity between `0.04` and `0.10`, inclusive;
- reduce toward `0.04` on narrow screens or visually busy views;
- scale with responsive `clamp()`-style bounds and `background-size: contain` while preserving the 3:2 canvas;
- allow only intentional cropping of transparent breathing room at the viewport edge;
- use a fixed position without contributing to layout size or causing layout shift;
- disable pointer events and text/image selection.

Opacity is the final rendered opacity, after a caller override and theme behavior are resolved. It should never be raised above the range to compensate for a weak asset. If a scene is not recognizable at this treatment, simplify its silhouette or choose a more suitable scene.

## Light, dark, and system themes

The same source should normally work in both themes through centralized CSS treatment. Light mode may use a restrained multiply-style blend and gentle desaturation; dark mode may use a screen-style blend with lower brightness, saturation, and opacity. The goal is equivalent perceptual weight, not identical computed values.

Use the current background scene as a baseline: roughly 6–7% in light mode, approximately 5% in dark mode, and 4% at narrow widths. These are review targets within the required range, not colors to bake into an image. Avoid automatic full inversion, which can change warm accents and outline relationships unpredictably.

System theme follows the resolved light or dark treatment without flashing the other variant during startup. Test every placement with real content in both themes. In forced-colors mode, hide atmospheric artwork; it provides no information and should not interfere with system-enforced contrast.

## Accessibility and interaction

Atmospheric artwork is presentation-only. `HarborIllustration` must render it with `aria-hidden="true"`, no accessible name, no focus target, no pointer handling, and no selectable content. A CSS background is preferred so the scene cannot be dragged or announced as an image.

Illustrations must never be the sole representation of a service, state, instruction, or result. Meaningful contextual art needs equivalent nearby text; purely decorative contextual art is also hidden from assistive technology. Do not put localized copy inside raster artwork.

No illustration should animate by default. Future motion requires a product reason, a static equivalent, and `prefers-reduced-motion` behavior. Decorative treatment is excluded from contrast calculations: all foreground content and focus indicators must meet their requirements without relying on the illustration or its fade.

## Reusable implementation contract

The Harbor-owned `HarborIllustration.vue` component lives outside the preserved shadcn primitive layer and is the only shell-level mechanism for atmospheric artwork. Screens import a local asset and configure composition through the component API rather than copying positioning, mask, or theme CSS.

| Prop | Contract |
|---|---|
| `image` | Required local, build-resolved asset URL. The component must not fetch or interpret remote content. |
| `placement` | `top-left`, `top-right`, `bottom-left`, or `bottom-right`; bottom-right is the normal default. Placement selects both anchoring and mask origin. |
| `opacity` | Optional numeric override clamped to `0.04`–`0.10`. Omission uses the reviewed theme default. |
| `size` | `compact`, `standard`, or `wide`. Each preset remains responsive and preserves the standard background canvas ratio. Use `standard` unless screen composition justifies another preset. |
| `fade` | `soft`, `balanced`, or `strong`. This changes the CSS mask, not the source image. Use `balanced` by default and `strong` when content approaches the safe region. |

Example:

```vue
<script setup lang="ts">
import harborBackground from '@/assets/illustrations/harbor-background.png'
import HarborIllustration from '@/components/harbor/HarborIllustration.vue'
</script>

<template>
  <HarborIllustration
    :image="harborBackground"
    placement="bottom-right"
    :opacity="0.065"
    size="wide"
    fade="balanced"
  />
</template>
```

The shell establishes an isolated stacking context. `HarborIllustration` occupies the background layer; rails, panes, navigation, feedback, dialogs, and all other UI occupy higher layers. Component CSS owns fixed positioning, responsive bounds, aspect ratio, background containment, masks including the WebKit-prefixed form needed by embedded WebViews, theme filters, blend mode, pointer behavior, selection behavior, and forced-color suppression.

New variants belong in the component's typed props and centralized semantic styling. Do not add screen-specific absolute-position utilities or mutate a raster until a reusable placement, size, fade, or theme treatment has been considered. Contextual illustrations may later gain a separate semantic component because their layout and accessibility needs differ from a fixed background; they should share this asset and provenance standard rather than overloading `HarborIllustration`.

Tests should prove that the component:

- remains `aria-hidden`, non-interactive, and non-selectable;
- clamps opacity to the approved range;
- maps every placement, size, and fade value to a stable treatment;
- preserves a local image URL and aspect ratio;
- remains behind application surfaces;
- has a non-empty mask in supported WebViews;
- renders with appropriately quiet light, dark, and narrow-screen treatment;
- disappears in forced-colors mode.

## Adding or reviewing artwork

Before accepting a new illustration:

1. Name the product concept and its Harbor metaphor.
2. Decide whether the piece is atmospheric or contextual and identify its safe region.
3. Review the composition at intended size before relying on opacity or a mask.
4. Export and optimize a transparent, correctly sized asset without a baked application background.
5. Record provenance, editable source, creation method, and rights.
6. Integrate it through the reusable component or the approved contextual pattern.
7. Test light, dark, system, narrow, wide, 200% zoom, and forced-colors behavior with representative content.
8. Remove it if it delays recognition of state, weakens readability, or makes a serious workflow feel playful.

## Future illustration ideas

The system can grow through a small library of purposeful scenes:

- an empty-project dock waiting for its first vessel;
- a lighthouse and marked channel for network setup and DNS readiness;
- several distinct islands connected by calm waterways for concurrent projects;
- a warehouse with preserved cargo for storage and unregister behavior;
- an orderly loading dock for queues and scheduled jobs;
- tugboats moving quietly around a cargo ship for background workers;
- a crane placing containers for start, rebuild, and orchestration progress;
- harbor buildings opening their lights for local services becoming ready;
- a weather buoy and sheltered breakwater for diagnostics and host health;
- a compact night-harbor scene for onboarding in dark mode, using the same composition language rather than a separate visual identity.

Add scenes slowly. A small, coherent set used with restraint will build more identity than a large catalog of loosely related nautical artwork.
