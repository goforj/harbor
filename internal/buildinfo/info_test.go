package buildinfo

import (
	"runtime/debug"
	"testing"
)

// TestResolveInfoPrefersReleaseVersion verifies packaged builds use the one
// release identity shared by the CLI, daemon, and desktop.
func TestResolveInfoPrefersReleaseVersion(t *testing.T) {
	actual := resolveInfo(" v1.4.2 ", &debug.BuildInfo{
		Main: debug.Module{Version: "v1.4.1"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: " abc123 "},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)

	expected := Info{Version: "v1.4.2", Revision: "abc123", Modified: true}
	if actual != expected {
		t.Fatalf("resolveInfo() = %#v, want %#v", actual, expected)
	}
}

// TestResolveInfoUsesModuleVersion verifies module-aware installs retain their
// published version without requiring release linker flags.
func TestResolveInfoUsesModuleVersion(t *testing.T) {
	actual := resolveInfo("", &debug.BuildInfo{
		Main: debug.Module{Version: "v0.3.0"},
	}, true)

	if actual.Version != "v0.3.0" {
		t.Fatalf("resolveInfo() version = %q, want %q", actual.Version, "v0.3.0")
	}
}

// TestResolveInfoUsesDevelopmentFallback verifies source builds advertise a
// valid explicit identity rather than Go's parenthesized sentinel.
func TestResolveInfoUsesDevelopmentFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		goBuild   *debug.BuildInfo
		available bool
	}{
		{name: "unavailable"},
		{name: "nil metadata", available: true},
		{name: "Go development sentinel", goBuild: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, available: true},
		{name: "blank module version", goBuild: &debug.BuildInfo{}, available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := resolveInfo("", test.goBuild, test.available)
			if actual.Version != developmentVersion {
				t.Fatalf("resolveInfo() version = %q, want %q", actual.Version, developmentVersion)
			}
		})
	}
}

// TestResolveInfoIgnoresInvalidModifiedSetting verifies malformed optional VCS
// metadata cannot change the product identity or claim a dirty checkout.
func TestResolveInfoIgnoresInvalidModifiedSetting(t *testing.T) {
	actual := resolveInfo("v1.0.0", &debug.BuildInfo{
		Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "sometimes"}},
	}, true)

	if actual != (Info{Version: "v1.0.0"}) {
		t.Fatalf("resolveInfo() = %#v, want clean release metadata", actual)
	}
}

// TestCurrentAlwaysReturnsVersion verifies every executable has a negotiation-
// safe non-empty build identity even outside a module-aware release.
func TestCurrentAlwaysReturnsVersion(t *testing.T) {
	if actual := Current(); actual.Version == "" || actual.Version == "(devel)" {
		t.Fatalf("Current() version = %q, want a stable non-empty identity", actual.Version)
	}
}
