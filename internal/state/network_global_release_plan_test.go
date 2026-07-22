package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// TestReadGlobalNetworkReleasePlanRejectsDurableCorruption verifies recovery refuses every corrupted plan owner and payload boundary.
func TestReadGlobalNetworkReleasePlanRejectsDurableCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, connection *gorm.DB, operationID domain.OperationID)
	}{
		{
			name: "unknown payload field",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanMutatePayload(t, connection, func(payload string) string {
					return payload[:len(payload)-1] + `,"unknown":true}`
				})
			},
		},
		{
			name: "trailing payload value",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanMutatePayload(t, connection, func(payload string) string {
					return payload + " {}"
				})
			},
		},
		{
			name: "noncanonical payload",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanMutatePayload(t, connection, func(payload string) string {
					return strings.Replace(payload, `,"policy":`, `, "policy":`, 1)
				})
			},
		},
		{
			name: "digest mismatch",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET authority_digest = ?", strings.Repeat("a", 64))
			},
		},
		{
			name: "singleton ID",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET id = 2")
			},
		},
		{
			name: "operation owner",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA foreign_keys = OFF")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET operation_id = 'operation-foreign'")
			},
		},
		{
			name: "operation revision",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA foreign_keys = OFF")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET operation_revision = operation_revision + 1")
			},
		},
		{
			name: "runtime checkpoint revision",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET checkpoint_revision = operation_revision - 1")
			},
		},
		{
			name: "future checkpoint revision",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET checkpoint_revision = 999")
			},
		},
		{
			name: "network revision",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA foreign_keys = OFF")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET network_revision = network_revision + 1")
			},
		},
		{
			name: "phase",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET phase = 'foreign'")
			},
		},
		{
			name: "operation state",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				globalNetworkReleasePlanExec(t, connection, "UPDATE operations SET state = 'queued', phase = 'queued' WHERE id = ?", operationID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
			if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err != nil {
				t.Fatalf("stage release plan: %v", err)
			}
			test.mutate(t, connection, request.Operation.ID)
			_, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
			var corrupt *CorruptStateError
			if found || !errors.As(err, &corrupt) {
				t.Fatalf("ReadGlobalNetworkReleasePlan() = found %t, error %v", found, err)
			}
		})
	}
}

