//go:build darwin && cgo

package trust

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

enum { harborErrSecSuccess = errSecSuccess };

static const char harbor_trust_owner_service[] = "com.goforj.harbor.trust-owner.v1";

static int harbor_trust_append(uint8_t **buffer, size_t *length, size_t *capacity, const void *value, size_t value_length) {
	if (value_length > SIZE_MAX - sizeof(uint32_t) || *length > SIZE_MAX - sizeof(uint32_t) - value_length) {
		return errSecAllocate;
	}
	size_t required = *length + sizeof(uint32_t) + value_length;
	if (required > *capacity) {
		size_t next = *capacity == 0 ? 4096 : *capacity;
		while (next < required) {
			if (next > SIZE_MAX / 2) {
				return errSecAllocate;
			}
			next *= 2;
		}
		uint8_t *grown = realloc(*buffer, next);
		if (grown == NULL) {
			return errSecAllocate;
		}
		*buffer = grown;
		*capacity = next;
	}
	uint32_t encoded = (uint32_t)value_length;
	memcpy(*buffer + *length, &encoded, sizeof(encoded));
	*length += sizeof(encoded);
	if (value_length != 0) {
		memcpy(*buffer + *length, value, value_length);
		*length += value_length;
	}
	return errSecSuccess;
}

static int harbor_trust_settings_are_exact(SecCertificateRef certificate, int *exact) {
	if (certificate == NULL || exact == NULL) {
		return errSecParam;
	}
	*exact = 0;
	CFArrayRef settings = NULL;
	OSStatus status = SecTrustSettingsCopyTrustSettings(certificate, kSecTrustSettingsDomainUser, &settings);
	if (status == errSecItemNotFound || status == errSecNoTrustSettings) {
		if (settings != NULL) {
			CFRelease(settings);
		}
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		if (settings != NULL) {
			CFRelease(settings);
		}
		return status;
	}
	if (settings == NULL) {
		return errSecDecode;
	}
	if (CFArrayGetCount(settings) == 1) {
		CFTypeRef candidate = CFArrayGetValueAtIndex(settings, 0);
		if (candidate != NULL && CFGetTypeID(candidate) == CFDictionaryGetTypeID()) {
			CFDictionaryRef dictionary = (CFDictionaryRef)candidate;
			if (CFDictionaryGetCount(dictionary) == 1) {
				CFTypeRef result = CFDictionaryGetValue(dictionary, kSecTrustSettingsResult);
				if (result != NULL && CFGetTypeID(result) == CFNumberGetTypeID()) {
					int32_t value = 0;
					if (CFNumberGetValue((CFNumberRef)result, kCFNumberSInt32Type, &value) && value == kSecTrustSettingsResultTrustRoot) {
						*exact = 1;
					}
				}
			}
		}
	}
	CFRelease(settings);
	return errSecSuccess;
}

// harbor_trust_copy_user_snapshot returns repeated [DER length][DER][exact byte] records.
static int harbor_trust_copy_user_snapshot(uint8_t **output, size_t *output_length) {
	if (output == NULL || output_length == NULL) {
		return errSecParam;
	}
	*output = NULL;
	*output_length = 0;
	CFArrayRef certificates = NULL;
	OSStatus status = SecTrustSettingsCopyCertificates(kSecTrustSettingsDomainUser, &certificates);
	if (status == errSecItemNotFound || status == errSecNoTrustSettings) {
		if (certificates != NULL) {
			CFRelease(certificates);
		}
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		if (certificates != NULL) {
			CFRelease(certificates);
		}
		return status;
	}
	if (certificates == NULL) {
		return errSecDecode;
	}
	CFIndex count = CFArrayGetCount(certificates);
	if (count < 0 || count > 256) {
		CFRelease(certificates);
		return errSecDecode;
	}
	uint8_t *buffer = NULL;
	size_t length = 0;
	size_t capacity = 0;
	for (CFIndex index = 0; index < count; index++) {
		CFTypeRef candidate = CFArrayGetValueAtIndex(certificates, index);
		if (candidate == NULL || CFGetTypeID(candidate) != SecCertificateGetTypeID()) {
			free(buffer);
			CFRelease(certificates);
			return errSecDecode;
		}
		CFDataRef data = SecCertificateCopyData((SecCertificateRef)candidate);
		if (data == NULL) {
			free(buffer);
			CFRelease(certificates);
			return errSecDecode;
		}
		CFIndex data_length = CFDataGetLength(data);
		if (data_length <= 0 || data_length > (64 * 1024)) {
			CFRelease(data);
			free(buffer);
			CFRelease(certificates);
			return errSecDecode;
		}
		status = harbor_trust_append(&buffer, &length, &capacity, CFDataGetBytePtr(data), (size_t)data_length);
		CFRelease(data);
		if (status != errSecSuccess) {
			free(buffer);
			CFRelease(certificates);
			return status;
		}
		int exact = 0;
		status = harbor_trust_settings_are_exact((SecCertificateRef)candidate, &exact);
		if (status != errSecSuccess) {
			free(buffer);
			CFRelease(certificates);
			return status;
		}
		if (length == capacity) {
			size_t next = capacity == 0 ? 4096 : capacity * 2;
			if (next <= capacity) {
				free(buffer);
				CFRelease(certificates);
				return errSecAllocate;
			}
			uint8_t *grown = realloc(buffer, next);
			if (grown == NULL) {
				free(buffer);
				CFRelease(certificates);
				return errSecAllocate;
			}
			buffer = grown;
			capacity = next;
		}
		buffer[length++] = (uint8_t)exact;
	}
	CFRelease(certificates);
	*output = buffer;
	*output_length = length;
	return errSecSuccess;
}

