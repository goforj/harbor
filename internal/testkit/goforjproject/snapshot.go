package goforjproject

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Snapshot captures the direct filesystem state of one generated checkout.
type Snapshot struct {
	// Entries contains the complete sorted direct filesystem state of the checkout.
	Entries []SnapshotEntry
}

// SnapshotEntry records one checkout path without following symbolic links.
type SnapshotEntry struct {
	// Path is the relative checkout path, with "." representing the root.
	Path string
	// Type identifies the direct filesystem object kind.
	Type SnapshotEntryType
	// Permissions contains the direct object's Unix permission bits.
	Permissions fs.FileMode
	// SHA256 contains regular-file content identity and is zero for other object kinds.
	SHA256 [sha256.Size]byte
	// LinkTarget contains the uninterpreted symbolic-link target and is empty for other object kinds.
	LinkTarget string
}

// SnapshotEntryType identifies the supported direct filesystem object kinds.
type SnapshotEntryType string

const (
	// SnapshotEntryDirectory identifies a directory entry.
	SnapshotEntryDirectory SnapshotEntryType = "directory"
	// SnapshotEntryRegularFile identifies a regular file entry.
	SnapshotEntryRegularFile SnapshotEntryType = "regular_file"
	// SnapshotEntrySymbolicLink identifies a symbolic-link entry.
	SnapshotEntrySymbolicLink SnapshotEntryType = "symbolic_link"
)

// CaptureSnapshot recursively records checkout state without following symbolic links.
func CaptureSnapshot(root string) (Snapshot, error) {
	information, err := os.Lstat(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect snapshot root: %w", err)
	}
	if !information.IsDir() {
		return Snapshot{}, fmt.Errorf("snapshot root %q is not a direct directory", root)
	}

	snapshot := Snapshot{}
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return fmt.Errorf("derive snapshot path: %w", err)
		}
		information, err := os.Lstat(filename)
		if err != nil {
			return fmt.Errorf("inspect snapshot path %q: %w", relative, err)
		}
		recorded, err := snapshotEntry(relative, filename, information)
		if err != nil {
			return err
		}
		snapshot.Entries = append(snapshot.Entries, recorded)
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture snapshot %q: %w", root, err)
	}
	sort.Slice(snapshot.Entries, func(left, right int) bool {
		return snapshot.Entries[left].Path < snapshot.Entries[right].Path
	})
	return snapshot, nil
}

// AssertSnapshotEqual fails t when want and got do not describe identical checkout state.
func AssertSnapshotEqual(t testing.TB, want Snapshot, got Snapshot) {
	t.Helper()
	if difference := want.Diff(got); difference != "" {
		t.Fatalf("checkout snapshot changed:\n%s", difference)
	}
}

// Diff returns a human-readable difference from snapshot to other, or an empty string when they are identical.
func (snapshot Snapshot) Diff(other Snapshot) string {
	entries := make(map[string]SnapshotEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		entries[entry.Path] = entry
	}
	otherEntries := make(map[string]SnapshotEntry, len(other.Entries))
	for _, entry := range other.Entries {
		otherEntries[entry.Path] = entry
	}

	paths := make([]string, 0, len(entries)+len(otherEntries))
	for path := range entries {
		paths = append(paths, path)
	}
	for path := range otherEntries {
		if _, found := entries[path]; !found {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	var differences []string
	for _, path := range paths {
		left, leftFound := entries[path]
		right, rightFound := otherEntries[path]
		switch {
		case !leftFound:
			differences = append(differences, fmt.Sprintf("added %s (%s)", path, right.Type))
		case !rightFound:
			differences = append(differences, fmt.Sprintf("removed %s (%s)", path, left.Type))
		case left != right:
			differences = append(differences, fmt.Sprintf("changed %s: %s -> %s", path, snapshotEntryDescription(left), snapshotEntryDescription(right)))
		}
	}
	return strings.Join(differences, "\n")
}

// snapshotEntry classifies one lstat result and reads regular content only after confirming that it is direct.
func snapshotEntry(relative string, filename string, information fs.FileInfo) (SnapshotEntry, error) {
	entry := SnapshotEntry{Path: relative, Permissions: information.Mode().Perm()}
	switch {
	case information.IsDir():
		entry.Type = SnapshotEntryDirectory
	case information.Mode().IsRegular():
		body, err := os.ReadFile(filename)
		if err != nil {
			return SnapshotEntry{}, fmt.Errorf("read snapshot file %q: %w", relative, err)
		}
		entry.Type = SnapshotEntryRegularFile
		entry.SHA256 = sha256.Sum256(body)
	case information.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(filename)
		if err != nil {
			return SnapshotEntry{}, fmt.Errorf("read snapshot symbolic link %q: %w", relative, err)
		}
		entry.Type = SnapshotEntrySymbolicLink
		entry.LinkTarget = target
	default:
		return SnapshotEntry{}, fmt.Errorf("snapshot path %q has unsupported file mode %s", relative, information.Mode().Type())
	}
	return entry, nil
}

// snapshotEntryDescription keeps assertion failures compact while retaining every recorded comparison field.
func snapshotEntryDescription(entry SnapshotEntry) string {
	description := fmt.Sprintf("%s mode=%#o", entry.Type, entry.Permissions)
	if entry.Type == SnapshotEntryRegularFile {
		return description + " sha256=" + fmt.Sprintf("%x", entry.SHA256)
	}
	if entry.Type == SnapshotEntrySymbolicLink {
		return description + " target=" + fmt.Sprintf("%q", entry.LinkTarget)
	}
	return description
}
