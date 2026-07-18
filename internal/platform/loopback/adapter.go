package loopback

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const postMutationObservationTimeout = 5 * time.Second

// backend confines platform implementations to typed facts and exact prefix effects.
type backend interface {
	interfaces(context.Context) ([]InterfaceFact, error)
	assignments(context.Context, netip.Addr) ([]AssignmentFact, error)
	ensure(context.Context, InterfaceFact, netip.Prefix) error
	release(context.Context, InterfaceFact, netip.Prefix) error
}

// Adapter applies the platform-neutral safety policy around host loopback effects.
type Adapter struct {
	backend backend
}

// New creates an adapter backed by the current operating system.
func New() *Adapter {
	return newAdapter(newPlatformBackend())
}

// newAdapter injects typed host effects so the safety policy can be tested without elevation.
func newAdapter(platform backend) *Adapter {
	return &Adapter{backend: platform}
}

// Observe returns bounded facts for one canonical IPv4 loopback address.
func (a *Adapter) Observe(ctx context.Context, address netip.Addr) (Observation, error) {
	const operation = "observe"
	address, err := validateAddress(address)
	if err != nil {
		return Observation{}, operationError(ErrorKindInvalidAddress, operation, address, "", Observation{}, err)
	}
	ctx = normalizedContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, operation, address, "", Observation{}, err)
	}

	interfaces, err := a.backend.interfaces(ctx)
	if err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, operation, address, "", Observation{}, err)
	}
	loopback, err := selectLoopback(interfaces)
	if err != nil {
		return Observation{}, operationError(loopbackSelectionErrorKind(err), operation, address, "", Observation{}, err)
	}

	assignments, err := a.backend.assignments(ctx, address)
	if err != nil {
		return Observation{}, operationError(ErrorKindObserveFailed, operation, address, "", Observation{}, err)
	}
	assignments, err = validateAssignments(address, assignments, interfaces, loopback)
	if err != nil {
		return Observation{}, operationError(ErrorKindInvalidFacts, operation, address, "", Observation{}, err)
	}

	observation := Observation{
		Address:     address,
		Loopback:    loopback,
		State:       classify(loopback, assignments),
		Assignments: assignments,
	}
	return observation, nil
}

// Ensure creates one exact /32 assignment when the requested address is absent.
func (a *Adapter) Ensure(ctx context.Context, address netip.Addr) (Change, error) {
	return a.ensure(ctx, address, "")
}

// EnsureIfObserved creates one exact /32 assignment only while the pre-mutation observation fingerprint still matches.
func (a *Adapter) EnsureIfObserved(ctx context.Context, address netip.Addr, expectedFingerprint string) (Change, error) {
	if err := validateExpectedFingerprint(expectedFingerprint); err != nil {
		return Change{}, operationError(ErrorKindInvalidFacts, "ensure", address, "", Observation{}, err)
	}
	return a.ensure(ctx, address, expectedFingerprint)
}

// ensure applies the ensure policy after optionally binding it to one caller-observed host snapshot.
func (a *Adapter) ensure(ctx context.Context, address netip.Addr, expectedFingerprint string) (Change, error) {
	const operation = "ensure"
	before, err := a.Observe(ctx, address)
	if err != nil {
		return Change{}, err
	}
	if err := matchExpectedFingerprint(before, expectedFingerprint); err != nil {
		return Change{Before: before, After: before}, operationError(ErrorKindObservationChanged, operation, before.Address, before.State, before, err)
	}
	switch before.State {
	case StateExact:
		return Change{Before: before, After: before}, nil
	case StateAbsent:
	case StateForeign, StateNonHostPrefix, StateAttributeConflict, StateAmbiguous:
		return Change{Before: before, After: before}, operationError(ErrorKindConflict, operation, before.Address, before.State, before, nil)
	default:
		return Change{Before: before, After: before}, operationError(ErrorKindInvalidFacts, operation, before.Address, before.State, before, nil)
	}

	prefix := netip.PrefixFrom(before.Address, 32)
	mutationContext := normalizedContext(ctx)
	if err := mutationContext.Err(); err != nil {
		return Change{Before: before}, operationError(ErrorKindMutationFailed, operation, before.Address, before.State, before, err)
	}
	mutationErr := a.backend.ensure(mutationContext, before.Loopback, prefix)
	change, observationErr := a.reconcileMutation(before)
	if mutationErr != nil {
		return change, operationError(ErrorKindMutationFailed, operation, before.Address, changeState(change), changeObservation(change), errors.Join(mutationErr, observationErr))
	}
	if observationErr != nil {
		return change, operationError(ErrorKindVerificationFailed, operation, before.Address, before.State, before, observationErr)
	}
	if change.After.State != StateExact {
		return change, operationError(ErrorKindVerificationFailed, operation, before.Address, change.After.State, change.After, nil)
	}
	return change, nil
}

