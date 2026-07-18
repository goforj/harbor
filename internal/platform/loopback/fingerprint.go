package loopback

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const observationFingerprintDomain = "goforj.harbor.loopback-observation.v3\x00"

// Fingerprint returns a stable digest of the host facts captured by the observation.
//
// Fingerprint rejects facts that could not have been emitted by Observe. Assignment
// order and the distinction between nil and empty assignment slices do not affect
// the digest.
func (o Observation) Fingerprint() (string, error) {
	if err := validateFingerprintObservation(o); err != nil {
		return "", fmt.Errorf("fingerprint loopback observation: %w", err)
	}

	payload := append([]byte(nil), observationFingerprintDomain...)
	payload = appendFingerprintAddress(payload, o.Address)
	payload = appendFingerprintString(payload, o.Loopback.Name)
	payload = binary.AppendUvarint(payload, uint64(o.Loopback.Index))
	payload = binary.AppendUvarint(payload, o.Loopback.WindowsLUID)
	payload = appendFingerprintString(payload, string(o.Loopback.Kind))
	payload = appendFingerprintBool(payload, o.Loopback.NativeLoopback)
	payload = appendFingerprintString(payload, string(o.State))

	assignments := make([][]byte, 0, len(o.Assignments))
	for _, assignment := range o.Assignments {
		assignments = append(assignments, fingerprintAssignment(assignment))
	}
	sort.Slice(assignments, func(left, right int) bool {
		return bytes.Compare(assignments[left], assignments[right]) < 0
	})
	payload = binary.AppendUvarint(payload, uint64(len(assignments)))
	for _, assignment := range assignments {
		payload = binary.AppendUvarint(payload, uint64(len(assignment)))
		payload = append(payload, assignment...)
	}

	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// validateFingerprintObservation proves the observation is a canonical result of the package's bounded fact model.
func validateFingerprintObservation(observation Observation) error {
	if _, err := validateAddress(observation.Address); err != nil {
		return err
	}
	if err := validateFingerprintLoopback(observation.Loopback); err != nil {
		return err
	}
	if len(observation.Assignments) > maximumAssignmentFacts {
		return fmt.Errorf("assignment facts exceed limit %d", maximumAssignmentFacts)
	}
	for index, assignment := range observation.Assignments {
		if err := validateFingerprintAssignment(observation, assignment); err != nil {
			return fmt.Errorf("assignment %d: %w", index, err)
		}
	}
	classified := classify(observation.Loopback, observation.Assignments)
	if observation.State != classified {
		return fmt.Errorf("state %q does not match classified state %q", observation.State, classified)
	}
	return nil
}

// validateFingerprintLoopback enforces the identity shape selected by Observe.
func validateFingerprintLoopback(loopback InterfaceFact) error {
	if loopback.Index <= 0 || strings.TrimSpace(loopback.Name) == "" || len(loopback.Name) > maximumInterfaceName {
		return fmt.Errorf("native loopback fact is malformed")
	}
	if !loopback.NativeLoopback {
		return fmt.Errorf("selected interface is not a native loopback")
	}
	if !validInterfaceKind(loopback.Kind) {
		return fmt.Errorf("native loopback kind is unsupported")
	}
	if loopback.Kind == InterfaceKindWindowsSoftware && loopback.WindowsLUID == 0 {
		return fmt.Errorf("Windows native loopback LUID is missing")
	}
	if loopback.Kind != InterfaceKindWindowsSoftware && loopback.WindowsLUID != 0 {
		return fmt.Errorf("non-Windows native loopback contains a Windows LUID")
	}
	return nil
}

// validateFingerprintAssignment binds an assignment to either the selected loopback or a bounded ordinary interface.
func validateFingerprintAssignment(observation Observation, assignment AssignmentFact) error {
	if assignment.Address != observation.Address || assignment.PrefixLength < 0 || assignment.PrefixLength > 32 {
		return fmt.Errorf("assignment fact is malformed")
	}
	if assignment.InterfaceIndex <= 0 || strings.TrimSpace(assignment.InterfaceName) == "" || len(assignment.InterfaceName) > maximumInterfaceName {
		return fmt.Errorf("assignment interface fact is malformed")
	}
	if assignment.InterfaceIndex == observation.Loopback.Index {
		if assignment.InterfaceName != observation.Loopback.Name ||
			!assignment.NativeLoopback ||
			assignment.InterfaceKind != observation.Loopback.Kind {
			return fmt.Errorf("assignment does not match the selected native loopback")
		}
	} else if assignment.NativeLoopback || assignment.InterfaceKind != "" {
		return fmt.Errorf("ordinary assignment reports native loopback evidence")
	}

	if observation.Loopback.Kind == InterfaceKindWindowsSoftware {
		if err := validateFingerprintWindowsAssignment(assignment.Windows); err != nil {
			return err
		}
		if assignment.InterfaceIndex == observation.Loopback.Index && assignment.Windows.InterfaceLUID != observation.Loopback.WindowsLUID {
			return fmt.Errorf("Windows assignment LUID does not match the selected loopback")
		}
	} else if assignment.Windows != nil {
		return fmt.Errorf("non-Windows assignment contains Windows attributes")
	}
	if observation.Loopback.Kind == InterfaceKindLinuxNative {
		if err := validateFingerprintLinuxAssignment(assignment.Linux); err != nil {
			return err
		}
	} else if assignment.Linux != nil {
		return fmt.Errorf("non-Linux assignment contains Linux attributes")
	}
	return nil
}

// validateFingerprintLinuxAssignment rejects scope values the Linux backend cannot emit.
func validateFingerprintLinuxAssignment(fact *LinuxAssignmentFact) error {
	if fact == nil {
		return fmt.Errorf("Linux assignment attributes are missing")
	}
	switch fact.Scope {
	case LinuxAddressScopeUniverse,
		LinuxAddressScopeSite,
		LinuxAddressScopeLink,
		LinuxAddressScopeHost,
		LinuxAddressScopeNowhere,
		LinuxAddressScopeUnknown:
		if len(fact.Label) > maximumLinuxLabel || strings.ContainsRune(fact.Label, 0) {
			return fmt.Errorf("Linux address label is malformed")
		}
		if fact.AdditionalAttributesSHA256 != "" {
			if len(fact.AdditionalAttributesSHA256) != 64 {
				return fmt.Errorf("Linux additional-attribute fingerprint is malformed")
			}
			for _, character := range fact.AdditionalAttributesSHA256 {
				if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
					continue
				}
				return fmt.Errorf("Linux additional-attribute fingerprint is malformed")
			}
		}
		return nil
	default:
		return fmt.Errorf("Linux address scope %q is unsupported", fact.Scope)
	}
}

