package domain

import (
	"strings"
	"testing"
)

// TestProblemValidate accepts stable machine and human representations together.
func TestProblemValidate(t *testing.T) {
	t.Parallel()

	problem := Problem{Code: "project_not_found", Message: "The project is no longer registered.", Retryable: false}
	if err := problem.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestProblemValidateRejectsInvalidFields covers both bounded problem fields.
func TestProblemValidateRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		problem Problem
	}{
		{name: "empty code", problem: Problem{Message: "A message."}},
		{name: "invalid code encoding", problem: Problem{Code: ProblemCode(string([]byte{0xff})), Message: "A message."}},
		{name: "spaced code", problem: Problem{Code: " conflict ", Message: "A message."}},
		{name: "control in code", problem: Problem{Code: "conflict\n", Message: "A message."}},
		{name: "long code", problem: Problem{Code: ProblemCode(strings.Repeat("a", maximumProblemCodeBytes+1)), Message: "A message."}},
		{name: "empty message", problem: Problem{Code: "conflict"}},
		{name: "invalid message encoding", problem: Problem{Code: "conflict", Message: string([]byte{0xff})}},
		{name: "spaced message", problem: Problem{Code: "conflict", Message: " A message. "}},
		{name: "control in message", problem: Problem{Code: "conflict", Message: "A message.\n"}},
		{name: "long message", problem: Problem{Code: "conflict", Message: strings.Repeat("a", maximumProblemMessageBytes+1)}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.problem.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid problem error")
			}
		})
	}
}
