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
