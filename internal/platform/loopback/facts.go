package loopback

import "net/netip"

const (
	maximumInterfaceFacts  = 4096
	maximumAssignmentFacts = 8
	maximumInterfaceName   = 1024
)

// InterfaceKind identifies the operating-system fact that verified an interface as loopback.
type InterfaceKind string

const (
	// InterfaceKindLinuxNative identifies Linux's native lo interface.
	InterfaceKindLinuxNative InterfaceKind = "linux-lo"
	// InterfaceKindDarwinNative identifies macOS's native lo0 interface.
	InterfaceKindDarwinNative InterfaceKind = "darwin-lo0"
	// InterfaceKindWindowsSoftware identifies an IP Helper software-loopback interface.
	InterfaceKindWindowsSoftware InterfaceKind = "windows-software-loopback"
)

// InterfaceFact describes one bounded host-interface observation.
type InterfaceFact struct {
	Name           string
	Index          int
	Kind           InterfaceKind
	NativeLoopback bool
	// WindowsLUID preserves the stable Windows interface identity across index reuse.
	WindowsLUID uint64
}

// AssignmentFact describes one exact address assignment reported by the host.
type AssignmentFact struct {
	Address        netip.Addr
	PrefixLength   int
	InterfaceName  string
	InterfaceIndex int
	NativeLoopback bool
	InterfaceKind  InterfaceKind
	Windows        *WindowsAssignmentFact
}

// AddressOrigin classifies the bounded origin fact reported for a Windows address.
type AddressOrigin string

const (
	// AddressOriginOther identifies an origin explicitly reported as other.
	AddressOriginOther AddressOrigin = "other"
	// AddressOriginManual identifies an address configured through a manual effect.
	AddressOriginManual AddressOrigin = "manual"
	// AddressOriginWellKnown identifies a well-known address origin.
	AddressOriginWellKnown AddressOrigin = "well-known"
	// AddressOriginDHCP identifies an address originating from DHCP.
	AddressOriginDHCP AddressOrigin = "dhcp"
	// AddressOriginRouterAdvertisement identifies a router-advertisement prefix origin.
	AddressOriginRouterAdvertisement AddressOrigin = "router-advertisement"
	// AddressOriginLinkLayer identifies a link-layer suffix origin.
	AddressOriginLinkLayer AddressOrigin = "link-layer"
	// AddressOriginRandom identifies a randomized suffix origin.
	AddressOriginRandom AddressOrigin = "random"
	// AddressOriginUnchanged identifies an IP Helper unchanged sentinel.
	AddressOriginUnchanged AddressOrigin = "unchanged"
	// AddressOriginUnknown identifies an origin outside the known IP Helper values.
	AddressOriginUnknown AddressOrigin = "unknown"
)

// AddressState classifies Windows duplicate-address-detection evidence.
type AddressState string

const (
	// AddressStateInvalid identifies an address whose duplicate-address-detection state is invalid.
	AddressStateInvalid AddressState = "invalid"
	// AddressStateTentative identifies an address still undergoing duplicate-address detection.
	AddressStateTentative AddressState = "tentative"
	// AddressStateDuplicate identifies an address rejected as a duplicate.
	AddressStateDuplicate AddressState = "duplicate"
	// AddressStateDeprecated identifies an address that is no longer preferred.
	AddressStateDeprecated AddressState = "deprecated"
	// AddressStatePreferred identifies an address accepted by duplicate-address detection.
	AddressStatePreferred AddressState = "preferred"
	// AddressStateUnknown identifies a state outside the known IP Helper values.
	AddressStateUnknown AddressState = "unknown"
)

// WindowsAssignmentFact contains the IP Helper attributes required for Harbor's active assignment shape.
type WindowsAssignmentFact struct {
	// InterfaceLUID preserves the stable interface identity reported with the address row.
	InterfaceLUID            uint64
	SkipAsSource             bool
	PrefixOrigin             AddressOrigin
	SuffixOrigin             AddressOrigin
	ValidLifetimeSeconds     uint32
	PreferredLifetimeSeconds uint32
	DADState                 AddressState
}

// State classifies the observed placement of one requested address.
type State string

const (
	// StateAbsent means the exact address is not assigned to any observed interface.
	StateAbsent State = "absent"
	// StateExact means one /32 with the required platform attributes exists on the selected native loopback.
	StateExact State = "exact"
	// StateForeign means the address exists on an interface other than the selected native loopback.
	StateForeign State = "foreign"
	// StateNonHostPrefix means the address exists on the selected loopback with a prefix other than /32.
	StateNonHostPrefix State = "non-/32"
	// StateAttributeConflict means a /32 lacks the platform attributes required by an exact assignment.
	StateAttributeConflict State = "attribute-conflict"
	// StateAmbiguous means more than one exact-address assignment was observed.
	StateAmbiguous State = "ambiguous"
)

// Observation contains the bounded facts used to classify one exact address.
type Observation struct {
	Address     netip.Addr
	Loopback    InterfaceFact
	State       State
	Assignments []AssignmentFact
}

// Change reports the facts before and after one requested mutation.
type Change struct {
	// Attempted distinguishes a platform call from an already-satisfied request.
	Attempted bool
	// Changed reports an observed state transition, not merely a successful command exit.
	Changed bool
	// Indeterminate means the platform call began but a fresh bounded observation could not classify its effect.
	Indeterminate bool
	// Before is the complete observation that admitted or rejected the platform call.
	Before Observation
	// After is populated when no call was needed or post-call reconciliation completed.
	After Observation
}
