//go:build darwin

package projectprocess

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	runtimeRepairNativeTestEnvironment   = "HARBOR_NATIVE_RUNTIME_REPAIR_TEST"
	runtimeRepairNativeHelperEnvironment = "HARBOR_RUNTIME_REPAIR_NATIVE_HELPER"
	runtimeRepairNativeHelperAddress     = "HARBOR_RUNTIME_REPAIR_NATIVE_ADDRESS"
)

// init turns a copied test binary into the exact dedicated-session forj dev process used by the native proof.
func init() {
	if os.Getenv(runtimeRepairNativeHelperEnvironment) != "1" {
		return
	}
	if len(os.Args) != 2 || os.Args[1] != "dev" {
		os.Exit(90)
	}
	address := os.Getenv(runtimeRepairNativeHelperAddress)
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		os.Exit(91)
	}
	defer listener.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	<-signals
}

// TestNativeDarwinRuntimeRepairLifecycle proves the reviewed native backend signals only an exact forj dev session and settles its listener.
func TestNativeDarwinRuntimeRepairLifecycle(t *testing.T) {
	if os.Getenv(runtimeRepairNativeTestEnvironment) != "1" {
		t.Skip("set HARBOR_NATIVE_RUNTIME_REPAIR_TEST=1 on a disposable macOS runner")
	}
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize checkout error = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable error = %v", err)
	}
	forjPath := filepath.Join(checkout, "forj")
	if err := copyRuntimeRepairNativeHelper(executable, forjPath); err != nil {
		t.Fatalf("copy forj helper error = %v", err)
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("reserve listener error = %v", err)
	}
	endpoint := netip.MustParseAddrPort(listener.Addr().String())
	if err := listener.Close(); err != nil {
		t.Fatalf("release listener reservation error = %v", err)
	}

	command := exec.Command(forjPath, "dev")
	command.Dir = checkout
	command.Env = append(
		os.Environ(),
		runtimeRepairNativeHelperEnvironment+"=1",
		runtimeRepairNativeHelperAddress+"="+endpoint.String(),
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start native forj helper error = %v", err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for native forj helper listener error = %v", err)
	}

	repairer := NewRuntimeRepairer()
	inspection, err := repairer.Inspect(t.Context(), RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Candidate == nil {
		t.Fatalf("Inspect() = %#v, want actionable candidate", inspection)
	}
	confirmation, err := repairer.Confirm(t.Context(), *inspection.Candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, want settled graceful signal", confirmation)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("wait for terminated native forj helper error = %v", err)
	}
}

// copyRuntimeRepairNativeHelper copies the test executable so proc_pidpath and argv[0] share one canonical forj identity.
func copyRuntimeRepairNativeHelper(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

// waitForRuntimeRepairNativeListener waits only for the exact child-owned loopback endpoint.
func waitForRuntimeRepairNativeListener(endpoint netip.AddrPort) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp4", endpoint.String(), 100*time.Millisecond)
		if err == nil {
			return connection.Close()
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("native runtime repair listener %s did not become ready", endpoint)
}
