package state

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// NetworkAddressReuse identifies a cooled-down address and the next durable lease generation.
type NetworkAddressReuse struct {
	Address    netip.Addr
	Generation uint64
}

// ReusableNetworkAddresses reads completed, deleted-project releases without removing their quarantine proof.
// The caller must freshly observe each selected address and commit through ReplaceProjectNetwork.
func (store *Store) ReusableNetworkAddresses(ctx context.Context, revision domain.Sequence, at time.Time) ([]NetworkAddressReuse, error) {
	if _, err := sequenceToModelInt("expected network revision", revision, false); err != nil {
		return nil, err
	}
	if err := validateStoredTime("network address reuse observation", at); err != nil {
		return nil, err
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	builder, err := store.networkState.WithContext(ctx).Builder()
	if err != nil {
		return nil, fmt.Errorf("open network address reuse state: %w", err)
	}
	var candidates []NetworkAddressReuse
	err = builder.Transaction(func(tx *gorm.DB) error {
		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		network, initialized, err := networkRecordFromModels(rows)
		if err != nil {
			return err
		}
		if !initialized {
			return &NetworkNotInitializedError{}
		}
		if network.Revision != revision {
			return &NetworkRevisionConflictError{Expected: revision, Actual: network.Revision}
		}
		candidates = reusableNetworkAddresses(rows, network, at)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read reusable network addresses: %w", err)
	}
	return candidates, nil
}

// reusableNetworkAddresses keeps live projects, unfinished teardown, foreign ownership, and cooldowns blocked.
func reusableNetworkAddresses(rows networkModelRows, network NetworkRecord, at time.Time) []NetworkAddressReuse {
	projects := make(map[string]bool, len(rows.Projects))
	for _, project := range rows.Projects {
		projects[project.ProjectId] = true
	}
	completed := make(map[string]bool, len(rows.Releases))
	for _, release := range rows.Releases {
		if release.State == "completed" && !release.CompletedAt.After(at) {
			completed[release.SourceProjectId] = true
		}
	}
	candidates := []NetworkAddressReuse{}
	for _, lease := range rows.Leases {
		if lease.State != "quarantined" || projects[lease.SourceProjectId] || !completed[lease.SourceProjectId] {
			continue
		}
		if lease.ReuseAfter.After(at) || lease.OwnershipInstallationId != string(network.Ownership.InstallationID) || uint64(lease.OwnershipGeneration) > network.Ownership.Generation {
			continue
		}
		generation := uint64(lease.ReleaseGeneration.Int64) + 1
		if _, err := unsignedToModelInt("network lease reuse generation", generation, false); err != nil {
			continue
		}
		candidates = append(candidates, NetworkAddressReuse{Address: netip.MustParseAddr(lease.Address), Generation: generation})
	}
	slices.SortFunc(candidates, func(left, right NetworkAddressReuse) int { return left.Address.Compare(right.Address) })
	return candidates
}
