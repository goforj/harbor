//go:build darwin && cgo

package main

/*
#include <os/log.h>
#include <stdlib.h>

static void report_harbor_launchdrelay_fatal(const char *phase, const char *detail) {
	os_log_with_type(OS_LOG_DEFAULT, OS_LOG_TYPE_FAULT,
		"harbor launchd relay fatal exit: phase=%{public}s detail=%{public}s", phase, detail);
}
*/
import "C"

import "unsafe"

// reportFatalDiagnostic records bounded public fields in the macOS unified log collected by hosted evidence.
func reportFatalDiagnostic(diagnostic fatalExitDiagnostic) {
	phase := C.CString(diagnostic.phase)
	defer C.free(unsafe.Pointer(phase))
	detail := C.CString(diagnostic.detail)
	defer C.free(unsafe.Pointer(detail))
	C.report_harbor_launchdrelay_fatal(phase, detail)
}
