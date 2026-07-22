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
	goForjCommandPath        = "github.com/goforj/goforj/cmd/forj"
	goForjModulePath         = "github.com/goforj/goforj"
	compatibleGoForjVersion  = "v0.21.1-0.20260722203521-55a1e5759956"
	compatibleGoForjRevision = "55a1e57599565c9768627db016fc781e3c705f15"
	goForjUpgradeInstruction = "use the canonical GoForj build v0.21.1-0.20260722203521-55a1e5759956 or its exact clean source revision 55a1e57599565c9768627db016fc781e3c705f15, run \"forj render\" in this project, and try again"
	maximumErrorPathBytes    = 256
	maximumErrorReasonBytes  = 512
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

// verifyGoForjExecutable accepts only a canonical GoForj command whose embedded build can prove the managed-address contract.
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

	version := strings.TrimSpace(information.Main.Version)
	if version == compatibleGoForjVersion || cleanCompatibleRevisionProvesCompatibility(information) {
		return nil
	}
	return incompatibleGoForjError(path, fmt.Sprintf("version %q is not the supported managed-session build %q", version, compatibleGoForjVersion))
}

// cleanCompatibleRevisionProvesCompatibility admits the exact clean source revision that defines Harbor's address contract.
func cleanCompatibleRevisionProvesCompatibility(information *debug.BuildInfo) bool {
	revision := ""
	modified := ""
	for _, setting := range information.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = strings.TrimSpace(setting.Value)
		}
	}
	return revision == compatibleGoForjRevision && modified == "false"
}

// incompatibleGoForjError keeps every preflight failure actionable without asking callers to interpret build metadata.
func incompatibleGoForjError(path string, reason string) error {
	location := "available to Harbor"
	if strings.TrimSpace(path) != "" {
		location = fmt.Sprintf("at \"%s\"", boundedVisibleASCII(path, maximumErrorPathBytes))
	}
	return fmt.Errorf("%w: GoForj %s cannot preserve Harbor-managed project addresses (%s); %s", errIncompatibleGoForj, location, boundedVisibleASCII(reason, maximumErrorReasonBytes), goForjUpgradeInstruction)
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
