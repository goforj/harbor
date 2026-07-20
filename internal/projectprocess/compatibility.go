package projectprocess

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	goForjCommandPath        = "github.com/goforj/goforj/cmd/forj"
	goForjModulePath         = "github.com/goforj/goforj"
	minimumGoForjVersion     = "v0.20.1-0.20260719152622-bf5f5e65ab64"
	minimumGoForjRevision    = "bf5f5e65ab64ba25cbd2fe53e42014bff1115a81"
	goForjUpgradeInstruction = "upgrade to a bf5f5e65-or-newer build (or v0.20.1+), run \"forj render\" in this project, and try again"
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
	if semver.IsValid(version) {
		if semver.Compare(version, minimumGoForjVersion) >= 0 {
			return nil
		}
		if cleanMinimumRevisionProvesCompatibility(information) {
			return nil
		}
		return incompatibleGoForjError(path, fmt.Sprintf("version %q is older than required version %q", version, minimumGoForjVersion))
	}
	if cleanMinimumRevisionProvesCompatibility(information) && developmentVersionMayUseRevision(version) {
		return nil
	}
	return incompatibleGoForjError(path, fmt.Sprintf("version %q cannot prove managed project address support", version))
}

// cleanMinimumRevisionProvesCompatibility admits the exact clean source revision that defines Harbor's address contract.
func cleanMinimumRevisionProvesCompatibility(information *debug.BuildInfo) bool {
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
	return revision == minimumGoForjRevision && modified == "false"
}

// developmentVersionMayUseRevision limits revision-based admission to Go's unversioned development metadata.
func developmentVersionMayUseRevision(version string) bool {
	return version == "" || version == "(devel)" || version == "devel" || version == "dev"
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
