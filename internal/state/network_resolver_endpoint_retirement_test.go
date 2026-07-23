package state

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// TestClearResolverStageNativeTCPEndpointsRetiresOnlyStaleEndpoints verifies the repair keeps every durable identity fact except stale endpoints.
func TestClearResolverStageNativeTCPEndpointsRetiresOnlyStaleEndpoints(t *testing.T) {
	fixture := newResolverEndpointRetirementFixture(t)
	before := networkDataPlaneActivationTestRows(t, fixture.database)
	request := ClearResolverStageNativeTCPEndpointsRequest{
		ExpectedNetworkRevision: fixture.networkRevision,
		At:                      fixture.at,
	}

	result, err := fixture.store.ClearResolverStageNativeTCPEndpoints(context.Background(), request)
	if err != nil {
		t.Fatalf("ClearResolverStageNativeTCPEndpoints() error = %v", err)
	}
	if result.Replayed || len(result.Record.Reservations.Endpoints) != 0 || result.Record.Revision <= request.ExpectedNetworkRevision || !result.Record.UpdatedAt.Equal(request.At) {
		t.Fatalf("ClearResolverStageNativeTCPEndpoints() = %#v", result)
	}
	after := networkDataPlaneActivationTestRows(t, fixture.database)
	if len(after.Endpoints) != 0 {
		t.Fatalf("endpoint rows after retirement = %#v, want none", after.Endpoints)
	}
	if !reflect.DeepEqual(before.Candidates, after.Candidates) ||
		!reflect.DeepEqual(before.SetupEvidence, after.SetupEvidence) ||
		!reflect.DeepEqual(before.Listeners, after.Listeners) ||
		!reflect.DeepEqual(before.Leases, after.Leases) ||
		!reflect.DeepEqual(before.Releases, after.Releases) ||
		!reflect.DeepEqual(before.Projects, after.Projects) {
		t.Fatal("resolver endpoint retirement changed durable state beyond endpoints and network root")
	}

	replay, err := fixture.store.ClearResolverStageNativeTCPEndpoints(context.Background(), ClearResolverStageNativeTCPEndpointsRequest{
		ExpectedNetworkRevision: result.Record.Revision,
		At:                      request.At.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("replayed ClearResolverStageNativeTCPEndpoints() error = %v", err)
	}
	if !replay.Replayed || !reflect.DeepEqual(replay.Record, result.Record) {
		t.Fatalf("replayed ClearResolverStageNativeTCPEndpoints() = %#v, want %#v", replay, result)
	}
}

// TestClearResolverStageNativeTCPEndpointsRejectsUnsafeAuthority verifies each repair fence fails before deleting a reservation.
func TestClearResolverStageNativeTCPEndpointsRejectsUnsafeAuthority(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*resolverEndpointRetirementFixture)
		request func(resolverEndpointRetirementFixture) ClearResolverStageNativeTCPEndpointsRequest
		want    string
	}{
		{
			name: "listener",
			mutate: func(fixture *resolverEndpointRetirementFixture) {
				row := models.NetworkSharedListener{
					NetworkStateId:    networkStateSingletonID,
					Kind:              "dns",
					Mode:              string(ListenerModeDirect),
					AdvertisedAddress: "127.0.0.1",
					AdvertisedPort:    53,
					BindAddress:       "127.0.0.1",
					BindPort:          53,
					Generation:        1,
					VerifiedAt:        fixture.at.Add(-time.Minute),
				}
				if err := fixture.database.Create(&row).Error; err != nil {
					t.Fatalf("create listener: %v", err)
				}
			},
			request: func(fixture resolverEndpointRetirementFixture) ClearResolverStageNativeTCPEndpointsRequest {
				return ClearResolverStageNativeTCPEndpointsRequest{ExpectedNetworkRevision: fixture.networkRevision, At: fixture.at}
			},
			want: "no shared listener",
		},
		{
			name: "non TCP endpoint",
			mutate: func(fixture *resolverEndpointRetirementFixture) {
				if err := fixture.database.
					Model(&models.PublicEndpointLease{}).
					Where("id = ?", fixture.endpointID).
					Updates(map[string]any{
						"protocol":                  string(EndpointProtocolHTTP),
						"loopback_address_lease_id": nil,
					}).Error; err != nil {
					t.Fatalf("change endpoint protocol: %v", err)
				}
			},
			request: func(fixture resolverEndpointRetirementFixture) ClearResolverStageNativeTCPEndpointsRequest {
				return ClearResolverStageNativeTCPEndpointsRequest{ExpectedNetworkRevision: fixture.networkRevision, At: fixture.at}
			},
			want: "non-TCP endpoint",
		},
		{
			name: "active project",
			mutate: func(fixture *resolverEndpointRetirementFixture) {
				if err := fixture.database.Model(&models.Project{}).Where("project_id = ?", fixture.projectID).Update("state", string(domain.ProjectReady)).Error; err != nil {
					t.Fatalf("activate project: %v", err)
				}
			},
			request: func(fixture resolverEndpointRetirementFixture) ClearResolverStageNativeTCPEndpointsRequest {
				return ClearResolverStageNativeTCPEndpointsRequest{ExpectedNetworkRevision: fixture.networkRevision, At: fixture.at}
			},
			want: "to be stopped",
		},
		{
			name:   "network revision drift",
			mutate: func(*resolverEndpointRetirementFixture) {},
			request: func(fixture resolverEndpointRetirementFixture) ClearResolverStageNativeTCPEndpointsRequest {
				return ClearResolverStageNativeTCPEndpointsRequest{ExpectedNetworkRevision: fixture.networkRevision + 1, At: fixture.at}
			},
			want: "network revision is",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverEndpointRetirementFixture(t)
			test.mutate(fixture)
			before := networkDataPlaneActivationTestRows(t, fixture.database)
			_, err := fixture.store.ClearResolverStageNativeTCPEndpoints(context.Background(), test.request(*fixture))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ClearResolverStageNativeTCPEndpoints() error = %v, want containing %q", err, test.want)
			}
			after := networkDataPlaneActivationTestRows(t, fixture.database)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("unsafe resolver endpoint retirement changed durable state")
			}
		})
	}
}

