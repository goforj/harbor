//go:build windows

package windowsinstallation

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestNativeRestrictedTokenRemovesAuthority proves actual entries are removed while SID and session remain unchanged.
func TestNativeRestrictedTokenRemovesAuthority(t *testing.T) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	token, err := restrictedRuntimeToken(source)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	facts, err := inspectRuntimeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	user, err := source.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var session uint32
	if err := tokenInteger(source, windows.TokenSessionId, &session); err != nil {
		t.Fatal(err)
	}
	if facts.UserSID != user.User.Sid.String() || facts.Session != session {
		t.Fatalf("restricted identity drift: %+v", facts)
	}
	t.Logf("restricted token facts: %+v", facts)
}

// TestNativeRuntimeTokenRejectsMandatoryPolicyDrift keeps observed token policy stricter than elevation metadata.
func TestNativeRuntimeTokenRejectsMandatoryPolicyDrift(t *testing.T) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	token, err := restrictedRuntimeToken(source)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	policy := uint32(0)
	if err := windows.SetTokenInformation(token, windows.TokenMandatoryPolicy, (*byte)(unsafe.Pointer(&policy)), 4); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntimeToken(token); err == nil {
		t.Fatal("token without no-write-up admitted")
	}
}

// TestNativeRuntimeTokenRejectsHighAuthority keeps the high hosted fixture outside the product worker contract.
func TestNativeRuntimeTokenRejectsHighAuthority(t *testing.T) {
	current := windows.GetCurrentProcessToken()
	if !current.IsElevated() {
		t.Skip("this account has no high fixture token")
	}
	if _, err := inspectRuntimeToken(current); err == nil {
		t.Fatal("original high token admitted")
	}
}
