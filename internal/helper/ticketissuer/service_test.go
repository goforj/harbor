package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
)

var ticketIssuerTestAddress = netip.MustParseAddr("127.77.0.10")

// fixedClock supplies one deterministic instant to ticket construction and result validation.
type fixedClock struct {
	now time.Time
}

// Now returns the clock's immutable test instant.
func (clock fixedClock) Now() time.Time {
	return clock.now
}

// scriptedPlanSource returns one result per durable-plan read.
type scriptedPlanSource struct {
	plans  []Plan
	errors []error
	calls  int
}

// Resolve returns an isolated scripted plan or failure.
func (source *scriptedPlanSource) Resolve(context.Context, Request) (Plan, error) {
	index := source.calls
	source.calls++
	if index < len(source.errors) && source.errors[index] != nil {
		return Plan{}, source.errors[index]
	}
	if len(source.plans) == 0 {
		return Plan{}, errors.New("plan script is empty")
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return clonePlan(source.plans[index]), nil
}

// scriptedOwnershipObserver returns one result per confirmed ownership projection read.
type scriptedOwnershipObserver struct {
	observations []ownership.Observation
	errors       []error
	calls        int
}

// Observe returns the next confirmed ownership projection result.
func (observer *scriptedOwnershipObserver) Observe(context.Context) (ownership.Observation, error) {
	index := observer.calls
	observer.calls++
	if index < len(observer.errors) && observer.errors[index] != nil {
		return ownership.Observation{}, observer.errors[index]
	}
	if len(observer.observations) == 0 {
		return ownership.Observation{}, nil
	}
	if index >= len(observer.observations) {
		index = len(observer.observations) - 1
	}
	return observer.observations[index], nil
}

// staticKeyLoader returns one established signing key or fixed failure.
type staticKeyLoader struct {
	key   ed25519.PrivateKey
	err   error
	calls int
}

// closingKeyLoader adapts a scripted key loader to the default-opening lifecycle contract.
type closingKeyLoader struct {
	KeyLoader
	closeCalls int
}

// Close records release of one default-opened signing-key store.
func (loader *closingKeyLoader) Close() error {
	loader.closeCalls++
	return nil
}

// Load returns an isolated key so issuer code cannot mutate the fixture.
func (loader *staticKeyLoader) Load(context.Context) (ed25519.PrivateKey, error) {
	loader.calls++
	return append(ed25519.PrivateKey(nil), loader.key...), loader.err
}

// capturingPublisher records the exact authority passed to durable publication.
type capturingPublisher struct {
	reference helper.TicketReference
	err       error
	ticket    helper.Ticket
	key       ed25519.PrivateKey
	calls     int
}

// closingPublisher adapts a scripted publisher to the default-opening lifecycle contract.
type closingPublisher struct {
	Publisher
	closeCalls int
}

// Close records release of one default-opened pending-ticket publisher.
func (publisher *closingPublisher) Close() error {
	publisher.closeCalls++
	return nil
}

// Publish captures one ticket and returns the scripted durable outcome.
func (publisher *capturingPublisher) Publish(_ context.Context, ticket helper.Ticket, key ed25519.PrivateKey) (helper.TicketReference, error) {
	publisher.calls++
	publisher.ticket = ticket
	publisher.key = append(ed25519.PrivateKey(nil), key...)
	return publisher.reference, publisher.err
}

// staticLoopbackObserver returns one exact assignment observation or fixed failure.
type staticLoopbackObserver struct {
	observation loopback.Observation
	err         error
	calls       int
}

// Observe returns the scripted assignment facts after checking the requested address.
func (observer *staticLoopbackObserver) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.calls++
	if observer.observation.Address.IsValid() && observer.observation.Address != address {
		return loopback.Observation{}, errors.New("loopback fixture address mismatch")
	}
	return observer.observation, observer.err
}

// staticConflictObserver returns one pre-assignment observation or fixed failure.
type staticConflictObserver struct {
	observation      hostconflict.Observation
	err              error
	calls            int
	requester        string
	requestCandidate netip.Addr
	requirements     []hostconflict.SocketRequirement
}

// Observe captures the signed requester and immutable request before returning native facts.
func (observer *staticConflictObserver) Observe(_ context.Context, request hostconflict.Request, requester string) (hostconflict.Observation, error) {
	observer.calls++
	observer.requester = requester
	observer.requestCandidate = request.Candidate()
	observer.requirements = request.Requirements()
	return observer.observation, observer.err
}

