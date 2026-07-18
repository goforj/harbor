package harbordapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/cmd"
)

// stubForegroundRunner records the exact lifetime context delegated by the command root.
type stubForegroundRunner struct {
	calls int
	ctx   context.Context
	run   func(context.Context) error
}

// Run captures one daemon invocation and applies the test-specific result.
func (runner *stubForegroundRunner) Run(ctx context.Context) error {
	runner.calls++
	runner.ctx = ctx
	if runner.run == nil {
		return nil
	}

	return runner.run(ctx)
}

// commandTestRoot mirrors the generated anonymous embedding that promotes root flags and methods to Kong.
type commandTestRoot struct {
	Commands
	Probe commandTestProbe `cmd:""`
}

// commandTestProbe gives conflict tests both a direct and nested subcommand path.
type commandTestProbe struct {
	Child commandTestLeaf `cmd:""`
	calls *int
}

// Run records direct probe execution after Kong validation succeeds.
func (probe *commandTestProbe) Run() error {
	(*probe.calls)++
	return nil
}

// commandTestLeaf records nested command execution after Kong validation succeeds.
type commandTestLeaf struct {
	calls *int
}

// Run records nested leaf execution without introducing unrelated command behavior.
func (leaf *commandTestLeaf) Run() error {
	(*leaf.calls)++
	return nil
}

// TestForegroundFlagRunsDaemonAtRoot verifies Kong promotion delegates the bound process context and preserves runner failures.
func TestForegroundFlagRunsDaemonAtRoot(t *testing.T) {
	want := errors.New("foreground stopped")
	runner := &stubForegroundRunner{run: func(context.Context) error { return want }}
	root := newCommandTestRoot(runner)
	parser, err := kong.New(root, kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}

	parseContext, err := parser.Parse([]string{"--foreground"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	ctx := context.WithValue(t.Context(), commandContextKey{}, "foreground")
	parseContext.BindTo(ctx, (*context.Context)(nil))
	if err := parseContext.Run(); !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, want)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if runner.ctx != ctx {
		t.Fatal("runner context did not preserve the bound CLI context")
	}
}

// TestForegroundRunPreservesCancellation verifies command delegation does not replace the CLI lifetime context.
func TestForegroundRunPreservesCancellation(t *testing.T) {
	runner := &stubForegroundRunner{run: func(ctx context.Context) error { return ctx.Err() }}
	commands := newTestCommands(runner)
	commands.Foreground = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := commands.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
}

// TestForegroundRejectsEverySubcommandPath verifies daemon mode cannot accompany direct or nested maintenance work.
func TestForegroundRejectsEverySubcommandPath(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "direct", args: []string{"--foreground", "probe"}, want: `subcommand "probe"`},
		{name: "nested", args: []string{"--foreground", "probe", "child"}, want: `subcommand "probe child"`},
		{name: "flag after command", args: []string{"probe", "--foreground"}, want: `subcommand "probe"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &stubForegroundRunner{}
			root := newCommandTestRoot(runner)
			parser, err := kong.New(root, kong.Name("harbord"))
			if err != nil {
				t.Fatalf("kong.New() error = %v", err)
			}

			_, err = parser.Parse(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse(%q) error = %v, want containing %q", test.args, err, test.want)
			}
			if runner.calls != 0 {
				t.Fatalf("runner calls = %d, want 0", runner.calls)
			}
			if *root.Probe.calls != 0 || *root.Probe.Child.calls != 0 {
				t.Fatalf("subcommand calls = probe %d, child %d; want both 0", *root.Probe.calls, *root.Probe.Child.calls)
			}
		})
	}
}

// TestOrdinarySubcommandNeverRunsDaemon verifies the promoted root Run method is inert outside explicit foreground mode.
func TestOrdinarySubcommandNeverRunsDaemon(t *testing.T) {
	runner := &stubForegroundRunner{}
	root := newCommandTestRoot(runner)
	parser, err := kong.New(root, kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}

	parseContext, err := parser.Parse([]string{"probe"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	parseContext.BindTo(t.Context(), (*context.Context)(nil))
	if err := parseContext.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := *root.Probe.calls; got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
}

// TestForegroundValidationRequiresParseContext verifies malformed direct calls fail instead of bypassing command exclusivity.
func TestForegroundValidationRequiresParseContext(t *testing.T) {
	commands := newTestCommands(&stubForegroundRunner{})
	commands.Foreground = true
	if err := commands.Validate(nil); err == nil {
		t.Fatal("Validate(nil) error = nil, want missing context error")
	}
}

// TestDisabledForegroundRunIsInert verifies parent command execution cannot activate the daemon implicitly.
func TestDisabledForegroundRunIsInert(t *testing.T) {
	runner := &stubForegroundRunner{}
	commands := newTestCommands(runner)
	if err := commands.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
}

// TestBareCommandRootIsInert verifies the root method remains safe behind the application's existing help path.
func TestBareCommandRootIsInert(t *testing.T) {
	runner := &stubForegroundRunner{}
	root := newCommandTestRoot(runner)
	parser, err := kong.New(root, kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}

	parseContext, err := parser.Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil) error = %v", err)
	}
	parseContext.BindTo(t.Context(), (*context.Context)(nil))
	if err := parseContext.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
}

// commandContextKey keeps the context identity assertion independent from string-key collisions.
type commandContextKey struct{}

// newTestCommands creates app commands with inert command leaves and a replaceable foreground boundary.
func newTestCommands(runner foregroundRunner) *Commands {
	return newCommands(&cmd.ResourcesCmd{}, &cmd.AboutCmd{}, &cmd.HelloWorldCmd{}, runner)
}

// newCommandTestRoot creates the same anonymous command embedding used by the generated RootCmd.
func newCommandTestRoot(runner foregroundRunner) *commandTestRoot {
	probeCalls := 0
	childCalls := 0
	return &commandTestRoot{
		Commands: *newTestCommands(runner),
		Probe: commandTestProbe{
			calls: &probeCalls,
			Child: commandTestLeaf{calls: &childCalls},
		},
	}
}
