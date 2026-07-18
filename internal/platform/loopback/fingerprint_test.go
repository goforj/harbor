package loopback

import (
	"encoding/hex"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestObservationFingerprintCanonicalizesEquivalentFacts proves host enumeration order and empty-slice representation are immaterial.
func TestObservationFingerprintCanonicalizesEquivalentFacts(t *testing.T) {
	absentNil := fingerprintLinuxObservation(StateAbsent)
	absentEmpty := absentNil
	absentEmpty.Assignments = []AssignmentFact{}
	assertSameFingerprint(t, absentNil, absentEmpty)

	first := fingerprintLinuxAssignment("native-loopback", 1, 32)
	second := AssignmentFact{
		Address:        testAddress,
		PrefixLength:   24,
		InterfaceName:  "ethernet-7",
		InterfaceIndex: 7,
	}
	ordered := fingerprintLinuxObservation(StateAmbiguous, first, second)
	reversed := fingerprintLinuxObservation(StateAmbiguous, second, first)
	before := cloneFingerprintObservation(reversed)
	assertSameFingerprint(t, ordered, reversed)
	if !reflect.DeepEqual(reversed, before) {
		t.Fatal("Fingerprint() reordered caller-owned assignment facts")
	}
}

// TestObservationFingerprintIncludesEveryObservationFact proves each independently variable fact changes the public digest.
func TestObservationFingerprintIncludesEveryObservationFact(t *testing.T) {
	reference := fingerprintWindowsObservation()
	referenceFingerprint, err := reference.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "requested and assignment address", mutate: func(observation *Observation) {
			address := netip.MustParseAddr("127.77.0.11")
			observation.Address = address
			observation.Assignments[0].Address = address
		}},
		{name: "loopback and assignment name", mutate: func(observation *Observation) {
			observation.Loopback.Name = "Loopback Pseudo-Interface 2"
			observation.Assignments[0].InterfaceName = observation.Loopback.Name
		}},
		{name: "loopback and assignment index", mutate: func(observation *Observation) {
			observation.Loopback.Index = 22
			observation.Assignments[0].InterfaceIndex = observation.Loopback.Index
		}},
		{name: "loopback and assignment kind", mutate: func(observation *Observation) {
			observation.Loopback.Kind = InterfaceKindLinuxNative
			observation.Assignments[0].InterfaceKind = InterfaceKindLinuxNative
			observation.Assignments[0].Windows = nil
		}},
		{name: "classified state and prefix", mutate: func(observation *Observation) {
			observation.Assignments[0].PrefixLength = 31
			observation.State = StateNonHostPrefix
		}},
		{name: "assignment interface placement", mutate: func(observation *Observation) {
			observation.Assignments[0].InterfaceName = "ethernet-9"
			observation.Assignments[0].InterfaceIndex = 9
			observation.Assignments[0].NativeLoopback = false
			observation.Assignments[0].InterfaceKind = ""
			observation.State = StateForeign
		}},
		{name: "Windows skip as source", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.SkipAsSource = false
			observation.State = StateAttributeConflict
		}},
		{name: "Windows prefix origin", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.PrefixOrigin = AddressOriginDHCP
			observation.State = StateAttributeConflict
		}},
		{name: "Windows suffix origin", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.SuffixOrigin = AddressOriginRandom
			observation.State = StateAttributeConflict
		}},
		{name: "Windows valid lifetime", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.ValidLifetimeSeconds = 600
			observation.State = StateAttributeConflict
		}},
		{name: "Windows preferred lifetime", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.PreferredLifetimeSeconds = 300
			observation.State = StateAttributeConflict
		}},
		{name: "Windows DAD state", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.DADState = AddressStateTentative
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := cloneFingerprintObservation(reference)
			test.mutate(&observation)
			fingerprint, err := observation.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint() error = %v", err)
			}
			if fingerprint == referenceFingerprint {
				t.Fatalf("Fingerprint() = %q after changing %s", fingerprint, test.name)
			}
		})
	}
}

