// Package main provides the standalone Harbor output broker process.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/goforj/harbor/internal/projectprocess"
)

const (
	configFlag   = "--config"
	stdoutFDFlag = "--stdout-fd"
	stderrFDFlag = "--stderr-fd"
)

// commandArguments identifies the owner-private launch manifest and inherited output descriptors.
type commandArguments struct {
	configPath string
	stdoutFD   int
	stderrFD   int
}

// main clears ambient configuration before accepting only the exact broker launch boundary.
func main() {
	os.Clearenv()
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run consumes the owner-private manifest and transfers inherited pipes to the broker runtime.
func run(ctx context.Context, arguments []string) error {
	parsed, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	config, err := projectprocess.ReadOutputBrokerLaunchConfig(parsed.configPath)
	if err != nil {
		return err
	}
	stdout, err := openInheritedOutputPipe(parsed.stdoutFD, "stdout")
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := openInheritedOutputPipe(parsed.stderrFD, "stderr")
	if err != nil {
		return err
	}
	defer stderr.Close()
	evidence, err := projectprocess.CaptureCurrentProcessEvidence()
	if err != nil {
		return err
	}
	return projectprocess.RunOutputBroker(ctx, projectprocess.OutputBrokerRuntimeConfig{
		ProjectID:         config.ProjectID,
		SessionID:         config.SessionID,
		OutputDirectory:   config.OutputDirectory,
		EndpointReference: config.EndpointReference,
		AttachmentTicket:  config.AttachmentTicket,
		ManifestPath:      parsed.configPath,
		Process:           evidence,
		Stdout:            stdout,
		Stderr:            stderr,
	})
}

// parseArguments accepts one exact broker process argument vector without placing a ticket in argv.
func parseArguments(arguments []string) (commandArguments, error) {
	if len(arguments) != 6 || arguments[0] != configFlag || arguments[2] != stdoutFDFlag || arguments[4] != stderrFDFlag {
		return commandArguments{}, errors.New("output broker requires --config PATH --stdout-fd FD --stderr-fd FD")
	}
	if arguments[1] == "" {
		return commandArguments{}, errors.New("output broker config path is required")
	}
	stdoutFD, err := parseOutputBrokerFD(arguments[3], "stdout")
	if err != nil {
		return commandArguments{}, err
	}
	stderrFD, err := parseOutputBrokerFD(arguments[5], "stderr")
	if err != nil {
		return commandArguments{}, err
	}
	if stdoutFD == stderrFD {
		return commandArguments{}, errors.New("output broker stdout and stderr descriptors must differ")
	}
	return commandArguments{configPath: arguments[1], stdoutFD: stdoutFD, stderrFD: stderrFD}, nil
}

// parseOutputBrokerFD accepts one inherited descriptor/handle number outside the standard streams.
func parseOutputBrokerFD(value, name string) (int, error) {
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil || parsed <= 2 || strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("output broker %s descriptor must be a canonical integer greater than 2", name)
	}
	return int(parsed), nil
}
