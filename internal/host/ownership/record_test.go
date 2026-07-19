package ownership

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
)

// TestSchemaVersionsKeepIdentityClaimsCurrent pins the staged schema rollout independently from record validation.
func TestSchemaVersionsKeepIdentityClaimsCurrent(t *testing.T) {
	t.Parallel()
	if IdentitySchemaVersion != 1 {
		t.Fatalf("IdentitySchemaVersion = %d, want 1", IdentitySchemaVersion)
	}
	if NetworkPolicySchemaVersion != 2 {
		t.Fatalf("NetworkPolicySchemaVersion = %d, want 2", NetworkPolicySchemaVersion)
	}
	if CurrentSchemaVersion != IdentitySchemaVersion {
		t.Fatalf("CurrentSchemaVersion = %d, want identity schema %d", CurrentSchemaVersion, IdentitySchemaVersion)
	}
}

// TestRecordValidateAcceptsCanonicalUnixAndWindowsOwners proves both local IPC identity spellings persist unchanged.
func TestRecordValidateAcceptsCanonicalUnixAndWindowsOwners(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"0", "501", "4294967295", "S-1-5-18", "S-1-5-21-1-2-3-1001"} {
		record := testRecord()
		record.OwnerIdentity = owner
		if err := record.Validate(); err != nil {
			t.Errorf("Record.Validate() owner %q error = %v", owner, err)
		}
	}
}

// TestRecordAndHelperTicketAcceptLongestCanonicalWindowsSID keeps protected ownership and ticket admission aligned.
func TestRecordAndHelperTicketAcceptLongestCanonicalWindowsSID(t *testing.T) {
	t.Parallel()
	owner := "S-1-281474976710655" + strings.Repeat("-4294967295", maximumSIDSubauthorities)
	if len(owner) <= helper.MaximumInstallationIDLength || len(owner) > helper.MaximumRequesterIdentityLength {
		t.Fatalf("longest canonical SID length = %d, want within requester-only extended bound", len(owner))
	}

	record := testRecord()
	record.OwnerIdentity = owner
	if err := record.Validate(); err != nil {
		t.Fatalf("Record.Validate() longest canonical SID error = %v", err)
	}

	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := helper.Ticket{
		Version:                  helper.ProtocolVersion,
		Operation:                helper.OperationReleaseLoopbackIdentity,
		InstallationID:           record.InstallationID,
		RequesterIdentity:        owner,
		OwnershipGeneration:      record.Generation,
		OwnershipSchemaVersion:   record.SchemaVersion,
		NetworkPolicyFingerprint: record.NetworkPolicyFingerprint,
		ApprovedPool:             record.LoopbackPoolPrefix,
		ApprovedAddress:          "127.44.0.10",
		ExpectedObservation: helper.ExpectedObservation{
			State:       helper.ObservationOwned,
			Fingerprint: strings.Repeat("a", 64),
		},
		Nonce:     strings.Repeat("n", 32),
		ExpiresAt: now.Add(time.Minute),
	}
	if err := ticket.Validate(now); err != nil {
		t.Fatalf("Ticket.Validate() longest canonical SID error = %v", err)
	}
}