// TestFingerprintAssignmentEncodingIncludesEveryField guards the field-by-field contract even where coherence fixes values together.
func TestFingerprintAssignmentEncodingIncludesEveryField(t *testing.T) {
	reference := fingerprintWindowsObservation().Assignments[0]
	referenceEncoding := fingerprintAssignment(reference)
	tests := []struct {
		name   string
		mutate func(*AssignmentFact)
	}{
		{name: "address", mutate: func(fact *AssignmentFact) { fact.Address = netip.MustParseAddr("127.77.0.12") }},
		{name: "prefix", mutate: func(fact *AssignmentFact) { fact.PrefixLength = 24 }},
		{name: "interface name", mutate: func(fact *AssignmentFact) { fact.InterfaceName = "other" }},
		{name: "interface index", mutate: func(fact *AssignmentFact) { fact.InterfaceIndex++ }},
		{name: "native loopback", mutate: func(fact *AssignmentFact) { fact.NativeLoopback = false }},
		{name: "interface kind", mutate: func(fact *AssignmentFact) { fact.InterfaceKind = InterfaceKindDarwinNative }},
		{name: "Windows presence", mutate: func(fact *AssignmentFact) { fact.Windows = nil }},
		{name: "skip as source", mutate: func(fact *AssignmentFact) { fact.Windows.SkipAsSource = false }},
		{name: "prefix origin", mutate: func(fact *AssignmentFact) { fact.Windows.PrefixOrigin = AddressOriginOther }},
		{name: "suffix origin", mutate: func(fact *AssignmentFact) { fact.Windows.SuffixOrigin = AddressOriginOther }},
		{name: "valid lifetime", mutate: func(fact *AssignmentFact) { fact.Windows.ValidLifetimeSeconds-- }},
		{name: "preferred lifetime", mutate: func(fact *AssignmentFact) { fact.Windows.PreferredLifetimeSeconds-- }},
		{name: "DAD state", mutate: func(fact *AssignmentFact) { fact.Windows.DADState = AddressStateDeprecated }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact := cloneFingerprintAssignment(reference)
			test.mutate(&fact)
			if slices.Equal(fingerprintAssignment(fact), referenceEncoding) {
				t.Fatalf("fingerprintAssignment() omitted %s", test.name)
			}
		})
	}
}

// TestObservationFingerprintAcceptsEveryClassifiedState keeps validation aligned with the mutation policy's state model.
func TestObservationFingerprintAcceptsEveryClassifiedState(t *testing.T) {
	exact := fingerprintLinuxAssignment("native-loopback", 1, 32)
	foreign := AssignmentFact{Address: testAddress, PrefixLength: 32, InterfaceName: "ethernet-2", InterfaceIndex: 2}
	tests := []Observation{
		fingerprintLinuxObservation(StateAbsent),
		fingerprintLinuxObservation(StateExact, exact),
		fingerprintLinuxObservation(StateForeign, foreign),
		fingerprintLinuxObservation(StateNonHostPrefix, fingerprintLinuxAssignment("native-loopback", 1, 8)),
		fingerprintLinuxObservation(StateAmbiguous, exact, foreign),
	}
	conflict := fingerprintWindowsObservation()
	conflict.Assignments[0].Windows.SkipAsSource = false
	conflict.State = StateAttributeConflict
	tests = append(tests, conflict)

	for _, observation := range tests {
		if _, err := observation.Fingerprint(); err != nil {
			t.Errorf("Fingerprint() state %q error = %v", observation.State, err)
		}
	}
}

