//go:build !darwin

// Package main reports that Harbor's native installer is unavailable on this platform.
package main

import (
	"fmt"
	"os"
	"runtime"
)

// main fails closed until this platform has its own reviewed installation transaction.
func main() {
	_, _ = fmt.Fprintf(os.Stderr, "Harbor installer is not implemented on %s\n", runtime.GOOS)
	os.Exit(1)
}
