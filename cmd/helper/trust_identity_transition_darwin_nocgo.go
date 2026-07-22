//go:build darwin && !cgo

package main

import "fmt"

// irreversiblyDropTrustIdentity fails closed when the Darwin atomic setuid boundary is unavailable.
func irreversiblyDropTrustIdentity(string) error {
	return fmt.Errorf("darwin trust identity drop requires cgo")
}
