package trustedhttpsharness

import "testing"

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
