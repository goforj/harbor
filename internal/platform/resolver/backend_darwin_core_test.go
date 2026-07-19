package resolver

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const darwinTestGeneration = uint32(7)

// fakeDarwinResolverStore records fixed-destination effects and maintains an in-memory directory snapshot.
type fakeDarwinResolverStore struct {
	entries                []darwinResolverEntry
	snapshotErr            error
	replaceErr             error
	removeErr              error
	afterSnapshot          func()
	afterReplaceValidation func()
	afterRemoveValidation  func()
	replaceGuards          []darwinResolverGuard
	replaceBytes           [][]byte
	removeGuards           []darwinResolverGuard
	nextInode              uint64
}

// snapshot returns independent entry bytes and optionally changes test state after the read.
func (store *fakeDarwinResolverStore) snapshot(context.Context) ([]darwinResolverEntry, error) {
	if store.snapshotErr != nil {
		return nil, store.snapshotErr
	}
	entries := cloneDarwinTestEntries(store.entries)
	if store.afterSnapshot != nil {
		store.afterSnapshot()
	}
	return entries, nil
}

// replace enforces the supplied guard before replacing only the fixed test entry.
func (store *fakeDarwinResolverStore) replace(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard darwinResolverGuard,
	content []byte,
) error {
	store.replaceGuards = append(store.replaceGuards, guard)
	store.replaceBytes = append(store.replaceBytes, slices.Clone(content))
	if store.replaceErr != nil {
		return store.replaceErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := matchDarwinResolverMutationState(ctx, request, store.entries, expectedFingerprint, guard); err != nil {
		return err
	}
	if store.afterReplaceValidation != nil {
		store.afterReplaceValidation()
	}
	if err := matchDarwinResolverMutationState(ctx, request, store.entries, expectedFingerprint, guard); err != nil {
		return err
	}
	store.nextInode++
	if store.nextInode < 1000 {
		store.nextInode = 1000
	}
	replacement := darwinTestEntry(fixedDarwinResolverName, content, store.nextInode)
	for index, entry := range store.entries {
		if entry.Name == fixedDarwinResolverName {
			store.entries[index] = replacement
			return nil
		}
	}
	store.entries = append(store.entries, replacement)
	return nil
}

// remove enforces the supplied guard before removing only the fixed test entry.
func (store *fakeDarwinResolverStore) remove(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard darwinResolverGuard,
) error {
	store.removeGuards = append(store.removeGuards, guard)
	if store.removeErr != nil {
		return store.removeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := matchDarwinResolverMutationState(ctx, request, store.entries, expectedFingerprint, guard); err != nil {
		return err
	}
	if store.afterRemoveValidation != nil {
		store.afterRemoveValidation()
	}
	if err := matchDarwinResolverMutationState(ctx, request, store.entries, expectedFingerprint, guard); err != nil {
		return err
	}
	for index, entry := range store.entries {
		if entry.Name == fixedDarwinResolverName {
			store.entries = append(store.entries[:index], store.entries[index+1:]...)
			return nil
		}
	}
	return errors.New("fixed Darwin resolver entry is absent")
}

// TestDarwinResolverCodecRoundTripsCanonicalOwnership pins the exact emitted resolver(5) bytes.
func TestDarwinResolverCodecRoundTripsCanonicalOwnership(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	content, err := marshalDarwinResolver(request)
	if err != nil {
		t.Fatalf("marshalDarwinResolver() error = %v", err)
	}
	want := fmt.Sprintf(
		"# harbor-resolver-owner version=1 installation=installation-test policy=%s\n"+
			"domain test\n"+
			"nameserver 127.0.0.1\n"+
			"port 25000\n",
		request.PolicyFingerprint(),
	)
	if string(content) != want {
		t.Fatalf("marshalDarwinResolver() = %q, want %q", content, want)
	}
	parsed, err := parseDarwinResolver(fixedDarwinResolverName, content)
	if err != nil {
		t.Fatalf("parseDarwinResolver() error = %v", err)
	}
	if parsed.Namespace != request.Suffix() || !slices.Equal(parsed.Servers, []netip.AddrPort{request.Endpoint()}) || parsed.Owner == nil || *parsed.Owner != request.OwnerMarker() {
		t.Fatalf("parseDarwinResolver() = %#v, want request namespace, endpoint, and owner", parsed)
	}

	entry := darwinTestEntry(fixedDarwinResolverName, content, 10)
	fact, relevant, err := darwinResolverRuleFact(entry, request)
	if err != nil {
		t.Fatalf("darwinResolverRuleFact() error = %v", err)
	}
	if !relevant || !fact.NativeExact || fact.Owner == nil || fact.NativeID != "test@0000000000000001:000000000000000a:00000007" {
		t.Fatalf("darwinResolverRuleFact() = %#v, %t, want exact owned fact", fact, relevant)
	}
	name, device, inode, generation, err := parseDarwinResolverNativeID(fact.NativeID)
	if err != nil || name != fixedDarwinResolverName || device != 1 || inode != 10 || generation != darwinTestGeneration {
		t.Fatalf("parseDarwinResolverNativeID() = %q, %d, %d, %d, %v", name, device, inode, generation, err)
	}
}

// TestDarwinResolverParserAcceptsBoundedForeignGrammar covers every admitted noncanonical directive.
func TestDarwinResolverParserAcceptsBoundedForeignGrammar(t *testing.T) {
	content := []byte(strings.Join([]string{
		"; ordinary comment",
		"domain child.test # claim override",
		"nameserver 192.0.2.1",
		"nameserver 2001:db8::1",
		"port 5353",
		"search child.test other.example",
		"search_order 0",
		"timeout 5",
		"options rotate",
		"sortlist 192.0.2.0/24",
		"lookup file bind",
		"",
	}, "\n"))
	parsed, err := parseDarwinResolver("fallback.example", content)
	if err != nil {
		t.Fatalf("parseDarwinResolver() error = %v", err)
	}
	wantServers := []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.1:5353"),
		netip.MustParseAddrPort("[2001:db8::1]:5353"),
	}
	if parsed.Namespace != ".child.test" || !slices.Equal(parsed.Servers, wantServers) || parsed.Owner != nil {
		t.Fatalf("parseDarwinResolver() = %#v, want bounded foreign grammar", parsed)
	}
	fallback, err := parseDarwinResolver("other.example", []byte("nameserver 192.0.2.2\n"))
	if err != nil {
		t.Fatalf("parseDarwinResolver() fallback error = %v", err)
	}
	if fallback.Namespace != ".other.example" || fallback.Servers[0] != netip.MustParseAddrPort("192.0.2.2:53") {
		t.Fatalf("parseDarwinResolver() fallback = %#v", fallback)
	}
}

// TestDarwinResolverParserRejectsMalformedOrUnboundedFacts covers fail-closed codec boundaries.
func TestDarwinResolverParserRejectsMalformedOrUnboundedFacts(t *testing.T) {
	validMarker := fmt.Sprintf(
		"%sversion=1 installation=installation-test policy=%s",
		darwinResolverOwnerPrefix,
		testAuthorityFingerprint,
	)
	tooManyNameservers := strings.Builder{}
	for index := 1; index <= maximumServersPerRule+1; index++ {
		fmt.Fprintf(&tooManyNameservers, "nameserver 192.0.2.%d\n", index)
	}
	tests := []struct {
		name     string
		filename string
		content  []byte
	}{
		{name: "invalid filename", filename: ".test"},
		{name: "large file", filename: "test", content: bytesOfLength(maximumDarwinResolverFileBytes + 1)},
		{name: "invalid UTF-8", filename: "test", content: []byte{0xff}},
		{name: "control character", filename: "test", content: []byte("domain test\r\n")},
		{name: "non-ASCII text", filename: "test", content: []byte("# café\n")},
		{name: "too many lines", filename: "test", content: []byte(strings.Repeat("\n", maximumDarwinResolverLines+1))},
		{name: "long line", filename: "test", content: []byte(strings.Repeat("x", maximumDarwinResolverLineBytes+1))},
		{name: "duplicate owner", filename: "test", content: []byte(validMarker + "\n" + validMarker + "\n")},
		{name: "reserved owner typo", filename: "test", content: []byte("# harbor-resolver-owner-bad\n")},
		{name: "owner field count", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=1 installation=x\n")},
		{name: "owner field syntax", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=1 installation policy=x\n")},
		{name: "owner duplicate field", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=1 version=2 policy=x\n")},
		{name: "owner missing field", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=1 owner=x policy=" + testAuthorityFingerprint + "\n")},
		{name: "owner version", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=01 installation=x policy=" + testAuthorityFingerprint + "\n")},
		{name: "owner value", filename: "test", content: []byte(darwinResolverOwnerPrefix + "version=1 installation=bad/owner policy=" + testAuthorityFingerprint + "\n")},
		{name: "too many arguments", filename: "test", content: []byte("options" + strings.Repeat(" x", maximumDarwinResolverDirectiveArgs+1) + "\n")},
		{name: "uppercase directive", filename: "test", content: []byte("Domain test\n")},
		{name: "domain arity", filename: "test", content: []byte("domain\n")},
		{name: "domain value", filename: "test", content: []byte("domain .test\n")},
		{name: "duplicate domain", filename: "test", content: []byte("domain test\ndomain child.test\n")},
		{name: "nameserver arity", filename: "test", content: []byte("nameserver\n")},
		{name: "too many nameservers", filename: "test", content: []byte(tooManyNameservers.String())},
		{name: "invalid nameserver", filename: "test", content: []byte("nameserver localhost\n")},
		{name: "mapped nameserver", filename: "test", content: []byte("nameserver ::ffff:127.0.0.1\n")},
		{name: "long nameserver zone", filename: "test", content: []byte("nameserver fe80::1%" + strings.Repeat("z", maximumAddressZoneLength+1) + "\n")},
		{name: "port arity", filename: "test", content: []byte("port\n")},
		{name: "port zero", filename: "test", content: []byte("port 0\n")},
		{name: "port spelling", filename: "test", content: []byte("port 053\n")},
		{name: "duplicate port", filename: "test", content: []byte("port 53\nport 54\n")},
		{name: "search arity", filename: "test", content: []byte("search\n")},
		{name: "search value", filename: "test", content: []byte("search .test\n")},
		{name: "integer arity", filename: "test", content: []byte("timeout\n")},
		{name: "integer spelling", filename: "test", content: []byte("search_order 01\n")},
		{name: "argument required", filename: "test", content: []byte("lookup\n")},
		{name: "unsupported directive", filename: "test", content: []byte("rotate yes\n")},
		{name: "duplicate nameserver", filename: "test", content: []byte("nameserver 192.0.2.1\nnameserver 192.0.2.1\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseDarwinResolver(test.filename, test.content); err == nil {
				t.Fatalf("parseDarwinResolver(%q, %q) accepted malformed facts", test.filename, test.content)
			}
		})
	}
}

// TestDarwinResolverEntrySecurityRejectsUnsafeNativeShapes covers every portable stat boundary.
func TestDarwinResolverEntrySecurityRejectsUnsafeNativeShapes(t *testing.T) {
	valid := darwinTestEntry("test", []byte("domain test\n"), 10)
	tests := []struct {
		name   string
		mutate func(*darwinResolverEntry)
	}{
		{name: "nonregular", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Regular = false }},
		{name: "noncanonical filename", mutate: func(entry *darwinResolverEntry) { entry.Name = "Test" }},
		{name: "missing inode", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Inode = 0 }},
		{name: "hard link", mutate: func(entry *darwinResolverEntry) { entry.Metadata.LinkCount = 2 }},
		{name: "nonroot owner", mutate: func(entry *darwinResolverEntry) { entry.Metadata.UID = 501 }},
		{name: "unknown mode bits", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Mode |= 1 << 16 }},
		{name: "special mode", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Mode |= 0o4000 }},
		{name: "group write", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Mode |= 0o020 }},
		{name: "owner unreadable", mutate: func(entry *darwinResolverEntry) { entry.Metadata.Mode = 0o244 }},
		{name: "large content", mutate: func(entry *darwinResolverEntry) { entry.Content = bytesOfLength(maximumDarwinResolverFileBytes + 1) }},
	}
	if err := validateDarwinResolverEntry(valid); err != nil {
		t.Fatalf("validateDarwinResolverEntry() valid error = %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := cloneDarwinTestEntry(valid)
			test.mutate(&entry)
			if err := validateDarwinResolverEntry(entry); err == nil {
				t.Fatalf("validateDarwinResolverEntry() accepted %#v", entry.Metadata)
			}
		})
	}

	if !darwinResolverMetadataExact(valid.Metadata) {
		t.Fatal("darwinResolverMetadataExact() rejected canonical metadata")
	}
	noncanonical := valid.Metadata
	noncanonical.GID = 20
	if darwinResolverMetadataExact(noncanonical) {
		t.Fatal("darwinResolverMetadataExact() accepted a noncanonical group")
	}
	noncanonical = valid.Metadata
	noncanonical.Flags = 1
	if darwinResolverMetadataExact(noncanonical) {
		t.Fatal("darwinResolverMetadataExact() accepted native file flags")
	}
}

