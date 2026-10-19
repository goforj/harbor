//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// darwinAgentFixture retains real user-owned files while replacing only launchctl effects.
func darwinAgentFixture(t *testing.T) (string, uint32, darwinAgentDependencies) {
	t.Helper()
	home := t.TempDir()
	parent := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, daemonservice.DarwinLabel+".plist")
	if err := os.WriteFile(path, daemonservice.DarwinDefinition(), 0o644); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	return path, uid, darwinAgentDependencies{
		lookupUser: func(identity string) (*user.User, error) {
			return &user.User{Uid: identity, HomeDir: home}, nil
		},
		launchctl: func(context.Context, ...string) ([]byte, error) { return nil, errDarwinAgentAbsent },
	}
}

// darwinAgentPrint provides only the static loaded identity, with nested runtime telemetry as a decoy.
func darwinAgentPrint(uid uint32, path string) []byte {
	service := "gui/" + strconv.FormatUint(uint64(uid), 10) + "/" + daemonservice.DarwinLabel
	return []byte(service + " = {\npath = " + path + "\ntype = LaunchAgent\nprogram = " + daemonservice.DarwinLauncher +
		"\nworking directory = " + daemonservice.DarwinWorkingDirectory + "\narguments = {\n" + daemonservice.DarwinLauncher + "\n}\ntelemetry = {\npath = decoy\n}\n}\n")
}

// TestRemoveDarwinUserAgentStopsOnlyTheExactService preserves durable data and neighboring definitions.
func TestRemoveDarwinUserAgentStopsOnlyTheExactService(t *testing.T) {
	path, uid, dependencies := darwinAgentFixture(t)
	sentinel := filepath.Join(filepath.Dir(path), "foreign.plist")
	if err := os.WriteFile(sentinel, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	dependencies.launchctl = func(_ context.Context, arguments ...string) ([]byte, error) {
		calls = append(calls, arguments[0])
		if len(calls) == 1 {
			return darwinAgentPrint(uid, path), nil
		}
		if arguments[0] == "print" {
			return nil, errDarwinAgentAbsent
		}
		return nil, nil
	}
	if err := removeDarwinUserAgent(uid, dependencies); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"print", "bootout", "print"}) {
		t.Fatalf("native calls = %v", calls)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent remains: %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "foreign" {
		t.Fatalf("foreign definition changed: %s %v", content, err)
	}
	if err := removeDarwinUserAgent(uid, darwinAgentDependencies{lookupUser: dependencies.lookupUser, launchctl: func(context.Context, ...string) ([]byte, error) { return nil, errDarwinAgentAbsent }}); err != nil {
		t.Fatalf("absent-agent replay: %v", err)
	}
}

// TestRemoveDarwinUserAgentPreservesForeignFiles covers unsafe direct names before any service mutation.
func TestRemoveDarwinUserAgentPreservesForeignFiles(t *testing.T) {
	for _, shape := range []string{"content", "mode", "symlink", "hardlink", "parent symlink", "owner"} {
		t.Run(shape, func(t *testing.T) {
			path, uid, dependencies := darwinAgentFixture(t)
			switch shape {
			case "content":
				if err := os.WriteFile(path, []byte("foreign"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".foreign"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".foreign", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".foreign"); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				parent := filepath.Dir(path)
				if err := os.Rename(parent, parent+".foreign"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+".foreign", parent); err != nil {
					t.Fatal(err)
				}
			case "owner":
				uid++
			}
			dependencies.launchctl = func(context.Context, ...string) ([]byte, error) {
				t.Fatal("foreign file reached launchctl")
				return nil, nil
			}
			if err := removeDarwinUserAgent(uid, dependencies); err == nil {
				t.Fatal("foreign file admitted")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("foreign artifact removed:", err)
			}
		})
	}
}

// TestRemoveDarwinUserAgentRetainsFilesWhenNativeProofFails distinguishes drift from absence.
func TestRemoveDarwinUserAgentRetainsFilesWhenNativeProofFails(t *testing.T) {
	for _, failure := range []string{"foreign service", "observe", "bootout", "still loaded", "post-observe", "replaced file"} {
		t.Run(failure, func(t *testing.T) {
			path, uid, dependencies := darwinAgentFixture(t)
			calls := 0
			dependencies.launchctl = func(_ context.Context, arguments ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					if failure == "observe" {
						return nil, errors.New("unavailable")
					}
					output := darwinAgentPrint(uid, path)
					if failure == "foreign service" {
						output = []byte(strings.ReplaceAll(string(output), daemonservice.DarwinLauncher, "/foreign"))
					}
					return output, nil
				}
				if arguments[0] == "bootout" {
					if failure == "bootout" {
						return nil, errors.New("denied")
					}
					if failure == "replaced file" {
						if err := os.Rename(path, path+".original"); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("foreign replacement"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					return nil, nil
				}
				if failure == "still loaded" {
					return darwinAgentPrint(uid, path), nil
				}
				if failure == "post-observe" {
					return nil, errors.New("unavailable")
				}
				return nil, errDarwinAgentAbsent
			}
			if err := removeDarwinUserAgent(uid, dependencies); err == nil {
				t.Fatal("missing failure")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("agent removed after failed proof:", err)
			}
			if failure == "foreign service" && calls != 1 {
				t.Fatal("foreign service was mutated")
			}
		})
	}
}

// TestMatchesDarwinUserAgentRejectsDecoysAndDuplicateArguments binds only complete outer facts.
func TestMatchesDarwinUserAgentRejectsDecoysAndDuplicateArguments(t *testing.T) {
	path := "/Users/owner/Library/LaunchAgents/" + daemonservice.DarwinLabel + ".plist"
	service := "gui/501/" + daemonservice.DarwinLabel
	valid := string(darwinAgentPrint(501, path))
	if !matchesDarwinUserAgent([]byte(valid), service, path) {
		t.Fatal("canonical print rejected")
	}
	for _, output := range []string{
		strings.Replace(valid, "path = "+path+"\n", "", 1),
		strings.Replace(valid, "type = LaunchAgent", "type = LaunchAgent\ntype = LaunchAgent", 1),
		strings.Replace(valid, "arguments = {", "arguments = {\n/foreign", 1),
		strings.Replace(valid, "arguments = {\n"+daemonservice.DarwinLauncher+"\n}", "arguments = {\n}\narguments = {\n}", 1),
		strings.Replace(valid, "gui/501/", "gui/502/", 1),
		valid + "another = {\n}\n",
		strings.Replace(valid, "telemetry = {", "environment = {\nAPP_ENV => foreign\n}\ntelemetry = {", 1),
		strings.Replace(valid, "telemetry = {", "sockets = {", 1),
	} {
		if matchesDarwinUserAgent([]byte(output), service, path) {
			t.Fatalf("foreign print admitted: %s", output)
		}
	}
}

// TestRemoveDarwinUserAgentRejectsInvalidNativeAccounts keeps root authority out of caller-selected homes.
func TestRemoveDarwinUserAgentRejectsInvalidNativeAccounts(t *testing.T) {
	for _, home := range []string{"relative", "/Users/owner/../other", "/Users/owner\nforeign"} {
		err := removeDarwinUserAgent(501, darwinAgentDependencies{lookupUser: func(string) (*user.User, error) { return &user.User{Uid: "501", HomeDir: home}, nil }})
		if err == nil {
			t.Fatal("invalid home admitted:", home)
		}
	}
}
