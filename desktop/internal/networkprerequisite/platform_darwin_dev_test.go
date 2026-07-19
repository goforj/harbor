//go:build darwin && dev

package networkprerequisite

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInspectDarwinDevelopmentArtifactAdmitsOnlyTheNativeCPU rejects binaries copied from another host before consent.
func TestInspectDarwinDevelopmentArtifactAdmitsOnlyTheNativeCPU(t *testing.T) {
	t.Parallel()

	expectedCPU, err := darwinDevelopmentArtifactCPU(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	wrongCPU := macho.CpuAmd64
	if expectedCPU == macho.CpuAmd64 {
		wrongCPU = macho.CpuArm64
	}

	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "native executable", content: darwinDevelopmentMachOFixture(t, expectedCPU, macho.TypeExec)},
		{name: "wrong CPU", content: darwinDevelopmentMachOFixture(t, wrongCPU, macho.TypeExec), want: "targets CPU"},
		{name: "ELF executable", content: []byte("\x7fELF\x02\x01\x01"), want: "not a thin Mach-O executable"},
		{name: "Mach-O object", content: darwinDevelopmentMachOFixture(t, expectedCPU, macho.TypeObj), want: "not a Mach-O executable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "helper")
			if err := os.WriteFile(path, test.content, darwinDevelopmentArtifactMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, darwinDevelopmentArtifactMode); err != nil {
				t.Fatal(err)
			}

			err := inspectDarwinDevelopmentArtifact(path, uint32(os.Geteuid()), uint32(os.Getegid()))
			if test.want == "" {
				if err != nil {
					t.Fatalf("inspectDarwinDevelopmentArtifact() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("inspectDarwinDevelopmentArtifact() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestDarwinDevelopmentArtifactDirectoryExistsSeparatesAbsenceFromUnsafePaths protects the legacy transition boundary.
func TestDarwinDevelopmentArtifactDirectoryExistsSeparatesAbsenceFromUnsafePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	exists, err := darwinDevelopmentArtifactDirectoryExists(missing)
	if err != nil || exists {
		t.Fatalf("missing directory = (%t, %v), want (false, nil)", exists, err)
	}

	directory := filepath.Join(root, "darwin-arm64")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	exists, err = darwinDevelopmentArtifactDirectoryExists(directory)
	if err != nil || !exists {
		t.Fatalf("present directory = (%t, %v), want (true, nil)", exists, err)
	}

	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("artifact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := darwinDevelopmentArtifactDirectoryExists(file); err == nil {
		t.Fatal("darwinDevelopmentArtifactDirectoryExists() accepted a regular file")
	}
}

// darwinDevelopmentMachOFixture creates the smallest thin header needed to exercise native artifact admission.
func darwinDevelopmentMachOFixture(t *testing.T, cpu macho.Cpu, fileType macho.Type) []byte {
	t.Helper()

	var content bytes.Buffer
	header := macho.FileHeader{
		Magic:  macho.Magic64,
		Cpu:    cpu,
		SubCpu: 0,
		Type:   fileType,
	}
	if err := binary.Write(&content, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&content, binary.LittleEndian, uint32(0)); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}
