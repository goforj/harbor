package state

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestProjectNetworkReleaseContractsAcceptCanonicalLifecycle verifies every public release boundary composes safely.
func TestProjectNetworkReleaseContractsAcceptCanonicalLifecycle(t *testing.T) {
	for _, state := range []ProjectNetworkReleaseState{
		ProjectNetworkReleaseReleasing,
		ProjectNetworkReleaseCompleted,
	} {
		if err := state.Validate(); err != nil {
			t.Fatalf("ProjectNetworkReleaseState(%q).Validate() error = %v", state, err)
		}
	}

	begin := releaseContractTestBeginRequest()
	if err := begin.Validate(); err != nil {
		t.Fatalf("BeginProjectNetworkReleaseRequest.Validate() error = %v", err)
	}
	complete := releaseContractTestCompleteRequest()
	if err := complete.Validate(); err != nil {
		t.Fatalf("CompleteProjectNetworkReleaseRequest.Validate() error = %v", err)
	}

	releasing := releaseContractTestRecord(false)
	if err := releasing.Validate(); err != nil {
		t.Fatalf("releasing ProjectNetworkReleaseRecord.Validate() error = %v", err)
	}
	completed := releaseContractTestRecord(true)
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed ProjectNetworkReleaseRecord.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		release  ProjectNetworkReleaseRecord
		complete bool
	}{
		{name: "releasing", release: releasing},
		{name: "completed", release: completed, complete: true},
	} {
		for _, replayed := range []bool{false, true} {
			result := ProjectNetworkReleaseMutationResult{
				Record:   releaseContractTestNetworkRecord(test.complete),
				Release:  test.release,
				Replayed: replayed,
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("%s result replayed=%t Validate() error = %v", test.name, replayed, err)
			}
		}
	}

	conflict := (&ProjectNetworkReleaseConflictError{
		ProjectID: "project-alpha", OperationID: "operation-unregister", Difference: "begin generation",
	}).Error()
	if !strings.Contains(conflict, "project-alpha") || !strings.Contains(conflict, "operation-unregister") || !strings.Contains(conflict, "begin generation") {
		t.Fatalf("ProjectNetworkReleaseConflictError.Error() = %q", conflict)
	}
	incomplete := (&ProjectNetworkReleaseIncompleteError{
		ProjectID: "project-alpha", OperationID: "operation-unregister", State: ProjectNetworkReleaseReleasing,
	}).Error()
	if !strings.Contains(incomplete, "not completed") {
		t.Fatalf("ProjectNetworkReleaseIncompleteError.Error() = %q", incomplete)
	}
	notFound := (&ProjectNetworkReleaseNotFoundError{
		ProjectID: "project-alpha", OperationID: "operation-unregister",
	}).Error()
	if !strings.Contains(notFound, "was not started") {
		t.Fatalf("ProjectNetworkReleaseNotFoundError.Error() = %q", notFound)
	}
	active := (&ProjectNetworkReleaseActiveError{
		ProjectID:   "project-alpha",
		OperationID: "operation-unregister",
		State:       ProjectNetworkReleaseReleasing,
		Action:      "update project state",
	}).Error()
	if !strings.Contains(active, "cannot update project state") || !strings.Contains(active, "operation-unregister") {
		t.Fatalf("ProjectNetworkReleaseActiveError.Error() = %q", active)
	}
}

