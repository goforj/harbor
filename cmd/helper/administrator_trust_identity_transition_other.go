//go:build !darwin

package main

import "fmt"

// irreversiblyEnterAdministratorTrustIdentity fails closed outside the reviewed Darwin administrator trust identity boundary.
func irreversiblyEnterAdministratorTrustIdentity(string) error {
	return fmt.Errorf("administrator trust identity transition is unsupported on this platform")
}
