package ticketkey

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootedFilesystemOperationsExerciseAdmissionBranches verifies confined helpers reject malformed paths and layouts.
func TestRootedFilesystemOperationsExerciseAdmissionBranches(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "helper-ticket-key")
	store := mustStore(t, directory)
	defer store.Close()
	filesystem := store.filesystem

	dirtyPath := strings.Join([]string{"nested", "..", "key"}, string(filepath.Separator))
	for _, path := range []string{"", ".", "../escape", dirtyPath, filepath.Join(string(filepath.Separator), "absolute")} {
		if err := validateRelativePath(path); err == nil {
			t.Errorf("validateRelativePath(%q) error = nil", path)
		}
	}
	if err := validateRelativePath("clean"); err != nil {
		t.Fatalf("validateRelativePath(clean) error = %v", err)
	}

	if err := filesystem.ensureDirectory("manual"); err != nil {
		t.Fatalf("ensureDirectory(first) error = %v", err)
	}
	if err := filesystem.ensureDirectory("manual"); err != nil {
		t.Fatalf("ensureDirectory(existing) error = %v", err)
	}
	if err := filesystem.writeExclusiveFile(filepath.Join("manual", keyFilename), []byte("bounded")); err != nil {
		t.Fatalf("writeExclusiveFile(first) error = %v", err)
	}
	if err := filesystem.writeExclusiveFile(filepath.Join("manual", keyFilename), []byte("replacement")); err == nil {
		t.Fatal("writeExclusiveFile(existing) error = nil")
	}
	if _, err := filesystem.readBoundedFile(filepath.Join("manual", keyFilename), 3); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readBoundedFile(oversized) error = %v", err)
	}
	if content, err := filesystem.readBoundedFile(filepath.Join("manual", keyFilename), 16); err != nil || !bytes.Equal(content, []byte("bounded")) {
		t.Fatalf("readBoundedFile() = %q, %v", content, err)
	}
	if err := filesystem.validateDirectory("manual", keyFilename); err != nil {
		t.Fatalf("validateDirectory() error = %v", err)
	}
	if err := filesystem.validateDirectory("manual", "other.json"); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("validateDirectory(wrong name) error = %v", err)
	}
	if err := filesystem.writeExclusiveFile(filepath.Join("manual", "unexpected"), []byte("x")); err != nil {
		t.Fatalf("writeExclusiveFile(unexpected) error = %v", err)
	}
	if err := filesystem.validateDirectory("manual", keyFilename); err == nil || !strings.Contains(err.Error(), "contains 2") {
		t.Fatalf("validateDirectory(extra entry) error = %v", err)
	}

	if err := filesystem.ensureDirectory(".staging-empty"); err != nil {
		t.Fatalf("ensureDirectory(empty staging) error = %v", err)
	}
	if err := filesystem.renameDirectoryNoReplace(".staging-empty", "empty-published"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("renameDirectoryNoReplace(empty) error = %v", err)
	}
	if err := filesystem.removeStaging(".staging-empty"); err != nil {
		t.Fatalf("removeStaging(empty) error = %v", err)
	}
	if err := filesystem.removeStaging(".staging-missing"); err != nil {
		t.Fatalf("removeStaging(missing) error = %v", err)
	}
	if err := filesystem.removeStaging("manual"); err == nil || !strings.Contains(err.Error(), "refuse") {
		t.Fatalf("removeStaging(non-staging) error = %v", err)
	}

	if err := filesystem.ensureDirectory("destination"); err != nil {
		t.Fatalf("ensureDirectory(destination) error = %v", err)
	}
	if err := filesystem.ensureDirectory(".staging-full"); err != nil {
		t.Fatalf("ensureDirectory(full staging) error = %v", err)
	}
	if err := filesystem.writeExclusiveFile(filepath.Join(".staging-full", keyFilename), []byte("key")); err != nil {
		t.Fatalf("writeExclusiveFile(staging key) error = %v", err)
	}
	if err := filesystem.renameDirectoryNoReplace(".staging-full", "destination"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renameDirectoryNoReplace(existing) error = %v", err)
	}
	if err := filesystem.removeStaging(".staging-full"); err != nil {
		t.Fatalf("removeStaging(full) error = %v", err)
	}

	if _, err := filesystem.lstat("../escape"); err == nil {
		t.Fatal("lstat(invalid) error = nil")
	}
	if err := filesystem.syncDirectory("missing"); err == nil {
		t.Fatal("syncDirectory(missing) error = nil")
	}
	if _, err := filesystem.openDirect("manual", false); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("openDirect(directory as file) error = %v", err)
	}
	if _, err := filesystem.openDirect(filepath.Join("manual", keyFilename), true); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("openDirect(file as directory) error = %v", err)
	}
}

// TestWriteAllRejectsBrokenWriters verifies persistence cannot spin or accept impossible write counts.
func TestWriteAllRejectsBrokenWriters(t *testing.T) {
	want := errors.New("write failed")
	tests := []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{name: "zero", writer: fixedWriter{written: 0}, want: io.ErrShortWrite},
		{name: "excess", writer: fixedWriter{written: 2}, want: io.ErrShortWrite},
		{name: "error", writer: fixedWriter{err: want}, want: want},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := writeAll(test.writer, []byte("x")); !errors.Is(err, test.want) {
				t.Fatalf("writeAll() error = %v, want %v", err, test.want)
			}
		})
	}
	var output bytes.Buffer
	if err := writeAll(&output, []byte("complete")); err != nil || output.String() != "complete" {
		t.Fatalf("writeAll(complete) = %q, %v", output.String(), err)
	}
}

// TestCorruptionErrorNilReceiversKeepErrorsSafe verifies error reporting never needs key material or a live receiver.
func TestCorruptionErrorNilReceiversKeepErrorsSafe(t *testing.T) {
	var corruption *CorruptionError
	if got := corruption.Error(); got != "helper ticket key store material is corrupt: validation failed" {
		t.Fatalf("nil CorruptionError.Error() = %q", got)
	}
	if corruption.Unwrap() != nil {
		t.Fatal("nil CorruptionError.Unwrap() returned an error")
	}
}

// TestOpenRejectsAFileRoot verifies storage can never be rooted in a non-directory object.
func TestOpenRejectsAFileRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper-ticket-key")
	if err := os.WriteFile(path, []byte("not a directory"), privateFileMode); err != nil {
		t.Fatalf("WriteFile(root) error = %v", err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Open(file root) error = %v", err)
	}
}

// fixedWriter returns one configured result without mutating the supplied buffer.
type fixedWriter struct {
	written int
	err     error
}

// Write returns the configured impossible or failed write result.
func (writer fixedWriter) Write([]byte) (int, error) {
	return writer.written, writer.err
}
