//go:build darwin && dev

package networkprerequisite

import (
	"context"
	"debug/macho"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
)

const darwinDevelopmentBootstrapScript = `on run arguments
	if (count of arguments) is not 5 then error "invalid Harbor development bootstrap invocation"
	set bootstrapPath to item 1 of arguments
	set helperPath to item 2 of arguments
	set launchdRelayPath to item 3 of arguments
	set userID to item 4 of arguments
	set groupID to item 5 of arguments
	set commandLine to quoted form of bootstrapPath & " --helper " & quoted form of helperPath & " --launchd-relay " & quoted form of launchdRelayPath & " --user-id " & quoted form of userID & " --group-id " & quoted form of groupID
	try
		do shell script commandLine with administrator privileges
		return "succeeded"
	on error errorMessage number errorNumber
		if errorNumber is -128 then return "declined"
		return "failed|" & (errorNumber as text) & "|" & errorMessage
	end try
end run`

const darwinDevelopmentArtifactMode = fs.FileMode(0o755)

// newPlatformEnsurer enables adjacent artifacts only in Wails' explicit macOS development build mode.
func newPlatformEnsurer() Ensurer {
	return newSourceEnsurer(sourceEnsurerDependencies{
		executable:              os.Executable,
		effectiveUID:            os.Geteuid,
		effectiveGID:            os.Getegid,
		platformDirectoryExists: darwinDevelopmentArtifactDirectoryExists,
		inspect:                 inspectDarwinDevelopmentArtifact,
		elevate:                 elevateDarwinDevelopmentBootstrap,
	})
}

// darwinDevelopmentArtifactDirectoryExists distinguishes an absent transition directory from an unsafe replacement.
func darwinDevelopmentArtifactDirectoryExists(path string) (bool, error) {
	information, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !information.IsDir() {
		return false, fmt.Errorf("development artifact directory %q is not a directory", path)
	}
	return true, nil
}

// inspectDarwinDevelopmentArtifact admits only native thin executables before macOS displays privileged consent.
func inspectDarwinDevelopmentArtifact(path string, userID uint32, groupID uint32) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if !information.Mode().IsRegular() || information.Mode() != darwinDevelopmentArtifactMode {
		return fmt.Errorf("development artifact %q is not a regular 0755 file", path)
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("development artifact %q has unsupported native metadata", path)
	}
	if status.Uid != userID || status.Gid != groupID || status.Nlink != 1 {
		return fmt.Errorf("development artifact %q has unexpected ownership or link count", path)
	}

	executable, err := macho.Open(path)
	if err != nil {
		return fmt.Errorf("development artifact %q is not a thin Mach-O executable: %w", path, err)
	}
	defer func() {
		_ = executable.Close()
	}()
	if executable.Type != macho.TypeExec {
		return fmt.Errorf("development artifact %q is not a Mach-O executable", path)
	}
	expectedCPU, err := darwinDevelopmentArtifactCPU(runtime.GOARCH)
	if err != nil {
		return err
	}
	if executable.Cpu != expectedCPU {
		return fmt.Errorf("development artifact %q targets CPU %s, want %s", path, executable.Cpu, expectedCPU)
	}
	return nil
}

// darwinDevelopmentArtifactCPU maps Go's build architecture to Mach-O's reviewed native CPU values.
func darwinDevelopmentArtifactCPU(goarch string) (macho.Cpu, error) {
	switch goarch {
	case "amd64":
		return macho.CpuAmd64, nil
	case "arm64":
		return macho.CpuArm64, nil
	default:
		return 0, fmt.Errorf("Harbor development artifacts do not support Darwin architecture %q", goarch)
	}
}

// elevateDarwinDevelopmentBootstrap uses macOS native consent around one fixed, shell-quoted bootstrap command.
func elevateDarwinDevelopmentBootstrap(ctx context.Context, request sourceBootstrapRequest) error {
	command := exec.CommandContext(
		ctx,
		"/usr/bin/osascript",
		"-e",
		darwinDevelopmentBootstrapScript,
		request.bootstrapPath,
		request.helperPath,
		request.launchdRelayPath,
		strconv.FormatUint(uint64(request.userID), 10),
		strconv.FormatUint(uint64(request.groupID), 10),
	)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output := &darwinDevelopmentAuthorizationOutput{}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return darwinDevelopmentAuthorizationLaunchFailure(err, output.String())
	}

	return parseDarwinDevelopmentAuthorizationResult(output.String())
}
