//go:build darwin && !cgo

package launcher

// newNativeTransport fails closed because Authorization Services requires the reviewed cgo bridge.
func newNativeTransport() Transport {
	return unavailableNativeTransport{}
}
