//go:build !darwin && !linux && !windows

package local

// endpointReference keeps unsupported platforms explicit instead of inventing an unauthenticated endpoint.
func endpointReference() (string, error) {
	return "", errUnsupportedPlatform
}
