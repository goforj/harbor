//go:build windows

package loopback

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/sys/windows"
)

// windowsAddressResult supplies one deterministic GetUnicastIpAddressEntry result.
type windowsAddressResult struct {
	row windows.MibUnicastIpAddressRow
	err error
}

// fakeWindowsIPHelper records every identity-bound IP Helper operation without mutating the test host.
type fakeWindowsIPHelper struct {
	interfaceFacts   []windows.MibIfRow2
	assignmentFacts  []windows.MibUnicastIpAddressRow
	interfaceLUID    windows.MibIfRow2
	interfaceIndex   windows.MibIfRow2
	interfaceLUIDErr error
	interfaceIdxErr  error
	addressResults   []windowsAddressResult
	createErr        error
	initializeErr    error
	deleteErrs       []error
	addressCalls     int
	createCalls      int
	deleteCalls      int
	initializeCalls  int
	wantLUID         uint64
	wantIndex        uint32
	created          windows.MibUnicastIpAddressRow
	deleted          windows.MibUnicastIpAddressRow
}

// interfaceRows returns the injected IP Helper interface table.
func (helper *fakeWindowsIPHelper) interfaceRows() ([]windows.MibIfRow2, error) {
	return append([]windows.MibIfRow2(nil), helper.interfaceFacts...), nil
}

// addressRows returns the injected IP Helper unicast table.
func (helper *fakeWindowsIPHelper) addressRows() ([]windows.MibUnicastIpAddressRow, error) {
	return append([]windows.MibUnicastIpAddressRow(nil), helper.assignmentFacts...), nil
}

// interfaceByLUID records the stable identity used for pre-mutation revalidation.
func (helper *fakeWindowsIPHelper) interfaceByLUID(luid uint64) (windows.MibIfRow2, error) {
	helper.wantLUID = luid
	return helper.interfaceLUID, helper.interfaceLUIDErr
}

// interfaceByIndex records the reusable identity used for pre-mutation revalidation.
func (helper *fakeWindowsIPHelper) interfaceByIndex(index uint32) (windows.MibIfRow2, error) {
	helper.wantIndex = index
	return helper.interfaceIndex, helper.interfaceIdxErr
}

// initializeAddress records that production follows IP Helper's documented initialization contract.
func (helper *fakeWindowsIPHelper) initializeAddress(*windows.MibUnicastIpAddressRow) error {
	helper.initializeCalls++
	return helper.initializeErr
}

// address returns the next observation and repeats the last one when reconciliation exhausts its bound.
func (helper *fakeWindowsIPHelper) address(windows.MibUnicastIpAddressRow) (windows.MibUnicastIpAddressRow, error) {
	helper.addressCalls++
	if len(helper.addressResults) == 0 {
		return windows.MibUnicastIpAddressRow{}, windows.ERROR_NOT_FOUND
	}
	index := helper.addressCalls - 1
	if index >= len(helper.addressResults) {
		index = len(helper.addressResults) - 1
	}
	result := helper.addressResults[index]
	return result.row, result.err
}

// createAddress records the exact row passed to CreateUnicastIpAddressEntry.
func (helper *fakeWindowsIPHelper) createAddress(row *windows.MibUnicastIpAddressRow) error {
	helper.createCalls++
	helper.created = *row
	return helper.createErr
}

// deleteAddress records the exact row passed to DeleteUnicastIpAddressEntry.
func (helper *fakeWindowsIPHelper) deleteAddress(row *windows.MibUnicastIpAddressRow) error {
	helper.deleteCalls++
	helper.deleted = *row
	if len(helper.deleteErrs) == 0 {
		return nil
	}
	index := helper.deleteCalls - 1
	if index >= len(helper.deleteErrs) {
		index = len(helper.deleteErrs) - 1
	}
	return helper.deleteErrs[index]
}

