package state

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// TestStoreReplaceProjectNetworkReallocatesReferencedLeaseAtomically verifies the complete FK-sensitive replacement path.
func TestStoreReplaceProjectNetworkReallocatesReferencedLeaseAtomically(t *testing.T) {
	store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	oldPrimary := networkReplaceTestLeaseRow(t, before.Leases, "project-alpha", "")
	retainedSecondary := networkReplaceTestLeaseRow(t, before.Leases, "project-alpha", "metrics")
	foreignLease := networkReplaceTestLeaseRow(t, before.Leases, "project-beta", "")
	oldWeb := networkReplaceTestEndpointRow(t, before.Endpoints, "project-alpha", "web")
	oldMySQL := networkReplaceTestEndpointRow(t, before.Endpoints, "project-alpha", "mysql")
	foreignEndpoint := networkReplaceTestEndpointRow(t, before.Endpoints, "project-beta", "web")

	result, err := store.ReplaceProjectNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("ReplaceProjectNetwork() error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 8 || !result.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("ReplaceProjectNetwork() result = %#v", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("NetworkMutationResult.Validate() error = %v", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
		t.Fatalf("Harbor sequence = %d, want 8", highWater)
	}

	after := networkReplaceTestRows(t, connection)
	quarantined := networkReplaceTestLeaseRowByID(t, after.Leases, oldPrimary.Id)
	if quarantined.State != "quarantined" || quarantined.ProjectId.Valid ||
		quarantined.ReleaseGeneration.Int64 != int64(request.Releases[0].ReleaseGeneration) ||
		quarantined.ReleaseEvidence.String != request.Releases[0].ReleaseEvidence ||
		quarantined.QuarantineReason.String != request.Releases[0].QuarantineReason ||
		quarantined.LeaseGeneration != oldPrimary.LeaseGeneration ||
		quarantined.EnsureEvidence != oldPrimary.EnsureEvidence ||
		!quarantined.LeasedAt.Equal(oldPrimary.LeasedAt) {
		t.Fatalf("quarantined primary = %#v", quarantined)
	}
	newPrimary := networkReplaceTestLeaseRow(t, after.Leases, "project-alpha", "")
	if newPrimary.Id == oldPrimary.Id || newPrimary.Address != "127.77.0.13" ||
		newPrimary.LeaseGeneration != int(request.Ensures[0].Generation) ||
		newPrimary.EnsureEvidence != request.Ensures[0].EnsureEvidence {
		t.Fatalf("new primary = %#v", newPrimary)
	}
	if got := networkReplaceTestLeaseRowByID(t, after.Leases, retainedSecondary.Id); !reflect.DeepEqual(got, retainedSecondary) {
		t.Fatalf("retained secondary changed: got %#v want %#v", got, retainedSecondary)
	}
	if got := networkReplaceTestLeaseRowByID(t, after.Leases, foreignLease.Id); !reflect.DeepEqual(got, foreignLease) {
		t.Fatalf("foreign lease changed: got %#v want %#v", got, foreignLease)
	}

	web := networkReplaceTestEndpointRow(t, after.Endpoints, "project-alpha", "web")
	mysql := networkReplaceTestEndpointRow(t, after.Endpoints, "project-alpha", "mysql")
	if web.Id != oldWeb.Id || !web.CreatedAt.Equal(initialization.At) || !web.UpdatedAt.Equal(initialization.At) {
		t.Fatalf("unchanged web lifecycle = %#v", web)
	}
	if mysql.Id != oldMySQL.Id || !mysql.CreatedAt.Equal(initialization.At) || !mysql.UpdatedAt.Equal(request.At) ||
		mysql.LoopbackAddressLeaseId.Int64 != int64(newPrimary.Id) {
		t.Fatalf("changed MySQL lifecycle = %#v", mysql)
	}
	if got := networkReplaceTestEndpointRow(t, after.Endpoints, "project-beta", "web"); !reflect.DeepEqual(got, foreignEndpoint) {
		t.Fatalf("foreign endpoint changed: got %#v want %#v", got, foreignEndpoint)
	}

	read, initialized, err := store.Network(context.Background())
	if err != nil || !initialized || !reflect.DeepEqual(read, result.Record) {
		t.Fatalf("Network() = %#v, %t, %v; want replacement result", read, initialized, err)
	}
}

// TestStoreReplaceProjectNetworkReplaysOnlySatisfiedDurableSemantics verifies replay direction and hidden fact matching.
func TestStoreReplaceProjectNetworkReplaysOnlySatisfiedDurableSemantics(t *testing.T) {
	t.Run("exact applied retry", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		first, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("first ReplaceProjectNetwork() error = %v", err)
		}
		beforeReplay := networkReplaceTestRows(t, connection)
		second, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("replayed ReplaceProjectNetwork() error = %v", err)
		}
		if !second.Replayed || !reflect.DeepEqual(second.Record, first.Record) {
			t.Fatalf("replayed result = %#v, first %#v", second, first)
		}
		if afterReplay := networkReplaceTestRows(t, connection); !reflect.DeepEqual(afterReplay, beforeReplay) {
			t.Fatal("semantic replay changed durable rows")
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
			t.Fatalf("Harbor sequence after replay = %d, want 8", highWater)
		}
	})

	t.Run("already satisfied no-op", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		request := networkReplaceTestNoopRequest(initialization)
		before := networkReplaceTestRows(t, connection)
		result, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("no-op ReplaceProjectNetwork() error = %v", err)
		}
		if !result.Replayed || result.Record.Revision != 7 {
			t.Fatalf("no-op result = %#v", result)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("no-op semantic replay changed durable rows")
		}
	})

	t.Run("hidden ensure mismatch after stale root", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		if _, err := store.ReplaceProjectNetwork(context.Background(), request); err != nil {
			t.Fatalf("seed replacement: %v", err)
		}
		request.Ensures[0].EnsureEvidence = "different sanitized evidence"
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var stale *NetworkRevisionConflictError
		if !errors.As(err, &stale) || stale.Expected != 7 || stale.Actual != 8 {
			t.Fatalf("hidden mismatch error = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("stale hidden mismatch changed durable rows")
		}
	})

	t.Run("future network owner cannot replay", func(t *testing.T) {
		store, _, _, initialization := newNetworkReplaceTestHarness(t, 1)
		request := networkReplaceTestNoopRequest(initialization)
		request.ExpectedNetworkRevision = 8
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var stale *NetworkRevisionConflictError
		if !errors.As(err, &stale) || stale.Expected != 8 || stale.Actual != 7 {
			t.Fatalf("future network replay error = %v", err)
		}
	})

	t.Run("future project owner cannot replay", func(t *testing.T) {
		store, _, _, initialization := newNetworkReplaceTestHarness(t, 1)
		request := networkReplaceTestNoopRequest(initialization)
		request.ExpectedProjectRevision = 8
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var stale *ProjectRevisionConflictError
		if !errors.As(err, &stale) || stale.Expected != 8 || stale.Actual != 5 {
			t.Fatalf("future project replay error = %v", err)
		}
	})
}

