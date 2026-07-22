//go:build darwin && !cgo

package trust

// New returns a clear capability error when the macOS Security.framework bridge was omitted from the build.
func New() (*Adapter, error) {
	return nil, ErrUnavailable
}
