//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Security
#include <Security/Authorization.h>
#include <Security/AuthorizationTags.h>
#include <string.h>

typedef char harbor_authorization_external_form_must_be_32_bytes[
	(sizeof(AuthorizationExternalForm) == 32) ? 1 : -1
];

static OSStatus harbor_recheck_execute_authorization(const unsigned char externalBytes[32]) {
	AuthorizationExternalForm externalForm;
	memcpy(externalForm.bytes, externalBytes, sizeof(externalForm.bytes));

	AuthorizationRef authorization = NULL;
	OSStatus status = AuthorizationCreateFromExternalForm(&externalForm, &authorization);
	if (status != errAuthorizationSuccess) {
		return status;
	}
	if (authorization == NULL) {
		return errAuthorizationInvalidRef;
	}

	AuthorizationItem item = {kAuthorizationRightExecute, 0, NULL, 0};
	AuthorizationRights rights = {1, &item};
	status = AuthorizationCopyRights(
		authorization,
		&rights,
		kAuthorizationEmptyEnvironment,
		kAuthorizationFlagExtendRights,
		NULL
	);
	OSStatus releaseStatus = AuthorizationFree(authorization, kAuthorizationFlagDefaults);
	if (status == errAuthorizationSuccess && releaseStatus != errAuthorizationSuccess) {
		return releaseStatus;
	}
	return status;
}
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"
)

const authorizationExternalFormDescriptor = 3

// authorizePlatformInvocation imports FD 3 and rechecks execute authority without permitting helper-side interaction.
func authorizePlatformInvocation() error {
	authorization := os.NewFile(authorizationExternalFormDescriptor, "harbor-authorization-external-form")
	if authorization == nil {
		return fmt.Errorf("authorization descriptor %d is unavailable", authorizationExternalFormDescriptor)
	}
	defer authorization.Close()

	return authorizeExternalInvocation(authorization, os.Geteuid(), recheckDarwinExecuteAuthorization)
}

// recheckDarwinExecuteAuthorization internalizes preauthorized credentials and refuses to open a second consent flow.
func recheckDarwinExecuteAuthorization(externalForm [authorizationExternalFormLength]byte) error {
	status := C.harbor_recheck_execute_authorization((*C.uchar)(unsafe.Pointer(&externalForm[0])))
	runtime.KeepAlive(externalForm)
	if status != C.errAuthorizationSuccess {
		return fmt.Errorf("Authorization Services denied execute right with status %d", int32(status))
	}
	return nil
}
