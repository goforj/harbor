//go:build windows

package ticketredeemer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const windowsTestUserSID = "S-1-5-21-1-2-3-1001"

// TestWindowsDescriptorPoliciesAcceptOnlyExactPrincipalsAndRights verifies every installer policy independently.
func TestWindowsDescriptorPoliciesAcceptOnlyExactPrincipalsAndRights(t *testing.T) {
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	cases := []struct {
		name   string
		sddl   string
		owner  string
		policy map[string]windowsACEPolicy
	}{
		{
			name:  "gateway",
			sddl:  fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)(A;OICI;0x1200a0;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID, windowsTestUserSID),
			owner: windowsAdministratorsSID,
			policy: map[string]windowsACEPolicy{
				windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
				windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
				windowsTestUserSID:       {mask: windowsFileExecuteAccess, flags: inherit},
			},
		},
		{
			name:  "pending directory",
			sddl:  fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)", windowsTestUserSID, windowsTestUserSID, windowsAdministratorsSID, windowsSystemSID),
			owner: windowsTestUserSID,
			policy: map[string]windowsACEPolicy{
				windowsTestUserSID:       {mask: windowsFileAllAccess, flags: inherit},
				windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
				windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
			},
		},
		{
			name:  "pending file",
			sddl:  fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)(A;;FA;;;%s)", windowsTestUserSID, windowsTestUserSID, windowsAdministratorsSID, windowsSystemSID),
			owner: windowsTestUserSID,
			policy: map[string]windowsACEPolicy{
				windowsTestUserSID:       {mask: windowsFileAllAccess},
				windowsAdministratorsSID: {mask: windowsFileAllAccess},
				windowsSystemSID:         {mask: windowsFileAllAccess},
			},
		},
		{
			name:  "machine directory",
			sddl:  fmt.Sprintf("O:%sD:P(A;OICI;FA;;;%s)(A;OICI;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID),
			owner: windowsAdministratorsSID,
			policy: map[string]windowsACEPolicy{
				windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: inherit},
				windowsSystemSID:         {mask: windowsFileAllAccess, flags: inherit},
			},
		},
		{
			name:  "machine file",
			sddl:  fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID),
			owner: windowsAdministratorsSID,
			policy: map[string]windowsACEPolicy{
				windowsAdministratorsSID: {mask: windowsFileAllAccess},
				windowsSystemSID:         {mask: windowsFileAllAccess},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatalf("SecurityDescriptorFromString() error = %v", err)
			}
			if err := validateWindowsDescriptor(descriptor, test.owner, test.policy); err != nil {
				t.Fatalf("validateWindowsDescriptor() error = %v", err)
			}
		})
	}
}

