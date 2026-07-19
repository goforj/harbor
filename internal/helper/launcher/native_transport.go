package launcher

import (
	"context"
	"io"
)

// NewNativeTransport constructs the reviewed native-consent backend for the current platform.
func NewNativeTransport() Transport {
	return newNativeTransport()
}

// unavailableNativeTransport preserves the no-child result on platforms without a reviewed backend.
type unavailableNativeTransport struct{}

// Invoke fails closed without reading or forwarding the opaque request.
func (unavailableNativeTransport) Invoke(context.Context, io.Reader, io.Writer) TransportResult {
	return TransportResult{State: TransportUnavailable}
}
