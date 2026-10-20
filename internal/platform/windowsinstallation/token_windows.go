//go:build windows

package windowsinstallation

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

// TokenFacts records effective runtime authority separately from Windows elevation metadata.
type TokenFacts struct {
	// UserSID binds every installed child to the intended interactive user.
	UserSID string `json:"user_sid"`
	// OwnerSID must match TokenUser even when the source token defaulted to Administrators.
	OwnerSID string `json:"owner_sid"`
	// Session binds the process to the same interactive logon session.
	Session uint32 `json:"session"`
	// Integrity is the native mandatory integrity RID.
	Integrity uint32 `json:"integrity"`
	// MandatoryPolicy requires no-write-up enforcement.
	MandatoryPolicy uint32 `json:"mandatory_policy"`
	// UIAccess must remain zero for Harbor product roles.
	UIAccess uint32 `json:"ui_access"`
	// Elevation must be zero on the actual product worker.
	Elevation uint32 `json:"elevation"`
}

// InspectCurrentToken refuses effective administrative authority before any installed worker opens user data.
func InspectCurrentToken() (TokenFacts, error) {
	return inspectRuntimeToken(windows.GetCurrentProcessToken())
}

// inspectRuntimeToken admits only medium tokens with no enabled administrative group or removable privilege.
func inspectRuntimeToken(token windows.Token) (TokenFacts, error) {
	var facts TokenFacts
	user, err := token.GetTokenUser()
	if err != nil {
		return facts, err
	}
	facts.UserSID = user.User.Sid.String()
	ownerInformation, err := tokenInformation(token, windows.TokenOwner)
	if err != nil {
		return facts, err
	}
	owner := (*struct{ Owner *windows.SID })(unsafe.Pointer(&ownerInformation[0])).Owner
	if owner == nil || !owner.IsValid() {
		return facts, errors.New("Windows token default owner is invalid")
	}
	facts.OwnerSID = owner.String()
	runtime.KeepAlive(ownerInformation)
	if facts.UserSID == "S-1-5-18" {
		return facts, errors.New("Harbor installed roles cannot run as LocalSystem")
	}
	for class, destination := range map[uint32]*uint32{windows.TokenSessionId: &facts.Session, windows.TokenMandatoryPolicy: &facts.MandatoryPolicy, windows.TokenUIAccess: &facts.UIAccess, windows.TokenElevation: &facts.Elevation} {
		if err := tokenInteger(token, class, destination); err != nil {
			return facts, err
		}
	}
	integrity, err := tokenInformation(token, windows.TokenIntegrityLevel)
	if err != nil {
		return facts, err
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&integrity[0]))
	if label.Label.Sid == nil || !label.Label.Sid.IsValid() {
		return facts, errors.New("Windows token integrity SID is invalid")
	}
	if count := label.Label.Sid.SubAuthorityCount(); count != 1 {
		return facts, errors.New("Windows token integrity SID is not canonical")
	}
	facts.Integrity = label.Label.Sid.SubAuthority(0)
	if label.Label.Sid.String() != "S-1-16-8192" {
		return facts, errors.New("Windows token mandatory authority is not medium integrity")
	}
	if err := admitRuntimeFacts(facts); err != nil {
		return facts, err
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return facts, err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == administratorsSID && (group.Attributes&windows.SE_GROUP_ENABLED != 0 || group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0) {
			return facts, errors.New("Harbor installed role retains administrative authority")
		}
	}
	privileges, err := tokenInformation(token, windows.TokenPrivileges)
	if err != nil {
		return facts, err
	}
	var changeNotify windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeChangeNotifyPrivilege"), &changeNotify); err != nil {
		return facts, err
	}
	for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&privileges[0])).AllPrivileges() {
		if privilege.Luid != changeNotify {
			return facts, errors.New("Harbor installed role retains a removable privilege entry")
		}
	}
	runtime.KeepAlive(integrity)
	runtime.KeepAlive(privileges)
	return facts, nil
}

