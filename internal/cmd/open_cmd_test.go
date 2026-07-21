package cmd

import (
	"context"
	"errors"
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// newOpenCommandFixture creates one open command around an observable one-shot daemon connection.
func newOpenCommandFixture(connection *fakeDaemonControlClient) *OpenCmd {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	return NewOpenCmd(client)
}

// TestOpenCommandResolvesTheDefaultResourceThroughFreshState proves the browser receives only daemon-reviewed data.
func TestOpenCommandResolvesTheDefaultResourceThroughFreshState(t *testing.T) {
	project := addTestRegistration(t.TempDir(), true).Project
	project.Resources = []domain.ResourceSnapshot{{
		ID:   "app-http",
		Name: "Application",
		Kind: "application",
		Owner: domain.ResourceOwner{
			Kind:  domain.ResourceOwnedByApp,
			AppID: "app",
		},
		URL: "http://127.0.0.1:3000",
	}}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	connection := &fakeDaemonControlClient{snapshot: snapshot}
	command := newOpenCommandFixture(connection)
	command.ProjectID = project.ID
	var opened string
	command.open = func(_ context.Context, rawURL string) error {
		opened = rawURL
		return nil
	}

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run open: %v", err)
	}
	if opened != "http://127.0.0.1:3000" {
		t.Fatalf("opened URL = %q, want reviewed app URL", opened)
	}
	if connection.snapshotCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = snapshot:%d close:%d, want one each", connection.snapshotCalls, connection.closeCalls)
	}
}

// TestOpenCommandSelectsAnExplicitProjectScopedResource prevents a resource ID from crossing project ownership.
func TestOpenCommandSelectsAnExplicitProjectScopedResource(t *testing.T) {
	project := addTestRegistration(t.TempDir(), true).Project
	project.Resources = []domain.ResourceSnapshot{{
		ID:   "api-reference",
		Name: "API Reference",
		Kind: "docs",
		Owner: domain.ResourceOwner{
			Kind:  domain.ResourceOwnedByApp,
			AppID: "app",
		},
		URL: "https://orders.test/swagger",
	}}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	connection := &fakeDaemonControlClient{snapshot: snapshot}
	command := newOpenCommandFixture(connection)
	command.ProjectID = project.ID
	command.ResourceID = "api-reference"
	var opened string
	command.open = func(_ context.Context, rawURL string) error {
		opened = rawURL
		return nil
	}

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run open: %v", err)
	}
	if opened != "https://orders.test/swagger" {
		t.Fatalf("opened URL = %q, want explicit resource URL", opened)
	}
}

// TestOpenCommandRejectsInvalidSelectorsBeforeConnecting keeps malformed CLI input outside the daemon boundary.
func TestOpenCommandRejectsInvalidSelectorsBeforeConnecting(t *testing.T) {
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return &fakeDaemonControlClient{}, nil
	})
	command := NewOpenCmd(client)
	command.ProjectID = " bad "
	if err := command.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "project ID") {
		t.Fatalf("invalid project error = %v, want project validation", err)
	}
	if connectCalls != 0 {
		t.Fatalf("connect calls = %d, want 0", connectCalls)
	}
}

// TestOpenCommandRejectsUnreviewedURLBeforeOpening prevents malformed resource data from reaching the OS handler.
func TestOpenCommandRejectsUnreviewedURLBeforeOpening(t *testing.T) {
	project := addTestRegistration(t.TempDir(), true).Project
	project.Resources = []domain.ResourceSnapshot{{
		ID:   "app-http",
		Name: "Application",
		Kind: "application",
		Owner: domain.ResourceOwner{
			Kind:  domain.ResourceOwnedByApp,
			AppID: "app",
		},
		URL: (&url.URL{Scheme: "file", Path: "/tmp/secret"}).String(),
	}}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	connection := &fakeDaemonControlClient{snapshot: snapshot}
	command := newOpenCommandFixture(connection)
	command.ProjectID = project.ID
	called := false
	command.open = func(_ context.Context, _ string) error {
		called = true
		return nil
	}

	err := command.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "HTTP or HTTPS") {
		t.Fatalf("invalid URL error = %v, want URL validation", err)
	}
	if called {
		t.Fatal("URL opener was called for an invalid resource")
	}
}

// TestOpenCommandPropagatesBrowserFailure keeps operating-system launch failures visible to shell callers.
func TestOpenCommandPropagatesBrowserFailure(t *testing.T) {
	project := addTestRegistration(t.TempDir(), true).Project
	project.Resources = []domain.ResourceSnapshot{{
		ID:   "app-http",
		Name: "Application",
		Kind: "application",
		Owner: domain.ResourceOwner{
			Kind:  domain.ResourceOwnedByApp,
			AppID: "app",
		},
		URL: "http://127.0.0.1:3000",
	}}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	connection := &fakeDaemonControlClient{snapshot: snapshot}
	command := newOpenCommandFixture(connection)
	command.ProjectID = project.ID
	browserErr := errors.New("browser unavailable")
	command.open = func(context.Context, string) error { return browserErr }

	if err := command.Run(t.Context()); !errors.Is(err, browserErr) {
		t.Fatalf("browser error = %v, want %v", err, browserErr)
	}
}

// TestOpenURLCommandUsesOneFixedHandlerPerSupportedOS prevents a URL from becoming shell syntax or an arbitrary command.
func TestOpenURLCommandUsesOneFixedHandlerPerSupportedOS(t *testing.T) {
	const rawURL = "https://orders.test/swagger?from=harbor"
	name, arguments, err := openURLCommand(rawURL)
	switch runtime.GOOS {
	case "darwin":
		if err != nil || name != "open" || len(arguments) != 1 || arguments[0] != rawURL {
			t.Fatalf("openURLCommand() = %q %#v %v, want macOS open handler", name, arguments, err)
		}
	case "linux":
		if err != nil || name != "xdg-open" || len(arguments) != 1 || arguments[0] != rawURL {
			t.Fatalf("openURLCommand() = %q %#v %v, want Linux xdg-open handler", name, arguments, err)
		}
	case "windows":
		if err != nil || name != "rundll32.exe" || len(arguments) != 2 || arguments[0] != "url.dll,FileProtocolHandler" || arguments[1] != rawURL {
			t.Fatalf("openURLCommand() = %q %#v %v, want Windows URL handler", name, arguments, err)
		}
	default:
		if err == nil {
			t.Fatalf("openURLCommand() error = nil on unsupported %s", runtime.GOOS)
		}
	}
}
