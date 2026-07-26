//go:build darwin && release

package daemonprerequisite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestDarwinEnsurerCreatesAndStartsTheExactLaunchAgent proves first launch activates only Harbor's fixed daemon service.
func TestDarwinEnsurerCreatesAndStartsTheExactLaunchAgent(t *testing.T) {
	home := t.TempDir()
	var calls [][]string
	var writtenPath string
	var writtenContent []byte
	ensurer := newDarwinEnsurer(darwinEnsurerDependencies{
		home:   func() (string, error) { return home, nil },
		userID: func() int { return 501 },
		run: func(_ context.Context, arguments ...string) error {
			calls = append(calls, append([]string(nil), arguments...))
			if arguments[0] == "print" {
				return errors.New("not loaded")
			}
			return nil
		},
		readFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		writeAgent: func(path string, content []byte) error {
			writtenPath = path
			writtenContent = append([]byte(nil), content...)
			return nil
		},
		inspect: func(string) error { return nil },
	})

	if err := ensurer.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPath := filepath.Join(home, "Library", "LaunchAgents", "com.goforj.harbor.daemon.plist")
	if writtenPath != wantPath || len(writtenContent) == 0 {
		t.Fatalf("written LaunchAgent = %q %q", writtenPath, writtenContent)
	}
	wantCalls := [][]string{
		{"print", "gui/501/com.goforj.harbor.daemon"},
		{"bootstrap", "gui/501", wantPath},
		{"kickstart", "gui/501/com.goforj.harbor.daemon"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("launchctl calls = %#v, want %#v", calls, wantCalls)
	}
}

// TestDarwinEnsurerPreservesAMismatchedLaunchAgent prevents release activation from replacing ambiguous user state.
func TestDarwinEnsurerPreservesAMismatchedLaunchAgent(t *testing.T) {
	ensurer := newDarwinEnsurer(darwinEnsurerDependencies{
		home:       func() (string, error) { return t.TempDir(), nil },
		userID:     func() int { return 501 },
		run:        func(context.Context, ...string) error { return nil },
		readFile:   func(string) ([]byte, error) { return []byte("foreign"), nil },
		writeAgent: func(string, []byte) error { t.Fatal("unexpected write"); return nil },
		inspect:    func(string) error { return nil },
	})

	if err := ensurer.Ensure(t.Context()); err == nil {
		t.Fatal("Ensure() error = nil")
	}
}