// TestStoreReplaceProjectNetworkPreservesEndpointLifecycle verifies full replacement does not manufacture update activity.
func TestStoreReplaceProjectNetworkPreservesEndpointLifecycle(t *testing.T) {
	store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	oldWeb := networkReplaceTestEndpointRow(t, before.Endpoints, "project-alpha", "web")
	oldMySQL := networkReplaceTestEndpointRow(t, before.Endpoints, "project-alpha", "mysql")
	foreign := networkReplaceTestEndpointRow(t, before.Endpoints, "project-beta", "web")
	at := initialization.At.Add(15 * time.Minute)
	changedWeb := initialization.Endpoints[0]
	changedWeb.Host = "app.alpha.test"
	changedWeb.Generation++
	unchangedMySQL := initialization.Endpoints[2]
	newStatus := recordTestHTTPEndpoint("status.alpha.test", "project-alpha", "status")
	request := ReplaceProjectNetworkRequest{
		ProjectID:               "project-alpha",
		ExpectedNetworkRevision: 7,
		ExpectedProjectRevision: 5,
		Ensures:                 []NetworkLeaseEnsure{},
		Releases:                []NetworkLeaseRelease{},
		Endpoints:               canonicalEndpointReservations([]EndpointReservation{changedWeb, unchangedMySQL, newStatus}),
		At:                      at,
	}
	result, err := store.ReplaceProjectNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("endpoint replacement error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 8 {
		t.Fatalf("endpoint replacement result = %#v", result)
	}
	after := networkReplaceTestRows(t, connection)
	web := networkReplaceTestEndpointRow(t, after.Endpoints, "project-alpha", "web")
	mysql := networkReplaceTestEndpointRow(t, after.Endpoints, "project-alpha", "mysql")
	status := networkReplaceTestEndpointRow(t, after.Endpoints, "project-alpha", "status")
	if web.Id != oldWeb.Id || !web.CreatedAt.Equal(initialization.At) || !web.UpdatedAt.Equal(at) {
		t.Fatalf("changed web lifecycle = %#v", web)
	}
	if mysql.Id != oldMySQL.Id || !mysql.CreatedAt.Equal(oldMySQL.CreatedAt) || !mysql.UpdatedAt.Equal(oldMySQL.UpdatedAt) {
		t.Fatalf("unchanged MySQL lifecycle = %#v", mysql)
	}
	if status.Id == oldWeb.Id || status.Id == oldMySQL.Id || !status.CreatedAt.Equal(at) || !status.UpdatedAt.Equal(at) {
		t.Fatalf("new status lifecycle = %#v", status)
	}
	if got := networkReplaceTestEndpointRow(t, after.Endpoints, "project-beta", "web"); !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign endpoint changed: got %#v want %#v", got, foreign)
	}
}

// TestStoreReplaceProjectNetworkAllocatesAndReusesOnlySafeAddresses covers free candidates and causal quarantine reuse.
func TestStoreReplaceProjectNetworkAllocatesAndReusesOnlySafeAddresses(t *testing.T) {
	t.Run("free candidate", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		request := networkReplaceTestNoopRequest(initialization)
		request.At = initialization.At.Add(10 * time.Minute)
		request.Ensures = []NetworkLeaseEnsure{networkReplaceTestEnsure("cache", "127.77.0.14", 30, request.At.Add(-time.Minute))}
		result, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("free ensure error = %v", err)
		}
		if result.Replayed || result.Record.Revision != 8 {
			t.Fatalf("free ensure result = %#v", result)
		}
		row := networkReplaceTestLeaseRow(t, networkReplaceTestRows(t, connection).Leases, "project-alpha", "cache")
		if row.Address != "127.77.0.14" || row.LeaseGeneration != 30 {
			t.Fatalf("free ensure row = %#v", row)
		}
	})

	t.Run("eligible quarantine", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		first := networkReplaceTestNoopRequest(initialization)
		first.At = initialization.At.Add(10 * time.Minute)
		first.Releases = []NetworkLeaseRelease{networkReplaceTestRelease(
			initialization.Ensures[1].Lease,
			31,
			first.At.Add(-3*time.Minute),
			first.At.Add(-2*time.Minute),
			first.At.Add(2*time.Minute),
		)}
		if _, err := store.ReplaceProjectNetwork(context.Background(), first); err != nil {
			t.Fatalf("release secondary error = %v", err)
		}
		quarantine := networkReplaceTestLeaseRow(t, networkReplaceTestRows(t, connection).Leases, "project-alpha", "metrics")
		second := networkReplaceTestNoopRequest(initialization)
		second.ExpectedNetworkRevision = 8
		second.At = first.At.Add(10 * time.Minute)
		second.Ensures = []NetworkLeaseEnsure{networkReplaceTestEnsure("cache", "127.77.0.12", 32, second.At.Add(-time.Minute))}
		result, err := store.ReplaceProjectNetwork(context.Background(), second)
		if err != nil {
			t.Fatalf("consume quarantine error = %v", err)
		}
		if result.Record.Revision != 9 {
			t.Fatalf("consume result revision = %d, want 9", result.Record.Revision)
		}
		consumed := networkReplaceTestLeaseRow(t, networkReplaceTestRows(t, connection).Leases, "project-alpha", "cache")
		if consumed.Id != quarantine.Id || consumed.Address != quarantine.Address || networkLeaseHasReleaseFields(consumed) {
			t.Fatalf("consumed quarantine = %#v, prior %#v", consumed, quarantine)
		}
	})

	t.Run("future quarantine", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		first := networkReplaceTestNoopRequest(initialization)
		first.At = initialization.At.Add(10 * time.Minute)
		first.Releases = []NetworkLeaseRelease{networkReplaceTestRelease(
			initialization.Ensures[1].Lease,
			31,
			first.At.Add(-3*time.Minute),
			first.At.Add(-2*time.Minute),
			first.At.Add(30*time.Minute),
		)}
		if _, err := store.ReplaceProjectNetwork(context.Background(), first); err != nil {
			t.Fatalf("release secondary error = %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		second := networkReplaceTestNoopRequest(initialization)
		second.ExpectedNetworkRevision = 8
		second.At = first.At.Add(10 * time.Minute)
		second.Ensures = []NetworkLeaseEnsure{networkReplaceTestEnsure("cache", "127.77.0.12", 32, second.At.Add(-time.Minute))}
		_, err := store.ReplaceProjectNetwork(context.Background(), second)
		assertNetworkReplaceConflict(t, err, "still quarantined")
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("future quarantine rejection changed durable rows")
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
			t.Fatalf("Harbor sequence after future quarantine = %d, want 8", highWater)
		}
	})

	t.Run("non-increasing reuse generation", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		first := networkReplaceTestNoopRequest(initialization)
		first.At = initialization.At.Add(10 * time.Minute)
		first.Releases = []NetworkLeaseRelease{networkReplaceTestRelease(
			initialization.Ensures[1].Lease,
			31,
			first.At.Add(-3*time.Minute),
			first.At.Add(-2*time.Minute),
			first.At.Add(time.Minute),
		)}
		if _, err := store.ReplaceProjectNetwork(context.Background(), first); err != nil {
			t.Fatalf("release secondary error = %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		second := networkReplaceTestNoopRequest(initialization)
		second.ExpectedNetworkRevision = 8
		second.At = first.At.Add(10 * time.Minute)
		second.Ensures = []NetworkLeaseEnsure{networkReplaceTestEnsure("cache", "127.77.0.12", 31, second.At.Add(-time.Minute))}
		_, err := store.ReplaceProjectNetwork(context.Background(), second)
		assertNetworkReplaceConflict(t, err, "must exceed quarantine release generation")
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("generation rewind rejection changed durable rows")
		}
	})
}

