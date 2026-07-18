//go:build darwin

package loopback

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

const darwinIfconfigPath = "/sbin/ifconfig"

// platformBackend implements exact macOS loopback effects.
type platformBackend struct{}

// newPlatformBackend creates the macOS adapter without acquiring privilege.
func newPlatformBackend() backend {
	return platformBackend{}
}

// interfaces verifies the native lo0 identity through Darwin interface flags.
func (platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	return nativeInterfaceFacts(ctx, "lo0", InterfaceKindDarwinNative, darwinLoopbackInterface)
}

// assignments returns every exact-address match visible through native interface APIs.
func (platformBackend) assignments(ctx context.Context, address netip.Addr) ([]AssignmentFact, error) {
	return nativeAssignmentFacts(ctx, address)
}

// ensure asks ifconfig for one exact host alias without invoking a shell.
func (platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	command := newPlatformCommand(ctx, darwinIfconfigPath, darwinEnsureArguments(interf, prefix)...)
	if err := command.Run(); err != nil {
		return fmt.Errorf("ifconfig alias failed: %w", err)
	}
	return nil
}

// release asks ifconfig to remove only the observed exact address without invoking a shell.
func (platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	command := newPlatformCommand(ctx, darwinIfconfigPath, interf.Name, "inet", prefix.Addr().String(), "-alias")
	if err := command.Run(); err != nil {
		return fmt.Errorf("ifconfig alias removal failed: %w", err)
	}
	return nil
}

// darwinEnsureArguments uses CIDR form because Darwin otherwise infers the loopback classful prefix.
func darwinEnsureArguments(interf InterfaceFact, prefix netip.Prefix) []string {
	return []string{interf.Name, "inet", prefix.String(), "alias"}
}

// darwinLoopbackInterface confirms lo0 exists with the kernel loopback flag.
func darwinLoopbackInterface() (bool, error) {
	interf, err := net.InterfaceByName("lo0")
	if err != nil {
		return false, fmt.Errorf("look up lo0: %w", err)
	}
	return interf.Flags&net.FlagLoopback != 0, nil
}
