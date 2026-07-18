package domain

import (
	"strings"
	"testing"
)

// TestTypedIdentifiersValidate exercises each ID type through its public validator.
func TestTypedIdentifiersValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "project", validate: func() error { return ProjectID("project-01").Validate() }},
		{name: "App", validate: func() error { return AppID("app").Validate() }},
		{name: "service", validate: func() error { return ServiceID("requirement.database.primary").Validate() }},
		{name: "resource", validate: func() error { return ResourceID("api-reference").Validate() }},
		{name: "operation", validate: func() error { return OperationID("operation-01").Validate() }},
		{name: "intent", validate: func() error { return IntentID("client/intent-01").Validate() }},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestProjectIDValidateRejectsUnsafeValues proves the shared validator remains generation-format neutral while excluding unsafe representations.
func TestProjectIDValidateRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   ProjectID
		want string
	}{
		{name: "empty", id: "", want: "must not be empty"},
		{name: "leading whitespace", id: " project", want: "surrounding whitespace"},
		{name: "trailing whitespace", id: "project ", want: "surrounding whitespace"},
		{name: "control character", id: "project\n01", want: "control characters"},
		{name: "invalid UTF-8", id: ProjectID(string([]byte{0xff})), want: "valid UTF-8"},
		{name: "too long", id: ProjectID(strings.Repeat("a", maximumIdentifierBytes+1)), want: "must not exceed"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.id.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