// TestStoreReplaceProjectNetworkRejectsCurrentStateConflicts verifies every host fact is bound to current durable ownership.
func TestStoreReplaceProjectNetworkRejectsCurrentStateConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ReplaceProjectNetworkRequest, InitializeNetworkRequest)
		want   string
	}{
		{name: "missing release key", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Releases[0].Lease.Key.SecondaryID = "missing"
		}, want: "has no active lease"},
		{name: "release address", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Releases[0].Lease.Address = netip.MustParseAddr("127.77.0.14")
		}, want: "does not match its active address ownership"},
		{name: "release ownership", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Releases[0].Lease.Ownership.Generation++
		}, want: "does not match its active address ownership"},
		{name: "release generation", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Releases[0].ReleaseGeneration = 20
		}, want: "must exceed active lease generation"},
		{name: "release time", mutate: func(request *ReplaceProjectNetworkRequest, initialization InitializeNetworkRequest) {
			request.Releases[0].ReleasedAt = initialization.Ensures[0].LeasedAt.Add(-time.Second)
			request.Releases[0].QuarantinedAt = request.Releases[0].ReleasedAt
			request.Releases[0].ReuseAfter = request.Releases[0].ReleasedAt.Add(time.Hour)
		}, want: "precede its active lease"},
		{name: "foreign installation", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Ensures[0].Lease.Ownership.InstallationID = "other-installation"
		}, want: "belongs to installation"},
		{name: "outside pool", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Ensures[0].Lease.Address = netip.MustParseAddr("127.78.0.13")
			request.Endpoints[1].Public = netip.MustParseAddrPort("127.78.0.13:3306")
		}, want: "not a network pool candidate"},
		{name: "retained foreign address", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Ensures[0].Lease.Key.SecondaryID = "cache"
			request.Ensures[0].Lease.Address = netip.MustParseAddr("127.77.0.11")
			request.Endpoints = request.Endpoints[:1]
			request.Releases = []NetworkLeaseRelease{}
		}, want: "retained by an active lease"},
		{name: "active key differs", mutate: func(request *ReplaceProjectNetworkRequest, initialization InitializeNetworkRequest) {
			request.Ensures = []NetworkLeaseEnsure{initialization.Ensures[1]}
			request.Ensures[0].EnsureEvidence = "different evidence"
			request.Releases = []NetworkLeaseRelease{}
			request.Endpoints = canonicalEndpointReservations([]EndpointReservation{initialization.Endpoints[0], initialization.Endpoints[2]})
		}, want: "already active with different durable facts"},
		{name: "missing final primary", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Ensures = []NetworkLeaseEnsure{}
			request.Endpoints = []EndpointReservation{}
		}, want: "requires a primary network lease"},
		{name: "unknown TCP identity", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Endpoints[1].Identity = recordTestLeaseKeyPointer("project-alpha", "unknown")
		}, want: "does not resolve to its final active lease"},
		{name: "foreign endpoint host", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Endpoints[0].Host = "beta.test"
		}, want: "already reserved"},
		{name: "wrong shared HTTP socket", mutate: func(request *ReplaceProjectNetworkRequest, _ InitializeNetworkRequest) {
			request.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.1:80")
		}, want: "does not use the advertised HTTPS socket"},
		{name: "root time regression", mutate: func(request *ReplaceProjectNetworkRequest, initialization InitializeNetworkRequest) {
			request.At = initialization.At.Add(-time.Second)
			request.Releases[0].ReleasedAt = request.At.Add(-3 * time.Minute)
			request.Releases[0].QuarantinedAt = request.At.Add(-2 * time.Minute)
			request.Releases[0].ReuseAfter = request.At.Add(time.Hour)
			request.Ensures[0].LeasedAt = request.At.Add(-time.Minute)
		}, want: "precedes network update time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
			test.mutate(&request, initialization)
			before := networkReplaceTestRows(t, connection)
			_, err := store.ReplaceProjectNetwork(context.Background(), request)
			assertNetworkReplaceConflict(t, err, test.want)
			if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatal("conflict changed durable rows")
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
				t.Fatalf("Harbor sequence after conflict = %d, want 7", highWater)
			}
		})
	}
}

