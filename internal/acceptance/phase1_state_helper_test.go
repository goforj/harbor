//go:build phase1acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

const (
	phase1StateHelperModeEnvironment      = "HARBOR_PHASE1_STATE_HELPER_MODE"
	phase1StateHelperProjectEnvironment   = "HARBOR_PHASE1_STATE_HELPER_PROJECT"
	phase1StateHelperOperationEnvironment = "HARBOR_PHASE1_STATE_HELPER_OPERATION"
	phase1StateHelperIntentEnvironment    = "HARBOR_PHASE1_STATE_HELPER_INTENT"
	phase1StateHelperOutputEnvironment    = "HARBOR_PHASE1_STATE_HELPER_OUTPUT"
	phase1StateHelperSeedMode             = "seed"
	phase1StateHelperInspectMode          = "inspect"
	phase1StateHelperAuthorityMode        = "authority"
)

// phase1StateAccess assembles the same repositories and state services used by harbord without starting network authority.
type phase1StateAccess struct {
	connections *database.Connections
	store       *state.Store
	journal     *state.OperationJournal
}

// phase1DurableEvidence contains only operation identities and lifecycle facts needed by the acceptance assertion.
type phase1DurableEvidence struct {
	Sequence             domain.Sequence         `json:"sequence"`
	ProjectID            domain.ProjectID        `json:"project_id"`
	ProjectPresent       bool                    `json:"project_present"`
	ProjectCount         int                     `json:"project_count"`
	NetworkInitialized   bool                    `json:"network_initialized"`
	ActiveOperationCount int                     `json:"active_operation_count"`
	OperationID          domain.OperationID      `json:"operation_id"`
	IntentID             domain.IntentID         `json:"intent_id"`
	OperationState       domain.OperationState   `json:"operation_state"`
	OperationRevision    domain.Sequence         `json:"operation_revision"`
	TransitionStates     []domain.OperationState `json:"transition_states"`
	TransitionSequences  []domain.Sequence       `json:"transition_sequences"`
}

// phase1AuthorityEvidence contains only the non-secret public identity needed to prove CA continuity.
type phase1AuthorityEvidence struct {
	Fingerprint string `json:"fingerprint"`
}

// TestPhase1StateSubprocess seeds or inspects durable state only when the parent acceptance binary selects its private mode.
func TestPhase1StateSubprocess(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv(phase1StateHelperModeEnvironment))
	if mode == "" {
		t.Skip("phase 1 state subprocess mode is not selected")
	}
	if mode == phase1StateHelperAuthorityMode {
		evidence, err := phase1InspectAuthority(t.Context())
		if err != nil {
			t.Fatalf("inspect persisted public authority through production APIs: %v", err)
		}
		output := strings.TrimSpace(os.Getenv(phase1StateHelperOutputEnvironment))
		if err := phase1WriteHelperEvidence(output, evidence); err != nil {
			t.Fatalf("write public authority evidence: %v", err)
		}
		return
	}

	projectID := domain.ProjectID(strings.TrimSpace(os.Getenv(phase1StateHelperProjectEnvironment)))
	operationID := domain.OperationID(strings.TrimSpace(os.Getenv(phase1StateHelperOperationEnvironment)))
	intentID := domain.IntentID(strings.TrimSpace(os.Getenv(phase1StateHelperIntentEnvironment)))
	if err := projectID.Validate(); err != nil {
		t.Fatalf("validate helper project identity: %v", err)
	}
	if err := operationID.Validate(); err != nil {
		t.Fatalf("validate helper operation identity: %v", err)
	}
	if err := intentID.Validate(); err != nil {
		t.Fatalf("validate helper intent identity: %v", err)
	}

	access, err := phase1OpenStateAccess()
	if err != nil {
		t.Fatalf("open production state access: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), phase1CommandTimeout)
		defer cancel()
		if err := access.connections.Close(ctx); err != nil {
			t.Errorf("close production state access: %v", err)
		}
	})

	switch mode {
	case phase1StateHelperSeedMode:
		operation, err := domain.NewOperation(
			operationID,
			intentID,
			domain.OperationKindProjectUnregister,
			projectID,
			time.Now().UTC().Round(0),
		)
		if err != nil {
			t.Fatalf("construct queued unregister operation: %v", err)
		}
		record, err := access.journal.Enqueue(t.Context(), operation)
		if err != nil {
			t.Fatalf("enqueue queued unregister operation through production journal: %v", err)
		}
		if record.Operation.ID != operationID || record.Operation.State != domain.OperationQueued {
			t.Fatalf("queued operation readback = %#v", record)
		}
	case phase1StateHelperInspectMode:
		evidence, err := phase1InspectDurableState(t.Context(), access, projectID, intentID)
		if err != nil {
			t.Fatalf("inspect durable state through production APIs: %v", err)
		}
		output := strings.TrimSpace(os.Getenv(phase1StateHelperOutputEnvironment))
		if err := phase1WriteHelperEvidence(output, evidence); err != nil {
			t.Fatalf("write durable state evidence: %v", err)
		}
	default:
		t.Fatalf("unsupported phase 1 state subprocess mode %q", mode)
	}
}