// TestDarwinResolverIdentityAndFingerprintBindEveryNativeAttribute covers CAS evidence encoding.
func TestDarwinResolverIdentityAndFingerprintBindEveryNativeAttribute(t *testing.T) {
	base := darwinTestEntry("test", []byte("domain test\n"), 10)
	baseFingerprint := darwinResolverEntryFingerprint(base)
	mutations := []func(*darwinResolverEntry){
		func(entry *darwinResolverEntry) { entry.Name = "child.test" },
		func(entry *darwinResolverEntry) { entry.Metadata.Regular = false },
		func(entry *darwinResolverEntry) { entry.Metadata.Device++ },
		func(entry *darwinResolverEntry) { entry.Metadata.Inode++ },
		func(entry *darwinResolverEntry) { entry.Metadata.Generation++ },
		func(entry *darwinResolverEntry) { entry.Metadata.UID++ },
		func(entry *darwinResolverEntry) { entry.Metadata.GID++ },
		func(entry *darwinResolverEntry) { entry.Metadata.Mode = 0o600 },
		func(entry *darwinResolverEntry) { entry.Metadata.Flags++ },
		func(entry *darwinResolverEntry) { entry.Metadata.LinkCount++ },
		func(entry *darwinResolverEntry) { entry.Content = []byte("domain child.test\n") },
	}
	for index, mutate := range mutations {
		candidate := cloneDarwinTestEntry(base)
		mutate(&candidate)
		if got := darwinResolverEntryFingerprint(candidate); got == baseFingerprint {
			t.Fatalf("mutation %d did not change darwinResolverEntryFingerprint()", index)
		}
	}
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	canonical := darwinTestEntry(fixedDarwinResolverName, darwinCanonicalTestContent(t, request), 11)
	canonicalFact, _, err := darwinResolverRuleFact(canonical, request)
	if err != nil || !canonicalFact.NativeExact {
		t.Fatalf("darwinResolverRuleFact() canonical = %#v, %v", canonicalFact, err)
	}
	flagged := cloneDarwinTestEntry(canonical)
	flagged.Metadata.Flags = 1
	flaggedFact, _, err := darwinResolverRuleFact(flagged, request)
	if err != nil || flaggedFact.NativeExact || flaggedFact.NativeAttributesSHA256 == canonicalFact.NativeAttributesSHA256 {
		t.Fatalf("darwinResolverRuleFact() flagged = %#v, %v, want drifted identity", flaggedFact, err)
	}
	regenerated := cloneDarwinTestEntry(canonical)
	regenerated.Metadata.Generation++
	regeneratedFact, _, err := darwinResolverRuleFact(regenerated, request)
	if err != nil || regeneratedFact.NativeID == canonicalFact.NativeID || regeneratedFact.NativeAttributesSHA256 == canonicalFact.NativeAttributesSHA256 {
		t.Fatalf("darwinResolverRuleFact() regenerated = %#v, %v, want distinct identity", regeneratedFact, err)
	}
	for _, malformed := range []string{
		"",
		"test",
		"test@1:2",
		"test@gggggggggggggggg:0000000000000001:00000007",
		"test@0000000000000001:0000000000000000:00000007",
		"test@0000000000000001:0000000000000001:gggggggg",
		"test@0000000000000001:0000000000000001:0000000A",
	} {
		if _, _, _, _, err := parseDarwinResolverNativeID(malformed); err == nil {
			t.Fatalf("parseDarwinResolverNativeID(%q) accepted malformed identity", malformed)
		}
	}
}

