package trustedhttpsharness

import (
	"os"
	"testing"
)

// TestFixtureExecutableBaseUsesNativeWindowsName keeps the cross-platform acceptance binary on one direct GoForj executable.
func TestFixtureExecutableBaseUsesNativeWindowsName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "forj"},
		{goos: "linux", want: "forj"},
		{goos: "windows", want: "forj.exe"},
	}
	for _, test := range tests {
		if got := fixtureExecutableBase(test.goos); got != test.want {
			t.Errorf("fixtureExecutableBase(%q) = %q, want %q", test.goos, got, test.want)
		}
	}
}

// TestFixtureExecutableModeUsesNativeExecutionSemantics rejects links and non-executable Unix files without inventing Windows mode bits.
func TestFixtureExecutableModeUsesNativeExecutionSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		goos string
		mode os.FileMode
		want bool
	}{
		{name: "Unix executable", goos: "linux", mode: 0o755, want: true},
		{name: "Unix data file", goos: "linux", mode: 0o644, want: false},
		{name: "Windows regular file", goos: "windows", mode: 0o666, want: true},
		{name: "Windows link", goos: "windows", mode: os.ModeSymlink | 0o666, want: false},
	}
	for _, test := range tests {
		if got := fixtureExecutableModeValid(test.goos, test.mode); got != test.want {
			t.Errorf("%s: fixtureExecutableModeValid(%q, %#o) = %t, want %t", test.name, test.goos, test.mode, got, test.want)
		}
	}
}