// TestObservationFingerprintRejectsMalformedFacts ensures untrusted fact structures cannot acquire an authorization digest.
func TestObservationFingerprintRejectsMalformedFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Observation)
	}{
		{name: "invalid requested address", mutate: func(observation *Observation) { observation.Address = netip.Addr{} }},
		{name: "non IPv4 requested address", mutate: func(observation *Observation) { observation.Address = netip.IPv6Loopback() }},
		{name: "mapped requested address", mutate: func(observation *Observation) { observation.Address = netip.MustParseAddr("::ffff:127.77.0.10") }},
		{name: "zero loopback index", mutate: func(observation *Observation) { observation.Loopback.Index = 0 }},
		{name: "blank loopback name", mutate: func(observation *Observation) { observation.Loopback.Name = "  " }},
		{name: "long loopback name", mutate: func(observation *Observation) {
			observation.Loopback.Name = strings.Repeat("x", maximumInterfaceName+1)
		}},
		{name: "ordinary selected interface", mutate: func(observation *Observation) { observation.Loopback.NativeLoopback = false }},
		{name: "unknown loopback kind", mutate: func(observation *Observation) { observation.Loopback.Kind = "unknown" }},
		{name: "too many assignments", mutate: func(observation *Observation) {
			observation.Assignments = make([]AssignmentFact, maximumAssignmentFacts+1)
			for index := range observation.Assignments {
				observation.Assignments[index] = fingerprintLinuxAssignment("native-loopback", 1, 32)
			}
			observation.State = StateAmbiguous
		}},
		{name: "wrong assignment address", mutate: func(observation *Observation) {
			observation.Assignments[0].Address = netip.MustParseAddr("127.77.0.11")
		}},
		{name: "negative assignment prefix", mutate: func(observation *Observation) { observation.Assignments[0].PrefixLength = -1 }},
		{name: "large assignment prefix", mutate: func(observation *Observation) { observation.Assignments[0].PrefixLength = 33 }},
		{name: "zero assignment index", mutate: func(observation *Observation) { observation.Assignments[0].InterfaceIndex = 0 }},
		{name: "blank assignment name", mutate: func(observation *Observation) { observation.Assignments[0].InterfaceName = "" }},
		{name: "long assignment name", mutate: func(observation *Observation) {
			observation.Assignments[0].InterfaceName = strings.Repeat("x", maximumInterfaceName+1)
		}},
		{name: "loopback assignment name mismatch", mutate: func(observation *Observation) { observation.Assignments[0].InterfaceName = "other" }},
		{name: "loopback assignment native flag mismatch", mutate: func(observation *Observation) { observation.Assignments[0].NativeLoopback = false }},
		{name: "loopback assignment kind mismatch", mutate: func(observation *Observation) { observation.Assignments[0].InterfaceKind = InterfaceKindDarwinNative }},
		{name: "foreign assignment native flag", mutate: func(observation *Observation) {
			observation.Assignments[0].InterfaceName = "ethernet-2"
			observation.Assignments[0].InterfaceIndex = 2
			observation.Assignments[0].NativeLoopback = true
			observation.Assignments[0].InterfaceKind = ""
			observation.State = StateForeign
		}},
		{name: "foreign assignment native kind", mutate: func(observation *Observation) {
			observation.Assignments[0].InterfaceName = "ethernet-2"
			observation.Assignments[0].InterfaceIndex = 2
			observation.Assignments[0].NativeLoopback = false
			observation.Assignments[0].InterfaceKind = InterfaceKindLinuxNative
			observation.State = StateForeign
		}},
		{name: "missing Windows attributes", mutate: func(observation *Observation) { observation.Assignments[0].Windows = nil }},
		{name: "invalid Windows prefix origin", mutate: func(observation *Observation) { observation.Assignments[0].Windows.PrefixOrigin = "invalid" }},
		{name: "prefix-only origin used as suffix", mutate: func(observation *Observation) {
			observation.Assignments[0].Windows.SuffixOrigin = AddressOriginRouterAdvertisement
		}},
		{name: "suffix-only origin used as prefix", mutate: func(observation *Observation) { observation.Assignments[0].Windows.PrefixOrigin = AddressOriginRandom }},
		{name: "invalid Windows DAD state", mutate: func(observation *Observation) { observation.Assignments[0].Windows.DADState = "invalid-enum" }},
		{name: "state does not match facts", mutate: func(observation *Observation) { observation.State = StateAbsent }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := fingerprintWindowsObservation()
			test.mutate(&observation)
			if fingerprint, err := observation.Fingerprint(); err == nil {
				t.Fatalf("Fingerprint() = %q, want error", fingerprint)
			}
		})
	}

	nonWindows := fingerprintLinuxObservation(StateExact, fingerprintLinuxAssignment("native-loopback", 1, 32))
	nonWindows.Assignments[0].Windows = &WindowsAssignmentFact{}
	if fingerprint, err := nonWindows.Fingerprint(); err == nil {
		t.Fatalf("Fingerprint() with Windows facts on Linux = %q, want error", fingerprint)
	}
}

