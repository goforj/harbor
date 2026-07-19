//go:build !linux && !darwin && !windows

package launcher

// newNativeTransport fails closed on platforms without a reviewed privileged-helper contract.
func newNativeTransport() Transport {
	return unavailableNativeTransport{}
}