// TestReadGlobalNetworkReleasePlanRejectsCheckpointSequenceReuse verifies an advanced checkpoint cannot borrow any materialized sequence owner.
func TestReadGlobalNetworkReleasePlanRejectsCheckpointSequenceReuse(t *testing.T) {
	tests := []struct {
		name    string
		offset  int
		want    string
		prepare func(t *testing.T, connection *gorm.DB, sequence int)
	}{
		{
			name:   "project",
			offset: 1,
			want:   "project",
			prepare: func(t *testing.T, connection *gorm.DB, sequence int) {
				globalNetworkReleasePlanInsertSequenceProject(t, connection, "project-collision", sequence)
			},
		},
		{
			name:   "recent resource",
			offset: 2,
			want:   "recent resource",
			prepare: func(t *testing.T, connection *gorm.DB, sequence int) {
				globalNetworkReleasePlanInsertSequenceProject(t, connection, "recent-collision", sequence-1)
				globalNetworkReleasePlanExec(
					t,
					connection,
					`INSERT INTO project_apps (project_id, app_id, name, state, active, required)
					 VALUES ('recent-collision', 'app', 'App', 'stopped', 0, 1)`,
				)
				globalNetworkReleasePlanExec(
					t,
					connection,
					`INSERT INTO project_resources (
						project_id,
						resource_id,
						name,
						kind,
						url,
						owner_kind,
						owner_app_id
					 ) VALUES (
						'recent-collision',
						'app-http',
						'App',
						'http',
						'https://recent-collision.test',
						'app',
						'app'
					 )`,
				)
				globalNetworkReleasePlanExec(
					t,
					connection,
					`INSERT INTO recent_resources (
						project_id,
						resource_id,
						accessed_at,
						sequence
					 ) VALUES (
						'recent-collision',
						'app-http',
						'2026-07-22T00:00:00Z',
						?
					 )`,
					sequence,
				)
			},
		},
		{
			name:   "operation",
			offset: 1,
			want:   "operation",
			prepare: func(t *testing.T, connection *gorm.DB, sequence int) {
				globalNetworkReleasePlanInsertSequenceOperation(t, connection, "operation-collision", sequence)
			},
		},
		{
			name:   "operation transition",
			offset: 2,
			want:   "operation transition",
			prepare: func(t *testing.T, connection *gorm.DB, sequence int) {
				globalNetworkReleasePlanInsertSequenceOperation(t, connection, "transition-collision", sequence-1)
				globalNetworkReleasePlanExec(
					t,
					connection,
					`INSERT INTO operation_transitions (
						operation_id,
						ordinal,
						state,
						phase,
						occurred_at,
						sequence
					 ) VALUES (
						'transition-collision',
						1,
						'queued',
						'queued',
						'2026-07-22T00:00:00Z',
						?
					 )`,
					sequence,
				)
			},
		},
		{
			name:   "network state",
			offset: 1,
			want:   "network state",
			prepare: func(t *testing.T, connection *gorm.DB, sequence int) {
				globalNetworkReleasePlanExec(t, connection, "PRAGMA foreign_keys = OFF")
				globalNetworkReleasePlanExec(t, connection, "UPDATE network_state SET revision = ? WHERE id = 1", sequence)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), request)
			if err != nil {
				t.Fatalf("stage release plan: %v", err)
			}
			sequence := int(staged.Revision) + test.offset
			globalNetworkReleasePlanExec(t, connection, "UPDATE harbor_state SET sequence = ? WHERE id = 1", sequence)
			test.prepare(t, connection, sequence)
			globalNetworkReleasePlanExec(
				t,
				connection,
				"UPDATE network_global_release_plans SET phase = 'low_ports', checkpoint_revision = ? WHERE id = 1",
				sequence,
			)
			_, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
			var corrupt *CorruptStateError
			if found || !errors.As(err, &corrupt) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadGlobalNetworkReleasePlan() = found %t, error %v; want checkpoint collision containing %q", found, err, test.want)
			}
		})
	}
}

// globalNetworkReleasePlanInsertSequenceProject inserts one otherwise valid stopped project at a chosen corrupt sequence owner.
func globalNetworkReleasePlanInsertSequenceProject(
	t *testing.T,
	connection *gorm.DB,
	projectID string,
	revision int,
) {
	t.Helper()
	globalNetworkReleasePlanExec(
		t,
		connection,
		`INSERT INTO projects (
			project_id,
			name,
			path,
			slug,
			state,
			favorite,
			updated_at,
			revision
		 ) VALUES (?, ?, ?, ?, 'stopped', 0, '2026-07-22T00:00:00Z', ?)`,
		projectID,
		projectID,
		"/tmp/"+projectID,
		projectID,
		revision,
	)
}

// globalNetworkReleasePlanInsertSequenceOperation inserts one queued operation header at a chosen corrupt sequence owner.
func globalNetworkReleasePlanInsertSequenceOperation(
	t *testing.T,
	connection *gorm.DB,
	operationID string,
	revision int,
) {
	t.Helper()
	globalNetworkReleasePlanExec(
		t,
		connection,
		`INSERT INTO operations (
			id,
			intent_id,
			kind,
			state,
			phase,
			requested_at,
			revision
		 ) VALUES (?, ?, 'maintenance.run', 'queued', 'queued', '2026-07-22T00:00:00Z', ?)`,
		operationID,
		"intent-"+operationID,
		revision,
	)
}

