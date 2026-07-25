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

// windowsCurrentUserRootStore implements the exact CurrentUser\Root certificate and marker boundary.
type windowsCurrentUserRootStore struct{}

// New returns the reviewed Windows CurrentUser\Root trust adapter.
func New() (*Adapter, error) {
	return newAdapter(newWindowsTrustBackend(windowsCurrentUserRootStore{})), nil
}

// snapshot returns only the authority certificate or Harbor-marked entries relevant to this request.
func (windowsCurrentUserRootStore) snapshot(ctx context.Context, request Request) ([]windowsTrustEntry, error) {
	if err := validateWindowsTrustRequest(request); err != nil {
		return nil, err
	}
	store, err := openWindowsCurrentUserRootStore()
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(store, 0)

	entries := make([]windowsTrustEntry, 0, 2)
	var previous *windows.CertContext
	for {
		if err := ctx.Err(); err != nil {
			if previous != nil {
				_ = windows.CertFreeCertificateContext(previous)
			}
			return nil, err
		}
		certificate, enumErr := windows.CertEnumCertificatesInStore(store, previous)
		previous = nil
		if enumErr != nil {
			if windowsCertificateEnumerationComplete(enumErr) {
				break
			}
			return nil, fmt.Errorf("enumerate CurrentUser Root certificates: %w", enumErr)
		}
		previous = certificate
		der, err := windowsCertificateDER(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			return nil, err
		}
		friendlyName, _, err := windowsCertificateFriendlyName(certificate)
		if err != nil {
			_ = windows.CertFreeCertificateContext(certificate)
			return nil, err
		}
		fingerprint := sha256.Sum256(der)
		if hex.EncodeToString(fingerprint[:]) != request.AuthorityFingerprint() &&
			!strings.HasPrefix(friendlyName, windowsTrustOwnerPrefix) {
			continue
		}
		if len(entries) == maximumTrustEntries {
			_ = windows.CertFreeCertificateContext(certificate)
			return nil, fmt.Errorf("Windows trust store exceeds relevant entry limit %d", maximumTrustEntries)
		}
		entries = append(entries, windowsTrustEntry{CertificateDER: der, FriendlyName: friendlyName})
	}
	return entries, nil
}

// ensure adds one absent certificate and rolls it back unless the returned stored context accepts Harbor ownership.
func (store windowsCurrentUserRootStore) ensure(ctx context.Context, request Request) error {
	if err := validateWindowsTrustRequest(request); err != nil {
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

	rootStore, err := openWindowsCurrentUserRootStore()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(rootStore, 0)

	var stored *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(rootStore, certificate, windows.CERT_STORE_ADD_NEW, &stored); err != nil {
		return fmt.Errorf("add CurrentUser Root certificate: %w: %v", errNativeMutationConflict, err)
	}
	if err := setWindowsCertificateFriendlyName(stored, windowsTrustOwnerName(request)); err != nil {
		rollbackErr := windows.CertDeleteCertificateFromStore(stored)
		return errors.Join(err, rollbackErr)
	}
	defer windows.CertFreeCertificateContext(stored)
	return nil
}

// release revalidates one exact marked certificate immediately before deleting that context.
func (store windowsCurrentUserRootStore) release(ctx context.Context, request Request) error {
	if err := validateWindowsTrustRequest(request); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootStore, err := openWindowsCurrentUserRootStore()
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