static int harbor_trust_find_owner(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	UInt32 data_length = 0;
	void *data = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL,
		sizeof(harbor_trust_owner_service) - 1,
		harbor_trust_owner_service,
		(UInt32)account_length,
		account,
		&data_length,
		&data,
		NULL
	);
	if (status == errSecItemNotFound) {
		return 0;
	}
	if (status != errSecSuccess) {
		return status;
	}
	if (data == NULL) {
		return data_length == 0 ? errSecDuplicateItem : errSecDecode;
	}
	int matches = data_length == fingerprint_length && memcmp(data, fingerprint, fingerprint_length) == 0;
	SecKeychainItemFreeContent(NULL, data);
	return matches ? 1 : errSecDuplicateItem;
}

static int harbor_trust_add_owner(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	int existing = harbor_trust_find_owner(account, account_length, fingerprint, fingerprint_length);
	if (existing == 1) {
		return errSecSuccess;
	}
	if (existing != 0) {
		return existing;
	}
	return SecKeychainAddGenericPassword(
		NULL,
		sizeof(harbor_trust_owner_service) - 1,
		harbor_trust_owner_service,
		(UInt32)account_length,
		account,
		(UInt32)fingerprint_length,
		fingerprint,
		NULL
	);
}

static int harbor_trust_delete_owner(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	UInt32 data_length = 0;
	void *data = NULL;
	SecKeychainItemRef item = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL,
		sizeof(harbor_trust_owner_service) - 1,
		harbor_trust_owner_service,
		(UInt32)account_length,
		account,
		&data_length,
		&data,
		&item
	);
	if (status == errSecItemNotFound) {
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		return status;
	}
	if (item == NULL) {
		if (data != NULL) {
			SecKeychainItemFreeContent(NULL, data);
		}
		return errSecDecode;
	}
	if (data == NULL) {
		CFRelease(item);
		return data_length == 0 ? errSecDuplicateItem : errSecDecode;
	}
	int matches = data_length == fingerprint_length && memcmp(data, fingerprint, fingerprint_length) == 0;
	SecKeychainItemFreeContent(NULL, data);
	if (!matches) {
		if (item != NULL) {
			CFRelease(item);
		}
		return errSecDuplicateItem;
	}
	status = SecKeychainItemDelete(item);
	if (item != NULL) {
		CFRelease(item);
	}
	return status;
}

static int harbor_trust_set_user_root(const uint8_t *der, size_t der_length) {
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, der, (CFIndex)der_length);
	if (data == NULL) {
		return errSecAllocate;
	}
	SecCertificateRef certificate = SecCertificateCreateWithData(kCFAllocatorDefault, data);
	CFRelease(data);
	if (certificate == NULL) {
		return errSecDecode;
	}
	int32_t result_value = kSecTrustSettingsResultTrustRoot;
	CFNumberRef result = CFNumberCreate(kCFAllocatorDefault, kCFNumberSInt32Type, &result_value);
	if (result == NULL) {
		CFRelease(certificate);
		return errSecAllocate;
	}
	const void *keys[] = {kSecTrustSettingsResult};
	const void *values[] = {result};
	CFDictionaryRef settings_dictionary = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFRelease(result);
	if (settings_dictionary == NULL) {
		CFRelease(certificate);
		return errSecAllocate;
	}
	const void *settings_values[] = {settings_dictionary};
	CFArrayRef settings = CFArrayCreate(kCFAllocatorDefault, settings_values, 1, &kCFTypeArrayCallBacks);
	CFRelease(settings_dictionary);
	if (settings == NULL) {
		CFRelease(certificate);
		return errSecAllocate;
	}
	OSStatus status = SecTrustSettingsSetTrustSettings(certificate, kSecTrustSettingsDomainUser, settings);
	CFRelease(settings);
	CFRelease(certificate);
	return status;
}

