//go:build darwin && cgo

package main

/*
#include <errno.h>
#include <stddef.h>
#include <unistd.h>

static int harbor_enter_administrator_trust_identity(
	uid_t expected,
	uid_t *before_real,
	uid_t *before_effective,
	uid_t *after_real,
	uid_t *after_effective
) {
	if (before_real == NULL || before_effective == NULL || after_real == NULL || after_effective == NULL) {
		return EINVAL;
	}
	*before_real = getuid();
	*before_effective = geteuid();
	*after_real = *before_real;
	*after_effective = *before_effective;
	if (expected == 0 || *before_real != expected || *before_effective != 0) {
		return EPERM;
	}
	if (setuid(0) != 0) {
		int status = errno;
		*after_real = getuid();
		*after_effective = geteuid();
		return status;
	}
	*after_real = getuid();
	*after_effective = geteuid();
	if (*after_real != 0 || *after_effective != 0) {
		return EPERM;
	}
	return 0;
}
*/
import "C"

import (
	"fmt"
	"syscall"
)

// irreversiblyEnterAdministratorTrustIdentity makes the setuid helper's real identity root before Security.framework flushes administrator trust settings.
func irreversiblyEnterAdministratorTrustIdentity(requester string) error {
	return transitionAdministratorTrustIdentity(requester, func(target uint32) (trustIdentityState, trustIdentityState, error) {
		var beforeReal C.uid_t
		var beforeEffective C.uid_t
		var afterReal C.uid_t
		var afterEffective C.uid_t
		status := C.harbor_enter_administrator_trust_identity(
			C.uid_t(target),
			&beforeReal,
			&beforeEffective,
			&afterReal,
			&afterEffective,
		)
		before := trustIdentityState{
			realUID:      uint32(beforeReal),
			effectiveUID: uint32(beforeEffective),
		}
		after := trustIdentityState{
			realUID:      uint32(afterReal),
			effectiveUID: uint32(afterEffective),
		}
		if status != 0 {
			return before, after, fmt.Errorf("darwin setuid: %w", syscall.Errno(status))
		}
		return before, after, nil
	})
}