// TestDarwinResolverOrphanRecoveryAdmissionPinsNamespaceOwnershipAndBounds proves cleanup cannot widen into foreign files.
func TestDarwinResolverOrphanRecoveryAdmissionPinsNamespaceOwnershipAndBounds(t *testing.T) {
	first := darwinResolverOrphanPrefix + strings.Repeat("0", darwinResolverOrphanHexBytes*2)
	second := darwinResolverOrphanPrefix + strings.Repeat("a", darwinResolverOrphanHexBytes*2)
	names, err := darwinResolverOrphanNames([]string{"test", second, "child.test", first})
	if err != nil {
		t.Fatalf("darwinResolverOrphanNames() error = %v", err)
	}
	if !slices.Equal(names, []string{first, second}) {
		t.Fatalf("darwinResolverOrphanNames() = %v, want sorted exact Harbor names", names)
	}
	for _, foreign := range []string{
		"harbor-resolver-" + strings.Repeat("0", darwinResolverOrphanHexBytes*2),
		darwinResolverOrphanPrefix + strings.Repeat("0", darwinResolverOrphanHexBytes*2-1),
		darwinResolverOrphanPrefix + strings.Repeat("A", darwinResolverOrphanHexBytes*2),
		darwinResolverOrphanPrefix + strings.Repeat("g", darwinResolverOrphanHexBytes*2),
	} {
		if isDarwinResolverOrphanName(foreign) {
			t.Fatalf("isDarwinResolverOrphanName(%q) accepted a foreign name", foreign)
		}
	}
	if _, err := darwinResolverOrphanNames([]string{first, first}); err == nil {
		t.Fatal("darwinResolverOrphanNames() accepted duplicate directory evidence")
	}
	tooManyOrphans := make([]string, maximumDarwinResolverOrphans+1)
	for index := range tooManyOrphans {
		tooManyOrphans[index] = fmt.Sprintf("%s%032x", darwinResolverOrphanPrefix, index)
	}
	if _, err := darwinResolverOrphanNames(tooManyOrphans); err == nil {
		t.Fatal("darwinResolverOrphanNames() accepted an unbounded orphan set")
	}
	tooManyEntries := make([]string, maximumDarwinResolverEntries+1)
	for index := range tooManyEntries {
		tooManyEntries[index] = fmt.Sprintf("foreign-%d.test", index)
	}
	if _, err := darwinResolverOrphanNames(tooManyEntries); err == nil {
		t.Fatal("darwinResolverOrphanNames() accepted an unbounded directory")
	}

	entry := darwinTestEntry(fixedDarwinResolverName, nil, 15)
	entry.Metadata.Mode = 0o600
	guard, err := darwinResolverOrphanGuard(first, entry)
	if err != nil {
		t.Fatalf("darwinResolverOrphanGuard() error = %v", err)
	}
	if !guard.Exists || guard.Inode != entry.Metadata.Inode || guard.Generation != entry.Metadata.Generation {
		t.Fatalf("darwinResolverOrphanGuard() = %#v, want complete native identity", guard)
	}
	owned := darwinTestEntry(
		fixedDarwinResolverName,
		darwinCanonicalTestContent(t, resolverTestRequest(t, networkpolicy.DarwinResolverFile)),
		16,
	)
	if _, err := darwinResolverOrphanGuard(second, owned); err != nil {
		t.Fatalf("darwinResolverOrphanGuard() completed owner error = %v", err)
	}
	driftedOwned := cloneDarwinTestEntry(owned)
	driftedOwned.Metadata.Mode = 0o600
	driftedOwned.Metadata.GID = 20
	driftedOwned.Metadata.Flags = 1
	if _, err := darwinResolverOrphanGuard(second, driftedOwned); err != nil {
		t.Fatalf("darwinResolverOrphanGuard() displaced owned drift error = %v", err)
	}
	rejections := []struct {
		name   string
		orphan string
		mutate func(*darwinResolverEntry)
	}{
		{name: "foreign namespace", orphan: "child.test"},
		{name: "wrong logical destination", orphan: first, mutate: func(entry *darwinResolverEntry) { entry.Name = "child.test" }},
		{name: "nonroot owner", orphan: first, mutate: func(entry *darwinResolverEntry) { entry.Metadata.UID = 501 }},
		{name: "noncanonical group", orphan: first, mutate: func(entry *darwinResolverEntry) { entry.Metadata.GID = 20 }},
		{name: "native flags", orphan: first, mutate: func(entry *darwinResolverEntry) { entry.Metadata.Flags = 1 }},
		{name: "hard link", orphan: first, mutate: func(entry *darwinResolverEntry) { entry.Metadata.LinkCount = 2 }},
		{name: "foreign hidden file", orphan: first, mutate: func(entry *darwinResolverEntry) {
			entry.Metadata.Mode = darwinResolverFileMode
			entry.Content = []byte("domain child.test\n")
		}},
		{name: "empty canonical file", orphan: first, mutate: func(entry *darwinResolverEntry) {
			entry.Metadata.Mode = darwinResolverFileMode
		}},
		{name: "foreign partial file", orphan: first, mutate: func(entry *darwinResolverEntry) {
			entry.Content = []byte("foreign")
		}},
	}
	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			candidate := cloneDarwinTestEntry(entry)
			if rejection.mutate != nil {
				rejection.mutate(&candidate)
			}
			if _, err := darwinResolverOrphanGuard(rejection.orphan, candidate); err == nil {
				t.Fatal("darwinResolverOrphanGuard() accepted unsafe recovery evidence")
			}
		})
	}
}

