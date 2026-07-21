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
	runtimeRepairNativeIgnoreTermination = "HARBOR_RUNTIME_REPAIR_NATIVE_IGNORE_TERM"
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
	if os.Getenv(runtimeRepairNativeIgnoreTermination) == "1" {
		for range signals {
		}
	}
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
		direct, directErr := inspectDarwinRuntimeRepairPass(t.Context(), RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint})
		t.Logf("direct Darwin runtime repair pass = %#v, err = %v", direct, directErr)
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

// TestNativeDarwinRuntimeRepairEscalatesAfterGracefulNonconvergence proves a confirmed exact scope cannot remain quarantined when its root ignores SIGTERM.
func TestNativeDarwinRuntimeRepairEscalatesAfterGracefulNonconvergence(t *testing.T) {
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
		runtimeRepairNativeIgnoreTermination+"=1",
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatalf("start stubborn native forj helper error = %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for stubborn native forj helper listener error = %v", err)
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
		t.Fatalf("Confirm() = %#v, want settled forceful confirmation", confirmation)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("stubborn native forj helper exited cleanly; expected SIGKILL escalation")
	}
}

// TestNativeDarwinRuntimeRepairRejectsAmbiguousScopeWithoutSignal proves a listener outside a dedicated forj dev session cannot become a repair candidate.
func TestNativeDarwinRuntimeRepairRejectsAmbiguousScopeWithoutSignal(t *testing.T) {
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
	if err := command.Start(); err != nil {
		t.Fatalf("start non-dedicated native forj helper error = %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for non-dedicated native listener error = %v", err)
	}

	inspection, err := NewRuntimeRepairer().Inspect(t.Context(), RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionAmbiguous || inspection.Candidate != nil {
		t.Fatalf("Inspect() = %#v, want ambiguous inspection without a candidate", inspection)
	}
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("ambiguous inspection stopped the listener: %v", err)
	}
}

// TestNativeDarwinUnattributedRuntimeInspectionAcceptsNonDedicatedScope proves an older direct launch can be correlated and explicitly settled without claiming a Harbor-created session.
func TestNativeDarwinUnattributedRuntimeInspectionAcceptsNonDedicatedScope(t *testing.T) {
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
	if err := command.Start(); err != nil {
		t.Fatalf("start non-dedicated native forj helper error = %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for non-dedicated native listener error = %v", err)
	}

	repairer := NewUnattributedRuntimeRepairer()
	inspection, err := repairer.Inspect(t.Context(), RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Candidate == nil {
		t.Fatalf("Inspect() = %#v, want actionable candidate", inspection)
	}
	if err := inspection.Validate(); err != nil {
		t.Fatalf("inspection.Validate() error = %v", err)
	}
	confirmation, err := repairer.Confirm(t.Context(), *inspection.Candidate)
	if err != nil || confirmation.State != RuntimeRepairConfirmationSettled || !confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, %v; want settled graceful signal", confirmation, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("wait for terminated native forj helper error = %v", err)
	}
}

// TestNativeDarwinRuntimeRepairRejectsDriftWithoutSignal proves a replacement listener cannot inherit an inspected repair candidate's signal.
func TestNativeDarwinRuntimeRepairRejectsDriftWithoutSignal(t *testing.T) {
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

	original := exec.Command(forjPath, "dev")
	original.Dir = checkout
	original.Env = append(
		os.Environ(),
		runtimeRepairNativeHelperEnvironment+"=1",
		runtimeRepairNativeHelperAddress+"="+endpoint.String(),
	)
	original.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := original.Start(); err != nil {
		t.Fatalf("start original native forj helper error = %v", err)
	}
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for original native listener error = %v", err)
	}

	repairer := NewRuntimeRepairer()
	inspection, err := repairer.Inspect(t.Context(), RuntimeRepairTarget{CheckoutRoot: checkout, Endpoint: endpoint})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspection.State != RuntimeRepairInspectionActionable || inspection.Candidate == nil {
		t.Fatalf("Inspect() = %#v, want actionable candidate", inspection)
	}
	if err := original.Process.Kill(); err != nil {
		t.Fatalf("kill original native helper error = %v", err)
	}
	// SIGKILL intentionally produces an ExitError; waiting only reaps the old scope before the replacement binds.
	_ = original.Wait()

	replacement := exec.Command(forjPath, "dev")
	replacement.Dir = checkout
	replacement.Env = append(
		os.Environ(),
		runtimeRepairNativeHelperEnvironment+"=1",
		runtimeRepairNativeHelperAddress+"="+endpoint.String(),
	)
	replacement.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := replacement.Start(); err != nil {
		t.Fatalf("start replacement native forj helper error = %v", err)
	}
	t.Cleanup(func() {
		if replacement.ProcessState == nil {
			_ = replacement.Process.Kill()
			_ = replacement.Wait()
		}
	})
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("wait for replacement native listener error = %v", err)
	}

	confirmation, err := repairer.Confirm(t.Context(), *inspection.Candidate)
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmation.State != RuntimeRepairConfirmationDrifted || confirmation.Signaled {
		t.Fatalf("Confirm() = %#v, want zero-signal drift", confirmation)
	}
	if err := waitForRuntimeRepairNativeListener(endpoint); err != nil {
		t.Fatalf("drifted confirmation stopped the replacement listener: %v", err)
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
