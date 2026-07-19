package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// testWindowsInvocationConnection records closure while providing one in-memory duplex stream.
type testWindowsInvocationConnection struct {
	bytes.Buffer
	closed bool
}

// Close records release of the one-shot helper channel.
func (connection *testWindowsInvocationConnection) Close() error {
	connection.closed = true
	return nil
}

// TestOpenWindowsInvocationAcceptsOnlyOneRandomPipeRoute verifies arguments carry routing metadata and nothing else.
func TestOpenWindowsInvocationAcceptsOnlyOneRandomPipeRoute(t *testing.T) {
	name := windowsInvocationPipePrefix + strings.Repeat("a1", windowsInvocationPipeRandomBytes)
	connection := &testWindowsInvocationConnection{}
	opened := ""
	invocation, err := openWindowsInvocation([]string{"harbor-helper.exe", name}, func(path string) (io.ReadWriteCloser, error) {
		opened = path
		return connection, nil
	})
	if err != nil {
		t.Fatalf("openWindowsInvocation() error = %v", err)
	}
	if opened != name || invocation.reader != connection || invocation.writer != connection {
		t.Fatalf("opened = %q, invocation = %#v", opened, invocation)
	}
	if err := invocation.close(); err != nil || !connection.closed {
		t.Fatalf("close error = %v, closed = %t", err, connection.closed)
	}
}

// TestOpenWindowsInvocationRejectsAmbientArguments verifies malformed routes never reach the pipe opener.
func TestOpenWindowsInvocationRejectsAmbientArguments(t *testing.T) {
	valid := windowsInvocationPipePrefix + strings.Repeat("ab", windowsInvocationPipeRandomBytes)
	tests := [][]string{
		nil,
		{"harbor-helper.exe"},
		{"harbor-helper.exe", valid, "request-data"},
		{"harbor-helper.exe", `\\.\pipe\other`},
		{"harbor-helper.exe", windowsInvocationPipePrefix + strings.Repeat("A1", windowsInvocationPipeRandomBytes)},
		{"harbor-helper.exe", windowsInvocationPipePrefix + strings.Repeat("0", windowsInvocationPipeRandomBytes*2-1)},
		{"harbor-helper.exe", windowsInvocationPipePrefix + strings.Repeat("z", windowsInvocationPipeRandomBytes*2)},
	}
	for _, arguments := range tests {
		calls := 0
		_, err := openWindowsInvocation(arguments, func(string) (io.ReadWriteCloser, error) {
			calls++
			return &testWindowsInvocationConnection{}, nil
		})
		if err == nil || calls != 0 {
			t.Fatalf("openWindowsInvocation(%q) = (%v, calls %d), want rejection before open", arguments, err, calls)
		}
	}
}

// TestOpenWindowsInvocationPreservesOpenFailure verifies connection errors cannot fall through to durable authority.
func TestOpenWindowsInvocationPreservesOpenFailure(t *testing.T) {
	wantErr := errors.New("pipe unavailable")
	name := windowsInvocationPipePrefix + strings.Repeat("01", windowsInvocationPipeRandomBytes)
	_, err := openWindowsInvocation([]string{"harbor-helper.exe", name}, func(string) (io.ReadWriteCloser, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("openWindowsInvocation() error = %v, want %v", err, wantErr)
	}
}

var _ io.ReadWriteCloser = (*testWindowsInvocationConnection)(nil)
