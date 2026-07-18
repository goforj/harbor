package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestHelperDependencyBoundary proves the production helper graph excludes broad application authority.
func TestHelperDependencyBoundary(t *testing.T) {
	forbiddenExact := map[string]struct{}{
		"net":      {},
		"net/http": {},
		"net/rpc":  {},
		"net/smtp": {},
		"os/exec":  {},
		"plugin":   {},
	}
	forbiddenPrefixes := []string{
		"github.com/docker",
		"github.com/goforj/harbor/app",
		"github.com/goforj/harbor/desktop",
		"github.com/goforj/harbor/internal/cmd",
		"github.com/goforj/harbor/internal/console",
		"github.com/goforj/harbor/internal/daemon",
		"github.com/goforj/harbor/internal/goforj",
		"github.com/goforj/harbor/internal/harbordruntime",
		"github.com/goforj/harbor/internal/projectdiscovery",
		"github.com/goforj/harbor/internal/runtime",
	}

	for _, target := range helperTargets() {
		dependencies := runGoListForTarget(t, target, "-deps", "-f", "{{.ImportPath}}", ".")
		for _, dependency := range strings.Fields(dependencies) {
			if _, forbidden := forbiddenExact[dependency]; forbidden && !unavoidableStandardDependency(target, dependency) {
				t.Fatalf("helper %s production dependencies include forbidden package %q", target, dependency)
			}
			for _, prefix := range forbiddenPrefixes {
				if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
					t.Fatalf("helper %s production dependencies include forbidden package %q", target, dependency)
				}
			}
		}
		assertNoUnreviewedNetworkImports(t, target)
	}
}

// TestHelperHasOnlyReviewedRuntimeDependencies keeps every platform's privileged graph explicitly auditable.
func TestHelperHasOnlyReviewedRuntimeDependencies(t *testing.T) {
	for _, target := range helperTargets() {
		allowed := reviewedRuntimeDependencies(target)
		dependencies := runGoListForTarget(t, target, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
		for _, dependency := range strings.Fields(dependencies) {
			if _, ok := allowed[dependency]; !ok {
				t.Fatalf("helper %s production dependencies include unapproved package %q", target, dependency)
			}
		}
	}
}

// TestHelperProtocolPackageHasNoHostExecutionImports protects the portable validator from path and process authority.
func TestHelperProtocolPackageHasNoHostExecutionImports(t *testing.T) {
	imports := runGoList(
		t,
		"-f",
		"{{range .Imports}}{{println .}}{{end}}",
		"github.com/goforj/harbor/internal/helper",
	)
	forbidden := map[string]struct{}{
		"net":           {},
		"net/http":      {},
		"os":            {},
		"os/exec":       {},
		"path":          {},
		"path/filepath": {},
	}
	for _, imported := range strings.Fields(imports) {
		if _, found := forbidden[imported]; found {
			t.Fatalf("helper protocol imports forbidden authority package %q", imported)
		}
	}
}

// runGoList returns one Go package query or fails the dependency-boundary test with its diagnostics.
func runGoList(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", append([]string{"list"}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

// runGoListForTarget returns one platform-specific package query or fails with its diagnostics.
func runGoListForTarget(t *testing.T, target string, arguments ...string) string {
	t.Helper()
	command := exec.Command("go", append([]string{"list"}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"CGO_ENABLED=0",
		"GOARCH=amd64",
		"GOOS="+target,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=%s go list %s: %v\n%s", target, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

// helperTargets returns every operating system supported by the privileged helper boundary.
func helperTargets() []string {
	return []string{"darwin", "linux", "windows"}
}

// reviewedRuntimeDependencies returns the exact audited graph available to one platform build.
func reviewedRuntimeDependencies(target string) map[string]struct{} {
	allowed := map[string]struct{}{
		"github.com/goforj/harbor/cmd/helper":                      {},
		"github.com/goforj/harbor/internal/helper":                 {},
		"github.com/goforj/harbor/internal/helper/loopbackhandler": {},
		"github.com/goforj/harbor/internal/helper/replaystore":     {},
		"github.com/goforj/harbor/internal/helper/ticketauth":      {},
		"github.com/goforj/harbor/internal/helper/ticketredeemer":  {},
		"github.com/goforj/harbor/internal/host/ownership":         {},
		"github.com/goforj/harbor/internal/platform/loopback":      {},
		"github.com/goforj/harbor/internal/platform/machinepaths":  {},
	}
	platformDependencies := map[string][]string{
		"darwin":  {"golang.org/x/net/route", "golang.org/x/sys/unix"},
		"linux":   {"github.com/goforj/harbor/internal/platform/linuxnetlink", "golang.org/x/sys/unix"},
		"windows": {"golang.org/x/sys/windows"},
	}
	for _, dependency := range platformDependencies[target] {
		allowed[dependency] = struct{}{}
	}
	return allowed
}

// unavoidableStandardDependency permits only the network type dependency compiled into the reviewed Windows syscall package.
func unavoidableStandardDependency(target string, dependency string) bool {
	return target == "windows" && dependency == "net"
}

// assertNoUnreviewedNetworkImports ensures the Windows syscall exception cannot hide Harbor-owned network authority.
func assertNoUnreviewedNetworkImports(t *testing.T, target string) {
	t.Helper()
	packages := runGoListForTarget(t, target, "-deps", "-f", "{{.ImportPath}}|{{join .Imports \",\"}}", ".")
	for _, line := range strings.Split(strings.TrimSpace(packages), "\n") {
		importer, imports, found := strings.Cut(line, "|")
		if !found {
			t.Fatalf("helper %s dependency record %q is malformed", target, line)
		}
		for _, imported := range strings.Split(imports, ",") {
			if imported != "net" {
				continue
			}
			if target == "windows" && importer == "golang.org/x/sys/windows" {
				continue
			}
			t.Fatalf("helper %s package %q imports forbidden package %q", target, importer, imported)
		}
	}
}
