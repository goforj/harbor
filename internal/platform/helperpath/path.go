package helperpath

// Executable returns the fixed privileged helper path selected by the platform installer contract.
// An empty result means the platform has no reviewed executable location yet.
func Executable() string {
	return platformExecutable()
}