// harbor_trust_remove_user_root_if_owned_exact narrows the stale window by rechecking target settings and ownership immediately before removal.
static int harbor_trust_remove_user_root_if_owned_exact(
	const uint8_t *der,
	size_t der_length,
	const char *account,
	size_t account_length,
	const char *fingerprint,
	size_t fingerprint_length,
	int *stale
) {
	if (der == NULL || der_length == 0 || account == NULL || account_length == 0 || fingerprint == NULL || fingerprint_length == 0 || stale == NULL) {
		return errSecParam;
	}
	*stale = 0;
	CFDataRef data = CFDataCreate(kCFAllocatorDefault, der, (CFIndex)der_length);
	if (data == NULL) {
		return errSecAllocate;
	}
	SecCertificateRef certificate = SecCertificateCreateWithData(kCFAllocatorDefault, data);
	CFRelease(data);
	if (certificate == NULL) {
		return errSecDecode;
	}
	int exact = 0;
	OSStatus status = harbor_trust_settings_are_exact(certificate, &exact);
	if (status != errSecSuccess) {
		CFRelease(certificate);
		return status;
	}
	if (!exact) {
		*stale = 1;
		CFRelease(certificate);
		return errSecSuccess;
	}
	int owner = harbor_trust_find_owner(account, account_length, fingerprint, fingerprint_length);
	if (owner == 0 || owner == errSecDuplicateItem) {
		*stale = 1;
		CFRelease(certificate);
		return errSecSuccess;
	}
	if (owner != 1) {
		CFRelease(certificate);
		return owner;
	}
	status = SecTrustSettingsRemoveTrustSettings(certificate, kSecTrustSettingsDomainUser);
	CFRelease(certificate);
	if (status == errSecItemNotFound) {
		*stale = 1;
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		return status;
	}
	return harbor_trust_delete_owner(account, account_length, fingerprint, fingerprint_length);
}

#pragma clang diagnostic pop
*/
import "C"

import (
	"context"
	"encoding/binary"
	"fmt"
	"unsafe"
)

const maximumDarwinTrustSnapshotBytes = 16 << 20

// darwinNativeTrustStore is the only production Security.framework boundary used by the adapter.
type darwinNativeTrustStore struct{}

// darwinTrustReleaseEffect performs the one native optimistic recheck and bounded removal.
type darwinTrustReleaseEffect func([]byte, string, string) (bool, error)

// New creates a macOS current-user trust adapter backed by Security.framework and the login keychain.
func New() (*Adapter, error) {
	return newAdapter(newDarwinTrustBackend(darwinNativeTrustStore{})), nil
}

// snapshot copies the complete current-user trust settings into Go-owned bounded records.
func (darwinNativeTrustStore) snapshot(ctx context.Context) ([]darwinTrustEntry, error) {
	if err := validateDarwinTrustContext(ctx); err != nil {
		return nil, err
	}
	var native *C.uint8_t
	var nativeLength C.size_t
	status := C.harbor_trust_copy_user_snapshot(&native, &nativeLength)
	if native != nil {
		defer C.free(unsafe.Pointer(native))
	}
	if status != C.harborErrSecSuccess {
		return nil, darwinTrustStatusError("copy current-user trust settings", status)
	}
	if uint64(nativeLength) > maximumDarwinTrustSnapshotBytes {
		return nil, fmt.Errorf("current-user trust snapshot exceeds %d bytes", maximumDarwinTrustSnapshotBytes)
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(native)), int(nativeLength))
	entries := make([]darwinTrustEntry, 0)
	for offset := 0; offset < len(bytes); {
		if len(bytes)-offset < 4 {
			return nil, fmt.Errorf("current-user trust snapshot has a truncated certificate length")
		}
		length := int(binary.LittleEndian.Uint32(bytes[offset : offset+4]))
		offset += 4
		if length <= 0 || length > maximumCertificatePEMBytes || length > len(bytes)-offset-1 {
			return nil, fmt.Errorf("current-user trust snapshot certificate length %d is invalid", length)
		}
		der := append([]byte(nil), bytes[offset:offset+length]...)
		offset += length
		exact := bytes[offset] == 1
		offset++
		entries = append(entries, darwinTrustEntry{CertificateDER: der, NativeExact: exact})
	}
	return entries, nil
}

