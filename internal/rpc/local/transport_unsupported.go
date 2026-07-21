//go:build !darwin && !linux && !windows

package local

import (
	"context"
	"errors"
)

// errUnsupportedPlatform keeps unsupported targets explicit instead of silently choosing a weaker transport.
var errUnsupportedPlatform = errors.New("Harbor local IPC is not supported on this platform")

// listen rejects platforms without an authenticated transport implementation.
func listen() (Listener, error) {
	return nil, errUnsupportedPlatform
}

// listenAt rejects endpoint-specific transport on unsupported platforms.
func listenAt(string) (Listener, error) {
	return nil, errUnsupportedPlatform
}

// dial rejects platforms without an authenticated transport implementation.
func dial(context.Context) (Conn, error) {
	return nil, errUnsupportedPlatform
}

// dialAt rejects endpoint-specific transport on unsupported platforms.
func dialAt(context.Context, string) (Conn, error) {
	return nil, errUnsupportedPlatform
}