// issuerFixture contains every independently replaceable ticket authority.
type issuerFixture struct {
	now       time.Time
	request   Request
	plan      Plan
	private   ed25519.PrivateKey
	owned     ownership.Observation
	plans     *scriptedPlanSource
	ownership *scriptedOwnershipObserver
	keys      *staticKeyLoader
	publisher *capturingPublisher
	loopback  *staticLoopbackObserver
	conflicts *staticConflictObserver
	service   *Service
}

// TestIssuePendingEnsureBindsEveryAuthority proves the successful absent-assignment issuance shape.
func TestIssuePendingEnsureBindsEveryAuthority(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	result, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := result.Validate(fixture.now); err != nil {
		t.Fatalf("Result.Validate() error = %v", err)
	}
	if result.OperationID != fixture.plan.OperationID || result.LeaseKey != fixture.plan.Lease.Key || result.Operation != fixture.plan.Mutation || result.Address != fixture.plan.Lease.Address {
		t.Fatalf("Issue() result = %#v", result)
	}
	if fixture.plans.calls != 2 || fixture.ownership.calls != 2 || fixture.keys.calls != 1 || fixture.loopback.calls != 1 || fixture.conflicts.calls != 1 || fixture.publisher.calls != 1 {
		t.Fatalf("issue calls = plans %d ownership %d keys %d loopback %d conflicts %d publisher %d", fixture.plans.calls, fixture.ownership.calls, fixture.keys.calls, fixture.loopback.calls, fixture.conflicts.calls, fixture.publisher.calls)
	}
	ticket := fixture.publisher.ticket
	if ticket.InstallationID != fixture.owned.Record.InstallationID || ticket.RequesterIdentity != fixture.owned.Record.OwnerIdentity || ticket.OwnershipGeneration != fixture.owned.Record.Generation || ticket.ApprovedPool != fixture.owned.Record.LoopbackPoolPrefix || ticket.ApprovedAddress != fixture.plan.Lease.Address.String() {
		t.Fatalf("published ticket ownership = %#v", ticket)
	}
	if ticket.ExpectedObservation.State != helper.ObservationAbsent || ticket.ExpectedPreAssignment == nil || len(ticket.ExpectedPreAssignment.Requirements) != 2 {
		t.Fatalf("published ticket observations = %#v / %#v", ticket.ExpectedObservation, ticket.ExpectedPreAssignment)
	}
	if ticket.ExpectedPreAssignment.Requirements[0] != (helper.SocketRequirement{Transport: helper.SocketTransportTCP4, Port: 443}) || ticket.ExpectedPreAssignment.Requirements[1] != (helper.SocketRequirement{Transport: helper.SocketTransportUDP4, Port: 53}) {
		t.Fatalf("published ticket requirements = %#v", ticket.ExpectedPreAssignment.Requirements)
	}
	if fixture.conflicts.requester != fixture.owned.Record.OwnerIdentity || fixture.conflicts.requestCandidate != fixture.plan.Lease.Address || !slicesEqualRequirements(fixture.conflicts.requirements, fixture.plan.Requirements) {
		t.Fatalf("conflict request = requester %q candidate %s requirements %#v", fixture.conflicts.requester, fixture.conflicts.requestCandidate, fixture.conflicts.requirements)
	}
	if ticket.Nonce != strings.Repeat("5a", ticketNonceBytes) || ticket.ExpiresAt != fixture.now.Add(ticketLifetime) {
		t.Fatalf("published nonce/expiry = %q / %s", ticket.Nonce, ticket.ExpiresAt)
	}
	if !bytes.Equal(fixture.publisher.key, fixture.private) {
		t.Fatal("publisher received a different signing key")
	}
}

// TestIssueOwnedOperationsSkipPreAssignment proves repair and release never apply absent-address route semantics.
func TestIssueOwnedOperationsSkipPreAssignment(t *testing.T) {
	for _, operation := range []helper.Operation{helper.OperationEnsureLoopbackIdentity, helper.OperationReleaseLoopbackIdentity} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newIssuerFixture(t, operation, LeaseActive)
			result, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request)
			if err != nil {
				t.Fatalf("Issue() error = %v", err)
			}
			if result.Operation != operation || fixture.conflicts.calls != 0 || fixture.publisher.ticket.ExpectedPreAssignment != nil || fixture.publisher.ticket.ExpectedObservation.State != helper.ObservationOwned {
				t.Fatalf("owned issue = result %#v ticket %#v conflicts %d", result, fixture.publisher.ticket, fixture.conflicts.calls)
			}
		})
	}
}

