//go:build linux

package containerruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nativeDockerTestEnvironment = "HARBOR_NATIVE_DOCKER_TEST"

// TestNativeDockerRuntimeAdmitsOnlyItsCheckoutAndFollowsReplacement proves the shipping
// read-only Engine boundary against two real, independently labeled Compose projects.
func TestNativeDockerRuntimeAdmitsOnlyItsCheckoutAndFollowsReplacement(t *testing.T) {
	if os.Getenv(nativeDockerTestEnvironment) != "1" {
		t.Skipf("set %s=1 to exercise the local Docker Engine", nativeDockerTestEnvironment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	installationID := fmt.Sprintf("harbor-native-%d", time.Now().UnixNano())
	target := newNativeComposeFixture(t, installationID+"-target", "target-initial")
	neighbor := newNativeComposeFixture(t, installationID+"-neighbor", "neighbor-only")
	target.up(ctx, t)
	neighbor.up(ctx, t)

	runtime, err := NewDocker()
	if err != nil {
		t.Fatalf("NewDocker() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("DockerRuntime.Close() error = %v", closeErr)
		}
	})

	before := nativeComposeContainerIDs(ctx, t, target, neighbor)
	observation, err := runtime.ObserveProject(ctx, target.root)
	if err != nil {
		t.Fatalf("ObserveProject() error = %v", err)
	}
	if len(observation.Services) != 1 || observation.Services[0].ID != "db" || len(observation.Services[0].Containers) != 1 {
		t.Fatalf("ObserveProject() = %#v", observation)
	}
	if observation.Services[0].Containers[0].ID != before.target {
		t.Fatalf("admitted container = %q, want target %q", observation.Services[0].Containers[0].ID, before.target)
	}
	if after := nativeComposeContainerIDs(ctx, t, target, neighbor); after != before {
		t.Fatalf("read-only ObserveProject() changed fixture containers: before %+v, after %+v", before, after)
	}

	follower, err := runtime.OpenServiceLogs(ctx, target.root, "db", 50)
	if err != nil {
		t.Fatalf("OpenServiceLogs() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := follower.Close(); closeErr != nil {
			t.Errorf("LogFollower.Close() error = %v", closeErr)
		}
	})
	if !follower.Available() {
		t.Fatal("OpenServiceLogs() reported the admitted target unavailable")
	}
	transcript := new(synchronizedTestBuffer)
	copyDone := make(chan error, 1)
	go func() { copyDone <- follower.CopyTo(transcript) }()
	nativeWaitForLog(t, ctx, transcript, "target-initial")
	if strings.Contains(transcript.String(), "neighbor-only") {
		t.Fatalf("target log follower admitted neighboring project output: %q", transcript.String())
	}
	if after := nativeComposeContainerIDs(ctx, t, target, neighbor); after != before {
		t.Fatalf("read-only log open changed fixture containers: before %+v, after %+v", before, after)
	}

	target.writeCompose(t, "target-replacement")
	target.recreate(ctx, t)
	nativeWaitForLog(t, ctx, transcript, "target-replacement")
	if err := follower.Close(); err != nil {
		t.Fatalf("LogFollower.Close() error = %v", err)
	}
	if err := <-copyDone; err != nil {
		t.Fatalf("LogFollower.CopyTo() error = %v", err)
	}
	if strings.Contains(transcript.String(), "neighbor-only") {
		t.Fatalf("replacement log follower admitted neighboring project output: %q", transcript.String())
	}
}

// nativeComposeFixture confines test-created containers to a random Compose identity and temporary checkout.
type nativeComposeFixture struct {
	root         string
	project      string
	installation string
}

// newNativeComposeFixture creates an isolated Compose checkout whose labels exercise Harbor's admission boundary.
func newNativeComposeFixture(t *testing.T, installation string, marker string) nativeComposeFixture {
	t.Helper()
	fixture := nativeComposeFixture{
		root:         t.TempDir(),
		project:      strings.ReplaceAll(installation, "-", ""),
		installation: installation,
	}
	fixture.writeCompose(t, marker)
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fixture.run(cleanupContext, t, "down", "--volumes", "--remove-orphans")
	})
	return fixture
}

// writeCompose changes only this fixture's marker so force recreation has unambiguous log evidence.
func (fixture nativeComposeFixture) writeCompose(t *testing.T, marker string) {
	t.Helper()
	content := fmt.Sprintf(`services:
  db:
    image: busybox:1.36.1
    command: ["sh", "-c", "echo %s; exec sleep 300"]
    labels:
      com.goforj.harbor.native-test: %s
`, marker, fixture.installation)
	if err := os.WriteFile(filepath.Join(fixture.root, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture compose file: %v", err)
	}
}

// up starts exactly this fixture before Harbor observes it.
func (fixture nativeComposeFixture) up(ctx context.Context, t *testing.T) {
	t.Helper()
	fixture.run(ctx, t, "up", "--detach")
	nativeWaitForContainer(t, ctx, fixture)
}

// recreate replaces only this fixture's db replica after the initial Harbor observation.
func (fixture nativeComposeFixture) recreate(ctx context.Context, t *testing.T) {
	t.Helper()
	fixture.run(ctx, t, "up", "--detach", "--force-recreate", "db")
	nativeWaitForContainer(t, ctx, fixture)
}

// run invokes the Compose CLI only to establish or retire the explicitly owned test fixture.
func (fixture nativeComposeFixture) run(ctx context.Context, t *testing.T, arguments ...string) string {
	t.Helper()
	base := []string{"compose", "--project-directory", fixture.root, "--project-name", fixture.project}
	command := exec.CommandContext(ctx, "docker", append(base, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(append(base, arguments...), " "), err, output)
	}
	return string(output)
}

// nativeWaitForContainer prevents a daemon scheduling delay from being misreported as adapter admission failure.
func nativeWaitForContainer(t *testing.T, ctx context.Context, fixture nativeComposeFixture) {
	t.Helper()
	for {
		id := strings.TrimSpace(fixture.run(ctx, t, "ps", "--quiet", "db"))
		if id != "" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fixture %s did not start: %v", fixture.project, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// nativeComposeIDs is the immutable fixture state that adapter calls must not alter.
type nativeComposeIDs struct {
	target   string
	neighbor string
}

// nativeComposeContainerIDs snapshots the one expected container from each isolated fixture.
func nativeComposeContainerIDs(ctx context.Context, t *testing.T, target, neighbor nativeComposeFixture) nativeComposeIDs {
	t.Helper()
	return nativeComposeIDs{
		target:   strings.TrimSpace(target.run(ctx, t, "ps", "--quiet", "db")),
		neighbor: strings.TrimSpace(neighbor.run(ctx, t, "ps", "--quiet", "db")),
	}
}

// nativeWaitForLog waits only until the follower delivers the named fixture marker.
func nativeWaitForLog(t *testing.T, ctx context.Context, transcript *synchronizedTestBuffer, marker string) {
	t.Helper()
	for !strings.Contains(transcript.String(), marker) {
		select {
		case <-ctx.Done():
			t.Fatalf("service log transcript did not contain %q before timeout: %q", marker, transcript.String())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
