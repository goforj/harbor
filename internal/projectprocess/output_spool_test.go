package projectprocess

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestOutputSpoolReopensExactSessionHistory proves persisted stdout/stderr frames retain order and identity.
func TestOutputSpoolReopensExactSessionHistory(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-output")
	sessionID := domain.SessionID("session-output")
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("openOutputSpool() error = %v", err)
	}
	if spool == nil {
		t.Fatal("openOutputSpool() returned nil spool")
	}
	if err := spool.appendNormalized(outputStreamStdout, []byte("stdout\n")); err != nil {
		t.Fatalf("append stdout: %v", err)
	}
	if err := spool.appendNormalized(outputStreamStderr, []byte("stderr\n")); err != nil {
		t.Fatalf("append stderr: %v", err)
	}
	if err := spool.close(); err != nil {
		t.Fatalf("close spool: %v", err)
	}

	snapshot, available, err := readOutputSpool(directory, projectID, sessionID)
	if err != nil || !available {
		t.Fatalf("readOutputSpool() = %#v, %t, %v", snapshot, available, err)
	}
	chunk := snapshot.transcript.read(0)
	if !chunk.Available || chunk.Text != "stdout\nstderr\n" || chunk.NextCursor != uint64(len(chunk.Text)) {
		t.Fatalf("replayed chunk = %#v, want ordered persisted output", chunk)
	}
	if !snapshot.closed {
		t.Fatal("replayed spool is not marked closed")
	}

	path, err := outputSpoolPath(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("outputSpoolPath() error = %v", err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat spool file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("spool file mode = %o, want 600", fileInfo.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat spool directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("spool directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}

	wrongHeader, _, _, _, err := readOutputSpoolFile(mustOpenOutputSpoolFile(t, path), projectID, domain.SessionID("session-other"))
	if err == nil || !errors.Is(err, ErrOutputSpoolCorrupt) || wrongHeader != (outputSpoolHeader{}) {
		t.Fatalf("identity mismatch = %#v, %v, want corrupt rejection", wrongHeader, err)
	}
}

// TestOutputSpoolIgnoresOneIncompleteCrashTail proves a daemon crash cannot expose a torn frame as output.
func TestOutputSpoolIgnoresOneIncompleteCrashTail(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-tail")
	sessionID := domain.SessionID("session-tail")
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("openOutputSpool() error = %v", err)
	}
	if err := spool.appendNormalized(outputStreamStdout, []byte("complete\n")); err != nil {
		t.Fatalf("append complete frame: %v", err)
	}
	if _, err := spool.file.Write([]byte("HOF1\x01")); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	if err := spool.file.Close(); err != nil {
		t.Fatalf("close crashed spool: %v", err)
	}

	snapshot, available, err := readOutputSpool(directory, projectID, sessionID)
	if err != nil || !available || snapshot.closed {
		t.Fatalf("read torn spool = %#v, %t, %v", snapshot, available, err)
	}
	if chunk := snapshot.transcript.read(0); chunk.Text != "complete\n" {
		t.Fatalf("torn-tail replay = %#v, want only complete frame", chunk)
	}
}

// TestOutputSpoolResumesUnclosedHistory proves a later daemon can reuse crash-safe history without treating it as process authority.
func TestOutputSpoolResumesUnclosedHistory(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-resume")
	sessionID := domain.SessionID("session-resume")
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("openOutputSpool() error = %v", err)
	}
	if err := spool.appendNormalized(outputStreamStdout, []byte("before\n")); err != nil {
		t.Fatalf("append before: %v", err)
	}
	if _, err := spool.file.Write([]byte("HOF1\x01")); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	if err := spool.file.Close(); err != nil {
		t.Fatalf("close interrupted spool: %v", err)
	}

	resumed, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("resume output spool: %v", err)
	}
	if resumed.nextCursor != uint64(len("before\n")) {
		t.Fatalf("resumed cursor = %d, want %d", resumed.nextCursor, len("before\n"))
	}
	if err := resumed.appendNormalized(outputStreamStderr, []byte("after\n")); err != nil {
		t.Fatalf("append after: %v", err)
	}
	if err := resumed.close(); err != nil {
		t.Fatalf("close resumed spool: %v", err)
	}

	snapshot, available, err := readOutputSpool(directory, projectID, sessionID)
	if err != nil || !available || !snapshot.closed {
		t.Fatalf("read resumed spool = %#v, %t, %v", snapshot, available, err)
	}
	if chunk := snapshot.transcript.read(0); chunk.Text != "before\nafter\n" || chunk.NextCursor != uint64(len("before\nafter\n")) {
		t.Fatalf("resumed history = %#v, want cursor-contiguous output", chunk)
	}
}

