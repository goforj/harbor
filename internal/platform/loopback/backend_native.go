//go:build linux || darwin

package loopback

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
)

// newPlatformCommand prevents project or elevated-process environment state from influencing fixed native tools.
func newPlatformCommand(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = "/"
	command.Env = []string{}
	return command
}

// nativeInterfaceFacts reads interface identity through Go's native operating-system network APIs.
func nativeInterfaceFacts(ctx context.Context, nativeName string, kind InterfaceKind, verify func() (bool, error)) ([]InterfaceFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	if len(interfaces) > maximumInterfaceFacts {
		return nil, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
	}
	nativeVerified, err := verify()
	if err != nil {
		return nil, err
	}
	facts := make([]InterfaceFact, 0, len(interfaces))
	for _, interf := range interfaces {
		isNative := interf.Name == nativeName && interf.Flags&net.FlagLoopback != 0 && nativeVerified
		fact := InterfaceFact{Name: interf.Name, Index: interf.Index, NativeLoopback: isNative}
		if isNative {
			fact.Kind = kind
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// nativeAssignmentFacts reads only exact-address matches from every observed interface.
func nativeAssignmentFacts(ctx context.Context, target netip.Addr) ([]AssignmentFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	facts := make([]AssignmentFact, 0, 1)
	for _, interf := range interfaces {
		addresses, err := interf.Addrs()
		if err != nil {
			return nil, fmt.Errorf("read interface %d addresses: %w", interf.Index, err)
		}
		for _, address := range addresses {
			observed, prefixLength, ok := nativeAddressFact(address)
			if !ok || observed != target {
				continue
			}
			facts = append(facts, AssignmentFact{
				Address:        observed,
				PrefixLength:   prefixLength,
				InterfaceName:  interf.Name,
				InterfaceIndex: interf.Index,
			})
			if len(facts) > maximumAssignmentFacts {
				return nil, fmt.Errorf("assignment count exceeds limit %d", maximumAssignmentFacts)
			}
		}
	}
	return facts, nil
}

// nativeAddressFact extracts the local address rather than the masked network prefix.
func nativeAddressFact(address net.Addr) (netip.Addr, int, bool) {
	switch value := address.(type) {
	case *net.IPNet:
		parsed, ok := netip.AddrFromSlice(value.IP)
		if !ok {
			return netip.Addr{}, 0, false
		}
		ones, bits := value.Mask.Size()
		if ones < 0 || bits != 32 {
			return netip.Addr{}, 0, false
		}
		return parsed.Unmap(), ones, true
	case *net.IPAddr:
		parsed, ok := netip.AddrFromSlice(value.IP)
		if !ok || !parsed.Unmap().Is4() {
			return netip.Addr{}, 0, false
		}
		return parsed.Unmap(), 32, true
	default:
		return netip.Addr{}, 0, false
	}
}