// TestWindowsDescriptorPoliciesRejectEveryBroadening prevents generic aliases and inherited or extra authority.
func TestWindowsDescriptorPoliciesRejectEveryBroadening(t *testing.T) {
	policy := map[string]windowsACEPolicy{
		windowsAdministratorsSID: {mask: windowsFileAllAccess},
		windowsSystemSID:         {mask: windowsFileAllAccess},
	}
	cases := []struct {
		name string
		sddl string
	}{
		{name: "unprotected", sddl: fmt.Sprintf("O:%sD:(A;;FA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "wrong owner", sddl: fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)", windowsSystemSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "missing", sddl: fmt.Sprintf("O:%sD:P(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID)},
		{name: "extra", sddl: fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)(A;;FA;;;WD)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "generic alias", sddl: fmt.Sprintf("O:%sD:P(A;;GA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "wrong flags", sddl: fmt.Sprintf("O:%sD:P(A;CI;FA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "denied", sddl: fmt.Sprintf("O:%sD:P(D;;FA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsSystemSID)},
		{name: "duplicate", sddl: fmt.Sprintf("O:%sD:P(A;;FA;;;%s)(A;;FA;;;%s)", windowsAdministratorsSID, windowsAdministratorsSID, windowsAdministratorsSID)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatalf("SecurityDescriptorFromString() error = %v", err)
			}
			if err := validateWindowsDescriptor(descriptor, windowsAdministratorsSID, policy); err == nil {
				t.Fatal("validateWindowsDescriptor() accepted broadened policy")
			}
		})
	}
}

// TestWindowsMachineDescriptorAndRenameEncoding preserve exact native buffer and policy construction.
func TestWindowsMachineDescriptorAndRenameEncoding(t *testing.T) {
	for _, directory := range []bool{false, true} {
		descriptor, owner, err := windowsMachineDescriptor(directory)
		if err != nil {
			t.Fatalf("windowsMachineDescriptor(%t) error = %v", directory, err)
		}
		flags := uint8(0)
		if directory {
			flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if err := validateWindowsDescriptor(descriptor, owner.String(), map[string]windowsACEPolicy{
			windowsAdministratorsSID: {mask: windowsFileAllAccess, flags: flags},
			windowsSystemSID:         {mask: windowsFileAllAccess, flags: flags},
		}); err != nil {
			t.Fatalf("validateWindowsDescriptor(machine) error = %v", err)
		}
	}
	buffer, err := windowsRenameBuffer(42, strings.Repeat("a", 64))
	if err != nil || len(buffer) <= 64*2 {
		t.Fatalf("windowsRenameBuffer() length = %d, error = %v", len(buffer), err)
	}
	if _, err := windowsRenameBuffer(42, "embedded\x00null"); err == nil {
		t.Fatal("windowsRenameBuffer() accepted an embedded NUL")
	}
}

// TestWindowsNativeErrorAndProcessAdmission keep NT classifications and elevation proof explicit.
func TestWindowsNativeErrorAndProcessAdmission(t *testing.T) {
	if !errors.Is(windowsNativeError(windows.STATUS_OBJECT_NAME_COLLISION), windows.ERROR_FILE_EXISTS) {
		t.Fatal("windowsNativeError() lost collision classification")
	}
	if !errors.Is(windowsNativeError(windows.STATUS_OBJECT_NAME_NOT_FOUND), windows.ERROR_FILE_NOT_FOUND) {
		t.Fatal("windowsNativeError() lost missing-name classification")
	}
	if !errors.Is(windowsNativeError(windows.STATUS_OBJECT_PATH_NOT_FOUND), windows.ERROR_PATH_NOT_FOUND) {
		t.Fatal("windowsNativeError() lost missing-path classification")
	}
	cause := errors.New("native error")
	if !errors.Is(windowsNativeError(cause), cause) {
		t.Fatal("windowsNativeError() changed a non-status error")
	}

	token := windows.GetCurrentProcessToken()
	administrators, err := windows.StringToSid(windowsAdministratorsSID)
	if err != nil {
		t.Fatalf("StringToSid() error = %v", err)
	}
	member, err := token.IsMember(administrators)
	if err != nil {
		t.Fatalf("IsMember() error = %v", err)
	}
	wantAdmitted := token.IsElevated() && member
	if admitted := validatePlatformProcessAdmission() == nil; admitted != wantAdmitted {
		t.Fatalf("validatePlatformProcessAdmission() admitted = %t, want %t", admitted, wantAdmitted)
	}
}

// TestWindowsOrdinaryTemporaryObjectsCannotSatisfyMachinePolicy proves inherited user state fails closed.
func TestWindowsOrdinaryTemporaryObjectsCannotSatisfyMachinePolicy(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open temporary directory: %v", err)
	}
	if err := validatePlatformMachineDirectory(directory); err == nil {
		t.Fatal("validatePlatformMachineDirectory() accepted inherited user state")
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close temporary directory: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ticket")
	if err := os.WriteFile(path, []byte("ticket"), 0o600); err != nil {
		t.Fatalf("write temporary file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open temporary file: %v", err)
	}
	if err := validatePlatformMachineFile(file); err == nil {
		t.Fatal("validatePlatformMachineFile() accepted inherited user state")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close temporary file: %v", err)
	}
}
