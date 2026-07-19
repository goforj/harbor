package networkprerequisite

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestDarwinDevelopmentAuthorizationOutputHardCapsNativeWrites verifies discarded bytes cannot turn process success into an I/O failure.
func TestDarwinDevelopmentAuthorizationOutputHardCapsNativeWrites(t *testing.T) {
	t.Parallel()

	output := &darwinDevelopmentAuthorizationOutput{}
	input := strings.Repeat("x", maximumDarwinDevelopmentAuthorizationOutputBytes+128)
	written, err := output.Write([]byte(input))
	if err != nil || written != len(input) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", written, err, len(input))
	}
	if got := len(output.String()); got != maximumDarwinDevelopmentAuthorizationOutputBytes {
		t.Fatalf("retained output contains %d bytes, want %d", got, maximumDarwinDevelopmentAuthorizationOutputBytes)
	}
	written, err = output.Write([]byte("discarded"))
	if err != nil || written != len("discarded") || len(output.String()) != maximumDarwinDevelopmentAuthorizationOutputBytes {
		t.Fatalf("full Write() = (%d, %v, %d retained)", written, err, len(output.String()))
	}

	output = &darwinDevelopmentAuthorizationOutput{}
	unicodeInput := "failed|1|" + strings.Repeat("🚢", maximumDarwinDevelopmentAuthorizationOutputBytes)
	if _, err := output.Write([]byte(unicodeInput)); err != nil {
		t.Fatalf("Write(Unicode) error = %v", err)
	}
	if !utf8.ValidString(output.String()) {
		t.Fatalf("retained Unicode output = %q, want valid UTF-8", output.String())
	}
	if err := parseDarwinDevelopmentAuthorizationResult(output.String()); !errors.Is(err, ErrFailed) {
		t.Fatalf("parse retained Unicode output error = %v, want ErrFailed", err)
	}
}

// TestParseDarwinDevelopmentAuthorizationResultCoversFixedGrammar pins every accepted and rejected AppleScript result shape.
func TestParseDarwinDevelopmentAuthorizationResultCoversFixedGrammar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		want     error
		contains string
	}{
		{name: "succeeded", output: "succeeded\n"},
		{name: "declined", output: "declined", want: ErrDeclined},
		{name: "failed", output: "failed|1|bootstrap failed|with detail", want: ErrFailed, contains: "authorization 1: bootstrap failed|with detail"},
		{name: "malformed failure", output: "failed|1", contains: "invalid macOS authorization failure"},
		{name: "unexpected result", output: "complete", contains: "invalid macOS authorization result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := parseDarwinDevelopmentAuthorizationResult(test.output)
			if test.want == nil && test.contains == "" {
				if err != nil {
					t.Fatalf("parse result error = %v", err)
				}
				return
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("parse result error = %v, want %v", err, test.want)
			}
			if test.contains != "" && (err == nil || !strings.Contains(err.Error(), test.contains)) {
				t.Fatalf("parse result error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

// TestDarwinDevelopmentAuthorizationLaunchFailurePreservesExitStatus keeps empty osascript failures distinguishable in the desktop.
func TestDarwinDevelopmentAuthorizationLaunchFailurePreservesExitStatus(t *testing.T) {
	t.Parallel()

	err := darwinDevelopmentAuthorizationLaunchFailure(errors.New("exit status 17"), "\n")
	if !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "macOS authorization launch: exit status 17") {
		t.Fatalf("launch failure = %v", err)
	}

	err = darwinDevelopmentAuthorizationLaunchFailure(errors.New("exit status 17"), "native diagnostic")
	if !strings.Contains(err.Error(), "native diagnostic") || strings.Contains(err.Error(), "exit status 17") {
		t.Fatalf("launch failure with output = %v", err)
	}
}

// TestDarwinDevelopmentAuthorizationFailurePreservesBoundedVisibleDiagnostics keeps source bootstrap failures actionable in the desktop.
func TestDarwinDevelopmentAuthorizationFailurePreservesBoundedVisibleDiagnostics(t *testing.T) {
	t.Parallel()

	err := darwinDevelopmentAuthorizationFailure("1", "bootstrap failed:\nunsafe directory")
	if !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "macOS authorization 1: bootstrap failed: unsafe directory") {
		t.Fatalf("authorization failure = %v", err)
	}

	long := darwinDevelopmentAuthorizationFailure("-1743", strings.Repeat("é", maximumDarwinDevelopmentAuthorizationDetailBytes))
	if !utf8.ValidString(long.Error()) || !strings.HasSuffix(long.Error(), "…") {
		t.Fatalf("bounded authorization failure = %q", long)
	}
	if detail := strings.TrimPrefix(long.Error(), ErrFailed.Error()+": macOS authorization -1743: "); len(detail) > maximumDarwinDevelopmentAuthorizationDetailBytes {
		t.Fatalf("authorization detail contains %d bytes, want at most %d", len(detail), maximumDarwinDevelopmentAuthorizationDetailBytes)
	}
}

// TestDarwinDevelopmentAuthorizationFailureRejectsInvalidNativeText keeps malformed process output out of the Wails error surface.
func TestDarwinDevelopmentAuthorizationFailureRejectsInvalidNativeText(t *testing.T) {
	t.Parallel()

	err := darwinDevelopmentAuthorizationFailure("forged\nstatus", string([]byte{0xff}))
	if !errors.Is(err, ErrFailed) || strings.Contains(err.Error(), "forged") || !strings.Contains(err.Error(), "authorization unknown failed without diagnostics") {
		t.Fatalf("invalid authorization failure = %q", err)
	}
}

// TestDarwinDevelopmentAuthorizationFailureSanitizesUnsafePresentationCharacters keeps native text from controlling the UI surface.
func TestDarwinDevelopmentAuthorizationFailureSanitizesUnsafePresentationCharacters(t *testing.T) {
	t.Parallel()

	err := darwinDevelopmentAuthorizationFailure("1", "before\x00after\x1b[31m\u202esecret\u2028next")
	detail := err.Error()
	if !strings.Contains(detail, "before after [31m secret next") {
		t.Fatalf("sanitized authorization failure = %q", err)
	}
	for _, character := range detail {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Fatalf("sanitized authorization failure contains unsafe character %U", character)
		}
	}
}
