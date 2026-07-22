//go:build !darwin

package trust

// New returns a clear capability error until the platform's native trust backend is reviewed.
func New() (*Adapter, error) {
	return nil, ErrUnavailable
}