// TestProjectNetworkReleaseCompletionRejectsCorruption covers every bounded completion fact.
func TestProjectNetworkReleaseCompletionRejectsCorruption(t *testing.T) {
	valid := *releaseContractTestRecord(true).Completion
	tests := []struct {
		name   string
		mutate func(*ProjectNetworkReleaseCompletion)
		want   string
	}{
		{name: "zero generation", mutate: func(value *ProjectNetworkReleaseCompletion) { value.Generation = 0 }, want: "generation must be positive"},
		{name: "generation overflow", mutate: func(value *ProjectNetworkReleaseCompletion) { value.Generation = recordTestOverflowUint() }, want: "database range"},
		{name: "zero time", mutate: func(value *ProjectNetworkReleaseCompletion) { value.CompletedAt = time.Time{} }, want: "completion time"},
		{name: "non UTC time", mutate: func(value *ProjectNetworkReleaseCompletion) {
			value.CompletedAt = value.CompletedAt.In(time.FixedZone("offset", 3600))
		}, want: "use UTC"},
		{name: "missing evidence", mutate: func(value *ProjectNetworkReleaseCompletion) { value.Evidence = " \n " }, want: "evidence is required"},
		{name: "evidence overflow", mutate: func(value *ProjectNetworkReleaseCompletion) {
			value.Evidence = strings.Repeat("x", maximumNetworkEvidenceLength+1)
		}, want: "evidence exceeds"},
		{name: "missing release set digest", mutate: func(value *ProjectNetworkReleaseCompletion) {
			value.ReleaseSetDigest = ""
		}, want: "release set digest"},
		{name: "short release set digest", mutate: func(value *ProjectNetworkReleaseCompletion) {
			value.ReleaseSetDigest = strings.Repeat("a", 63)
		}, want: "64 lowercase hexadecimal"},
		{name: "uppercase release set digest", mutate: func(value *ProjectNetworkReleaseCompletion) {
			value.ReleaseSetDigest = strings.Repeat("A", 64)
		}, want: "64 lowercase hexadecimal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			assertNetworkMutationValidationError(t, value.Validate(), test.want)
		})
	}
}

// TestProjectNetworkReleaseRecordRejectsCorruption protects restart recovery from contradictory hidden facts.
func TestProjectNetworkReleaseRecordRejectsCorruption(t *testing.T) {
	tests := []struct {
		name      string
		completed bool
		mutate    func(*ProjectNetworkReleaseRecord)
		want      string
	}{
		{name: "project", mutate: func(value *ProjectNetworkReleaseRecord) { value.ProjectID = " bad " }, want: "project ID"},
		{name: "operation", mutate: func(value *ProjectNetworkReleaseRecord) { value.OperationID = " bad operation " }, want: "operation ID"},
		{name: "state", mutate: func(value *ProjectNetworkReleaseRecord) { value.State = "cancelled" }, want: "unsupported"},
		{name: "begin generation", mutate: func(value *ProjectNetworkReleaseRecord) { value.BeginGeneration = 0 }, want: "generation must be positive"},
		{name: "begin generation overflow", mutate: func(value *ProjectNetworkReleaseRecord) { value.BeginGeneration = recordTestOverflowUint() }, want: "database range"},
		{name: "begin time", mutate: func(value *ProjectNetworkReleaseRecord) { value.BeganAt = time.Time{} }, want: "begin time"},
		{name: "nil leases", mutate: func(value *ProjectNetworkReleaseRecord) { value.ActiveLeases = nil }, want: "active leases must be initialized"},
		{name: "nil endpoints", mutate: func(value *ProjectNetworkReleaseRecord) { value.Endpoints = nil }, want: "endpoints must be initialized"},
		{name: "releasing completion", mutate: func(value *ProjectNetworkReleaseRecord) {
			completion := *releaseContractTestRecord(true).Completion
			value.Completion = &completion
		}, want: "must not contain completion"},
		{name: "foreign lease", mutate: func(value *ProjectNetworkReleaseRecord) { value.ActiveLeases[0].Lease.Key.ProjectID = "project-beta" }, want: "belongs to project"},
		{name: "future lease", mutate: func(value *ProjectNetworkReleaseRecord) {
			value.ActiveLeases[0].LeasedAt = value.BeganAt.Add(time.Second)
		}, want: "after the network mutation time"},
		{name: "lease order", mutate: func(value *ProjectNetworkReleaseRecord) {
			value.ActiveLeases[0], value.ActiveLeases[1] = value.ActiveLeases[1], value.ActiveLeases[0]
		}, want: "unique and ordered"},
		{name: "missing primary", mutate: func(value *ProjectNetworkReleaseRecord) {
			value.ActiveLeases = value.ActiveLeases[1:]
			value.Endpoints = []EndpointReservation{}
		}, want: "requires its primary lease"},
		{name: "foreign endpoint", mutate: func(value *ProjectNetworkReleaseRecord) { value.Endpoints[0].Key.ProjectID = "project-beta" }, want: "belongs to project"},
		{name: "unknown endpoint lease", mutate: func(value *ProjectNetworkReleaseRecord) {
			value.Endpoints[1].Identity = recordTestLeaseKeyPointer("project-alpha", "unknown")
		}, want: "unknown active lease"},
		{name: "endpoint lease address", mutate: func(value *ProjectNetworkReleaseRecord) {
			value.Endpoints[1].Public = netip.AddrPortFrom(netip.MustParseAddr("127.77.0.12"), value.Endpoints[1].Public.Port())
		}, want: "does not use its active lease address"},
		{name: "completed without facts", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) { value.Completion = nil }, want: "requires completion facts"},
		{name: "completed invalid facts", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) { value.Completion.Evidence = "" }, want: "evidence is required"},
		{name: "completed generation", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) { value.Completion.Generation = value.BeginGeneration }, want: "must exceed"},
		{name: "completed before begin", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) {
			value.Completion.CompletedAt = value.BeganAt.Add(-time.Second)
		}, want: "must not precede"},
		{name: "completed active lease", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) {
			value.ActiveLeases = releaseContractTestRecord(false).ActiveLeases[:1]
		}, want: "must not retain"},
		{name: "completed endpoint", completed: true, mutate: func(value *ProjectNetworkReleaseRecord) {
			value.Endpoints = releaseContractTestRecord(false).Endpoints[:1]
		}, want: "must not retain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneReleaseContractTestRecord(releaseContractTestRecord(test.completed))
			test.mutate(&value)
			assertNetworkMutationValidationError(t, value.Validate(), test.want)
		})
	}
}