// validateFingerprintWindowsAssignment rejects enum values that the Windows backend cannot emit.
func validateFingerprintWindowsAssignment(fact *WindowsAssignmentFact) error {
	if fact == nil {
		return fmt.Errorf("Windows assignment attributes are missing")
	}
	if fact.InterfaceLUID == 0 {
		return fmt.Errorf("Windows assignment LUID is missing")
	}
	if !validFingerprintPrefixOrigin(fact.PrefixOrigin) {
		return fmt.Errorf("Windows prefix origin %q is unsupported", fact.PrefixOrigin)
	}
	if !validFingerprintSuffixOrigin(fact.SuffixOrigin) {
		return fmt.Errorf("Windows suffix origin %q is unsupported", fact.SuffixOrigin)
	}
	if !validFingerprintAddressState(fact.DADState) {
		return fmt.Errorf("Windows address state %q is unsupported", fact.DADState)
	}
	return nil
}

// validFingerprintPrefixOrigin recognizes every bounded value emitted for an IP Helper prefix origin.
func validFingerprintPrefixOrigin(origin AddressOrigin) bool {
	switch origin {
	case AddressOriginOther,
		AddressOriginManual,
		AddressOriginWellKnown,
		AddressOriginDHCP,
		AddressOriginRouterAdvertisement,
		AddressOriginUnchanged,
		AddressOriginUnknown:
		return true
	default:
		return false
	}
}