// TestRecordValidateRejectsNoncanonicalState covers every persisted ownership dimension before fingerprinting or storage.
func TestRecordValidateRejectsNoncanonicalState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Record)
		want   string
	}{
		{name: "old schema", mutate: func(record *Record) { record.SchemaVersion = 0 }, want: "schema version"},
		{name: "future schema", mutate: func(record *Record) { record.SchemaVersion = NetworkPolicySchemaVersion + 1 }, want: "schema version"},
		{name: "identity schema with network policy", mutate: func(record *Record) {
			record.NetworkPolicyFingerprint = strings.Repeat("a", 64)
		}, want: "must be empty"},
		{name: "network policy schema without fingerprint", mutate: func(record *Record) {
			record.SchemaVersion = NetworkPolicySchemaVersion
		}, want: "64 lowercase hexadecimal"},
		{name: "network policy schema with short fingerprint", mutate: func(record *Record) {
			record.SchemaVersion = NetworkPolicySchemaVersion
			record.NetworkPolicyFingerprint = strings.Repeat("a", 63)
		}, want: "64 lowercase hexadecimal"},
		{name: "network policy schema with long fingerprint", mutate: func(record *Record) {
			record.SchemaVersion = NetworkPolicySchemaVersion
			record.NetworkPolicyFingerprint = strings.Repeat("a", 65)
		}, want: "64 lowercase hexadecimal"},
		{name: "network policy schema with uppercase fingerprint", mutate: func(record *Record) {
			record.SchemaVersion = NetworkPolicySchemaVersion
			record.NetworkPolicyFingerprint = strings.Repeat("A", 64)
		}, want: "64 lowercase hexadecimal"},
		{name: "network policy schema with nonhex fingerprint", mutate: func(record *Record) {
			record.SchemaVersion = NetworkPolicySchemaVersion
			record.NetworkPolicyFingerprint = strings.Repeat("g", 64)
		}, want: "64 lowercase hexadecimal"},
		{name: "missing installation", mutate: func(record *Record) { record.InstallationID = "" }, want: "installation ID is required"},
		{name: "long installation", mutate: func(record *Record) {
			record.InstallationID = strings.Repeat("a", helper.MaximumInstallationIDLength+1)
		}, want: "installation ID exceeds"},
		{name: "path installation", mutate: func(record *Record) { record.InstallationID = "../harbor" }, want: "must start and end"},
		{name: "punctuated installation", mutate: func(record *Record) { record.InstallationID = "harbor/install" }, want: "outside ASCII"},
		{name: "missing owner", mutate: func(record *Record) { record.OwnerIdentity = "" }, want: "owner identity is required"},
		{name: "long owner", mutate: func(record *Record) { record.OwnerIdentity = strings.Repeat("1", maximumOwnerIdentityLength+1) }, want: "owner identity exceeds"},
		{name: "signed uid", mutate: func(record *Record) { record.OwnerIdentity = "+501" }, want: "canonical unsigned UID"},
		{name: "leading-zero uid", mutate: func(record *Record) { record.OwnerIdentity = "0501" }, want: "canonical unsigned UID"},
		{name: "overflow uid", mutate: func(record *Record) { record.OwnerIdentity = "4294967296" }, want: "canonical unsigned UID"},
		{name: "lowercase sid", mutate: func(record *Record) { record.OwnerIdentity = "s-1-5-18" }, want: "canonical unsigned UID"},
		{name: "short sid", mutate: func(record *Record) { record.OwnerIdentity = "S-1-5" }, want: "canonical Windows SID"},
		{name: "sid revision", mutate: func(record *Record) { record.OwnerIdentity = "S-2-5-18" }, want: "canonical Windows SID"},
		{name: "leading-zero sid", mutate: func(record *Record) { record.OwnerIdentity = "S-1-05-18" }, want: "canonical Windows SID"},
		{name: "empty sid component", mutate: func(record *Record) { record.OwnerIdentity = "S-1-5--18" }, want: "canonical Windows SID"},
		{name: "sid authority overflow", mutate: func(record *Record) { record.OwnerIdentity = "S-1-281474976710656-18" }, want: "canonical Windows SID"},
		{name: "sid subauthority overflow", mutate: func(record *Record) { record.OwnerIdentity = "S-1-5-4294967296" }, want: "canonical Windows SID"},
		{name: "too many sid subauthorities", mutate: func(record *Record) { record.OwnerIdentity = "S-1-5-1-2-3-4-5-6-7-8-9-10-11-12-13-14-15-16" }, want: "canonical Windows SID"},
		{name: "zero generation", mutate: func(record *Record) { record.Generation = 0 }, want: "generation must be greater"},
		{name: "invalid pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "not-a-prefix" }, want: "is invalid"},
		{name: "ipv6 pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "::1/128" }, want: "not IPv4 loopback"},
		{name: "public pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "10.0.0.0/24" }, want: "not IPv4 loopback"},
		{name: "pool extends beyond loopback", mutate: func(record *Record) { record.LoopbackPoolPrefix = "127.0.0.0/7" }, want: "extends outside"},
		{name: "host bits pool", mutate: func(record *Record) { record.LoopbackPoolPrefix = "127.44.1.7/24" }, want: "not canonical"},
		{name: "missing verifier key", mutate: func(record *Record) { record.TicketVerifierKey = "" }, want: "want 32"},
		{name: "invalid verifier encoding", mutate: func(record *Record) { record.TicketVerifierKey = "%%%=" }, want: "canonical base64"},
		{name: "unpadded verifier encoding", mutate: func(record *Record) { record.TicketVerifierKey = strings.TrimRight(record.TicketVerifierKey, "=") }, want: "canonical base64"},
		{name: "short verifier key", mutate: func(record *Record) {
			record.TicketVerifierKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize-1))
		}, want: "want 32"},
		{name: "long verifier key", mutate: func(record *Record) {
			record.TicketVerifierKey = base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize+1))
		}, want: "want 32"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := testRecord()
			test.mutate(&record)
			err := record.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Record.Validate() error = %v, want substring %q", err, test.want)
			}
			if _, fingerprintErr := record.Fingerprint(); fingerprintErr == nil {
				t.Fatal("Record.Fingerprint() error = nil for invalid record")
			}
		})
	}
}