// TestDarwinResolverQuarantineNamespaceIsExactAndBounded confines root-only recovery to reviewed directory names.
func TestDarwinResolverQuarantineNamespaceIsExactAndBounded(t *testing.T) {
	first := darwinResolverQuarantinePrefix + strings.Repeat("0", darwinResolverOrphanHexBytes*2)
	second := darwinResolverQuarantinePrefix + strings.Repeat("a", darwinResolverOrphanHexBytes*2)
	names, err := darwinResolverQuarantineNames([]string{"test", second, "child.test", first})
	if err != nil {
		t.Fatalf("darwinResolverQuarantineNames() error = %v", err)
	}
	if !slices.Equal(names, []string{first, second}) {
		t.Fatalf("darwinResolverQuarantineNames() = %v, want sorted exact Harbor names", names)
	}
	for _, foreign := range []string{
		"harbor-resolver-quarantine-" + strings.Repeat("0", darwinResolverOrphanHexBytes*2),
		darwinResolverQuarantinePrefix + strings.Repeat("0", darwinResolverOrphanHexBytes*2-1),
		darwinResolverQuarantinePrefix + strings.Repeat("A", darwinResolverOrphanHexBytes*2),
		darwinResolverQuarantinePrefix + strings.Repeat("g", darwinResolverOrphanHexBytes*2),
	} {
		if isDarwinResolverQuarantineName(foreign) || isDarwinResolverTransactionName(foreign) {
			t.Fatalf("Darwin resolver quarantine namespace accepted %q", foreign)
		}
	}
	if !isDarwinResolverTransactionName(first) {
		t.Fatalf("isDarwinResolverTransactionName(%q) rejected a quarantine", first)
	}
	if _, err := darwinResolverQuarantineNames([]string{first, first}); err == nil {
		t.Fatal("darwinResolverQuarantineNames() accepted duplicate directory evidence")
	}
	tooMany := make([]string, maximumDarwinResolverOrphans+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("%s%032x", darwinResolverQuarantinePrefix, index)
	}
	if _, err := darwinResolverQuarantineNames(tooMany); err == nil {
		t.Fatal("darwinResolverQuarantineNames() accepted an unbounded quarantine set")
	}
}

