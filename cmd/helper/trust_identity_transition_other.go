//go:build !darwin

package main

import "fmt"

// irreversiblyDropTrustIdentity fails closed outside the reviewed Darwin trust identity boundary.
func irreversiblyDropTrustIdentity(string) error {
	return fmt.Errorf("trust identity drop is unsupported on this platform")
}
