package state

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
)

// TestReusableNetworkAddressesEligibility keeps cooldown expiry subordinate to teardown and ownership evidence.
func TestReusableNetworkAddressesEligibility(t *testing.T) {
	cases := []struct {
		name   string
		change func(*networkModelRows, time.Time)
		want   int
	}{
		{"completed deleted project", func(*networkModelRows, time.Time) {}, 1},
		{"active lease", func(r *networkModelRows, _ time.Time) { r.Leases[0].State = "leased" }, 0},
		{"no teardown", func(r *networkModelRows, _ time.Time) { r.Releases = nil }, 0},
		{"unfinished teardown", func(r *networkModelRows, _ time.Time) { r.Releases[0].State = "releasing" }, 0},
		{"future completion", func(r *networkModelRows, at time.Time) {
			later := at.Add(time.Minute)
			r.Releases[0].CompletedAt = &later
		}, 0},
		{"project retained", func(r *networkModelRows, _ time.Time) {
			r.Projects = append(r.Projects, models.Project{ProjectId: r.Leases[0].SourceProjectId})
		}, 0},
		{"cooldown", func(r *networkModelRows, at time.Time) { later := at.Add(time.Minute); r.Leases[0].ReuseAfter = &later }, 0},
		{"deadline equality", func(r *networkModelRows, at time.Time) { r.Leases[0].ReuseAfter = &at }, 1},
		{"foreign installation", func(r *networkModelRows, _ time.Time) { r.Leases[0].OwnershipInstallationId = "another-installation" }, 0},
		{"foreign generation", func(r *networkModelRows, _ time.Time) {
			r.Leases[0].OwnershipGeneration = r.States[0].OwnershipGeneration + 1
		}, 0},
		{"generation exhausted", func(r *networkModelRows, _ time.Time) {
			r.Leases[0].ReleaseGeneration = null.IntFrom(int64(math.MaxInt64))
		}, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rows := validNetworkModelRows()
			network, _, err := networkRecordFromModels(rows)
			if err != nil {
				t.Fatal(err)
			}
			quarantineNetworkLease(&rows.Leases[0])
			projectID := rows.Leases[0].SourceProjectId
			rows.Projects = slices.DeleteFunc(rows.Projects, func(p models.Project) bool { return p.ProjectId == projectID })
			completedAt := networkTestTime().Add(10 * time.Minute)
			rows.Releases = []models.NetworkProjectRelease{{State: "completed", SourceProjectId: projectID, CompletedAt: &completedAt}}
			at := networkTestTime().Add(time.Hour)
			test.change(&rows, at)
			got := reusableNetworkAddresses(rows, network, at)
			if len(got) != test.want {
				t.Fatalf("candidates = %v, want %d", got, test.want)
			}
			if len(got) > 0 && got[0].Generation != uint64(rows.Leases[0].ReleaseGeneration.Int64)+1 {
				t.Fatal("reuse did not advance the release generation")
			}
		})
	}
}

// TestStoreReusableNetworkAddressesRequiresCompletedUnregisterAndExactRevision proves real teardown can replenish the pool without a direct database write.
func TestStoreReusableNetworkAddressesRequiresCompletedUnregisterAndExactRevision(t *testing.T) {
	store, connection, _, _, begin, _ := newNetworkReleaseTestHarness(t, 1)
	staged, err := store.BeginProjectNetworkRelease(t.Context(), begin)
	if err != nil {
		t.Fatal(err)
	}
	request := networkReleaseTestCompleteRequest(begin, staged.Release)
	completed, err := store.CompleteProjectNetworkRelease(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	at := request.At.Add(2 * time.Hour)
	if got, err := store.ReusableNetworkAddresses(t.Context(), completed.Record.Revision, at); err != nil || len(got) != 0 {
		t.Fatalf("retained project candidates = %v, %v", got, err)
	}
	if _, err := store.CompleteProjectUnregister(t.Context(), request.ProjectID, request.OperationID, request.ExpectedOperationRevision, "completed unregister", request.At.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	network, _, err := store.Network(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := networkReplaceTestRows(t, connection)
	got, err := store.ReusableNetworkAddresses(t.Context(), network.Revision, at)
	if err != nil || len(got) != len(request.Releases) {
		t.Fatalf("released candidates = %v, %v", got, err)
	}
	for _, candidate := range got {
		matched := false
		for _, release := range request.Releases {
			if candidate.Address == release.Lease.Address && candidate.Generation == release.ReleaseGeneration+1 {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("candidate lacks exact release history: %v", candidate)
		}
	}
	if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(before, after) {
		t.Fatal("reuse read mutated durable quarantine evidence")
	}
	if _, err := store.ReusableNetworkAddresses(t.Context(), network.Revision-1, at); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err := store.ReusableNetworkAddresses(t.Context(), 0, at); err == nil {
		t.Fatal("zero revision accepted")
	}
	if _, err := store.ReusableNetworkAddresses(t.Context(), network.Revision, time.Time{}); err == nil {
		t.Fatal("zero observation time accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.ReusableNetworkAddresses(ctx, network.Revision, at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestStoreReusableNetworkAddressesRequiresInitializedNetwork rejects missing setup rather than guessing a pool.
func TestStoreReusableNetworkAddressesRequiresInitializedNetwork(t *testing.T) {
	store, _ := newNetworkInitializeTestHarness(t, true)
	_, err := store.ReusableNetworkAddresses(t.Context(), 1, networkTestTime())
	var missing *NetworkNotInitializedError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want uninitialized network", err)
	}
}
