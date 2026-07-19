//go:build darwin

package launchdsocket

import (
	"errors"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// inspectPlatformSocket reads protocol, listening, and local-address facts directly from one retained descriptor.
func inspectPlatformSocket(file *os.File) (socketObservation, error) {
	descriptor, err := activatedDescriptorNumber(file)
	if err != nil {
		return socketObservation{}, err
	}
	socketType, err := unix.GetsockoptInt(descriptor, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil {
		return socketObservation{}, fmt.Errorf("read socket type: %w", err)
	}
	accepting, err := unix.GetsockoptInt(descriptor, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		return socketObservation{}, fmt.Errorf("read socket listening state: %w", err)
	}
	tcp := false
	if socketType == unix.SOCK_STREAM {
		if _, tcpErr := unix.GetsockoptInt(descriptor, unix.IPPROTO_TCP, unix.TCP_NODELAY); tcpErr == nil {
			tcp = true
		} else if !errors.Is(tcpErr, unix.ENOPROTOOPT) {
			return socketObservation{}, fmt.Errorf("verify TCP socket protocol: %w", tcpErr)
		}
	}

	nativeAddress, err := unix.Getsockname(descriptor)
	if err != nil {
		return socketObservation{}, fmt.Errorf("read socket local address: %w", err)
	}
	inet4, ipv4 := nativeAddress.(*unix.SockaddrInet4)
	observation := socketObservation{
		IPv4:      ipv4,
		TCP:       tcp,
		Listening: accepting == 1,
	}
	if ipv4 {
		observation.Local = netip.AddrPortFrom(netip.AddrFrom4(inet4.Addr), uint16(inet4.Port))
	}
	return observation, nil
}