// TestWindowsAddressRowBindsStableInterfaceAndExactAttributes verifies the CreateUnicastIpAddressEntry input.
func TestWindowsAddressRowBindsStableInterfaceAndExactAttributes(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	prefix := netip.PrefixFrom(testAddress, 32)
	row, err := backend.windowsAddressRow(interf, prefix)
	if err != nil {
		t.Fatalf("windowsAddressRow() error = %v", err)
	}
	address, ok := windowsIPv4Address(&row.Address)
	if !ok || address != testAddress || row.InterfaceLuid != interf.WindowsLUID || row.InterfaceIndex != uint32(interf.Index) || row.OnLinkPrefixLength != 32 {
		t.Fatalf("windowsAddressRow() identity = %s/%d on %d/%d", address, row.OnLinkPrefixLength, row.InterfaceLuid, row.InterfaceIndex)
	}
	if row.SkipAsSource == 0 || row.PrefixOrigin != windows.IpPrefixOriginManual || row.SuffixOrigin != windows.IpSuffixOriginManual || row.DadState != windows.IpDadStateTentative {
		t.Fatalf("windowsAddressRow() attributes = skip %d, prefix %d, suffix %d, DAD %d", row.SkipAsSource, row.PrefixOrigin, row.SuffixOrigin, row.DadState)
	}
	if row.ValidLifetime != ^uint32(0) || row.PreferredLifetime != ^uint32(0) || helper.initializeCalls != 1 {
		t.Fatalf("windowsAddressRow() lifetimes = valid %d, preferred %d; initialize calls = %d", row.ValidLifetime, row.PreferredLifetime, helper.initializeCalls)
	}
}

// TestWindowsBackendCarriesLoopbackLUIDIntoObservation verifies the stable identity reaches signed public facts.
func TestWindowsBackendCarriesLoopbackLUIDIntoObservation(t *testing.T) {
	interf := windowsTestInterface()
	native := windowsTestInterfaceRow(interf)
	ordinary := windows.MibIfRow2{InterfaceLuid: 2002, InterfaceIndex: 2}
	copy(ordinary.Alias[:], windows.StringToUTF16("Ethernet"))
	backend, helper := newWindowsTestBackend(interf)
	helper.interfaceFacts = []windows.MibIfRow2{native, ordinary}

	facts, err := backend.interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces() error = %v", err)
	}
	if len(facts) != 2 || facts[0] != interf {
		t.Fatalf("interfaces() = %#v", facts)
	}
	if facts[1].WindowsLUID != 0 || facts[1].NativeLoopback || facts[1].Kind != "" {
		t.Fatalf("ordinary interface leaked native identity = %#v", facts[1])
	}
}

// TestWindowsLoopbackCandidateRequiresAllNativeFacts keeps down or non-loopback rows out of mutation authority.
func TestWindowsLoopbackCandidateRequiresAllNativeFacts(t *testing.T) {
	interf := windowsTestInterface()
	reference := windowsTestInterfaceRow(interf)
	tests := []struct {
		name   string
		mutate func(*windows.MibIfRow2)
	}{
		{name: "zero LUID", mutate: func(row *windows.MibIfRow2) { row.InterfaceLuid = 0 }},
		{name: "zero index", mutate: func(row *windows.MibIfRow2) { row.InterfaceIndex = 0 }},
		{name: "wrong type", mutate: func(row *windows.MibIfRow2) { row.Type = windows.IF_TYPE_ETHERNET_CSMACD }},
		{name: "wrong access", mutate: func(row *windows.MibIfRow2) { row.AccessType = 2 }},
		{name: "down", mutate: func(row *windows.MibIfRow2) { row.OperStatus = windows.IfOperStatusDown }},
	}
	if !exactWindowsLoopbackCandidate(reference) {
		t.Fatal("exactWindowsLoopbackCandidate() rejected the reference loopback")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := reference
			test.mutate(&row)
			if exactWindowsLoopbackCandidate(row) {
				t.Fatal("exactWindowsLoopbackCandidate() accepted incomplete evidence")
			}
		})
	}
}

