package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestHelperDependencyBoundary proves the production helper graph excludes broad application authority.
func TestHelperDependencyBoundary(t *testing.T) {
	dependencies := runGoList(t, "-deps", "-f", "{{.ImportPath}}", ".")
	forbiddenExact := map[string]struct{}{
		"net":      {},
		"net/http": {},
		"net/rpc":  {},
		"net/smtp": {},
		"os/exec":  {},
		"plugin":   {},
	}
	forbiddenPrefixes := []string{
		"github.com/goforj/harbor/app",
		"github.com/goforj/harbor/desktop",
		"github.com/goforj/harbor/internal/cmd",
		"github.com/goforj/harbor/internal/console",
		"github.com/goforj/harbor/internal/runtime",
	}

	for _, dependency := range strings.Fields(dependencies) {
		if _, forbidden := forbiddenExact[dependency]; forbidden {
			t.Fatalf("helper production dependencies include forbidden package %q", dependency)
		}
		for _, prefix := range forbiddenPrefixes {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Fatalf("helper production dependencies include forbidden package %q", dependency)
			}
		}
	}
}

// TestHelperHasNoThirdPartyRuntimeDependencies keeps the privileged binary's initial graph auditable.
func TestHelperHasNoThirdPartyRuntimeDependencies(t *testing.T) {
	dependencies := runGoList(t, "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
	allowed := map[string]struct{}{
		"github.com/goforj/harbor/cmd/helper":      {},
		"github.com/goforj/harbor/internal/helper": {},
	}
	for _, dependency := range strings.Fields(dependencies) {
		if _, ok := allowed[dependency]; !ok {
			t.Fatalf("helper production dependencies include unapproved third-party package %q", dependency)
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
