//go:build !darwin || !cgo

package main

// reportFatalDiagnostic is unavailable outside cgo-enabled macOS builds.
func reportFatalDiagnostic(fatalExitDiagnostic) {}
