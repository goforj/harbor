//go:build windows

package replaystore

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const windowsTestInteractiveSID = "S-1-5-21-1000-2000-3000-1001"

// TestReplayMachineDescriptorUsesMachinePrincipals verifies user identity never enters the replay boundary.
func TestReplayMachineDescriptorUsesMachinePrincipals(t *testing.T) {
	for _, directory := range []bool{false, true} {
		descriptor, owner, err := replayMachineDescriptor(directory)
		if err != nil {
			t.Fatalf("replayMachineDescriptor(%t) error = %v", directory, err)
		}
		if owner.String() != windowsAdministratorsSID {
			t.Fatalf("replayMachineDescriptor(%t) owner = %q, want %q", directory, owner.String(), windowsAdministratorsSID)
		}
		if err := validateWindowsDescriptor(descriptor, directory); err != nil {
			t.Fatalf("validateWindowsDescriptor(%t) error = %v", directory, err)
		}
	}
}

// TestValidateWindowsDescriptorRejectsInteractiveUserAccess proves a filtered caller's stable user SID is never authorized.
func TestValidateWindowsDescriptorRejectsInteractiveUserAccess(t *testing.T) {
	interactiveSID := replayTestInteractiveSID(t)
	descriptor := replayTestDescriptor(t, "O:"+windowsAdministratorsSID+"D:P(A;;FA;;;"+interactiveSID+")(A;;FA;;;"+windowsSystemSID+")")
	err := validateWindowsDescriptor(descriptor, false)
	if err == nil || !strings.Contains(err.Error(), "unexpected or duplicate SID") {
		t.Fatalf("validateWindowsDescriptor(interactive SID) error = %v", err)
	}
}

// TestValidateWindowsDescriptorRejectsInteractiveUserOwnership prevents owner rights from bypassing the machine DACL.
func TestValidateWindowsDescriptorRejectsInteractiveUserOwnership(t *testing.T) {
	interactiveSID := replayTestInteractiveSID(t)
	descriptor := replayTestDescriptor(t, "O:"+interactiveSID+"D:P(A;;FA;;;"+windowsAdministratorsSID+")(A;;FA;;;"+windowsSystemSID+")")
	err := validateWindowsDescriptor(descriptor, false)
	if err == nil || !strings.Contains(err.Error(), "want Windows Administrators") {
		t.Fatalf("validateWindowsDescriptor(interactive owner) error = %v", err)
	}
}

// TestValidateWindowsDescriptorRequiresProtectedExactDACL rejects inheritance and generic access aliases.
func TestValidateWindowsDescriptorRequiresProtectedExactDACL(t *testing.T) {
	for _, test := range []struct {
		name string
		sddl string
		want string
	}{
		{
			name: "unprotected",
			sddl: "O:" + windowsAdministratorsSID + "D:(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "not protected",
		},
		{
			name: "generic all",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;GA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")",
			want: "access mask",
		},
		{
			name: "extra principal",
			sddl: "O:" + windowsAdministratorsSID + "D:P(A;;FA;;;" + windowsAdministratorsSID + ")(A;;FA;;;" + windowsSystemSID + ")(A;;FR;;;WD)",
			want: "exactly two entries",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := replayTestDescriptor(t, test.sddl)
			err := validateWindowsDescriptor(descriptor, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateWindowsDescriptor() error = %v, want %q", err, test.want)
			}
		})
	}
}

// replayTestDescriptor parses one self-relative descriptor for direct access-list assertions.
func replayTestDescriptor(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("windows.SecurityDescriptorFromString() error = %v", err)
	}
	return descriptor
}

// replayTestInteractiveSID prefers the executing user while retaining a non-machine fallback for system-run tests.
func replayTestInteractiveSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("windows.Token.GetTokenUser() error = %v", err)
	}
	principal := user.User.Sid.String()
	if principal == windowsAdministratorsSID || principal == windowsSystemSID {
		return windowsTestInteractiveSID
	}
	return principal
}