// TestWindowsAssignmentFactsRetainDADState verifies readiness evidence is not filtered or normalized away.
func TestWindowsAssignmentFactsRetainDADState(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	row, err := backend.windowsAddressRow(interf, netip.PrefixFrom(testAddress, 32))
	if err != nil {
		t.Fatalf("windowsAddressRow() error = %v", err)
	}
	row.DadState = windows.IpDadStateTentative
	helper.assignmentFacts = []windows.MibUnicastIpAddressRow{row}

	facts, err := backend.assignments(context.Background(), testAddress)
	if err != nil {
		t.Fatalf("assignments() error = %v", err)
	}
	if len(facts) != 1 || facts[0].Windows == nil || facts[0].Windows.InterfaceLUID != interf.WindowsLUID || facts[0].Windows.DADState != AddressStateTentative {
		t.Fatalf("assignments() = %#v", facts)
	}
}

// TestWindowsEnsureWaitsForPreferredDAD verifies tentative presence cannot complete an Ensure operation.
func TestWindowsEnsureWaitsForPreferredDAD(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	tentative, err := backend.windowsAddressRow(interf, netip.PrefixFrom(testAddress, 32))
	if err != nil {
		t.Fatalf("windowsAddressRow() error = %v", err)
	}
	tentative.DadState = windows.IpDadStateTentative
	preferred := tentative
	preferred.DadState = windows.IpDadStatePreferred
	helper.addressResults = []windowsAddressResult{{row: tentative}, {row: preferred}}

	if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err != nil {
		t.Fatalf("ensure() error = %v", err)
	}
	if helper.createCalls != 1 || helper.deleteCalls != 0 || helper.addressCalls != 2 {
		t.Fatalf("ensure() calls = create %d, delete %d, address %d", helper.createCalls, helper.deleteCalls, helper.addressCalls)
	}
	assertWindowsMutationIdentity(t, helper.created, interf)
}

// TestWindowsEnsureRollsBackUnreadyCreate verifies only this operation's confirmed create is removed on readiness failure.
func TestWindowsEnsureRollsBackUnreadyCreate(t *testing.T) {
	interf := windowsTestInterface()
	tests := []struct {
		name      string
		result    windowsAddressResult
		candidate windowsAddressResult
	}{
		{
			name:      "duplicate DAD",
			result:    windowsAddressResult{row: windowsTestAddressResult(interf, windows.IpDadStateDuplicate)},
			candidate: windowsAddressResult{row: windowsTestAddressResult(interf, windows.IpDadStateDuplicate)},
		},
		{
			name:      "lookup failure",
			result:    windowsAddressResult{err: windows.ERROR_ACCESS_DENIED},
			candidate: windowsAddressResult{row: windowsTestAddressResult(interf, windows.IpDadStateTentative)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend, helper := newWindowsTestBackend(interf)
			helper.addressResults = []windowsAddressResult{test.result, test.candidate, {err: windows.ERROR_NOT_FOUND}}
			if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
				t.Fatal("ensure() error = nil")
			}
			if helper.createCalls != 1 || helper.deleteCalls != 1 {
				t.Fatalf("ensure() calls = create %d, delete %d", helper.createCalls, helper.deleteCalls)
			}
			assertWindowsMutationIdentity(t, helper.deleted, interf)
		})
	}
}

