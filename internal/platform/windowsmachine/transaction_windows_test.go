//go:build windows

package windowsmachine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// machineFixtureName keeps every native mutex test isolated from product machine transactions.
func machineFixtureName(t *testing.T) string {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native machine authority fixture requires an elevated disposable worker")
	}
	return fmt.Sprintf(`Global\HarborMachineFixture-%d-%d`, os.Getpid(), time.Now().UnixNano())
}

// TestNativeMachineTransaction holds the actual named mutex across callbacks, cancellation and callback failures.
func TestNativeMachineTransaction(t *testing.T) {
	name := machineFixtureName(t)
	held, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- withMachineTransaction(context.Background(), name, func() error { close(held); <-release; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	called := false
	if err := withMachineTransaction(ctx, name, func() error { called = true; return nil }); !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("concurrent operation: %v, called=%v", err, called)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("callback failed")
	if err := withMachineTransaction(context.Background(), name, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatal(err)
	}
	if err := withMachineTransaction(context.Background(), name, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := withMachineTransaction(cancelled, name, func() error { t.Fatal("cancelled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestNativeMachineTransactionPreservesForeignAndAbandonedObjects refuses a differently granted object and reports an interrupted prior owner.
func TestNativeMachineTransactionPreservesForeignAndAbandonedObjects(t *testing.T) {
	for _, kind := range []string{"foreign", "abandoned"} {
		t.Run(kind, func(t *testing.T) {
			name := machineFixtureName(t)
			sddl := machineTransactionSDDL
			if kind == "foreign" {
				sddl += "(A;;0x1f0001;;;BU)"
			}
			descriptor, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
			type created struct {
				handle windows.Handle
				err    error
			}
			ready := make(chan created, 1)
			go func() {
				if kind == "abandoned" {
					runtime.LockOSThread()
				}
				flags := uint32(0)
				if kind == "abandoned" {
					flags = 1
				}
				handle, err := windows.CreateMutexEx(&attributes, windows.StringToUTF16Ptr(name), flags, windows.READ_CONTROL|windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
				runtime.KeepAlive(descriptor)
				ready <- created{handle, err}
				// A locked goroutine exiting terminates its OS thread and leaves the retained owned mutex abandoned.
			}()
			object := <-ready
			if object.err != nil {
				t.Fatal(object.err)
			}
			defer windows.CloseHandle(object.handle)
			called := false
			if err := withMachineTransaction(context.Background(), name, func() error { called = true; return nil }); err == nil || called {
				t.Fatalf("%s object admitted: %v, called=%v", kind, err, called)
			}
		})
	}
}

// TestTransactionDescriptorRejectsAuthorityDrift covers owner, group, protection and exact effective-grant admission before waiting or invoking a writer.
func TestTransactionDescriptorRejectsAuthorityDrift(t *testing.T) {
	for _, sddl := range []string{
		"O:SYG:BAD:P(A;;0x1f0001;;;BA)(A;;0x1f0001;;;SY)",
		"O:BAG:SYD:P(A;;0x1f0001;;;BA)(A;;0x1f0001;;;SY)",
		"O:BAG:BAD:(A;;0x1f0001;;;BA)(A;;0x1f0001;;;SY)",
		"O:BAG:BAD:P(A;;0x1f0001;;;BA)",
		"O:BAG:BAD:P(A;;0x1f0001;;;BA)(A;;0x100001;;;SY)",
		"O:BAG:BAD:P(A;OI;0x1f0001;;;BA)(A;;0x1f0001;;;SY)",
		"O:BAG:BAD:P(A;;0x1f0001;;;BA)(A;;0x1f0001;;;BA)",
		"O:BAG:BAD:P(A;;0x1f0001;;;BA)(A;;0x1f0001;;;BU)",
		"O:BAG:BAD:P(D;;0x1f0001;;;BA)(A;;0x1f0001;;;SY)",
	} {
		descriptor, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := admitTransactionDescriptor(descriptor); err == nil {
			t.Fatalf("foreign descriptor admitted: %s", sddl)
		}
	}
	descriptor, err := windows.SecurityDescriptorFromString(machineTransactionSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitTransactionDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
}
