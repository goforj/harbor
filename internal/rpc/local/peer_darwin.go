//go:build darwin

package local

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// readUnixPeerIdentity combines macOS's LOCAL_PEERCRED owner with LOCAL_PEERPID process admission.
func readUnixPeerIdentity(connection *net.UnixConn) (PeerIdentity, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return PeerIdentity{}, fmt.Errorf("access Unix socket descriptor: %w", err)
	}

	var credentials *unix.Xucred
	var processID int
	var credentialErr error
	if err := raw.Control(func(descriptor uintptr) {
		credentials, credentialErr = unix.GetsockoptXucred(int(descriptor), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credentialErr == nil {
			processID, credentialErr = unix.GetsockoptInt(int(descriptor), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		}
	}); err != nil {
		return PeerIdentity{}, fmt.Errorf("inspect Unix socket descriptor: %w", err)
	}
	if credentialErr != nil {
		return PeerIdentity{}, fmt.Errorf("read LOCAL_PEERCRED: %w", credentialErr)
	}
	if credentials == nil {
		return PeerIdentity{}, fmt.Errorf("read LOCAL_PEERCRED: operating system returned no credentials")
	}
	if processID <= 0 {
		return PeerIdentity{}, fmt.Errorf("read LOCAL_PEERPID: invalid process ID %d", processID)
	}

	return PeerIdentity{
		UserID:    strconv.FormatUint(uint64(credentials.Uid), 10),
		ProcessID: uint32(processID),
	}, nil
}