// TestStoreReplaceProjectNetworkRequiresSchemaRootAndExactOwners verifies lifecycle and optimistic boundaries are typed.
func TestStoreReplaceProjectNetworkRequiresSchemaRootAndExactOwners(t *testing.T) {
	t.Run("schema absent", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		_, err := store.ReplaceProjectNetwork(context.Background(), networkReplaceTestStandaloneRequest())
		if err == nil || !strings.Contains(err.Error(), "schema is not installed") {
			t.Fatalf("schema absent error = %v", err)
		}
	})

	t.Run("root absent", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		applyNetworkInitializeTestMigration(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), networkReplaceTestStandaloneRequest())
		var missing *NetworkNotInitializedError
		if !errors.As(err, &missing) {
			t.Fatalf("root absent error = %v", err)
		}
	})

	t.Run("partial schema", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, networkStoreReadTestSchema[0])
		_, err := store.ReplaceProjectNetwork(context.Background(), networkReplaceTestStandaloneRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "schema is incomplete") {
			t.Fatalf("partial schema error = %v", err)
		}
	})

	t.Run("network revision", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		request.ExpectedNetworkRevision = 6
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var stale *NetworkRevisionConflictError
		if !errors.As(err, &stale) || stale.Expected != 6 || stale.Actual != 7 {
			t.Fatalf("network revision error = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("network stale check changed rows")
		}
	})

	t.Run("project revision", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		request.ExpectedProjectRevision = 4
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var stale *ProjectRevisionConflictError
		if !errors.As(err, &stale) || stale.Expected != 4 || stale.Actual != 5 {
			t.Fatalf("project revision error = %v", err)
		}
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("project stale check changed rows")
		}
	})

	t.Run("project missing", func(t *testing.T) {
		store, _, request, _ := newNetworkReplaceTestHarness(t, 1)
		request.ProjectID = "project-missing"
		request.ExpectedProjectRevision = 4
		for index := range request.Ensures {
			request.Ensures[index].Lease.Key.ProjectID = request.ProjectID
		}
		for index := range request.Releases {
			request.Releases[index].Lease.Key.ProjectID = request.ProjectID
		}
		for index := range request.Endpoints {
			request.Endpoints[index].Key.ProjectID = request.ProjectID
			if request.Endpoints[index].Identity != nil {
				request.Endpoints[index].Identity.ProjectID = request.ProjectID
			}
		}
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var missing *ProjectNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("project missing error = %v", err)
		}
	})
}

// TestStoreReplaceProjectNetworkValidatesClonesAndCancelsBeforeWriting verifies the public writer boundary.
func TestStoreReplaceProjectNetworkValidatesClonesAndCancelsBeforeWriting(t *testing.T) {
	invalid := networkReplaceTestStandaloneRequest()
	invalid.Ensures = nil
	var absentStore *Store
	if _, err := absentStore.ReplaceProjectNetwork(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "must be initialized") {
		t.Fatalf("pre-storage validation error = %v", err)
	}

	t.Run("canceled", func(t *testing.T) {
		store, _, request, _ := newNetworkReplaceTestHarness(t, 1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.ReplaceProjectNetwork(ctx, request)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
			t.Fatalf("Harbor sequence after cancellation = %d, want 7", highWater)
		}
	})

	t.Run("queued clone", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		wantEnsureEvidence := request.Ensures[0].EnsureEvidence
		wantReleaseEvidence := request.Releases[0].ReleaseEvidence
		wantHost := request.Endpoints[0].Host
		wantIdentity := *request.Endpoints[1].Identity
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		ctx := &networkReplaceSignalContext{Context: context.Background(), reached: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := store.ReplaceProjectNetwork(ctx, request)
			result <- err
		}()
		<-ctx.reached
		request.Ensures[0].EnsureEvidence = "mutated ensure"
		request.Releases[0].ReleaseEvidence = "mutated release"
		request.Endpoints[0].Host = "mutated.test"
		request.Endpoints[1].Identity.ProjectID = "project-mutated"
		store.mutations.permit <- struct{}{}
		released = true
		if err := <-result; err != nil {
			t.Fatalf("queued replacement error = %v", err)
		}
		rows := networkReplaceTestRows(t, connection)
		ensure := networkReplaceTestLeaseRow(t, rows.Leases, "project-alpha", "")
		release := networkReplaceTestLeaseRowByAddress(t, rows.Leases, "127.77.0.10")
		web := networkReplaceTestEndpointRow(t, rows.Endpoints, "project-alpha", "web")
		mysql := networkReplaceTestEndpointRow(t, rows.Endpoints, "project-alpha", "mysql")
		if ensure.EnsureEvidence != wantEnsureEvidence || release.ReleaseEvidence.String != wantReleaseEvidence ||
			web.Hostname != wantHost || !mysql.LoopbackAddressLeaseId.Valid {
			t.Fatalf("queued cloned rows = ensure %#v release %#v web %#v mysql %#v", ensure, release, web, mysql)
		}
		leaseByID := networkReplaceTestLeaseRowByID(t, rows.Leases, int(mysql.LoopbackAddressLeaseId.Int64))
		key, err := networkLeaseKeyFromModel(domain.ProjectID(leaseByID.SourceProjectId), leaseByID.Kind, leaseByID.SecondaryId)
		if err != nil || key != wantIdentity {
			t.Fatalf("queued TCP identity = %#v, error %v; want %#v", key, err, wantIdentity)
		}
	})
}

// TestStoreReplaceProjectNetworkConcurrentRetriesAllocateOnce verifies the shared coordinator converges equivalent callers.
func TestStoreReplaceProjectNetworkConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _, request, _ := newNetworkReplaceTestHarness(t, 4)
	start := make(chan struct{})
	results := make(chan struct {
		result NetworkMutationResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.ReplaceProjectNetwork(context.Background(), request)
			results <- struct {
				result NetworkMutationResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent replacement errors = %v and %v", first.err, second.err)
	}
	if first.result.Replayed == second.result.Replayed || !reflect.DeepEqual(first.result.Record, second.result.Record) {
		t.Fatalf("concurrent results = %#v and %#v", first.result, second.result)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
		t.Fatalf("Harbor sequence after concurrent retries = %d, want 8", highWater)
	}
}

// TestStoreReplaceProjectNetworkRetainsSatisfiedLeaseFacts verifies endpoint-only changes do not rewrite lease evidence.
func TestStoreReplaceProjectNetworkRetainsSatisfiedLeaseFacts(t *testing.T) {
	store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	secondary := networkReplaceTestLeaseRow(t, before.Leases, "project-alpha", "metrics")
	request := networkReplaceTestNoopRequest(initialization)
	request.At = initialization.At.Add(20 * time.Minute)
	request.Ensures = []NetworkLeaseEnsure{initialization.Ensures[1]}
	request.Endpoints[0].Host = "app.alpha.test"
	request.Endpoints[0].Generation++
	request.Endpoints = canonicalEndpointReservations(request.Endpoints)

	result, err := store.ReplaceProjectNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("endpoint-only replacement error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 8 {
		t.Fatalf("endpoint-only replacement result = %#v", result)
	}
	after := networkReplaceTestRows(t, connection)
	if got := networkReplaceTestLeaseRowByID(t, after.Leases, secondary.Id); !reflect.DeepEqual(got, secondary) {
		t.Fatalf("satisfied ensure was rewritten: got %#v want %#v", got, secondary)
	}
	if len(after.Leases) != len(before.Leases) {
		t.Fatalf("lease count = %d, want %d", len(after.Leases), len(before.Leases))
	}
}

// TestStoreReplaceProjectNetworkCanRemoveEveryProjectEndpoint verifies an endpoint-free project keeps its primary identity.
func TestStoreReplaceProjectNetworkCanRemoveEveryProjectEndpoint(t *testing.T) {
	store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	foreign := networkReplaceTestEndpointRow(t, before.Endpoints, "project-beta", "web")
	primary := networkReplaceTestLeaseRow(t, before.Leases, "project-alpha", "")
	request := networkReplaceTestNoopRequest(initialization)
	request.At = initialization.At.Add(20 * time.Minute)
	request.Endpoints = []EndpointReservation{}

	result, err := store.ReplaceProjectNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("empty endpoint replacement error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 8 {
		t.Fatalf("empty endpoint replacement result = %#v", result)
	}
	after := networkReplaceTestRows(t, connection)
	for _, row := range after.Endpoints {
		if row.ProjectId == "project-alpha" {
			t.Fatalf("target endpoint survived replacement: %#v", row)
		}
	}
	if got := networkReplaceTestEndpointRow(t, after.Endpoints, "project-beta", "web"); !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign endpoint changed: got %#v want %#v", got, foreign)
	}
	if got := networkReplaceTestLeaseRowByID(t, after.Leases, primary.Id); !reflect.DeepEqual(got, primary) {
		t.Fatalf("target primary changed: got %#v want %#v", got, primary)
	}
}

