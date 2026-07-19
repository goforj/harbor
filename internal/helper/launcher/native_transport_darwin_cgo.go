//go:build darwin && cgo

package launcher

/*
#cgo LDFLAGS: -framework Security
#include <Security/Authorization.h>
#include <Security/AuthorizationTags.h>
#include <string.h>

typedef char harbor_launcher_external_form_must_be_32_bytes[
	(sizeof(AuthorizationExternalForm) == 32) ? 1 : -1
];

static OSStatus harbor_preauthorize_execute(
	unsigned char externalBytes[32],
	AuthorizationRef *authorizationOut
) {
	*authorizationOut = NULL;
	AuthorizationRef authorization = NULL;
	OSStatus status = AuthorizationCreate(
		NULL,
		kAuthorizationEmptyEnvironment,
		kAuthorizationFlagDefaults,
		&authorization
	);
	if (status != errAuthorizationSuccess) {
		return status;
	}
	if (authorization == NULL) {
		return errAuthorizationInvalidRef;
	}

	AuthorizationItem item = {kAuthorizationRightExecute, 0, NULL, 0};
	AuthorizationRights rights = {1, &item};
	AuthorizationFlags flags = kAuthorizationFlagDefaults |
		kAuthorizationFlagExtendRights |
		kAuthorizationFlagInteractionAllowed |
		kAuthorizationFlagPreAuthorize;
	status = AuthorizationCopyRights(
		authorization,
		&rights,
		kAuthorizationEmptyEnvironment,
		flags,
		NULL
	);
	if (status == errAuthorizationSuccess) {
		AuthorizationExternalForm externalForm;
		status = AuthorizationMakeExternalForm(authorization, &externalForm);
		if (status == errAuthorizationSuccess) {
			memcpy(externalBytes, externalForm.bytes, sizeof(externalForm.bytes));
			*authorizationOut = authorization;
			return errAuthorizationSuccess;
		}
	}
	OSStatus releaseStatus = AuthorizationFree(authorization, kAuthorizationFlagDestroyRights);
	if (status == errAuthorizationSuccess && releaseStatus != errAuthorizationSuccess) {
		return releaseStatus;
	}
	return status;
}

static OSStatus harbor_release_execute_authorization(AuthorizationRef authorization) {
	if (authorization == NULL) {
		return errAuthorizationInvalidRef;
	}
	return AuthorizationFree(authorization, kAuthorizationFlagDestroyRights);
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/helperpath"
)

// newNativeTransport selects macOS's factored Authorization Services transport.
func newNativeTransport() Transport {
	return newDarwinNativeTransport(
		helperpath.Executable(),
		inspectInstalledDarwinHelper,
		preauthorizeDarwinExecute,
		newDarwinAuthorizationPipe,
		newOSDarwinCommand,
	)
}

// preauthorizeDarwinExecute obtains and retains kAuthorizationRightExecute through the helper lifecycle.
func preauthorizeDarwinExecute() (darwinAuthorizationGrant, error) {
	var externalForm darwinAuthorizationExternalForm
	var authorization C.AuthorizationRef
	status := C.harbor_preauthorize_execute(
		(*C.uchar)(unsafe.Pointer(&externalForm[0])),
		&authorization,
	)
	runtime.KeepAlive(externalForm)
	switch status {
	case C.errAuthorizationSuccess:
		return darwinAuthorizationGrant{
			externalForm: externalForm,
			release: func() error {
				status := C.harbor_release_execute_authorization(authorization)
				if status != C.errAuthorizationSuccess {
					return fmt.Errorf("release Authorization Services execute credentials with status %d", int32(status))
				}
				return nil
			},
		}, nil
	case C.errAuthorizationCanceled:
		return darwinAuthorizationGrant{declined: true}, nil
	default:
		return darwinAuthorizationGrant{},
			fmt.Errorf("Authorization Services denied execute preauthorization with status %d", int32(status))
	}
}
