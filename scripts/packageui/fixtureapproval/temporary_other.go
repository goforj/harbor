//go:build !windows

package main

import "path/filepath"

// fixtureTemporaryRoot preserves the fixed Unix package-test boundary independently of caller temporary overrides.
func fixtureTemporaryRoot() (string, error) {
	return filepath.EvalSymlinks("/tmp")
}