// TestBeginProjectNetworkReleaseRequestRejectsCorruption covers three-owner optimistic staging input.
func TestBeginProjectNetworkReleaseRequestRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BeginProjectNetworkReleaseRequest)
		want   string
	}{
		{name: "project", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ProjectID = " bad " }, want: "project ID"},
		{name: "operation", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.OperationID = " bad operation " }, want: "operation ID"},
		{name: "network revision", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedNetworkRevision = 0 }, want: "network revision must be positive"},
		{name: "project revision", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedProjectRevision = 0 }, want: "project revision must be positive"},
		{name: "operation revision", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.ExpectedOperationRevision = 0 }, want: "operation revision must be positive"},
		{name: "shared revision", mutate: func(value *BeginProjectNetworkReleaseRequest) {
			value.ExpectedOperationRevision = value.ExpectedNetworkRevision
		}, want: "pairwise distinct"},
		{name: "begin generation", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.BeginGeneration = 0 }, want: "generation must be positive"},
		{name: "begin generation overflow", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.BeginGeneration = recordTestOverflowUint() }, want: "database range"},
		{name: "time", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.At = time.Time{} }, want: "begin time"},
		{name: "non UTC time", mutate: func(value *BeginProjectNetworkReleaseRequest) { value.At = value.At.In(time.FixedZone("offset", 3600)) }, want: "use UTC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := releaseContractTestBeginRequest()
			test.mutate(&value)
			assertNetworkMutationValidationError(t, value.Validate(), test.want)
		})
	}
}

