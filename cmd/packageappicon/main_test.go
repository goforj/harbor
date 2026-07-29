package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestIconDocumentsAreValidJSON keeps the generated Apple asset inputs parseable.
func TestIconDocumentsAreValidJSON(t *testing.T) {
	for name, document := range map[string]string{
		"Icon Composer": iconComposerContents,
	} {
		if !json.Valid([]byte(document)) {
			t.Fatalf("%s document is not valid JSON", name)
		}
	}
}

// TestIconComposerArtworkIsValidSVG keeps the checked-in foreground layer usable by Apple's compiler.
func TestIconComposerArtworkIsValidSVG(t *testing.T) {
	artwork, err := os.ReadFile(filepath.Join("..", "..", "desktop", "build", "appicon-symbol.svg"))
	if err != nil {
		t.Fatalf("read Icon Composer artwork: %v", err)
	}
	var document struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(artwork, &document); err != nil {
		t.Fatalf("parse Icon Composer artwork: %v", err)
	}
	if document.XMLName.Local != "svg" {
		t.Fatalf("Icon Composer artwork root = %q, want svg", document.XMLName.Local)
	}
}

// recordedCommand captures one native packaging invocation.
type recordedCommand struct {
	name      string
	arguments []string
}

// TestRunBuildsModernCatalogAndResignsBundle verifies the complete post-package command boundary.
func TestRunBuildsModernCatalogAndResignsBundle(t *testing.T) {
	desktopDirectory := filepath.Join(t.TempDir(), "desktop")
	binDirectory := filepath.Join(desktopDirectory, "build", "bin")
	appBundle := filepath.Join(binDirectory, "Harbor.app")
	executablePath := filepath.Join(appBundle, "Contents", "MacOS", "harbor-desktop")
	resourcesDirectory := filepath.Join(appBundle, "Contents", "Resources")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resourcesDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AppIcon.icns", "Assets.car", "iconfile.icns"} {
		if err := os.WriteFile(filepath.Join(resourcesDirectory, name), []byte("legacy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(desktopDirectory, "build", "appicon.png"), []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopDirectory, "build", "appicon-symbol.svg"), []byte("symbol"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []recordedCommand
	runner := func(_ context.Context, _ string, name string, arguments ...string) error {
		if name == "/usr/bin/xcrun" && len(arguments) > 0 && arguments[0] == "actool" {
			for _, stale := range []string{"AppIcon.icns", "Assets.car"} {
				if _, err := os.Stat(filepath.Join(resourcesDirectory, stale)); !os.IsNotExist(err) {
					t.Fatalf("stale icon resource %s exists before actool: %v", stale, err)
				}
			}
			for _, generated := range []string{"AppIcon.icns", "Assets.car"} {
				if err := os.WriteFile(filepath.Join(resourcesDirectory, generated), []byte("compiled"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		commands = append(commands, recordedCommand{name: name, arguments: append([]string(nil), arguments...)})
		return nil
	}

	if err := run(context.Background(), binDirectory, []string{executablePath}, runner); err != nil {
		t.Fatalf("run icon packager: %v", err)
	}

	if got, want := len(commands), 5; got != want {
		t.Fatalf("command count = %d, want %d", got, want)
	}
	if got, want := commands[0], (recordedCommand{
		name:      "/usr/bin/xcrun",
		arguments: []string{"--find", "actool"},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("capability command = %#v, want %#v", got, want)
	}
	actool := commands[1]
	if actool.name != "/usr/bin/xcrun" || actool.arguments[0] != "actool" {
		t.Fatalf("catalog command = %#v, want xcrun actool", actool)
	}
	if got := actool.arguments[len(actool.arguments)-1]; !strings.HasSuffix(got, "AppIcon.icon") {
		t.Fatalf("Icon Composer input = %q, want AppIcon.icon", got)
	}
	for _, argument := range actool.arguments {
		if strings.HasSuffix(argument, "Assets.xcassets") {
			t.Fatalf("catalog command retained competing app-icon input: %#v", actool)
		}
	}
	if _, err := os.Stat(filepath.Join(resourcesDirectory, "Assets.car")); err != nil {
		t.Fatalf("compiled asset catalog is unavailable: %v", err)
	}
	for _, name := range []string{"AppIcon.icon", "AppIcon.icns", "iconfile.icns"} {
		if _, err := os.Stat(filepath.Join(resourcesDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("competing icon resource %s exists after modern packaging: %v", name, err)
		}
	}
	partialInfoPath := ""
	for index, argument := range actool.arguments {
		if argument == "--output-partial-info-plist" && index+1 < len(actool.arguments) {
			partialInfoPath = actool.arguments[index+1]
		}
	}
	if partialInfoPath == "" {
		t.Fatalf("catalog command omitted partial information plist: %#v", actool)
	}
	legacySelectorRemoval := commands[2]
	if !reflect.DeepEqual(legacySelectorRemoval, recordedCommand{
		name: "/usr/libexec/PlistBuddy",
		arguments: []string{
			"-c", "Delete :CFBundleIconFile",
			filepath.Join(appBundle, "Contents", "Info.plist"),
		},
	}) {
		t.Fatalf("legacy selector removal command = %#v, want CFBundleIconFile deletion", legacySelectorRemoval)
	}
	iconNameSelector := commands[3]
	if !reflect.DeepEqual(iconNameSelector, recordedCommand{
		name: "/usr/libexec/PlistBuddy",
		arguments: []string{
			"-c", "Set :CFBundleIconName AppIcon",
			filepath.Join(appBundle, "Contents", "Info.plist"),
		},
	}) {
		t.Fatalf("icon name selector command = %#v, want AppIcon selection", iconNameSelector)
	}
	codesign := commands[len(commands)-1]
	if !reflect.DeepEqual(codesign, recordedCommand{
		name:      "/usr/bin/codesign",
		arguments: []string{"--force", "--deep", "--sign", "-", appBundle},
	}) {
		t.Fatalf("codesign command = %#v", codesign)
	}
}

// TestRunKeepsLegacyIconWhenActoolIsUnavailable verifies optional icon tooling cannot block development.
func TestRunKeepsLegacyIconWhenActoolIsUnavailable(t *testing.T) {
	desktopDirectory := filepath.Join(t.TempDir(), "desktop")
	binDirectory := filepath.Join(desktopDirectory, "build", "bin")
	executablePath := filepath.Join(binDirectory, "Harbor.app", "Contents", "MacOS", "harbor-desktop")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopDirectory, "build", "appicon.png"), []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktopDirectory, "build", "appicon-symbol.svg"), []byte("symbol"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	runner := func(_ context.Context, _ string, _ string, _ ...string) error {
		calls++
		return os.ErrNotExist
	}

	if err := run(context.Background(), binDirectory, []string{executablePath}, runner); err != nil {
		t.Fatalf("run without actool: %v", err)
	}
	if calls != 1 {
		t.Fatalf("command count = %d, want capability check only", calls)
	}
}

// TestPackagePathsRejectsBundleOutsideWailsOutput verifies callers cannot redirect packaging.
func TestPackagePathsRejectsBundleOutsideWailsOutput(t *testing.T) {
	desktopDirectory := filepath.Join(t.TempDir(), "desktop")
	binDirectory := filepath.Join(desktopDirectory, "build", "bin")
	executablePath := filepath.Join(t.TempDir(), "Harbor.app", "Contents", "MacOS", "harbor-desktop")

	_, _, _, err := packagePaths(binDirectory, executablePath)
	if err == nil || !strings.Contains(err.Error(), "is not inside desktop/build/bin/*.app") {
		t.Fatalf("packagePaths error = %v, want bundle boundary rejection", err)
	}
}
