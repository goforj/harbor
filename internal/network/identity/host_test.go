package identity

import (
	"context"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestInstallationIDValidation verifies the shared helper-facing installation identity contract.
func TestInstallationIDValidation(t *testing.T) {
	valid := []InstallationID{
		"a",
		"A0._-z",
		InstallationID(strings.Repeat("a", maximumInstallationIDLength)),
	}
	for _, installationID := range valid {
		if err := installationID.Validate(); err != nil {
			t.Fatalf("InstallationID(%q).Validate() error = %v", installationID, err)
		}
	}

	tests := []struct {
		name           string
		installationID InstallationID
		contains       string
	}{
		{name: "empty", contains: "required"},
		{name: "too long", installationID: InstallationID(strings.Repeat("a", maximumInstallationIDLength+1)), contains: "exceeds"},
		{name: "leading dot", installationID: ".harbor", contains: "start and end"},
		{name: "leading underscore", installationID: "_harbor", contains: "start and end"},
		{name: "trailing hyphen", installationID: "harbor-", contains: "start and end"},
		{name: "path punctuation", installationID: "harbor/local", contains: "outside ASCII"},
		{name: "other punctuation", installationID: "harbor+local", contains: "outside ASCII"},
		{name: "whitespace", installationID: "harbor local", contains: "outside ASCII"},
		{name: "non ASCII", installationID: "hárbor", contains: "outside ASCII"},
		{name: "invalid UTF-8", installationID: InstallationID(string([]byte{0xff})), contains: "ASCII"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.installationID.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("InstallationID(%q).Validate() error = %v, want substring %q", test.installationID, err, test.contains)
			}
		})
	}
}

// fakeHost proves the consumer-owned interfaces can be implemented independently.
type fakeHost struct{}

// Observe returns a bounded empty observation for interface conformance tests.
func (fakeHost) Observe(_ context.Context, _ ObserveRequest) (Observation, error) {
	return Observation{}, nil
}

// Probe returns the requested address and ports as available for interface conformance tests.
func (fakeHost) Probe(_ context.Context, request ProbeRequest) (ProbeResult, error) {
	result := ProbeResult{Address: request.Address.Unmap()}
	ports := slices.Clone(request.Ports)
	slices.Sort(ports)
	for _, port := range ports {
		result.Ports = append(result.Ports, PortProbe{Port: port, Available: true})
	}
	return result, nil
}

// Mutate echoes the semantic effect without executing an operating-system command.
func (fakeHost) Mutate(_ context.Context, mutation Mutation) (MutationResult, error) {
	return MutationResult{Action: mutation.Action, Lease: mutation.Lease, Changed: true}, nil
}

var (
	_ HostObserver = fakeHost{}
	_ HostProber   = fakeHost{}
	_ HostMutator  = fakeHost{}
)

