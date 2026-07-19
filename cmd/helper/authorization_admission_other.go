//go:build !darwin

package main

// authorizePlatformInvocation keeps non-Darwin helper admission on its existing installer and ticket boundaries.
func authorizePlatformInvocation() error {
	return nil
}