// phase1InspectAuthority opens existing trust material without bootstrapping and emits only its public fingerprint.
func phase1InspectAuthority(ctx context.Context) (evidence phase1AuthorityEvidence, inspectErr error) {
	store, err := materialstore.OpenDefault()
	if err != nil {
		return phase1AuthorityEvidence{}, err
	}
	defer func() {
		inspectErr = errors.Join(inspectErr, store.Close())
	}()

	// Open deliberately refuses missing material, so inspection cannot create the identity it is meant to observe.
	manager, err := certificates.Open(ctx, store, certificates.Config{})
	if err != nil {
		return phase1AuthorityEvidence{}, err
	}
	root, err := manager.PublicRoot()
	if err != nil {
		return phase1AuthorityEvidence{}, err
	}
	if err := phase1ValidateAuthorityFingerprint(root.Fingerprint); err != nil {
		return phase1AuthorityEvidence{}, err
	}
	return phase1AuthorityEvidence{Fingerprint: root.Fingerprint}, nil
}

// phase1ValidateAuthorityFingerprint requires the canonical public SHA-256 identity emitted by certificate management.
func phase1ValidateAuthorityFingerprint(fingerprint string) error {
	if fingerprint != strings.ToLower(fingerprint) {
		return fmt.Errorf("public authority fingerprint must be lowercase")
	}
	digest, err := hex.DecodeString(fingerprint)
	if err != nil {
		return fmt.Errorf("decode public authority fingerprint: %w", err)
	}
	if len(digest) != sha256.Size {
		return fmt.Errorf("public authority fingerprint contains %d bytes, want %d", len(digest), sha256.Size)
	}
	return nil
}

// phase1OpenStateAccess constructs named-database repositories exactly as the generated daemon Wire graph does.
func phase1OpenStateAccess() (*phase1StateAccess, error) {
	if _, err := state.ConfigureDatabase(); err != nil {
		return nil, err
	}
	connections := database.NewConnections(inspects.NewManager())
	mutations := state.NewMutationCoordinator(connections)
	harborState := models.NewHarborStateRepo(connections)
	store := state.NewStore(
		harborState,
		models.NewProjectRepo(connections),
		models.NewNetworkStateRepo(connections),
		mutations,
	)
	journal := state.NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		harborState,
		mutations,
	)
	return &phase1StateAccess{connections: connections, store: store, journal: journal}, nil
}

// phase1InspectDurableState reads the client projection and complete operation history through production state boundaries.
func phase1InspectDurableState(
	ctx context.Context,
	access *phase1StateAccess,
	projectID domain.ProjectID,
	intentID domain.IntentID,
) (phase1DurableEvidence, error) {
	runtimeState, err := access.store.RuntimeState(ctx)
	if err != nil {
		return phase1DurableEvidence{}, err
	}
	if err := runtimeState.Validate(); err != nil {
		return phase1DurableEvidence{}, fmt.Errorf("validate runtime state: %w", err)
	}
	record, err := access.journal.OperationByIntent(ctx, intentID)
	if err != nil {
		return phase1DurableEvidence{}, err
	}
	transitions, err := access.journal.Transitions(ctx, record.Operation.ID)
	if err != nil {
		return phase1DurableEvidence{}, err
	}
	active, err := access.journal.ActiveOperations(ctx)
	if err != nil {
		return phase1DurableEvidence{}, err
	}

	evidence := phase1DurableEvidence{
		Sequence:             runtimeState.Snapshot.Sequence,
		ProjectID:            record.Operation.ProjectID,
		ProjectCount:         len(runtimeState.Snapshot.Projects),
		NetworkInitialized:   runtimeState.NetworkInitialized,
		ActiveOperationCount: len(active),
		OperationID:          record.Operation.ID,
		IntentID:             record.Operation.IntentID,
		OperationState:       record.Operation.State,
		OperationRevision:    record.Revision,
		TransitionStates:     make([]domain.OperationState, 0, len(transitions)),
		TransitionSequences:  make([]domain.Sequence, 0, len(transitions)),
	}
	for _, project := range runtimeState.Snapshot.Projects {
		if project.ID == projectID {
			evidence.ProjectPresent = true
			break
		}
	}
	for _, transition := range transitions {
		evidence.TransitionStates = append(evidence.TransitionStates, transition.State)
		evidence.TransitionSequences = append(evidence.TransitionSequences, transition.Sequence)
	}
	return evidence, nil
}

