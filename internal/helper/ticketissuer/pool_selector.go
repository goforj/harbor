package ticketissuer

import (
	"context"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
)

const poolSelectorIdentityCount = 8

// PoolSelector derives one safe production pool solely from fresh native observations.
type PoolSelector struct {
	loopback  LoopbackObserver
	conflicts ConflictObserver
}

// NewPoolSelector creates a selector from explicit assignment and host-conflict observers.
func NewPoolSelector(loopbackObserver LoopbackObserver, conflicts ConflictObserver) *PoolSelector {
	if loopbackObserver == nil || conflicts == nil {
		panic("ticketissuer.NewPoolSelector requires both native observers")
	}
	return &PoolSelector{loopback: loopbackObserver, conflicts: conflicts}
}

// NewDefaultPoolSelector creates a selector bound to the current platform's native observers.
func NewDefaultPoolSelector() *PoolSelector {
	return NewPoolSelector(loopback.New(), nativeConflictObserver{})
}

// Select scans every production candidate at most once in installation-stable wraparound order.
func (selector *PoolSelector) Select(
	ctx context.Context,
	installationID identity.InstallationID,
	requesterIdentity string,
) (identity.PoolSelection, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return identity.PoolSelection{}, err
	}
	startOffset, err := identity.DefaultPoolStartOffset(installationID)
	if err != nil {
		return identity.PoolSelection{}, fmt.Errorf("select default helper pool: %w", err)
	}
	prefixes := identity.DefaultPoolPrefixes()
	startIndex := int(startOffset % uint64(len(prefixes)))
	exhaustion := &identity.PoolSelectionExhaustionError{
		CandidatePools: len(prefixes),
		StartIndex:     startIndex,
	}

	for offset := range len(prefixes) {
		if err := ctx.Err(); err != nil {
			return identity.PoolSelection{}, err
		}
		prefix := prefixes[(startIndex+offset)%len(prefixes)]
		observations := make([]identity.PoolAddressObservation, poolSelectorIdentityCount)
		address := prefix.Addr()
		assignmentBlocked := false
		for index := range observations {
			if err := ctx.Err(); err != nil {
				return identity.PoolSelection{}, err
			}
			observed, err := selector.loopback.Observe(ctx, address)
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf(
					"select default helper pool: %w",
					NewPoolObservationError(PoolObservationAssignment, address, err),
				)
			}
			if observed.Address != address {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: assignment observation address %s does not match %s", observed.Address, address)
			}
			fingerprint, err := observed.Fingerprint()
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: fingerprint assignment %s: %w", address, err)
			}
			assignmentState := identity.PoolAssignmentAbsent
			if observed.State != loopback.StateAbsent {
				assignmentState = identity.PoolAssignmentPresent
				assignmentBlocked = true
			}
			observations[index] = identity.PoolAddressObservation{
				Address:            address,
				AssignmentState:    assignmentState,
				AssignmentEvidence: fingerprint,
			}
			address = address.Next()
		}
		if assignmentBlocked {
			exhaustion.AssignmentBlockedPools++
			continue
		}

		hostConflictBlocked := false
		indeterminate := false
		for index := range observations {
			if err := ctx.Err(); err != nil {
				return identity.PoolSelection{}, err
			}
			address := observations[index].Address
			request, err := hostconflict.NewPreAssignmentRequest(address, nil)
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: construct route-only request %s: %w", address, err)
			}
			observed, err := selector.conflicts.Observe(ctx, request, requesterIdentity)
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf(
					"select default helper pool: %w",
					NewPoolObservationError(PoolObservationHostConflicts, address, err),
				)
			}
			if observed.Request.Purpose() != request.Purpose() ||
				observed.Request.Candidate() != request.Candidate() ||
				!slices.Equal(observed.Request.Requirements(), request.Requirements()) {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: host-conflict observation does not match route-only request for %s", address)
			}
			assessment, err := observed.Classify()
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: classify host conflicts %s: %w", address, err)
			}
			fingerprint, err := observed.Fingerprint()
			if err != nil {
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: fingerprint host conflicts %s: %w", address, err)
			}

			state := identity.PoolHostConflictSafe
			switch assessment.State {
			case hostconflict.StateSafe:
			case hostconflict.StateConflict:
				state = identity.PoolHostConflictPresent
				hostConflictBlocked = true
			case hostconflict.StateIndeterminate:
				state = identity.PoolHostConflictIndeterminate
				indeterminate = true
			default:
				return identity.PoolSelection{}, fmt.Errorf("select default helper pool: host-conflict state for %s is %q", address, assessment.State)
			}
			observations[index].HostConflictState = state
			observations[index].HostConflictEvidence = fingerprint
		}
		if hostConflictBlocked {
			exhaustion.HostConflictBlockedPools++
		}
		if indeterminate {
			exhaustion.IndeterminatePools++
		}
		if hostConflictBlocked || indeterminate {
			continue
		}

		selection, err := identity.SelectPool(identity.PoolSelectionRequest{
			Candidates: []identity.PoolCandidateObservation{{
				Prefix:    prefix,
				Addresses: observations,
			}},
			StartOffset: startOffset,
		})
		if err != nil {
			return identity.PoolSelection{}, fmt.Errorf("select default helper pool: validate selected candidate: %w", err)
		}
		return selection, nil
	}

	return identity.PoolSelection{}, exhaustion
}
