package harbordruntime

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/goforj/harbor/internal/domain"
)

var (
	// ErrManagedPublicationSessionOpen means a project already has an active publication observation stream.
	ErrManagedPublicationSessionOpen = errors.New("managed publication session is already open")
	// ErrManagedPublicationFenceNotFound means an observation referenced no current publication stream.
	ErrManagedPublicationFenceNotFound = errors.New("managed publication fence is not open")
	// ErrManagedPublicationFenceMismatch means an observation was sent for a different session generation.
	ErrManagedPublicationFenceMismatch = errors.New("managed publication fence does not match the open session")
)

// ManagedPublicationRegistry retains the latest complete publication observation for each attached session.
//
// The registry is intentionally ephemeral. It is a handoff point between a future authenticated managed-session
// adapter and the pure native-route planner; durable reservations and session identity remain the authority for
// deciding whether a later route plan is still valid.
type ManagedPublicationRegistry struct {
	mu      sync.RWMutex
	entries map[domain.ProjectID]managedPublicationRegistryEntry
}

// managedPublicationRegistryEntry stores one exact session fence and its complete replacement observation.
type managedPublicationRegistryEntry struct {
	fence        ManagedPublicationFence
	publications []ManagedEndpointPublication
}

// NewManagedPublicationRegistry creates an empty process-local publication registry.
func NewManagedPublicationRegistry() *ManagedPublicationRegistry {
	return &ManagedPublicationRegistry{entries: make(map[domain.ProjectID]managedPublicationRegistryEntry)}
}

// Open reserves one project publication stream for an attached session.
//
// Attachment authentication is deliberately outside this registry. The caller must have already proved the peer,
// ticket, checkout, descriptor digest, and active-session exclusivity before presenting the attached session here.
func (registry *ManagedPublicationRegistry) Open(session domain.ProjectSession) (ManagedPublicationFence, error) {
	if registry == nil {
		return ManagedPublicationFence{}, errors.New("managed publication registry is required")
	}
	if err := session.Validate(); err != nil {
		return ManagedPublicationFence{}, fmt.Errorf("validate managed publication session: %w", err)
	}
	if session.State != domain.SessionAttached {
		return ManagedPublicationFence{}, fmt.Errorf("managed publication session %q is %q, not attached", session.ID, session.State)
	}
	fence := ManagedPublicationFence{
		ProjectID:         session.ProjectID,
		SessionID:         session.ID,
		SessionGeneration: session.Generation,
	}
	if err := fence.Validate(); err != nil {
		return ManagedPublicationFence{}, err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.entries == nil {
		registry.entries = make(map[domain.ProjectID]managedPublicationRegistryEntry)
	}
	if _, exists := registry.entries[fence.ProjectID]; exists {
		return ManagedPublicationFence{}, fmt.Errorf("open managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationSessionOpen)
	}
	registry.entries[fence.ProjectID] = managedPublicationRegistryEntry{
		fence:        fence,
		publications: []ManagedEndpointPublication{},
	}
	return fence, nil
}

// Replace atomically replaces every publication currently observed for one exact open session.
func (registry *ManagedPublicationRegistry) Replace(fence ManagedPublicationFence, publications []ManagedEndpointPublication) error {
	if registry == nil {
		return errors.New("managed publication registry is required")
	}
	if err := fence.Validate(); err != nil {
		return err
	}
	canonical, err := canonicalManagedPublicationSet(fence, publications)
	if err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, found := registry.entries[fence.ProjectID]
	if !found {
		return fmt.Errorf("replace managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceNotFound)
	}
	if entry.fence != fence {
		return fmt.Errorf("replace managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceMismatch)
	}
	entry.publications = canonical
	registry.entries[fence.ProjectID] = entry
	return nil
}

// Snapshot returns a defensive copy of the complete latest observation for one exact open session.
func (registry *ManagedPublicationRegistry) Snapshot(fence ManagedPublicationFence) ([]ManagedEndpointPublication, error) {
	if registry == nil {
		return nil, errors.New("managed publication registry is required")
	}
	if err := fence.Validate(); err != nil {
		return nil, err
	}

	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, found := registry.entries[fence.ProjectID]
	if !found {
		return nil, fmt.Errorf("snapshot managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceNotFound)
	}
	if entry.fence != fence {
		return nil, fmt.Errorf("snapshot managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceMismatch)
	}
	return slices.Clone(entry.publications), nil
}

// Close removes one exact open publication stream and rejects stale session cleanup.
func (registry *ManagedPublicationRegistry) Close(fence ManagedPublicationFence) error {
	if registry == nil {
		return errors.New("managed publication registry is required")
	}
	if err := fence.Validate(); err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry, found := registry.entries[fence.ProjectID]
	if !found {
		return fmt.Errorf("close managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceNotFound)
	}
	if entry.fence != fence {
		return fmt.Errorf("close managed publication project %q: %w", fence.ProjectID, ErrManagedPublicationFenceMismatch)
	}
	delete(registry.entries, fence.ProjectID)
	return nil
}

// canonicalManagedPublicationSet validates one complete replacement and orders it for deterministic observations.
func canonicalManagedPublicationSet(fence ManagedPublicationFence, publications []ManagedEndpointPublication) ([]ManagedEndpointPublication, error) {
	input := ManagedPublicationPlanInput{Fence: fence, Publications: publications}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	canonical := make([]ManagedEndpointPublication, len(publications))
	copy(canonical, publications)
	slices.SortFunc(canonical, func(left, right ManagedEndpointPublication) int {
		if left.EndpointID < right.EndpointID {
			return -1
		}
		if left.EndpointID > right.EndpointID {
			return 1
		}
		return 0
	})
	for index := 1; index < len(canonical); index++ {
		if canonical[index-1].EndpointID == canonical[index].EndpointID {
			return nil, fmt.Errorf("managed publication endpoint %q is duplicated", canonical[index].EndpointID)
		}
	}
	return canonical, nil
}
