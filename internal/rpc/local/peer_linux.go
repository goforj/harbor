//go:build linux

package local

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// readUnixPeerIdentity reads Linux's kernel-authenticated UID and PID for a Unix-domain peer.
func readUnixPeerIdentity(connection *net.UnixConn) (PeerIdentity, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return PeerIdentity{}, fmt.Errorf("access Unix socket descriptor: %w", err)
	}

	var credentials *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(descriptor uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(int(descriptor), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return PeerIdentity{}, fmt.Errorf("inspect Unix socket descriptor: %w", err)
	}
	if credentialErr != nil {
		return PeerIdentity{}, fmt.Errorf("read SO_PEERCRED: %w", credentialErr)
	}

	return linuxPeerIdentity(credentials)
}

// linuxPeerIdentity validates and formats credentials returned by Linux's SO_PEERCRED contract.
func linuxPeerIdentity(credentials *unix.Ucred) (PeerIdentity, error) {
	if credentials == nil {
		return PeerIdentity{}, fmt.Errorf("read SO_PEERCRED: operating system returned no credentials")
	}
	if credentials.Pid <= 0 {
		return PeerIdentity{}, fmt.Errorf("read SO_PEERCRED: invalid process ID %d", credentials.Pid)
	}

	return PeerIdentity{
		UserID:    strconv.FormatUint(uint64(credentials.Uid), 10),
		ProcessID: uint32(credentials.Pid),
	}, nil
}