// ensure installs the exact root trust shape before recording the Harbor ownership marker.
func (darwinNativeTrustStore) ensure(ctx context.Context, request Request) error {
	if err := validateDarwinTrustContext(ctx); err != nil {
		return err
	}
	if err := validateDarwinTrustRequester(request); err != nil {
		return err
	}
	if err := validateDarwinTrustOwnerAccount(request); err != nil {
		return err
	}
	root, err := darwinRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	account := darwinTrustOwnerAccount(request)
	fingerprint := request.AuthorityFingerprint()
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	status := C.harbor_trust_set_user_root(
		(*C.uint8_t)(unsafe.Pointer(&root[0])),
		C.size_t(len(root)),
	)
	if status != C.harborErrSecSuccess {
		return darwinTrustStatusError("set current-user root trust", status)
	}
	status = C.harbor_trust_add_owner(
		(*C.char)(unsafe.Pointer(&accountPointer[0])), accountLength,
		(*C.char)(unsafe.Pointer(&fingerprintPointer[0])), fingerprintLength,
	)
	if status != C.harborErrSecSuccess {
		return darwinTrustStatusError("record current-user trust ownership", status)
	}
	return nil
}

// release optimistically revalidates exact settings and ownership next to the bounded native removal.
func (darwinNativeTrustStore) release(ctx context.Context, request Request) error {
	if err := validateDarwinTrustContext(ctx); err != nil {
		return err
	}
	if err := validateDarwinTrustRequester(request); err != nil {
		return err
	}
	if err := validateDarwinTrustOwnerAccount(request); err != nil {
		return err
	}
	root, err := darwinRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	account := darwinTrustOwnerAccount(request)
	fingerprint := request.AuthorityFingerprint()
	return executeDarwinTrustRelease(root, account, fingerprint, removeExactOwnedDarwinTrust)
}

// executeDarwinTrustRelease maps native revalidation outcomes onto the portable stale-observation contract.
func executeDarwinTrustRelease(
	root []byte,
	account string,
	fingerprint string,
	effect darwinTrustReleaseEffect,
) error {
	stale, err := effect(root, account, fingerprint)
	if err != nil {
		return err
	}
	if stale {
		return errNativeObservationChanged
	}
	return nil
}

// removeExactOwnedDarwinTrust invokes the single native recheck-and-remove helper for one validated target.
func removeExactOwnedDarwinTrust(root []byte, account string, fingerprint string) (bool, error) {
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	var stale C.int
	status := C.harbor_trust_remove_user_root_if_owned_exact(
		(*C.uint8_t)(unsafe.Pointer(&root[0])),
		C.size_t(len(root)),
		(*C.char)(unsafe.Pointer(&accountPointer[0])),
		accountLength,
		(*C.char)(unsafe.Pointer(&fingerprintPointer[0])),
		fingerprintLength,
		&stale,
	)
	if status != C.harborErrSecSuccess {
		return false, darwinTrustStatusError("remove exact owned current-user root trust", status)
	}
	return stale != 0, nil
}

// ownerExists checks only the fixed Harbor marker account bound to this exact authority.
func (darwinNativeTrustStore) ownerExists(ctx context.Context, request Request) (bool, error) {
	if err := validateDarwinTrustContext(ctx); err != nil {
		return false, err
	}
	if err := validateDarwinTrustRequester(request); err != nil {
		return false, err
	}
	if err := validateDarwinTrustOwnerAccount(request); err != nil {
		return false, err
	}
	account := darwinTrustOwnerAccount(request)
	fingerprint := request.AuthorityFingerprint()
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	status := C.harbor_trust_find_owner(
		(*C.char)(unsafe.Pointer(&accountPointer[0])), accountLength,
		(*C.char)(unsafe.Pointer(&fingerprintPointer[0])), fingerprintLength,
	)
	switch status {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, darwinTrustStatusError("observe current-user trust ownership", C.int(status))
	}
}

// validateDarwinTrustContext keeps native calls cancellation-aware even though Security.framework calls are synchronous.
func validateDarwinTrustContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// cStringBytes returns NUL-free C input storage with its exact byte length.
func cStringBytes(value string) ([]byte, C.size_t) {
	bytes := append([]byte(value), 0)
	return bytes, C.size_t(len(bytes) - 1)
}

// darwinTrustStatusError keeps native OSStatus values bounded and machine-readable.
func darwinTrustStatusError(operation string, status C.int) error {
	return fmt.Errorf("%s failed with macOS OSStatus %d", operation, int(status))
}
