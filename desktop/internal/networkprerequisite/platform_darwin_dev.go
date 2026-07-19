//go:build darwin && dev

package networkprerequisite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const darwinDevelopmentBootstrapScript = `on run arguments
	if (count of arguments) is not 4 then error "invalid Harbor development bootstrap invocation"
	set bootstrapPath to item 1 of arguments
	set helperPath to item 2 of arguments
	set userID to item 3 of arguments
	set groupID to item 4 of arguments
	set commandLine to quoted form of bootstrapPath & " --helper " & quoted form of helperPath & " --user-id " & quoted form of userID & " --group-id " & quoted form of groupID
	try
		do shell script commandLine with administrator privileges
		return "succeeded"
	on error errorMessage number errorNumber
		if errorNumber is -128 then return "declined"
		return "failed"
	end try
end run`

const darwinDevelopmentArtifactMode = fs.FileMode(0o755)

// newPlatformEnsurer enables adjacent artifacts only in Wails' explicit macOS development build mode.
func newPlatformEnsurer() Ensurer {
	return newSourceEnsurer(sourceEnsurerDependencies{
		executable:   os.Executable,
		effectiveUID: os.Geteuid,
		effectiveGID: os.Getegid,
		inspect:      inspectDarwinDevelopmentArtifact,
		elevate:      elevateDarwinDevelopmentBootstrap,
	})
}

// inspectDarwinDevelopmentArtifact rejects links, foreign ownership, and mutable permission shapes before native consent.
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
	return nil
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
		strconv.FormatUint(uint64(request.userID), 10),
		strconv.FormatUint(uint64(request.groupID), 10),
	)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := command.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: macOS could not open Harbor development authorization", ErrFailed)
	}

	switch strings.TrimSpace(string(output)) {
	case "succeeded":
		return nil
	case "declined":
		return ErrDeclined
	case "failed":
		return ErrFailed
	default:
		return errors.New("privileged networking installation returned an invalid macOS authorization result")
	}
}
