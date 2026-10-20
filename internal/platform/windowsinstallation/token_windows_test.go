//go:build windows

package windowsinstallation

import (
	"fmt"
	"runtime"
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

// TestRuntimeFactsRejectAuthorityDrift covers scalar policy failures that Windows does not let an ordinary caller author.
func TestRuntimeFactsRejectAuthorityDrift(t *testing.T) {
	valid := TokenFacts{UserSID: "S-1-5-21-1-2-3-1000", OwnerSID: "S-1-5-21-1-2-3-1000", Session: 1, Integrity: 8192, MandatoryPolicy: 3}
	if err := admitRuntimeFacts(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*TokenFacts){func(f *TokenFacts) { f.OwnerSID = administratorsSID }, func(f *TokenFacts) { f.UserSID = "" }, func(f *TokenFacts) { f.UserSID = systemSID; f.OwnerSID = systemSID }, func(f *TokenFacts) { f.Session = 0 }, func(f *TokenFacts) { f.Integrity = 12288 }, func(f *TokenFacts) { f.MandatoryPolicy = 2 }, func(f *TokenFacts) { f.UIAccess = 1 }, func(f *TokenFacts) { f.Elevation = 1 }} {
		changed := valid
		mutate(&changed)
		if admitRuntimeFacts(changed) == nil {
			t.Fatalf("authority drift admitted: %+v", changed)
		}
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

// TestNativeRestrictedDefaultDACL gives only newly derived objects user ownership without mutating the source token.
func TestNativeRestrictedDefaultDACL(t *testing.T) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	original := nativeTokenDefaultDACL(t, source)
	token, err := restrictedRuntimeToken(source)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := source.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;;GA;;;%s)(A;;GA;;;SY)", user.User.Sid.String()))
	if err != nil {
		t.Fatal(err)
	}
	if actual := nativeTokenDefaultDACL(t, token); actual != expected.String() {
		t.Fatalf("restricted default object DACL=%s expected %s", actual, expected.String())
	}
	if actual := nativeTokenDefaultDACL(t, source); actual != original {
		t.Fatalf("source default DACL changed: %s became %s", original, actual)
	}
}

// nativeTokenDefaultDACL serializes a token's copied default ACL while its native backing bytes remain alive.
func nativeTokenDefaultDACL(t *testing.T, token windows.Token) string {
	t.Helper()
	information, err := tokenInformation(token, windows.TokenDefaultDacl)
	if err != nil {
		t.Fatal(err)
	}
	defaultDACL := (*struct{ DACL *windows.ACL })(unsafe.Pointer(&information[0]))
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if err := descriptor.SetDACL(defaultDACL.DACL, true, false); err != nil {
		t.Fatal(err)
	}
	serialized := descriptor.String()
	runtime.KeepAlive(information)
	return serialized
}
