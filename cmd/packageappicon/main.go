// Package main compiles Harbor's canonical icon into a modern macOS asset catalog.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const iconComposerContents = `{
  "fill": {
    "automatic-gradient": "srgb:1.00000,0.03529,0.09020,1.00000"
  },
  "groups": [
    {
      "layers": [
        {
          "image-name": "harbor.svg",
          "name": "Harbor"
        }
      ],
      "shadow": {
        "kind": "neutral",
        "opacity": 0.15
      },
      "specular": false,
      "translucency": {
        "enabled": false,
        "value": 0
      }
    }
  ],
  "supported-platforms": {
    "circles": [],
    "squares": "shared"
  }
}
`

// commandRunner executes one native packaging command.
type commandRunner func(context.Context, string, string, ...string) error

// main packages the modern icon after Wails has assembled the macOS app bundle.
func main() {
	workingDirectory, err := os.Getwd()
	if err == nil {
		err = run(context.Background(), workingDirectory, os.Args[1:], runCommand)
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "package Harbor macOS app icon: %v\n", err)
		os.Exit(1)
	}
}

// run validates Wails' fixed bundle shape before compiling and signing its icon assets.
func run(ctx context.Context, workingDirectory string, arguments []string, runner commandRunner) error {
	if len(arguments) != 1 {
		return errors.New("expected the Wails application executable path")
	}
	if runner == nil {
		panic("macOS app icon packager requires a command runner")
	}

	desktopDirectory, appBundle, resourcesDirectory, err := packagePaths(workingDirectory, arguments[0])
	if err != nil {
		return err
	}
	sourceIcon := filepath.Join(desktopDirectory, "build", "appicon.png")
	if _, err := os.Stat(sourceIcon); err != nil {
		return fmt.Errorf("read canonical app icon: %w", err)
	}
	composerArtwork := filepath.Join(desktopDirectory, "build", "appicon-symbol.svg")
	if _, err := os.Stat(composerArtwork); err != nil {
		return fmt.Errorf("read Icon Composer artwork: %w", err)
	}

	if err := runner(ctx, workingDirectory, "/usr/bin/xcrun", "--find", "actool"); err != nil {
		_, _ = fmt.Fprintln(
			os.Stderr,
			"warning: full Xcode is unavailable; keeping Wails' legacy macOS app icon",
		)
		return nil
	}

	temporaryDirectory, err := os.MkdirTemp("", "harbor-appicon-")
	if err != nil {
		return fmt.Errorf("create app icon workspace: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(temporaryDirectory)
	}()

	for _, name := range []string{"AppIcon.icon", "AppIcon.icns", "Assets.car"} {
		if err := os.RemoveAll(filepath.Join(resourcesDirectory, name)); err != nil {
			return fmt.Errorf("remove stale macOS icon resource %s: %w", name, err)
		}
	}

	iconComposerDirectory := filepath.Join(temporaryDirectory, "AppIcon.icon")
	iconComposerAssetsDirectory := filepath.Join(iconComposerDirectory, "Assets")
	if err := os.MkdirAll(iconComposerAssetsDirectory, 0o755); err != nil {
		return fmt.Errorf("create Icon Composer document: %w", err)
	}
	if err := os.WriteFile(filepath.Join(iconComposerDirectory, "icon.json"), []byte(iconComposerContents), 0o644); err != nil {
		return fmt.Errorf("write Icon Composer document: %w", err)
	}
	sourceBytes, err := os.ReadFile(composerArtwork)
	if err != nil {
		return fmt.Errorf("read Icon Composer artwork: %w", err)
	}
	if err := os.WriteFile(filepath.Join(iconComposerAssetsDirectory, "harbor.svg"), sourceBytes, 0o644); err != nil {
		return fmt.Errorf("write Icon Composer artwork: %w", err)
	}

	partialInfoPath := filepath.Join(temporaryDirectory, "asset-info.plist")
	if err := runner(
		ctx,
		temporaryDirectory,
		"/usr/bin/xcrun",
		"actool",
		"--compile", resourcesDirectory,
		"--platform", "macosx",
		"--minimum-deployment-target", "10.13",
		"--app-icon", "AppIcon",
		"--output-partial-info-plist", partialInfoPath,
		iconComposerDirectory,
	); err != nil {
		return fmt.Errorf("compile app icon catalog: %w", err)
	}
	bundledComposerDirectory := filepath.Join(resourcesDirectory, "AppIcon.icon")
	if err := os.RemoveAll(bundledComposerDirectory); err != nil {
		return fmt.Errorf("replace bundled Icon Composer document: %w", err)
	}
	if err := os.Rename(iconComposerDirectory, bundledComposerDirectory); err != nil {
		return fmt.Errorf("install bundled Icon Composer document: %w", err)
	}
	legacyIconPath := filepath.Join(resourcesDirectory, "iconfile.icns")
	if err := os.Remove(legacyIconPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy macOS icon: %w", err)
	}
	infoPath := filepath.Join(appBundle, "Contents", "Info.plist")
	if err := runner(
		ctx,
		temporaryDirectory,
		"/usr/libexec/PlistBuddy",
		"-c", "Merge "+partialInfoPath,
		infoPath,
	); err != nil {
		return fmt.Errorf("merge compiled macOS icon metadata: %w", err)
	}

	if err := runner(ctx, temporaryDirectory, "/usr/bin/codesign", "--force", "--deep", "--sign", "-", appBundle); err != nil {
		return fmt.Errorf("sign app bundle after icon packaging: %w", err)
	}
	return nil
}

// packagePaths restricts packaging to the app bundle produced beneath desktop/build/bin.
func packagePaths(workingDirectory string, executablePath string) (string, string, string, error) {
	absoluteWorkingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve Wails build directory: %w", err)
	}
	absoluteWorkingDirectory = filepath.Clean(absoluteWorkingDirectory)
	buildDirectory := filepath.Dir(absoluteWorkingDirectory)
	desktopDirectory := filepath.Dir(buildDirectory)
	if filepath.Base(absoluteWorkingDirectory) != "bin" ||
		filepath.Base(buildDirectory) != "build" ||
		filepath.Base(desktopDirectory) != "desktop" {
		return "", "", "", fmt.Errorf("Wails hook directory %q is not desktop/build/bin", absoluteWorkingDirectory)
	}

	absoluteExecutable, err := filepath.Abs(executablePath)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve Wails executable: %w", err)
	}
	absoluteExecutable = filepath.Clean(absoluteExecutable)
	macosDirectory := filepath.Dir(absoluteExecutable)
	contentsDirectory := filepath.Dir(macosDirectory)
	appBundle := filepath.Dir(contentsDirectory)
	if filepath.Base(macosDirectory) != "MacOS" ||
		filepath.Base(contentsDirectory) != "Contents" ||
		filepath.Ext(appBundle) != ".app" ||
		filepath.Dir(appBundle) != absoluteWorkingDirectory {
		return "", "", "", fmt.Errorf("Wails executable %q is not inside desktop/build/bin/*.app", absoluteExecutable)
	}

	return desktopDirectory, appBundle, filepath.Join(contentsDirectory, "Resources"), nil
}

// runCommand preserves native tool diagnostics for failed packaging steps.
func runCommand(ctx context.Context, directory string, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	if name == "/usr/bin/xcrun" &&
		len(arguments) == 2 &&
		arguments[0] == "--find" &&
		arguments[1] == "actool" {
		return command.Run()
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
