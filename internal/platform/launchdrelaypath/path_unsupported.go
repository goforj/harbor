//go:build !darwin

package launchdrelaypath

// platformExecutable fails closed until Darwin provides the reviewed service contract.
func platformExecutable() string {
	return ""
}