// TestDarwinResolverGuardValidationAndMatching pins fixed-path CAS evidence before native effects.
func TestDarwinResolverGuardValidationAndMatching(t *testing.T) {
	entry := darwinTestEntry("test", []byte("domain test\n"), 10)
	absent := darwinResolverGuard{Name: fixedDarwinResolverName}
	existing := darwinResolverGuard{
		Exists:                 true,
		Name:                   fixedDarwinResolverName,
		Device:                 entry.Metadata.Device,
		Inode:                  entry.Metadata.Inode,
		Generation:             entry.Metadata.Generation,
		NativeAttributesSHA256: darwinResolverEntryFingerprint(entry),
	}
	if err := validateDarwinResolverGuard(absent); err != nil {
		t.Fatalf("validateDarwinResolverGuard() absent error = %v", err)
	}
	if err := validateDarwinResolverGuard(existing); err != nil {
		t.Fatalf("validateDarwinResolverGuard() existing error = %v", err)
	}
	invalid := []darwinResolverGuard{
		{Name: "child.test"},
		{Name: fixedDarwinResolverName, Device: 1},
		{Name: fixedDarwinResolverName, Generation: 1},
		{Exists: true, Name: fixedDarwinResolverName, NativeAttributesSHA256: existing.NativeAttributesSHA256},
		{Exists: true, Name: fixedDarwinResolverName, Inode: 10, NativeAttributesSHA256: "bad"},
	}
	for _, guard := range invalid {
		if err := validateDarwinResolverGuard(guard); err == nil {
			t.Fatalf("validateDarwinResolverGuard() accepted %#v", guard)
		}
	}
	if err := matchDarwinResolverGuard(absent, darwinResolverEntry{}, false); err != nil {
		t.Fatalf("matchDarwinResolverGuard() absent error = %v", err)
	}
	if err := matchDarwinResolverGuard(existing, entry, true); err != nil {
		t.Fatalf("matchDarwinResolverGuard() existing error = %v", err)
	}
	if err := matchDarwinResolverGuard(absent, entry, true); err == nil {
		t.Fatal("matchDarwinResolverGuard() accepted unexpected creation")
	}
	mutations := []func(*darwinResolverEntry){
		func(candidate *darwinResolverEntry) { candidate.Name = "child.test" },
		func(candidate *darwinResolverEntry) { candidate.Metadata.Device++ },
		func(candidate *darwinResolverEntry) { candidate.Metadata.Inode++ },
		func(candidate *darwinResolverEntry) { candidate.Metadata.Generation++ },
		func(candidate *darwinResolverEntry) { candidate.Metadata.Flags++ },
		func(candidate *darwinResolverEntry) { candidate.Content = []byte("domain child.test\n") },
	}
	for index, mutate := range mutations {
		candidate := cloneDarwinTestEntry(entry)
		mutate(&candidate)
		if err := matchDarwinResolverGuard(existing, candidate, true); err == nil {
			t.Fatalf("matchDarwinResolverGuard() accepted mutation %d", index)
		}
	}
}

// TestDarwinResolverObserveClassifiesExactAndMoreSpecificClaims proves complete directory conversion.
func TestDarwinResolverObserveClassifiesExactAndMoreSpecificClaims(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	canonical := darwinCanonicalTestContent(t, request)
	child := []byte(fmt.Sprintf(
		"%sversion=1 installation=%s policy=%s\ndomain child.test\nnameserver 192.0.2.1\n",
		darwinResolverOwnerPrefix,
		request.InstallationID(),
		request.PolicyFingerprint(),
	))
	store := &fakeDarwinResolverStore{entries: []darwinResolverEntry{
		darwinTestEntry("test", canonical, 30),
		darwinTestEntry("other.example", []byte("domain other.example\n"), 20),
		darwinTestEntry("child.test", child, 10),
	}}
	observation, err := newDarwinResolverBackend(store).observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() error = %v", err)
	}
	if !observation.Complete || observation.Truncated || len(observation.Rules) != 2 {
		t.Fatalf("observe() = %#v, want two complete relevant facts", observation)
	}
	if observation.Rules[0].Namespace != ".child.test" || observation.Rules[0].Owner != nil {
		t.Fatalf("child fact = %#v, want foreign descendant", observation.Rules[0])
	}
	if observation.Rules[1].Namespace != ".test" || observation.Rules[1].Owner == nil || !observation.Rules[1].NativeExact {
		t.Fatalf("exact fact = %#v, want exact owner", observation.Rules[1])
	}
	assessment, err := observation.Classify()
	if err != nil || assessment.State != StateForeign || assessment.Owned != OwnedStateExact || assessment.ForeignCount != 1 {
		t.Fatalf("Classify() = %#v, %v, want foreign plus exact owner", assessment, err)
	}

	store.entries = []darwinResolverEntry{darwinTestEntry("test", []byte("domain other.example\n"), 50)}
	occupied, err := newDarwinResolverBackend(store).observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() occupied destination error = %v", err)
	}
	occupiedAssessment, err := occupied.Classify()
	if err != nil || len(occupied.Rules) != 1 || occupied.Rules[0].Namespace != request.Suffix() || occupiedAssessment.State != StateForeign {
		t.Fatalf("occupied fixed destination = %#v, %#v, %v", occupied, occupiedAssessment, err)
	}
}

