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

enum { harborErrSecSuccess = errSecSuccess, harborErrSecItemNotFound = errSecItemNotFound, harborErrSecDuplicateItem = errSecDuplicateItem };

static const char harbor_trust_owner_service[] = "com.goforj.harbor.trust-owner.v1";
static const char harbor_admin_trust_owner_service[] = "com.goforj.harbor.admin-trust-owner.v1";
static const char harbor_system_keychain_path[] = "/Library/Keychains/System.keychain";

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

static int harbor_trust_settings_are_exact_in_domain(SecCertificateRef certificate, SecTrustSettingsDomain domain, int *exact) {
	if (certificate == NULL || exact == NULL) {
		return errSecParam;
	}
	*exact = 0;
	CFArrayRef settings = NULL;
	OSStatus status = SecTrustSettingsCopyTrustSettings(certificate, domain, &settings);
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

static int harbor_trust_settings_are_exact(SecCertificateRef certificate, int *exact) {
	return harbor_trust_settings_are_exact_in_domain(certificate, kSecTrustSettingsDomainUser, exact);
}

// harbor_trust_copy_user_snapshot returns repeated [DER length][DER][exact byte] records.
static int harbor_trust_copy_snapshot(SecTrustSettingsDomain domain, uint8_t **output, size_t *output_length) {
	if (output == NULL || output_length == NULL) {
		return errSecParam;
	}
	*output = NULL;
	*output_length = 0;
	CFArrayRef certificates = NULL;
	OSStatus status = SecTrustSettingsCopyCertificates(domain, &certificates);
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
		status = harbor_trust_settings_are_exact_in_domain((SecCertificateRef)candidate, domain, &exact);
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

static int harbor_trust_copy_user_snapshot(uint8_t **output, size_t *output_length) {
	return harbor_trust_copy_snapshot(kSecTrustSettingsDomainUser, output, output_length);
}

static int harbor_trust_copy_admin_snapshot(uint8_t **output, size_t *output_length) {
	return harbor_trust_copy_snapshot(kSecTrustSettingsDomainAdmin, output, output_length);
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

// harbor_admin_trust_owner_query builds an attributes-only query against the system keychain.
static int harbor_admin_trust_owner_query(const char *account, size_t account_length, SecKeychainRef *keychain_output, CFMutableDictionaryRef *query_output) {
	SecKeychainRef keychain = NULL;
	OSStatus status = SecKeychainOpen(harbor_system_keychain_path, &keychain);
	if (status != errSecSuccess) {
		return status;
	}
	CFStringRef service = CFStringCreateWithBytes(kCFAllocatorDefault, (const UInt8 *)harbor_admin_trust_owner_service, sizeof(harbor_admin_trust_owner_service) - 1, kCFStringEncodingUTF8, false);
	CFStringRef account_value = CFStringCreateWithBytes(kCFAllocatorDefault, (const UInt8 *)account, (CFIndex)account_length, kCFStringEncodingUTF8, false);
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (service == NULL || account_value == NULL || query == NULL) {
		if (service != NULL) {
			CFRelease(service);
		}
		if (account_value != NULL) {
			CFRelease(account_value);
		}
		if (query != NULL) {
			CFRelease(query);
		}
		CFRelease(keychain);
		return errSecAllocate;
	}
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, service);
	CFDictionarySetValue(query, kSecAttrAccount, account_value);
	CFRelease(account_value);
	CFRelease(service);
	*keychain_output = keychain;
	*query_output = query;
	return errSecSuccess;
}

// SecItem lookup and deletion use a search list; kSecUseKeychain is reserved for adding an item.
static int harbor_admin_trust_select_system_keychain_for_query(SecKeychainRef keychain, CFMutableDictionaryRef query) {
	const void *values[] = {keychain};
	CFArrayRef search_list = CFArrayCreate(kCFAllocatorDefault, values, 1, &kCFTypeArrayCallBacks);
	if (search_list == NULL) {
		return errSecAllocate;
	}
	CFDictionarySetValue(query, kSecMatchSearchList, search_list);
	CFRelease(search_list);
	return errSecSuccess;
}

// harbor_admin_trust_owner_attribute_matches verifies an unencrypted generic attribute without requesting password data.
static int harbor_admin_trust_owner_attribute_matches(CFDictionaryRef attributes, const char *fingerprint, size_t fingerprint_length) {
	if (attributes == NULL || CFGetTypeID(attributes) != CFDictionaryGetTypeID()) {
		return errSecDecode;
	}
	CFTypeRef generic = CFDictionaryGetValue(attributes, kSecAttrGeneric);
	if (generic == NULL || CFGetTypeID(generic) != CFDataGetTypeID()) {
		return errSecDuplicateItem;
	}
	CFDataRef generic_data = (CFDataRef)generic;
	CFIndex generic_length = CFDataGetLength(generic_data);
	const UInt8 *generic_bytes = CFDataGetBytePtr(generic_data);
	if (generic_length != (CFIndex)fingerprint_length || generic_bytes == NULL || memcmp(generic_bytes, fingerprint, fingerprint_length) != 0) {
		return errSecDuplicateItem;
	}
	return 1;
}

// harbor_admin_trust_find_owner_exact verifies the account and generic fingerprint attributes without decrypting password data.
static int harbor_admin_trust_find_owner_exact(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	SecKeychainRef keychain = NULL;
	CFMutableDictionaryRef query = NULL;
	OSStatus status = harbor_admin_trust_owner_query(account, account_length, &keychain, &query);
	if (status != errSecSuccess) {
		return status;
	}
	status = harbor_admin_trust_select_system_keychain_for_query(keychain, query);
	if (status != errSecSuccess) {
		CFRelease(query);
		CFRelease(keychain);
		return status;
	}
	CFDictionarySetValue(query, kSecReturnAttributes, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
	CFTypeRef result = NULL;
	status = SecItemCopyMatching(query, &result);
	CFRelease(query);
	CFRelease(keychain);
	if (status == errSecItemNotFound) {
		return 0;
	}
	if (status != errSecSuccess) {
		return status;
	}
	int matches = harbor_admin_trust_owner_attribute_matches((CFDictionaryRef)result, fingerprint, fingerprint_length);
	CFRelease(result);
	return matches;
}

static int harbor_admin_trust_add_owner(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	SecKeychainRef keychain = NULL;
	CFMutableDictionaryRef query = NULL;
	OSStatus status = harbor_admin_trust_owner_query(account, account_length, &keychain, &query);
	if (status != errSecSuccess) {
		return status;
	}
	CFDataRef generic = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)fingerprint, (CFIndex)fingerprint_length);
	CFDataRef empty_password = CFDataCreate(kCFAllocatorDefault, NULL, 0);
	if (generic == NULL || empty_password == NULL) {
		if (generic != NULL) {
			CFRelease(generic);
		}
		if (empty_password != NULL) {
			CFRelease(empty_password);
		}
		CFRelease(query);
		CFRelease(keychain);
		return errSecAllocate;
	}
	CFDictionarySetValue(query, kSecUseKeychain, keychain);
	CFDictionarySetValue(query, kSecAttrGeneric, generic);
	CFDictionarySetValue(query, kSecValueData, empty_password);
	status = SecItemAdd(query, NULL);
	CFRelease(empty_password);
	CFRelease(generic);
	CFRelease(query);
	CFRelease(keychain);
	return status;
}

static int harbor_admin_trust_delete_owner(const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length) {
	int owner = harbor_admin_trust_find_owner_exact(account, account_length, fingerprint, fingerprint_length);
	if (owner == 0) {
		return errSecItemNotFound;
	}
	if (owner != 1) {
		return owner;
	}
	SecKeychainRef keychain = NULL;
	CFMutableDictionaryRef query = NULL;
	OSStatus status = harbor_admin_trust_owner_query(account, account_length, &keychain, &query);
	if (status != errSecSuccess) {
		return status;
	}
	status = harbor_admin_trust_select_system_keychain_for_query(keychain, query);
	if (status != errSecSuccess) {
		CFRelease(query);
		CFRelease(keychain);
		return status;
	}
	CFDataRef generic = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)fingerprint, (CFIndex)fingerprint_length);
	if (generic == NULL) {
		CFRelease(query);
		CFRelease(keychain);
		return errSecAllocate;
	}
	CFDictionarySetValue(query, kSecAttrGeneric, generic);
	status = SecItemDelete(query);
	CFRelease(generic);
	CFRelease(query);
	CFRelease(keychain);
	return status;
}

