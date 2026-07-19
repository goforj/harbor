package state

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/null/v6"
)

// TestHelperApprovalPlanIntentValidationRejectsMalformedAuthority covers every independently invalid intent field.
func TestHelperApprovalPlanIntentValidationRejectsMalformedAuthority(t *testing.T) {
	valid := helperApprovalValidationIntent("", "127.77.0.10")
	tests := []struct {
		name   string
		mutate func(*HelperApprovalPlanIntent)
		want   string
	}{
		{name: "mutation", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Mutation = helper.Operation("arbitrary")
		}, want: "not allowlisted"},
		{name: "lease", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Key.ProjectID = ""
		}, want: "project ID"},
		{name: "mapped address", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Lease.Address = netip.MustParseAddr("::ffff:127.77.0.10")
		}, want: "canonical IPv4"},
		{name: "lease state", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.LeaseState = ticketissuer.LeaseState("retired")
		}, want: "unsupported"},
		{name: "pending release", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Mutation = helper.OperationReleaseLoopbackIdentity
		}, want: "requires an ensure"},
		{name: "requirements order", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Requirements = []hostconflict.SocketRequirement{
				{Transport: hostconflict.TransportUDP4, Port: 53},
				{Transport: hostconflict.TransportTCP4, Port: 443},
			}
		}, want: "canonical order"},
		{name: "socket requirement", mutate: func(intent *HelperApprovalPlanIntent) {
			intent.Requirements = []hostconflict.SocketRequirement{{Transport: hostconflict.Transport("sctp4"), Port: 443}}
		}, want: "transport"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := valid
			intent.Requirements = append([]hostconflict.SocketRequirement(nil), valid.Requirements...)
			test.mutate(&intent)
			if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestHelperApprovalPlanRecordValidationRejectsUnboundAuthority covers record identity and revision bounds.
func TestHelperApprovalPlanRecordValidationRejectsUnboundAuthority(t *testing.T) {
	valid := HelperApprovalPlanRecord{
		OperationID:       "operation-approval",
		OperationRevision: 4,
		Intent:            helperApprovalValidationIntent("", "127.77.0.10"),
	}
	tests := []struct {
		name   string
		mutate func(*HelperApprovalPlanRecord)
		want   string
	}{
		{name: "operation", mutate: func(record *HelperApprovalPlanRecord) {
			record.OperationID = ""
		}, want: "operation ID"},
		{name: "revision", mutate: func(record *HelperApprovalPlanRecord) {
			record.OperationRevision = 0
		}, want: "must be positive"},
		{name: "intent", mutate: func(record *HelperApprovalPlanRecord) {
			record.Intent.Mutation = helper.Operation("arbitrary")
		}, want: "not allowlisted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := record.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestStageProjectNetworkReleaseApprovalRequestValidationRejectsMalformedLifecycle covers staging metadata before writer admission.
func TestStageProjectNetworkReleaseApprovalRequestValidationRejectsMalformedLifecycle(t *testing.T) {
	valid := StageProjectNetworkReleaseApprovalRequest{
		OperationID:               "operation-approval",
		ExpectedOperationRevision: 4,
		Phase:                     "waiting for host approval",
		At:                        time.Date(2026, time.July, 19, 1, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*StageProjectNetworkReleaseApprovalRequest)
		want   string
	}{
		{name: "operation", mutate: func(request *StageProjectNetworkReleaseApprovalRequest) {
			request.OperationID = ""
		}, want: "operation ID"},
		{name: "revision", mutate: func(request *StageProjectNetworkReleaseApprovalRequest) {
			request.ExpectedOperationRevision = 0
		}, want: "must be positive"},
		{name: "phase", mutate: func(request *StageProjectNetworkReleaseApprovalRequest) {
			request.Phase = " "
		}, want: "phase"},
		{name: "time", mutate: func(request *StageProjectNetworkReleaseApprovalRequest) {
			request.At = time.Time{}
		}, want: "time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectNetworkReleaseApprovalResultValidationRejectsInconsistentReadback covers aggregate lifecycle, ownership, order, and effect checks.
func TestProjectNetworkReleaseApprovalResultValidationRejectsInconsistentReadback(t *testing.T) {
	primaryIntent := helperApprovalValidationIntent("", "127.77.0.10")
	primaryIntent.Mutation = helper.OperationReleaseLoopbackIdentity
	primaryIntent.LeaseState = ticketissuer.LeaseActive
	primary := HelperApprovalPlanRecord{
		OperationID:       "operation-approval",
		OperationRevision: 4,
		Intent:            primaryIntent,
		leaseID:           null.IntFrom(41),
	}
	secondaryIntent := helperApprovalValidationIntent("search", "127.77.0.11")
	secondaryIntent.Mutation = helper.OperationReleaseLoopbackIdentity
	secondaryIntent.LeaseState = ticketissuer.LeaseActive
	secondary := HelperApprovalPlanRecord{
		OperationID:       primary.OperationID,
		OperationRevision: primary.OperationRevision,
		Intent:            secondaryIntent,
		leaseID:           null.IntFrom(42),
	}
	valid := ProjectNetworkReleaseApprovalResult{
		Operation: OperationRecord{
			Operation: domain.Operation{ID: primary.OperationID, State: domain.OperationRequiresApproval},
			Revision:  primary.OperationRevision,
		},
		Plans: []HelperApprovalPlanRecord{primary, secondary},
	}
	tests := []struct {
		name   string
		mutate func(*ProjectNetworkReleaseApprovalResult)
		want   string
	}{
		{name: "state", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Operation.Operation.State = domain.OperationRunning
		}, want: "state"},
		{name: "empty", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans = nil
		}, want: "at least one"},
		{name: "invalid plan", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans[0].OperationID = ""
		}, want: "operation ID"},
		{name: "operation mismatch", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans[0].OperationID = "operation-other"
		}, want: "does not match"},
		{name: "effect", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans[0].Intent.Mutation = helper.OperationEnsureLoopbackIdentity
		}, want: "non-release"},
		{name: "order", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans[0], result.Plans[1] = result.Plans[1], result.Plans[0]
		}, want: "canonical order"},
		{name: "address", mutate: func(result *ProjectNetworkReleaseApprovalResult) {
			result.Plans[1].Intent.Lease.Address = result.Plans[0].Intent.Lease.Address
		}, want: "duplicated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			result.Plans = append([]HelperApprovalPlanRecord(nil), valid.Plans...)
			test.mutate(&result)
			if err := result.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestResumeProjectNetworkReleaseApprovalRequestValidationRejectsMalformedLifecycle covers every resumption metadata constraint.
func TestResumeProjectNetworkReleaseApprovalRequestValidationRejectsMalformedLifecycle(t *testing.T) {
	valid := ResumeProjectNetworkReleaseApprovalRequest{
		OperationID:               "operation-approval",
		ExpectedOperationRevision: 4,
		Phase:                     "host approval completed",
		At:                        time.Date(2026, time.July, 19, 1, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*ResumeProjectNetworkReleaseApprovalRequest)
		want   string
	}{
		{name: "operation", mutate: func(request *ResumeProjectNetworkReleaseApprovalRequest) {
			request.OperationID = ""
		}, want: "operation ID"},
		{name: "revision", mutate: func(request *ResumeProjectNetworkReleaseApprovalRequest) {
			request.ExpectedOperationRevision = 0
		}, want: "must be positive"},
		{name: "phase", mutate: func(request *ResumeProjectNetworkReleaseApprovalRequest) {
			request.Phase = " "
		}, want: "phase"},
		{name: "time", mutate: func(request *ResumeProjectNetworkReleaseApprovalRequest) {
			request.At = time.Time{}
		}, want: "time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestValidateHelperApprovalLeaseOwnershipPreservesHistoricalReleases covers current and prior-generation authority separately.
func TestValidateHelperApprovalLeaseOwnershipPreservesHistoricalReleases(t *testing.T) {
	current := identity.Ownership{InstallationID: "installation-test", Generation: 4}
	if err := validateHelperApprovalLeaseOwnership(ticketissuer.LeasePending, current, current); err != nil {
		t.Fatalf("pending current ownership error = %v", err)
	}
	historical := current
	historical.Generation--
	if err := validateHelperApprovalLeaseOwnership(ticketissuer.LeaseActive, historical, current); err != nil {
		t.Fatalf("active historical ownership error = %v", err)
	}

	tests := []struct {
		name       string
		leaseState ticketissuer.LeaseState
		ownership  identity.Ownership
		want       string
	}{
		{name: "installation", leaseState: ticketissuer.LeaseActive, ownership: identity.Ownership{InstallationID: "installation-other", Generation: 3}, want: "different Harbor installation"},
		{name: "pending generation", leaseState: ticketissuer.LeasePending, ownership: historical, want: "current ownership generation"},
		{name: "future active generation", leaseState: ticketissuer.LeaseActive, ownership: identity.Ownership{InstallationID: current.InstallationID, Generation: 5}, want: "newer"},
		{name: "state", leaseState: ticketissuer.LeaseState("retired"), ownership: current, want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateHelperApprovalLeaseOwnership(test.leaseState, test.ownership, current); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateHelperApprovalLeaseOwnership() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestHelperApprovalPlanRecordConversionRejectsCorruptRows covers every independently persisted plan authority field.
func TestHelperApprovalPlanRecordConversionRejectsCorruptRows(t *testing.T) {
	valid := helperApprovalValidationPlanModel()
	tests := []struct {
		name   string
		mutate func(*models.HelperApprovalPlan, *[]hostconflict.SocketRequirement)
		want   string
	}{
		{name: "database ID", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.Id = 0
		}, want: "database ID"},
		{name: "operation", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.OperationId = ""
		}, want: "operation ID"},
		{name: "revision", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.OperationRevision = 0
		}, want: "must be positive"},
		{name: "network", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.NetworkStateId = 2
		}, want: "network state ID"},
		{name: "lease key", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.Kind = "tertiary"
		}, want: "lease kind"},
		{name: "address", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.Address = "192.0.2.1"
		}, want: "IPv4 loopback"},
		{name: "generation", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.OwnershipGeneration = 0
		}, want: "must be positive"},
		{name: "ownership", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.OwnershipInstallationId = ""
		}, want: "installation ID"},
		{name: "intent", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.Mutation = "arbitrary"
		}, want: "not allowlisted"},
		{name: "requirements", mutate: func(_ *models.HelperApprovalPlan, requirements *[]hostconflict.SocketRequirement) {
			*requirements = nil
		}, want: "initialized"},
		{name: "pending lease", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.LoopbackAddressLeaseId = null.IntFrom(41)
		}, want: "pending plan references"},
		{name: "active lease", mutate: func(row *models.HelperApprovalPlan, _ *[]hostconflict.SocketRequirement) {
			row.LeaseState = string(ticketissuer.LeaseActive)
		}, want: "no positive lease"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			requirements := []hostconflict.SocketRequirement{}
			test.mutate(&row, &requirements)
			if _, err := helperApprovalPlanRecordFromModel(row, requirements); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("helperApprovalPlanRecordFromModel() error = %v, want containing %q", err, test.want)
			}
		})
	}

	active := valid
	active.LeaseState = string(ticketissuer.LeaseActive)
	active.LoopbackAddressLeaseId = null.IntFrom(41)
	record, err := helperApprovalPlanRecordFromModel(active, []hostconflict.SocketRequirement{})
	if err != nil {
		t.Fatalf("helperApprovalPlanRecordFromModel() active error = %v", err)
	}
	if record.leaseID != active.LoopbackAddressLeaseId {
		t.Fatalf("active lease ID = %#v, want %#v", record.leaseID, active.LoopbackAddressLeaseId)
	}
}

