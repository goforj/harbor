//go:build windows

package projectprocess

import (
	"os/exec"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestSessionScopeRefusesExistingJobWithoutMutatingIt excludes same-name foreign objects from adoption at launch.
func TestSessionScopeRefusesExistingJobWithoutMutatingIt(t *testing.T) {
	directory := canonicalTestPath(t, t.TempDir())
	name, err := windows.UTF16PtrFromString(sessionScopeName("project-collision", "session-collision", directory))
	if err != nil {
		t.Fatal(err)
	}
	handle, _, nativeErr := createSessionJob.Call(0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		t.Fatal(nativeErr)
	}
	job := windows.Handle(handle)
	defer windows.CloseHandle(job)
	if platform, err := prepareSessionPlatformProcess(exec.Command("unused"), "project-collision", "session-collision", directory, true); err == nil {
		platform.close()
		t.Fatal("launch accepted a pre-existing job")
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		t.Fatal(err)
	}
	if limits.BasicLimitInformation.LimitFlags != 0 {
		t.Fatal("foreign job policy was modified")
	}
}

// TestSessionScopeRejectsUncontainedDescendantPolicy prevents an opened job with breakaway permissions becoming Stop authority.
func TestSessionScopeRejectsUncontainedDescendantPolicy(t *testing.T) {
	directory := canonicalTestPath(t, t.TempDir())
	platform, err := prepareSessionPlatformProcess(exec.Command("unused"), "project-policy", "session-policy", directory, true)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.close()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(platform.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	job, err := openOwnedSessionJob("project-policy", "session-policy", directory, sessionJobQuery)
	if err == nil {
		windows.CloseHandle(job)
		t.Fatal("scope with escaping descendants was accepted")
	}
}

// TestSessionScopeBreakawayPolicyDoesNotEscapeAnUnrelatedRestrictiveJob keeps CI and external service scopes intact.
func TestSessionScopeBreakawayPolicyDoesNotEscapeAnUnrelatedRestrictiveJob(t *testing.T) {
	for _, limits := range []uint32{0, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK, windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK} {
		got := sessionBreakawayFlags(limits)
		if (got == windows.CREATE_BREAKAWAY_FROM_JOB) != (limits&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0) {
			t.Fatalf("wrong explicit breakaway policy %#x for job %#x", got, limits)
		}
	}
}

// TestSessionScopeBrokerRetainsQueryOnlyJobHandle exercises the same native keeper open used by the standalone broker.
func TestSessionScopeBrokerRetainsQueryOnlyJobHandle(t *testing.T) {
	directory := canonicalTestPath(t, t.TempDir())
	platform, err := prepareSessionPlatformProcess(exec.Command("unused"), "project-keeper", "session-keeper", directory, true)
	if err != nil {
		t.Fatal(err)
	}
	defer platform.close()
	release, err := retainOutputBrokerProcessScope(OutputBrokerRuntimeConfig{ProjectID: "project-keeper", SessionID: "session-keeper", OutputDirectory: directory, RetainProcessScope: true})
	if err != nil {
		t.Fatal(err)
	}
	release()
}
