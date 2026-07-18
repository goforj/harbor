package konghelp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// guidedRootFlagFixture gives root help visible, short, and hidden flags without Harbor-specific formatter policy.
type guidedRootFlagFixture struct {
	Foreground bool `help:"Run the Harbor daemon in the foreground"`
	Quiet      bool `short:"q" help:"Suppress routine output"`
	Internal   bool `hidden:"" help:"Internal implementation detail"`
}

// TestGuidedRootHelpRendersVisibleFlags verifies product root flags remain discoverable beside universal help.
func TestGuidedRootHelpRendersVisibleFlags(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	parser, err := kong.New(&guidedRootFlagFixture{}, kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	parseContext, err := kong.Trace(parser, nil)
	if err != nil {
		t.Fatalf("kong.Trace() error = %v", err)
	}

	var output bytes.Buffer
	renderGuidedFormatter(&output, kong.HelpOptions{}, parseContext)
	help := output.String()
	for _, want := range []string{
		"--foreground",
		"Run the Harbor daemon in the foreground",
		"-q, --quiet",
		"Suppress routine output",
		"-h, --help",
		"Show context-sensitive help.",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("guided root help missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "--internal") || strings.Contains(help, "Internal implementation detail") {
		t.Fatalf("guided root help exposed a hidden flag:\n%s", help)
	}
	if count := strings.Count(help, "-h, --help"); count != 1 {
		t.Fatalf("guided root help contains the help flag row %d times, want 1:\n%s", count, help)
	}
}

// TestGuidedFlagRowsAddsHelpWithoutVisibleFlags verifies hidden-only command policy still exposes one useful flag.
func TestGuidedFlagRowsAddsHelpWithoutVisibleFlags(t *testing.T) {
	parser, err := kong.New(&struct {
		Internal bool `hidden:""`
	}{})
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}

	rows := guidedFlagRows(parser.Model.Node)
	if len(rows) != 1 || rows[0].name != "-h, --help" || rows[0].help == "" {
		t.Fatalf("guidedFlagRows() = %#v, want universal help only", rows)
	}
}