// Release removes only an exact /32 assignment observed on the selected native loopback.
func (a *Adapter) Release(ctx context.Context, address netip.Addr) (Change, error) {
	return a.release(ctx, address, "")
}

// ReleaseIfObserved removes an exact /32 assignment only while the pre-mutation observation fingerprint still matches.
func (a *Adapter) ReleaseIfObserved(ctx context.Context, address netip.Addr, expectedFingerprint string) (Change, error) {
	if err := validateExpectedFingerprint(expectedFingerprint); err != nil {
		return Change{}, operationError(ErrorKindInvalidFacts, "release", address, "", Observation{}, err)
	}
	return a.release(ctx, address, expectedFingerprint)
}

// release applies the release policy after optionally binding it to one caller-observed host snapshot.
func (a *Adapter) release(ctx context.Context, address netip.Addr, expectedFingerprint string) (Change, error) {
	const operation = "release"
	before, err := a.Observe(ctx, address)
	if err != nil {
		return Change{}, err
	}
	if err := matchExpectedFingerprint(before, expectedFingerprint); err != nil {
		return Change{Before: before, After: before}, operationError(ErrorKindObservationChanged, operation, before.Address, before.State, before, err)
	}
	switch before.State {
	case StateAbsent:
		return Change{Before: before, After: before}, nil
	case StateExact:
	case StateForeign, StateNonHostPrefix, StateAttributeConflict, StateAmbiguous:
		return Change{Before: before, After: before}, operationError(ErrorKindConflict, operation, before.Address, before.State, before, nil)
	default:
		return Change{Before: before, After: before}, operationError(ErrorKindInvalidFacts, operation, before.Address, before.State, before, nil)
	}

	prefix := netip.PrefixFrom(before.Address, 32)
	mutationContext := normalizedContext(ctx)
	if err := mutationContext.Err(); err != nil {
		return Change{Before: before}, operationError(ErrorKindMutationFailed, operation, before.Address, before.State, before, err)
	}
	mutationErr := a.backend.release(mutationContext, before.Loopback, prefix)
	change, observationErr := a.reconcileMutation(before)
	if mutationErr != nil {
		return change, operationError(ErrorKindMutationFailed, operation, before.Address, changeState(change), changeObservation(change), errors.Join(mutationErr, observationErr))
	}
	if observationErr != nil {
		return change, operationError(ErrorKindVerificationFailed, operation, before.Address, before.State, before, observationErr)
	}
	if change.After.State != StateAbsent {
		return change, operationError(ErrorKindVerificationFailed, operation, before.Address, change.After.State, change.After, nil)
	}
	return change, nil
}

// validateExpectedFingerprint keeps conditional mutations in the observation fingerprint's exact lowercase SHA-256 namespace.
func validateExpectedFingerprint(fingerprint string) error {
	if len(fingerprint) != 64 {
		return fmt.Errorf("expected observation fingerprint must contain 64 lowercase hexadecimal characters")
	}
	for _, character := range fingerprint {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return fmt.Errorf("expected observation fingerprint must contain 64 lowercase hexadecimal characters")
	}
	return nil
}

// matchExpectedFingerprint rejects drift immediately before the platform mutation while preserving unconditional adapter callers.
func matchExpectedFingerprint(observation Observation, expectedFingerprint string) error {
	if expectedFingerprint == "" {
		return nil
	}
	actual, err := observation.Fingerprint()
	if err != nil {
		return err
	}
	if actual != expectedFingerprint {
		return fmt.Errorf("loopback observation changed before mutation")
	}
	return nil
}

// reconcileMutation uses a fresh bounded context because cancellation cannot prove whether an operating-system effect landed.
func (a *Adapter) reconcileMutation(before Observation) (Change, error) {
	ctx, cancel := context.WithTimeout(context.Background(), postMutationObservationTimeout)
	defer cancel()
	after, err := a.Observe(ctx, before.Address)
	if err != nil {
		return Change{Attempted: true, Indeterminate: true, Before: before}, err
	}
	return Change{
		Attempted:     true,
		Changed:       before.State != after.State,
		Indeterminate: false,
		Before:        before,
		After:         after,
	}, nil
}