// TestCompleteProjectNetworkReleaseRequestRejectsCorruption covers complete host-effect evidence before durable teardown.
func TestCompleteProjectNetworkReleaseRequestRejectsCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompleteProjectNetworkReleaseRequest)
		want   string
	}{
		{name: "project", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ProjectID = " bad " }, want: "project ID"},
		{name: "operation", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.OperationID = " bad operation " }, want: "operation ID"},
		{name: "shared revision", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.ExpectedProjectRevision = value.ExpectedNetworkRevision
		}, want: "pairwise distinct"},
		{name: "begin generation", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ExpectedBeginGeneration = 0 }, want: "generation must be positive"},
		{name: "completion generation", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.CompletionGeneration = 0 }, want: "generation must be positive"},
		{name: "completion generation order", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.CompletionGeneration = value.ExpectedBeginGeneration
		}, want: "must exceed"},
		{name: "time", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.At = time.Time{} }, want: "completion time"},
		{name: "evidence", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.ReleaseEvidence = " \t " }, want: "evidence is required"},
		{name: "evidence overflow", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.ReleaseEvidence = strings.Repeat("x", maximumNetworkEvidenceLength+1)
		}, want: "evidence exceeds"},
		{name: "nil releases", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.Releases = nil }, want: "releases must be initialized"},
		{name: "empty releases", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.Releases = []NetworkLeaseRelease{} }, want: "requires the project's primary"},
		{name: "invalid release", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.Releases[0].ReleaseGeneration = 0 }, want: "lease release 0"},
		{name: "foreign release", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.Releases[0].Lease.Key.ProjectID = "project-beta"
		}, want: "belongs to project"},
		{name: "future release", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.Releases[0].ReleasedAt = value.At.Add(time.Second)
			value.Releases[0].QuarantinedAt = value.At.Add(2 * time.Second)
		}, want: "after the network mutation time"},
		{name: "release order", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.Releases[0], value.Releases[1] = value.Releases[1], value.Releases[0]
		}, want: "unique and ordered"},
		{name: "duplicate address", mutate: func(value *CompleteProjectNetworkReleaseRequest) {
			value.Releases[1].Lease.Address = value.Releases[0].Lease.Address
		}, want: "address"},
		{name: "missing primary", mutate: func(value *CompleteProjectNetworkReleaseRequest) { value.Releases = value.Releases[1:] }, want: "requires the project's primary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := releaseContractTestCompleteRequest()
			value.Releases = slices.Clone(value.Releases)
			test.mutate(&value)
			assertNetworkMutationValidationError(t, value.Validate(), test.want)
		})
	}
}