// validFingerprintSuffixOrigin recognizes every bounded value emitted for an IP Helper suffix origin.
func validFingerprintSuffixOrigin(origin AddressOrigin) bool {
	switch origin {
	case AddressOriginOther,
		AddressOriginManual,
		AddressOriginWellKnown,
		AddressOriginDHCP,
		AddressOriginLinkLayer,
		AddressOriginRandom,
		AddressOriginUnchanged,
		AddressOriginUnknown:
		return true
	default:
		return false
	}
}

// validFingerprintAddressState recognizes every bounded duplicate-address-detection state emitted by IP Helper.
func validFingerprintAddressState(state AddressState) bool {
	switch state {
	case AddressStateInvalid,
		AddressStateTentative,
		AddressStateDuplicate,
		AddressStateDeprecated,
		AddressStatePreferred,
		AddressStateUnknown:
		return true
	default:
		return false
	}
}

// fingerprintAssignment encodes every assignment fact so bytewise sorting has the same stable field semantics as hashing.
func fingerprintAssignment(assignment AssignmentFact) []byte {
	encoded := appendFingerprintAddress(nil, assignment.Address)
	encoded = binary.AppendUvarint(encoded, uint64(assignment.PrefixLength))
	encoded = appendFingerprintString(encoded, assignment.InterfaceName)
	encoded = binary.AppendUvarint(encoded, uint64(assignment.InterfaceIndex))
	encoded = appendFingerprintBool(encoded, assignment.NativeLoopback)
	encoded = appendFingerprintString(encoded, string(assignment.InterfaceKind))
	encoded = appendFingerprintBool(encoded, assignment.Linux != nil)
	if assignment.Linux != nil {
		encoded = appendFingerprintString(encoded, string(assignment.Linux.Scope))
		encoded = binary.AppendUvarint(encoded, uint64(assignment.Linux.Flags))
		encoded = appendFingerprintString(encoded, assignment.Linux.Label)
		encoded = appendFingerprintBool(encoded, assignment.Linux.AddressMatchesLocal)
		encoded = appendFingerprintBool(encoded, assignment.Linux.CacheInfoPresent)
		encoded = binary.AppendUvarint(encoded, uint64(assignment.Linux.ValidLifetimeSeconds))
		encoded = binary.AppendUvarint(encoded, uint64(assignment.Linux.PreferredLifetimeSeconds))
		encoded = appendFingerprintString(encoded, assignment.Linux.AdditionalAttributesSHA256)
	}
	encoded = appendFingerprintBool(encoded, assignment.Windows != nil)
	if assignment.Windows == nil {
		return encoded
	}
	encoded = binary.AppendUvarint(encoded, assignment.Windows.InterfaceLUID)
	encoded = appendFingerprintBool(encoded, assignment.Windows.SkipAsSource)
	encoded = appendFingerprintString(encoded, string(assignment.Windows.PrefixOrigin))
	encoded = appendFingerprintString(encoded, string(assignment.Windows.SuffixOrigin))
	encoded = binary.AppendUvarint(encoded, uint64(assignment.Windows.ValidLifetimeSeconds))
	encoded = binary.AppendUvarint(encoded, uint64(assignment.Windows.PreferredLifetimeSeconds))
	encoded = appendFingerprintString(encoded, string(assignment.Windows.DADState))
	return encoded
}

// appendFingerprintAddress records the address family explicitly before its canonical bytes.
func appendFingerprintAddress(destination []byte, address netip.Addr) []byte {
	destination = append(destination, 4)
	value := address.As4()
	return append(destination, value[:]...)
}

// appendFingerprintString length-prefixes strings so field boundaries cannot collide.
func appendFingerprintString(destination []byte, value string) []byte {
	destination = binary.AppendUvarint(destination, uint64(len(value)))
	return append(destination, value...)
}

// appendFingerprintBool uses one bounded byte rather than a textual representation.
func appendFingerprintBool(destination []byte, value bool) []byte {
	if value {
		return append(destination, 1)
	}
	return append(destination, 0)
}
