# Harbor native build assets

Wails v3 uses `desktop/Taskfile.yml`, `build/config.yml`, and the root
`cmd/desktopbuild` command. Build the frontend first with `wails3 task frontend`,
then run `wails3 build` from `desktop`. Native outputs retain the installer-owned
`build/bin/Harbor.app/Contents/MacOS/harbor-desktop` path on macOS and
`build/bin/harbor-desktop[.exe]` on Linux and Windows.

`appicon.png` is Harbor's canonical opaque application and tray icon. The
checked-in Windows `windows/icon.ico` comes from the same artwork.
`appicon-symbol.svg` is the transparent anchor layer for macOS Icon Composer.

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
