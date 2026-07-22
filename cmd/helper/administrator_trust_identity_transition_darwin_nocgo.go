//go:build darwin && !cgo

package main

import "fmt"

// irreversiblyEnterAdministratorTrustIdentity fails closed when the Darwin atomic setuid boundary is unavailable.
func irreversiblyEnterAdministratorTrustIdentity(string) error {
	return fmt.Errorf("darwin administrator trust identity transition requires cgo")
}
