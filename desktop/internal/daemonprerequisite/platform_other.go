//go:build (!darwin && !linux && !windows) || !release

package daemonprerequisite

// newPlatformEnsurer preserves source-development ownership and unsupported installed platforms.
func newPlatformEnsurer() Ensurer {
	return unavailableEnsurer{}
}
