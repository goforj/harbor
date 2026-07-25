//go:build linux

package projectprocess

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestLinuxProcessAbsentTreatsProcfsExitRacesAsAbsence keeps short-lived processes from turning cleanup into uncertainty.
func TestLinuxProcessAbsentTreatsProcfsExitRacesAsAbsence(t *testing.T) {
	for _, err := range []error{
		os.ErrNotExist,
		&os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ENOENT},
		&os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH},
		fmt.Errorf("wrapped procfs race: %w", syscall.ESRCH),
	} {
		if !linuxProcessAbsent(err) {
			t.Fatalf("linuxProcessAbsent(%v) = false, want true", err)
		}
	}
	if linuxProcessAbsent(&os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.EPERM}) {
		t.Fatal("linuxProcessAbsent(EPERM) = true, want false")
	}
}