// TestOwnershipValidation verifies that authority always includes an exact nonzero generation.
func TestOwnershipValidation(t *testing.T) {
	valid, err := NewOwnership("installation-a", 7)
	if err != nil {
		t.Fatalf("NewOwnership() error = %v", err)
	}
	if got, want := valid, (Ownership{InstallationID: "installation-a", Generation: 7}); got != want {
		t.Fatalf("ownership = %#v, want %#v", got, want)
	}

	tests := []struct {
		name           string
		installationID InstallationID
		generation     uint64
		contains       string
	}{
		{name: "empty installation", generation: 1, contains: "installation ID is required"},
		{name: "invalid installation", installationID: "installation a", generation: 1, contains: "outside ASCII"},
		{name: "zero generation", installationID: "installation-a", contains: "greater than zero"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewOwnership(test.installationID, test.generation)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("NewOwnership() error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

// TestLeaseKeyValidation verifies primary and stable named-secondary semantics.
func TestLeaseKeyValidation(t *testing.T) {
	primary := mustPrimary(t, "alpha")
	if primary.Kind() != LeaseKindPrimary {
		t.Fatalf("primary.Kind() = %q", primary.Kind())
	}
	secondary := mustSecondary(t, "alpha", "endpoint.database.primary.tcp")
	if secondary.Kind() != LeaseKindSecondary {
		t.Fatalf("secondary.Kind() = %q", secondary.Kind())
	}

	if _, err := NewSecondaryKey(domain.ProjectID("alpha"), ""); err == nil || !strings.Contains(err.Error(), "secondary ID is required") {
		t.Fatalf("empty NewSecondaryKey() error = %v", err)
	}
	if _, err := NewSecondaryKey(domain.ProjectID("alpha"), "bad secondary"); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("whitespace NewSecondaryKey() error = %v", err)
	}
	if _, err := NewSecondaryKey(domain.ProjectID("alpha"), "région-é"); err != nil {
		t.Fatalf("UTF-8 NewSecondaryKey() error = %v", err)
	}
	if _, err := NewSecondaryKey(domain.ProjectID("alpha"), strings.Repeat("s", maximumLeaseTokenLength)); err != nil {
		t.Fatalf("maximum-length NewSecondaryKey() error = %v", err)
	}
	if _, err := NewSecondaryKey(domain.ProjectID("alpha"), strings.Repeat("s", maximumLeaseTokenLength+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized NewSecondaryKey() error = %v", err)
	}
	if _, err := NewPrimaryKey(domain.ProjectID(" alpha")); err == nil || !strings.Contains(err.Error(), "project ID") {
		t.Fatalf("invalid NewPrimaryKey() error = %v", err)
	}
}

// TestLeaseQuarantineAndConflictValidation verifies every planner input remains tied to exact loopback candidates.
func TestLeaseQuarantineAndConflictValidation(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11")
	ownership := mustOwnership(t, "installation-a", 1)
	lease := Lease{Key: mustPrimary(t, "alpha"), Address: mustAddress(t, "127.77.0.10"), Ownership: ownership}
	if err := lease.Validate(); err != nil {
		t.Fatalf("Lease.Validate() error = %v", err)
	}
	lease.Address = mustAddress(t, "10.0.0.10")
	if err := lease.Validate(); err == nil || !strings.Contains(err.Error(), "not IPv4 loopback") {
		t.Fatalf("non-loopback Lease.Validate() error = %v", err)
	}
	lease = Lease{Key: LeaseKey{}, Address: mustAddress(t, "127.77.0.10"), Ownership: ownership}
	if err := lease.Validate(); err == nil || !strings.Contains(err.Error(), "project ID") {
		t.Fatalf("invalid-key Lease.Validate() error = %v", err)
	}
	lease = Lease{Key: mustPrimary(t, "alpha"), Address: mustAddress(t, "127.77.0.10"), Ownership: Ownership{InstallationID: "installation-a"}}
	if err := lease.Validate(); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("invalid-ownership Lease.Validate() error = %v", err)
	}

	if err := (Quarantine{Address: mustAddress(t, "127.77.0.10"), Reason: "cooldown"}).Validate(pool); err != nil {
		t.Fatalf("Quarantine.Validate() error = %v", err)
	}
	if err := (Quarantine{Address: mustAddress(t, "127.77.0.10")}).Validate(pool); err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("empty-reason Quarantine.Validate() error = %v", err)
	}
	if err := (Quarantine{Address: mustAddress(t, "127.78.0.10"), Reason: "outside"}).Validate(pool); err == nil || !strings.Contains(err.Error(), "not a pool candidate") {
		t.Fatalf("outside Quarantine.Validate() error = %v", err)
	}

	for _, kind := range []ConflictKind{ConflictKindAddress, ConflictKindListener, ConflictKindResolver, ConflictKindOwnership} {
		conflict := Conflict{Address: mustAddress(t, "127.77.0.10"), Kind: kind}
		if kind == ConflictKindListener {
			conflict.Port = 3306
		}
		if err := conflict.Validate(pool); err != nil {
			t.Fatalf("Conflict(%q).Validate() error = %v", kind, err)
		}
	}
	if err := (Conflict{Address: mustAddress(t, "127.78.0.10"), Kind: ConflictKindAddress}).Validate(pool); err == nil || !strings.Contains(err.Error(), "not a pool candidate") {
		t.Fatalf("outside Conflict.Validate() error = %v", err)
	}
	if err := (Conflict{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindListener}).Validate(pool); err == nil || !strings.Contains(err.Error(), "listener port is required") {
		t.Fatalf("missing-port Conflict.Validate() error = %v", err)
	}
}

// TestSemanticHostRequests verifies bounded observe, probe, and mutate contracts.
func TestSemanticHostRequests(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	ownership := mustOwnership(t, "installation-a", 1)
	observe := ObserveRequest{Pool: pool, Ownership: ownership}
	if err := observe.Validate(); err != nil {
		t.Fatalf("ObserveRequest.Validate() error = %v", err)
	}
	if err := (ObserveRequest{Ownership: ownership}).Validate(); err == nil {
		t.Fatal("zero-pool ObserveRequest.Validate() error = nil")
	}
	if err := (ObserveRequest{Pool: pool, Ownership: Ownership{InstallationID: "installation-a"}}).Validate(); err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("invalid-owner ObserveRequest.Validate() error = %v", err)
	}

	probe := ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{6379, 3306}}
	if err := probe.Validate(); err != nil {
		t.Fatalf("ProbeRequest.Validate() error = %v", err)
	}
	result, err := (fakeHost{}).Probe(context.Background(), probe)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got, want := result.Ports, []PortProbe{{Port: 3306, Available: true}, {Port: 6379, Available: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Probe() ports = %#v, want %#v", got, want)
	}

	lease := Lease{Key: mustPrimary(t, "alpha"), Address: mustAddress(t, "127.77.0.10"), Ownership: ownership}
	mutation := Mutation{Action: MutationActionEnsure, Pool: pool, Lease: lease}
	if err := mutation.Validate(); err != nil {
		t.Fatalf("Mutation.Validate() error = %v", err)
	}
	mutationResult, err := (fakeHost{}).Mutate(context.Background(), mutation)
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if mutationResult.Action != MutationActionEnsure || mutationResult.Lease != lease || !mutationResult.Changed {
		t.Fatalf("Mutate() result = %#v", mutationResult)
	}
}

// TestProbeRequestValidation verifies exact address and native-port requirements.
func TestProbeRequestValidation(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	tests := []struct {
		name     string
		request  ProbeRequest
		contains string
	}{
		{name: "invalid address", request: ProbeRequest{Pool: pool, Address: netip.Addr{}, Ports: []uint16{3306}}, contains: "not IPv4 loopback"},
		{name: "non-loopback address", request: ProbeRequest{Pool: pool, Address: mustAddress(t, "10.0.0.10"), Ports: []uint16{3306}}, contains: "not IPv4 loopback"},
		{name: "invalid pool", request: ProbeRequest{Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{3306}}, contains: "prefix is invalid"},
		{name: "outside pool", request: ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.11"), Ports: []uint16{3306}}, contains: "not a pool candidate"},
		{name: "no ports", request: ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10")}, contains: "at least one port"},
		{name: "zero port", request: ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{0}}, contains: "port zero"},
		{name: "duplicate port", request: ProbeRequest{Pool: pool, Address: mustAddress(t, "127.77.0.10"), Ports: []uint16{3306, 3306}}, contains: "duplicate port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

// TestMutationValidation verifies platform adapters receive only ensure or release effects.
func TestMutationValidation(t *testing.T) {
	pool := mustPool(t, "127.77.0.10")
	ownership := mustOwnership(t, "installation-a", 1)
	lease := Lease{Key: mustPrimary(t, "alpha"), Address: mustAddress(t, "127.77.0.10"), Ownership: ownership}
	for _, action := range []MutationAction{MutationActionEnsure, MutationActionRelease} {
		if err := (Mutation{Action: action, Pool: pool, Lease: lease}).Validate(); err != nil {
			t.Fatalf("Mutation(%q).Validate() error = %v", action, err)
		}
	}
	if err := (Mutation{Action: "replace", Pool: pool, Lease: lease}).Validate(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported Mutation.Validate() error = %v", err)
	}
	if err := (Mutation{Action: MutationActionEnsure, Lease: lease}).Validate(); err == nil || !strings.Contains(err.Error(), "prefix is invalid") {
		t.Fatalf("zero-pool Mutation.Validate() error = %v", err)
	}
	outside := lease
	outside.Address = mustAddress(t, "127.77.0.11")
	if err := (Mutation{Action: MutationActionEnsure, Pool: pool, Lease: outside}).Validate(); err == nil || !strings.Contains(err.Error(), "not a pool candidate") {
		t.Fatalf("outside-pool Mutation.Validate() error = %v", err)
	}
}
