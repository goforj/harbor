package helper

const (
	// WindowsInvocationExitPipeConnection identifies failure before the helper connected to its fixed-format pipe.
	WindowsInvocationExitPipeConnection = 10
	// WindowsInvocationExitMessageMode identifies failure to enable message framing on the connected pipe.
	WindowsInvocationExitMessageMode = 11
	// WindowsInvocationExitTokenIdentity identifies failure to read the elevated helper's token user.
	WindowsInvocationExitTokenIdentity = 12
	// WindowsInvocationExitPipeSecurity identifies rejection of the connected pipe's owner or protected DACL.
	WindowsInvocationExitPipeSecurity = 13
	// WindowsInvocationExitServerIdentity identifies rejection of the launcher process behind the pipe.
	WindowsInvocationExitServerIdentity = 14
	// WindowsInvocationExitRetainConnection identifies failure to retain the authenticated pipe handle.
	WindowsInvocationExitRetainConnection = 15
)
