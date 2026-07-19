package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	windowsInvocationPipePrefix      = `\\.\pipe\goforj-harbor-helper-`
	windowsInvocationPipeRandomBytes = 32
)

// windowsInvocationPipeOpener opens and authenticates one launcher-owned routing endpoint.
type windowsInvocationPipeOpener func(string) (io.ReadWriteCloser, error)

// openWindowsInvocation accepts exactly one fixed-format pipe route and no request authority in arguments.
func openWindowsInvocation(arguments []string, openPipe windowsInvocationPipeOpener) (invocationStreams, error) {
	if len(arguments) != 2 {
		return invocationStreams{}, fmt.Errorf("Windows helper invocation has %d arguments, want executable and one pipe route", len(arguments))
	}
	pipeName := arguments[1]
	if !validWindowsInvocationPipeName(pipeName) {
		return invocationStreams{}, errors.New("Windows helper invocation pipe route is invalid")
	}
	connection, err := openPipe(pipeName)
	if err != nil {
		return invocationStreams{}, fmt.Errorf("open Windows helper invocation pipe: %w", err)
	}
	return invocationStreams{
		reader: connection,
		writer: connection,
		close:  connection.Close,
	}, nil
}

// validWindowsInvocationPipeName accepts only the launcher's local 256-bit random route format.
func validWindowsInvocationPipeName(name string) bool {
	if !strings.HasPrefix(name, windowsInvocationPipePrefix) {
		return false
	}
	random := strings.TrimPrefix(name, windowsInvocationPipePrefix)
	if len(random) != windowsInvocationPipeRandomBytes*2 || strings.ToLower(random) != random {
		return false
	}
	decoded, err := hex.DecodeString(random)
	return err == nil && len(decoded) == windowsInvocationPipeRandomBytes
}
