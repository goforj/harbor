//go:build !windows

package main

import (
	"context"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

// TestNativeConnectionRefusal distinguishes a closed listener from ambiguous socket failures.
func TestNativeConnectionRefusal(t *testing.T) {
	wrapped := &net.OpError{Op: "dial", Net: "tcp4", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	if !connectionRefused(wrapped) {
		t.Fatal("wrapped native refusal was rejected")
	}
	for _, err := range []error{nil, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.EACCES, context.DeadlineExceeded, io.EOF} {
		if connectionRefused(err) {
			t.Fatalf("ambiguous socket error accepted as absence: %v", err)
		}
	}
}
