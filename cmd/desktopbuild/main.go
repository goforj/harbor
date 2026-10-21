// Package main builds the native desktop while preserving Harbor's installer payload layout.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// main keeps platform packaging in one command shared by development and CI.
func main() {
	dev := flag.Bool("dev", false, "build with the Wails development runtime")
	release := flag.Bool("release", false, "enable installed-product daemon activation")
	flag.Parse()
	if err := build(*dev, *release); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// build runs from desktop so embedded assets and installed payload paths remain unchanged.
func build(dev, release bool) error {
	if _, err := os.Stat("go.mod"); err != nil {
		return err
	}
	if _, err := os.Stat("frontend/dist"); err != nil {
		return errors.New("run desktopbuild from desktop after building the frontend")
	}
	output := filepath.Join("build", "bin", "harbor-desktop")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	if needsDevelopmentArtifacts(runtime.GOOS, dev, release) {
		if err := command("go", "run", "github.com/goforj/harbor/cmd/devartifacts"); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		resource := "harbor_windows_" + runtime.GOARCH + ".syso"
		defer os.Remove(resource)
		if err := command("wails3", "generate", "syso", "-arch", runtime.GOARCH, "-icon", "build/windows/icon.ico", "-info", "build/windows/info.json", "-manifest", "build/windows/wails.exe.manifest", "-out", resource); err != nil {
			return err
		}
	}
	args := buildArguments(runtime.GOOS, output, os.Getenv("HARBOR_DESKTOP_LDFLAGS"), dev, release)
	if err := command("go", args...); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	bundle := filepath.Join("build", "bin", "Harbor.app", "Contents")
	for _, directory := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(bundle, directory), 0o755); err != nil {
			return err
		}
	}
	plist := "Info.plist"
	if dev {
		plist = "Info.dev.plist"
	}
	for source, target := range map[string]string{
		output:                                  filepath.Join(bundle, "MacOS", "harbor-desktop"),
		filepath.Join("build", "darwin", plist): filepath.Join(bundle, "Info.plist"),
	} {
		if err := copyFile(source, target); err != nil {
			return err
		}
	}
	if err := command("/usr/libexec/PlistBuddy", "-c", "Set :LSMinimumSystemVersion "+darwinDeploymentTarget(runtime.Version()), filepath.Join(bundle, "Info.plist")); err != nil {
		return err
	}
	// Retain the legacy source-development fallback when full Xcode is unavailable.
	if err := command("wails3", "generate", "icons", "-input", "build/appicon.png", "-macfilename", filepath.Join(bundle, "Resources", "iconfile.icns"), "-windowsfilename", filepath.Join("build", "bin", "icon.ico")); err != nil {
		return err
	}
	executable, err := filepath.Abs(filepath.Join(bundle, "MacOS", "harbor-desktop"))
	if err != nil {
		return err
	}
	return commandIn(filepath.Join("build", "bin"), "go", "run", "github.com/goforj/harbor/cmd/packageappicon", executable)
}

// needsDevelopmentArtifacts retains the Darwin-only source bootstrap supported before the Wails migration.
func needsDevelopmentArtifacts(platform string, dev, release bool) bool {
	return platform == "darwin" && (dev || !release)
}

// buildArguments preserves release reproducibility while retaining available development source provenance.
func buildArguments(platform, output, extraLDFlags string, dev, release bool) []string {
	tags := "production"
	if release {
		tags += ",release"
	}
	ldflags := "-w -s"
	vcs := "-buildvcs=false"
	if dev {
		tags = "dev"
		ldflags = ""
		vcs = "-buildvcs=auto"
	}
	if platform == "linux" {
		tags += ",gtk3"
	}
	if platform == "windows" && !dev {
		ldflags += " -H windowsgui"
	}
	if extraLDFlags != "" {
		ldflags += " " + extraLDFlags
	}
	if platform == "darwin" {
		ldflags += " -extldflags=-mmacosx-version-min=" + darwinDeploymentTarget(runtime.Version())
	}
	return []string{"build", "-trimpath", vcs, "-tags", tags, "-ldflags", ldflags, "-o", output, "."}
}

// command inherits process-local build configuration without shell interpretation.
func command(name string, arguments ...string) error {
	return commandIn("", name, arguments...)
}

// commandIn runs existing fixed-path packaging hooks in their expected build directory.
func commandIn(directory, name string, arguments ...string) error {
	cmd := exec.Command(name, arguments...)
	cmd.Dir = directory
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if runtime.GOOS == "darwin" && name == "go" {
		cmd.Env = darwinBuildEnvironment(os.Environ(), runtime.Version())
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, arguments, err)
	}
	return nil
}

// darwinDeploymentTarget follows the compiler's supported OS floor without raising the module's Go requirement.
func darwinDeploymentTarget(goVersion string) string {
	if version.Compare(goVersion, "go1.27") >= 0 {
		return "13.0"
	}
	return "12.0"
}

// darwinBuildEnvironment puts deployment targets in cgo's cache inputs as well as the native compiler environment.
func darwinBuildEnvironment(environment []string, goVersion string) []string {
	target := darwinDeploymentTarget(goVersion)
	result := append([]string(nil), environment...)
	for _, key := range []string{"CGO_CFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS", "MACOSX_DEPLOYMENT_TARGET"} {
		value := ""
		filtered := make([]string, 0, len(result)+1)
		for _, entry := range result {
			if strings.HasPrefix(entry, key+"=") {
				value = strings.TrimPrefix(entry, key+"=")
			} else {
				filtered = append(filtered, entry)
			}
		}
		if key == "MACOSX_DEPLOYMENT_TARGET" {
			value = target
		} else {
			if value == "" {
				value = "-O2 -g"
			}
			value += " -mmacosx-version-min=" + target
		}
		result = append(filtered, key+"="+value)
	}
	return result
}

// copyFile preserves executable permissions when assembling Harbor's app bundle.
func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, info.Mode().Perm())
}