// TestHelperApprovalLeaseFromActiveRowRejectsCorruptRows covers the exact durable lease conversion boundary.
func TestHelperApprovalLeaseFromActiveRowRejectsCorruptRows(t *testing.T) {
	valid := models.LoopbackAddressLease{
		Id:                      41,
		NetworkStateId:          networkStateSingletonID,
		ProjectId:               null.StringFrom("project-alpha"),
		SourceProjectId:         "project-alpha",
		Kind:                    string(identity.LeaseKindPrimary),
		Address:                 "127.77.0.10",
		State:                   "leased",
		OwnershipInstallationId: "installation-test",
		OwnershipGeneration:     1,
	}
	tests := []struct {
		name   string
		mutate func(*models.LoopbackAddressLease)
		want   string
	}{
		{name: "state", mutate: func(row *models.LoopbackAddressLease) { row.State = "released" }, want: "exact active"},
		{name: "project", mutate: func(row *models.LoopbackAddressLease) { row.ProjectId = null.String{} }, want: "exact active"},
		{name: "source project", mutate: func(row *models.LoopbackAddressLease) { row.SourceProjectId = "project-other" }, want: "exact active"},
		{name: "key", mutate: func(row *models.LoopbackAddressLease) { row.Kind = "tertiary" }, want: "lease kind"},
		{name: "address", mutate: func(row *models.LoopbackAddressLease) { row.Address = "192.0.2.1" }, want: "IPv4 loopback"},
		{name: "generation", mutate: func(row *models.LoopbackAddressLease) { row.OwnershipGeneration = 0 }, want: "must be positive"},
		{name: "ownership", mutate: func(row *models.LoopbackAddressLease) { row.OwnershipInstallationId = "" }, want: "installation ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			if _, err := helperApprovalLeaseFromActiveRow(row); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("helperApprovalLeaseFromActiveRow() error = %v, want containing %q", err, test.want)
			}
		})
	}
	if lease, err := helperApprovalLeaseFromActiveRow(valid); err != nil || lease.Key.ProjectID != "project-alpha" {
		t.Fatalf("helperApprovalLeaseFromActiveRow() = %#v, %v", lease, err)
	}
}

