//go:build !darwin || !release

package daemonprerequisite

// newPlatformEnsurer keeps daemon activation exclusive to packaged macOS builds.
func newPlatformEnsurer() Ensurer {
	return unavailableEnsurer{}
}
