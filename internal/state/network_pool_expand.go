package state

import (
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/poolpolicy"
	"gorm.io/gorm"
)

// expandNetworkSetupPoolInTransaction preserves all project authority while committing the independently observed host expansion.
func expandNetworkSetupPoolInTransaction(tx *gorm.DB, request CompleteNetworkSetupRequest, plan ticketissuer.PoolPlan) error {
	beforeRows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	before, initialized, err := networkRecordFromModels(beforeRows)
	if err != nil {
		return err
	}
	if !initialized || before.Pool.Prefix().String() != plan.PreviousPool {
		return fmt.Errorf("pool expansion source differs from durable network")
	}
	if err := poolpolicy.Expansion(before.Pool.Prefix(), plan.Pool.Prefix()); err != nil {
		return err
	}
	if request.At.Before(before.UpdatedAt) {
		return fmt.Errorf("pool expansion observation precedes current network revision")
	}
	projected, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return err
	}
	source := plan.Ownership
	source.LoopbackPoolPrefix = plan.PreviousPool
	if projected.Record != source {
		return fmt.Errorf("pool expansion source ownership changed")
	}
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return err
	}
	changed := tx.Model(&models.NetworkState{}).Where("id = ? AND revision = ?", networkStateSingletonID, int(before.Revision)).Updates(map[string]any{"pool_network": plan.Pool.Prefix().Addr().String(), "pool_prefix_length": plan.Pool.Prefix().Bits(), "revision": int(sequence), "updated_at": request.At})
	if err := requireOneMutation(changed, "expand network pool", "1"); err != nil {
		return err
	}
	// Candidate rows retain their IDs so any existing database references remain valid.
	for _, row := range beforeRows.Candidates {
		if err := requireOneMutation(tx.Model(&models.NetworkPoolCandidate{}).Where("id = ? AND ordinal = ?", row.Id, row.Ordinal).Update("ordinal", row.Ordinal+poolpolicy.Capacity), "stage expanded candidate", row.Address); err != nil {
			return err
		}
	}
	old := make(map[string]models.NetworkPoolCandidate, len(beforeRows.Candidates))
	for _, row := range beforeRows.Candidates {
		old[row.Address] = row
	}
	for index, address := range plan.Pool.Candidates() {
		if row, found := old[address.String()]; found {
			if err := requireOneMutation(tx.Model(&models.NetworkPoolCandidate{}).Where("id = ?", row.Id).Update("ordinal", index+1), "retain expanded candidate", row.Address); err != nil {
				return err
			}
		} else {
			row := models.NetworkPoolCandidate{NetworkStateId: networkStateSingletonID, Ordinal: index + 1, Address: address.String(), Generation: int(plan.Ownership.Generation)}
			if err := requireOneCreate(tx.Create(&row), "insert expanded candidate", address.String()); err != nil {
				return err
			}
		}
	}
	projection := tx.Model(&models.MachineOwnershipProjection{}).Where("id = ? AND record_fingerprint = ?", machineOwnershipProjectionSingletonID, projected.Fingerprint).Updates(map[string]any{"loopback_pool_prefix": plan.Ownership.LoopbackPoolPrefix, "record_fingerprint": request.ConfirmedOwnership.Fingerprint, "confirmed_at": request.At})
	if err := requireOneMutation(projection, "confirm expanded ownership", "1"); err != nil {
		return err
	}
	poolEvidence, err := networkSetupPoolCompletionEvidence(request)
	if err != nil {
		return err
	}
	for component, evidence := range map[NetworkSetupComponent]string{NetworkSetupComponentMachineOwnership: networkSetupOwnershipEvidencePrefix + request.ConfirmedOwnership.Fingerprint, NetworkSetupComponentLoopbackPool: poolEvidence} {
		change := tx.Model(&models.NetworkSetupEvidence{}).Where("network_state_id = ? AND component = ?", networkStateSingletonID, string(component)).Updates(map[string]any{"evidence": evidence, "verified_at": request.At})
		if err := requireOneMutation(change, "update expanded setup evidence", string(component)); err != nil {
			return err
		}
	}
	afterRows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	after, found, err := networkRecordFromModels(afterRows)
	if err != nil {
		return err
	}
	if !found || after.Pool.Prefix() != plan.Pool.Prefix() || after.Pool.Capacity() != poolpolicy.Capacity || after.Revision != sequence {
		return fmt.Errorf("expanded network readback differs from approved target")
	}
	if !reflect.DeepEqual(beforeRows.Leases, afterRows.Leases) || !reflect.DeepEqual(beforeRows.Endpoints, afterRows.Endpoints) || !reflect.DeepEqual(beforeRows.Releases, afterRows.Releases) || before.Stage != after.Stage || before.Ownership != after.Ownership || !reflect.DeepEqual(before.Reservations, after.Reservations) {
		return fmt.Errorf("pool expansion changed retained project authority")
	}
	return nil
}