// TestHelperApprovalReadbackComparisonRequiresExactAuthority covers every aggregate equality dimension.
func TestHelperApprovalReadbackComparisonRequiresExactAuthority(t *testing.T) {
	operation := OperationRecord{
		Operation: domain.Operation{ID: "operation-approval"},
		Revision:  4,
	}
	prepared := []preparedHelperApprovalPlan{{
		Intent:  helperApprovalValidationIntent("", "127.77.0.10"),
		LeaseID: null.IntFrom(41),
	}}
	read := []HelperApprovalPlanRecord{{
		OperationID:       operation.Operation.ID,
		OperationRevision: operation.Revision,
		Intent:            prepared[0].Intent,
		leaseID:           prepared[0].LeaseID,
	}}
	if !sameHelperApprovalPlanReadback(read, operation, prepared) {
		t.Fatal("sameHelperApprovalPlanReadback() rejected exact authority")
	}
	if sameHelperApprovalPlanReadback(nil, operation, prepared) {
		t.Fatal("sameHelperApprovalPlanReadback() accepted a length mismatch")
	}

	tests := []struct {
		name   string
		mutate func(*HelperApprovalPlanRecord)
	}{
		{name: "operation", mutate: func(record *HelperApprovalPlanRecord) { record.OperationID = "operation-other" }},
		{name: "revision", mutate: func(record *HelperApprovalPlanRecord) { record.OperationRevision++ }},
		{name: "intent", mutate: func(record *HelperApprovalPlanRecord) {
			record.Intent.Mutation = helper.OperationReleaseLoopbackIdentity
		}},
		{name: "lease ID", mutate: func(record *HelperApprovalPlanRecord) { record.leaseID = null.IntFrom(42) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := append([]HelperApprovalPlanRecord(nil), read...)
			test.mutate(&candidate[0])
			if sameHelperApprovalPlanReadback(candidate, operation, prepared) {
				t.Fatal("sameHelperApprovalPlanReadback() accepted different authority")
			}
		})
	}
}

