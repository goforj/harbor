package main

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maximumFatalDiagnosticDetailBytes bounds one public unified-log field below the workflow's output limit.
const maximumFatalDiagnosticDetailBytes = 384

// fatalExitDiagnostic contains the safe fields emitted to the macOS unified log.
type fatalExitDiagnostic struct {
	phase  string
	detail string
}

// fatalDiagnostic maps a relay error to bounded, non-secret host diagnostics.
func fatalDiagnostic(err error) fatalExitDiagnostic {
	if err == nil {
		return fixedFatalDiagnostic("unknown", "unclassified-failure")
	}

	message := err.Error()
	switch {
	case errorsIsContextTermination(err):
		return fixedFatalDiagnostic("shutdown", "context-terminated")
	case strings.HasPrefix(message, "launchd relay requires the exact owned argument vector"),
		strings.HasPrefix(message, "launchd relay owner UID"),
		strings.HasPrefix(message, "launchd relay policy fingerprint"),
		strings.HasPrefix(message, "launchd relay HTTP upstream"),
		strings.HasPrefix(message, "launchd relay HTTPS upstream"),
		strings.HasPrefix(message, "launchd relay upstreams must be distinct"):
		return fixedFatalDiagnostic("configuration", "configuration-rejected")
	case strings.HasPrefix(message, "determine launchd relay identity:"),
		strings.HasPrefix(message, "launchd relay effective UID"):
		return fixedFatalDiagnostic("identity", "identity-rejected")
	case strings.HasPrefix(message, "capture launchd relay service identity:"),
		strings.HasPrefix(message, "validate launchd relay service identity:"):
		return fixedFatalDiagnostic("service-identity", "service-identity-rejected")
	case strings.HasPrefix(message, "construct launchd ingress relay"):
		return fixedFatalDiagnostic("runtime-construction", "runtime-construction-failed")
	case strings.HasPrefix(message, "activate launchd ingress sockets:"):
		return fatalExitDiagnostic{
			phase:  "socket-activation",
			detail: boundedSingleLine(message),
		}
	case strings.HasPrefix(message, "clear launchd relay ambient environment:"):
		return fixedFatalDiagnostic("environment-cleanup", "environment-cleanup-failed")
	case strings.HasPrefix(message, "serve launchd ingress relay:"):
		return fatalExitDiagnostic{
			phase:  "relay-service",
			detail: boundedSingleLine(message),
		}
	default:
		return fixedFatalDiagnostic("unknown", "unclassified-failure")
	}
}

// fixedFatalDiagnostic keeps phases that may contain configuration or environment values opaque.
func fixedFatalDiagnostic(phase string, detail string) fatalExitDiagnostic {
	return fatalExitDiagnostic{
		phase:  phase,
		detail: detail,
	}
}

// boundedSingleLine removes control characters and limits public log detail to one modest line.
func boundedSingleLine(value string) string {
	var builder strings.Builder
	truncated := false
	for _, character := range value {
		if unicode.IsControl(character) {
			character = ' '
		}
		if builder.Len()+len(string(character)) > maximumFatalDiagnosticDetailBytes {
			truncated = true
			break
		}
		builder.WriteRune(character)
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "unavailable"
	}
	if truncated {
		for len(result)+len("...") > maximumFatalDiagnosticDetailBytes {
			_, size := utf8.DecodeLastRuneInString(result)
			result = result[:len(result)-size]
		}
		return result + "..."
	}
	return result
}

// errorsIsContextTermination identifies ordinary process shutdown without exposing its error text.
func errorsIsContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
