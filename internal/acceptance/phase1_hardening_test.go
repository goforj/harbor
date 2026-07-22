//go:build phase1acceptance

package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// phase1TestEnvironmentMap parses deterministic child environments for focused boundary assertions.
func phase1TestEnvironmentMap(t *testing.T, environment []string, caseInsensitive bool) map[string]string {
	t.Helper()
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			t.Fatalf("invalid environment entry %q", entry)
		}
		result[phase1EnvironmentKey(key, caseInsensitive)] = value
	}
	return result
}

// phase1TestEvidence creates a focused writer with every static daemon log initialized.
func phase1TestEvidence(directory string) *phase1Evidence {
	logs := make(map[string]*phase1BoundedLog, len(phase1EvidenceLogNames))
	for _, name := range phase1EvidenceLogNames {
		logs[name] = new(phase1BoundedLog)
	}
	return &phase1Evidence{directory: directory, logs: logs}
}

// TestPhase1ChildEnvironmentFiltersInheritedState verifies production children cannot inherit test controls or ambient secrets.
func TestPhase1ChildEnvironmentFiltersInheritedState(t *testing.T) {
	base := []string{
		"PATH=/usr/local/bin:/usr/bin",
		"LANG=en_US.UTF-8",
		"HARBOR_PHASE1_CLI_BINARY=/tmp/parent-harbor",
		"APP_KEY=base64:parent-secret",
		"APP_UNRELATED=parent-value",
		"DB_PASSWORD=parent-password",
		"FORJ_DEBUG=parent-debug",
		"LIGHTHOUSE_TOKEN=parent-token",
		"GITHUB_TOKEN=github-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"MALFORMED",
	}
	overrides := map[string]string{
		"APP_NAME":   "harbor",
		"DB_DRIVER":  "sqlite",
		"FORJ_DEBUG": "",
		"GORACE":     "halt_on_error=1",
		"HOME":       "/sandbox/home",
	}
	environment := phase1TestEnvironmentMap(
		t,
		phase1ChildEnvironmentForPlatform(base, overrides, false),
		false,
	)

	wanted := map[string]string{
		"APP_NAME":   "harbor",
		"DB_DRIVER":  "sqlite",
		"FORJ_DEBUG": "",
		"GORACE":     "halt_on_error=1",
		"HOME":       "/sandbox/home",
		"LANG":       "en_US.UTF-8",
		"PATH":       "/usr/local/bin:/usr/bin",
	}
	if len(environment) != len(wanted) {
		t.Fatalf("child environment = %#v, want only %#v", environment, wanted)
	}
	for key, value := range wanted {
		if environment[key] != value {
			t.Fatalf("child environment %s = %q, want %q", key, environment[key], value)
		}
	}
	for _, forbidden := range []string{
		"HARBOR_PHASE1_CLI_BINARY",
		"APP_KEY",
		"APP_UNRELATED",
		"DB_PASSWORD",
		"LIGHTHOUSE_TOKEN",
		"GITHUB_TOKEN",
		"AWS_SECRET_ACCESS_KEY",
	} {
		if _, found := environment[forbidden]; found {
			t.Fatalf("child environment retained forbidden inherited key %s", forbidden)
		}
	}
}

// TestPhase1ChildEnvironmentUsesWindowsKeyEquality verifies an override cannot leave a differently cased inherited duplicate.
func TestPhase1ChildEnvironmentUsesWindowsKeyEquality(t *testing.T) {
	environment := phase1TestEnvironmentMap(
		t,
		phase1ChildEnvironmentForPlatform(
			[]string{"Path=C:\\parent", "systemroot=C:\\Windows", "App_Key=secret"},
			map[string]string{"PATH": `C:\sandbox`, "SYSTEMROOT": `C:\Windows`},
			true,
		),
		true,
	)
	if len(environment) != 2 {
		t.Fatalf("Windows child environment = %#v, want two reviewed keys", environment)
	}
	if environment["PATH"] != `C:\sandbox` || environment["SYSTEMROOT"] != `C:\Windows` {
		t.Fatalf("Windows child environment = %#v, want explicit overrides", environment)
	}
	if _, found := environment["APP_KEY"]; found {
		t.Fatal("Windows child environment retained differently cased APP_KEY")
	}
}

// TestPhase1EvidenceRemovesAndRejectsUnexpectedEntries verifies the upload root ends as the exact direct-file allowlist.
func TestPhase1EvidenceRemovesAndRejectsUnexpectedEntries(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ambient-secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write unexpected evidence file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0o700); err != nil {
		t.Fatalf("create unexpected evidence directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "summary.json"), 0o700); err != nil {
		t.Fatalf("create invalid allowlisted entry: %v", err)
	}
	evidence := phase1TestEvidence(directory)
	evidence.logs["unreviewed-daemon"] = new(phase1BoundedLog)

	err := evidence.write()
	for _, message := range []string{"ambient-secret.txt", "nested", "summary.json", "unreviewed-daemon"} {
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("evidence write error = %v, want rejected entry %q", err, message)
		}
	}
	if err := phase1VerifyEvidenceDirectory(directory, phase1EvidenceFilenames()); err != nil {
		t.Fatalf("verify hardened evidence directory: %v", err)
	}
	for _, name := range []string{"ambient-secret.txt", "nested", "unreviewed-daemon.log"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected evidence entry %q remains: %v", name, err)
		}
	}
}

