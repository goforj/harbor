//go:build windows

package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"golang.org/x/sys/windows"
)

const (
	windowsRootStoreName        = "ROOT"
	windowsFriendlyNameProperty = 11
	maximumWindowsFriendlyBytes = (maximumNativeIDLength + 1) * 2
	windowsCertificateEncodings = windows.X509_ASN_ENCODING | windows.PKCS_7_ASN_ENCODING
)

var (
	windowsCrypt32                           = windows.NewLazySystemDLL("crypt32.dll")
	windowsCertGetCertificateContextProperty = windowsCrypt32.NewProc("CertGetCertificateContextProperty")
	windowsCertSetCertificateContextProperty = windowsCrypt32.NewProc("CertSetCertificateContextProperty")
)

// windowsRootStore implements one exact Windows Root certificate and marker boundary.
type windowsRootStore struct {
	mechanism networkpolicy.TrustMechanism
}

// New returns the reviewed Windows CurrentUser\Root trust adapter.
func New() (*Adapter, error) {
	store := windowsRootStore{mechanism: networkpolicy.WindowsCurrentUserTrust}
	return newAdapter(newWindowsTrustBackend(store.mechanism, store)), nil
}

// NewMachine returns the reviewed Windows LocalMachine\Root trust adapter.
func NewMachine() (*Adapter, error) {
	store := windowsRootStore{mechanism: networkpolicy.WindowsMachineTrust}
	return newAdapter(newWindowsTrustBackend(store.mechanism, store)), nil
}