// changeState selects reconciled state only when the post-mutation observation completed.
func changeState(change Change) State {
	if !change.Indeterminate {
		return change.After.State
	}
	return change.Before.State
}

// changeObservation selects reconciled evidence only when the post-mutation observation completed.
func changeObservation(change Change) Observation {
	if !change.Indeterminate {
		return change.After
	}
	return change.Before
}

// normalizedContext gives platform calls a usable cancellation contract when callers omit one.
func normalizedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// validateAddress confines mutations to canonical IPv4 addresses inside 127.0.0.0/8.
func validateAddress(address netip.Addr) (netip.Addr, error) {
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return address, fmt.Errorf("address %s is not canonical IPv4 loopback", address)
	}
	return address, nil
}

// selectLoopback requires exactly one platform-verified native loopback interface.
func selectLoopback(interfaces []InterfaceFact) (InterfaceFact, error) {
	if len(interfaces) > maximumInterfaceFacts {
		return InterfaceFact{}, fmt.Errorf("interface facts exceed limit %d", maximumInterfaceFacts)
	}
	seenIndexes := make(map[int]struct{}, len(interfaces))
	candidates := make([]InterfaceFact, 0, 1)
	for _, fact := range interfaces {
		if fact.Index <= 0 || strings.TrimSpace(fact.Name) == "" || len(fact.Name) > maximumInterfaceName {
			return InterfaceFact{}, fmt.Errorf("interface fact is malformed")
		}
		if _, exists := seenIndexes[fact.Index]; exists {
			return InterfaceFact{}, fmt.Errorf("interface index %d is duplicated", fact.Index)
		}
		seenIndexes[fact.Index] = struct{}{}
		if fact.NativeLoopback {
			if !validInterfaceKind(fact.Kind) {
				return InterfaceFact{}, fmt.Errorf("native loopback kind is unsupported")
			}
			if fact.Kind == InterfaceKindWindowsSoftware && fact.WindowsLUID == 0 {
				return InterfaceFact{}, fmt.Errorf("Windows native loopback LUID is missing")
			}
			if fact.Kind != InterfaceKindWindowsSoftware && fact.WindowsLUID != 0 {
				return InterfaceFact{}, fmt.Errorf("non-Windows native loopback contains a Windows LUID")
			}
			candidates = append(candidates, fact)
		} else if fact.Kind != "" || fact.WindowsLUID != 0 {
			return InterfaceFact{}, fmt.Errorf("ordinary interface reports native loopback identity")
		}
	}
	if len(candidates) == 0 {
		return InterfaceFact{}, errLoopbackMissing
	}
	if len(candidates) != 1 {
		return InterfaceFact{}, errLoopbackAmbiguous
	}
	return candidates[0], nil
}

// validInterfaceKind confines injected facts to the platform evidence implemented by this package.
func validInterfaceKind(kind InterfaceKind) bool {
	switch kind {
	case InterfaceKindLinuxNative, InterfaceKindDarwinNative, InterfaceKindWindowsSoftware:
		return true
	default:
		return false
	}
}

var (
	errLoopbackMissing   = fmt.Errorf("native loopback interface is missing")
	errLoopbackAmbiguous = fmt.Errorf("native loopback interface is ambiguous")
)

// loopbackSelectionErrorKind distinguishes missing and ambiguous native interfaces from malformed facts.
func loopbackSelectionErrorKind(err error) ErrorKind {
	switch err {
	case errLoopbackMissing:
		return ErrorKindLoopbackMissing
	case errLoopbackAmbiguous:
		return ErrorKindLoopbackAmbiguous
	default:
		return ErrorKindInvalidFacts
	}
}

