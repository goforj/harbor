package ticketredeemer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketauth"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
)

// TestValidateLayoutRequiresTheFixedShape rejects every path dimension that could redirect elevated I/O.
func TestValidateLayoutRequiresTheFixedShape(t *testing.T) {
	valid := testPaths(filepath.Join(string(filepath.Separator), "tmp", "harbor-redeemer-layout"))
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
		openOwnership: func(string) (ownershipObserver, error) {
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
		Root:             root,
		StateDirectory:   filepath.Join(root, "state"),
		OwnershipPath:    filepath.Join(root, "state", "ownership.json"),
		ReplayDirectory:  filepath.Join(root, "state", "replay"),
		TicketsDirectory: filepath.Join(root, "tickets"),
		PendingDirectory: filepath.Join(root, "tickets", "pending"),
		ClaimsDirectory:  filepath.Join(root, "tickets", "claims"),
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
	err         error
	closeErr    error
}

// Observe returns the configured protected-state outcome.
func (observer *testOwnershipObserver) Observe(context.Context) (ownership.Observation, error) {
	return observer.observation, observer.err
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
		Version:             helper.ProtocolVersion,
		Operation:           helper.OperationEnsureLoopbackIdentity,
		InstallationID:      "harbor-redeemer-test",
		RequesterIdentity:   requester,
		OwnershipGeneration: 7,
		ApprovedPool:        "127.77.0.0/24",
		ApprovedAddress:     "127.77.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationAbsent,
			Fingerprint: strings.Repeat("a", 64),
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testRedeemerTime returns the shared canonical UTC test instant.
func testRedeemerTime() time.Time {
	return time.Date(2026, time.July, 18, 15, 0, 0, 0, time.UTC)
}
