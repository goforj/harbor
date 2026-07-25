//go:build !windows

package main

import "io"

// openPlatformInvocation retains the inherited standard streams used by reviewed Unix launchers.
func openPlatformInvocation(_ []string, standardInput io.Reader, standardOutput io.Writer) (invocationStreams, error) {
	return invocationStreams{
		reader: standardInput,
		writer: standardOutput,
		close:  func() error { return nil },
	}, nil
}

// platformInvocationFailureExitCode keeps inherited-stream failures on the ordinary helper failure status.
func platformInvocationFailureExitCode(error) int {
	return 1
}

// platformRuntimeFailureExitCode keeps ordinary helper runtime failures on the protocol failure status.
func platformRuntimeFailureExitCode(error) int {
	return 1
}
