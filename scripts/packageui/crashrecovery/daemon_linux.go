//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/platform/daemonservice"
	"github.com/goforj/harbor/internal/rpc"
)

// installedCLI selects only the package-owned CLI used by the graphical fixture.
func installedCLI() (string, error) {
	information, err := os.Lstat("/usr/bin/harbor")
	if err != nil {
		return "", err
	}
	if err := daemonservice.AdmitExecutable(information); err != nil {
		return "", err
	}
	return "/usr/bin/harbor", nil
}

// installedLinuxUnit refuses mutable definitions and user service overrides.
func installedLinuxUnit(ctx context.Context) (map[string]string, error) {
	information, err := os.Lstat(daemonservice.LinuxUnitPath)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(daemonservice.LinuxUnitPath)
	if err != nil {
		return nil, err
	}
	if err := daemonservice.AdmitUnit(information, content); err != nil {
		return nil, err
	}
	for _, path := range []string{daemonservice.LinuxLauncherPath, daemonservice.LinuxDaemonPath} {
		information, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if err := daemonservice.AdmitExecutable(information); err != nil {
			return nil, err
		}
	}
	output, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "show", daemonservice.LinuxUnit,
		"--property=MainPID,FragmentPath,DropInPaths,LoadState,KillMode,ExecStart").Output()
	if err != nil {
		return nil, err
	}
	properties := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("malformed systemd property")
		}
		if _, exists := properties[name]; exists {
			return nil, fmt.Errorf("duplicate systemd property %s", name)
		}
		properties[name] = value
	}
	if properties["FragmentPath"] != daemonservice.LinuxUnitPath || properties["DropInPaths"] != "" ||
		properties["LoadState"] != "loaded" || properties["KillMode"] != "process" ||
		!strings.HasPrefix(properties["ExecStart"], "{ path="+daemonservice.LinuxLauncherPath+" ; argv[]="+daemonservice.LinuxLauncherPath+" ;") {
		return nil, fmt.Errorf("installed daemon service identity differs from the package contract")
	}
	return properties, nil
}

// observeInstalledDaemon binds the authenticated peer to systemd and native kernel birth evidence.
func observeInstalledDaemon(ctx context.Context, peer control.DaemonPeer) (daemonIdentity, error) {
	uid := strconv.Itoa(os.Geteuid())
	if os.Geteuid() <= 0 || peer.Transport.UserID != uid || peer.Transport.ProcessID <= 1 {
		return daemonIdentity{}, fmt.Errorf("daemon peer is not the current unprivileged user")
	}
	properties, err := installedLinuxUnit(ctx)
	if err != nil {
		return daemonIdentity{}, err
	}
	pid := strconv.FormatUint(uint64(peer.Transport.ProcessID), 10)
	if properties["MainPID"] != pid {
		return daemonIdentity{}, fmt.Errorf("authenticated daemon is not the installed service main process")
	}
	root := "/proc/" + pid
	information, err := os.Stat(root)
	if err != nil {
		return daemonIdentity{}, err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(status.Uid), 10) != uid {
		return daemonIdentity{}, fmt.Errorf("daemon kernel owner differs from authenticated user")
	}
	credentials, err := os.ReadFile(root + "/status")
	if err != nil {
		return daemonIdentity{}, err
	}
	matched := false
	for _, line := range strings.Split(string(credentials), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if matched || len(fields) != 5 {
			return daemonIdentity{}, fmt.Errorf("ambiguous daemon kernel credentials")
		}
		for _, value := range fields[1:] {
			if value != uid {
				return daemonIdentity{}, fmt.Errorf("daemon real or saved user differs from authenticated peer")
			}
		}
		matched = true
	}
	if !matched {
		return daemonIdentity{}, fmt.Errorf("daemon kernel credentials unavailable")
	}
	executable, err := os.Readlink(root + "/exe")
	if err != nil {
		return daemonIdentity{}, err
	}
	if executable != daemonservice.LinuxDaemonPath {
		return daemonIdentity{}, fmt.Errorf("daemon executable is not the installed binary")
	}
	stat, err := os.ReadFile(root + "/stat")
	if err != nil {
		return daemonIdentity{}, err
	}
	birth, err := linuxDaemonBirth(stat)
	if err != nil {
		return daemonIdentity{}, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return daemonIdentity{}, err
	}
	if strings.TrimSpace(string(boot)) == "" {
		return daemonIdentity{}, fmt.Errorf("kernel boot identity is empty")
	}
	return daemonIdentity{PID: peer.Transport.ProcessID, UserID: uid, Executable: executable, Birth: "linux:" + strings.TrimSpace(string(boot)) + ":" + birth}, nil
}

// linuxDaemonBirth handles parenthesized process names without trusting their whitespace.
func linuxDaemonBirth(stat []byte) (string, error) {
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+2 >= len(stat) {
		return "", fmt.Errorf("malformed daemon process record")
	}
	fields := strings.Fields(string(stat[closing+2:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("daemon process birth is missing")
	}
	tick, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || tick == 0 {
		return "", fmt.Errorf("invalid daemon process birth")
	}
	return fields[19], nil
}

// crashInstalledDaemon reauthenticates and revalidates before signaling only the service main process.
func crashInstalledDaemon(ctx context.Context, before daemonIdentity) error {
	client, err := control.NewClient(ctx, control.ClientConfig{Role: rpc.RoleCLI})
	if err != nil {
		return err
	}
	defer client.Close()
	current, err := observeInstalledDaemon(ctx, client.Peer())
	if err != nil {
		return err
	}
	if current != before {
		return fmt.Errorf("daemon identity drifted before crash; no signal sent")
	}
	again, err := observeInstalledDaemon(ctx, client.Peer())
	if err != nil {
		return err
	}
	if again != before {
		return fmt.Errorf("daemon native facts drifted before crash; no signal sent")
	}
	return exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "kill", "--kill-whom=main", "--signal=SIGKILL", daemonservice.LinuxUnit).Run()
}

// ensureInstalledDaemon starts the existing package service without changing its restart policy.
func ensureInstalledDaemon(ctx context.Context) error {
	if _, err := installedLinuxUnit(ctx); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "start", daemonservice.LinuxUnit).Run()
}
