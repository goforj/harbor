//go:build linux

package local

import (
	"testing"

	"golang.org/x/sys/unix"
)

// TestLinuxPeerIdentityRejectsInvalidProcess verifies unusable kernel process IDs fail before admission.
func TestLinuxPeerIdentityRejectsInvalidProcess(t *testing.T) {
	if identity, err := linuxPeerIdentity(nil); err == nil {
		t.Fatalf("nil credentials unexpectedly produced identity %+v", identity)
	}
	for _, processID := range []int32{-1, 0} {
		identity, err := linuxPeerIdentity(&unix.Ucred{Uid: 1000, Pid: processID})
		if err == nil {
			t.Fatalf("process ID %d unexpectedly produced identity %+v", processID, identity)
		}
	}
}

// TestLinuxPeerIdentityFormatsKernelCredential verifies UID and PID retain their lossless platform values.
func TestLinuxPeerIdentityFormatsKernelCredential(t *testing.T) {
	identity, err := linuxPeerIdentity(&unix.Ucred{Uid: 1000, Pid: 42})
	if err != nil {
		t.Fatalf("format Linux peer identity: %v", err)
	}
	want := PeerIdentity{UserID: "1000", ProcessID: 42}
	if identity != want {
		t.Fatalf("identity = %+v, want %+v", identity, want)
	}
}
