//go:build !darwin && !linux

package main

// openPlatformLowPortHandler keeps low-port effects unavailable outside reviewed native adapters.
func openPlatformLowPortHandler() (closingLowPortHandler, error) {
	return unavailableClosingLowPortHandler{}, nil
}
