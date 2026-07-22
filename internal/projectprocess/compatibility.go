package projectprocess

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	goForjCommandPath       = "github.com/goforj/goforj/cmd/forj"
	goForjModulePath        = "github.com/goforj/goforj"
	maximumErrorPathBytes   = 256
	maximumErrorReasonBytes = 512
)

var errIncompatibleGoForj = errors.New("incompatible GoForj executable")

// ExecutableVerifier verifies the exact executable path Harbor will launch.
type ExecutableVerifier func(string) error

// buildInformationReader isolates standard-library binary inspection from compatibility policy tests.
type buildInformationReader func(string) (*debug.BuildInfo, error)

// newGoForjExecutableVerifier binds the compatibility policy to one side-effect-free build information reader.
func newGoForjExecutableVerifier(reader buildInformationReader) ExecutableVerifier {
	if reader == nil {
		panic("projectprocess GoForj executable verifier requires a build information reader")
	}
	return func(path string) error {
		return verifyGoForjExecutable(path, reader)
	}
}

// productionGoForjExecutableVerifier inspects embedded Go metadata without executing the selected binary.
func productionGoForjExecutableVerifier() ExecutableVerifier {
	return newGoForjExecutableVerifier(buildinfo.ReadFile)
}

// verifyGoForjExecutable accepts only an executable whose embedded build information identifies the canonical GoForj command and module.
func verifyGoForjExecutable(path string, reader buildInformationReader) error {
	information, err := reader(path)
	if err != nil {
		return incompatibleGoForjError(path, fmt.Sprintf("read embedded build information: %v", err))
	}
	if information == nil {
		return incompatibleGoForjError(path, "embedded build information is unavailable")
	}
	if information.Path != goForjCommandPath {
		return incompatibleGoForjError(path, fmt.Sprintf("command path is %q, want %q", information.Path, goForjCommandPath))
	}
	if information.Main.Path != goForjModulePath || information.Main.Replace != nil {
		return incompatibleGoForjError(path, fmt.Sprintf("main module is not the canonical %q module", goForjModulePath))
	}
	return nil
}

// incompatibleGoForjError keeps every preflight failure actionable without asking callers to interpret build metadata.
func incompatibleGoForjError(path string, reason string) error {
	location := "available to Harbor"
	if strings.TrimSpace(path) != "" {
		location = fmt.Sprintf("at \"%s\"", boundedVisibleASCII(path, maximumErrorPathBytes))
	}
	return fmt.Errorf("%w: GoForj executable %s does not identify the canonical command and module (%s)", errIncompatibleGoForj, location, boundedVisibleASCII(reason, maximumErrorReasonBytes))
}

// boundedVisibleASCII prevents executable metadata from injecting invisible control text into durable lifecycle problems.
func boundedVisibleASCII(value string, maximumBytes int) string {
	truncated := len(value) > maximumBytes
	if truncated {
		value = value[:maximumBytes]
	}
	quoted := strconv.QuoteToASCII(value)
	visible := quoted[1 : len(quoted)-1]
	if truncated {
		visible += "..."
	}
	return visible
}
