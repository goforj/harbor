//go:build !linux && !darwin && !windows

package helperpath

// platformExecutable fails closed until the platform provides a natively resolved installer path.
func platformExecutable() string {
	return ""
}
