package networkplan

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
)

const (
	redirectedPortDomain     = "goforj.harbor.host-network-plan-ports.v1\x00"
	firstRedirectedPort      = uint16(21000)
	redirectedPortsPerBlock  = uint16(3)
	redirectedPortBlockCount = uint64(3000)
)

var (
	// ErrUnsupportedPlatform identifies a product profile that Harbor cannot safely construct.
	ErrUnsupportedPlatform = errors.New("unsupported host network plan platform")

	localhost          = netip.MustParseAddr("127.0.0.1")
	windowsDNSLoopback = netip.MustParseAddr("127.0.0.2")
)

// Platform identifies one versioned host-integration product profile.
type Platform string

const (
	// PlatformMacOS identifies Harbor's first supported macOS product profile.
	PlatformMacOS Platform = "macos-v1"
	// PlatformUbuntu2404 identifies Harbor's first supported Ubuntu 24.04 product profile.
	PlatformUbuntu2404 Platform = "ubuntu-24.04-v1"
	// PlatformWindows11 identifies Harbor's first supported Windows 11 product profile.
	PlatformWindows11 Platform = "windows-11-v1"
)

// Request contains every stable input needed to construct a host network policy.
type Request struct {
	Platform             Platform
	InstallationID       identity.InstallationID
	Pool                 identity.Pool
	AuthorityFingerprint string
}

// UnsupportedPlatformError records the platform value that prevented construction.
type UnsupportedPlatformError struct {
	Platform Platform
}

// Error describes the unsupported product profile.
func (unsupported *UnsupportedPlatformError) Error() string {
	return fmt.Sprintf("%s %q", ErrUnsupportedPlatform, unsupported.Platform)
}

// Unwrap preserves sentinel matching while retaining the rejected platform.
func (unsupported *UnsupportedPlatformError) Unwrap() error {
	return ErrUnsupportedPlatform
}

// Build validates stable inputs and constructs the complete policy for one supported product profile.
func Build(request Request) (networkpolicy.Policy, error) {
	if err := request.InstallationID.Validate(); err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("host network plan: installation ID: %w", err)
	}
	if err := request.Pool.Validate(); err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("host network plan: pool: %w", err)
	}

	switch request.Platform {
	case PlatformMacOS:
		return buildRedirectedPolicy(request, networkpolicy.MacOSMechanisms())
	case PlatformUbuntu2404:
		return buildRedirectedPolicy(request, networkpolicy.UbuntuMechanisms())
	case PlatformWindows11:
		return buildWindowsPolicy(request)
	default:
		return networkpolicy.Policy{}, &UnsupportedPlatformError{Platform: request.Platform}
	}
}

// BuildLegacyMacOS constructs only the exact persisted macOS policy that used current-user trust.
func BuildLegacyMacOS(request Request) (networkpolicy.Policy, error) {
	if request.Platform != PlatformMacOS {
		return networkpolicy.Policy{}, &UnsupportedPlatformError{Platform: request.Platform}
	}
	if err := request.InstallationID.Validate(); err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("legacy macOS host network plan: installation ID: %w", err)
	}
	if err := request.Pool.Validate(); err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("legacy macOS host network plan: pool: %w", err)
	}
	return buildRedirectedPolicy(request, networkpolicy.LegacyMacOSMechanisms())
}

// buildRedirectedPolicy keeps all supported redirected profiles on the same installation-stable socket contract.
func buildRedirectedPolicy(request Request, mechanisms networkpolicy.Mechanisms) (networkpolicy.Policy, error) {
	firstPort := redirectedPortBlockStart(request.InstallationID)
	dnsSocket := netip.AddrPortFrom(localhost, firstPort)
	httpBind := netip.AddrPortFrom(localhost, firstPort+1)
	httpsBind := netip.AddrPortFrom(localhost, firstPort+2)

	return networkpolicy.New(
		request.AuthorityFingerprint,
		mechanisms,
		networkpolicy.Listener{Advertised: dnsSocket, Bind: dnsSocket},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(localhost, 80), Bind: httpBind},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(localhost, 443), Bind: httpsBind},
	)
}

// buildWindowsPolicy rejects a pool collision before reserving the dedicated NRPT listener address.
func buildWindowsPolicy(request Request) (networkpolicy.Policy, error) {
	if request.Pool.Contains(windowsDNSLoopback) {
		return networkpolicy.Policy{}, fmt.Errorf("host network plan: Windows DNS address %s is a project-pool candidate", windowsDNSLoopback)
	}

	dnsSocket := netip.AddrPortFrom(windowsDNSLoopback, 53)
	httpSocket := netip.AddrPortFrom(localhost, 80)
	httpsSocket := netip.AddrPortFrom(localhost, 443)
	return networkpolicy.New(
		request.AuthorityFingerprint,
		networkpolicy.WindowsMechanisms(),
		networkpolicy.Listener{Advertised: dnsSocket, Bind: dnsSocket},
		networkpolicy.Listener{Advertised: httpSocket, Bind: httpSocket},
		networkpolicy.Listener{Advertised: httpsSocket, Bind: httpsSocket},
	)
}

// redirectedPortBlockStart uses alignment so every possible digest maps all three listeners inside Harbor's private planning window.
func redirectedPortBlockStart(installationID identity.InstallationID) uint16 {
	digest := sha256.Sum256([]byte(redirectedPortDomain + string(installationID)))
	block := binary.BigEndian.Uint64(digest[:8]) % redirectedPortBlockCount
	return firstRedirectedPort + uint16(block)*redirectedPortsPerBlock
}
