//go:build darwin || linux

package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestProjectRemovalIntentJournalRejectsLeafSwap verifies the retained root never follows a replaced journal leaf.
func TestProjectRemovalIntentJournalRejectsLeafSwap(t *testing.T) {
	base := t.TempDir()
	leaf := filepath.Join(base, projectRemovalIntentDirectory)
	redirected := filepath.Join(base, "redirected")
	if err := os.Mkdir(leaf, projectRemovalIntentDirectoryMode); err != nil {
		t.Fatalf("create journal leaf: %v", err)
	}
	if err := os.Mkdir(redirected, projectRemovalIntentDirectoryMode); err != nil {
		t.Fatalf("create redirected directory: %v", err)
	}

	journal := newProjectRemovalIntentJournalFixture(base)
	var swapErr error
	journal.afterDirectoryObservation = func() {
		swapErr = swapPathForSymlink(leaf, leaf+"-original", "redirected")
	}
	_, err := journal.LoadOrCreate(t.Context(), "project-orders", "intent-first", nil)
	if swapErr != nil {
		t.Fatalf("swap journal leaf: %v", swapErr)
	}
	if err == nil {
		t.Fatal("leaf swap error = nil")
	}
	if _, err := os.Lstat(filepath.Join(redirected, projectRemovalIntentLockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("redirected lock inspection error = %v, want no lock", err)
	}
}

// TestProjectRemovalIntentJournalRejectsLockSwap verifies locking never follows a substituted child outside the retained root.
func TestProjectRemovalIntentJournalRejectsLockSwap(t *testing.T) {
	base := t.TempDir()
	journal := newProjectRemovalIntentJournalFixture(base)
	if _, err := journal.LoadOrCreate(t.Context(), "project-orders", "intent-first", nil); err != nil {
		t.Fatalf("create retained intent: %v", err)
	}
	if err := journal.Clear(t.Context(), "project-orders", "intent-first"); err != nil {
		t.Fatalf("clear retained intent: %v", err)
	}

	outside := filepath.Join(base, "outside-lock")
	want := []byte("do not touch")
	if err := os.WriteFile(outside, want, projectRemovalIntentFileMode); err != nil {
		t.Fatalf("create outside lock target: %v", err)
	}
	leaf := filepath.Join(base, projectRemovalIntentDirectory)
	lock := filepath.Join(leaf, projectRemovalIntentLockFilename)
	swapper := newProjectRemovalIntentJournalFixture(base)
	var swapErr error
	swapper.beforeDirectOpen = func(name string) {
		if name == projectRemovalIntentLockFilename && swapErr == nil {
			swapErr = swapPathForSymlink(lock, lock+"-original", "../outside-lock")
		}
	}
	_, err := swapper.LoadOrCreate(t.Context(), "project-orders", "intent-second", nil)
	if swapErr != nil {
		t.Fatalf("swap lock: %v", swapErr)
	}
	if err == nil {
		t.Fatal("lock swap error = nil")
	}
	assertProjectRemovalIntentOutsideFile(t, outside, want)
}

// TestProjectRemovalIntentJournalRejectsRecordSwap verifies reads never follow a substituted record outside the retained root.
func TestProjectRemovalIntentJournalRejectsRecordSwap(t *testing.T) {
	base := t.TempDir()
	journal := newProjectRemovalIntentJournalFixture(base)
	if _, err := journal.LoadOrCreate(t.Context(), "project-orders", "intent-first", nil); err != nil {
		t.Fatalf("create retained intent: %v", err)
	}

	outside := filepath.Join(base, "outside-record")
	want := []byte(`{"version":1,"project_id":"project-orders","intent_id":"intent-outside"}`)
	if err := os.WriteFile(outside, want, projectRemovalIntentFileMode); err != nil {
		t.Fatalf("create outside record target: %v", err)
	}
	recordName := projectRemovalIntentPath("project-orders")
	record := filepath.Join(base, projectRemovalIntentDirectory, recordName)
	swapper := newProjectRemovalIntentJournalFixture(base)
	var swapErr error
	swapper.beforeDirectOpen = func(name string) {
		if name == recordName && swapErr == nil {
			swapErr = swapPathForSymlink(record, record+"-original", "../outside-record")
		}
	}
	_, err := swapper.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		return "intent-unexpected", nil
	})
	if swapErr != nil {
		t.Fatalf("swap record: %v", swapErr)
	}
	if err == nil {
		t.Fatal("record swap error = nil")
	}
	assertProjectRemovalIntentOutsideFile(t, outside, want)
}

// swapPathForSymlink replaces one direct test object after observation to reproduce a same-user path race.
func swapPathForSymlink(path string, preserved string, target string) error {
	if err := os.Rename(path, preserved); err != nil {
		return err
	}
	return os.Symlink(target, path)
}

// assertProjectRemovalIntentOutsideFile verifies a rejected path race caused no content or permission mutation.
func assertProjectRemovalIntentOutsideFile(t *testing.T, path string, want []byte) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read outside target: %v", err)
	}
	if !bytes.Equal(contents, want) {
		t.Fatalf("outside target = %q, want %q", contents, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("inspect outside target: %v", err)
	}
	if info.Mode().Perm() != projectRemovalIntentFileMode {
		t.Fatalf("outside target permissions = %04o, want %04o", info.Mode().Perm(), projectRemovalIntentFileMode)
	}
}