// TestWindowsEnsureRefusesToDeleteChangedRollbackCandidate prevents a raced foreign replacement from being removed.
func TestWindowsEnsureRefusesToDeleteChangedRollbackCandidate(t *testing.T) {
	interf := windowsTestInterface()
	tests := []struct {
		name   string
		mutate func(*windows.MibUnicastIpAddressRow)
	}{
		{name: "LUID", mutate: func(row *windows.MibUnicastIpAddressRow) { row.InterfaceLuid++ }},
		{name: "index", mutate: func(row *windows.MibUnicastIpAddressRow) { row.InterfaceIndex++ }},
		{name: "prefix", mutate: func(row *windows.MibUnicastIpAddressRow) { row.OnLinkPrefixLength = 31 }},
		{name: "attributes", mutate: func(row *windows.MibUnicastIpAddressRow) { row.SkipAsSource = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend, helper := newWindowsTestBackend(interf)
			conflict := windowsTestAddressResult(interf, windows.IpDadStatePreferred)
			test.mutate(&conflict)
			helper.addressResults = []windowsAddressResult{{row: conflict}, {row: conflict}}
			if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
				t.Fatal("ensure() error = nil")
			}
			if helper.createCalls != 1 || helper.deleteCalls != 0 || helper.addressCalls != 2 {
				t.Fatalf("conflicting rollback calls = create %d, delete %d, address %d", helper.createCalls, helper.deleteCalls, helper.addressCalls)
			}
		})
	}
}

// TestWindowsEnsureBoundsTentativeDADAndVerifiesRollback covers the asynchronous timeout path without wall-clock sleeps.
func TestWindowsEnsureBoundsTentativeDADAndVerifiesRollback(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	backend.reconciliationAttempts = 2
	helper.addressResults = []windowsAddressResult{
		{row: windowsTestAddressResult(interf, windows.IpDadStateTentative)},
		{row: windowsTestAddressResult(interf, windows.IpDadStateTentative)},
		{row: windowsTestAddressResult(interf, windows.IpDadStateTentative)},
		{err: windows.ERROR_NOT_FOUND},
	}
	if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("ensure() error = nil")
	}
	if helper.addressCalls != 4 || helper.deleteCalls != 1 {
		t.Fatalf("bounded ensure calls = address %d, delete %d", helper.addressCalls, helper.deleteCalls)
	}
}

// TestWindowsEnsureNeverRollsBackRejectedCreate prevents a racing foreign owner from being deleted.
func TestWindowsEnsureNeverRollsBackRejectedCreate(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	helper.createErr = windows.ERROR_OBJECT_ALREADY_EXISTS
	if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
		t.Fatalf("ensure() error = %v, want ERROR_OBJECT_ALREADY_EXISTS", err)
	}
	if helper.deleteCalls != 0 || helper.addressCalls != 0 {
		t.Fatalf("rejected create cleanup = delete %d, address %d", helper.deleteCalls, helper.addressCalls)
	}
}

// TestWindowsEnsureJoinsRollbackFailure preserves both the readiness cause and cleanup uncertainty.
func TestWindowsEnsureJoinsRollbackFailure(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	helper.addressResults = []windowsAddressResult{
		{err: windows.ERROR_ACCESS_DENIED},
		{row: windowsTestAddressResult(interf, windows.IpDadStateTentative)},
	}
	helper.deleteErrs = []error{windows.ERROR_PRIVILEGE_NOT_HELD}
	err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32))
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Fatalf("ensure() error = %v, want readiness and rollback causes", err)
	}
}

