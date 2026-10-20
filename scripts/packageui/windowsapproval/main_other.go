//go:build !windows

package main

import (
	"fmt"
	"os"
)

// main refuses to substitute a cross-compiled fixture for native Windows authority.
func main() {
	fmt.Fprintln(os.Stderr, "the preauthorized networking fixture requires native Windows")
	os.Exit(1)
}