// TestProjectNetworkReleaseMutationResultRejectsInconsistentProjection keeps suppression and completion claims exact.
func TestProjectNetworkReleaseMutationResultRejectsInconsistentProjection(t *testing.T) {
	t.Run("invalid network", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Record.Revision = 0
		assertNetworkMutationValidationError(t, result.Validate(), "mutation result")
	})
	t.Run("invalid release", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.BeginGeneration = 0
		assertNetworkMutationValidationError(t, result.Validate(), "mutation result")
	})
	t.Run("missing suppression", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Record.Reservations.SuppressedProjectIDs = []domain.ProjectID{}
		assertNetworkMutationValidationError(t, result.Validate(), "does not suppress")
	})
	t.Run("begin after revision", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.BeganAt = result.Record.UpdatedAt.Add(time.Second)
		for index := range result.Release.ActiveLeases {
			result.Release.ActiveLeases[index].LeasedAt = result.Release.BeganAt
		}
		assertNetworkMutationValidationError(t, result.Validate(), "begins after")
	})
	t.Run("completion after revision", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(true), Release: releaseContractTestRecord(true)}
		result.Release.Completion.CompletedAt = result.Record.UpdatedAt.Add(time.Second)
		assertNetworkMutationValidationError(t, result.Validate(), "completes after")
	})
	t.Run("releasing lease count", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.ActiveLeases = result.Release.ActiveLeases[:1]
		result.Release.Endpoints = result.Release.Endpoints[:1]
		assertNetworkMutationValidationError(t, result.Validate(), "inconsistent active leases")
	})
	t.Run("releasing lease ownership", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.ActiveLeases[0].Lease.Ownership.Generation++
		assertNetworkMutationValidationError(t, result.Validate(), "inconsistent active leases")
	})
	t.Run("hidden HTTP endpoint socket", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.Endpoints[0].Public = result.Record.Reservations.Listeners.HTTP.Advertised
		assertNetworkMutationValidationError(t, result.Validate(), "advertised HTTPS socket")
	})
	t.Run("hidden endpoint host collision", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(false), Release: releaseContractTestRecord(false)}
		result.Release.Endpoints[0].Host = result.Record.Reservations.Endpoints[0].Host
		assertNetworkMutationValidationError(t, result.Validate(), "collides with the public projection")
	})
	t.Run("hidden TCP endpoint socket collision", func(t *testing.T) {
		visible := []EndpointReservation{
			recordTestTCPEndpoint("visible.test", "project-beta", "mysql", "127.77.0.11:3306"),
		}
		hidden := []EndpointReservation{
			recordTestTCPEndpoint("hidden.test", "project-alpha", "mysql", "127.77.0.11:3306"),
		}
		assertNetworkMutationValidationError(
			t,
			validateProjectNetworkReleaseVisibility(recordTestListeners(), visible, hidden),
			"native endpoint socket",
		)
	})
	t.Run("hidden TCP endpoint shared socket collision", func(t *testing.T) {
		hidden := []EndpointReservation{
			recordTestTCPEndpoint("hidden.test", "project-alpha", "mysql", "127.0.0.1:443"),
		}
		assertNetworkMutationValidationError(
			t,
			validateProjectNetworkReleaseVisibility(recordTestListeners(), []EndpointReservation{}, hidden),
			"HTTPS listener",
		)
	})
	t.Run("completed active lease", func(t *testing.T) {
		result := ProjectNetworkReleaseMutationResult{Record: releaseContractTestNetworkRecord(true), Release: releaseContractTestRecord(true)}
		result.Record.Leases = append(result.Record.Leases, releaseContractTestRecord(false).ActiveLeases[0].Lease)
		result.Record.Leases = canonicalNetworkLeases(result.Record.Leases)
		result.Record.Quarantines = result.Record.Quarantines[1:]
		assertNetworkMutationValidationError(t, result.Validate(), "retains an active project lease")
	})

	resultType := reflect.TypeOf(ProjectNetworkReleaseMutationResult{})
	fields := make([]string, 0, resultType.NumField())
	for index := 0; index < resultType.NumField(); index++ {
		fields = append(fields, resultType.Field(index).Name)
	}
	if want := []string{"Record", "Release", "Replayed"}; !reflect.DeepEqual(fields, want) {
		t.Fatalf("ProjectNetworkReleaseMutationResult fields = %v, want %v", fields, want)
	}
}

// releaseContractTestBeginRequest returns one three-owner optimistic suppression request.
func releaseContractTestBeginRequest() BeginProjectNetworkReleaseRequest {
	return BeginProjectNetworkReleaseRequest{
		ProjectID:                 "project-alpha",
		OperationID:               "operation-unregister",
		ExpectedNetworkRevision:   30,
		ExpectedProjectRevision:   5,
		ExpectedOperationRevision: 29,
		BeginGeneration:           40,
		At:                        networkMutationTestTime(),
	}
}

// releaseContractTestCompleteRequest returns full helper evidence for the staged project's active leases.
func releaseContractTestCompleteRequest() CompleteProjectNetworkReleaseRequest {
	record := releaseContractTestRecord(false)
	completedAt := record.BeganAt.Add(4 * time.Minute)
	releases := make([]NetworkLeaseRelease, 0, len(record.ActiveLeases))
	for index, ensure := range record.ActiveLeases {
		releases = append(releases, releaseContractTestRelease(ensure, time.Duration(index+1)*time.Minute, completedAt))
	}
	return CompleteProjectNetworkReleaseRequest{
		ProjectID:                 record.ProjectID,
		OperationID:               record.OperationID,
		ExpectedNetworkRevision:   31,
		ExpectedProjectRevision:   5,
		ExpectedOperationRevision: 29,
		ExpectedBeginGeneration:   record.BeginGeneration,
		CompletionGeneration:      record.BeginGeneration + 1,
		Releases:                  releases,
		ReleaseEvidence:           "verified route withdrawal and host teardown",
		At:                        completedAt,
	}
}

