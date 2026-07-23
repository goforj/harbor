package projectprocess

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// validManagedLaunchContext returns one deterministic context with the same shape production launchers will receive.
func validManagedLaunchContext(t *testing.T) ManagedLaunchContext {
	t.Helper()
	root := filepath.Clean(t.TempDir())
	return ManagedLaunchContext{
		SchemaVersion:             ManagedLaunchContextSchemaVersion,
		ProjectID:                 "project-context",
		SessionID:                 "session-context",
		ProjectRoot:               root,
		ExpectedSessionGeneration: 1,
		DescriptorDigest:          strings.Repeat("a", 64),
		EndpointReference:         filepath.Join(string(filepath.Separator), "tmp", "harbord.sock"),
		Owner:                     domain.SessionOwnerHarbor,
		Ticket:                    strings.Repeat("b", 64),
	}
}

// TestManagedLaunchContextValidationRejectsAmbiguousValues keeps launch authority fail-closed at the process boundary.
func TestManagedLaunchContextValidationRejectsAmbiguousValues(t *testing.T) {
	base := validManagedLaunchContext(t)
	tests := []struct {
		name   string
		mutate func(*ManagedLaunchContext)
	}{
		{name: "schema", mutate: func(context *ManagedLaunchContext) { context.SchemaVersion = "managed-launch-context.v2" }},
		{name: "project root", mutate: func(context *ManagedLaunchContext) { context.ProjectRoot = "relative" }},
		{name: "generation", mutate: func(context *ManagedLaunchContext) { context.ExpectedSessionGeneration = 0 }},
		{name: "digest", mutate: func(context *ManagedLaunchContext) { context.DescriptorDigest = strings.Repeat("A", 64) }},
		{name: "endpoint", mutate: func(context *ManagedLaunchContext) { context.EndpointReference = "harbord.sock" }},
		{name: "owner", mutate: func(context *ManagedLaunchContext) { context.Owner = domain.SessionOwnerTerminal }},
		{name: "ticket", mutate: func(context *ManagedLaunchContext) { context.Ticket = "short" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted ambiguous context")
			}
		})
	}
}

// TestWriteManagedLaunchContextUsesOwnerOnlyOneUseFiles verifies the credential never enters the checkout or command environment.
func TestWriteManagedLaunchContextUsesOwnerOnlyOneUseFiles(t *testing.T) {
	context := validManagedLaunchContext(t)
	path, err := writeManagedLaunchContext(context)
	if err != nil {
		t.Fatalf("writeManagedLaunchContext() error = %v", err)
	}
	t.Cleanup(func() { _ = removeManagedLaunchContext(path) })
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat managed launch context: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("managed launch context mode = %v, want regular 0600 file", info.Mode())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read managed launch context: %v", err)
	}
	var decoded ManagedLaunchContext
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode managed launch context: %v", err)
	}
	if decoded != context {
		t.Fatalf("decoded context = %#v, want %#v", decoded, context)
	}
	if strings.Contains(strings.Join(withDevelopmentEnvironment([]string{"FORJ_INTERNAL_MANAGED_CONTEXT=ambient"}, EnvironmentOverrides{}, path), "\x00"), context.Ticket) {
		t.Fatal("managed launch ticket appeared in child environment")
	}
	if err := removeManagedLaunchContext(path); err != nil {
		t.Fatalf("removeManagedLaunchContext() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed launch context stat after removal = %v, want not exist", err)
	}
}

// TestWithDevelopmentEnvironmentReplacesAmbientContext verifies a caller cannot override Harbor's reserved context reference.
func TestWithDevelopmentEnvironmentReplacesAmbientContext(t *testing.T) {
	path := filepath.Join(string(filepath.Separator), "tmp", "managed-launch.json")
	result := withDevelopmentEnvironment(
		[]string{"FORJ_INTERNAL_MANAGED_CONTEXT=ambient", "UNRELATED=preserved"},
		EnvironmentOverrides{},
		path,
	)
	if got := strings.Join(result, "|"); got != "UNRELATED=preserved|FORJ_DEV_PLAIN=1|FORJ_BUILD_ENV_OVERRIDES=|FORJ_INTERNAL_MANAGED_CONTEXT="+path {
		t.Fatalf("withDevelopmentEnvironment() = %q", got)
	}
}
