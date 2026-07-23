//go:build darwin && cgo

package projectterminal

/*
#include <errno.h>
#include <pwd.h>
#include <stdlib.h>

static int harbor_account_shell(const char *name, char *buffer, size_t buffer_size, char **shell) {
	struct passwd account;
	struct passwd *result = NULL;
	int code = getpwnam_r(name, &account, buffer, buffer_size, &result);
	if (code != 0) {
		return code;
	}
	if (result == NULL || result->pw_shell == NULL || result->pw_shell[0] == '\0') {
		return ENOENT;
	}
	*shell = result->pw_shell;
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// accountLoginShell returns username's shell from macOS Directory Services.
func accountLoginShell(username string) (string, bool, error) {
	name := C.CString(username)
	defer C.free(unsafe.Pointer(name))
	buffer := make([]byte, 16*1024)
	var shell *C.char
	code := C.harbor_account_shell(
		name,
		(*C.char)(unsafe.Pointer(&buffer[0])),
		C.size_t(len(buffer)),
		&shell,
	)
	if code == C.ENOENT {
		return "", false, nil
	}
	if code != 0 {
		err := syscall.Errno(code)
		if errors.Is(err, syscall.ERANGE) {
			return "", false, fmt.Errorf("macOS account record exceeds the terminal lookup buffer")
		}
		return "", false, fmt.Errorf("read macOS account shell: %w", err)
	}
	return C.GoString(shell), true, nil
}
