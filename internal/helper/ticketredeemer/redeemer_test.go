package ticketredeemer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// TestValidateLayoutRequiresTheFixedShape rejects every path dimension that could redirect elevated I/O.
func TestValidateLayoutRequiresTheFixedShape(t *testing.T) {
	valid := testPaths(t.TempDir())
	if err := validateLayout(valid); err != nil {
		t.Fatalf("validateLayout(valid) error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*machinepaths.Paths)
	}{
		{name: "empty root", mutate: func(paths *machinepaths.Paths) { paths.Root = "" }},
		{name: "relative root", mutate: func(paths *machinepaths.Paths) { paths.Root = "relative" }},
		{name: "unclean root", mutate: func(paths *machinepaths.Paths) {
			paths.Root += string(filepath.Separator) + ".." + string(filepath.Separator) + "root"
		}},
		{name: "state", mutate: func(paths *machinepaths.Paths) { paths.StateDirectory += "-other" }},
		{name: "replay", mutate: func(paths *machinepaths.Paths) { paths.ReplayDirectory += "-other" }},
		{name: "ownership", mutate: func(paths *machinepaths.Paths) { paths.OwnershipPath += "-other" }},
		{name: "host projection", mutate: func(paths *machinepaths.Paths) { paths.HostProjectionPath += "-other" }},
		{name: "tickets", mutate: func(paths *machinepaths.Paths) { paths.TicketsDirectory += "-other" }},
		{name: "pending", mutate: func(paths *machinepaths.Paths) { paths.PendingDirectory += "-other" }},
		{name: "claims", mutate: func(paths *machinepaths.Paths) { paths.ClaimsDirectory += "-other" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			paths := valid
			test.mutate(&paths)
			if err := validateLayout(paths); err == nil {
				t.Fatal("validateLayout() accepted redirected paths")
			}
		})
	}
}

// TestValidateDependenciesRejectsEveryMissingBoundary prevents a partially wired redeemer from failing open.
func TestValidateDependenciesRejectsEveryMissingBoundary(t *testing.T) {
	valid := inertDependencies()
	if err := validateDependencies(valid); err != nil {
		t.Fatalf("validateDependencies(valid) error = %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*dependencies)
	}{
		{name: "clock", mutate: func(value *dependencies) { value.clock = nil }},
		{name: "process admission", mutate: func(value *dependencies) { value.admitProcess = nil }},
		{name: "ownership", mutate: func(value *dependencies) { value.openOwnership = nil }},
		{name: "pending open", mutate: func(value *dependencies) { value.files.openPending = nil }},
		{name: "claim open", mutate: func(value *dependencies) { value.files.openClaim = nil }},
		{name: "existence", mutate: func(value *dependencies) { value.files.entryExists = nil }},
		{name: "rename", mutate: func(value *dependencies) { value.files.rename = nil }},
		{name: "claim security", mutate: func(value *dependencies) { value.files.secureClaim = nil }},
		{name: "file sync", mutate: func(value *dependencies) { value.files.syncFile = nil }},
		{name: "directory sync", mutate: func(value *dependencies) { value.files.syncDir = nil }},
		{name: "close", mutate: func(value *dependencies) { value.files.closeFile = nil }},
		{name: "read", mutate: func(value *dependencies) { value.files.read = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := validateDependencies(candidate); err == nil {
				t.Fatal("validateDependencies() accepted incomplete dependencies")
			}
		})
	}
}

// TestVerifyEnvelopeDistinguishesExpiredAuthenticContent proves stale classification never authenticates a bad signature.
func TestVerifyEnvelopeDistinguishesExpiredAuthenticContent(t *testing.T) {
	now := testRedeemerTime()
	publicKey, privateKey := testRedeemerKey('v')
	ticket := testRedeemerTicket(now, "501")
	ticket.ExpiresAt = now.Add(-time.Second)
	envelope, err := ticketauth.Sign(ticket, privateKey, ticket.ExpiresAt.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if _, err := verifyEnvelope(envelope, publicKey, now); !errors.Is(err, helper.ErrTicketReferenceStale) {
		t.Fatalf("verifyEnvelope(expired) error = %v, want stale", err)
	}

	envelope.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if _, err := verifyEnvelope(envelope, publicKey, now); !errors.Is(err, helper.ErrTicketRedemptionFailed) || errors.Is(err, helper.ErrTicketReferenceStale) {
		t.Fatalf("verifyEnvelope(bad signature) error = %v, want failed only", err)
	}
}

// TestDecodeVerifierKeyRequiresCanonicalEd25519Encoding rejects every alternate protected-state spelling.
func TestDecodeVerifierKeyRequiresCanonicalEd25519Encoding(t *testing.T) {
	publicKey, _ := testRedeemerKey('k')
	canonical := base64.StdEncoding.EncodeToString(publicKey)
	if got, err := decodeVerifierKey(canonical); err != nil || string(got) != string(publicKey) {
		t.Fatalf("decodeVerifierKey() = %x, %v", got, err)
	}
	for _, value := range []string{"", "%%%%", base64.StdEncoding.EncodeToString(publicKey[:len(publicKey)-1]), strings.TrimRight(canonical, "=")} {
		if _, err := decodeVerifierKey(value); err == nil {
			t.Fatalf("decodeVerifierKey(%q) accepted invalid content", value)
		}
	}
}

// TestValidateBootstrapTicketRequiresFreshPoolAuthority covers every first-claim constraint independently of signature admission.
func TestValidateBootstrapTicketRequiresFreshPoolAuthority(t *testing.T) {
	valid := testRedeemerPoolTicket(testRedeemerTime(), "501")
	if err := validateBootstrapTicket(valid, "501"); err != nil {
		t.Fatalf("validateBootstrapTicket() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*helper.Ticket)
	}{
		{name: "operation", mutate: func(ticket *helper.Ticket) { ticket.Operation = helper.OperationEnsureLoopbackIdentity }},
		{name: "requester", mutate: func(ticket *helper.Ticket) { ticket.RequesterIdentity = "502" }},
		{name: "generation", mutate: func(ticket *helper.Ticket) { ticket.OwnershipGeneration = 2 }},
		{name: "ownership schema", mutate: func(ticket *helper.Ticket) {
			ticket.OwnershipSchemaVersion = ownership.NetworkPolicySchemaVersion
			ticket.NetworkPolicyFingerprint = strings.Repeat("a", 64)
		}},
		{name: "network policy fingerprint", mutate: func(ticket *helper.Ticket) {
			ticket.NetworkPolicyFingerprint = strings.Repeat("a", 64)
		}},
		{name: "pool authority", mutate: func(ticket *helper.Ticket) { ticket.ExpectedLoopbackPool = nil }},
		{name: "owned identity", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation.State = helper.ObservationOwned
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := testRedeemerPoolTicket(testRedeemerTime(), "501")
			test.mutate(&ticket)
			if err := validateBootstrapTicket(ticket, "501"); err == nil {
				t.Fatal("validateBootstrapTicket() error = nil")
			}
		})
	}
}

// TestValidateOwnershipObservationRequiresCanonicalFingerprint proves injected protected state cannot omit validation evidence.
func TestValidateOwnershipObservationRequiresCanonicalFingerprint(t *testing.T) {
	publicKey, _ := testRedeemerKey('o')
	record := ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     "harbor-redeemer-test",
		OwnerIdentity:      "501",
		Generation:         1,
		LoopbackPoolPrefix: "127.77.0.8/29",
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(publicKey),
	}
	valid := testOwnershipObservation(t, record)
	if err := validateOwnershipObservation(valid); err != nil {
		t.Fatalf("validateOwnershipObservation() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ownership.Observation)
	}{
		{name: "missing", mutate: func(observation *ownership.Observation) { observation.Exists = false }},
		{name: "record", mutate: func(observation *ownership.Observation) { observation.Record.Generation = 0 }},
		{name: "fingerprint", mutate: func(observation *ownership.Observation) { observation.Fingerprint = strings.Repeat("0", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation := valid
			test.mutate(&observation)
			if err := validateOwnershipObservation(observation); err == nil {
				t.Fatal("validateOwnershipObservation() error = nil")
			}
		})
	}
}

// TestBootstrapOwnershipRejectsAtomicClaimFailures proves no injected claim result can broaden the authenticated record.
func TestBootstrapOwnershipRejectsAtomicClaimFailures(t *testing.T) {
	now := testRedeemerTime()
	publicKey, privateKey := testRedeemerKey('b')
	ticket := testRedeemerPoolTicket(now, "501")
	envelope, err := ticketauth.Sign(ticket, privateKey, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	wantRecord := ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     ticket.InstallationID,
		OwnerIdentity:      ticket.RequesterIdentity,
		Generation:         ticket.OwnershipGeneration,
		LoopbackPoolPrefix: ticket.ApprovedPool,
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(publicKey),
	}
	conflicting := wantRecord
	conflicting.InstallationID = "harbor-other-installation"
	tests := []struct {
		name     string
		observer *testOwnershipObserver
	}{
		{name: "claim error", observer: &testOwnershipObserver{claimErr: errors.New("claim unavailable")}},
		{name: "missing result", observer: &testOwnershipObserver{}},
		{name: "different result", observer: &testOwnershipObserver{claim: testOwnershipObservation(t, conflicting)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redeemer := &Redeemer{
				topology:  &topology{requesterIdentity: ticket.RequesterIdentity},
				ownership: test.observer,
			}
			if _, _, err := redeemer.bootstrapOwnership(t.Context(), envelope, now); !errors.Is(err, helper.ErrTicketRedemptionFailed) || !errors.Is(err, ErrReferenceConsumed) {
				t.Fatalf("bootstrapOwnership() error = %v, want consumed failure", err)
			}
			if len(test.observer.claimed) != 1 || test.observer.claimed[0] != wantRecord {
				t.Fatalf("Claim() records = %#v, want %#v", test.observer.claimed, wantRecord)
			}
		})
	}
}

// TestReadBoundedRejectsEmptyOversizedAndClosedFiles covers every metadata and stream boundary before decoding.
func TestReadBoundedRejectsEmptyOversizedAndClosedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open empty fixture: %v", err)
	}
	if _, err := readBounded(file, 4); err == nil {
		t.Fatal("readBounded() accepted empty content")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close empty fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatalf("write oversized fixture: %v", err)
	}
	file, err = os.Open(path)
	if err != nil {
		t.Fatalf("open oversized fixture: %v", err)
	}
	if _, err := readBounded(file, 4); err == nil {
		t.Fatal("readBounded() accepted oversized content")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close oversized fixture: %v", err)
	}
	if _, err := readBounded(file, 4); err == nil {
		t.Fatal("readBounded() accepted a closed handle")
	}
	if err := validatePendingEnvelopeFile(file); err == nil {
		t.Fatal("validatePendingEnvelopeFile() accepted a closed handle")
	}
}

// inertDependencies supplies non-nil functions without touching storage.
func inertDependencies() dependencies {
	return dependencies{
		clock: testRedeemerClock{now: testRedeemerTime()},
		admitProcess: func() error {
			return nil
		},
		openOwnership: func(string) (ownershipStore, error) {
			return &testOwnershipObserver{}, nil
		},
		files: fileOperations{
			openPending: func(*os.File, string, string) (*os.File, error) { return nil, nil },
			openClaim:   func(*os.File, string, string) (*os.File, error) { return nil, nil },
			entryExists: func(*os.File, string, string) (bool, error) { return false, nil },
			rename:      func(*os.File, *os.File, *os.File, string, string) (bool, error) { return false, nil },
			secureClaim: func(*os.File) error { return nil },
			syncFile:    func(*os.File) error { return nil },
			syncDir:     func(*os.File) error { return nil },
			closeFile:   func(*os.File) error { return nil },
			read:        func(*os.File, int64) ([]byte, error) { return nil, nil },
		},
	}
}

// testPaths builds the exact machinepaths graph beneath one test root.
func testPaths(root string) machinepaths.Paths {
	return machinepaths.Paths{
		Root:               root,
		StateDirectory:     filepath.Join(root, "state"),
		OwnershipPath:      filepath.Join(root, "state", "ownership.json"),
		HostProjectionPath: filepath.Join(root, "state", "host-projection.json"),
		ReplayDirectory:    filepath.Join(root, "state", "replay"),
		TicketsDirectory:   filepath.Join(root, "tickets"),
		PendingDirectory:   filepath.Join(root, "tickets", "pending"),
		ClaimsDirectory:    filepath.Join(root, "tickets", "claims"),
	}
}

// testRedeemerClock supplies deterministic trusted admission time.
type testRedeemerClock struct {
	now time.Time
}

// Now returns the test's fixed UTC instant.
func (clock testRedeemerClock) Now() time.Time {
	return clock.now
}

// testOwnershipObserver supplies controlled protected ownership outcomes for pure dependency tests.
type testOwnershipObserver struct {
	observation ownership.Observation
	claim       ownership.Observation
	err         error
	claimErr    error
	closeErr    error
	claimed     []ownership.Record
}

// Observe returns the configured protected-state outcome.
func (observer *testOwnershipObserver) Observe(context.Context) (ownership.Observation, error) {
	return observer.observation, observer.err
}

// Claim records the requested protected authority and returns the configured atomic-claim outcome.
func (observer *testOwnershipObserver) Claim(_ context.Context, record ownership.Record) (ownership.Observation, error) {
	observer.claimed = append(observer.claimed, record)
	return observer.claim, observer.claimErr
}

// Close returns the configured handle-release outcome.
func (observer *testOwnershipObserver) Close() error {
	return observer.closeErr
}

// testRedeemerKey derives deterministic signing material without fixture secrets.
func testRedeemerKey(marker byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = marker
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

// testRedeemerTicket builds one semantically valid signed authority fixture.
func testRedeemerTicket(now time.Time, requester string) helper.Ticket {
	return helper.Ticket{
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackIdentity,
		InstallationID:         "harbor-redeemer-test",
		RequesterIdentity:      requester,
		OwnershipGeneration:    7,
		OwnershipSchemaVersion: ownership.IdentitySchemaVersion,
		ApprovedPool:           "127.77.0.0/24",
		ApprovedAddress:        "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("a", 64),
		},
		ExpectedPreAssignment: &helper.ExpectedPreAssignment{
			Fingerprint:  strings.Repeat("b", 64),
			Requirements: []helper.SocketRequirement{},
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testRedeemerPoolTicket builds one generation-one exact-eight bootstrap authority fixture.
func testRedeemerPoolTicket(now time.Time, requester string) helper.Ticket {
	identities := make([]helper.ExpectedLoopbackIdentity, 0, 8)
	address := netip.MustParseAddr("127.77.0.8")
	for range 8 {
		identities = append(identities, helper.ExpectedLoopbackIdentity{
			Address: address.String(),
			ExpectedObservation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: strings.Repeat("a", 64),
			},
			ExpectedPreAssignment: &helper.ExpectedPreAssignment{
				Fingerprint:  strings.Repeat("b", 64),
				Requirements: []helper.SocketRequirement{},
			},
		})
		address = address.Next()
	}
	return helper.Ticket{
		Version:                helper.ProtocolVersion,
		Operation:              helper.OperationEnsureLoopbackPool,
		InstallationID:         "harbor-redeemer-test",
		RequesterIdentity:      requester,
		OwnershipGeneration:    1,
		OwnershipSchemaVersion: ownership.IdentitySchemaVersion,
		ApprovedPool:           "127.77.0.8/29",
		ExpectedLoopbackPool: &helper.ExpectedLoopbackPool{
			Identities: identities,
		},
		Nonce:     strings.Repeat("p", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testRedeemerResolverTicket builds one signed schema-2 resolver target suitable for transition admission.
func testRedeemerResolverTicket(now time.Time, requester string, operation helper.Operation) helper.Ticket {
	localhost := netip.MustParseAddr("127.0.0.1")
	dns := netip.AddrPortFrom(localhost, 25000)
	policy, err := networkpolicy.New(
		strings.Repeat("c", 64),
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{Advertised: dns, Bind: dns},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 80),
			Bind:       netip.AddrPortFrom(localhost, 25001),
		},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 443),
			Bind:       netip.AddrPortFrom(localhost, 25002),
		},
	)
	if err != nil {
		panic(err)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		panic(err)
	}
	return helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                operation,
		InstallationID:           "harbor-redeemer-test",
		RequesterIdentity:        requester,
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: fingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		ExpectedResolverObservation: &helper.ExpectedResolverObservation{
			Fingerprint: strings.Repeat("d", 64),
		},
		Nonce:     strings.Repeat("r", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testOwnershipObservation returns one canonical protected observation or stops the test on an invalid fixture.
func testOwnershipObservation(t *testing.T, record ownership.Record) ownership.Observation {
	t.Helper()
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	return ownership.Observation{Exists: true, Record: record, Fingerprint: fingerprint}
}

// testRedeemerTime returns the shared canonical UTC test instant.
func testRedeemerTime() time.Time {
	return time.Date(2026, time.July, 18, 15, 0, 0, 0, time.UTC)
}
