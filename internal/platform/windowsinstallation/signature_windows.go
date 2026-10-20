//go:build windows

package windowsinstallation

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wintrust            = windows.NewLazySystemDLL("wintrust.dll")
	providerData        = wintrust.NewProc("WTHelperProvDataFromStateData")
	providerSigner      = wintrust.NewProc("WTHelperGetProvSignerFromChain")
	providerCertificate = wintrust.NewProc("WTHelperGetProvCertFromChain")
)

// providerCertificatePrefix is the documented CRYPT_PROVIDER_CERT prefix needed to pin the verified leaf.
type providerCertificatePrefix struct {
	size        uint32
	certificate *windows.CertContext
}

// verifySignature checks the retained PE or script and pins the leaf from that exact live trust-provider state.
func verifySignature(file *os.File, publisher string) (result error) {
	for _, procedure := range []*windows.LazyProc{providerData, providerSigner, providerCertificate} {
		if err := procedure.Find(); err != nil {
			return fmt.Errorf("resolve Windows Authenticode provider: %w", err)
		}
	}
	pointer, err := windows.UTF16PtrFromString(file.Name())
	if err != nil {
		return err
	}
	information := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: pointer, File: windows.Handle(file.Fd())}
	trust := &windows.WinTrustData{Size: uint32(unsafe.Sizeof(windows.WinTrustData{})), UIChoice: windows.WTD_UI_NONE, RevocationChecks: windows.WTD_REVOKE_NONE, UnionChoice: windows.WTD_CHOICE_FILE, FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(information), StateAction: windows.WTD_STATEACTION_VERIFY, ProvFlags: windows.WTD_CACHE_ONLY_URL_RETRIEVAL | windows.WTD_DISABLE_MD2_MD4, UIContext: windows.WTD_UICONTEXT_EXECUTE}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, trust)
	defer func() {
		trust.StateAction = windows.WTD_STATEACTION_CLOSE
		result = errors.Join(result, windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, trust))
		runtime.KeepAlive(information)
		runtime.KeepAlive(file)
	}()
	if verifyErr != nil {
		return fmt.Errorf("verify Windows Authenticode trust: %w", verifyErr)
	}
	data, _, _ := providerData.Call(uintptr(trust.StateData))
	if data == 0 {
		return errors.New("Windows Authenticode provider returned no verified state")
	}
	signer, _, _ := providerSigner.Call(data, 0, 0, 0)
	if signer == 0 {
		return errors.New("Windows Authenticode provider returned no primary signer")
	}
	native, _, _ := providerCertificate.Call(signer, 0)
	if native == 0 {
		return errors.New("Windows Authenticode provider returned no primary certificate")
	}
	certificate, err := readProviderCertificatePrefix(native)
	if err != nil {
		return err
	}
	if certificate.EncodedCert == nil || certificate.Length == 0 || certificate.Length > 64*1024 {
		return errors.New("Windows Authenticode primary certificate exceeds bounds")
	}
	digest := sha1.Sum(unsafe.Slice(certificate.EncodedCert, int(certificate.Length)))
	if strings.ToUpper(hex.EncodeToString(digest[:])) != publisher {
		return errors.New("Windows executable or script signer differs from compiled installed publisher")
	}
	return nil
}

// readProviderCertificatePrefix bounds native reads while the caller retains the owning verified trust state.
func readProviderCertificatePrefix(address uintptr) (*windows.CertContext, error) {
	var prefix providerCertificatePrefix
	var copied uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&prefix)), unsafe.Sizeof(prefix), &copied); err != nil {
		return nil, fmt.Errorf("read verified Windows provider certificate: %w", err)
	}
	if copied != unsafe.Sizeof(prefix) || prefix.size < uint32(unsafe.Sizeof(prefix)) || prefix.certificate == nil {
		return nil, errors.New("Windows Authenticode primary certificate has an invalid layout")
	}
	return prefix.certificate, nil
}