// TestIssueActiveHistoricalLeaseUsesCurrentMachineAuthority proves ownership rotation does not strand an older exact assignment.
func TestIssueActiveHistoricalLeaseUsesCurrentMachineAuthority(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationReleaseLoopbackIdentity, LeaseActive)
	fixture.plans.plans[0].Lease.Ownership.Generation = fixture.owned.Record.Generation - 1

	result, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.Operation != helper.OperationReleaseLoopbackIdentity ||
		fixture.publisher.ticket.OwnershipGeneration != fixture.owned.Record.Generation {
		t.Fatalf("historical release result/ticket = %#v / %#v", result, fixture.publisher.ticket)
	}
}

// TestIssueFailsBeforePublication covers every independent authority boundary used by the service.
func TestIssueFailsBeforePublication(t *testing.T) {
	sentinel := errors.New("issuer sentinel")
	tests := []struct {
		name     string
		mutate   func(*issuerFixture)
		contains string
	}{
		{name: "plan read", mutate: func(fixture *issuerFixture) { fixture.plans.errors = []error{sentinel} }, contains: "resolve approval plan"},
		{name: "invalid plan", mutate: func(fixture *issuerFixture) { fixture.plans.plans[0].OperationState = domain.OperationRunning }, contains: "invalid approval plan"},
		{name: "wrong plan operation", mutate: func(fixture *issuerFixture) { fixture.plans.plans[0].OperationID = "operation-other" }, contains: "requested identity"},
		{name: "ownership read", mutate: func(fixture *issuerFixture) { fixture.ownership.errors = []error{sentinel} }, contains: "observe machine ownership"},
		{name: "ownership absent", mutate: func(fixture *issuerFixture) { fixture.ownership.observations = []ownership.Observation{{}} }, contains: "not claimed"},
		{name: "ownership fingerprint", mutate: func(fixture *issuerFixture) { fixture.ownership.observations[0].Fingerprint = strings.Repeat("0", 64) }, contains: "fingerprint is invalid"},
		{name: "wrong requester", mutate: func(fixture *issuerFixture) {
			changed := fixture.owned
			changed.Record.OwnerIdentity = "2000"
			changed.Fingerprint = mustOwnershipFingerprint(t, changed.Record)
			fixture.ownership.observations = []ownership.Observation{changed}
		}, contains: "does not own"},
		{name: "lease owner", mutate: func(fixture *issuerFixture) { fixture.plans.plans[0].Lease.Ownership.Generation++ }, contains: "does not match machine ownership"},
		{name: "lease pool", mutate: func(fixture *issuerFixture) {
			fixture.plans.plans[0].Lease.Address = netip.MustParseAddr("127.78.0.10")
		}, contains: "outside the machine-owned pool"},
		{name: "key read", mutate: func(fixture *issuerFixture) { fixture.keys.err = sentinel }, contains: "load established signing key"},
		{name: "key malformed", mutate: func(fixture *issuerFixture) { fixture.keys.key = ed25519.PrivateKey("short") }, contains: "signing key is invalid"},
		{name: "key mismatch", mutate: func(fixture *issuerFixture) { fixture.keys.key = deterministicPrivateKey(9) }, contains: "does not match machine ownership"},
		{name: "loopback read", mutate: func(fixture *issuerFixture) { fixture.loopback.err = sentinel }, contains: "observe loopback assignment"},
		{name: "loopback state", mutate: func(fixture *issuerFixture) { fixture.loopback.observation = exactLoopbackObservation() }, contains: "state is"},
		{name: "loopback malformed", mutate: func(fixture *issuerFixture) { fixture.loopback.observation.Loopback.Index = 0 }, contains: "fingerprint loopback"},
		{name: "conflict read", mutate: func(fixture *issuerFixture) { fixture.conflicts.err = sentinel }, contains: "observe pre-assignment conflicts"},
		{name: "conflict unsafe", mutate: func(fixture *issuerFixture) {
			fixture.conflicts.observation = conflictingHostObservation(t, fixture.plan.Requirements)
		}, contains: "pre-assignment state is"},
		{name: "conflict malformed", mutate: func(fixture *issuerFixture) { fixture.conflicts.observation.Routes.Selected = nil }, contains: "classify pre-assignment"},
		{name: "entropy", mutate: func(fixture *issuerFixture) { fixture.service.entropy = errorReader{err: sentinel} }, contains: "generate nonce"},
		{name: "second plan read", mutate: func(fixture *issuerFixture) { fixture.plans.errors = []error{nil, sentinel} }, contains: "revalidate approval plan"},
		{name: "changed plan", mutate: func(fixture *issuerFixture) {
			changed := clonePlan(fixture.plan)
			changed.OperationRevision++
			fixture.plans.plans = []Plan{fixture.plan, changed}
		}, contains: "plan changed"},
		{name: "second ownership read", mutate: func(fixture *issuerFixture) { fixture.ownership.errors = []error{nil, sentinel} }, contains: "revalidate ownership"},
		{name: "changed ownership", mutate: func(fixture *issuerFixture) {
			changed := fixture.owned
			changed.Record.Generation++
			changed.Fingerprint = mustOwnershipFingerprint(t, changed.Record)
			fixture.ownership.observations = []ownership.Observation{fixture.owned, changed}
		}, contains: "durable lease does not match machine ownership"},
		{name: "publication", mutate: func(fixture *issuerFixture) { fixture.publisher.err = sentinel }, contains: "publish capability"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
			test.mutate(fixture)
			result, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Issue() = %#v, %v, want %q", result, err, test.contains)
			}
			if result != (Result{}) || fixture.publisher.calls != boolInt(test.name == "publication") {
				t.Fatalf("failed Issue() result/publisher = %#v / %d", result, fixture.publisher.calls)
			}
		})
	}
}