// validateAssignments binds every exact-address fact to a known observed interface.
func validateAssignments(address netip.Addr, assignments []AssignmentFact, interfaces []InterfaceFact, loopback InterfaceFact) ([]AssignmentFact, error) {
	if len(assignments) > maximumAssignmentFacts {
		return nil, fmt.Errorf("assignment facts exceed limit %d", maximumAssignmentFacts)
	}
	byIndex := make(map[int]InterfaceFact, len(interfaces))
	for _, fact := range interfaces {
		byIndex[fact.Index] = fact
	}
	validated := make([]AssignmentFact, len(assignments))
	for index, assignment := range assignments {
		if assignment.Address != address || assignment.PrefixLength < 0 || assignment.PrefixLength > 32 {
			return nil, fmt.Errorf("assignment fact is malformed")
		}
		interf, exists := byIndex[assignment.InterfaceIndex]
		if !exists {
			return nil, fmt.Errorf("assignment interface %d was not observed", assignment.InterfaceIndex)
		}
		if assignment.InterfaceName != "" && assignment.InterfaceName != interf.Name {
			return nil, fmt.Errorf("assignment interface name does not match index %d", assignment.InterfaceIndex)
		}
		assignment.InterfaceName = interf.Name
		assignment.NativeLoopback = interf.NativeLoopback
		assignment.InterfaceKind = interf.Kind
		if loopback.Kind == InterfaceKindLinuxNative && assignment.Linux == nil {
			return nil, fmt.Errorf("Linux assignment attributes are missing")
		}
		if loopback.Kind != InterfaceKindLinuxNative && assignment.Linux != nil {
			return nil, fmt.Errorf("non-Linux assignment contains Linux attributes")
		}
		if loopback.Kind == InterfaceKindWindowsSoftware && assignment.Windows == nil {
			return nil, fmt.Errorf("Windows assignment attributes are missing")
		}
		if loopback.Kind == InterfaceKindWindowsSoftware && assignment.Windows.InterfaceLUID == 0 {
			return nil, fmt.Errorf("Windows assignment LUID is missing")
		}
		if loopback.Kind == InterfaceKindWindowsSoftware && assignment.InterfaceIndex == loopback.Index && assignment.Windows.InterfaceLUID != loopback.WindowsLUID {
			return nil, fmt.Errorf("Windows assignment LUID does not match the selected loopback")
		}
		if loopback.Kind != InterfaceKindWindowsSoftware && assignment.Windows != nil {
			return nil, fmt.Errorf("non-Windows assignment contains Windows attributes")
		}
		validated[index] = assignment
	}
	sort.Slice(validated, func(left, right int) bool {
		if validated[left].InterfaceIndex != validated[right].InterfaceIndex {
			return validated[left].InterfaceIndex < validated[right].InterfaceIndex
		}
		return validated[left].PrefixLength < validated[right].PrefixLength
	})
	return validated, nil
}

// classify converts exact host placement facts into the only states mutation policy accepts.
func classify(loopback InterfaceFact, assignments []AssignmentFact) State {
	if len(assignments) == 0 {
		return StateAbsent
	}
	if len(assignments) != 1 {
		return StateAmbiguous
	}
	assignment := assignments[0]
	if assignment.InterfaceIndex != loopback.Index {
		return StateForeign
	}
	if assignment.PrefixLength != 32 {
		return StateNonHostPrefix
	}
	if loopback.Kind == InterfaceKindLinuxNative && !exactLinuxAttributes(assignment.Linux, loopback.Name) {
		return StateAttributeConflict
	}
	if loopback.Kind == InterfaceKindWindowsSoftware && !exactWindowsAttributes(assignment.Windows) {
		return StateAttributeConflict
	}
	return StateExact
}

const linuxPermanentAddressFlag uint32 = 1 << 7

// exactLinuxAttributes proves the address has the same host-only, permanent shape Harbor creates.
func exactLinuxAttributes(fact *LinuxAssignmentFact, interfaceName string) bool {
	return fact != nil &&
		fact.Scope == LinuxAddressScopeHost &&
		fact.Flags == linuxPermanentAddressFlag &&
		fact.Label == interfaceName &&
		fact.AddressMatchesLocal &&
		fact.CacheInfoPresent &&
		fact.ValidLifetimeSeconds == ^uint32(0) &&
		fact.PreferredLifetimeSeconds == ^uint32(0) &&
		fact.AdditionalAttributesSHA256 == ""
}

// exactWindowsAttributes proves the address is usable as well as configured with Harbor's exact active shape.
func exactWindowsAttributes(fact *WindowsAssignmentFact) bool {
	return fact != nil &&
		fact.SkipAsSource &&
		fact.PrefixOrigin == AddressOriginManual &&
		fact.SuffixOrigin == AddressOriginManual &&
		fact.ValidLifetimeSeconds == ^uint32(0) &&
		fact.PreferredLifetimeSeconds == ^uint32(0) &&
		fact.DADState == AddressStatePreferred
}