// TestStoreReplaceProjectNetworkReplaysAcrossLaterIndependentRevisions verifies replay is monotonic across global owners.
func TestStoreReplaceProjectNetworkReplaysAcrossLaterIndependentRevisions(t *testing.T) {
	t.Run("later project revision", func(t *testing.T) {
		store, _, request, _ := newNetworkReplaceTestHarness(t, 1)
		applied, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("seed replacement: %v", err)
		}
		project, err := store.Project(context.Background(), "project-alpha")
		if err != nil {
			t.Fatalf("read project: %v", err)
		}
		project.Project.Favorite = !project.Project.Favorite
		updated, err := store.PutProject(context.Background(), project.Project)
		if err != nil || updated.Revision != 9 {
			t.Fatalf("PutProject() = %#v, %v; want revision 9", updated, err)
		}

		replayed, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("replay after project update: %v", err)
		}
		if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, applied.Record) {
			t.Fatalf("replay after project update = %#v, want %#v", replayed, applied)
		}
	})

	t.Run("later network revision", func(t *testing.T) {
		store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
		applied, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("seed replacement: %v", err)
		}
		beta := initialization.Endpoints[1]
		beta.Host = "app.beta.test"
		beta.Generation++
		advance := ReplaceProjectNetworkRequest{
			ProjectID:               "project-beta",
			ExpectedNetworkRevision: 8,
			ExpectedProjectRevision: 6,
			Ensures:                 []NetworkLeaseEnsure{},
			Releases:                []NetworkLeaseRelease{},
			Endpoints:               canonicalEndpointReservations([]EndpointReservation{beta}),
			At:                      request.At.Add(time.Minute),
		}
		advanced, err := store.ReplaceProjectNetwork(context.Background(), advance)
		if err != nil || advanced.Record.Revision != 9 || advanced.Replayed {
			t.Fatalf("independent network replacement = %#v, %v", advanced, err)
		}

		replayed, err := store.ReplaceProjectNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("replay after network update: %v", err)
		}
		if !replayed.Replayed || replayed.Record.Revision != 9 {
			t.Fatalf("replay after network update = %#v", replayed)
		}
		if !networkProjectReplacementSatisfied(networkReplaceTestRows(t, connection), request) {
			t.Fatal("later network projection no longer satisfies the original project request")
		}
		if applied.Record.Revision != 8 {
			t.Fatalf("original replacement revision = %d, want 8", applied.Record.Revision)
		}
	})
}

// TestStoreReplaceProjectNetworkRejectsReleaseLifecycleAndSuppressedCollisions verifies teardown freezes reconciliation.
func TestStoreReplaceProjectNetworkRejectsReleaseLifecycleAndSuppressedCollisions(t *testing.T) {
	t.Run("target releasing", func(t *testing.T) {
		store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
		networkReplaceTestStageRelease(t, store, connection, "project-alpha", initialization.At)
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		assertNetworkReplaceConflict(t, err, `project release is "releasing"`)
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("release lifecycle rejection changed durable rows")
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
			t.Fatalf("Harbor sequence after lifecycle rejection = %d, want 8", highWater)
		}
	})

	t.Run("suppressed foreign endpoint", func(t *testing.T) {
		store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
		networkReplaceTestStageRelease(t, store, connection, "project-beta", initialization.At)
		request.Endpoints[0].Host = "beta.test"
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		assertNetworkReplaceConflict(t, err, "already reserved")
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("suppressed collision rejection changed durable rows")
		}
	})

	t.Run("target completed", func(t *testing.T) {
		store, connection, request, initialization := newNetworkReplaceTestHarness(t, 1)
		networkReplaceTestCompleteRelease(t, store, connection, "project-alpha", initialization.At)
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		assertNetworkReplaceConflict(t, err, `project release is "completed"`)
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("completed lifecycle rejection changed durable rows")
		}
	})

	t.Run("completed primary proof", func(t *testing.T) {
		store, connection, _, initialization := newNetworkReplaceTestHarness(t, 1)
		networkReplaceTestCompleteRelease(t, store, connection, "project-beta", initialization.At)
		request := networkReplaceTestNoopRequest(initialization)
		request.At = initialization.At.Add(10 * time.Minute)
		request.Ensures = []NetworkLeaseEnsure{
			networkReplaceTestEnsure("cache", "127.77.0.11", 300, request.At.Add(-time.Minute)),
		}
		before := networkReplaceTestRows(t, connection)
		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		assertNetworkReplaceConflict(t, err, "preserves completed release ownership")
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("completed release proof rejection changed durable rows")
		}
	})
}

