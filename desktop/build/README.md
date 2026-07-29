# Build Directory

The build directory is used to house all the build files and assets for your application.

`appicon.png` is Harbor's canonical full-bleed application icon consumed by
Wails. Keep its canvas opaque so macOS applies the platform mask only once.
Keep the checked-in Windows icon generated from that same PNG so native
application and installer surfaces use the same mark.

`appicon-symbol.svg` is the transparent anchor layer used only by Icon Composer.
On macOS, the Wails post-build hook combines that layer with Harbor's red
background in a temporary `AppIcon.icon` document, asks `actool` to generate
the applicable compiled resources in an isolated staging directory, installs
only `Assets.car`, then re-signs the development bundle. The temporary source
and fallback outputs are not runtime resources. This enhancement requires full
Xcode's `actool`; source development keeps the legacy icon without failing when
only Apple's command-line tools are installed.

After modern compilation succeeds, the hook removes Wails' legacy
`iconfile.icns`, `actool`'s legacy `AppIcon.icns` fallback, the raw Composer
document, and `CFBundleIconFile`. This leaves `Assets.car` selected solely
through `CFBundleIconName`; retaining either legacy file can make macOS 26 choose
the framed bitmap instead of the Icon Composer stack. Before compilation the
hook clears prior generated icon outputs because `wails dev` reuses its
application bundle and would otherwise retain resources from an older layout.

The structure is:

* bin - Output directory
* darwin - macOS specific files
* windows - Windows specific files

## Mac

The `darwin` directory holds files specific to Mac builds.
These may be customised and used as part of the build. To return these files to the default state, simply delete them
and
build with `wails build`.

The directory contains the following files:

- `Info.plist` - the main plist file used for Mac builds. It is used when building using `wails build`.
- `Info.dev.plist` - same as the main plist file but used when building using `wails dev`.

## Windows

The `windows` directory contains the manifest and rc files used when building with `wails build`.
These may be customised for your application. To return these files to the default state, simply delete them and
build with `wails build`.

- `icon.ico` - The icon used for the application. This is used when building using `wails build`. If you wish to
  use a different icon, simply replace this file with your own. If it is missing, a new `icon.ico` file
  will be created using the `appicon.png` file in the build directory.
- `installer/*` - The files used to create the Windows installer. These are used when building using `wails build`.
- `info.json` - Application details used for Windows builds. The data here will be used by the Windows installer,
  as well as the application itself (right click the exe -> properties -> details)
- `wails.exe.manifest` - The main application manifest file.
