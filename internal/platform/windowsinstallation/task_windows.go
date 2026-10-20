//go:build windows

package windowsinstallation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Task executes the fixed signed scheduler policy while the installation remains retained.
func (installation *Installation) Task(ctx context.Context, action string) error {
	if err := admitTaskAction(action); err != nil {
		return err
	}
	policy, err := installation.Path("task-policy")
	if err != nil {
		return err
	}
	root, err := MachineRoot()
	if err != nil {
		return err
	}
	if !strings.EqualFold(root, installation.root) {
		return errors.New("task policy requires the fixed admitted machine installation")
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	var session uint32
	if err := tokenInteger(token, windows.TokenSessionId, &session); err != nil {
		return err
	}
	if err := admitTaskUser(installation.selection.UserSID, user.User.Sid.String(), session); err != nil {
		return err
	}
	shell, err := retainInteractiveShell(user.User.Sid.String(), session)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(shell)
	if action == "register" || action == "remove" {
		installer, err := installation.Path("installer")
		if err != nil {
			return err
		}
		current, err := os.Executable()
		if err != nil {
			return err
		}
		if !strings.EqualFold(current, installer) || !windows.GetCurrentProcessToken().IsElevated() {
			return errors.New("task mutation requires the elevated admitted installer")
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executable, err := taskPowerShell()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "AllSigned", "-File", policy, "-Action", action)
	command.Dir = root
	command.Env = taskEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installed task %s: %w: %s", action, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// admitTaskAction prevents caller-selected scripts, task names, principals, or scheduler operations.
func admitTaskAction(action string) error {
	switch action {
	case "register", "start", "inspect", "remove":
		return nil
	default:
		return errors.New("Windows installed task action is not admitted")
	}
}

// taskPowerShell resolves Windows PowerShell through the operating system rather than caller environment.
func taskPowerShell() (string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), nil
}

// taskEnvironment removes caller module discovery and startup overrides before signed policy execution.
func taskEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name := strings.SplitN(entry, "=", 2)[0]
		if strings.EqualFold(name, "PSModulePath") || strings.EqualFold(name, "PSModuleAutoLoadingPreference") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "PSModulePath=", "PSModuleAutoLoadingPreference=None")
}

// admitTaskUser binds scheduler actions to the selected account before starting the signed interactive-session proof.
func admitTaskUser(selected, current string, session uint32) error {
	if !validUserSID(selected) || selected != current || current == systemSID || session == 0 {
		return errors.New("task operation requires the installation's intended interactive user")
	}
	return nil
}
