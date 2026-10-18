# Harbor native build assets

Wails v3 uses `desktop/Taskfile.yml`, `build/config.yml`, and the root
`cmd/desktopbuild` command. Build the frontend first with `wails3 task frontend`,
then run `wails3 build` from `desktop`. Native outputs retain the installer-owned
`build/bin/Harbor.app/Contents/MacOS/harbor-desktop` path on macOS and
`build/bin/harbor-desktop[.exe]` on Linux and Windows.

`appicon.png` is Harbor's canonical opaque application icon and the Windows/Linux tray icon. The
checked-in Windows `windows/icon.ico` comes from the same artwork.
`appicon-symbol.svg` is the transparent anchor layer for macOS Icon Composer.
On macOS, leave Wails' application `Icon` unset so the Dock uses the compiled
bundle catalog. Setting it to the raw PNG overrides the native icon rendering.
The menu bar uses `tray-template.png`, a transparent black anchor passed to
`SetTemplateIcon` so macOS controls its appearance in light and dark modes.

Regenerate the menu bar asset from the canonical SVG paths with
`node desktop/build/generate-tray-icon.mjs` from the repository root after
installing the frontend dependencies and Playwright Chromium. The checked-in
48-pixel PNG supports high-density menu bars; normal builds need no browser.

The builder invokes `cmd/packageappicon` from its fixed `build/bin` directory.
With full Xcode, that command compiles a temporary Composer document into
`Assets.car`, removes legacy `.icns` and raw Composer resources, selects
`CFBundleIconName=AppIcon`, and re-signs the bundle. When `actool` is unavailable,
source development keeps the legacy icon fallback. Package CI requires Xcode 26
and the compiled catalog.

The Darwin plists and Windows JSON/manifest are Harbor-owned literal build
inputs, not v2 CLI templates. The development plist retains
`com.goforj.harbor.dev`; the packaged plist retains `com.goforj.harbor`.
`cmd/desktopbuild` passes the checked-in Windows icon, manifest, and version
metadata to the pinned v3 resource generator before compiling the executable.

Product installer definitions live under root `packaging/`; the historical
`windows/installer` files are not used by Harbor's package workflows.
Do not regenerate this directory from generic Wails templates, which would
replace Harbor's identity and artwork.
