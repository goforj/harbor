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
	"strconv"

	"github.com/goforj/harbor/internal/devbootstrap"
	"github.com/goforj/harbor/internal/platform/helperpath"
	"github.com/goforj/harbor/internal/platform/launchdrelaypath"
)

// main applies one package-selected installation operation without ambient path authority.
func main() {
	os.Clearenv()
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor installer: %v\n", err)
		os.Exit(1)
	}
}

// run accepts only the console-user identity needed to provision Harbor's fixed ticket boundary.
func run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] != "install" {
		return errors.New("install operation is required")
	}
	flags := flag.NewFlagSet("harbor-installer install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	userID := flags.String("user-id", "", "non-root Harbor user ID")
	groupID := flags.String("group-id", "", "Harbor user group ID")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	parsedUserID, err := parseID("user-id", *userID, false)
	if err != nil {
		return err
	}
	parsedGroupID, err := parseID("group-id", *groupID, true)
	if err != nil {
		return err
	}
	if err := devbootstrap.Bootstrap(devbootstrap.Config{
		HelperSource:       helperpath.Executable(),
		LaunchdRelaySource: launchdrelaypath.Executable(),
		UserID:             parsedUserID,
		GroupID:            parsedGroupID,
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "Harbor installation bootstrap complete.")
	return err
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
