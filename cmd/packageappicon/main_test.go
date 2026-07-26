package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestIconDocumentsAreValidJSON keeps the generated Apple asset inputs parseable.
func TestIconDocumentsAreValidJSON(t *testing.T) {
	for name, document := range map[string]string{
		"asset catalog": appIconContents,
		"Icon Composer": iconComposerContents,
	} {
		if !json.Valid([]byte(document)) {
			t.Fatalf("%s document is not valid JSON", name)
		}
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
	if err := os.WriteFile(filepath.Join(desktopDirectory, "build", "appicon.png"), []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}

	var commands []recordedCommand
	runner := func(_ context.Context, _ string, name string, arguments ...string) error {
		commands = append(commands, recordedCommand{name: name, arguments: append([]string(nil), arguments...)})
		return nil
	}

	if err := run(context.Background(), binDirectory, []string{executablePath}, runner); err != nil {
		t.Fatalf("run icon packager: %v", err)
	}

	if got, want := len(commands), len(iconVariants)+3; got != want {
		t.Fatalf("command count = %d, want %d", got, want)
	}
	if got, want := commands[0], (recordedCommand{
		name:      "/usr/bin/xcrun",
		arguments: []string{"--find", "actool"},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("capability command = %#v, want %#v", got, want)
	}
	for index, variant := range iconVariants {
		command := commands[index+1]
		if command.name != "/usr/bin/sips" {
			t.Fatalf("command %d = %q, want sips", index, command.name)
		}
		if got := command.arguments[len(command.arguments)-1]; !strings.HasSuffix(got, variant.name) {
			t.Fatalf("command %d output = %q, want suffix %q", index, got, variant.name)
		}
	}

	actool := commands[len(iconVariants)+1]
	if actool.name != "/usr/bin/xcrun" || actool.arguments[0] != "actool" {
		t.Fatalf("catalog command = %#v, want xcrun actool", actool)
	}
	if got := actool.arguments[len(actool.arguments)-2]; !strings.HasSuffix(got, "AppIcon.icon") {
		t.Fatalf("Icon Composer input = %q, want AppIcon.icon", got)
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