// TestObservationFingerprintKnownVector fixes the v1 encoding across implementations and platforms.
func TestObservationFingerprintKnownVector(t *testing.T) {
	fingerprint, err := fingerprintWindowsObservation().Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	const want = "eeb482c99187b58a1ebc790f3a030c6eab9c9c5ccf84bf5e421bdb2157dc4832"
	if fingerprint != want {
		t.Fatalf("Fingerprint() = %q, want %q", fingerprint, want)
	}
}

// TestObservationFingerprintOutputGrammar keeps ticket evidence bounded to lowercase raw SHA-256 hex.
func TestObservationFingerprintOutputGrammar(t *testing.T) {
	fingerprint, err := fingerprintLinuxObservation(StateAbsent).Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fingerprint) {
		t.Fatalf("Fingerprint() = %q, want 64 lowercase hexadecimal characters", fingerprint)
	}
	if decoded, err := hex.DecodeString(fingerprint); err != nil || len(decoded) != sha256DigestBytes {
		t.Fatalf("hex.DecodeString(Fingerprint()) length = %d, error = %v", len(decoded), err)
	}
}

const sha256DigestBytes = 32

// fingerprintLinuxObservation creates a coherent Linux observation for fingerprint tests.
func fingerprintLinuxObservation(state State, assignments ...AssignmentFact) Observation {
	return Observation{
		Address: testAddress,
		Loopback: InterfaceFact{
			Name:           "native-loopback",
			Index:          1,
			Kind:           InterfaceKindLinuxNative,
			NativeLoopback: true,
		},
		State:       state,
		Assignments: assignments,
	}
}

// fingerprintLinuxAssignment creates the normalized assignment shape returned by Observe.
func fingerprintLinuxAssignment(name string, index int, prefixLength int) AssignmentFact {
	return AssignmentFact{
		Address:        testAddress,
		PrefixLength:   prefixLength,
		InterfaceName:  name,
		InterfaceIndex: index,
		NativeLoopback: index == 1,
		InterfaceKind:  InterfaceKindLinuxNative,
	}
}

// fingerprintWindowsObservation creates a coherent exact Windows observation with every platform attribute populated.
func fingerprintWindowsObservation() Observation {
	loopback := InterfaceFact{
		Name:           "Loopback Pseudo-Interface 1",
		Index:          12,
		Kind:           InterfaceKindWindowsSoftware,
		NativeLoopback: true,
	}
	return Observation{
		Address:  testAddress,
		Loopback: loopback,
		State:    StateExact,
		Assignments: []AssignmentFact{{
			Address:        testAddress,
			PrefixLength:   32,
			InterfaceName:  loopback.Name,
			InterfaceIndex: loopback.Index,
			NativeLoopback: true,
			InterfaceKind:  loopback.Kind,
			Windows: &WindowsAssignmentFact{
				SkipAsSource:             true,
				PrefixOrigin:             AddressOriginManual,
				SuffixOrigin:             AddressOriginManual,
				ValidLifetimeSeconds:     ^uint32(0),
				PreferredLifetimeSeconds: ^uint32(0),
				DADState:                 AddressStatePreferred,
			},
		}},
	}
}

// cloneFingerprintObservation copies nested assignment facts so test mutations remain isolated.
func cloneFingerprintObservation(observation Observation) Observation {
	clone := observation
	clone.Assignments = make([]AssignmentFact, len(observation.Assignments))
	for index, assignment := range observation.Assignments {
		clone.Assignments[index] = cloneFingerprintAssignment(assignment)
	}
	return clone
}

// cloneFingerprintAssignment copies optional Windows attributes so callers can mutate them independently.
func cloneFingerprintAssignment(assignment AssignmentFact) AssignmentFact {
	clone := assignment
	if assignment.Windows != nil {
		windows := *assignment.Windows
		clone.Windows = &windows
	}
	return clone
}

// assertSameFingerprint compares canonical digests while preserving useful test failure context.
func assertSameFingerprint(t *testing.T, left Observation, right Observation) {
	t.Helper()
	leftFingerprint, err := left.Fingerprint()
	if err != nil {
		t.Fatalf("left Fingerprint() error = %v", err)
	}
	rightFingerprint, err := right.Fingerprint()
	if err != nil {
		t.Fatalf("right Fingerprint() error = %v", err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("Fingerprint() = %q and %q, want equal", leftFingerprint, rightFingerprint)
	}
}