// phase1WriteHelperEvidence publishes one exclusive non-secret helper result for its parent process.
func phase1WriteHelperEvidence(path string, evidence any) (writeErr error) {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("state evidence path %q must be absolute", path)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		writeErr = errors.Join(writeErr, file.Close())
		if writeErr != nil {
			_ = os.Remove(path)
		}
	}()
	if err := json.NewEncoder(file).Encode(evidence); err != nil {
		return err
	}
	return file.Sync()
}

// phase1RunAuthoritySubprocess reads the persisted public CA identity while no daemon owns its material store.
func phase1RunAuthoritySubprocess(
	ctx context.Context,
	sandbox phase1Sandbox,
) (phase1AuthorityEvidence, error) {
	executable, err := os.Executable()
	if err != nil {
		return phase1AuthorityEvidence{}, err
	}
	output := filepath.Join(sandbox.root, "authority-evidence.json")
	_ = os.Remove(output)
	defer os.Remove(output)
	overrides := map[string]string{
		phase1StateHelperModeEnvironment:   phase1StateHelperAuthorityMode,
		phase1StateHelperOutputEnvironment: output,
	}
	command := exec.CommandContext(
		ctx,
		executable,
		"-test.run=^TestPhase1StateSubprocess$",
		"-test.count=1",
	)
	command.Dir = sandbox.root
	command.Env = phase1MergedEnvironment(sandbox.environment, overrides)
	stdout := new(phase1BoundedLog)
	stderr := new(phase1BoundedLog)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return phase1AuthorityEvidence{}, fmt.Errorf(
			"authority helper failed: %w: %s %s",
			err,
			strings.TrimSpace(string(stdout.snapshot())),
			strings.TrimSpace(string(stderr.snapshot())),
		)
	}

	var evidence phase1AuthorityEvidence
	if err := phase1DecodeHelperEvidence(output, &evidence); err != nil {
		return phase1AuthorityEvidence{}, err
	}
	if err := phase1ValidateAuthorityFingerprint(evidence.Fingerprint); err != nil {
		return phase1AuthorityEvidence{}, err
	}
	return evidence, nil
}

// phase1RunStateSubprocess invokes the build-tagged test binary so no helper surface enters either shipping executable.
func phase1RunStateSubprocess(
	ctx context.Context,
	sandbox phase1Sandbox,
	mode string,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	intentID domain.IntentID,
) (phase1DurableEvidence, error) {
	executable, err := os.Executable()
	if err != nil {
		return phase1DurableEvidence{}, err
	}
	output := ""
	if mode == phase1StateHelperInspectMode {
		output = filepath.Join(sandbox.root, "state-"+string(intentID)+".json")
		_ = os.Remove(output)
		defer os.Remove(output)
	}
	overrides := map[string]string{
		phase1StateHelperModeEnvironment:      mode,
		phase1StateHelperProjectEnvironment:   string(projectID),
		phase1StateHelperOperationEnvironment: string(operationID),
		phase1StateHelperIntentEnvironment:    string(intentID),
		phase1StateHelperOutputEnvironment:    output,
	}
	command := exec.CommandContext(
		ctx,
		executable,
		"-test.run=^TestPhase1StateSubprocess$",
		"-test.count=1",
	)
	command.Dir = sandbox.root
	command.Env = phase1MergedEnvironment(sandbox.environment, overrides)
	stdout := new(phase1BoundedLog)
	stderr := new(phase1BoundedLog)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return phase1DurableEvidence{}, fmt.Errorf(
			"state helper %s failed: %w: %s %s",
			mode,
			err,
			strings.TrimSpace(string(stdout.snapshot())),
			strings.TrimSpace(string(stderr.snapshot())),
		)
	}
	if mode != phase1StateHelperInspectMode {
		return phase1DurableEvidence{}, nil
	}

	var evidence phase1DurableEvidence
	if err := phase1DecodeHelperEvidence(output, &evidence); err != nil {
		return phase1DurableEvidence{}, err
	}
	return evidence, nil
}

// phase1DecodeHelperEvidence accepts exactly one bounded JSON result from the private helper subprocess.
func phase1DecodeHelperEvidence(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("state helper evidence contains trailing data")
	}
	return nil
}
