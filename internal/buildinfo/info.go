package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

const developmentVersion = "dev"

// releaseVersion is set by release builds with -ldflags. Module-aware Go
// installs can leave it empty and use the main module version instead.
var releaseVersion string

// Info identifies the Harbor product build independently from IPC protocol
// versions and component roles.
type Info struct {
	// Version is the shared Harbor product release advertised across component boundaries.
	Version string
	// Revision identifies the source commit when Go embedded VCS metadata.
	Revision string
	// Modified records whether the executable was built from a changed checkout.
	Modified bool
}

// Current returns the metadata embedded in the running Harbor executable.
func Current() Info {
	goBuild, ok := debug.ReadBuildInfo()
	return resolveInfo(releaseVersion, goBuild, ok)
}

// resolveInfo keeps release precedence and development fallbacks deterministic
// without requiring tests to mutate process-wide linker state.
func resolveInfo(linkedVersion string, goBuild *debug.BuildInfo, available bool) Info {
	info := Info{Version: normalizedVersion(linkedVersion)}
	if !available || goBuild == nil {
		if info.Version == "" {
			info.Version = developmentVersion
		}
		return info
	}

	if info.Version == "" {
		info.Version = normalizedVersion(goBuild.Main.Version)
	}
	if info.Version == "" {
		info.Version = developmentVersion
	}

	for _, setting := range goBuild.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			info.Modified, _ = strconv.ParseBool(setting.Value)
		}
	}

	return info
}

// normalizedVersion rejects Go's development sentinel because it is not a
// stable product version and is not valid in Harbor's wire-token grammar.
func normalizedVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || version == "(devel)" {
		return ""
	}

	return version
}