// TestIssueNeverReturnsUncertainPublishedReference keeps a possibly durable orphan from becoming launch authority.
func TestIssueNeverReturnsUncertainPublishedReference(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	fixture.publisher.reference = helper.TicketReference(strings.Repeat("b", 64))
	fixture.publisher.err = errors.New("durability uncertain")
	result, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request)
	if err == nil || result != (Result{}) || fixture.publisher.calls != 1 {
		t.Fatalf("Issue() = %#v, %v after %d publications", result, err, fixture.publisher.calls)
	}
}

// TestIssueCancellationAndCloseStayOutsideAuthorities covers lifecycle failures before any durable work.
func TestIssueCancellationAndCloseStayOutsideAuthorities(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.Issue(cancelled, fixture.owned.Record.OwnerIdentity, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(cancelled) error = %v", err)
	}
	closed := 0
	fixture.service.closeStore = func() error { closed++; return nil }
	if err := fixture.service.Close(); err != nil || closed != 1 {
		t.Fatalf("Close() error/count = %v/%d", err, closed)
	}
	if err := fixture.service.Close(); err != nil || closed != 1 {
		t.Fatalf("Close() replay error/count = %v/%d", err, closed)
	}
	if _, err := fixture.service.Issue(context.Background(), fixture.owned.Record.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Issue(closed) error = %v", err)
	}
	if fixture.plans.calls != 0 {
		t.Fatalf("closed/cancelled issuer resolved %d plans", fixture.plans.calls)
	}
}