// restrictedRuntimeToken removes privileges rather than relying on disabled entries that can be enabled again.
func restrictedRuntimeToken(source windows.Token) (windows.Token, error) {
	if err := createRestrictedToken.Find(); err != nil {
		return 0, err
	}
	user, err := source.GetTokenUser()
	if err != nil {
		return 0, err
	}
	administrators, err := windows.StringToSid(administratorsSID)
	if err != nil {
		return 0, err
	}
	groups := []windows.SIDAndAttributes{{Sid: administrators}}
	buffer, err := tokenInformation(source, windows.TokenPrivileges)
	if err != nil {
		return 0, err
	}
	var changeNotify windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeChangeNotifyPrivilege"), &changeNotify); err != nil {
		return 0, err
	}
	var removed []windows.LUIDAndAttributes
	for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0])).AllPrivileges() {
		if privilege.Luid != changeNotify {
			removed = append(removed, windows.LUIDAndAttributes{Luid: privilege.Luid})
		}
	}
	var removedPointer *windows.LUIDAndAttributes
	if len(removed) != 0 {
		removedPointer = &removed[0]
	}
	var token windows.Token
	// LUA_TOKEN creates the documented filtered token; DISABLE_MAX_PRIVILEGE would ignore explicit removal.
	result, _, callErr := createRestrictedToken.Call(uintptr(source), 4, 1, uintptr(unsafe.Pointer(&groups[0])), uintptr(len(removed)), uintptr(unsafe.Pointer(removedPointer)), 0, 0, uintptr(unsafe.Pointer(&token)))
	runtime.KeepAlive(buffer)
	runtime.KeepAlive(groups)
	runtime.KeepAlive(removed)
	if result == 0 {
		if callErr == windows.ERROR_SUCCESS {
			return 0, errors.New("CreateRestrictedToken failed without an operating-system error")
		}
		return 0, callErr
	}

	// CreateRestrictedToken inherits the source handle's access mask. Duplicate only
	// the already restricted object so configuring it never requires modifying or
	// broadening access to the original source token.
	owned, err := duplicateRuntimeToken(token, user.User.Sid)
	closeErr := token.Close()
	if err := errors.Join(err, closeErr); err != nil {
		if owned != 0 {
			err = errors.Join(err, owned.Close())
		}
		return 0, err
	}
	if err := configureRuntimeToken(owned, user.User.Sid); err != nil {
		return 0, errors.Join(err, owned.Close())
	}
	return owned, nil
}

// configureRuntimeToken gives only the demoted user and SYSTEM authority over new private process objects.
func configureRuntimeToken(token windows.Token, owner *windows.SID) error {
	medium, err := windows.StringToSid("S-1-16-8192")
	if err != nil {
		return err
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(token, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		return fmt.Errorf("set Windows medium integrity: %w", err)
	}
	ownerInformation := struct{ Owner *windows.SID }{owner}
	if err := windows.SetTokenInformation(token, windows.TokenOwner, (*byte)(unsafe.Pointer(&ownerInformation)), uint32(unsafe.Sizeof(ownerInformation))); err != nil {
		return fmt.Errorf("set Windows current-user default owner: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;;GA;;;%s)(A;;GA;;;SY)", owner.String()))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	defaultDACL := struct{ DACL *windows.ACL }{dacl}
	if err := windows.SetTokenInformation(token, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&defaultDACL)), uint32(unsafe.Sizeof(defaultDACL))); err != nil {
		return fmt.Errorf("set Windows current-user default object DACL: %w", err)
	}
	_, err = inspectRuntimeToken(token)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(medium)
	runtime.KeepAlive(owner)
	return err
}

// tokenInteger retains checked native scalar observations without treating zero as an observation failure.
func tokenInteger(token windows.Token, class uint32, value *uint32) error {
	var length uint32
	if err := windows.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(value)), 4, &length); err != nil {
		return err
	}
	if length != 4 {
		return errors.New("Windows token scalar has an unexpected native size")
	}
	return nil
}

// tokenInformation bounds variable-length native token observations before allocating memory.
func tokenInformation(token windows.Token, class uint32) ([]byte, error) {
	var length uint32
	err := windows.GetTokenInformation(token, class, nil, 0, &length)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || length == 0 || length > 1024*1024 {
		return nil, fmt.Errorf("Windows token observation length %d: %w", length, err)
	}
	buffer := make([]byte, length)
	if err := windows.GetTokenInformation(token, class, &buffer[0], length, &length); err != nil {
		return nil, err
	}
	return buffer[:length], nil
}

// admitRuntimeFacts separates native observation from scalar policy rejection so every invariant has a direct test.
func admitRuntimeFacts(facts TokenFacts) error {
	if facts.UserSID == "" || facts.UserSID == "S-1-5-18" || facts.OwnerSID != facts.UserSID {
		return errors.New("Windows token default owner differs from its interactive user SID")
	}
	if facts.Session == 0 || facts.Integrity != 8192 || facts.MandatoryPolicy&1 == 0 || facts.UIAccess != 0 || facts.Elevation != 0 {
		return errors.New("Harbor installed role requires interactive medium integrity, no-write-up and no UIAccess")
	}
	return nil
}

// duplicateRuntimeToken authors private configuration rights on only an already restricted token object.
func duplicateRuntimeToken(source windows.Token, user *windows.SID) (windows.Token, error) {
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;;0xf01ff;;;%s)(A;;0xf01ff;;;SY)", user.String(), user.String()))
	if err != nil {
		return 0, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	var token windows.Token
	err = windows.DuplicateTokenEx(source, windows.TOKEN_ALL_ACCESS, &attributes, windows.SecurityImpersonation, windows.TokenPrimary, &token)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return 0, fmt.Errorf("duplicate already restricted Windows token: %w", err)
	}
	return token, nil
}