// TestRecordFingerprintIsDeterministicAndComplete pins canonical serialization and every security-relevant field.
func TestRecordFingerprintIsDeterministicAndComplete(t *testing.T) {
	t.Parallel()
	record := testRecord()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const wantJSON = `{"schema_version":1,"installation_id":"harbor-installation","owner_identity":"501","generation":7,"loopback_pool_prefix":"127.44.0.0/24","ticket_verifier_key":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="}`
	if string(encoded) != wantJSON {
		t.Fatalf("json.Marshal() = %q, want %q", encoded, wantJSON)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	if want := "49551235096f63f2e38ae45e77155528e01cb5d97eba435674932df647579f4a"; fingerprint != want {
		t.Fatalf("Record.Fingerprint() = %q, want %q", fingerprint, want)
	}
	second, err := record.Fingerprint()
	if err != nil || second != fingerprint {
		t.Fatalf("second Record.Fingerprint() = %q, %v, want %q", second, err, fingerprint)
	}

	mutations := []func(*Record){
		func(record *Record) { record.InstallationID = "harbor-other" },
		func(record *Record) { record.OwnerIdentity = "502" },
		func(record *Record) { record.Generation++ },
		func(record *Record) { record.LoopbackPoolPrefix = "127.45.0.0/24" },
		func(record *Record) { record.TicketVerifierKey = testVerifierKey(33) },
	}
	for index, mutate := range mutations {
		changed := record
		mutate(&changed)
		got, err := changed.Fingerprint()
		if err != nil {
			t.Fatalf("mutation %d Record.Fingerprint() error = %v", index, err)
		}
		if got == fingerprint {
			t.Fatalf("mutation %d retained fingerprint %q", index, got)
		}
	}
}

// TestRecordNetworkPolicyFingerprintIsCanonicalAndComplete pins schema-2 serialization and its policy binding.
func TestRecordNetworkPolicyFingerprintIsCanonicalAndComplete(t *testing.T) {
	t.Parallel()
	record := testNetworkPolicyRecord()
	if err := record.Validate(); err != nil {
		t.Fatalf("Record.Validate() error = %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const wantJSON = `{"schema_version":2,"installation_id":"harbor-installation","owner_identity":"501","generation":7,"loopback_pool_prefix":"127.44.0.0/24","network_policy_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ticket_verifier_key":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="}`
	if string(encoded) != wantJSON {
		t.Fatalf("json.Marshal() = %q, want %q", encoded, wantJSON)
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	const wantFingerprint = "f7bef2cc608c8794d3fbab2a246cc3e877f20e1ea7389da8163aaea075b76a0b"
	if fingerprint != wantFingerprint {
		t.Fatalf("Record.Fingerprint() = %q, want %q", fingerprint, wantFingerprint)
	}

	changed := record
	changed.NetworkPolicyFingerprint = strings.Repeat("b", 64)
	changedFingerprint, err := changed.Fingerprint()
	if err != nil {
		t.Fatalf("changed Record.Fingerprint() error = %v", err)
	}
	if changedFingerprint == fingerprint {
		t.Fatalf("changed Record.Fingerprint() retained %q", fingerprint)
	}
}

// testRecord returns one canonical claim shared by record and store behavior tests.
func testRecord() Record {
	return Record{
		SchemaVersion:      CurrentSchemaVersion,
		InstallationID:     "harbor-installation",
		OwnerIdentity:      "501",
		Generation:         7,
		LoopbackPoolPrefix: "127.44.0.0/24",
		TicketVerifierKey:  testVerifierKey(1),
	}
}

// testNetworkPolicyRecord returns one canonical claim bound to a distinguishable host-network policy.
func testNetworkPolicyRecord() Record {
	record := testRecord()
	record.SchemaVersion = NetworkPolicySchemaVersion
	record.NetworkPolicyFingerprint = strings.Repeat("a", 64)
	return record
}

// testVerifierKey returns distinguishable canonical public-key bytes without coupling tests to key generation entropy.
func testVerifierKey(first byte) string {
	key := make([]byte, ed25519.PublicKeySize)
	for index := range key {
		key[index] = first + byte(index)
	}
	return base64.StdEncoding.EncodeToString(key)
}
