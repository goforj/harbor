//go:build !darwin || !dev

package networkprerequisite

// newPlatformEnsurer requires packaged installations and unsupported development hosts to use their native installer.
func newPlatformEnsurer() Ensurer {
	return unavailableEnsurer{}
}