// TestWindowsReleaseWaitsForAbsence verifies successful Delete does not complete while the row remains visible.
func TestWindowsReleaseWaitsForAbsence(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	copy(helper.interfaceLUID.Alias[:], windows.StringToUTF16("renamed by user"))
	copy(helper.interfaceIndex.Alias[:], windows.StringToUTF16("another display name"))
	present := windowsTestAddressResult(interf, windows.IpDadStatePreferred)
	helper.addressResults = []windowsAddressResult{{row: present}, {err: windows.ERROR_NOT_FOUND}}
	if err := backend.release(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if helper.deleteCalls != 1 || helper.addressCalls != 2 || helper.createCalls != 0 {
		t.Fatalf("release() calls = delete %d, address %d, create %d", helper.deleteCalls, helper.addressCalls, helper.createCalls)
	}
}

// TestWindowsReleaseDoesNotRecreateAfterVerificationFailure keeps uncertain disappearance visible to outer reconciliation.
func TestWindowsReleaseDoesNotRecreateAfterVerificationFailure(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	backend.reconciliationAttempts = 2
	present := windowsTestAddressResult(interf, windows.IpDadStatePreferred)
	helper.addressResults = []windowsAddressResult{{row: present}}
	if err := backend.release(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("release() error = nil")
	}
	if helper.createCalls != 0 || helper.deleteCalls != 1 || helper.addressCalls != 2 {
		t.Fatalf("release() calls = create %d, delete %d, address %d", helper.createCalls, helper.deleteCalls, helper.addressCalls)
	}
}

// TestWindowsReleaseRejectsUnexpectedLookupIdentity treats an impossible key mismatch as unsafe host evidence.
func TestWindowsReleaseRejectsUnexpectedLookupIdentity(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	present := windowsTestAddressResult(interf, windows.IpDadStatePreferred)
	present.InterfaceLuid++
	helper.addressResults = []windowsAddressResult{{row: present}}
	if err := backend.release(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
		t.Fatal("release() error = nil")
	}
	if helper.deleteCalls != 1 || helper.addressCalls != 1 || helper.createCalls != 0 {
		t.Fatalf("release() calls = delete %d, address %d, create %d", helper.deleteCalls, helper.addressCalls, helper.createCalls)
	}
}

// TestWindowsMutationRevalidatesLUIDAndIndex rejects every form of interface identity drift before an effect.
func TestWindowsMutationRevalidatesLUIDAndIndex(t *testing.T) {
	interf := windowsTestInterface()
	tests := []struct {
		name   string
		mutate func(*fakeWindowsIPHelper)
	}{
		{name: "LUID lookup failed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceLUIDErr = windows.ERROR_NOT_FOUND }},
		{name: "index lookup failed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceIdxErr = windows.ERROR_NOT_FOUND }},
		{name: "LUID changed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceIndex.InterfaceLuid++ }},
		{name: "index changed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceLUID.InterfaceIndex++ }},
		{name: "type changed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceIndex.Type = windows.IF_TYPE_ETHERNET_CSMACD }},
		{name: "access changed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceIndex.AccessType = 2 }},
		{name: "operational state changed", mutate: func(helper *fakeWindowsIPHelper) { helper.interfaceIndex.OperStatus = windows.IfOperStatusDown }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend, helper := newWindowsTestBackend(interf)
			test.mutate(helper)
			if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err == nil {
				t.Fatal("ensure() error = nil")
			}
			if helper.createCalls != 0 || helper.deleteCalls != 0 {
				t.Fatalf("identity drift mutated host = create %d, delete %d", helper.createCalls, helper.deleteCalls)
			}
		})
	}

	backend, helper := newWindowsTestBackend(interf)
	preferred := windowsTestAddressResult(interf, windows.IpDadStatePreferred)
	helper.addressResults = []windowsAddressResult{{row: preferred}}
	if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); err != nil {
		t.Fatalf("ensure() with stable identity error = %v", err)
	}
	if helper.wantLUID != interf.WindowsLUID || helper.wantIndex != uint32(interf.Index) {
		t.Fatalf("revalidation identity = %d/%d", helper.wantLUID, helper.wantIndex)
	}
}

// TestWindowsBackendHonorsCancellationBeforeMutation proves canceled requests cannot enter IP Helper effects.
func TestWindowsBackendHonorsCancellationBeforeMutation(t *testing.T) {
	interf := windowsTestInterface()
	backend, helper := newWindowsTestBackend(interf)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prefix := netip.PrefixFrom(testAddress, 32)
	if err := backend.ensure(ctx, interf, prefix); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure() error = %v, want context.Canceled", err)
	}
	if err := backend.release(ctx, interf, prefix); !errors.Is(err, context.Canceled) {
		t.Fatalf("release() error = %v, want context.Canceled", err)
	}
	if helper.createCalls != 0 || helper.deleteCalls != 0 {
		t.Fatalf("canceled mutations = create %d, delete %d", helper.createCalls, helper.deleteCalls)
	}
}

// TestWindowsStatusErrorPreservesErrno verifies raw IP Helper statuses remain inspectable with errors.Is.
func TestWindowsStatusErrorPreservesErrno(t *testing.T) {
	if err := windowsStatusError(0); err != nil {
		t.Fatalf("windowsStatusError(0) = %v", err)
	}
	if err := windowsStatusError(uintptr(windows.ERROR_ACCESS_DENIED)); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("windowsStatusError(ERROR_ACCESS_DENIED) = %v", err)
	}
}

// TestWindowsProcedureResolutionFailurePreventsEffects keeps missing DLL symbols from resembling successful calls.
func TestWindowsProcedureResolutionFailurePreventsEffects(t *testing.T) {
	cause := windows.ERROR_PROC_NOT_FOUND
	helper := nativeWindowsIPHelper{procedureErr: cause}
	var row windows.MibUnicastIpAddressRow
	if err := helper.initializeAddress(&row); !errors.Is(err, cause) {
		t.Fatalf("initializeAddress() error = %v, want %v", err, cause)
	}
	if err := helper.createAddress(&row); !errors.Is(err, cause) {
		t.Fatalf("createAddress() error = %v, want %v", err, cause)
	}
	if err := helper.deleteAddress(&row); !errors.Is(err, cause) {
		t.Fatalf("deleteAddress() error = %v, want %v", err, cause)
	}

	interf := windowsTestInterface()
	backend, fake := newWindowsTestBackend(interf)
	fake.initializeErr = cause
	if err := backend.ensure(context.Background(), interf, netip.PrefixFrom(testAddress, 32)); !errors.Is(err, cause) {
		t.Fatalf("ensure() error = %v, want %v", err, cause)
	}
	if fake.createCalls != 0 || fake.deleteCalls != 0 {
		t.Fatalf("initialization failure effects = create %d, delete %d", fake.createCalls, fake.deleteCalls)
	}
}

// TestWindowsReconciliationWaitHonorsCancellation prevents a poll interval from hiding shutdown.
func TestWindowsReconciliationWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForWindowsReconciliation(ctx, windowsReconciliationInterval); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForWindowsReconciliation() error = %v, want context.Canceled", err)
	}
}

// TestWindowsBackendObservesWithoutMutation exercises both IP Helper fact tables on Windows CI.
func TestWindowsBackendObservesWithoutMutation(t *testing.T) {
	adapter := New()
	observation, err := adapter.Observe(context.Background(), netip.MustParseAddr("127.254.254.254"))
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.State != StateAbsent {
		t.Skipf("host already assigns the reserved test identity: %+v", observation)
	}
}

// TestWindowsOriginsRemainBounded covers known and unknown IP Helper origin values.
func TestWindowsOriginsRemainBounded(t *testing.T) {
	prefixes := map[uint32]AddressOrigin{
		windows.IpPrefixOriginOther:               AddressOriginOther,
		windows.IpPrefixOriginManual:              AddressOriginManual,
		windows.IpPrefixOriginWellKnown:           AddressOriginWellKnown,
		windows.IpPrefixOriginDhcp:                AddressOriginDHCP,
		windows.IpPrefixOriginRouterAdvertisement: AddressOriginRouterAdvertisement,
		windows.IpPrefixOriginUnchanged:           AddressOriginUnchanged,
		99:                                        AddressOriginUnknown,
	}
	for input, want := range prefixes {
		if got := windowsPrefixOrigin(input); got != want {
			t.Fatalf("windowsPrefixOrigin(%d) = %q, want %q", input, got, want)
		}
	}
	suffixes := map[uint32]AddressOrigin{
		windows.IpSuffixOriginOther:            AddressOriginOther,
		windows.IpSuffixOriginManual:           AddressOriginManual,
		windows.IpSuffixOriginWellKnown:        AddressOriginWellKnown,
		windows.IpSuffixOriginDhcp:             AddressOriginDHCP,
		windows.IpSuffixOriginLinkLayerAddress: AddressOriginLinkLayer,
		windows.IpSuffixOriginRandom:           AddressOriginRandom,
		windows.IpSuffixOriginUnchanged:        AddressOriginUnchanged,
		99:                                     AddressOriginUnknown,
	}
	for input, want := range suffixes {
		if got := windowsSuffixOrigin(input); got != want {
			t.Fatalf("windowsSuffixOrigin(%d) = %q, want %q", input, got, want)
		}
	}
	states := map[uint32]AddressState{
		windows.IpDadStateInvalid:    AddressStateInvalid,
		windows.IpDadStateTentative:  AddressStateTentative,
		windows.IpDadStateDuplicate:  AddressStateDuplicate,
		windows.IpDadStateDeprecated: AddressStateDeprecated,
		windows.IpDadStatePreferred:  AddressStatePreferred,
		99:                           AddressStateUnknown,
	}
	for input, want := range states {
		if got := windowsDADState(input); got != want {
			t.Fatalf("windowsDADState(%d) = %q, want %q", input, got, want)
		}
	}
}

// newWindowsTestBackend returns a backend whose reconciliation advances without wall-clock delay.
func newWindowsTestBackend(interf InterfaceFact) (*platformBackend, *fakeWindowsIPHelper) {
	row := windowsTestInterfaceRow(interf)
	helper := &fakeWindowsIPHelper{interfaceLUID: row, interfaceIndex: row}
	return &platformBackend{ipHelper: helper, reconciliationAttempts: 3}, helper
}

// windowsTestInterface returns one stable Windows software-loopback identity.
func windowsTestInterface() InterfaceFact {
	return InterfaceFact{
		Name:           "Loopback Pseudo-Interface 1",
		Index:          12,
		Kind:           InterfaceKindWindowsSoftware,
		NativeLoopback: true,
		WindowsLUID:    1001,
	}
}

// windowsTestInterfaceRow converts the public identity into one coherent IP Helper row.
func windowsTestInterfaceRow(interf InterfaceFact) windows.MibIfRow2 {
	row := windows.MibIfRow2{
		InterfaceLuid:  interf.WindowsLUID,
		InterfaceIndex: uint32(interf.Index),
		Type:           windows.IF_TYPE_SOFTWARE_LOOPBACK,
		AccessType:     windowsInterfaceAccessLoopback,
		OperStatus:     windows.IfOperStatusUp,
	}
	copy(row.Alias[:], windows.StringToUTF16(interf.Name))
	return row
}

// windowsTestAddressResult returns Harbor's exact IP Helper row with the requested DAD state.
func windowsTestAddressResult(interf InterfaceFact, dadState uint32) windows.MibUnicastIpAddressRow {
	var row windows.MibUnicastIpAddressRow
	configureWindowsAddressRow(&row, interf, netip.PrefixFrom(testAddress, 32))
	row.DadState = dadState
	return row
}

// assertWindowsMutationIdentity verifies an effect stayed bound to the signed interface pair and requested address.
func assertWindowsMutationIdentity(t *testing.T, row windows.MibUnicastIpAddressRow, interf InterfaceFact) {
	t.Helper()
	address, ok := windowsIPv4Address(&row.Address)
	if !ok || address != testAddress || row.InterfaceLuid != interf.WindowsLUID || row.InterfaceIndex != uint32(interf.Index) {
		t.Fatalf("mutation identity = %s on %d/%d", address, row.InterfaceLuid, row.InterfaceIndex)
	}
}
