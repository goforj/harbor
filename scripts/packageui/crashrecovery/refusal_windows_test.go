//go:build windows

package main

import (
	"context"
	"io"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// TestConnectionRefusedWindows separates native refusal from ambiguous or interrupted network observations.
func TestConnectionRefusedWindows(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"native refusal", windows.WSAECONNREFUSED, true},
		{"wrapped native refusal", &net.OpError{Op: "dial", Net: "tcp4", Err: &os.SyscallError{Syscall: "connectex", Err: windows.WSAECONNREFUSED}}, true},
		{"socket timeout", windows.WSAETIMEDOUT, false},
		{"context timeout", context.DeadlineExceeded, false},
		{"canceled", context.Canceled, false},
		{"host unreachable", windows.WSAEHOSTUNREACH, false},
		{"socket permission denied", windows.WSAEACCES, false},
		{"native permission denied", windows.ERROR_ACCESS_DENIED, false},
		{"EOF", io.EOF, false},
		{"no error", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := connectionRefused(test.err); got != test.want {
				t.Fatalf("connectionRefused(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