// TestDarwinResolverObserveRejectsIncompleteOrUnsafeSnapshots covers backend observation failures.
func TestDarwinResolverObserveRejectsIncompleteOrUnsafeSnapshots(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	tests := []struct {
		name    string
		request Request
		prepare func(*fakeDarwinResolverStore)
	}{
		{name: "invalid request", request: Request{}},
		{name: "wrong mechanism", request: resolverTestRequest(t, networkpolicy.UbuntuSystemdResolved)},
		{name: "snapshot failure", request: request, prepare: func(store *fakeDarwinResolverStore) { store.snapshotErr = errors.New("read failed") }},
		{name: "too many entries", request: request, prepare: func(store *fakeDarwinResolverStore) {
			store.entries = make([]darwinResolverEntry, maximumDarwinResolverEntries+1)
		}},
		{name: "duplicate entries", request: request, prepare: func(store *fakeDarwinResolverStore) {
			store.entries = []darwinResolverEntry{
				darwinTestEntry("test", []byte("domain test\n"), 1),
				darwinTestEntry("test", []byte("domain test\n"), 2),
			}
		}},
		{name: "unsafe entry", request: request, prepare: func(store *fakeDarwinResolverStore) {
			entry := darwinTestEntry("test", []byte("domain test\n"), 1)
			entry.Metadata.UID = 501
			store.entries = []darwinResolverEntry{entry}
		}},
		{name: "malformed entry", request: request, prepare: func(store *fakeDarwinResolverStore) {
			store.entries = []darwinResolverEntry{darwinTestEntry("test", []byte("unknown value\n"), 1)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeDarwinResolverStore{}
			if test.prepare != nil {
				test.prepare(store)
			}
			if _, err := newDarwinResolverBackend(store).observe(t.Context(), test.request); err == nil {
				t.Fatal("observe() accepted incomplete or unsafe snapshot")
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newDarwinResolverBackend(&fakeDarwinResolverStore{}).observe(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("observe() canceled error = %v", err)
	}
	canceledAfterSnapshot, cancelAfterSnapshot := context.WithCancel(context.Background())
	store := &fakeDarwinResolverStore{afterSnapshot: cancelAfterSnapshot}
	if _, err := newDarwinResolverBackend(store).observe(canceledAfterSnapshot, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("observe() post-snapshot canceled error = %v", err)
	}
	loopCanceled := &stagedErrorContext{failAt: 3}
	loopStore := &fakeDarwinResolverStore{entries: []darwinResolverEntry{darwinTestEntry("other.example", nil, 1)}}
	if _, err := newDarwinResolverBackend(loopStore).observe(loopCanceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("observe() loop canceled error = %v", err)
	}
}

// TestDarwinResolverAdapterEnsuresRepairsAndReleasesOnlyFixedOwnership exercises the full CAS lifecycle.
func TestDarwinResolverAdapterEnsuresRepairsAndReleasesOnlyFixedOwnership(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	store := &fakeDarwinResolverStore{}
	adapter := newAdapter(newDarwinResolverBackend(store))

	absent, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() absent error = %v", err)
	}
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, absent))
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	if !ensured.Attempted || !ensured.Changed || ensured.After.Rules[0].Owner == nil || !ensured.After.Rules[0].NativeExact {
		t.Fatalf("EnsureIfObserved() = %#v, want exact owned replacement", ensured)
	}
	if len(store.replaceGuards) != 1 || store.replaceGuards[0].Exists || store.replaceGuards[0].Name != fixedDarwinResolverName {
		t.Fatalf("replace guard = %#v, want fixed absent guard", store.replaceGuards)
	}

	released, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, ensured.After))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if !released.Attempted || !released.Changed || len(released.After.Rules) != 0 || len(store.removeGuards) != 1 || !store.removeGuards[0].Exists {
		t.Fatalf("ReleaseIfObserved() = %#v, guards %#v", released, store.removeGuards)
	}

	driftedContent := append(darwinCanonicalTestContent(t, request), []byte("search_order 1\n")...)
	store.entries = []darwinResolverEntry{darwinTestEntry("test", driftedContent, 2000)}
	drifted, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() drifted error = %v", err)
	}
	assessment, err := drifted.Classify()
	if err != nil || assessment.State != StateOwnedDrifted {
		t.Fatalf("Classify() drifted = %#v, %v", assessment, err)
	}
	if _, err := adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, drifted)); err != nil {
		t.Fatalf("EnsureIfObserved() repair error = %v", err)
	}
	lastGuard := store.replaceGuards[len(store.replaceGuards)-1]
	if !lastGuard.Exists || lastGuard.Inode != 2000 {
		t.Fatalf("repair guard = %#v, want observed drifted inode", lastGuard)
	}
}

// TestDarwinResolverAdapterPreservesForeignFilesDuringRelease proves mutation scope is one exact file.
func TestDarwinResolverAdapterPreservesForeignFilesDuringRelease(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	foreign := darwinTestEntry("child.test", []byte("domain child.test\nnameserver 192.0.2.1\n"), 41)
	store := &fakeDarwinResolverStore{entries: []darwinResolverEntry{
		foreign,
		darwinTestEntry("test", darwinCanonicalTestContent(t, request), 42),
	}}
	adapter := newAdapter(newDarwinResolverBackend(store))
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	change, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, before))
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	if len(store.entries) != 1 || store.entries[0].Name != foreign.Name || len(change.After.Rules) != 1 || change.After.Rules[0].Namespace != ".child.test" {
		t.Fatalf("release retained entries = %#v, change = %#v", store.entries, change)
	}

	foreignExact := darwinTestEntry("test", []byte("domain test\nnameserver 192.0.2.2\n"), 50)
	store.entries = []darwinResolverEntry{foreignExact}
	foreignObservation, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() foreign error = %v", err)
	}
	if _, err := adapter.EnsureIfObserved(t.Context(), request, resolverFingerprint(t, foreignObservation)); err == nil {
		t.Fatal("EnsureIfObserved() replaced a foreign exact-path file")
	}
	if len(store.replaceGuards) != 0 {
		t.Fatalf("foreign ensure made %d replacement calls", len(store.replaceGuards))
	}
	if _, err := adapter.ReleaseIfObserved(t.Context(), request, resolverFingerprint(t, foreignObservation)); err != nil {
		t.Fatalf("ReleaseIfObserved() foreign no-op error = %v", err)
	}
	if len(store.entries) != 1 || len(store.removeGuards) != 1 {
		t.Fatalf("foreign release changed entries %#v", store.entries)
	}
}

