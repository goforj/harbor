//go:build darwin

package main

import (
	"os"
	"syscall"
	"testing"
)

// TestOpenDarwinParentLivenessFailsClosedForAbsentOrInvalidFD verifies only an inherited pipe can establish parent liveness.
func TestOpenDarwinParentLivenessFailsClosedForAbsentOrInvalidFD(t *testing.T) {
	tests := []struct {
		name string
		open darwinParentLivenessOpener
	}{
		{
			name: "absent descriptor",
			open: func(uintptr, string) *os.File {
				return nil
			},
		},
		{
			name: "non-pipe descriptor",
			open: func(uintptr, string) *os.File {
				file, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatalf("open invalid liveness descriptor: %v", err)
				}
				return file
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			liveness, err := openDarwinParentLiveness(parentLivenessDescriptor, test.open)
			if err == nil || liveness != nil {
				t.Fatalf("open liveness = %v, %v; want rejection", liveness, err)
			}
		})
	}
}

// TestOpenDarwinParentLivenessPreventsChildInheritance verifies nested privileged commands cannot retain the launcher lifetime pipe.
func TestOpenDarwinParentLivenessPreventsChildInheritance(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create parent liveness pipe: %v", err)
	}
	defer writer.Close()

	liveness, err := openDarwinParentLiveness(reader.Fd(), func(uintptr, string) *os.File {
		return reader
	})
	if err != nil {
		t.Fatalf("open parent liveness: %v", err)
	}
	defer liveness.Close()

	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, liveness.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatalf("inspect parent liveness descriptor flags: %v", errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("parent liveness descriptor can leak into nested child processes")
	}
}
