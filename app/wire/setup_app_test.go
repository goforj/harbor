package wire

import (
	"testing"

	"github.com/alecthomas/kong"
)

// TestInitializeApplicationWiresSetupCommand proves the production CLI exposes setup without invoking native consent during assembly.
func TestInitializeApplicationWiresSetupCommand(t *testing.T) {
	application, err := InitializeApplication()
	if err != nil {
		t.Fatalf("InitializeApplication() error = %v", err)
	}

	parser, err := kong.New(application.RootCmd(), kong.Name("harbor"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	parsed, err := parser.Parse([]string{"setup"})
	if err != nil {
		t.Fatalf("Parse(setup) error = %v", err)
	}
	if parsed.Command() != "setup" {
		t.Fatalf("Parse(setup) command = %q, want setup", parsed.Command())
	}
}
