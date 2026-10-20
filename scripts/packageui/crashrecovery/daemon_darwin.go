//go:build darwin

package main

/*
#include <libproc.h>
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/platform/daemonservice"
	"github.com/goforj/harbor/internal/rpc"
	"golang.org/x/sys/unix"
)

// installedCLI selects the root-owned release CLI through the package's stable selector.
func installedCLI() (string, error) {
	path, err := filepath.EvalSymlinks("/usr/local/bin/harbor")
	if err != nil {
		return "", err
	}
	expected, err := filepath.EvalSymlinks(daemonservice.DarwinWorkingDirectory + "/bin/harbor")
	if err != nil {
		return "", err
	}
	if path != expected {
		return "", fmt.Errorf("installed CLI does not select the current package release")
	}
	information, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if err := daemonservice.AdmitExecutable(information); err != nil {
		return "", err
	}
	return "/usr/local/bin/harbor", nil
}

// installedDarwinJob validates the retained exact LaunchAgent before any launchd mutation.
func installedDarwinJob(ctx context.Context) (string, map[string]string, error) {
	if os.Geteuid() <= 0 {
		return "", nil, fmt.Errorf("installed GUI recovery requires an unprivileged user")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", daemonservice.DarwinLabel+".plist")
	information, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || information.Mode().Perm() != 0o644 || status.Uid != uint32(os.Geteuid()) || status.Nlink != 1 {
		return "", nil, fmt.Errorf("installed LaunchAgent owner or mode differs from the package contract")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	if !daemonservice.MatchesDarwinDefinition(content) {
		return "", nil, fmt.Errorf("installed LaunchAgent definition differs from package contract")
	}
	launcher, err := os.Lstat(daemonservice.DarwinLauncher)
	if err != nil {
		return "", nil, err
	}
	if err := daemonservice.AdmitExecutable(launcher); err != nil {
		return "", nil, err
	}
	job := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + daemonservice.DarwinLabel
	output, err := exec.CommandContext(ctx, "/bin/launchctl", "print", job).Output()
	if err != nil {
		return "", nil, err
	}
	facts, err := darwinJobScalars(string(output), job)
	if err != nil {
		return "", nil, err
	}
	if facts["path"] != path || facts["program"] != daemonservice.DarwinLauncher || facts["arguments"] != daemonservice.DarwinLauncher {
		return "", nil, fmt.Errorf("loaded launchd job does not match the installed LaunchAgent")
	}
	return job, facts, nil
}

// darwinJobScalars excludes nested launchd fields and rejects duplicate direct facts.
func darwinJobScalars(output, job string) (map[string]string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != job+" = {" {
		return nil, fmt.Errorf("unexpected launchd job root")
	}
	depth := 1
	facts := make(map[string]string)
	arguments := false
	for index, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if arguments && depth == 2 && line != "}" {
			if facts["arguments"] != "" {
				return nil, fmt.Errorf("unexpected extra launchd argument")
			}
			facts["arguments"] = line
		}
		if arguments && depth == 2 && line == "}" {
			arguments = false
		}
		if depth == 1 {
			name, value, ok := strings.Cut(line, " = ")
			if ok && name == "arguments" && value == "{" {
				if _, exists := facts[name]; exists {
					return nil, fmt.Errorf("duplicate launchd arguments")
				}
				facts[name] = ""
				arguments = true
			}
			if ok && value != "{" {
				if _, exists := facts[name]; exists {
					return nil, fmt.Errorf("duplicate launchd job field %s", name)
				}
				facts[name] = value
			}
		}
		if strings.HasSuffix(line, "{") {
			depth++
		}
		if line == "}" {
			depth--
		}
		if depth < 0 || (depth == 0 && index != len(lines)-2) {
			return nil, fmt.Errorf("malformed launchd job structure")
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unclosed launchd job structure")
	}
	return facts, nil
}

// observeInstalledDaemon correlates the authenticated peer, exact launchd job, and native process facts.
func observeInstalledDaemon(ctx context.Context, peer control.DaemonPeer) (daemonIdentity, error) {
	uid := strconv.Itoa(os.Geteuid())
	if peer.Transport.UserID != uid || peer.Transport.ProcessID <= 1 {
		return daemonIdentity{}, fmt.Errorf("daemon peer is not the current user's process")
	}
	_, facts, err := installedDarwinJob(ctx)
	if err != nil {
		return daemonIdentity{}, err
	}
	if facts["pid"] != strconv.FormatUint(uint64(peer.Transport.ProcessID), 10) {
		return daemonIdentity{}, fmt.Errorf("authenticated daemon is not the exact installed launchd process")
	}
	pid := int(peer.Transport.ProcessID)
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return daemonIdentity{}, err
	}
	if len(processes) != 1 || processes[0].Proc.P_pid != int32(pid) || processes[0].Eproc.Ucred.Uid != uint32(os.Geteuid()) ||
		processes[0].Eproc.Pcred.P_ruid != uint32(os.Geteuid()) || processes[0].Eproc.Pcred.P_svuid != uint32(os.Geteuid()) {
		return daemonIdentity{}, fmt.Errorf("daemon kernel identity does not match authenticated peer")
	}
	started := processes[0].Proc.P_starttime
	if started.Sec <= 0 {
		return daemonIdentity{}, fmt.Errorf("daemon kernel birth is empty")
	}
	buffer := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	length := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buffer[0]), C.uint32_t(len(buffer)))
	if length <= 0 {
		return daemonIdentity{}, fmt.Errorf("read daemon native executable failed")
	}
	executable := strings.TrimRight(string(buffer[:int(length)]), "\x00")
	expected, err := filepath.EvalSymlinks(daemonservice.DarwinWorkingDirectory + "/bin/harbord")
	if err != nil {
		return daemonIdentity{}, err
	}
	if executable != expected {
		return daemonIdentity{}, fmt.Errorf("daemon executable differs from current installed release")
	}
	information, err := os.Lstat(expected)
	if err != nil {
		return daemonIdentity{}, err
	}
	if err := daemonservice.AdmitExecutable(information); err != nil {
		return daemonIdentity{}, err
	}
	again, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return daemonIdentity{}, err
	}
	if len(again) != 1 || again[0].Proc.P_starttime != started || again[0].Eproc.Ucred.Uid != uint32(os.Geteuid()) ||
		again[0].Eproc.Pcred.P_ruid != uint32(os.Geteuid()) || again[0].Eproc.Pcred.P_svuid != uint32(os.Geteuid()) {
		return daemonIdentity{}, fmt.Errorf("daemon native identity drifted during observation")
	}
	return daemonIdentity{PID: peer.Transport.ProcessID, UserID: uid, Executable: executable, Birth: fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec)}, nil
}

// crashInstalledDaemon reauthenticates immediately before killing only the fixed user's exact launchd job.
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
	job := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + daemonservice.DarwinLabel
	return exec.CommandContext(ctx, "/bin/launchctl", "kill", "SIGKILL", job).Run()
}

// ensureInstalledDaemon uses the installed job's ordinary non-replacing launch path.
func ensureInstalledDaemon(ctx context.Context) error {
	job, _, err := installedDarwinJob(ctx)
	if err != nil {
		return err
	}
	return exec.CommandContext(ctx, "/bin/launchctl", "kickstart", job).Run()
}
