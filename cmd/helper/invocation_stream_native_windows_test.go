//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// TestOpenWindowsHelperPipeAuthenticatesNativeServerAndPreservesMessageEOF verifies the helper's real kernel boundary without elevation.
func TestOpenWindowsHelperPipeAuthenticatesNativeServerAndPreservesMessageEOF(t *testing.T) {
	userID, err := currentWindowsInvocationUserID()
	if err != nil {
		t.Fatalf("read native Windows helper test user: %v", err)
	}
	path := nativeWindowsHelperTestPipePath()
	security := fmt.Sprintf("O:%sD:P(A;;GA;;;%s)(A;;GA;;;%s)", userID, userID, windowsInvocationSystemSID)
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: security,
		MessageMode:        true,
		InputBufferSize:    16 * 1024,
		OutputBufferSize:   16 * 1024,
	})
	if err != nil {
		t.Fatalf("create native Windows helper test pipe: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	request := strings.Repeat("helper-request-", 512)
	response := strings.Repeat("helper-response-", 384)
	type serverResult struct {
		response        string
		clientProcessID uint32
		err             error
	}
	serverResults := make(chan serverResult, 1)
	acceptStarted := make(chan struct{})
	go func() {
		close(acceptStarted)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResults <- serverResult{err: fmt.Errorf("accept native Windows helper connection: %w", acceptErr)}
			return
		}
		defer connection.Close()
		if deadlineErr := connection.SetDeadline(time.Now().Add(10 * time.Second)); deadlineErr != nil {
			serverResults <- serverResult{err: fmt.Errorf("set native Windows helper server deadline: %w", deadlineErr)}
			return
		}
		handle, ok := connection.(interface{ Fd() uintptr })
		if !ok {
			serverResults <- serverResult{err: fmt.Errorf("native Windows helper server type %T does not expose a kernel handle", connection)}
			return
		}
		var clientProcessID uint32
		if processErr := windows.GetNamedPipeClientProcessId(windows.Handle(handle.Fd()), &clientProcessID); processErr != nil {
			serverResults <- serverResult{err: fmt.Errorf("read native Windows helper client process: %w", processErr)}
			return
		}
		written, writeErr := io.WriteString(connection, request)
		if writeErr != nil || written != len(request) {
			serverResults <- serverResult{err: errors.Join(writeErr, io.ErrShortWrite)}
			return
		}
		closeWriter, ok := connection.(interface{ CloseWrite() error })
		if !ok {
			serverResults <- serverResult{err: fmt.Errorf("native Windows helper server type %T does not support CloseWrite", connection)}
			return
		}
		if closeErr := closeWriter.CloseWrite(); closeErr != nil {
			serverResults <- serverResult{err: fmt.Errorf("finish native Windows helper request message: %w", closeErr)}
			return
		}
		body, readErr := io.ReadAll(connection)
		serverResults <- serverResult{response: string(body), clientProcessID: clientProcessID, err: readErr}
	}()
	<-acceptStarted

	arguments := []string{os.Args[0], path}
	var invocation invocationStreams
	deadline := time.Now().Add(10 * time.Second)
	for {
		invocation, err = openWindowsInvocation(arguments, openWindowsHelperPipe)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) || time.Now().After(deadline) {
			t.Fatalf("open native Windows helper invocation: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	connection, ok := invocation.reader.(*windowsHelperPipeConnection)
	if !ok || invocation.writer != connection {
		t.Fatalf("native Windows helper invocation streams = (%T, %T), want one pipe connection", invocation.reader, invocation.writer)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = invocation.close()
		}
	})

	handle := windows.Handle(connection.Fd())
	if err := validateWindowsInvocationPipeSecurity(handle, userID); err != nil {
		t.Fatalf("validate native Windows helper pipe security: %v", err)
	}
	if err := validateWindowsInvocationServer(handle, userID); err != nil {
		t.Fatalf("validate native Windows helper pipe server: %v", err)
	}
	var serverProcessID uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &serverProcessID); err != nil {
		t.Fatalf("read native Windows helper server process: %v", err)
	}
	if serverProcessID != uint32(os.Getpid()) {
		t.Errorf("native Windows helper server process = %d, want %d", serverProcessID, os.Getpid())
	}

	body, err := io.ReadAll(invocation.reader)
	if err != nil {
		t.Fatalf("read native Windows helper request through message EOF: %v", err)
	}
	if string(body) != request {
		t.Fatalf("native Windows helper request length = %d, want %d", len(body), len(request))
	}
	written, err := io.WriteString(invocation.writer, response)
	if err != nil || written != len(response) {
		t.Fatalf("write native Windows helper response = (%d, %v), want %d bytes", written, err, len(response))
	}
	if err := invocation.close(); err != nil {
		t.Fatalf("close native Windows helper invocation: %v", err)
	}
	closed = true

	result := <-serverResults
	if result.err != nil {
		t.Fatalf("native Windows helper server exchange: %v", result.err)
	}
	if result.clientProcessID != uint32(os.Getpid()) {
		t.Errorf("native Windows helper client process = %d, want %d", result.clientProcessID, os.Getpid())
	}
	if result.response != response {
		t.Fatalf("native Windows helper response length = %d, want %d", len(result.response), len(response))
	}
}

// nativeWindowsHelperTestPipePath returns one valid collision-resistant invocation route for native tests.
func nativeWindowsHelperTestPipePath() string {
	randomSuffix := fmt.Sprintf("%064x", uint64(time.Now().UnixNano())^uint64(os.Getpid()))
	return windowsInvocationPipePrefix + randomSuffix
}
