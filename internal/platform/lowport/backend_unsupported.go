//go:build !darwin && !linux

package lowport

import "errors"

var errUnavailable = errors.New("native low-port adapter is unavailable on this platform")

// New fails closed until a reviewed native low-port adapter is available.
func New() (*Adapter, error) {
	return nil, operationError(ErrorKindUnavailable, "construct", errUnavailable)
}