// TestStoreReplaceProjectNetworkRollsBackEveryMutationPhase verifies no partial replacement or sequence survives a write error.
func TestStoreReplaceProjectNetworkRollsBackEveryMutationPhase(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		want      string
	}{
		{
			name: "endpoint deletion",
			statement: `CREATE TRIGGER fail_network_replace_delete BEFORE DELETE ON public_endpoint_leases
				BEGIN SELECT RAISE(ABORT, 'forced endpoint deletion failure'); END`,
			want: "forced endpoint deletion failure",
		},
		{
			name: "lease release",
			statement: `CREATE TRIGGER fail_network_replace_release BEFORE UPDATE ON loopback_address_leases
				WHEN OLD.address = '127.77.0.10' AND NEW.state = 'quarantined'
				BEGIN SELECT RAISE(ABORT, 'forced lease release failure'); END`,
			want: "forced lease release failure",
		},
		{
			name: "lease ensure",
			statement: `CREATE TRIGGER fail_network_replace_ensure BEFORE INSERT ON loopback_address_leases
				BEGIN SELECT RAISE(ABORT, 'forced lease ensure failure'); END`,
			want: "forced lease ensure failure",
		},
		{
			name: "endpoint insertion",
			statement: `CREATE TRIGGER fail_network_replace_endpoint BEFORE INSERT ON public_endpoint_leases
				BEGIN SELECT RAISE(ABORT, 'forced endpoint insertion failure'); END`,
			want: "forced endpoint insertion failure",
		},
		{
			name: "root update",
			statement: `CREATE TRIGGER fail_network_replace_root BEFORE UPDATE ON network_state
				BEGIN SELECT RAISE(ABORT, 'forced network root failure'); END`,
			want: "forced network root failure",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
			before := networkReplaceTestRows(t, connection)
			mustProjectStoreReadExec(t, connection, test.statement)
			_, err := store.ReplaceProjectNetwork(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mutation error = %v, want containing %q", err, test.want)
			}
			if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed %s changed durable rows: before %s after %s", test.name, networkReplaceTestDump(before), networkReplaceTestDump(after))
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
				t.Fatalf("Harbor sequence after failed %s = %d, want 7", test.name, highWater)
			}
		})
	}
}

// TestStoreReplaceProjectNetworkRejectsTamperedReadback verifies exact hidden facts and endpoint lifecycle are transactional.
func TestStoreReplaceProjectNetworkRejectsTamperedReadback(t *testing.T) {
	t.Run("query failure", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		want := errors.New("network replacement readback sentinel")
		active := false
		updateCallback := "harbor:test_network_replace_readback_active"
		queryCallback := "harbor:test_network_replace_readback_error"
		if err := connection.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
			if tx.Statement.Table == "network_state" {
				active = true
			}
		}); err != nil {
			t.Fatalf("register readback activation: %v", err)
		}
		if err := connection.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
			if active && tx.Statement.Table == "network_state" {
				tx.AddError(want)
			}
		}); err != nil {
			t.Fatalf("register readback failure: %v", err)
		}
		t.Cleanup(func() {
			_ = connection.Callback().Update().Remove(updateCallback)
			_ = connection.Callback().Query().Remove(queryCallback)
		})

		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		if !errors.Is(err, want) {
			t.Fatalf("readback query error = %v, want sentinel identity", err)
		}
		active = false
		assertNetworkReplaceRollback(t, store, connection, before)
	})

	t.Run("released ensure history", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER corrupt_network_replace_release
			AFTER UPDATE OF revision ON network_state
			BEGIN
				UPDATE loopback_address_leases SET ensure_evidence = 'tampered ensure history'
				WHERE address = '127.77.0.10';
			END`)

		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "released row differs") {
			t.Fatalf("tampered release readback error = %v", err)
		}
		assertNetworkReplaceRollback(t, store, connection, before)
	})

	t.Run("endpoint creation time", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER corrupt_network_replace_endpoint
			AFTER INSERT ON public_endpoint_leases
			WHEN NEW.project_id = 'project-alpha' AND NEW.endpoint_id = 'web'
			BEGIN
				UPDATE public_endpoint_leases SET updated_at = datetime(NEW.updated_at, '+1 second') WHERE id = NEW.id;
			END`)

		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "row differs from its exact lifecycle plan") {
			t.Fatalf("tampered endpoint readback error = %v", err)
		}
		assertNetworkReplaceRollback(t, store, connection, before)
	})
}

