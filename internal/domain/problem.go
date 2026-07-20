package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maximumProblemCodeBytes    = 128
	maximumProblemMessageBytes = 4096
)

const (
	// ProjectRecoveryAmbiguousLaunchProblemCode identifies a legacy launch whose exact process identity was not persisted.
	ProjectRecoveryAmbiguousLaunchProblemCode ProblemCode = "project.recovery.ambiguous_launch"
	// ProjectRecoveryIsolationPhase identifies the transition that withholds a runtime with unresolved process authority.
	ProjectRecoveryIsolationPhase = "isolating unresolved process authority"
	// ProjectRecoveryRequiredPhase identifies the terminal marker that retains unresolved runtime authority for repair.
	ProjectRecoveryRequiredPhase = "recovery required"
)

// ProblemCode is a stable machine-readable classification for an operation failure.
type ProblemCode string

// Problem describes an actionable failure without exposing implementation details.
type Problem struct {
	Code      ProblemCode `json:"code"`
	Message   string      `json:"message"`
	Retryable bool        `json:"retryable"`
}

// Validate reports whether the problem has a bounded code and human-readable message.
func (problem Problem) Validate() error {
	code := string(problem.Code)
	if code == "" {
		return fmt.Errorf("problem code must not be empty")
	}
	if !utf8.ValidString(code) {
		return fmt.Errorf("problem code must be valid UTF-8")
	}
	if strings.TrimSpace(code) != code {
		return fmt.Errorf("problem code must not contain surrounding whitespace")
	}
	if containsControlCharacter(code) {
		return fmt.Errorf("problem code must not contain control characters")
	}
	if len(code) > maximumProblemCodeBytes {
		return fmt.Errorf("problem code must not exceed %d bytes", maximumProblemCodeBytes)
	}

	if problem.Message == "" {
		return fmt.Errorf("problem message must not be empty")
	}
	if !utf8.ValidString(problem.Message) {
		return fmt.Errorf("problem message must be valid UTF-8")
	}
	if strings.TrimSpace(problem.Message) != problem.Message {
		return fmt.Errorf("problem message must not contain surrounding whitespace")
	}
	if containsControlCharacter(problem.Message) {
		return fmt.Errorf("problem message must not contain control characters")
	}
	if len(problem.Message) > maximumProblemMessageBytes {
		return fmt.Errorf("problem message must not exceed %d bytes", maximumProblemMessageBytes)
	}
	return nil
}
