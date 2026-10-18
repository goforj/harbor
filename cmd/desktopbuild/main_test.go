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
				if platform == "darwin" && !strings.Contains(flags, "-extldflags=-mmacosx-version-min=") {
					t.Fatal("missing explicit native linker target", args)
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

// TestDevelopmentArtifactsStayDarwinOnly prevents unsupported bootstrap commands from entering other native builds.
func TestDevelopmentArtifactsStayDarwinOnly(t *testing.T) {
	for _, test := range []struct {
		platform           string
		dev, release, want bool
	}{
		{"darwin", true, false, true},
		{"darwin", false, false, true},
		{"darwin", false, true, false},
		{"darwin", true, true, true},
		{"linux", true, false, false},
		{"linux", false, false, false},
		{"windows", true, false, false},
		{"windows", false, false, false},
	} {
		if got := needsDevelopmentArtifacts(test.platform, test.dev, test.release); got != test.want {
			t.Fatalf("%+v: got %t", test, got)
		}
	}
}

// TestDarwinDeploymentTarget follows the supported OS minimum of the selected Go toolchain.
func TestDarwinDeploymentTarget(t *testing.T) {
	for _, test := range []struct{ goVersion, target string }{
		{"go1.26.1", "12.0"}, {"go1.27.0", "13.0"}, {"go1.27rc1", "13.0"},
	} {
		if got := darwinDeploymentTarget(test.goVersion); got != test.target {
			t.Fatalf("%s: got %s, want %s", test.goVersion, got, test.target)
		}
	}
}

// TestDarwinBuildEnvironment preserves user flags and changes cgo cache inputs without mutating the caller's environment.
func TestDarwinBuildEnvironment(t *testing.T) {
	for _, custom := range []bool{false, true} {
		base := []string{"PATH=/tools", "MACOSX_DEPLOYMENT_TARGET=11.0"}
		if custom {
			base = append(base, "CGO_CFLAGS=-O0 -DLOCAL_TEST", "CGO_CXXFLAGS=-O1", "CGO_LDFLAGS=-framework Security")
		}
		original := append([]string(nil), base...)
		got := darwinBuildEnvironment(base, "go1.27.0")
		if !reflect.DeepEqual(base, original) {
			t.Fatal("modified caller environment")
		}
		values := map[string]string{}
		for _, entry := range got {
			key, value, _ := strings.Cut(entry, "=")
			if _, exists := values[key]; exists {
				t.Fatalf("duplicate variable: %s", key)
			}
			values[key] = value
		}
		if values["PATH"] != "/tools" || values["MACOSX_DEPLOYMENT_TARGET"] != "13.0" {
			t.Fatal(values)
		}
		for _, key := range []string{"CGO_CFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS"} {
			if !strings.HasSuffix(values[key], "-mmacosx-version-min=13.0") {
				t.Fatal(values)
			}
			if !custom && !strings.HasPrefix(values[key], "-O2 -g ") {
				t.Fatal(values)
			}
		}
		if custom && (!strings.Contains(values["CGO_CFLAGS"], "-DLOCAL_TEST") || !strings.Contains(values["CGO_CXXFLAGS"], "-O1") || !strings.Contains(values["CGO_LDFLAGS"], "-framework Security")) {
			t.Fatal(values)
		}
	}
}