// TestStoreReplaceProjectNetworkRejectsReadbackSequenceCollision verifies the new root remains the sole revision owner.
func TestStoreReplaceProjectNetworkRejectsReadbackSequenceCollision(t *testing.T) {
	store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
	before := networkReplaceTestRows(t, connection)
	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER collide_network_replace_revision
		AFTER UPDATE OF revision ON network_state
		BEGIN
			INSERT INTO operations
				(id, intent_id, kind, state, phase, requested_at, revision)
				VALUES ('operation-network-collision', 'intent-network-collision', 'maintenance.run', 'queued', 'queued', '2026-07-18T12:00:00Z', NEW.revision);
		END`)

	_, err := store.ReplaceProjectNetwork(context.Background(), request)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision") {
		t.Fatalf("readback sequence collision error = %v", err)
	}
	assertNetworkReplaceRollback(t, store, connection, before)
	var count int64
	if err := connection.Model(&models.Operation{}).Where("id = ?", "operation-network-collision").Count(&count).Error; err != nil {
		t.Fatalf("count collision operation: %v", err)
	}
	if count != 0 {
		t.Fatalf("collision operation survived rollback: count = %d", count)
	}
}

// TestStoreReplaceProjectNetworkPreservesStorageErrorAndQueuedCancellation verifies errors cross the writer boundary intact.
func TestStoreReplaceProjectNetworkPreservesStorageErrorAndQueuedCancellation(t *testing.T) {
	t.Run("storage error identity", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		want := errors.New("network replacement query sentinel")
		callback := "harbor:test_network_replace_error"
		if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "network_state" {
				tx.AddError(want)
			}
		}); err != nil {
			t.Fatalf("register query failure: %v", err)
		}
		t.Cleanup(func() { _ = connection.Callback().Query().Remove(callback) })

		_, err := store.ReplaceProjectNetwork(context.Background(), request)
		if !errors.Is(err, want) {
			t.Fatalf("storage error = %v, want sentinel identity", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
			t.Fatalf("Harbor sequence after storage error = %d, want 7", highWater)
		}
	})

	t.Run("canceled while queued", func(t *testing.T) {
		store, connection, request, _ := newNetworkReplaceTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		base, cancel := context.WithCancel(context.Background())
		ctx := &networkReplaceSignalContext{Context: base, reached: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := store.ReplaceProjectNetwork(ctx, request)
			result <- err
		}()
		<-ctx.reached
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error = %v", err)
		}
		store.mutations.permit <- struct{}{}
		released = true
		if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("queued cancellation changed durable rows")
		}
	})
}

// networkReplaceSignalContext exposes the post-clone cancellation check as a deterministic test barrier.
type networkReplaceSignalContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

// Err signals that request cloning completed before reporting the embedded context state.
func (ctx *networkReplaceSignalContext) Err() error {
	ctx.once.Do(func() { close(ctx.reached) })
	return ctx.Context.Err()
}

// newNetworkReplaceTestHarness initializes the production schema and returns one valid primary reallocation.
func newNetworkReplaceTestHarness(
	t *testing.T,
	maximumConnections int,
) (*Store, *gorm.DB, ReplaceProjectNetworkRequest, InitializeNetworkRequest) {
	t.Helper()
	store, connection := newNetworkInitializeTestHarnessWithConnections(t, true, maximumConnections)
	initialization := networkMutationTestInitializeRequest()
	initialization.Pool = recordTestPool(
		"127.77.0.0/24",
		"127.77.0.10",
		"127.77.0.11",
		"127.77.0.12",
		"127.77.0.13",
		"127.77.0.14",
		"127.77.0.15",
	)
	result, err := store.InitializeNetwork(context.Background(), initialization)
	if err != nil || result.Replayed || result.Record.Revision != 7 {
		t.Fatalf("InitializeNetwork() = %#v, %v", result, err)
	}
	at := initialization.At.Add(10 * time.Minute)
	release := networkReplaceTestRelease(
		initialization.Ensures[0].Lease,
		31,
		at.Add(-3*time.Minute),
		at.Add(-2*time.Minute),
		at.Add(30*time.Minute),
	)
	ensure := networkReplaceTestEnsure("", "127.77.0.13", 32, at.Add(-time.Minute))
	web := initialization.Endpoints[0]
	mysql := initialization.Endpoints[2]
	mysql.Public = netip.MustParseAddrPort("127.77.0.13:3306")
	mysql.Generation++
	request := ReplaceProjectNetworkRequest{
		ProjectID:               "project-alpha",
		ExpectedNetworkRevision: 7,
		ExpectedProjectRevision: 5,
		Ensures:                 []NetworkLeaseEnsure{ensure},
		Releases:                []NetworkLeaseRelease{release},
		Endpoints:               canonicalEndpointReservations([]EndpointReservation{web, mysql}),
		At:                      at,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("replacement fixture Validate() error = %v", err)
	}
	return store, connection, request, initialization
}

// networkReplaceTestNoopRequest returns the currently satisfied target endpoint projection with empty lease deltas.
func networkReplaceTestNoopRequest(initialization InitializeNetworkRequest) ReplaceProjectNetworkRequest {
	return ReplaceProjectNetworkRequest{
		ProjectID:               "project-alpha",
		ExpectedNetworkRevision: 7,
		ExpectedProjectRevision: 5,
		Ensures:                 []NetworkLeaseEnsure{},
		Releases:                []NetworkLeaseRelease{},
		Endpoints: canonicalEndpointReservations([]EndpointReservation{
			initialization.Endpoints[0],
			initialization.Endpoints[2],
		}),
		At: initialization.At.Add(10 * time.Minute),
	}
}

// networkReplaceTestStandaloneRequest returns a valid request without relying on initialized durable state.
func networkReplaceTestStandaloneRequest() ReplaceProjectNetworkRequest {
	return networkMutationTestReplaceRequest()
}

// networkReplaceTestEnsure returns one completed alpha-project ensure at an exact causal generation.
func networkReplaceTestEnsure(
	secondaryID string,
	address string,
	generation uint64,
	leasedAt time.Time,
) NetworkLeaseEnsure {
	return NetworkLeaseEnsure{
		Lease: identity.Lease{
			Key:     identity.LeaseKey{ProjectID: "project-alpha", SecondaryID: secondaryID},
			Address: netip.MustParseAddr(address),
			Ownership: identity.Ownership{
				InstallationID: "harbor-installation",
				Generation:     10,
			},
		},
		Generation:     generation,
		EnsureEvidence: " verified replacement ensure ",
		LeasedAt:       leasedAt,
	}
}

// networkReplaceTestRelease returns one complete quarantine transition for an existing lease.
func networkReplaceTestRelease(
	lease identity.Lease,
	generation uint64,
	releasedAt time.Time,
	quarantinedAt time.Time,
	reuseAfter time.Time,
) NetworkLeaseRelease {
	return NetworkLeaseRelease{
		Lease:             lease,
		ReleaseGeneration: generation,
		ReleaseEvidence:   " verified replacement release ",
		ReleasedAt:        releasedAt,
		QuarantinedAt:     quarantinedAt,
		ReuseAfter:        reuseAfter,
		QuarantineReason:  "verified replacement pending safe reuse",
	}
}

// networkReplaceTestRows reads the complete hidden aggregate in production ordering.
func networkReplaceTestRows(t *testing.T, connection *gorm.DB) networkModelRows {
	t.Helper()
	rows, err := readNetworkModelRows(connection)
	if err != nil {
		t.Fatalf("readNetworkModelRows() error = %v", err)
	}
	return rows
}

// networkReplaceTestLeaseRow returns one exact source project/key row regardless of active state.
func networkReplaceTestLeaseRow(
	t *testing.T,
	rows []models.LoopbackAddressLease,
	projectID string,
	secondaryID string,
) models.LoopbackAddressLease {
	t.Helper()
	var matches []models.LoopbackAddressLease
	var active []models.LoopbackAddressLease
	for _, row := range rows {
		if row.SourceProjectId == projectID && row.SecondaryId == secondaryID {
			matches = append(matches, row)
			if row.State == "leased" {
				active = append(active, row)
			}
		}
	}
	if len(active) == 1 {
		return active[0]
	}
	if len(matches) != 1 {
		t.Fatalf("lease rows for %s/%s = %#v", projectID, secondaryID, matches)
	}
	return matches[0]
}

// networkReplaceTestLeaseRowByID returns one exact lease surrogate.
func networkReplaceTestLeaseRowByID(t *testing.T, rows []models.LoopbackAddressLease, id int) models.LoopbackAddressLease {
	t.Helper()
	for _, row := range rows {
		if row.Id == id {
			return row
		}
	}
	t.Fatalf("lease row ID %d was not found", id)
	return models.LoopbackAddressLease{}
}

// networkReplaceTestLeaseRowByAddress returns one exact durable address owner.
func networkReplaceTestLeaseRowByAddress(t *testing.T, rows []models.LoopbackAddressLease, address string) models.LoopbackAddressLease {
	t.Helper()
	for _, row := range rows {
		if row.Address == address {
			return row
		}
	}
	t.Fatalf("lease address %s was not found", address)
	return models.LoopbackAddressLease{}
}

// networkReplaceTestEndpointRow returns one project-scoped endpoint row.
func networkReplaceTestEndpointRow(
	t *testing.T,
	rows []models.PublicEndpointLease,
	projectID string,
	endpointID string,
) models.PublicEndpointLease {
	t.Helper()
	for _, row := range rows {
		if row.ProjectId == projectID && row.EndpointId == endpointID {
			return row
		}
	}
	t.Fatalf("endpoint row %s/%s was not found", projectID, endpointID)
	return models.PublicEndpointLease{}
}

// assertNetworkReplaceConflict requires the typed project replacement boundary and safe diagnostic fragment.
func assertNetworkReplaceConflict(t *testing.T, err error, want string) {
	t.Helper()
	var conflict *NetworkProjectReplacementConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), want) {
		t.Fatalf("replacement error = %v, want typed conflict containing %q", err, want)
	}
}

// networkReplaceTestDump identifies raw rows in assertion failures without exposing evidence by default.
func networkReplaceTestDump(rows networkModelRows) string {
	return fmt.Sprintf("states=%d leases=%d endpoints=%d releases=%d", len(rows.States), len(rows.Leases), len(rows.Endpoints), len(rows.Releases))
}

// networkReplaceTestStageRelease inserts one valid releasing marker owned by an unregister operation.
func networkReplaceTestStageRelease(
	t *testing.T,
	store *Store,
	connection *gorm.DB,
	projectID domain.ProjectID,
	beganAt time.Time,
) {
	t.Helper()
	operationID := domain.OperationID("operation-network-release-" + string(projectID))
	operation, err := domain.NewOperation(
		operationID,
		domain.IntentID("intent-network-release-"+string(projectID)),
		domain.OperationKindProjectUnregister,
		projectID,
		beganAt.Add(-time.Minute),
	)
	if err != nil {
		t.Fatalf("create release operation: %v", err)
	}
	if _, err := projectStoreMutationJournal(store).Enqueue(context.Background(), operation); err != nil {
		t.Fatalf("enqueue release operation: %v", err)
	}
	row := models.NetworkProjectRelease{
		NetworkStateId:  networkStateSingletonID,
		ProjectId:       null.StringFrom(string(projectID)),
		SourceProjectId: string(projectID),
		OperationId:     string(operationID),
		State:           "releasing",
		BeginGeneration: 100,
		BeganAt:         beganAt,
	}
	if err := connection.Create(&row).Error; err != nil {
		t.Fatalf("insert network project release: %v", err)
	}
}

// networkReplaceTestCompleteRelease inserts one valid completed teardown boundary with retained source quarantines.
func networkReplaceTestCompleteRelease(
	t *testing.T,
	store *Store,
	connection *gorm.DB,
	projectID domain.ProjectID,
	completedAt time.Time,
) {
	t.Helper()
	operationID := domain.OperationID("operation-network-complete-" + string(projectID))
	operation, err := domain.NewOperation(
		operationID,
		domain.IntentID("intent-network-complete-"+string(projectID)),
		domain.OperationKindProjectUnregister,
		projectID,
		completedAt.Add(-5*time.Minute),
	)
	if err != nil {
		t.Fatalf("create completed release operation: %v", err)
	}
	if _, err := projectStoreMutationJournal(store).Enqueue(context.Background(), operation); err != nil {
		t.Fatalf("enqueue completed release operation: %v", err)
	}
	if err := connection.Where("project_id = ?", string(projectID)).Delete(&models.PublicEndpointLease{}).Error; err != nil {
		t.Fatalf("delete completed release endpoints: %v", err)
	}
	var leases []models.LoopbackAddressLease
	if err := connection.Where("project_id = ? AND state = ?", string(projectID), "leased").Order("id ASC").Find(&leases).Error; err != nil {
		t.Fatalf("read completed release leases: %v", err)
	}
	if len(leases) == 0 {
		t.Fatalf("completed release project %q has no active leases", projectID)
	}
	releasedAt := completedAt.Add(-3 * time.Minute)
	quarantinedAt := completedAt.Add(-2 * time.Minute)
	reuseAfter := completedAt.Add(time.Minute)
	releases := make([]NetworkLeaseRelease, 0, len(leases))
	for _, lease := range leases {
		key, err := networkLeaseKeyFromModel(projectID, lease.Kind, lease.SecondaryId)
		if err != nil {
			t.Fatalf("restore completed release lease key: %v", err)
		}
		address, err := parseCanonicalNetworkAddress("completed release lease", lease.Address)
		if err != nil {
			t.Fatalf("restore completed release lease address: %v", err)
		}
		release := NetworkLeaseRelease{
			Lease: identity.Lease{
				Key:     key,
				Address: address,
				Ownership: identity.Ownership{
					InstallationID: identity.InstallationID(lease.OwnershipInstallationId),
					Generation:     uint64(lease.OwnershipGeneration),
				},
			},
			ReleaseGeneration: uint64(150 + lease.Id),
			ReleaseEvidence:   "verified completed release",
			ReleasedAt:        releasedAt,
			QuarantinedAt:     quarantinedAt,
			ReuseAfter:        reuseAfter,
			QuarantineReason:  "completed project release pending safe reuse",
		}
		if err := release.Validate(); err != nil {
			t.Fatalf("build completed release fact: %v", err)
		}
		releases = append(releases, release)
		updated := connection.Model(&models.LoopbackAddressLease{}).
			Where("id = ? AND state = ?", lease.Id, "leased").
			Updates(map[string]any{
				"project_id":         nil,
				"state":              "quarantined",
				"release_generation": release.ReleaseGeneration,
				"release_evidence":   release.ReleaseEvidence,
				"released_at":        release.ReleasedAt,
				"quarantined_at":     release.QuarantinedAt,
				"reuse_after":        release.ReuseAfter,
				"quarantine_reason":  release.QuarantineReason,
			})
		if updated.Error != nil || updated.RowsAffected != 1 {
			t.Fatalf("quarantine completed release lease %d: rows %d, error %v", lease.Id, updated.RowsAffected, updated.Error)
		}
	}
	row := models.NetworkProjectRelease{
		NetworkStateId:       networkStateSingletonID,
		ProjectId:            null.String{},
		SourceProjectId:      string(projectID),
		OperationId:          string(operationID),
		State:                "completed",
		BeginGeneration:      100,
		BeganAt:              completedAt.Add(-4 * time.Minute),
		CompletionGeneration: null.IntFrom(200),
		CompletedAt:          &completedAt,
		ReleaseEvidence:      null.StringFrom("verified completed project release"),
		ReleaseSetDigest:     null.StringFrom(projectNetworkReleaseSetDigest(releases)),
	}
	if err := connection.Create(&row).Error; err != nil {
		t.Fatalf("insert completed network project release: %v", err)
	}
	if _, initialized, err := store.Network(context.Background()); err != nil || !initialized {
		t.Fatalf("completed release aggregate = initialized %t, error %v", initialized, err)
	}
}

// assertNetworkReplaceRollback proves a failed replacement restores the exact network rows and global sequence.
func assertNetworkReplaceRollback(t *testing.T, store *Store, connection *gorm.DB, before networkModelRows) {
	t.Helper()
	if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed replacement changed durable rows: before %s after %s", networkReplaceTestDump(before), networkReplaceTestDump(after))
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
		t.Fatalf("Harbor sequence after failed replacement = %d, want 7", highWater)
	}
}