// TestRequestPlanAndResultValidation covers malformed public contract shapes independently from host effects.
func TestRequestPlanAndResultValidation(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	invalidRequests := []Request{
		{},
		{OperationID: fixture.request.OperationID},
		{OperationID: " bad ", LeaseKey: fixture.request.LeaseKey},
	}
	for _, request := range invalidRequests {
		if err := request.Validate(); err == nil {
			t.Fatalf("Request.Validate(%#v) error = nil", request)
		}
	}

	planTests := []struct {
		name   string
		mutate func(*Plan)
	}{
		{name: "operation", mutate: func(plan *Plan) { plan.OperationID = "" }},
		{name: "zero revision", mutate: func(plan *Plan) { plan.OperationRevision = 0 }},
		{name: "large revision", mutate: func(plan *Plan) { plan.OperationRevision = domain.MaximumSequence + 1 }},
		{name: "state", mutate: func(plan *Plan) { plan.OperationState = domain.OperationRunning }},
		{name: "mutation", mutate: func(plan *Plan) { plan.Mutation = "install_anything" }},
		{name: "lease", mutate: func(plan *Plan) { plan.Lease.Address = netip.Addr{} }},
		{name: "mapped lease", mutate: func(plan *Plan) { plan.Lease.Address = netip.MustParseAddr("::ffff:127.77.0.10") }},
		{name: "pending release", mutate: func(plan *Plan) { plan.Mutation = helper.OperationReleaseLoopbackIdentity }},
		{name: "lease state", mutate: func(plan *Plan) { plan.LeaseState = "unknown" }},
		{name: "nil requirements", mutate: func(plan *Plan) { plan.Requirements = nil }},
		{name: "duplicate requirements", mutate: func(plan *Plan) { plan.Requirements = append(plan.Requirements, plan.Requirements[0]) }},
		{name: "unordered requirements", mutate: func(plan *Plan) {
			plan.Requirements[0], plan.Requirements[1] = plan.Requirements[1], plan.Requirements[0]
		}},
	}
	for _, test := range planTests {
		t.Run(test.name, func(t *testing.T) {
			plan := clonePlan(fixture.plan)
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatalf("Plan.Validate(%#v) error = nil", plan)
			}
		})
	}

	validResult := Result{
		OperationID: fixture.plan.OperationID,
		LeaseKey:    fixture.plan.Lease.Key,
		Reference:   fixture.publisher.reference,
		Operation:   fixture.plan.Mutation,
		Address:     fixture.plan.Lease.Address,
		ExpiresAt:   fixture.now.Add(time.Minute),
	}
	resultTests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "operation ID", mutate: func(result *Result) { result.OperationID = "" }},
		{name: "lease key", mutate: func(result *Result) { result.LeaseKey = identity.LeaseKey{} }},
		{name: "reference", mutate: func(result *Result) { result.Reference = "bad" }},
		{name: "operation", mutate: func(result *Result) { result.Operation = "bad" }},
		{name: "address", mutate: func(result *Result) { result.Address = netip.MustParseAddr("192.0.2.1") }},
		{name: "expired", mutate: func(result *Result) { result.ExpiresAt = fixture.now }},
		{name: "non UTC", mutate: func(result *Result) {
			result.ExpiresAt = fixture.now.In(time.FixedZone("offset", 3600)).Add(time.Minute)
		}},
		{name: "too long", mutate: func(result *Result) { result.ExpiresAt = fixture.now.Add(helper.MaxTicketLifetime + time.Second) }},
	}
	for _, test := range resultTests {
		t.Run("result "+test.name, func(t *testing.T) {
			result := validResult
			test.mutate(&result)
			if err := result.Validate(fixture.now); err == nil {
				t.Fatalf("Result.Validate(%#v) error = nil", result)
			}
		})
	}
}

// TestNewRejectsMissingAuthorities proves required collaborators fail before later use.
func TestNewRejectsMissingAuthorities(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	defer func() {
		if recover() == nil {
			t.Fatal("New() did not panic for a missing required dependency")
		}
	}()
	New(nil, fixture.ownership, fixture.keys, fixture.publisher, fixture.loopback, fixture.conflicts, fixedClock{now: fixture.now}, bytes.NewReader(nil))
}

// TestOpenDefaultRejectsMissingAuthorities avoids opening fixed stores for invalid composition.
func TestOpenDefaultRejectsMissingAuthorities(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	if _, err := OpenDefault(nil, fixture.ownership); err == nil {
		t.Fatal("OpenDefault(nil plans) error = nil")
	}
	if _, err := OpenDefault(fixture.plans, nil); err == nil {
		t.Fatal("OpenDefault(nil ownership) error = nil")
	}
}

