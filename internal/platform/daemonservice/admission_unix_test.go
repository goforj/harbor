//go:build darwin || linux

package daemonservice

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// packageInformation supplies native ownership variations without requiring unit tests to change real root files.
type packageInformation struct {
	os.FileInfo
	status *syscall.Stat_t
	mode   os.FileMode
}

// Sys preserves the native metadata shape accepted by Unix package admission.
func (information packageInformation) Sys() any { return information.status }

// Mode permits direct representation of executable, symlink, and set-ID fixtures.
func (information packageInformation) Mode() os.FileMode { return information.mode }

// TestPackageAdmissionRejectsEveryForeignShape keeps package startup from following or elevating a substituted component.
func TestPackageAdmissionRejectsEveryForeignShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "component")
	if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	base, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{"canonical", "owner", "links", "writable", "symlink", "directory", "setuid", "setgid", "not executable"} {
		t.Run(shape, func(t *testing.T) {
			status := *base.Sys().(*syscall.Stat_t)
			status.Uid = 0
			status.Nlink = 1
			information := packageInformation{FileInfo: base, status: &status, mode: 0o755}
			switch shape {
			case "owner":
				status.Uid = 1
			case "links":
				status.Nlink = 2
			case "writable":
				information.mode = 0o777
			case "symlink":
				information.mode = os.ModeSymlink | 0o755
			case "directory":
				information.mode = os.ModeDir | 0o755
			case "setuid":
				information.mode |= os.ModeSetuid
			case "setgid":
				information.mode |= os.ModeSetgid
			case "not executable":
				information.mode = 0o644
			}
			if err := AdmitExecutable(information); (err == nil) != (shape == "canonical") {
				t.Fatalf("%s admission = %v", shape, err)
			}
		})
	}
	status := *base.Sys().(*syscall.Stat_t)
	status.Uid = 0
	status.Nlink = 1
	unit := packageInformation{FileInfo: base, status: &status, mode: 0o644}
	if err := AdmitUnit(unit, LinuxDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := AdmitUnit(unit, append(LinuxDefinition(), []byte("foreign\n")...)); err == nil {
		t.Fatal("foreign unit admitted")
	}
	unit.mode = 0o755
	if err := AdmitUnit(unit, LinuxDefinition()); err == nil {
		t.Fatal("foreign unit mode admitted")
	}
}
