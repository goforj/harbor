package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestBuildArguments preserves installer filenames, production isolation, and the existing Linux dependency baseline.
func TestBuildArguments(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, dev := range []bool{false, true} {
			for _, release := range []bool{false, true} {
				args := buildArguments(platform, "output with spaces", "-X example.version=1.2.3", dev, release)
				if !reflect.DeepEqual(args[:3], []string{"build", "-trimpath", "-buildvcs=false"}) || args[len(args)-2] != "output with spaces" {
					t.Fatal(args)
				}
				tags, flags := args[4], args[6]
				if dev == strings.Contains(tags, "production") || strings.Contains(tags, "release") != (!dev && release) {
					t.Fatal(args)
				}
				if strings.Contains(tags, "gtk3") != (platform == "linux") {
					t.Fatal(args)
				}
				if strings.Contains(flags, "windowsgui") != (platform == "windows" && !dev) {
					t.Fatal(args)
				}
				if !strings.Contains(flags, "-X example.version=1.2.3") {
					t.Fatal(args)
				}
			}
		}
	}
}

// TestCopyFilePreservesPayload checks executable content independently of native build tools.
func TestCopyFilePreservesPayload(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(source, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "binary" {
		t.Fatalf("copy: %q %v", data, err)
	}
	if err := copyFile(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("missing source accepted")
	}
	if err := copyFile(source, filepath.Join(dir, "missing", "target")); err == nil {
		t.Fatal("invalid destination accepted")
	}
}

// TestBuildRequiresDesktopSources prevents a build from accidentally targeting another module or an unprepared frontend.
func TestBuildRequiresDesktopSources(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := build(false, false); err == nil {
		t.Fatal("accepted a directory without go.mod")
	}
	if err := os.WriteFile("go.mod", []byte("module example.invalid/fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := build(false, false); err == nil || !strings.Contains(err.Error(), "after building the frontend") {
		t.Fatalf("missing frontend error = %v", err)
	}
}
