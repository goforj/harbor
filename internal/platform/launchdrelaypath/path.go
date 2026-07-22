package launchdrelaypath

// Executable returns the fixed launchd relay path selected by the Darwin installer contract.
// An empty result means the platform has no reviewed relay location.
func Executable() string {
	return platformExecutable()
}