// TestPhase1EvidenceReplacesAllowlistedSymlink verifies an allowlisted name cannot redirect artifact writes.
func TestPhase1EvidenceReplacesAllowlistedSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside-summary")
	if err := os.WriteFile(target, []byte("outside remains unchanged"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "summary.json")); err != nil {
		t.Skipf("create evidence symlink on this host: %v", err)
	}

	err := phase1TestEvidence(directory).write()
	if err == nil || !strings.Contains(err.Error(), "summary.json") {
		t.Fatalf("evidence write error = %v, want symlink rejection", err)
	}
	if err := phase1VerifyEvidenceDirectory(directory, phase1EvidenceFilenames()); err != nil {
		t.Fatalf("verify evidence after symlink replacement: %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(contents) != "outside remains unchanged" {
		t.Fatalf("symlink target contents = %q, want unchanged", contents)
	}
}

// TestPhase1EvidenceFailsOnDaemonRaceReport verifies a race-runtime crash cannot masquerade as the deliberate hard kill.
func TestPhase1EvidenceFailsOnDaemonRaceReport(t *testing.T) {
	evidence := phase1TestEvidence(t.TempDir())
	if _, err := evidence.logs[phase1FirstDaemonLogName].Write([]byte("WARNING: DATA RACE\nstack\n")); err != nil {
		t.Fatalf("write race diagnostic: %v", err)
	}
	err := evidence.write()
	if err == nil || !strings.Contains(err.Error(), "data race report") {
		t.Fatalf("evidence write error = %v, want race-report gate", err)
	}
	if err := phase1VerifyEvidenceDirectory(evidence.directory, phase1EvidenceFilenames()); err != nil {
		t.Fatalf("verify diagnostic evidence after race failure: %v", err)
	}
}

// TestPhase1WindowsRedactionIgnoresPathCaseAndRemovesPipeSID verifies simulated Windows logs cannot expose endpoint identity.
func TestPhase1WindowsRedactionIgnoresPathCaseAndRemovesPipeSID(t *testing.T) {
	endpoint := `\\.\pipe\goforj-harbor-S-1-5-21-1000-2000-3000-4000`
	replacements := phase1EndpointRedactions(endpoint)
	if len(replacements) != 2 || replacements[1] != "S-1-5-21-1000-2000-3000-4000" {
		t.Fatalf("endpoint redactions = %v, want pipe and SID", replacements)
	}
	contents := strings.Join([]string{
		`pipe=\\.\PIPE\GOFORJ-HARBOR-s-1-5-21-1000-2000-3000-4000`,
		`owner=s-1-5-21-1000-2000-3000-4000`,
	}, "\n")
	redacted := phase1RedactLogForPlatform(contents, replacements, true)
	if strings.Contains(strings.ToLower(redacted), "s-1-5-21-1000-2000-3000-4000") {
		t.Fatalf("Windows redaction retained the SID: %q", redacted)
	}
	if strings.Count(redacted, "<sandbox>") != 2 {
		t.Fatalf("Windows redaction = %q, want pipe and standalone SID redacted", redacted)
	}
}

// TestPhase1ActiveOperationDiagnosticExcludesOperationIdentity verifies setup failure output retains only allowlisted active-operation fields.
func TestPhase1ActiveOperationDiagnosticExcludesOperationIdentity(t *testing.T) {
	evidence := &phase1Evidence{replacements: []string{"/private/sandbox"}}
	snapshot := domain.Snapshot{
		Sequence: 19,
		Operations: []domain.Operation{
			{
				ID:       "operation-sensitive",
				IntentID: "intent-sensitive",
				Kind:     domain.OperationKindNetworkSetup,
				State:    domain.OperationRunning,
				Phase:    "copy /private/sandbox APP_KEY=secret-value",
			},
			{
				ID:       "operation-terminal",
				IntentID: "intent-terminal",
				Kind:     domain.OperationKindNetworkResolverSetup,
				State:    domain.OperationSucceeded,
				Phase:    "complete",
			},
		},
	}

	diagnostic := phase1ActiveOperationDiagnostic(snapshot, evidence)
	for _, forbidden := range []string{
		"operation-sensitive",
		"intent-sensitive",
		"operation-terminal",
		"intent-terminal",
		"/private/sandbox",
		"secret-value",
		"network.resolver.setup",
	} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("active operation diagnostic exposed %q: %s", forbidden, diagnostic)
		}
	}
	for _, expected := range []string{
		"kind=network.setup",
		"state=running",
		`phase="copy <sandbox> APP_KEY= <redacted>"`,
		"snapshot_sequence=19",
	} {
		if !strings.Contains(diagnostic, expected) {
			t.Fatalf("active operation diagnostic = %q, want %q", diagnostic, expected)
		}
	}
}
