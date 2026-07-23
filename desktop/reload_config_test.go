package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const harborModulePath = "github.com/goforj/harbor/"

// TestDesktopReloadsDirectApprovalPackages keeps development reloads aligned with desktop-native approvals.
func TestDesktopReloadsDirectApprovalPackages(t *testing.T) {
	t.Parallel()

	watched := desktopReloadDirectories(t)
	for packagePath := range desktopApprovalPackages(t) {
		directory := "../" + strings.TrimPrefix(packagePath, harborModulePath)
		if _, ok := watched[directory]; !ok {
			t.Errorf("desktop approval package %q is not included in Wails -reloaddirs", packagePath)
		}
	}
}

// desktopReloadDirectories reads the outer GoForj command because that process decides whether Wails rebuilds the native backend.
func desktopReloadDirectories(t *testing.T) map[string]struct{} {
	t.Helper()

	configuration, err := os.ReadFile("../.goforj.yml")
	if err != nil {
		t.Fatalf("read development configuration: %v", err)
	}

	for _, line := range strings.Split(string(configuration), "\n") {
		command := strings.TrimPrefix(strings.TrimSpace(line), "exec: ")
		if !strings.HasPrefix(command, "wails dev ") {
			continue
		}

		arguments := strings.Fields(command)
		for index, argument := range arguments {
			if argument != "-reloaddirs" || index+1 == len(arguments) {
				continue
			}

			directories := make(map[string]struct{})
			for _, directory := range strings.Split(arguments[index+1], ",") {
				directories[directory] = struct{}{}
			}
			return directories
		}
	}

	t.Fatal("Harbor Desktop Wails reload directories are not configured")
	return nil
}

// desktopApprovalPackages derives the set from production imports so a new approval cannot silently split the webview from its native backend.
func desktopApprovalPackages(t *testing.T) map[string]struct{} {
	t.Helper()

	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse desktop application imports: %v", err)
	}

	approvalPackages := make(map[string]struct{})
	for _, file := range packages["main"].Files {
		for _, imported := range file.Imports {
			packagePath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote desktop import %q: %v", imported.Path.Value, err)
			}
			if strings.HasPrefix(packagePath, harborModulePath+"internal/") && strings.HasSuffix(packagePath, "approval") {
				approvalPackages[packagePath] = struct{}{}
			}
		}
	}

	return approvalPackages
}
