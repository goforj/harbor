// Command platformproof executes Harbor's operating-system capability proofs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strings"

	"github.com/goforj/harbor/internal/platformproof"
)

const defaultProofAddresses = "127.77.254.10,127.77.254.11,127.77.254.12"

// commandOptions contains the common evidence identity accepted by each proof command.
type commandOptions struct {
	addresses []netip.Addr
	port      uint16
}

// main executes one bounded proof and reports machine-readable evidence on standard output.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "platform proof failed: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches only the fixed proof commands built into this test binary.
func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("expected project-identity, identity-absent, or verify command")
	}

	switch arguments[0] {
	case "project-identity":
		options, err := parseOptions("project-identity", arguments[1:], true)
		if err != nil {
			return err
		}
		evidence, err := platformproof.ProveProjectIdentities(ctx, proofRequest(options))
		if err != nil {
			return err
		}
		return writeEvidence(evidence)
	case "identity-absent":
		options, err := parseOptions("identity-absent", arguments[1:], false)
		if err != nil {
			return err
		}
		evidence, err := platformproof.ProveIdentitiesAbsent(proofRequest(options))
		if err != nil {
			return err
		}
		return writeEvidence(evidence)
	case "verify":
		return verifyEvidence(arguments[1:])
	default:
		return fmt.Errorf("unknown platform proof command %q", arguments[0])
	}
}

// verifyEvidence enforces that all required platform artifacts came from this commit and proved the native port.
func verifyEvidence(arguments []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "", "directory containing downloaded platform evidence")
	commit := flags.String("commit", "", "exact commit required in every artifact")
	platforms := flags.String("platforms", "linux,darwin,windows", "comma-separated GOOS values required by the gate")
	port := flags.Uint("port", 3306, "native port required by the gate")
	requireRunnerIdentity := flags.Bool("require-runner-identity", false, "require hosted runner image metadata")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *root == "" || *port == 0 || *port > 65535 {
		return errors.New("verify requires an evidence root and a valid native port")
	}
	requiredPlatforms := splitNonEmpty(*platforms)
	return platformproof.VerifyEvidenceDirectory(*root, platformproof.EvidenceRequirement{
		Commit:                *commit,
		Platforms:             requiredPlatforms,
		Port:                  uint16(*port),
		RequireRunnerIdentity: *requireRunnerIdentity,
	})
}

// parseOptions validates addresses and the fixed native port before a proof begins.
func parseOptions(name string, arguments []string, requirePort bool) (commandOptions, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	addressValue := flags.String("addresses", defaultProofAddresses, "three comma-separated IPv4 loopback identities")
	portValue := flags.Uint("port", 3306, "native TCP port shared by all identities")
	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	addresses, err := platformproof.ParseAddresses(*addressValue)
	if err != nil {
		return commandOptions{}, err
	}
	if *portValue > 65535 || (requirePort && *portValue == 0) {
		return commandOptions{}, fmt.Errorf("invalid native port %d", *portValue)
	}
	return commandOptions{addresses: addresses, port: uint16(*portValue)}, nil
}

// proofRequest records the exact CI environment alongside the proof result.
func proofRequest(options commandOptions) platformproof.ProjectIdentityRequest {
	return platformproof.ProjectIdentityRequest{
		Addresses:          options.addresses,
		Port:               options.port,
		Commit:             os.Getenv("GITHUB_SHA"),
		RunnerName:         os.Getenv("RUNNER_NAME"),
		RunnerImage:        firstEnvironmentValue("ImageOS", "RUNNER_OS"),
		RunnerImageVersion: os.Getenv("ImageVersion"),
	}
}

// firstEnvironmentValue preserves the hosted image identity when GitHub exposes it.
func firstEnvironmentValue(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// splitNonEmpty normalizes the fixed platform list without accepting empty requirements.
func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.TrimSpace(part); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

// writeEvidence emits one indented JSON document and no presentation text.
func writeEvidence(evidence any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(evidence); err != nil {
		return fmt.Errorf("encode proof evidence: %w", err)
	}
	return nil
}