// TestDarwinResolverMutationRejectsChangedDescendantObservation proves native effects bind every admitted claim.
func TestDarwinResolverMutationRejectsChangedDescendantObservation(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	child := darwinTestEntry("child.test", []byte("domain child.test\nnameserver 192.0.2.1\n"), 71)
	tests := []struct {
		name    string
		entries []darwinResolverEntry
		mutate  func(*Adapter, Request, string) error
	}{
		{
			name: "ensure",
			mutate: func(adapter *Adapter, request Request, fingerprint string) error {
				_, err := adapter.EnsureIfObserved(t.Context(), request, fingerprint)
				return err
			},
		},
		{
			name:    "release",
			entries: []darwinResolverEntry{darwinTestEntry("test", darwinCanonicalTestContent(t, request), 72)},
			mutate: func(adapter *Adapter, request Request, fingerprint string) error {
				_, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprint)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeDarwinResolverStore{entries: cloneDarwinTestEntries(test.entries)}
			adapter := newAdapter(newDarwinResolverBackend(store))
			before, err := adapter.Observe(t.Context(), request)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			changed := false
			store.afterSnapshot = func() {
				if changed {
					return
				}
				changed = true
				store.entries = append(store.entries, child)
			}
			if err := test.mutate(adapter, request, resolverFingerprint(t, before)); err == nil {
				t.Fatal("mutation accepted a descendant inserted after observation")
			}
			if _, fixedExists := findDarwinTestEntry(store.entries, fixedDarwinResolverName); fixedExists != (test.name == "release") {
				t.Fatalf("fixed destination existence = %t after rejected %s", fixedExists, test.name)
			}
			if _, childExists := findDarwinTestEntry(store.entries, child.Name); !childExists {
				t.Fatal("rejected mutation did not preserve inserted foreign claim")
			}
		})
	}
}

// TestDarwinResolverMutationRevalidatesImmediatelyBeforeEffect closes the staging-time whole-observation race.
func TestDarwinResolverMutationRevalidatesImmediatelyBeforeEffect(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	child := darwinTestEntry("child.test", []byte("domain child.test\nnameserver 192.0.2.1\n"), 81)
	tests := []struct {
		name    string
		entries []darwinResolverEntry
		invoke  func(*Adapter, Request, string) error
		arm     func(*fakeDarwinResolverStore, func())
	}{
		{
			name: "ensure",
			invoke: func(adapter *Adapter, request Request, fingerprint string) error {
				_, err := adapter.EnsureIfObserved(t.Context(), request, fingerprint)
				return err
			},
			arm: func(store *fakeDarwinResolverStore, mutate func()) {
				store.afterReplaceValidation = mutate
			},
		},
		{
			name:    "release",
			entries: []darwinResolverEntry{darwinTestEntry("test", darwinCanonicalTestContent(t, request), 82)},
			invoke: func(adapter *Adapter, request Request, fingerprint string) error {
				_, err := adapter.ReleaseIfObserved(t.Context(), request, fingerprint)
				return err
			},
			arm: func(store *fakeDarwinResolverStore, mutate func()) {
				store.afterRemoveValidation = mutate
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeDarwinResolverStore{entries: cloneDarwinTestEntries(test.entries)}
			adapter := newAdapter(newDarwinResolverBackend(store))
			before, err := adapter.Observe(t.Context(), request)
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			mutated := false
			test.arm(store, func() {
				if mutated {
					return
				}
				mutated = true
				store.entries = append(store.entries, child)
			})
			if err := test.invoke(adapter, request, resolverFingerprint(t, before)); err == nil {
				t.Fatal("mutation accepted a descendant inserted after its first native validation")
			}
			_, fixedExists := findDarwinTestEntry(store.entries, fixedDarwinResolverName)
			if fixedExists != (test.name == "release") {
				t.Fatalf("fixed destination existence = %t after rejected %s", fixedExists, test.name)
			}
			if _, childExists := findDarwinTestEntry(store.entries, child.Name); !childExists {
				t.Fatal("rejected mutation did not preserve the concurrent foreign rule")
			}
		})
	}
}

// TestDarwinResolverPostPublicationSurroundingsDetectsRacingClaims proves rollback evidence ignores only the fixed target.
func TestDarwinResolverPostPublicationSurroundingsDetectsRacingClaims(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	before := []darwinResolverEntry{darwinTestEntry("other.example", []byte("domain other.example\n"), 91)}
	expected, err := darwinResolverSurroundingsFingerprint(t.Context(), request, before)
	if err != nil {
		t.Fatalf("darwinResolverSurroundingsFingerprint() error = %v", err)
	}
	after := append(
		cloneDarwinTestEntries(before),
		darwinTestEntry(fixedDarwinResolverName, darwinCanonicalTestContent(t, request), 92),
	)
	if err := matchDarwinResolverSurroundings(t.Context(), request, after, expected); err != nil {
		t.Fatalf("matchDarwinResolverSurroundings() fixed replacement error = %v", err)
	}
	after = append(after, darwinTestEntry("child.test", []byte("domain child.test\n"), 93))
	if err := matchDarwinResolverSurroundings(t.Context(), request, after, expected); err == nil {
		t.Fatal("matchDarwinResolverSurroundings() accepted a racing descendant claim")
	}
}

// TestDarwinResolverBackendRejectsUnadmittedMutationPlans covers direct backend misuse and store failures.
func TestDarwinResolverBackendRejectsUnadmittedMutationPlans(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	store := &fakeDarwinResolverStore{}
	backend := &darwinResolverBackend{store: store}
	absent := Observation{Request: request, Complete: true}
	otherRequest, err := NewRequest("installation-other", request.Policy())
	if err != nil {
		t.Fatalf("NewRequest() alternate error = %v", err)
	}
	tests := []struct {
		name    string
		ensure  bool
		request Request
		before  Observation
	}{
		{name: "ensure invalid request", ensure: true, request: Request{}, before: absent},
		{name: "release invalid request", request: Request{}, before: absent},
		{name: "ensure invalid observation", ensure: true, request: request, before: Observation{}},
		{name: "release invalid observation", request: request, before: Observation{}},
		{name: "ensure request mismatch", ensure: true, request: otherRequest, before: absent},
		{name: "release request mismatch", request: otherRequest, before: absent},
		{name: "ensure incomplete", ensure: true, request: request, before: Observation{Request: request}},
		{name: "release absent", request: request, before: absent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.ensure {
				err = backend.ensure(t.Context(), test.request, test.before)
			} else {
				err = backend.release(t.Context(), test.request, test.before)
			}
			if err == nil {
				t.Fatal("backend mutation accepted an unadmitted plan")
			}
		})
	}

	store.replaceErr = errors.New("replace failed")
	if err := backend.ensure(t.Context(), request, absent); !errors.Is(err, store.replaceErr) {
		t.Fatalf("ensure() error = %v, want replace failure", err)
	}
	store.replaceErr = nil
	entry := darwinTestEntry("test", darwinCanonicalTestContent(t, request), 90)
	store.entries = []darwinResolverEntry{entry}
	exact, err := backend.observe(t.Context(), request)
	if err != nil {
		t.Fatalf("observe() exact error = %v", err)
	}
	store.removeErr = errors.New("remove failed")
	if err := backend.release(t.Context(), request, exact); !errors.Is(err, store.removeErr) {
		t.Fatalf("release() error = %v, want remove failure", err)
	}
	store.removeErr = nil
	forged := cloneObservation(exact)
	forged.Rules[0].NativeExact = false
	forged.Rules[0].NativeID = "malformed"
	if err := backend.ensure(t.Context(), request, forged); err == nil {
		t.Fatal("ensure() accepted malformed owned native evidence")
	}
	if err := backend.release(t.Context(), request, forged); err == nil {
		t.Fatal("release() accepted malformed owned native evidence")
	}
}

