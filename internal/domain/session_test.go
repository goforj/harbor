package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjectSessionValidateAcceptsEveryLifecycleShape proves only planned sessions may omit process authority.
func TestProjectSessionValidateAcceptsEveryLifecycleShape(t *testing.T) {
	for _, state := range []SessionState{SessionPlanned, SessionAwaitingAttach, SessionAttached, SessionStopping, SessionDisconnected} {
		session := validProjectSession(t)
		session.State = state
		if state == SessionPlanned {
			session.Process = nil
		}
		if err := session.Validate(); err != nil {
			t.Fatalf("ProjectSession{%s}.Validate() error = %v", state, err)
		}
	}
}

// TestProjectSessionValidateRejectsInvalidSessionFacts covers every identity, lifecycle, digest, generation, and time boundary.
func TestProjectSessionValidateRejectsInvalidSessionFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectSession)
		want   string
	}{
		{name: "session ID", mutate: func(session *ProjectSession) { session.ID = "" }, want: "session ID"},
		{name: "project ID", mutate: func(session *ProjectSession) { session.ProjectID = " project" }, want: "project ID"},
		{name: "owner", mutate: func(session *ProjectSession) { session.Owner = "desktop" }, want: "session owner"},
		{name: "state", mutate: func(session *ProjectSession) { session.State = "ready" }, want: "session state"},
		{name: "descriptor digest length", mutate: func(session *ProjectSession) { session.DescriptorDigest = "abc" }, want: "descriptor digest"},
		{name: "descriptor digest uppercase", mutate: func(session *ProjectSession) { session.DescriptorDigest = strings.Repeat("A", 64) }, want: "descriptor digest"},
		{name: "credential digest alphabet", mutate: func(session *ProjectSession) { session.CredentialDigest = strings.Repeat("g", 64) }, want: "credential digest"},
		{name: "generation", mutate: func(session *ProjectSession) { session.Generation = 0 }, want: "generation"},
		{name: "planned process", mutate: func(session *ProjectSession) { session.State = SessionPlanned }, want: "must not contain process"},
		{name: "attached process", mutate: func(session *ProjectSession) { session.Process = nil }, want: "must contain process"},
		{name: "creation zero", mutate: func(session *ProjectSession) { session.CreatedAt = time.Time{} }, want: "creation time"},
		{name: "creation zone", mutate: func(session *ProjectSession) { session.CreatedAt = session.CreatedAt.In(time.FixedZone("UTC-like", 0)) }, want: "canonical UTC"},
		{name: "update zero", mutate: func(session *ProjectSession) { session.UpdatedAt = time.Time{} }, want: "update time"},
		{name: "time order", mutate: func(session *ProjectSession) { session.UpdatedAt = session.CreatedAt.Add(-time.Nanosecond) }, want: "must not precede"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := validProjectSession(t)
			test.mutate(&session)
			if err := session.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ProjectSession.Validate() error = %v, want text %q", err, test.want)
			}
		})
	}
}

// TestProjectSessionValidateRejectsMonotonicTimes keeps in-memory clock metadata out of durable comparisons.
func TestProjectSessionValidateRejectsMonotonicTimes(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previousLocal })

	session := validProjectSession(t)
	session.CreatedAt = time.Now()
	session.UpdatedAt = session.CreatedAt
	if err := session.Validate(); err == nil || !strings.Contains(err.Error(), "monotonic") {
		t.Fatalf("ProjectSession.Validate() monotonic error = %v", err)
	}
}

// TestProcessEvidenceValidateRejectsUnsafeAuthority covers every fact used before Harbor may signal a process.
func TestProcessEvidenceValidateRejectsUnsafeAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProcessEvidence)
		want   string
	}{
		{name: "PID", mutate: func(evidence *ProcessEvidence) { evidence.PID = 0 }, want: "PID"},
		{name: "empty birth token", mutate: func(evidence *ProcessEvidence) { evidence.BirthToken = "" }, want: "birth token"},
		{name: "invalid birth token UTF-8", mutate: func(evidence *ProcessEvidence) { evidence.BirthToken = string([]byte{0xff}) }, want: "valid UTF-8"},
		{name: "birth token whitespace", mutate: func(evidence *ProcessEvidence) { evidence.BirthToken = " birth" }, want: "surrounding whitespace"},
		{name: "birth token control", mutate: func(evidence *ProcessEvidence) { evidence.BirthToken = "birth\x00token" }, want: "control characters"},
		{name: "birth token bound", mutate: func(evidence *ProcessEvidence) {
			evidence.BirthToken = strings.Repeat("b", maximumProcessBirthTokenBytes+1)
		}, want: "must not exceed"},
		{name: "relative executable", mutate: func(evidence *ProcessEvidence) { evidence.ExecutableIdentity = "forj" }, want: "canonical absolute"},
		{name: "unclean executable", mutate: func(evidence *ProcessEvidence) {
			evidence.ExecutableIdentity = filepath.Dir(evidence.ExecutableIdentity) + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(evidence.ExecutableIdentity)
		}, want: "canonical absolute"},
		{name: "executable bound", mutate: func(evidence *ProcessEvidence) {
			evidence.ExecutableIdentity = string(filepath.Separator) + strings.Repeat("e", maximumExecutableIdentityBytes)
		}, want: "must not exceed"},
		{name: "argument digest", mutate: func(evidence *ProcessEvidence) { evidence.ArgumentDigest = strings.Repeat("Z", 64) }, want: "argument digest"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := *validProjectSession(t).Process
			test.mutate(&evidence)
			if err := evidence.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ProcessEvidence.Validate() error = %v, want text %q", err, test.want)
			}
		})
	}
}

// validProjectSession returns one attached session whose security facts are canonical on the current platform.
func validProjectSession(t *testing.T) ProjectSession {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	at := time.Date(2026, 7, 19, 5, 45, 0, 0, time.UTC)
	return ProjectSession{
		ID:               "session-orders",
		ProjectID:        "project-orders",
		Owner:            SessionOwnerHarbor,
		State:            SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64),
		CredentialDigest: strings.Repeat("b", 64),
		Generation:       1,
		Process: &ProcessEvidence{
			PID:                4102,
			BirthToken:         "process-birth-4102",
			ExecutableIdentity: filepath.Clean(executable),
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: at,
		UpdatedAt: at.Add(time.Second),
	}
}
