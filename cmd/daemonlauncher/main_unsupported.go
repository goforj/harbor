//go:build !darwin

package main

import (
	"fmt"
	"os"
	"runtime"
)

// main reports that the stable launcher has no package contract on this platform yet.
func main() {
	_, _ = fmt.Fprintf(os.Stderr, "Harbor daemon launcher is not implemented on %s\n", runtime.GOOS)
	os.Exit(1)
}
