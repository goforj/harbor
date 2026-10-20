//go:build windows

package windowsjob

import (
	"fmt"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestInformationRoundTripFromFreshGoroutines proves native buffers retain their values across small, growing Go stacks.
func TestInformationRoundTripFromFreshGoroutines(t *testing.T) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if err := Set(job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	failures := make(chan error, 64)
	for index := 0; index < cap(failures); index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 32; attempt++ {
				observed := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
				if err := Query(job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&observed), uint32(unsafe.Sizeof(observed))); err != nil {
					failures <- err
					return
				}
				if observed.BasicLimitInformation.LimitFlags != limits.BasicLimitInformation.LimitFlags {
					failures <- fmt.Errorf("native job limits = %#x; want %#x", observed.BasicLimitInformation.LimitFlags, limits.BasicLimitInformation.LimitFlags)
					return
				}
			}
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

// TestInformationRejectsInvalidHandles preserves native failures instead of accepting an unfilled zero-value buffer.
func TestInformationRejectsInvalidHandles(t *testing.T) {
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := Query(windows.InvalidHandle, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err == nil {
		t.Fatal("invalid native job query succeeded")
	}
	if err := Set(windows.InvalidHandle, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err == nil {
		t.Fatal("invalid native job limit mutation succeeded")
	}
}
