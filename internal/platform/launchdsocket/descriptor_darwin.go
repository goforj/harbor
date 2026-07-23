//go:build darwin

package launchdsocket

import (
	"errors"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// socketOptionReader reads one integer socket option without changing descriptor ownership.
type socketOptionReader func(int, int, int) (int, error)

// inspectSocketProtocol verifies that a descriptor is a TCP stream without relying on SO_ACCEPTCONN.
//
// macOS can return ENOPROTOOPT for SO_ACCEPTCONN on launchd-provided TCP sockets.
// Launchd owns the passive listen transition for these fixed socket names, so
// Harbor verifies descriptor ownership, socket type, protocol, and bind instead.
func inspectSocketProtocol(descriptor int, getSocketOption socketOptionReader) (bool, error) {
	socketType, err := getSocketOption(descriptor, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return false, fmt.Errorf("read socket type: %w", err)
	}
	if socketType != unix.SOCK_STREAM {
		return false, nil
	}
	if _, err := getSocketOption(descriptor, unix.IPPROTO_TCP, unix.TCP_NODELAY); err == nil {
		return true, nil
	} else if !errors.Is(err, unix.ENOPROTOOPT) {
		return false, fmt.Errorf("verify TCP socket protocol: %w", err)
	}
	return false, nil
}

// inspectPlatformSocket reads protocol and local-address facts directly from one retained descriptor.
func inspectPlatformSocket(file *os.File) (socketObservation, error) {
	descriptor, err := activatedDescriptorNumber(file)
	if err != nil {
		return socketObservation{}, err
	}
	tcp, err := inspectSocketProtocol(descriptor, unix.GetsockoptInt)
	if err != nil {
		return socketObservation{}, err
	}

	nativeAddress, err := unix.Getsockname(descriptor)
	if err != nil {
		return socketObservation{}, fmt.Errorf("read socket local address: %w", err)
	}
	inet4, ipv4 := nativeAddress.(*unix.SockaddrInet4)
	observation := socketObservation{
		IPv4: ipv4,
		TCP:  tcp,
	}
	if ipv4 {
		observation.Local = netip.AddrPortFrom(netip.AddrFrom4(inet4.Addr), uint16(inet4.Port))
	}
	return observation, nil
}
