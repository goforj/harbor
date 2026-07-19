//go:build windows

package main

import "errors"

// productionEffectiveUID fails closed because launchd relay execution is macOS-only.
func productionEffectiveUID() (uint32, error) {
	return 0, errors.New("launchd relay identity is unavailable on Windows")
}
