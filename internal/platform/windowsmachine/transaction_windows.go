//go:build windows

// Package windowsmachine serializes the fixed privileged Windows writer boundary without application dependencies.
package windowsmachine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const machineTransactionName = `Global\GoForj.Harbor.InstallationTransaction.v1`
const machineTransactionSDDL = "O:BAG:BAD:P(A;;0x1f0001;;;BA)(A;;0x1f0001;;;SY)"

// WithTransaction excludes every installed helper writer and privileged installation lifecycle operation.
func WithTransaction(ctx context.Context, operation func() error) error {
	return withMachineTransaction(ctx, machineTransactionName, operation)
}

// withMachineTransaction permits unique native fixture names without redirecting product authority through environment or arguments.
func withMachineTransaction(ctx context.Context, name string, operation func() error) (result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString(machineTransactionSDDL)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	mutex, err := windows.CreateMutexEx(&attributes, windows.StringToUTF16Ptr(name), 0, windows.READ_CONTROL|windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	runtime.KeepAlive(descriptor)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(mutex)) }()
	actual, err := windows.GetSecurityInfo(mutex, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if err := admitTransactionDescriptor(actual); err != nil {
		return fmt.Errorf("preserve foreign machine transaction object: %w", err)
	}
	// Windows mutex ownership belongs to an OS thread, including while Go callbacks block or schedule other goroutines.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := windows.WaitForSingleObject(mutex, 50)
		if err != nil {
			return err
		}
		switch state {
		case windows.WAIT_OBJECT_0:
			defer func() { result = errors.Join(result, windows.ReleaseMutex(mutex)) }()
			return operation()
		case windows.WAIT_ABANDONED:
			return errors.Join(errors.New("previous Harbor machine transaction exited without settlement; retry only after inspecting installation state"), windows.ReleaseMutex(mutex))
		case uint32(windows.WAIT_TIMEOUT):
		default:
			return errors.New("unexpected machine transaction wait state")
		}
	}
}

// admitTransactionDescriptor rejects a foreign lock instead of adopting or repairing its privileged authority.
func admitTransactionDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	group, _, err := descriptor.Group()
	if err != nil {
		return err
	}
	if owner == nil || group == nil || owner.String() != "S-1-5-32-544" || group.String() != "S-1-5-32-544" {
		return errors.New("foreign transaction owner or group")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("transaction DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 2 {
		return errors.New("transaction grants differ")
	}
	seen := make(map[string]bool, 2)
	for index := uint32(0); index < 2; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || uint32(ace.Mask) != 0x1f0001 {
			return errors.New("transaction grant differs")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if (sid != "S-1-5-32-544" && sid != "S-1-5-18") || seen[sid] {
			return errors.New("transaction principal differs or is duplicated")
		}
		seen[sid] = true
	}
	return nil
}