// TestReadGlobalNetworkReleasePlanCancellationAndClone verifies canceled reads do not open a plan and callers cannot mutate a returned authority.
func TestReadGlobalNetworkReleasePlanCancellationAndClone(t *testing.T) {
	journal, _, request := newGlobalNetworkReleaseStageFixture(t)
	if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err != nil {
		t.Fatalf("stage release plan: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := journal.ReadGlobalNetworkReleasePlan(ctx, request.Operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ReadGlobalNetworkReleasePlan() error = %v", err)
	}
	first, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
	if err != nil || !found {
		t.Fatalf("first read = %#v, %t, %v", first, found, err)
	}
	first.Authority.Root.CertificatePEM[0] ^= 1
	first.Authority.LoopbackTargets[0].ObservationFingerprint = strings.Repeat("e", 64)
	second, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
	if err != nil || !found || reflect.DeepEqual(first.Authority, second.Authority) {
		t.Fatalf("second read = %#v, %t, %v", second, found, err)
	}
}

// globalNetworkReleasePlanMutatePayload updates payload and its digest together so decoding, rather than checksum failure, proves strictness.
func globalNetworkReleasePlanMutatePayload(t *testing.T, connection *gorm.DB, mutate func(string) string) {
	t.Helper()
	var row globalNetworkReleasePlanRow
	if err := connection.First(&row).Error; err != nil {
		t.Fatalf("read persisted release plan: %v", err)
	}
	payload := mutate(row.AuthorityPayload)
	globalNetworkReleasePlanExec(t, connection, "UPDATE network_global_release_plans SET authority_payload = ?, authority_digest = ?", payload, digestGlobalNetworkReleasePayload(payload))
}

// globalNetworkReleasePlanExec executes one intentional durable corruption fixture mutation.
func globalNetworkReleasePlanExec(t *testing.T, connection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := connection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute plan fixture statement %q: %v", statement, err)
	}
}

// TestGlobalNetworkReleaseAuthorityCodecRoundTrip verifies the durable authority boundary is canonical and isolated.
func TestGlobalNetworkReleaseAuthorityCodecRoundTrip(t *testing.T) {
	authority := validGlobalNetworkReleaseAuthority(t)
	payload, digest, err := encodeGlobalNetworkReleaseAuthority(authority)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if digest != digestGlobalNetworkReleasePayload(payload) {
		t.Fatalf("digest = %q", digest)
	}
	decoded, err := decodeGlobalNetworkReleaseAuthority(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded.Root.CertificatePEM[0] ^= 1
	decoded.LoopbackTargets[0].ObservationFingerprint = strings.Repeat("e", 64)
	again, err := decodeGlobalNetworkReleaseAuthority(payload)
	if err != nil {
		t.Fatalf("second decode: %v", err)
	}
	if again.Root.CertificatePEM[0] == decoded.Root.CertificatePEM[0] || again.LoopbackTargets[0].ObservationFingerprint == decoded.LoopbackTargets[0].ObservationFingerprint {
		t.Fatal("decoded authority aliases caller-owned memory")
	}
}

// TestGlobalNetworkReleaseAuthorityCodecRejectsNoncanonicalPayloads verifies strict durable JSON decoding.
func TestGlobalNetworkReleaseAuthorityCodecRejectsNoncanonicalPayloads(t *testing.T) {
	payload, _, err := encodeGlobalNetworkReleaseAuthority(validGlobalNetworkReleaseAuthority(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"unknown":      payload[:len(payload)-1] + `,"unknown":true}`,
		"trailing":     payload + " {}",
		"noncanonical": strings.Replace(payload, `,"policy":`, `, "policy":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGlobalNetworkReleaseAuthority(value); err == nil {
				t.Fatal("decode unexpectedly succeeded")
			}
		})
	}
}

// TestGlobalNetworkReleasePlanPhaseValidate verifies the initial and future persisted phases are recognized.
func TestGlobalNetworkReleasePlanPhaseValidate(t *testing.T) {
	for _, phase := range []GlobalNetworkReleasePlanPhase{
		GlobalNetworkReleasePlanPhaseRuntimeRelease,
		GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver,
		GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks,
		GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership,
		GlobalNetworkReleasePlanPhaseProjection,
	} {
		if err := phase.Validate(); err != nil {
			t.Fatalf("%q: %v", phase, err)
		}
	}
	if err := GlobalNetworkReleasePlanPhase("foreign").Validate(); err == nil {
		t.Fatal("foreign phase unexpectedly accepted")
	}
}