// releaseContractTestRecord returns either recoverable staged facts or the completed tombstone projection.
func releaseContractTestRecord(completed bool) ProjectNetworkReleaseRecord {
	request := networkMutationTestInitializeRequest()
	record := ProjectNetworkReleaseRecord{
		ProjectID:       "project-alpha",
		OperationID:     "operation-unregister",
		State:           ProjectNetworkReleaseReleasing,
		BeginGeneration: 40,
		BeganAt:         request.At,
		ActiveLeases:    slices.Clone(request.Ensures[:2]),
		Endpoints: []EndpointReservation{
			request.Endpoints[0],
			request.Endpoints[2],
		},
	}
	record.Endpoints = canonicalEndpointReservations(record.Endpoints)
	if !completed {
		return record
	}
	record.State = ProjectNetworkReleaseCompleted
	record.ActiveLeases = []NetworkLeaseEnsure{}
	record.Endpoints = []EndpointReservation{}
	record.Completion = &ProjectNetworkReleaseCompletion{
		Generation:       record.BeginGeneration + 1,
		CompletedAt:      record.BeganAt.Add(4 * time.Minute),
		Evidence:         "verified route withdrawal and host teardown",
		ReleaseSetDigest: strings.Repeat("a", 64),
	}
	return record
}

// releaseContractTestRelease creates one exact release fact after staging and before completion.
func releaseContractTestRelease(ensure NetworkLeaseEnsure, offset time.Duration, completedAt time.Time) NetworkLeaseRelease {
	releasedAt := networkMutationTestTime().Add(offset)
	return NetworkLeaseRelease{
		Lease:             ensure.Lease,
		ReleaseGeneration: ensure.Generation + 1,
		ReleaseEvidence:   "verified address release",
		ReleasedAt:        releasedAt,
		QuarantinedAt:     releasedAt,
		ReuseAfter:        completedAt.Add(time.Hour),
		QuarantineReason:  "project unregister pending safe reuse",
	}
}

// releaseContractTestNetworkRecord returns the public network projection at either teardown boundary.
func releaseContractTestNetworkRecord(completed bool) NetworkRecord {
	request := networkMutationTestInitializeRequest()
	record := networkInitializationProjection(request, 31)
	record.Reservations.SuppressedProjectIDs = []domain.ProjectID{"project-alpha"}
	record.Reservations.Endpoints = []EndpointReservation{request.Endpoints[1]}
	if !completed {
		return record
	}
	record.Revision = 32
	record.UpdatedAt = request.At.Add(4 * time.Minute)
	record.Leases = []identity.Lease{request.Ensures[2].Lease}
	record.Quarantines = []identity.Quarantine{
		{Address: request.Ensures[0].Lease.Address, Reason: "project unregister pending safe reuse"},
		{Address: request.Ensures[1].Lease.Address, Reason: "project unregister pending safe reuse"},
	}
	return record
}

// cloneReleaseContractTestRecord prevents table mutations from sharing completion or endpoint identity pointers.
func cloneReleaseContractTestRecord(record ProjectNetworkReleaseRecord) ProjectNetworkReleaseRecord {
	record.ActiveLeases = slices.Clone(record.ActiveLeases)
	record.Endpoints = canonicalEndpointReservations(record.Endpoints)
	if record.Completion != nil {
		completion := *record.Completion
		record.Completion = &completion
	}
	return record
}