// resolverEndpointRetirementFixture provides one stopped project with a legacy resolver-stage native endpoint reservation.
type resolverEndpointRetirementFixture struct {
	store           *Store
	database        *gorm.DB
	networkRevision domain.Sequence
	at              time.Time
	projectID       domain.ProjectID
	endpointID      int
}

// newResolverEndpointRetirementFixture prepares a valid resolver aggregate whose only mutable legacy fact is one TCP endpoint.
func newResolverEndpointRetirementFixture(t *testing.T) *resolverEndpointRetirementFixture {
	t.Helper()
	legacy := newNetworkResolverPolicyMigrationFixture(t)
	connections := legacy.journal.mutations.connections
	store := NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		NewMutationCoordinator(connections),
	)
	var root models.NetworkState
	if err := legacy.database.First(&root, networkStateSingletonID).Error; err != nil {
		t.Fatalf("read resolver network root: %v", err)
	}
	fixture := &resolverEndpointRetirementFixture{
		store:           store,
		database:        legacy.database,
		networkRevision: domain.Sequence(root.Revision),
		at:              root.UpdatedAt.Add(time.Minute),
		projectID:       "project-retired-native-endpoint",
	}
	address := storeNetworkFixtureCandidate(t, root, legacy.database)
	if err := legacy.database.Transaction(func(tx *gorm.DB) error {
		sequence, err := allocateHarborSequence(tx)
		if err != nil {
			return err
		}
		project := validRuntimeStateProject(fixture.projectID)
		project.UpdatedAt = root.UpdatedAt
		projectRow, apps, services, resources, err := projectModelsFromDomain(project, sequence)
		if err != nil {
			return err
		}
		if err := tx.Create(&projectRow).Error; err != nil {
			return err
		}
		if len(apps) != 0 {
			if err := tx.Create(&apps).Error; err != nil {
				return err
			}
		}
		if len(services) != 0 {
			if err := tx.Create(&services).Error; err != nil {
				return err
			}
		}
		if len(resources) != 0 {
			if err := tx.Create(&resources).Error; err != nil {
				return err
			}
		}
		lease := models.LoopbackAddressLease{
			NetworkStateId:          networkStateSingletonID,
			ProjectId:               null.StringFrom(string(fixture.projectID)),
			SourceProjectId:         string(fixture.projectID),
			Kind:                    "primary",
			Address:                 address,
			State:                   "leased",
			LeaseGeneration:         1,
			OwnershipInstallationId: root.InstallationId,
			OwnershipGeneration:     root.OwnershipGeneration,
			EnsureEvidence:          "legacy native endpoint fixture",
			LeasedAt:                root.UpdatedAt,
		}
		if err := tx.Create(&lease).Error; err != nil {
			return err
		}
		endpoint := models.PublicEndpointLease{
			NetworkStateId:         networkStateSingletonID,
			ProjectId:              string(fixture.projectID),
			EndpointId:             "mysql",
			Protocol:               string(EndpointProtocolTCP),
			Hostname:               "mysql.retired.test",
			Address:                address,
			Port:                   3306,
			LoopbackAddressLeaseId: null.IntFrom(int64(lease.Id)),
			Generation:             1,
			CreatedAt:              root.UpdatedAt,
			UpdatedAt:              root.UpdatedAt,
		}
		if err := tx.Create(&endpoint).Error; err != nil {
			return err
		}
		fixture.endpointID = endpoint.Id
		return nil
	}); err != nil {
		t.Fatalf("seed resolver endpoint retirement fixture: %v", err)
	}
	return fixture
}

// storeNetworkFixtureCandidate chooses the first durable pool candidate without reconstructing network ownership in test setup.
func storeNetworkFixtureCandidate(t *testing.T, root models.NetworkState, database *gorm.DB) string {
	t.Helper()
	var candidate models.NetworkPoolCandidate
	if err := database.Where("network_state_id = ?", root.Id).Order("ordinal ASC").First(&candidate).Error; err != nil {
		t.Fatalf("read network pool candidate: %v", err)
	}
	return candidate.Address
}
