// Package main seals one fixed Harbor native package payload.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/goforj/harbor/internal/releasebundle"
)

// main reports one bounded package-sealing result.
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "seal Harbor release bundle: %v\n", err)
		os.Exit(1)
	}
}

// run parses only the fixed Darwin development package inputs.
func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("releasebundle", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	payload := flags.String("payload", "", "absolute package payload root")
	version := flags.String("version", "", "semantic product version")
	sequence := flags.String("sequence", "", "positive release sequence")
	revision := flags.String("revision", "", "full lowercase source revision")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *payload == "" || *version == "" || *sequence == "" || *revision == "" {
		return errors.New("--payload, --version, --sequence, and --revision are required")
	}
	parsedSequence, err := strconv.ParseUint(*sequence, 10, 64)
	if err != nil || strconv.FormatUint(parsedSequence, 10) != *sequence {
		return fmt.Errorf("--sequence %q is not canonical unsigned decimal text", *sequence)
	}
	manifest, err := releasebundle.SealDarwinDevelopmentPayload(*payload, releasebundle.DarwinConfig{
		Version:         *version,
		ReleaseSequence: parsedSequence,
		SourceRevision:  *revision,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Sealed Harbor %s development package sequence %d (%s).\n", manifest.Version, manifest.ReleaseSequence, manifest.BundleDigest)
	return err
}
