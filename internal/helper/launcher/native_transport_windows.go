//go:build windows

package launcher

// newNativeTransport fails closed until the Windows consent backend is linked.
func newNativeTransport() Transport {
	return unavailableNativeTransport{}
}
