//go:build linux

package projectprocess

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/domain"
)

const (
	maximumOutputBrokerProcessArgumentsBytes = 16 * 1024
	maximumOutputBrokerProcessArguments      = 32
)

// observePersistedOutputBrokerProcessEvidence reads Linux procfs identity without trusting request-supplied argv.
func observePersistedOutputBrokerProcessEvidence(pid int64) (domain.ProcessEvidence, error) {
	if pid <= 0 || int64(int(pid)) != pid {
		return domain.ProcessEvidence{}, fmt.Errorf("output broker process PID %d is outside this platform's range", pid)
	}
	processPID := int(pid)
	birthToken, present, err := observeProcessBirthToken(processPID)
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("observe output broker process birth: %w", err)
	}
	if !present {
		return domain.ProcessEvidence{}, fmt.Errorf("observe output broker process birth: %w", os.ErrNotExist)
	}
	executableLink, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", processPID))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("read output broker executable: %w", err)
	}
	if strings.HasSuffix(executableLink, " (deleted)") {
		return domain.ProcessEvidence{}, errors.New("output broker executable image was deleted")
	}
	executable, err := canonicalExecutable(filepath.Clean(executableLink))
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("canonicalize output broker executable: %w", err)
	}
	arguments, err := readLinuxProcessArguments(processPID)
	if err != nil {
		return domain.ProcessEvidence{}, err
	}
	finalBirthToken, finalPresent, err := observeProcessBirthToken(processPID)
	if err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("reobserve output broker process birth: %w", err)
	}
	if !finalPresent || finalBirthToken != birthToken {
		return domain.ProcessEvidence{}, errors.New("output broker process identity changed during observation")
	}
	evidence := domain.ProcessEvidence{
		PID:                pid,
		BirthToken:         birthToken,
		ExecutableIdentity: executable,
		ArgumentDigest:     digestArguments(arguments),
	}
	if err := evidence.Validate(); err != nil {
		return domain.ProcessEvidence{}, fmt.Errorf("validate observed output broker process evidence: %w", err)
	}
	return evidence, nil
}

// readLinuxProcessArguments reduces procfs command-line bytes before they can become durable authority.
func readLinuxProcessArguments(pid int) ([]string, error) {
	body, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, fmt.Errorf("read output broker arguments: %w", err)
	}
	if len(body) == 0 {
		return nil, errors.New("read output broker arguments: argument vector is empty")
	}
	if len(body) > maximumOutputBrokerProcessArgumentsBytes {
		return nil, fmt.Errorf("read output broker arguments: command line exceeds %d bytes", maximumOutputBrokerProcessArgumentsBytes)
	}
	if body[len(body)-1] != 0 {
		return nil, errors.New("read output broker arguments: procfs record is not NUL terminated")
	}
	parts := strings.Split(string(body[:len(body)-1]), "\x00")
	if len(parts) == 0 {
		return nil, errors.New("read output broker arguments: argument vector is empty")
	}
	if len(parts) > maximumOutputBrokerProcessArguments {
		return nil, fmt.Errorf("read output broker arguments: argument count exceeds %d", maximumOutputBrokerProcessArguments)
	}
	for index, argument := range parts {
		if argument == "" {
			return nil, fmt.Errorf("read output broker arguments: argument %d is empty", index)
		}
	}
	return parts, nil
}
