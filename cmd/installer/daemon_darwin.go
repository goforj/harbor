//go:build darwin

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/platform/daemonservice"
	"golang.org/x/sys/unix"
)

// errDarwinAgentAbsent distinguishes a native missing-service result from an unavailable observation.
var errDarwinAgentAbsent = errors.New("Harbor per-user daemon service is absent")

// darwinAgentDependencies limits uninstall to one OS-resolved user and fixed launchctl verbs.
type darwinAgentDependencies struct {
	lookupUser func(string) (*user.User, error)
	launchctl  func(context.Context, ...string) ([]byte, error)
}

// removeDarwinUserAgent admits the saved and loaded identities before stopping or removing either.
func removeDarwinUserAgent(userID uint32, dependencies darwinAgentDependencies) error {
	identity := strconv.FormatUint(uint64(userID), 10)
	account, err := dependencies.lookupUser(identity)
	if err != nil {
		return fmt.Errorf("resolve Harbor daemon owner: %w", err)
	}
	if account.Uid != identity || !filepath.IsAbs(account.HomeDir) || filepath.Clean(account.HomeDir) != account.HomeDir || strings.ContainsAny(account.HomeDir, "\r\n\t{}") {
		return errors.New("Harbor daemon owner has an invalid native home identity")
	}
	parent, err := openDarwinAgentParent(account.HomeDir, userID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	name := daemonservice.DarwinLabel + ".plist"
	path := filepath.Join(account.HomeDir, "Library", "LaunchAgents", name)
	var saved os.FileInfo
	if parent != nil {
		defer parent.Close()
		saved, err = admitDarwinAgentFile(parent, name, userID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	service := "gui/" + identity + "/" + daemonservice.DarwinLabel
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, serviceErr := dependencies.launchctl(ctx, "print", service)
	switch {
	case serviceErr == nil:
		if saved == nil || !matchesDarwinUserAgent(output, service, path) {
			return errors.New("preserve non-matching loaded Harbor daemon LaunchAgent")
		}
		if _, err := dependencies.launchctl(ctx, "bootout", service); err != nil {
			return fmt.Errorf("unload Harbor daemon LaunchAgent: %w", err)
		}
		if _, err := dependencies.launchctl(ctx, "print", service); !errors.Is(err, errDarwinAgentAbsent) {
			return fmt.Errorf("Harbor daemon LaunchAgent absence was not confirmed: %v", err)
		}
	case errors.Is(serviceErr, errDarwinAgentAbsent):
	default:
		return fmt.Errorf("observe Harbor daemon LaunchAgent: %w", serviceErr)
	}
	if saved == nil {
		return nil
	}
	return removeExactDarwinAgent(parent, name, saved, userID)
}

// openDarwinAgentParent retains no-follow descriptors instead of traversing user-controlled redirects as root.
func openDarwinAgentParent(home string, userID uint32) (*os.File, error) {
	descriptor, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Harbor daemon owner home: %w", err)
	}
	parent := os.NewFile(uintptr(descriptor), home)
	for _, name := range []string{"", "Library", "LaunchAgents"} {
		if name != "" {
			next, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			_ = parent.Close()
			if err != nil {
				return nil, fmt.Errorf("open Harbor LaunchAgent parent %q: %w", name, err)
			}
			parent = os.NewFile(uintptr(next), name)
		}
		information, err := parent.Stat()
		if err == nil {
			status, ok := information.Sys().(*syscall.Stat_t)
			if !ok || status.Uid != userID || information.Mode().Perm()&0o022 != 0 {
				err = errors.New("Harbor LaunchAgent parent has a foreign owner or writable mode")
			}
		}
		if err != nil {
			_ = parent.Close()
			return nil, err
		}
	}
	return parent, nil
}

// admitDarwinAgentFile requires canonical bytes and one direct user-owned inode before deletion authority exists.
func admitDarwinAgentFile(parent *os.File, name string, userID uint32) (os.FileInfo, error) {
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open Harbor daemon LaunchAgent: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	defer file.Close()
	information, err := file.Stat()
	if err != nil {
		return nil, err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || status.Uid != userID || status.Nlink != 1 || information.Mode().Perm() != 0o644 {
		return nil, errors.New("preserve Harbor daemon LaunchAgent with foreign file identity")
	}
	// Both exact serialized definitions fit below this bound, including the longer legacy policy.
	content, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return nil, err
	}
	if !daemonservice.MatchesDarwinDefinition(content) {
		return nil, errors.New("preserve non-matching saved Harbor daemon LaunchAgent")
	}
	return information, nil
}

// removeExactDarwinAgent quarantines the direct name before rechecking its inode, so a replacement is never deleted.
func removeExactDarwinAgent(parent *os.File, name string, saved os.FileInfo, userID uint32) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	retired := "." + name + ".remove-" + hex.EncodeToString(nonce[:])
	if err := unix.RenameatxNp(int(parent.Fd()), name, int(parent.Fd()), retired, unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY); err != nil {
		return fmt.Errorf("retire Harbor daemon LaunchAgent: %w", err)
	}
	observed, err := admitDarwinAgentFile(parent, retired, userID)
	if err == nil && !os.SameFile(saved, observed) {
		err = errors.New("Harbor daemon LaunchAgent changed during uninstall")
	}
	if err != nil {
		restoreErr := unix.RenameatxNp(int(parent.Fd()), retired, int(parent.Fd()), name, unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY)
		return errors.Join(err, restoreErr)
	}
	if err := unix.Unlinkat(int(parent.Fd()), retired, 0); err != nil {
		return fmt.Errorf("remove admitted Harbor daemon LaunchAgent: %w", err)
	}
	return parent.Sync()
}

// matchesDarwinUserAgent binds top-level native facts while rejecting nested or duplicate decoys.
func matchesDarwinUserAgent(output []byte, service string, path string) bool {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != service+" = {" || strings.TrimSpace(lines[len(lines)-1]) != "}" {
		return false
	}
	want := map[string]string{"path": path, "type": "LaunchAgent", "program": daemonservice.DarwinLauncher, "working directory": daemonservice.DarwinWorkingDirectory}
	depth := 0
	argumentBlocks := 0
	arguments := 0
	inArguments := false
	inEnvironment := false
	environments := 0
	environmentKeys := make(map[string]bool)
	for _, line := range lines[1 : len(lines)-1] {
		line = strings.TrimSpace(line)
		if depth == 0 {
			if key, value, found := strings.Cut(line, " = "); found {
				switch strings.ReplaceAll(strings.ToLower(key), " ", "") {
				case "username", "groupname", "rootdirectory", "sockets", "machservices", "inetdcompatibility":
					return false
				}
				if expected, required := want[key]; required {
					if value != expected {
						return false
					}
					delete(want, key)
				} else if key == "path" || key == "type" || key == "program" || key == "working directory" {
					return false
				}
			}
			if line == "arguments = {" {
				argumentBlocks++
				inArguments = true
			}
			if line == "environment = {" {
				environments++
				inEnvironment = true
			}
		} else if inArguments && depth == 1 && line != "}" {
			if line != daemonservice.DarwinLauncher {
				return false
			}
			arguments++
		} else if inEnvironment && depth == 1 && line != "}" {
			key, value, found := strings.Cut(line, " => ")
			if !found || environmentKeys[key] {
				return false
			}
			environmentKeys[key] = true
			switch key {
			case "XPC_SERVICE_NAME":
				if value != daemonservice.DarwinLabel {
					return false
				}
			case "OSLogRateLimit":
				count, err := strconv.ParseUint(value, 10, 32)
				if err != nil || strconv.FormatUint(count, 10) != value {
					return false
				}
			default:
				return false
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			return false
		}
		if depth == 0 {
			inArguments = false
			inEnvironment = false
		}
	}
	return depth == 0 && len(want) == 0 && argumentBlocks == 1 && arguments == 1 && environments <= 1
}

// runAgentLaunchctl bounds native calls and recognizes only the exact missing-service diagnostic.
func runAgentLaunchctl(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "/bin/launchctl", arguments...)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	output, err := command.CombinedOutput()
	if len(output) > 64<<10 {
		return nil, errors.New("Harbor LaunchAgent observation exceeded its bound")
	}
	if err != nil {
		var exit *exec.ExitError
		if len(arguments) == 2 && arguments[0] == "print" && errors.As(err, &exit) && strings.Contains(string(output), "Could not find service") && strings.Contains(string(output), daemonservice.DarwinLabel) {
			return nil, errDarwinAgentAbsent
		}
		return nil, fmt.Errorf("launchctl %s failed: %w: %.4096s", arguments[0], err, output)
	}
	return output, nil
}