static int harbor_trust_set_root_in_domain(const uint8_t *der, size_t der_length, SecTrustSettingsDomain domain) {
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
	OSStatus status = SecTrustSettingsSetTrustSettings(certificate, domain, settings);
	CFRelease(settings);
	CFRelease(certificate);
	return status;
}

static int harbor_trust_set_user_root(const uint8_t *der, size_t der_length) {
	return harbor_trust_set_root_in_domain(der, der_length, kSecTrustSettingsDomainUser);
}

static int harbor_trust_set_admin_root(const uint8_t *der, size_t der_length) {
	return harbor_trust_set_root_in_domain(der, der_length, kSecTrustSettingsDomainAdmin);
}

// harbor_trust_admin_root_is_exact rechecks the exact target trust settings while the administrator mutation lock is held.
static int harbor_trust_admin_root_is_exact(const uint8_t *der, size_t der_length) {
	if (der == NULL || der_length == 0) {
		return errSecParam;
	}
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
	OSStatus status = harbor_trust_settings_are_exact_in_domain(certificate, kSecTrustSettingsDomainAdmin, &exact);
	CFRelease(certificate);
	if (status != errSecSuccess) {
		return status;
	}
	return exact ? 1 : 0;
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

static int harbor_trust_remove_admin_root_if_owned_exact(const uint8_t *der, size_t der_length, const char *account, size_t account_length, const char *fingerprint, size_t fingerprint_length, int *stale) {
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
	OSStatus status = harbor_trust_settings_are_exact_in_domain(certificate, kSecTrustSettingsDomainAdmin, &exact);
	if (status != errSecSuccess) {
		CFRelease(certificate);
		return status;
	}
	if (!exact) {
		*stale = 1;
		CFRelease(certificate);
		return errSecSuccess;
	}
	int owner = harbor_admin_trust_find_owner_exact(account, account_length, fingerprint, fingerprint_length);
	if (owner == 0 || owner == errSecDuplicateItem) {
		*stale = 1;
		CFRelease(certificate);
		return errSecSuccess;
	}
	if (owner != 1) {
		CFRelease(certificate);
		return owner;
	}
	status = SecTrustSettingsRemoveTrustSettings(certificate, kSecTrustSettingsDomainAdmin);
	CFRelease(certificate);
	if (status == errSecItemNotFound) {
		*stale = 1;
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		return status;
	}
	status = harbor_admin_trust_delete_owner(account, account_length, fingerprint, fingerprint_length);
	if (status == errSecItemNotFound || status == errSecDuplicateItem) {
		*stale = 1;
		return errSecSuccess;
	}
	return status;
}

#pragma clang diagnostic pop
*/
import "C"

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const maximumDarwinTrustSnapshotBytes = 16 << 20

const darwinAdministratorTrustMutationLockPath = "/var/run/com.goforj.harbor.admin-trust.lock"

// darwinNativeTrustStore is the only production Security.framework boundary used by the adapter.
type darwinNativeTrustStore struct{}

// darwinTrustReleaseEffect performs the one native optimistic recheck and bounded removal.
type darwinTrustReleaseEffect func([]byte, string, string) (bool, error)

// New creates a macOS current-user trust adapter backed by Security.framework and the login keychain.
func New() (*Adapter, error) {
	return newAdapter(newDarwinTrustBackend(darwinNativeTrustStore{})), nil
}

// snapshot copies the complete current-user trust settings into Go-owned bounded records.
func (darwinNativeTrustStore) snapshot(ctx context.Context, request Request) ([]darwinTrustEntry, error) {
	if err := validateDarwinTrustContext(ctx); err != nil {
		return nil, err
	}
	var native *C.uint8_t
	var nativeLength C.size_t
	var status C.int
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		status = C.harbor_trust_copy_admin_snapshot(&native, &nativeLength)
	} else {
		status = C.harbor_trust_copy_user_snapshot(&native, &nativeLength)
	}
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
	if err := validateDarwinAdministratorMutation(request); err != nil {
		return err
	}
	if err := validateDarwinTrustOwnerAccount(request); err != nil {
		return err
	}
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		if err := validateDarwinAdministratorTrustOwnerAttribute(request); err != nil {
			return err
		}
	}
	root, err := darwinRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	account := darwinTrustOwnerAccount(request)
	fingerprint := request.AuthorityFingerprint()
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		ownerAttribute := darwinAdministratorTrustOwnerAttribute(request)
		ownerAttributePointer, ownerAttributeLength := cStringBytes(ownerAttribute)
		unlock, err := acquireDarwinAdministratorTrustMutationLock()
		if err != nil {
			return err
		}
		defer unlock()
		markerStatus := C.harbor_admin_trust_find_owner_exact(
			(*C.char)(unsafe.Pointer(&accountPointer[0])),
			accountLength,
			(*C.char)(unsafe.Pointer(&ownerAttributePointer[0])),
			ownerAttributeLength,
		)
		if markerStatus != 0 && markerStatus != 1 {
			return newAdministratorTrustStatusError("owner-recheck", int(markerStatus))
		}
		trustStatus := C.harbor_trust_admin_root_is_exact((*C.uint8_t)(unsafe.Pointer(&root[0])), C.size_t(len(root)))
		if trustStatus != 0 && trustStatus != 1 {
			return newAdministratorTrustStatusError("root-recheck", int(trustStatus))
		}
		if markerStatus == 0 && trustStatus == 1 {
			return fmt.Errorf("administrator root trust became exact without this owner marker: %w", errNativeMutationConflict)
		}
		if markerStatus == 1 && trustStatus == 1 {
			return nil
		}
		createdMarker := false
		if markerStatus == 0 {
			markerStatus = C.harbor_admin_trust_add_owner(
				(*C.char)(unsafe.Pointer(&accountPointer[0])),
				accountLength,
				(*C.char)(unsafe.Pointer(&ownerAttributePointer[0])),
				ownerAttributeLength,
			)
			if markerStatus != C.harborErrSecSuccess {
				return newAdministratorTrustStatusError("owner-record", int(markerStatus))
			}
			createdMarker = true
			trustStatus = C.harbor_trust_admin_root_is_exact((*C.uint8_t)(unsafe.Pointer(&root[0])), C.size_t(len(root)))
			if trustStatus != 0 {
				if darwinAdministratorMarkerCleanupRequired(createdMarker) {
					_ = deleteDarwinAdministratorOwner(account, ownerAttribute)
				}
				if trustStatus == 1 {
					return fmt.Errorf("administrator root trust changed before install: %w", errNativeMutationConflict)
				}
				return newAdministratorTrustStatusError("root-recheck-after-marker", int(trustStatus))
			}
		}
		status := C.harbor_trust_set_admin_root((*C.uint8_t)(unsafe.Pointer(&root[0])), C.size_t(len(root)))
		if status != C.harborErrSecSuccess {
			if darwinAdministratorMarkerCleanupRequired(createdMarker) {
				_ = deleteDarwinAdministratorOwner(account, ownerAttribute)
			}
			return newAdministratorTrustStatusError("set-root", int(status))
		}
		return nil
	}
	status := C.harbor_trust_set_user_root((*C.uint8_t)(unsafe.Pointer(&root[0])), C.size_t(len(root)))
	if status != C.harborErrSecSuccess {
		return darwinTrustStatusError("set current-user root trust", status)
	}
	status = C.harbor_trust_add_owner((*C.char)(unsafe.Pointer(&accountPointer[0])), accountLength, (*C.char)(unsafe.Pointer(&fingerprintPointer[0])), fingerprintLength)
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
	if err := validateDarwinAdministratorMutation(request); err != nil {
		return err
	}
	if err := validateDarwinTrustOwnerAccount(request); err != nil {
		return err
	}
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		if err := validateDarwinAdministratorTrustOwnerAttribute(request); err != nil {
			return err
		}
	}
	root, err := darwinRootDER(request.Root().CertificatePEM)
	if err != nil {
		return err
	}
	account := darwinTrustOwnerAccount(request)
	fingerprint := request.AuthorityFingerprint()
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		ownerAttribute := darwinAdministratorTrustOwnerAttribute(request)
		unlock, err := acquireDarwinAdministratorTrustMutationLock()
		if err != nil {
			return err
		}
		defer unlock()
		return executeDarwinTrustRelease(root, account, ownerAttribute, removeExactOwnedDarwinAdministratorTrust)
	}
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