// TestOutputSpoolRejectsChecksumCorruption proves a damaged complete frame is not treated as history.
func TestOutputSpoolRejectsChecksumCorruption(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-corrupt")
	sessionID := domain.SessionID("session-corrupt")
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("openOutputSpool() error = %v", err)
	}
	if err := spool.appendNormalized(outputStreamStdout, []byte("corrupt-me")); err != nil {
		t.Fatalf("append frame: %v", err)
	}
	if err := spool.close(); err != nil {
		t.Fatalf("close spool: %v", err)
	}
	path, err := outputSpoolPath(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("outputSpoolPath() error = %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open spool for corruption: %v", err)
	}
	headerBytes := outputSpoolHeaderPrefixBytes + len(projectID) + len(sessionID) + 4
	if _, err := file.Seek(int64(headerBytes+outputSpoolFrameHeaderBytes), 0); err != nil {
		t.Fatalf("seek payload: %v", err)
	}
	if _, err := file.Write([]byte("X")); err != nil {
		t.Fatalf("corrupt payload: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupted spool: %v", err)
	}
	_, available, err := readOutputSpool(directory, projectID, sessionID)
	if err == nil || available || !errors.Is(err, ErrOutputSpoolCorrupt) {
		t.Fatalf("corrupt spool = available %t, error %v, want fail closed", available, err)
	}
}

// TestOutputSpoolCompactionPreservesAbsoluteTruncation proves bounded history never resets the caller cursor to zero.
func TestOutputSpoolCompactionPreservesAbsoluteTruncation(t *testing.T) {
	directory := t.TempDir()
	projectID := domain.ProjectID("project-compact")
	sessionID := domain.SessionID("session-compact")
	spool, err := openOutputSpool(directory, projectID, sessionID)
	if err != nil {
		t.Fatalf("openOutputSpool() error = %v", err)
	}
	spool.maximum = 140
	for _, value := range []string{strings.Repeat("a", 20), strings.Repeat("b", 20), strings.Repeat("c", 20)} {
		if err := spool.appendNormalized(outputStreamStdout, []byte(value)); err != nil {
			t.Fatalf("append %q: %v", value[:1], err)
		}
	}
	if err := spool.close(); err != nil {
		t.Fatalf("close compacted spool: %v", err)
	}
	snapshot, available, err := readOutputSpool(directory, projectID, sessionID)
	if err != nil || !available {
		t.Fatalf("read compacted spool = %#v, %t, %v", snapshot, available, err)
	}
	chunk := snapshot.transcript.read(0)
	if !chunk.Truncated || chunk.Text != strings.Repeat("c", 20) || chunk.NextCursor != 60 {
		t.Fatalf("compacted chunk = %#v, want absolute truncated suffix", chunk)
	}
}

// TestOutputSpoolPathHashesIdentifiers proves path components cannot be redirected by separators in IDs.
func TestOutputSpoolPathHashesIdentifiers(t *testing.T) {
	directory := t.TempDir()
	path, err := outputSpoolPath(directory, domain.ProjectID("project/../../escape"), domain.SessionID("session/one"))
	if err != nil {
		t.Fatalf("outputSpoolPath() error = %v", err)
	}
	if filepath.Dir(path) != filepath.Join(directory, outputSpoolDirectoryName) || strings.Contains(filepath.Base(path), "escape") {
		t.Fatalf("hashed spool path = %q, want a single opaque filename", path)
	}
}

// mustOpenOutputSpoolFile opens a test spool while keeping the identity mismatch assertion local.
func mustOpenOutputSpoolFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output spool: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
