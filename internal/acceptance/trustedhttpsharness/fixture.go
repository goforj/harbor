package trustedhttpsharness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const maximumFixtureBuildOutputBytes = 64 << 10

// fixtureBuildInvoker runs one direct GoForj build with exact arguments in an exact generated checkout.
type fixtureBuildInvoker func(context.Context, string, string, ...string) error

// PrepareGeneratedResponses creates each generated project's own OpenAPI identity
// before checkout baselines are captured.
func PrepareGeneratedResponses(
	ctx context.Context,
	forjExecutable string,
	projects []ProjectSpec,
	rendered []goforjproject.Project,
) error {
	return prepareGeneratedResponsesWith(ctx, forjExecutable, projects, rendered, invokeFixtureBuild)
}

// prepareGeneratedResponsesWith keeps production validation and post-build
// artifact checks directly testable without launching GoForj in unit tests.
func prepareGeneratedResponsesWith(
	ctx context.Context,
	forjExecutable string,
	projects []ProjectSpec,
	rendered []goforjproject.Project,
	invoke fixtureBuildInvoker,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFixtureExecutable(forjExecutable); err != nil {
		return err
	}
	if err := validateProjectSpecs(projects); err != nil {
		return err
	}
	if invoke == nil {
		panic("trusted HTTPS fixture preparation requires an invoker")
	}
	if len(rendered) != len(projects) {
		return fmt.Errorf("rendered fixture count is %d, want %d", len(rendered), len(projects))
	}

	for index, project := range projects {
		generated := rendered[index]
		if generated.Name != project.Name || generated.Module != project.Module || generated.Port != project.AppPort {
			return fmt.Errorf("rendered fixture %d does not match its trusted HTTPS project specification", index)
		}
		if generated.Root == "" || !filepath.IsAbs(generated.Root) || filepath.Clean(generated.Root) != generated.Root {
			return fmt.Errorf("rendered fixture %q root %q must be absolute and clean", generated.Name, generated.Root)
		}
		if err := invoke(ctx, forjExecutable, generated.Root, "build:api-index"); err != nil {
			return fmt.Errorf("build generated OpenAPI identity for %q: %w", project.Name, err)
		}
		title, err := readGeneratedOpenAPITitle(generated.Root)
		if err != nil {
			return fmt.Errorf("validate generated OpenAPI identity for %q: %w", project.Name, err)
		}
		if title != project.Name {
			return fmt.Errorf("generated OpenAPI title for %q is %q", project.Name, title)
		}
		if err := invoke(ctx, forjExecutable, generated.Root, "build", "-o", "./bin/app"); err != nil {
			return fmt.Errorf("build generated application for %q: %w", project.Name, err)
		}
	}
	return nil
}

// validateFixtureExecutable requires one direct caller-selected GoForj binary,
// preventing PATH lookup or a replaced link from changing fixture generation.
func validateFixtureExecutable(filename string) error {
	if filename == "" || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return fmt.Errorf("GoForj fixture executable %q must be absolute and clean", filename)
	}
	if filepath.Base(filename) != "forj" {
		return fmt.Errorf("GoForj fixture executable basename is %q, want %q", filepath.Base(filename), "forj")
	}
	information, err := os.Lstat(filename)
	if err != nil {
		return fmt.Errorf("inspect GoForj fixture executable: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() || information.Mode().Perm()&0o111 == 0 {
		return errors.New("GoForj fixture executable must be a direct executable regular file")
	}
	return nil
}

// invokeFixtureBuild runs one pinned GoForj build before the checkout snapshot;
// it does not modify generated source or add test handlers.
func invokeFixtureBuild(ctx context.Context, executable string, root string, arguments ...string) error {
	stdout := &boundedBuffer{maximum: maximumFixtureBuildOutputBytes}
	stderr := &boundedBuffer{maximum: maximumFixtureBuildOutputBytes}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = root
	command.Env = os.Environ()
	command.Stdin = strings.NewReader("")
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return errors.New("GoForj build output exceeded the acceptance bound")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf(
			"forj %s failed: %w (stdout: %q, stderr: %q)",
			strings.Join(arguments, " "),
			err,
			strings.TrimSpace(string(stdout.Bytes())),
			strings.TrimSpace(string(stderr.Bytes())),
		)
	}
	return nil
}

// readGeneratedOpenAPITitle reads only the direct bounded artifact served by
// the unmodified generated App's `/swagger/doc.json` route.
func readGeneratedOpenAPITitle(root string) (string, error) {
	filename := filepath.Join(root, "build", "openapi.json")
	information, err := os.Lstat(filename)
	if err != nil {
		return "", err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return "", errors.New("generated OpenAPI artifact is not a direct regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximumProbeOutputBytes+1))
	if err != nil {
		return "", err
	}
	return decodeOpenAPITitle(body)
}
