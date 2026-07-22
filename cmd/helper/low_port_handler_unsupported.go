//go:build !darwin

package main

// openPlatformLowPortHandler keeps low-port effects unavailable outside the reviewed Darwin adapter.
func openPlatformLowPortHandler() (closingLowPortHandler, error) {
	return unavailableClosingLowPortHandler{}, nil
}