// TestOpenDefaultOpensOnlyIssuerStores proves default issuance cannot open the protected ownership store.
func TestOpenDefaultOpensOnlyIssuerStores(t *testing.T) {
	fixture := newIssuerFixture(t, helper.OperationEnsureLoopbackIdentity, LeasePending)
	if _, err := openDefault(fixture.plans, fixture.ownership, defaultOpeners{}); err == nil || !strings.Contains(err.Error(), "openers are incomplete") {
		t.Fatalf("openDefault(incomplete) error = %v", err)
	}

	sentinel := errors.New("default issuer store sentinel")
	publisherOpens := 0
	if _, err := openDefault(fixture.plans, fixture.ownership, defaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) { return nil, sentinel },
		openPublisher: func() (defaultPublisherCloser, error) {
			publisherOpens++
			return nil, nil
		},
	}); !errors.Is(err, sentinel) || publisherOpens != 0 {
		t.Fatalf("openDefault(key failure) error/publisher opens = %v / %d", err, publisherOpens)
	}

	failedKeys := &closingKeyLoader{KeyLoader: fixture.keys}
	if _, err := openDefault(fixture.plans, fixture.ownership, defaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) { return failedKeys, nil },
		openPublisher: func() (defaultPublisherCloser, error) {
			return nil, sentinel
		},
	}); !errors.Is(err, sentinel) || failedKeys.closeCalls != 1 {
		t.Fatalf("openDefault(publisher failure) error/key closes = %v / %d", err, failedKeys.closeCalls)
	}

	keys := &closingKeyLoader{KeyLoader: fixture.keys}
	publisher := &closingPublisher{Publisher: fixture.publisher}
	keyOpens := 0
	publisherOpens = 0
	service, err := openDefault(fixture.plans, fixture.ownership, defaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			keyOpens++
			return keys, nil
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			publisherOpens++
			return publisher, nil
		},
	})
	if err != nil {
		t.Fatalf("openDefault() error = %v", err)
	}
	if keyOpens != 1 || publisherOpens != 1 || fixture.ownership.calls != 0 {
		t.Fatalf("openDefault() opens/ownership reads = keys %d publisher %d ownership %d", keyOpens, publisherOpens, fixture.ownership.calls)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := service.Close(); err != nil || keys.closeCalls != 1 || publisher.closeCalls != 1 {
		t.Fatalf("Close(replay) = %v, key/publisher closes %d/%d", err, keys.closeCalls, publisher.closeCalls)
	}
}

// errorReader returns one deterministic entropy failure.
type errorReader struct {
	err error
}

// Read returns the configured failure without writing destination bytes.
func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// newIssuerFixture builds one valid, fully safe issuance graph.
func newIssuerFixture(t *testing.T, operation helper.Operation, leaseState LeaseState) *issuerFixture {
	t.Helper()
	now := time.Date(2026, time.July, 18, 23, 0, 0, 0, time.UTC)
	privateKey := deterministicPrivateKey(7)
	record := ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     "harbor-test-installation",
		OwnerIdentity:      "1000",
		Generation:         7,
		LoopbackPoolPrefix: "127.77.0.0/24",
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
	}
	owned := ownership.Observation{Exists: true, Record: record, Fingerprint: mustOwnershipFingerprint(t, record)}
	leaseKey, err := identity.NewPrimaryKey("project-test")
	if err != nil {
		t.Fatal(err)
	}
	leaseOwnership, err := identity.NewOwnership(identity.InstallationID(record.InstallationID), record.Generation)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		OperationID:       "operation-test",
		OperationRevision: 11,
		OperationState:    domain.OperationRequiresApproval,
		Mutation:          operation,
		Lease: identity.Lease{
			Key:       leaseKey,
			Address:   ticketIssuerTestAddress,
			Ownership: leaseOwnership,
		},
		LeaseState: leaseState,
		Requirements: []hostconflict.SocketRequirement{
			{Transport: hostconflict.TransportTCP4, Port: 443},
			{Transport: hostconflict.TransportUDP4, Port: 53},
		},
	}
	loopbackObservation := absentLoopbackObservation()
	if leaseState == LeaseActive {
		loopbackObservation = exactLoopbackObservation()
	}
	conflictObservation := safeHostObservation(t, plan.Requirements)
	plans := &scriptedPlanSource{plans: []Plan{plan}}
	ownershipObserver := &scriptedOwnershipObserver{observations: []ownership.Observation{owned}}
	keys := &staticKeyLoader{key: privateKey}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	loopbackObserver := &staticLoopbackObserver{observation: loopbackObservation}
	conflicts := &staticConflictObserver{observation: conflictObservation}
	service := New(plans, ownershipObserver, keys, publisher, loopbackObserver, conflicts, fixedClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes*2)))
	return &issuerFixture{
		now: now, request: Request{OperationID: plan.OperationID, LeaseKey: plan.Lease.Key}, plan: plan,
		private: privateKey, owned: owned, plans: plans, ownership: ownershipObserver, keys: keys,
		publisher: publisher, loopback: loopbackObserver, conflicts: conflicts, service: service,
	}
}