// TestDarwinResolverOwnedPlannerRejectsForgedNativeEvidence covers guarded-rule failure branches.
func TestDarwinResolverOwnedPlannerRejectsForgedNativeEvidence(t *testing.T) {
	request := resolverTestRequest(t, networkpolicy.DarwinResolverFile)
	owner := request.OwnerMarker()
	base := resolverExactRule(request, "test@0000000000000001:0000000000000001:00000007")
	base.Mechanism = networkpolicy.DarwinResolverFile
	base.NativeAttributesSHA256 = testNativeAttributesFingerprint
	base.Owner = &owner
	observation := Observation{Request: request, Complete: true, Rules: []RuleFact{base}}

	if _, err := uniqueDarwinOwnedGuard(Observation{Request: request, Complete: true}, request); err == nil {
		t.Fatal("uniqueDarwinOwnedGuard() accepted no owner")
	}
	duplicate := observation
	duplicate.Rules = []RuleFact{base, base}
	if _, err := uniqueDarwinOwnedGuard(duplicate, request); err == nil {
		t.Fatal("uniqueDarwinOwnedGuard() accepted duplicate owners")
	}
	malformed := observation
	malformed.Rules = []RuleFact{cloneRuleFact(base)}
	malformed.Rules[0].NativeID = "bad"
	if _, err := uniqueDarwinOwnedGuard(malformed, request); err == nil {
		t.Fatal("uniqueDarwinOwnedGuard() accepted malformed native identity")
	}
	outside := observation
	outside.Rules = []RuleFact{cloneRuleFact(base)}
	outside.Rules[0].Namespace = ".child.test"
	if _, err := uniqueDarwinOwnedGuard(outside, request); err == nil {
		t.Fatal("uniqueDarwinOwnedGuard() accepted ownership outside fixed path")
	}
	wrongName := base
	wrongName.NativeID = "child.test@0000000000000001:0000000000000001:00000007"
	if _, err := darwinResolverGuardFromRule(wrongName); err == nil {
		t.Fatal("darwinResolverGuardFromRule() accepted another destination")
	}
	badDigest := base
	badDigest.NativeAttributesSHA256 = "bad"
	if _, err := darwinResolverGuardFromRule(badDigest); err == nil {
		t.Fatal("darwinResolverGuardFromRule() accepted malformed digest")
	}
	malformedGuard := base
	malformedGuard.NativeID = "bad"
	if _, err := darwinResolverGuardFromRule(malformedGuard); err == nil {
		t.Fatal("darwinResolverGuardFromRule() accepted malformed native ID")
	}
	if _, err := marshalDarwinResolver(Request{}); err == nil {
		t.Fatal("marshalDarwinResolver() accepted an invalid request")
	}
}

// stagedErrorContext returns cancellation on one deterministic Err call.
type stagedErrorContext struct {
	calls  int
	failAt int
}

// Deadline reports no deadline because staged cancellation is call-count driven.
func (*stagedErrorContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

// Done returns no channel because callers exercise the Err contract directly.
func (*stagedErrorContext) Done() <-chan struct{} {
	return nil
}

// Err returns context cancellation at and after the configured call count.
func (ctx *stagedErrorContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.failAt {
		return context.Canceled
	}
	return nil
}

// Value carries no values because resolver backends do not consume context metadata.
func (*stagedErrorContext) Value(any) any {
	return nil
}

// darwinTestEntry returns one secure root-owned direct resolver file fixture.
func darwinTestEntry(name string, content []byte, inode uint64) darwinResolverEntry {
	return darwinResolverEntry{
		Name:    name,
		Content: slices.Clone(content),
		Metadata: darwinResolverMetadata{
			Regular:    true,
			Device:     1,
			Inode:      inode,
			Generation: darwinTestGeneration,
			UID:        0,
			GID:        0,
			Mode:       darwinResolverFileMode,
			LinkCount:  1,
		},
	}
}

// darwinCanonicalTestContent returns canonical bytes for one validated Darwin request fixture.
func darwinCanonicalTestContent(t *testing.T, request Request) []byte {
	t.Helper()
	content, err := marshalDarwinResolver(request)
	if err != nil {
		t.Fatalf("marshalDarwinResolver() fixture error = %v", err)
	}
	return content
}

// cloneDarwinTestEntry returns independent file content for mutation tests.
func cloneDarwinTestEntry(entry darwinResolverEntry) darwinResolverEntry {
	entry.Content = slices.Clone(entry.Content)
	return entry
}

// cloneDarwinTestEntries returns independent entries for fake snapshot boundaries.
func cloneDarwinTestEntries(entries []darwinResolverEntry) []darwinResolverEntry {
	cloned := make([]darwinResolverEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = cloneDarwinTestEntry(entry)
	}
	return cloned
}

// findDarwinTestEntry locates one direct fake-store entry by name.
func findDarwinTestEntry(entries []darwinResolverEntry, name string) (darwinResolverEntry, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return darwinResolverEntry{}, false
}

// bytesOfLength returns deterministic bounded-parser input without sharing mutable storage.
func bytesOfLength(length int) []byte {
	return []byte(strings.Repeat("x", length))
}
