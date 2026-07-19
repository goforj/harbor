//go:build darwin || linux

// Package main provides Harbor's explicit privileged source-development bootstrap.
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
)

// explicitFlag records whether one required option appeared and rejects last-value-wins ambiguity.
type explicitFlag struct {
	value string
	uses  int
}

// String returns the current flag spelling for flag package diagnostics.
func (value *explicitFlag) String() string {
	return value.value
}

// Set records one explicit value and rejects duplicate authority inputs.
func (value *explicitFlag) Set(input string) error {
	if value.uses != 0 {
		return errors.New("flag may be specified only once")
	}
	value.value = input
	value.uses++
	return nil
}

// main parses only the three reviewed inputs and delegates fixed-destination mutation to devbootstrap.
func main() {
	// Ambient project configuration has no role in this privileged development-only process.
	os.Clearenv()
	os.Exit(runCommand(os.Args[1:], os.Stdout, os.Stderr, devbootstrap.Bootstrap))
}

// runCommand reports one bounded result and returns the process status without hiding it behind os.Exit in tests.
func runCommand(arguments []string, output io.Writer, diagnostics io.Writer, bootstrap func(devbootstrap.Config) error) int {
	configuration, err := parseArguments(arguments)
	if err != nil {
		_, _ = fmt.Fprintf(diagnostics, "harbor development bootstrap: %v\n", err)
		return 2
	}
	if err := bootstrap(configuration); err != nil {
		_, _ = fmt.Fprintf(diagnostics, "harbor development bootstrap: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(output, "Harbor development bootstrap complete.")
	return 0
}

// parseArguments requires every caller-selected value to appear explicitly and rejects positional authority.
func parseArguments(arguments []string) (devbootstrap.Config, error) {
	flags := flag.NewFlagSet("harbor-devbootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var helper explicitFlag
	var userID explicitFlag
	var groupID explicitFlag
	flags.Var(&helper, "helper", "absolute path to an already-built harbor-helper")
	flags.Var(&userID, "user-id", "non-root pending-ticket owner UID")
	flags.Var(&groupID, "group-id", "pending-ticket owner GID")
	if err := flags.Parse(arguments); err != nil {
		return devbootstrap.Config{}, err
	}
	if flags.NArg() != 0 {
		return devbootstrap.Config{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	for _, value := range []struct {
		name string
		uses int
	}{
		{name: "helper", uses: helper.uses},
		{name: "user-id", uses: userID.uses},
		{name: "group-id", uses: groupID.uses},
	} {
		if value.uses == 0 {
			return devbootstrap.Config{}, fmt.Errorf("--%s is required", value.name)
		}
	}
	if helper.value == "" {
		return devbootstrap.Config{}, fmt.Errorf("--helper must not be empty")
	}
	parsedUserID, err := parseID("user-id", userID.value)
	if err != nil {
		return devbootstrap.Config{}, err
	}
	parsedGroupID, err := parseID("group-id", groupID.value)
	if err != nil {
		return devbootstrap.Config{}, err
	}
	return devbootstrap.Config{HelperSource: helper.value, UserID: parsedUserID, GroupID: parsedGroupID}, nil
}

// parseID accepts one explicit unsigned decimal identity without platform-dependent integer width.
func parseID(name string, value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("--%s %q is not a valid Unix ID: %w", name, value, err)
	}
	if strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("--%s %q is not a canonical decimal Unix ID", name, value)
	}
	if parsed == math.MaxUint32 {
		return 0, fmt.Errorf("--%s %q is reserved by chown", name, value)
	}
	return uint32(parsed), nil
}