// removeExactOwnedDarwinAdministratorTrust invokes the administrator-domain CAS removal.
func removeExactOwnedDarwinAdministratorTrust(root []byte, account string, fingerprint string) (bool, error) {
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	var stale C.int
	status := C.harbor_trust_remove_admin_root_if_owned_exact((*C.uint8_t)(unsafe.Pointer(&root[0])), C.size_t(len(root)), (*C.char)(unsafe.Pointer(&accountPointer[0])), accountLength, (*C.char)(unsafe.Pointer(&fingerprintPointer[0])), fingerprintLength, &stale)
	if status != C.harborErrSecSuccess {
		return false, darwinTrustStatusError("remove exact owned administrator root trust", status)
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
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		if err := validateDarwinAdministratorTrustOwnerAttribute(request); err != nil {
			return false, err
		}
		ownerAttribute := darwinAdministratorTrustOwnerAttribute(request)
		ownerAttributePointer, ownerAttributeLength := cStringBytes(ownerAttribute)
		status := C.harbor_admin_trust_find_owner_exact(
			(*C.char)(unsafe.Pointer(&accountPointer[0])),
			accountLength,
			(*C.char)(unsafe.Pointer(&ownerAttributePointer[0])),
			ownerAttributeLength,
		)
		switch status {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, darwinTrustStatusError("observe administrator trust ownership", status)
		}
	}
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

// validateDarwinAdministratorMutation requires the effective identity Security.framework will use for the admin domain.
func validateDarwinAdministratorMutation(request Request) error {
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust && os.Geteuid() != 0 {
		return fmt.Errorf("Darwin administrator trust mutation requires effective root")
	}
	return nil
}

// acquireDarwinAdministratorTrustMutationLock serializes marker-first system-keychain effects across helper processes.
func acquireDarwinAdministratorTrustMutationLock() (func(), error) {
	fileDescriptor, err := syscall.Open(
		darwinAdministratorTrustMutationLockPath,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open administrator trust mutation lock: %w", err)
	}
	var information syscall.Stat_t
	if err := syscall.Fstat(fileDescriptor, &information); err != nil {
		_ = syscall.Close(fileDescriptor)
		return nil, fmt.Errorf("stat administrator trust mutation lock: %w", err)
	}
	if information.Mode&syscall.S_IFMT != syscall.S_IFREG || information.Uid != 0 || information.Mode&0o022 != 0 {
		_ = syscall.Close(fileDescriptor)
		return nil, fmt.Errorf("administrator trust mutation lock has unsafe ownership or mode")
	}
	if err := syscall.Flock(fileDescriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fileDescriptor)
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("administrator trust mutation is busy; retry the one-shot helper")
		}
		return nil, fmt.Errorf("lock administrator trust mutation: %w", err)
	}
	return func() {
		_ = syscall.Flock(fileDescriptor, syscall.LOCK_UN)
		_ = syscall.Close(fileDescriptor)
	}, nil
}

// deleteDarwinAdministratorOwner makes failed marker-first installation recoverable without masking the trust failure.
func deleteDarwinAdministratorOwner(account string, fingerprint string) error {
	accountPointer, accountLength := cStringBytes(account)
	fingerprintPointer, fingerprintLength := cStringBytes(fingerprint)
	status := C.harbor_admin_trust_delete_owner((*C.char)(unsafe.Pointer(&accountPointer[0])), accountLength, (*C.char)(unsafe.Pointer(&fingerprintPointer[0])), fingerprintLength)
	if status == C.harborErrSecSuccess || status == C.harborErrSecItemNotFound {
		return nil
	}
	return darwinTrustStatusError("delete administrator trust ownership", status)
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
