//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDevBootstrapDependencyBoundary keeps the privileged development graph bespoke, local, and auditable.
func TestDevBootstrapDependencyBoundary(t *testing.T) {
	forbiddenExact := map[string]struct{}{
		"net":      {},
		"net/http": {},
		"net/rpc":  {},
		"os/exec":  {},
		"plugin":   {},
	}
	forbiddenPrefixes := []string{
		"github.com/docker",
		"github.com/goforj/env",
		"github.com/goforj/harbor/app",
		"github.com/goforj/harbor/desktop",
		"github.com/goforj/harbor/internal/cmd",
		"github.com/goforj/harbor/internal/console",
		"github.com/goforj/harbor/internal/daemon",
		"github.com/goforj/harbor/internal/goforj",
		"github.com/goforj/harbor/internal/runtime",
		"github.com/joho/godotenv",
	}

	for _, target := range []string{"darwin", "linux"} {
		dependencies := runGoListForDevBootstrapTarget(t, target, "-deps", "-f", "{{.ImportPath}}", ".")
		for _, dependency := range strings.Fields(dependencies) {
			if _, forbidden := forbiddenExact[dependency]; forbidden {
				t.Fatalf("development bootstrap %s dependencies include forbidden package %q", target, dependency)
			}
			for _, prefix := range forbiddenPrefixes {
				if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
					t.Fatalf("development bootstrap %s dependencies include forbidden package %q", target, dependency)
				}
			}
		}
	}
}

// TestDevBootstrapHasOnlyReviewedRuntimeDependencies pins every non-standard package in both supported builds.
func TestDevBootstrapHasOnlyReviewedRuntimeDependencies(t *testing.T) {
	allowed := map[string]struct{}{
		"github.com/goforj/harbor/cmd/devbootstrap":               {},
		"github.com/goforj/harbor/internal/devbootstrap":          {},
		"github.com/goforj/harbor/internal/platform/helperpath":   {},
		"github.com/goforj/harbor/internal/platform/machinepaths": {},
		"golang.org/x/sys/unix":                                   {},
	}
	for _, target := range []string{"darwin", "linux"} {
		dependencies := runGoListForDevBootstrapTarget(t, target, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
		for _, dependency := range strings.Fields(dependencies) {
			if _, reviewed := allowed[dependency]; !reviewed {
				t.Fatalf("development bootstrap %s dependencies include unreviewed package %q", target, dependency)
			}
		}
	}
}

// runGoListForDevBootstrapTarget returns one isolated target graph or fails with the compiler diagnostics.
func runGoListForDevBootstrapTarget(t *testing.T, target string, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", append([]string{"list"}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"CGO_ENABLED=0",
		"GOARCH=amd64",
		"GOOS="+target,
		"GOCACHE=/tmp/gocache",
		"GOMODCACHE=/tmp/gomodcache",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=%s go list %s: %v\n%s", target, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
