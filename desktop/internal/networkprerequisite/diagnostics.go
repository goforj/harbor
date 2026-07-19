package networkprerequisite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maximumDarwinDevelopmentAuthorizationDetailBytes = 512

const maximumDarwinDevelopmentAuthorizationOutputBytes = maximumDarwinDevelopmentAuthorizationDetailBytes + 64

// darwinDevelopmentAuthorizationOutput retains enough of the fixed result envelope and visible detail without trusting native output volume.
type darwinDevelopmentAuthorizationOutput struct {
	content   []byte
	truncated bool
}

// Write accepts complete process writes while retaining only the reviewed diagnostic budget.
func (output *darwinDevelopmentAuthorizationOutput) Write(value []byte) (int, error) {
	written := len(value)
	remaining := maximumDarwinDevelopmentAuthorizationOutputBytes - len(output.content)
	if remaining <= 0 {
		output.truncated = output.truncated || len(value) != 0
		return written, nil
	}
	if len(value) > remaining {
		output.truncated = true
		value = value[:remaining]
	}
	output.content = append(output.content, value...)
	return written, nil
}

// String returns the retained native output for protocol parsing and diagnostics.
func (output *darwinDevelopmentAuthorizationOutput) String() string {
	content := output.content
	if output.truncated {
		for len(content) != 0 && !utf8.Valid(content) {
			content = content[:len(content)-1]
		}
	}
	return string(content)
}

// parseDarwinDevelopmentAuthorizationResult accepts only the fixed AppleScript result grammar.
func parseDarwinDevelopmentAuthorizationResult(output string) error {
	result := strings.TrimSpace(output)
	switch result {
	case "succeeded":
		return nil
	case "declined":
		return ErrDeclined
	}
	if failure, found := strings.CutPrefix(result, "failed|"); found {
		status, detail, valid := strings.Cut(failure, "|")
		if !valid {
			return errors.New("privileged networking installation returned an invalid macOS authorization failure")
		}
		return darwinDevelopmentAuthorizationFailure(status, detail)
	}

	return errors.New("privileged networking installation returned an invalid macOS authorization result")
}

// darwinDevelopmentAuthorizationLaunchFailure keeps a native exit status actionable when osascript produced no diagnostics.
func darwinDevelopmentAuthorizationLaunchFailure(cause error, output string) error {
	if strings.TrimSpace(output) == "" {
		output = cause.Error()
	}
	return darwinDevelopmentAuthorizationFailure("launch", output)
}

// darwinDevelopmentAuthorizationFailure keeps fixed-bootstrap diagnostics visible without allowing an unbounded native response into the UI.
func darwinDevelopmentAuthorizationFailure(status string, output string) error {
	status = boundedDarwinDevelopmentAuthorizationStatus(status)
	detail := sanitizeDarwinDevelopmentAuthorizationDetail(output)
	if len(detail) > maximumDarwinDevelopmentAuthorizationDetailBytes {
		detail = detail[:maximumDarwinDevelopmentAuthorizationDetailBytes-len("…")]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
		detail += "…"
	}
	if detail == "" {
		return fmt.Errorf("%w: macOS authorization %s failed without diagnostics", ErrFailed, status)
	}

	return fmt.Errorf("%w: macOS authorization %s: %s", ErrFailed, status, detail)
}

// sanitizeDarwinDevelopmentAuthorizationDetail flattens native diagnostics and removes characters that can alter UI presentation.
func sanitizeDarwinDevelopmentAuthorizationDetail(output string) string {
	if !utf8.ValidString(output) {
		return ""
	}
	detail := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return character
	}, output)
	return strings.Join(strings.Fields(detail), " ")
}

// boundedDarwinDevelopmentAuthorizationStatus admits only the fixed launch label or a canonical AppleScript error number.
func boundedDarwinDevelopmentAuthorizationStatus(status string) string {
	if status == "launch" {
		return status
	}
	parsed, err := strconv.ParseInt(status, 10, 32)
	if err != nil || strconv.FormatInt(parsed, 10) != status {
		return "unknown"
	}
	return status
}
