package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// TestNewProjectLifecycleCoordinatorLeavesManagedLaunchDisabled keeps the ordinary GoForj development launch as the production default.
func TestNewProjectLifecycleCoordinatorLeavesManagedLaunchDisabled(t *testing.T) {
	coordinator := NewProjectLifecycleCoordinator(
		&state.Store{},
		&state.OperationJournal{},
		projectprocess.New(projectprocess.Options{}),
		projectLifecycleTestRouteReconciler{},
	)
	t.Cleanup(coordinator.cancel)

	if coordinator.newManagedLaunch != nil {
		t.Fatal("NewProjectLifecycleCoordinator() configured a managed launch context")
	}
}

// TestNewHarborProjectSessionWithTicketHashesTheExactEncodedProof prevents a raw-byte/hex-string credential mismatch.
func TestNewHarborProjectSessionWithTicketHashesTheExactEncodedProof(t *testing.T) {
	session, ticket, err := newHarborProjectSessionWithTicket("project-orders", "/workspace/orders", time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("newHarborProjectSessionWithTicket() error = %v", err)
	}
	if len(ticket) != 64 || strings.ToLower(ticket) != ticket {
		t.Fatalf("launch ticket = %q, want 64 lowercase hexadecimal characters", ticket)
	}
	digest := sha256.Sum256([]byte(ticket))
	if session.CredentialDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("credential digest = %q, want hash of exact launch ticket", session.CredentialDigest)
	}
	if session.State != domain.SessionPlanned || session.Generation != 1 {
		t.Fatalf("session launch state = %q generation %d, want planned generation 1", session.State, session.Generation)
	}
}

// TestPrepareLaunchSessionBuildsFinalContextBeforePersistence verifies descriptor and generation fencing at the process boundary.
func TestPrepareLaunchSessionBuildsFinalContextBeforePersistence(t *testing.T) {
	coordinator := &ProjectLifecycleCoordinator{newManagedLaunch: newHarborProjectSessionWithTicket}
	digest := strings.Repeat("d", 64)
	session, launch, err := coordinator.prepareLaunchSession(
		"project-orders",
		"/workspace/orders",
		time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC),
		projectprocess.ProjectDescriptorObservation{TopologyDigest: digest},
	)
	if err != nil {
		t.Fatalf("prepareLaunchSession() error = %v", err)
	}
	if launch == nil {
		t.Fatal("prepareLaunchSession() returned no managed context")
	}
	if launch.ProjectID != session.ProjectID || launch.SessionID != session.ID || launch.ProjectRoot != "/workspace/orders" {
		t.Fatalf("launch identity = %#v, want session identity and canonical root", launch)
	}
	if launch.ExpectedSessionGeneration != session.Generation+1 || launch.DescriptorDigest != digest || launch.Ticket == "" {
		t.Fatalf("launch fence = %#v, want next generation, descriptor digest, and ticket", launch)
	}
	if err := launch.Validate(); err != nil {
		t.Fatalf("launch context validation = %v", err)
	}
}