// deterministicPrivateKey returns one valid Ed25519 identity from a repeated seed byte.
func deterministicPrivateKey(marker byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{marker}, ed25519.SeedSize))
}

// mustOwnershipFingerprint returns the canonical protected record digest or fails the test.
func mustOwnershipFingerprint(t *testing.T, record ownership.Record) string {
	t.Helper()
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// absentLoopbackObservation returns one valid assignment-free Linux fact set.
func absentLoopbackObservation() loopback.Observation {
	return loopback.Observation{
		Address: ticketIssuerTestAddress,
		Loopback: loopback.InterfaceFact{
			Name: "lo", Index: 1, Kind: loopback.InterfaceKindLinuxNative, NativeLoopback: true,
		},
		State:       loopback.StateAbsent,
		Assignments: []loopback.AssignmentFact{},
	}
}

// exactLoopbackObservation returns one valid permanent host-scoped Linux /32 fact set.
func exactLoopbackObservation() loopback.Observation {
	observation := absentLoopbackObservation()
	observation.Assignments = []loopback.AssignmentFact{{
		Address: ticketIssuerTestAddress, PrefixLength: 32, InterfaceName: "lo", InterfaceIndex: 1,
		NativeLoopback: true, InterfaceKind: loopback.InterfaceKindLinuxNative,
		Linux: &loopback.LinuxAssignmentFact{
			Scope: loopback.LinuxAddressScopeHost, Flags: 1 << 7, Label: "lo", AddressMatchesLocal: true,
			CacheInfoPresent: true, ValidLifetimeSeconds: ^uint32(0), PreferredLifetimeSeconds: ^uint32(0),
		},
	}}
	observation.State = loopback.StateExact
	return observation
}

// safeHostObservation returns one complete Linux baseline for the exact request.
func safeHostObservation(t *testing.T, requirements []hostconflict.SocketRequirement) hostconflict.Observation {
	t.Helper()
	request, err := hostconflict.NewPreAssignmentRequest(ticketIssuerTestAddress, requirements)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := hostconflict.NewLinuxScope(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	interfaceIdentity := hostconflict.InterfaceIdentity{Name: "lo", Index: 1}
	baseline := hostconflict.RouteFact{
		Destination: netip.MustParsePrefix("127.0.0.0/8"), Interface: interfaceIdentity,
		NativeLoopback: true, Normalization: hostconflict.RouteNormalizationDirect,
	}
	return hostconflict.Observation{
		Request: request,
		Scope:   scope,
		Loopback: hostconflict.LoopbackIdentity{
			Interface: interfaceIdentity,
			Kind:      hostconflict.LoopbackKindLinuxNative,
		},
		Routes:  hostconflict.RouteSnapshot{Complete: true, Selected: &baseline, Matching: []hostconflict.RouteFact{baseline}},
		Sockets: hostconflict.SocketSnapshot{Complete: true, Endpoints: []hostconflict.SocketFact{}},
		Policy: hostconflict.PolicyFacts{Linux: &hostconflict.LinuxPolicyFacts{
			Complete: true,
			RouteLocalnet: []hostconflict.RouteLocalnetFact{{
				Interface: interfaceIdentity,
				Enabled:   false,
			}},
		}},
	}
}

// conflictingHostObservation adds one exact accepting socket to an otherwise safe observation.
func conflictingHostObservation(t *testing.T, requirements []hostconflict.SocketRequirement) hostconflict.Observation {
	t.Helper()
	observation := safeHostObservation(t, requirements)
	observation.Sockets.Endpoints = []hostconflict.SocketFact{{
		Protocol: hostconflict.SocketProtocolTCP, Address: ticketIssuerTestAddress, Port: 443,
		TCPAccepting: true, IPv6Only: hostconflict.IPv6OnlyNotApplicable,
	}}
	return observation
}

// slicesEqualRequirements compares canonical requirement slices without importing source-owned memory.
func slicesEqualRequirements(left []hostconflict.SocketRequirement, right []hostconflict.SocketRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// boolInt converts one expected single-call condition into its count.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ io.Reader = errorReader{}