// snapshot returns only the authority certificate or Harbor-marked entries relevant to this request.
func (store windowsRootStore) snapshot(ctx context.Context, request Request) ([]windowsTrustEntry, error) {
	if err := validateWindowsTrustRequest(request, store.mechanism); err != nil {
		return nil, err
	}
	defaultStore, err := store.open()
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(defaultStore, 0)
	entries := make([]windowsTrustEntry, 0, 2)
	seen := make(map[string]struct{}, 2)
	if err := appendWindowsTrustEntries(ctx, request, defaultStore, seen, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// appendWindowsTrustEntries adds bounded relevant facts while collapsing duplicate logical-store entries.
func appendWindowsTrustEntries(
	ctx context.Context,
	request Request,
	store windows.Handle,
	seen map[string]struct{},
	entries *[]windowsTrustEntry,
) error {
	var previous *windows.CertContext
	for {
		if err := ctx.Err(); err != nil {
			if previous != nil {
				_ = windows.CertFreeCertificateContext(previous)
			}
			return err
		}
		certificate, enumErr := windows.CertEnumCertificatesInStore(store, previous)
		previous = nil
		if enumErr != nil {
			if windowsCertificateEnumerationComplete(enumErr) {
				break
			}
			return fmt.Errorf("enumerate CurrentUser Root certificates: %w", enumErr)
		}
		previous = certificate
		der, err := windowsCertificateDER(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			return err
		}
		friendlyName, _, err := windowsCertificateFriendlyName(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			return err
		}
		fingerprint := sha256.Sum256(der)
		fingerprintText := hex.EncodeToString(fingerprint[:])
		if fingerprintText != request.AuthorityFingerprint() &&
			!strings.HasPrefix(friendlyName, windowsTrustOwnerPrefix(request.Mechanism())) {
			continue
		}
		key := fingerprintText + "\x00" + friendlyName
		if _, ok := seen[key]; ok {
			continue
		}
		if len(*entries) == maximumTrustEntries {
			_ = windows.CertFreeCertificateContext(certificate)
			return fmt.Errorf("Windows trust store exceeds relevant entry limit %d", maximumTrustEntries)
		}
		seen[key] = struct{}{}
		*entries = append(*entries, windowsTrustEntry{CertificateDER: der, FriendlyName: friendlyName})
	}
	return nil
}

// ensure writes one marked certificate through the backend's exact Root store.
func (store windowsRootStore) ensure(ctx context.Context, request Request) error {
	if err := validateWindowsTrustRequest(request, store.mechanism); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := store.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if len(before) != 0 {
		return fmt.Errorf("CurrentUser Root authority changed before ensure: %w", errNativeMutationConflict)
	}
	der, err := windowsRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	certificate, err := windows.CertCreateCertificateContext(windowsCertificateEncodings, &der[0], uint32(len(der)))
	if err != nil {
		return fmt.Errorf("create Windows certificate context: %w", err)
	}
	defer windows.CertFreeCertificateContext(certificate)
	if err := setWindowsCertificateFriendlyName(certificate, windowsTrustOwnerName(request)); err != nil {
		return err
	}
	rootStore, err := store.open()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(rootStore, 0)

	var stored *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(rootStore, certificate, windows.CERT_STORE_ADD_NEW, &stored); err != nil {
		return fmt.Errorf("add CurrentUser Root certificate: %w: %v", errNativeMutationConflict, err)
	}
	if stored == nil {
		return errors.New("CurrentUser Root insertion returned no certificate context")
	}
	defer windows.CertFreeCertificateContext(stored)
	return nil
}

// release revalidates one exact marked certificate immediately before deleting that context.
func (store windowsRootStore) release(ctx context.Context, request Request) error {
	if err := validateWindowsTrustRequest(request, store.mechanism); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootStore, err := store.open()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(rootStore, 0)

	var selected *windows.CertContext
	var previous *windows.CertContext
	matches := 0
	for {
		certificate, enumErr := windows.CertEnumCertificatesInStore(rootStore, previous)
		previous = nil
		if enumErr != nil {
			if windowsCertificateEnumerationComplete(enumErr) {
				break
			}
			if selected != nil {
				_ = windows.CertFreeCertificateContext(selected)
			}
			return fmt.Errorf("enumerate CurrentUser Root certificates for release: %w", enumErr)
		}
		previous = certificate
		der, err := windowsCertificateDER(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			if selected != nil {
				_ = windows.CertFreeCertificateContext(selected)
			}
			return err
		}
		fingerprint := sha256.Sum256(der)
		if hex.EncodeToString(fingerprint[:]) != request.AuthorityFingerprint() {
			continue
		}
		friendlyName, present, err := windowsCertificateFriendlyName(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			if selected != nil {
				_ = windows.CertFreeCertificateContext(selected)
			}
			return err
		}
		if !present || friendlyName != windowsTrustOwnerName(request) {
			continue
		}
		matches++
		if matches == 1 {
			selected = windows.CertDuplicateCertificateContext(certificate)
			if selected == nil {
				_ = windows.CertFreeCertificateContext(certificate)
				return errors.New("duplicate owned CurrentUser Root certificate context")
			}
		}
	}
	if matches != 1 || selected == nil {
		if selected != nil {
			_ = windows.CertFreeCertificateContext(selected)
		}
		return fmt.Errorf("CurrentUser Root owned certificate changed before release: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		_ = windows.CertFreeCertificateContext(selected)
		return err
	}
	if err := windows.CertDeleteCertificateFromStore(selected); err != nil {
		return fmt.Errorf("delete owned CurrentUser Root certificate: %w", err)
	}
	return nil
}

// openWindowsCurrentUserRootStore opens only the interactive account's standard root store.
func openWindowsCurrentUserRootStore() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(windowsRootStoreName)
	if err != nil {
		return 0, fmt.Errorf("encode CurrentUser Root store name: %w", err)
	}
	store, err := windows.CertOpenSystemStore(0, name)
	if err != nil {
		return 0, fmt.Errorf("open CurrentUser Root certificate store: %w", err)
	}
	return store, nil
}

// open returns only the physical Root store selected by the backend's reviewed trust scope.
func (store windowsRootStore) open() (windows.Handle, error) {
	switch store.mechanism {
	case networkpolicy.WindowsCurrentUserTrust:
		return openWindowsCurrentUserRootStore()
	case networkpolicy.WindowsMachineTrust:
		return openWindowsMachineRootStore()
	default:
		return 0, fmt.Errorf("open Windows Root store for unsupported mechanism %q", store.mechanism)
	}
}

// openWindowsMachineRootStore opens only the machine registry Root store used by the elevated helper.
func openWindowsMachineRootStore() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(windowsRootStoreName)
	if err != nil {
		return 0, fmt.Errorf("encode LocalMachine Root store name: %w", err)
	}
	store, err := windows.CertOpenStore(
		uintptr(windows.CERT_STORE_PROV_SYSTEM_REGISTRY),
		0,
		0,
		uint32(windows.CERT_SYSTEM_STORE_LOCAL_MACHINE),
		uintptr(unsafe.Pointer(name)),
	)
	if err != nil {
		return 0, fmt.Errorf("open LocalMachine Root registry store: %w", err)
	}
	return store, nil
}

// windowsCertificateDER copies one bounded certificate before its enumeration context is released.
func windowsCertificateDER(certificate *windows.CertContext) ([]byte, error) {
	if certificate == nil || certificate.EncodedCert == nil || certificate.Length == 0 ||
		certificate.Length > maximumCertificatePEMBytes {
		return nil, errors.New("CurrentUser Root returned an invalid certificate context")
	}
	return append([]byte(nil), unsafe.Slice(certificate.EncodedCert, int(certificate.Length))...), nil
}

// windowsCertificateFriendlyName reads one bounded UTF-16 ownership property.
func windowsCertificateFriendlyName(certificate *windows.CertContext) (string, bool, error) {
	var size uint32
	result, _, callErr := windowsCertGetCertificateContextProperty.Call(
		uintptr(unsafe.Pointer(certificate)),
		windowsFriendlyNameProperty,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		if windowsPropertyNotFound(callErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read CurrentUser Root friendly-name size: %w", callErr)
	}
	if size < 2 || size > maximumWindowsFriendlyBytes || size%2 != 0 {
		return "", false, fmt.Errorf("CurrentUser Root friendly-name property has invalid size %d", size)
	}
	buffer := make([]uint16, size/2)
	result, _, callErr = windowsCertGetCertificateContextProperty.Call(
		uintptr(unsafe.Pointer(certificate)),
		windowsFriendlyNameProperty,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if result == 0 {
		return "", false, fmt.Errorf("read CurrentUser Root friendly name: %w", callErr)
	}
	if buffer[len(buffer)-1] != 0 {
		return "", false, errors.New("CurrentUser Root friendly name is not null terminated")
	}
	return windows.UTF16ToString(buffer), true, nil
}

// setWindowsCertificateFriendlyName writes the complete canonical owner marker to one stored context.
func setWindowsCertificateFriendlyName(certificate *windows.CertContext, value string) error {
	encoded, err := windows.UTF16FromString(value)
	if err != nil {
		return fmt.Errorf("encode CurrentUser Root owner marker: %w", err)
	}
	if len(encoded)*2 > maximumWindowsFriendlyBytes {
		return errors.New("CurrentUser Root owner marker is oversized")
	}
	blob := windows.CryptDataBlob{Size: uint32(len(encoded) * 2), Data: (*byte)(unsafe.Pointer(&encoded[0]))}
	result, _, callErr := windowsCertSetCertificateContextProperty.Call(
		uintptr(unsafe.Pointer(certificate)),
		windowsFriendlyNameProperty,
		0,
		uintptr(unsafe.Pointer(&blob)),
	)
	if result == 0 {
		return fmt.Errorf("write CurrentUser Root owner marker: %w", callErr)
	}
	return nil
}

// windowsCertificateEnumerationComplete recognizes CryptoAPI's ordinary end-of-store result.
func windowsCertificateEnumerationComplete(err error) bool {
	return windowsErrorCode(err) == uintptr(windows.CRYPT_E_NOT_FOUND)
}

// windowsPropertyNotFound recognizes an absent optional certificate property.
func windowsPropertyNotFound(err error) bool {
	return windowsErrorCode(err) == uintptr(windows.CRYPT_E_NOT_FOUND)
}

// windowsErrorCode extracts one native syscall status without accepting wrapped unrelated errors.
func windowsErrorCode(err error) uintptr {
	var status syscall.Errno
	if errors.As(err, &status) {
		return uintptr(status)
	}
	return 0
}
