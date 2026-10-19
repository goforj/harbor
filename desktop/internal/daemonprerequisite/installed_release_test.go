//go:build (darwin || linux) && release

package daemonprerequisite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/platform/userpaths"
)

// TestInstalledDaemonLifecycle exercises the same release adapter as desktop first launch on disposable package workers.
func TestInstalledDaemonLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_INSTALLED_DAEMON_ACCEPTANCE") != "1" {
		t.Skip("set HARBOR_INSTALLED_DAEMON_ACCEPTANCE=1 on a clean native package worker")
	}
	if os.Geteuid() == 0 {
		t.Fatal("installed desktop and daemon must run unelevated")
	}
	cli := "/usr/bin/harbor"
	if runtime.GOOS == "darwin" {
		cli = "/usr/local/bin/harbor"
	}
	database, err := userpaths.DatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh-install proof requires an absent user database: %v", err)
	}
	project := os.Getenv("HARBOR_INSTALLED_PROJECT_DIRECTORY")
	evidence := os.Getenv("HARBOR_INSTALLED_DAEMON_EVIDENCE")
	if !filepath.IsAbs(project) || !filepath.IsAbs(evidence) {
		t.Fatal("explicit absolute project and evidence paths are required")
	}
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		".goforj.yml":      "module_name: example.test/installed-lifecycle\n",
		".env":             "APP_NAME='Installed lifecycle proof'\n",
		"project-data.txt": "preserved project data\n",
	} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	clientContext, closeClient := context.WithCancel(ctx)
	if err := New().Ensure(clientContext); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()
		_, _ = installedCommand(cleanup, cli, "daemon", "stop")
	}()
	awaitInstalledDaemon(t, ctx, cli, true)
	closeClient()
	awaitInstalledDaemon(t, ctx, cli, true)
	output, err := installedCommand(ctx, cli, "add", project, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var registration control.ProjectRegistration
	if err := json.Unmarshal(output, &registration); err != nil || registration.Project.ID == "" {
		t.Fatalf("registration: %s %v", output, err)
	}
	data, err := userpaths.DataDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "package-preservation.txt"), []byte("preserved user data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	assertInstalledRegistration(t, ctx, cli, registration.Project.ID)
	if _, err := installedCommand(ctx, cli, "daemon", "stop"); err != nil {
		t.Fatal(err)
	}
	awaitInstalledDaemon(t, ctx, cli, false)
	awaitInstalledServiceStopped(t, ctx)
	if err := New().Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	awaitInstalledDaemon(t, ctx, cli, true)
	assertInstalledRegistration(t, ctx, cli, registration.Project.ID)
	if _, err := installedCommand(ctx, cli, "daemon", "stop"); err != nil {
		t.Fatal(err)
	}
	awaitInstalledDaemon(t, ctx, cli, false)
	awaitInstalledServiceStopped(t, ctx)
	result, err := json.MarshalIndent(map[string]any{
		"platform":            runtime.GOOS,
		"fresh_user_database": "migrated by installed service",
		"client_close":        "daemon remains available",
		"reopen":              "registration retained",
		"daemon_restart":      "registration retained",
		"unprivileged":        true,
		"final_daemon_state":  "stopped",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, append(result, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// awaitInstalledServiceStopped joins the native manager before attempting a fresh Start or package removal.
func awaitInstalledServiceStopped(t *testing.T, ctx context.Context) {
	t.Helper()
	for {
		var command *exec.Cmd
		if runtime.GOOS == "linux" {
			command = exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "show", "harbord.service", "--property=ActiveState", "--value")
		} else {
			command = exec.CommandContext(ctx, "/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Geteuid())+"/com.goforj.harbor.daemon")
		}
		output, err := command.CombinedOutput()
		if err == nil {
			if runtime.GOOS == "linux" && strings.TrimSpace(string(output)) == "inactive" {
				return
			}
			if runtime.GOOS == "darwin" {
				for _, line := range strings.Split(string(output), "\n") {
					if strings.TrimSpace(line) == "state = not running" {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("installed service did not stop: %v: %.4096s", err, output)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// installedCommand uses only a package-selected CLI and the ordinary authenticated control boundary.
func installedCommand(ctx context.Context, cli string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, cli, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("installed Harbor %v: %w: %.4096s", arguments, err, output)
	}
	return output, nil
}

// awaitInstalledDaemon observes real service readiness without treating a manager acknowledgement as Ready.
func awaitInstalledDaemon(t *testing.T, ctx context.Context, cli string, ready bool) {
	t.Helper()
	for {
		output, err := installedCommand(ctx, cli, "daemon", "status", "--json")
		var status control.DaemonStatus
		available := err == nil && json.Unmarshal(output, &status) == nil && status.Validate() == nil
		if available == ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("installed daemon readiness=%t: %v: %.4096s", ready, ctx.Err(), output)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// assertInstalledRegistration proves manager reuse and daemon restart retain one authoritative project identity.
func assertInstalledRegistration(t *testing.T, ctx context.Context, cli string, projectID domain.ProjectID) {
	t.Helper()
	output, err := installedCommand(ctx, cli, "daemon", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal(output, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].ID != projectID {
		t.Fatalf("registration changed across client/daemon reopen: %s", output)
	}
}