// TestCompareHelperApprovalIntentsDefinesTotalLeaseOrder covers every canonical ordering key.
func TestCompareHelperApprovalIntentsDefinesTotalLeaseOrder(t *testing.T) {
	base := helperApprovalValidationIntent("", "127.77.0.10")
	if compareHelperApprovalIntents(base, base) != 0 {
		t.Fatal("compareHelperApprovalIntents() did not consider equal intents equal")
	}
	tests := []struct {
		name  string
		left  HelperApprovalPlanIntent
		right HelperApprovalPlanIntent
	}{
		{name: "project", left: helperApprovalValidationIntent("", "127.77.0.10"), right: helperApprovalValidationIntent("", "127.77.0.10")},
		{name: "kind", left: helperApprovalValidationIntent("", "127.77.0.10"), right: helperApprovalValidationIntent("search", "127.77.0.11")},
		{name: "secondary", left: helperApprovalValidationIntent("mysql", "127.77.0.11"), right: helperApprovalValidationIntent("search", "127.77.0.12")},
		{name: "address", left: helperApprovalValidationIntent("search", "127.77.0.11"), right: helperApprovalValidationIntent("search", "127.77.0.12")},
	}
	tests[0].left.Lease.Key.ProjectID = "project-alpha"
	tests[0].right.Lease.Key.ProjectID = "project-beta"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if compareHelperApprovalIntents(test.left, test.right) >= 0 || compareHelperApprovalIntents(test.right, test.left) <= 0 {
				t.Fatal("compareHelperApprovalIntents() did not define reciprocal order")
			}
		})
	}
}

