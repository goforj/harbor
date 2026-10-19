//go:build linux && release

package daemonprerequisite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// linuxEnsurerDependencies keeps package admission and the user manager independently testable.
type linuxEnsurerDependencies struct {
	userID  func() int
	inspect func() error
	run     func(context.Context, ...string) ([]byte, error)
}

// linuxEnsurer starts a package-owned unit without enabling linger or creating a child daemon.
type linuxEnsurer struct {
	dependencies linuxEnsurerDependencies
}

// newPlatformEnsurer activates only an installed release, leaving forj dev unchanged.
func newPlatformEnsurer() Ensurer {
	return &linuxEnsurer{dependencies: linuxEnsurerDependencies{
		userID: os.Geteuid, inspect: inspectLinuxPackage, run: runSystemctl,
	}}
}

// Ensure refuses shadowed units and drop-ins before starting Harbor as the current user.
func (ensurer *linuxEnsurer) Ensure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ensurer.dependencies.userID() <= 0 {
		return errors.New("Harbor desktop must run as a non-root Linux user")
	}
	if err := ensurer.dependencies.inspect(); err != nil {
		return err
	}
	if _, err := ensurer.dependencies.run(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("reload Harbor user service: %w", err)
	}
	output, err := ensurer.dependencies.run(ctx, "show", daemonservice.LinuxUnit,
		"--property=FragmentPath", "--property=DropInPaths", "--property=LoadState")
	if err != nil {
		return fmt.Errorf("inspect Harbor user service: %w", err)
	}
	if err := admitLinuxUnit(output); err != nil {
		return err
	}
	if _, err := ensurer.dependencies.run(ctx, "start", daemonservice.LinuxUnit); err != nil {
		return fmt.Errorf("start Harbor user service: %w", err)
	}
	return nil
}

// admitLinuxUnit rejects overrides instead of starting an unrelated Harbor-labeled service.
func admitLinuxUnit(output []byte) error {
	want := map[string]string{
		"FragmentPath": daemonservice.LinuxUnitPath,
		"DropInPaths":  "",
		"LoadState":    "loaded",
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "=")
		expected, known := want[key]
		if !found || !known || value != expected {
			return errors.New("preserve non-matching Harbor systemd user service or drop-in")
		}
		delete(want, key)
	}
	if len(want) != 0 {
		return errors.New("Harbor systemd user service observation is incomplete")
	}
	return nil
}

// inspectLinuxPackage admits direct root-owned unit and executable files at fixed package paths.
func inspectLinuxPackage() error {
	for _, path := range []string{daemonservice.LinuxUnitPath, daemonservice.LinuxDaemonPath} {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("open installed Harbor component %q: %w", path, err)
		}
		information, err := file.Stat()
		if err == nil {
			status, ok := information.Sys().(*syscall.Stat_t)
			if !ok || !information.Mode().IsRegular() || status.Uid != 0 || information.Mode().Perm()&0o022 != 0 || status.Nlink != 1 {
				err = errors.New("component is not a direct root-owned package file")
			} else if path == daemonservice.LinuxDaemonPath && information.Mode().Perm()&0o111 == 0 {
				err = errors.New("installed Harbor daemon is not executable")
			} else if path == daemonservice.LinuxUnitPath {
				content := make([]byte, len(daemonservice.LinuxDefinition())+1)
				n, readErr := file.Read(content)
				if readErr != nil || !bytes.Equal(content[:n], daemonservice.LinuxDefinition()) {
					err = errors.New("installed Harbor systemd unit differs from the package contract")
				}
			}
		}
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return fmt.Errorf("admit installed Harbor component %q: %w", path, err)
		}
	}
	return nil
}

// runSystemctl selects the current user's local bus without honoring service-path overrides.
func runSystemctl(ctx context.Context, arguments ...string) ([]byte, error) {
	uid := strconv.Itoa(os.Geteuid())
	runtimeDirectory := "/run/user/" + uid
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", append([]string{"--user", "--no-pager"}, arguments...)...)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + os.Getenv("HOME"),
		"XDG_RUNTIME_DIR=" + runtimeDirectory,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDirectory + "/bus",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %.4096s", err, output)
	}
	return output, nil
}
