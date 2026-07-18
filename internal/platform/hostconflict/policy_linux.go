//go:build linux

package hostconflict

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

const linuxMaximumProcValueBytes = 16

// linuxProcOperations isolates verified procfs traversal and bounded value reads from policy semantics.
type linuxProcOperations struct {
	open     func(string, int, uint32) (int, error)
	openAt   func(int, string, int, uint32) (int, error)
	close    func(int) error
	fileStat func(int, *unix.Stat_t) error
	fsStat   func(int, *unix.Statfs_t) error
	read     func(int, []byte) (int, error)
}

// observeLinuxPolicy reads namespace-scoped sysctls through verified procfs descriptors.
func observeLinuxPolicy(ctx context.Context, interfaces linuxInterfaceSnapshot) (*LinuxPolicyFacts, error) {
	operations := linuxProcOperations{
		open:     unix.Open,
		openAt:   unix.Openat,
		close:    unix.Close,
		fileStat: unix.Fstat,
		fsStat:   unix.Fstatfs,
		read:     unix.Read,
	}
	return observeLinuxPolicyWith(ctx, interfaces, operations)
}

// observeLinuxPolicyWith checks cancellation between each bounded procfs operation.
func observeLinuxPolicyWith(ctx context.Context, interfaces linuxInterfaceSnapshot, operations linuxProcOperations) (*LinuxPolicyFacts, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	procDescriptor, err := operations.open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("host conflict Linux open procfs: %w", err)
	}
	defer func() { _ = operations.close(procDescriptor) }()
	if err := verifyLinuxProcDescriptor(operations, procDescriptor, "root"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sysDescriptor, err := openLinuxProcDirectory(operations, procDescriptor, "sys")
	if err != nil {
		return nil, err
	}
	defer func() { _ = operations.close(sysDescriptor) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	netDescriptor, err := openLinuxProcDirectory(operations, sysDescriptor, "net")
	if err != nil {
		return nil, err
	}
	defer func() { _ = operations.close(netDescriptor) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ipv4Descriptor, err := openLinuxProcDirectory(operations, netDescriptor, "ipv4")
	if err != nil {
		return nil, err
	}
	defer func() { _ = operations.close(ipv4Descriptor) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ipNonlocalBind, err := readLinuxProcBoolean(operations, ipv4Descriptor, "ip_nonlocal_bind")
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	confDescriptor, err := openLinuxProcDirectory(operations, ipv4Descriptor, "conf")
	if err != nil {
		return nil, err
	}
	defer func() { _ = operations.close(confDescriptor) }()
	return assembleLinuxPolicy(interfaces, ipNonlocalBind, func(interfaceName string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return readLinuxRouteLocalnet(operations, confDescriptor, interfaceName)
	})
}

// assembleLinuxPolicy applies Linux's all-or-interface effective value and future-interface default rule.
func assembleLinuxPolicy(interfaces linuxInterfaceSnapshot, ipNonlocalBind bool, routeLocalnet func(string) (bool, error)) (*LinuxPolicyFacts, error) {
	allEnabled, err := routeLocalnet("all")
	if err != nil {
		return nil, err
	}
	defaultEnabled, err := routeLocalnet("default")
	if err != nil {
		return nil, err
	}

	facts := &LinuxPolicyFacts{
		Complete:       interfaces.complete && !allEnabled && !defaultEnabled,
		Truncated:      interfaces.truncated,
		IPNonlocalBind: ipNonlocalBind,
		RouteLocalnet:  make([]RouteLocalnetFact, 0, len(interfaces.ordered)),
	}
	for _, linuxInterface := range interfaces.ordered {
		if strings.Contains(linuxInterface.identity.Name, "/") || linuxInterface.identity.Name == "." || linuxInterface.identity.Name == ".." {
			return nil, fmt.Errorf("host conflict Linux interface %q is not a safe procfs component", linuxInterface.identity.Name)
		}
		enabled, err := routeLocalnet(linuxInterface.identity.Name)
		if err != nil {
			return nil, err
		}
		facts.RouteLocalnet = append(facts.RouteLocalnet, RouteLocalnetFact{
			Interface: linuxInterface.identity,
			Enabled:   allEnabled || enabled,
		})
	}
	if facts.Truncated {
		facts.Complete = false
	}
	return facts, nil
}

// openLinuxProcDirectory walks one component at a time so a mutable path cannot escape verified procfs.
func openLinuxProcDirectory(operations linuxProcOperations, parentDescriptor int, name string) (int, error) {
	descriptor, err := operations.openAt(parentDescriptor, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("host conflict Linux open procfs directory %q: %w", name, err)
	}
	if err := verifyLinuxProcDescriptor(operations, descriptor, name); err != nil {
		_ = operations.close(descriptor)
		return -1, err
	}
	return descriptor, nil
}

// verifyLinuxProcDescriptor rejects submounts that could substitute attacker-controlled policy files.
func verifyLinuxProcDescriptor(operations linuxProcOperations, fileDescriptor int, label string) error {
	var fileSystem unix.Statfs_t
	if err := operations.fsStat(fileDescriptor, &fileSystem); err != nil {
		return fmt.Errorf("host conflict Linux inspect procfs %q: %w", label, err)
	}
	if uint64(fileSystem.Type) != uint64(unix.PROC_SUPER_MAGIC) {
		return fmt.Errorf("host conflict Linux policy path %q is not procfs", label)
	}
	return nil
}

// readLinuxRouteLocalnet reads one interface directory without interpolating a slash-containing path.
func readLinuxRouteLocalnet(operations linuxProcOperations, confDescriptor int, interfaceName string) (bool, error) {
	interfaceDescriptor, err := openLinuxProcDirectory(operations, confDescriptor, interfaceName)
	if err != nil {
		return false, err
	}
	defer func() { _ = operations.close(interfaceDescriptor) }()
	return readLinuxProcBoolean(operations, interfaceDescriptor, "route_localnet")
}

// readLinuxProcBoolean accepts only the canonical one-bit sysctl representation.
func readLinuxProcBoolean(operations linuxProcOperations, parentDescriptor int, name string) (bool, error) {
	fileDescriptor, err := operations.openAt(parentDescriptor, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("host conflict Linux open procfs value %q: %w", name, err)
	}
	defer func() { _ = operations.close(fileDescriptor) }()
	if err := verifyLinuxProcDescriptor(operations, fileDescriptor, name); err != nil {
		return false, err
	}
	var status unix.Stat_t
	if err := operations.fileStat(fileDescriptor, &status); err != nil {
		return false, fmt.Errorf("host conflict Linux inspect procfs value %q: %w", name, err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, fmt.Errorf("host conflict Linux procfs value %q is not regular", name)
	}

	buffer := make([]byte, linuxMaximumProcValueBytes+1)
	length := 0
	for length < len(buffer) {
		read, err := operations.read(fileDescriptor, buffer[length:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("host conflict Linux read procfs value %q: %w", name, err)
		}
		if read == 0 {
			break
		}
		length += read
	}
	if length > linuxMaximumProcValueBytes {
		return false, fmt.Errorf("host conflict Linux procfs value %q exceeds its bound", name)
	}
	switch string(buffer[:length]) {
	case "0\n":
		return false, nil
	case "1\n":
		return true, nil
	default:
		return false, fmt.Errorf("host conflict Linux procfs value %q is not a canonical boolean", name)
	}
}