// TestHelperApprovalPersistenceHelpersRejectOutOfRangeRevisions covers durable integer bounds before any database query or write is attempted.
func TestHelperApprovalPersistenceHelpersRejectOutOfRangeRevisions(t *testing.T) {
	invalidOperation := OperationRecord{Revision: 0}
	if err := insertHelperApprovalPlans(nil, invalidOperation, nil); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("insertHelperApprovalPlans() error = %v, want revision bound", err)
	}
	if _, err := readHelperApprovalPlanRecordsInTransaction(nil, "operation-approval", 0); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("readHelperApprovalPlanRecordsInTransaction() error = %v, want revision bound", err)
	}
	if _, err := helperApprovalPlanIDsInTransaction(nil, "operation-approval", 0); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("helperApprovalPlanIDsInTransaction() error = %v, want revision bound", err)
	}
	validOperation := OperationRecord{Revision: 1}
	invalidOwnership := preparedHelperApprovalPlan{Intent: helperApprovalValidationIntent("", "127.77.0.10")}
	invalidOwnership.Intent.Lease.Ownership.Generation = 0
	if err := insertHelperApprovalPlans(nil, validOperation, []preparedHelperApprovalPlan{invalidOwnership}); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("insertHelperApprovalPlans() error = %v, want ownership bound", err)
	}
}

// helperApprovalValidationIntent returns one canonical pending ensure intent for pure validation tests.
func helperApprovalValidationIntent(secondaryID string, address string) HelperApprovalPlanIntent {
	return HelperApprovalPlanIntent{
		Mutation: helper.OperationEnsureLoopbackIdentity,
		Lease: identity.Lease{
			Key: identity.LeaseKey{
				ProjectID:   "project-alpha",
				SecondaryID: secondaryID,
			},
			Address: netip.MustParseAddr(address),
			Ownership: identity.Ownership{
				InstallationID: "installation-test",
				Generation:     1,
			},
		},
		LeaseState:   ticketissuer.LeasePending,
		Requirements: []hostconflict.SocketRequirement{},
	}
}

// helperApprovalValidationPlanModel returns one canonical pending approval row for conversion tests.
func helperApprovalValidationPlanModel() models.HelperApprovalPlan {
	return models.HelperApprovalPlan{
		Id:                      51,
		OperationId:             "operation-approval",
		OperationRevision:       4,
		NetworkStateId:          networkStateSingletonID,
		Mutation:                string(helper.OperationEnsureLoopbackIdentity),
		LeaseState:              string(ticketissuer.LeasePending),
		ProjectId:               "project-alpha",
		Kind:                    string(identity.LeaseKindPrimary),
		Address:                 "127.77.0.10",
		OwnershipInstallationId: "installation-test",
		OwnershipGeneration:     1,
	}
}
