//go:build darwin

package replaystore

import (
	"os"
	"os/exec"
	"testing"
)

// TestDarwinReplayValidationRejectsExtendedACL proves private mode bits cannot conceal a named access grant.
func TestDarwinReplayValidationRejectsExtendedACL(t *testing.T) {
	_, directory, _, file, _ := replayUnixObjects(t)
	owner := uint32(os.Geteuid())
	for _, test := range []struct {
		name      string
		file      *os.File
		directory bool
	}{
		{name: "directory", file: directory, directory: true},
		{name: "file", file: file},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("/bin/chmod", "+a", "everyone allow read", test.file.Name())
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("apply macOS ACL fixture: %v: %s", err, output)
			}
			t.Cleanup(func() { _ = exec.Command("/bin/chmod", "-N", test.file.Name()).Run() })
			if err := validateUnixReplayObject(test.file, test.directory, owner); err == nil {
				t.Fatal("validateUnixReplayObject() accepted a macOS extended ACL")
			}
		})
	}
}
