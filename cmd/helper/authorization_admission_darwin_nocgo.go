//go:build darwin && !cgo

package main

import "errors"

// authorizePlatformInvocation fails closed because Authorization Services requires the reviewed cgo bridge.
func authorizePlatformInvocation() error {
	return errors.New("macOS helper authorization requires cgo")
}
