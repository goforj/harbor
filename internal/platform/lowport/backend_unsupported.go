//go:build !darwin

package lowport

import "errors"

var errUnavailable = errors.New("Darwin low-port launchd relay is unavailable on this platform")

// New fails closed until the Darwin launchd adapter is available.
func New() (*Adapter, error) {
	return nil, operationError(ErrorKindUnavailable, "construct", errUnavailable)
}
