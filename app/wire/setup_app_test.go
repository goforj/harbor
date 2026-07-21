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

// TestInitializeApplicationWiresOpenCommand proves the resource-opening UX is registered without contacting harbord during assembly.
func TestInitializeApplicationWiresOpenCommand(t *testing.T) {
	application, err := InitializeApplication()
	if err != nil {
		t.Fatalf("InitializeApplication() error = %v", err)
	}

	parser, err := kong.New(application.RootCmd(), kong.Name("harbor"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	parsed, err := parser.Parse([]string{"open", "project-orders"})
	if err != nil {
		t.Fatalf("Parse(open) error = %v", err)
	}
	if parsed.Command() != "open <project>" {
		t.Fatalf("Parse(open) command = %q, want open <project>", parsed.Command())
	}
}

// TestInitializeApplicationWiresLogsCommand proves the bounded log surface is registered without contacting harbord during assembly.
func TestInitializeApplicationWiresLogsCommand(t *testing.T) {
	application, err := InitializeApplication()
	if err != nil {
		t.Fatalf("InitializeApplication() error = %v", err)
	}

	parser, err := kong.New(application.RootCmd(), kong.Name("harbor"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	parsed, err := parser.Parse([]string{"logs", "project-orders", "--service", "mysql", "--follow"})
	if err != nil {
		t.Fatalf("Parse(logs) error = %v", err)
	}
	if parsed.Command() != "logs <project>" {
		t.Fatalf("Parse(logs) command = %q, want logs <project>", parsed.Command())
	}
}
