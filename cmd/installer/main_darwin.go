//go:build darwin

// Package main provides Harbor's fixed-path native installation bootstrap.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/goforj/harbor/internal/devbootstrap"
	"github.com/goforj/harbor/internal/platform/installpaths"
	"github.com/goforj/harbor/internal/releasebundle"
)

// main applies one package-selected installation operation without ambient path authority.
func main() {
	os.Clearenv()
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor installer: %v\n", err)
		os.Exit(1)
	}
}

// run accepts only the fixed package lifecycle operations and the console-user identity they require.
func run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("install or uninstall operation is required")
	}
	switch arguments[0] {
	case "install":
		return runInstall(arguments[1:], output)
	case "uninstall":
		return runUninstall(arguments[1:], output)
	default:
		return fmt.Errorf("unsupported Harbor installation operation %q", arguments[0])
	}
}

// runInstall parses the one identity that may own Harbor's per-user privileged ticket ingress.
func runInstall(arguments []string, output io.Writer) error {
	userID, groupID, err := parseIdentityFlags("harbor-installer install", arguments)
	if err != nil {
		return err
	}
	currentRelease, err := installpaths.CurrentRelease()
	if err != nil {
		return err
	}
	if err := devbootstrap.Bootstrap(devbootstrap.Config{
		HelperSource:       filepath.Join(currentRelease, "libexec", "com.goforj.harbor.helper"),
		LaunchdRelaySource: filepath.Join(currentRelease, "libexec", "com.goforj.harbor.launchdrelay"),
		UserID:             userID,
		GroupID:            groupID,
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Harbor installation bootstrap complete.")
	return err
}

// runUninstall requires the activated user's exact identity before admitting and removing package-owned state.
func runUninstall(arguments []string, output io.Writer) error {
	userID, groupID, err := parseIdentityFlags("harbor-installer uninstall", arguments)
	if err != nil {
		return err
	}
	if err := uninstallDarwin(userID, groupID, productionUninstallDependencies()); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Harbor uninstall complete.")
	return err
}

// parseIdentityFlags rejects ambient or noncanonical user selection for both package lifecycle operations.
func parseIdentityFlags(name string, arguments []string) (uint32, uint32, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	userID := flags.String("user-id", "", "non-root Harbor user ID")
	groupID := flags.String("group-id", "", "Harbor user group ID")
	if err := flags.Parse(arguments); err != nil {
		return 0, 0, err
	}
	if flags.NArg() != 0 {
		return 0, 0, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	parsedUserID, err := parseID("user-id", *userID, false)
	if err != nil {
		return 0, 0, err
	}
	parsedGroupID, err := parseID("group-id", *groupID, true)
	if err != nil {
		return 0, 0, err
	}
	return parsedUserID, parsedGroupID, nil
}

// parseID rejects missing, noncanonical, or reserved native identities before privileged work begins.
func parseID(name string, value string, allowRoot bool) (uint32, error) {
	if value == "" {
		return 0, fmt.Errorf("--%s is required", name)
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("--%s %q is not a canonical unsigned decimal identity", name, value)
	}
	if parsed == math.MaxUint32 {
		return 0, fmt.Errorf("--%s %q is reserved", name, value)
	}
	if parsed == 0 && !allowRoot {
		return 0, fmt.Errorf("--%s must identify a non-root user", name)
	}
	return uint32(parsed), nil
}

// darwinUninstallPaths freezes every package and host-integration path the development uninstaller may inspect or remove.
type darwinUninstallPaths struct {
	application       string
	machineRoot       string
	helper            string
	relay             string
	cli               string
	productParent     string
	ownership         string
	hostProjection    string
	resolver          string
	launchDaemon      string
	pendingTickets    string
	claimedTickets    string
	replayTombstones  string
	currentSelection  string
	packageIdentifier string
}

// darwinUninstallDependencies separates native process and filesystem effects from the fail-closed uninstall transaction.
type darwinUninstallDependencies struct {
	effectiveUID       func() int
	verifyInstallation func() (releasebundle.Manifest, error)
	requireReceipt     func(string) error
	forgetReceipt      func(string) error
	requireLabelAbsent func(string) error
	remove             func(string) error
	removeAll          func(string) error
	removeEmpty        func(string) error
}

// productionUninstallDependencies binds uninstall authority only to fixed native tools and operating-system calls.
func productionUninstallDependencies() darwinUninstallDependencies {
	return darwinUninstallDependencies{
		effectiveUID:       os.Geteuid,
		verifyInstallation: releasebundle.VerifyInstalledDarwinDevelopmentRelease,
		requireReceipt:     requirePackageReceipt,
		forgetReceipt:      forgetPackageReceipt,
		requireLabelAbsent: requireLaunchdLabelAbsent,
		remove:             os.Remove,
		removeAll:          os.RemoveAll,
		removeEmpty:        os.Remove,
	}
}

// productionUninstallPaths returns the only filesystem and package identities this development uninstaller recognizes.
func productionUninstallPaths() darwinUninstallPaths {
	machineRoot := "/Library/Application Support/GoForj/Harbor"
	return darwinUninstallPaths{
		application:       "/Applications/Harbor.app",
		machineRoot:       machineRoot,
		helper:            "/Library/PrivilegedHelperTools/com.goforj.harbor.helper",
		relay:             "/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay",
		cli:               "/usr/local/bin/harbor",
		productParent:     "/Library/Application Support/GoForj",
		ownership:         filepath.Join(machineRoot, "Privileged", "state", "ownership.json"),
		hostProjection:    filepath.Join(machineRoot, "Privileged", "state", "host-projection.json"),
		resolver:          "/etc/resolver/test",
		launchDaemon:      "/Library/LaunchDaemons/com.goforj.harbor.launchdrelay.plist",
		pendingTickets:    filepath.Join(machineRoot, "Privileged", "tickets", "pending"),
		claimedTickets:    filepath.Join(machineRoot, "Privileged", "tickets", "claims"),
		replayTombstones:  filepath.Join(machineRoot, "Privileged", "state", "replay"),
		currentSelection:  filepath.Join(machineRoot, "current"),
		packageIdentifier: installpaths.ProductID,
	}
}

// uninstallDarwin proves all removal authority before deleting any package-owned object.
func uninstallDarwin(
	userID uint32,
	groupID uint32,
	dependencies darwinUninstallDependencies,
) error {
	if dependencies.effectiveUID() != 0 {
		return errors.New("Harbor uninstall requires effective UID 0")
	}
	if _, err := dependencies.verifyInstallation(); err != nil {
		return fmt.Errorf("verify installed Harbor product identity: %w", err)
	}
	paths := productionUninstallPaths()
	if err := dependencies.requireReceipt(paths.packageIdentifier); err != nil {
		return fmt.Errorf("verify Harbor package receipt: %w", err)
	}
	if err := requireDarwinHostIntegrationReleased(paths, dependencies); err != nil {
		return err
	}
	if err := verifyDarwinRemovalTrees(paths, userID, groupID); err != nil {
		return err
	}

	for _, path := range []string{paths.cli, paths.helper, paths.relay} {
		if err := dependencies.remove(path); err != nil {
			return fmt.Errorf("remove admitted Harbor path %q: %w", path, err)
		}
	}
	for _, path := range []string{paths.application, paths.machineRoot} {
		if err := dependencies.removeAll(path); err != nil {
			return fmt.Errorf("remove admitted Harbor tree %q: %w", path, err)
		}
	}
	if err := dependencies.removeEmpty(paths.productParent); err != nil && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty Harbor product parent: %w", err)
	}
	if err := dependencies.forgetReceipt(paths.packageIdentifier); err != nil {
		return fmt.Errorf("forget removed Harbor package receipt: %w", err)
	}
	return nil
}

// requireDarwinHostIntegrationReleased prevents product removal from erasing the authority needed to finish networking cleanup.
func requireDarwinHostIntegrationReleased(paths darwinUninstallPaths, dependencies darwinUninstallDependencies) error {
	for _, path := range []string{paths.ownership, paths.hostProjection, paths.resolver, paths.launchDaemon} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("Harbor host integration remains at %q; run network release before uninstall", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Harbor host integration %q: %w", path, err)
		}
	}
	if err := dependencies.requireLabelAbsent("com.goforj.harbor.launchdrelay"); err != nil {
		return err
	}
	return nil
}

// verifyDarwinRemovalTrees rejects redirects, foreign ownership, and pending user input before recursive removal.
func verifyDarwinRemovalTrees(paths darwinUninstallPaths, userID uint32, groupID uint32) error {
	if err := verifyDarwinRemovalTree(paths.application, "", "", nil, userID, groupID); err != nil {
		return fmt.Errorf("admit Harbor application removal: %w", err)
	}
	runtimeFileDirectories := map[string]struct{}{
		paths.claimedTickets:   {},
		paths.replayTombstones: {},
	}
	if err := verifyDarwinRemovalTree(
		paths.machineRoot,
		paths.currentSelection,
		paths.pendingTickets,
		runtimeFileDirectories,
		userID,
		groupID,
	); err != nil {
		return fmt.Errorf("admit Harbor machine-root removal: %w", err)
	}
	return nil
}

// verifyDarwinRemovalTree permits only root-owned package objects plus one empty current-user ticket directory.
func verifyDarwinRemovalTree(
	root string,
	allowedSymlink string,
	pendingTickets string,
	runtimeFileDirectories map[string]struct{},
	userID uint32,
	groupID uint32,
) error {
	return filepath.Walk(root, func(path string, information os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		status, ok := information.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("native ownership is unavailable for %q", path)
		}
		if information.Mode()&os.ModeSymlink != 0 {
			if path != allowedSymlink {
				return fmt.Errorf("unexpected symlink %q", path)
			}
			return nil
		}
		if path == pendingTickets {
			if !information.IsDir() || status.Uid != userID || status.Gid != groupID {
				return fmt.Errorf("pending ticket directory %q has a foreign identity", path)
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("pending ticket directory %q is not empty", path)
			}
			return nil
		}
		if pendingTickets != "" && strings.HasPrefix(path, pendingTickets+string(os.PathSeparator)) {
			return fmt.Errorf("unexpected pending ticket artifact %q", path)
		}
		// Claims and replay records intentionally do not bind GID because their exact 0600 policy grants the group no access.
		for directory := range runtimeFileDirectories {
			if !strings.HasPrefix(path, directory+string(os.PathSeparator)) {
				continue
			}
			if filepath.Dir(path) != directory ||
				!information.Mode().IsRegular() ||
				information.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0o600 ||
				status.Uid != 0 ||
				status.Nlink != 1 {
				return fmt.Errorf("Harbor runtime tombstone %q has a foreign identity", path)
			}
			return nil
		}
		if status.Uid != 0 || status.Gid != 0 {
			return fmt.Errorf("package tree object %q is owned by %d:%d, want 0:0", path, status.Uid, status.Gid)
		}
		if information.Mode().IsRegular() && status.Nlink != 1 {
			return fmt.Errorf("package tree file %q has %d links, want 1", path, status.Nlink)
		}
		return nil
	})
}

// requirePackageReceipt admits only an installation still tracked by Apple's package database.
func requirePackageReceipt(identifier string) error {
	return runNativeUninstallCommand("/usr/sbin/pkgutil", "--pkg-info", identifier)
}

// forgetPackageReceipt removes the native receipt only after every admitted product path is gone.
func forgetPackageReceipt(identifier string) error {
	return runNativeUninstallCommand("/usr/sbin/pkgutil", "--forget", identifier)
}

// requireLaunchdLabelAbsent keeps the admitted helper available when its low-port service has not actually retired.
func requireLaunchdLabelAbsent(label string) error {
	command := exec.Command("/bin/launchctl", "print", "system/"+label)
	if err := command.Run(); err == nil {
		return fmt.Errorf("Harbor launchd label %q remains active; run network release before uninstall", label)
	} else if _, ok := err.(*exec.ExitError); !ok {
		return fmt.Errorf("inspect Harbor launchd label %q: %w", label, err)
	}
	return nil
}

// runNativeUninstallCommand returns bounded native diagnostics without granting the package lifecycle a shell.
func runNativeUninstallCommand(name string, arguments ...string) error {
	command := exec.Command(name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		if len(output) > 4096 {
			output = output[:4096]
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
