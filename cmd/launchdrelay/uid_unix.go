//go:build darwin || linux

package main

import "os"

// productionEffectiveUID returns the process identity selected by the system launchd job.
func productionEffectiveUID() (uint32, error) {
	return uint32(os.Geteuid()), nil
}
