//go:build linux

package loopback

import (
	"context"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"
)

const linuxIPPath = "/usr/sbin/ip"

// platformBackend implements exact Linux loopback effects.
type platformBackend struct{}

// newPlatformBackend creates the Linux adapter without acquiring privilege.
func newPlatformBackend() backend {
	return platformBackend{}
}

// interfaces verifies lo with both kernel flags and its native ARP hardware type.
func (platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	return nativeInterfaceFacts(ctx, "lo", InterfaceKindLinuxNative, linuxLoopbackHardware)
}

// assignments returns every exact-address match visible through native interface APIs.
func (platformBackend) assignments(ctx context.Context, address netip.Addr) ([]AssignmentFact, error) {
	return nativeAssignmentFacts(ctx, address)
}

// ensure asks iproute2 for one exact /32 effect without invoking a shell.
func (platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	return runLinuxIP(ctx, "add", interf, prefix)
}

// release asks iproute2 to remove only the requested /32 without invoking a shell.
func (platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	return runLinuxIP(ctx, "del", interf, prefix)
}

// linuxLoopbackHardware proves lo is the kernel's ARPHRD_LOOPBACK device.
func linuxLoopbackHardware() (bool, error) {
	file, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("open interface verification socket: %w", err)
	}
	defer unix.Close(file)
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return false, fmt.Errorf("prepare lo verification: %w", err)
	}
	if err := unix.IoctlIfreq(file, unix.SIOCGIFHWADDR, request); err != nil {
		return false, fmt.Errorf("verify lo hardware type: %w", err)
	}
	return request.Uint16() == unix.ARPHRD_LOOPBACK, nil
}

// runLinuxIP confines iproute2 to one fixed executable, interface, address, and prefix.
func runLinuxIP(ctx context.Context, action string, interf InterfaceFact, prefix netip.Prefix) error {
	command := newPlatformCommand(ctx, linuxIPPath, "address", action, prefix.String(), "dev", interf.Name)
	if err := command.Run(); err != nil {
		return fmt.Errorf("ip address %s failed: %w", action, err)
	}
	return nil
}
