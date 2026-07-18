package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/goforj/harbor/desktop/internal/wirefixture"
)

// main writes the frontend fixture from the same Go types used by the Wails boundary.
func main() {
	output := flag.String("output", "", "path to the generated frontend fixture")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "output path is required")
		os.Exit(2)
	}

	payload, err := wirefixture.TypeScript()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, payload, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write fixture: %v\n", err)
		os.Exit(1)
	}
}
