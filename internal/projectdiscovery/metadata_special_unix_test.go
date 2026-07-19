//go:build darwin || linux

package projectdiscovery

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// metadataScanResult carries both the read count and error across the timeout boundary.
type metadataScanResult struct {
	visits int
	err    error
}

// shortMetadataSocketPath keeps the special-file fixture beneath Darwin's compact sockaddr_un path limit.
func shortMetadataSocketPath(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "h-metadata-")
	if err != nil {
		t.Fatalf("create short metadata socket root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove short metadata socket root: %v", err)
		}
	})
	return filepath.Join(root, ".env.example")
}

// assertMetadataRejectedWithoutRead proves special metadata paths fail before content scanning can begin.
func assertMetadataRejectedWithoutRead(t *testing.T, filename string) {
	t.Helper()
	result := make(chan metadataScanResult, 1)
	go func() {
		visits := 0
		err := scanMetadataLines(filename, func(string) (bool, error) {
			visits++
			return false, nil
		})
		result <- metadataScanResult{visits: visits, err: err}
	}()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case scanned := <-result:
		if scanned.err == nil || !strings.Contains(scanned.err.Error(), "regular file") {
			t.Fatalf("scanMetadataLines() error = %v, want regular-file rejection", scanned.err)
		}
		if scanned.visits != 0 {
			t.Fatalf("scanMetadataLines() visits = %d, want none", scanned.visits)
		}
		var invalid *InvalidProjectError
		if !errors.As(scanned.err, &invalid) {
			t.Fatalf("scanMetadataLines() error = %T, want InvalidProjectError", scanned.err)
		}
	case <-timer.C:
		t.Fatal("scanMetadataLines() did not reject special metadata within two seconds")
	}
}

// TestScanMetadataLinesRejectsFIFOWithoutBlocking proves an optional dotenv FIFO cannot stall discovery waiting for a writer.
func TestScanMetadataLinesRejectsFIFOWithoutBlocking(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".env")
	if err := unix.Mkfifo(filename, 0o600); err != nil {
		t.Fatalf("create metadata FIFO: %v", err)
	}
	assertMetadataRejectedWithoutRead(t, filename)
}

// TestScanMetadataLinesRejectsSocketWithoutReading proves an optional dotenv socket is never treated as project content.
func TestScanMetadataLinesRejectsSocketWithoutReading(t *testing.T) {
	filename := shortMetadataSocketPath(t)
	listener, err := net.Listen("unix", filename)
	if err != nil {
		t.Fatalf("create metadata socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	assertMetadataRejectedWithoutRead(t, filename)
}

// TestScanMetadataLinesRejectsDeviceWithoutReading proves device nodes cannot substitute for optional dotenv metadata.
func TestScanMetadataLinesRejectsDeviceWithoutReading(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".env")
	if err := os.Symlink("/dev/null", filename); err != nil {
		t.Fatalf("create device metadata alias: %v", err)
	}
	assertMetadataRejectedWithoutRead(t, filename)
}
