package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
)

const networkResolverSetupPlanSingletonID = 1

// resolverSetupSourceOwnership derives the only schema-one authority that may be upgraded to the target record.
func resolverSetupSourceOwnership(target ownership.Record) (ownership.Record, string, error) {
	if err := target.Validate(); err != nil {
		return ownership.Record{}, "", fmt.Errorf("resolver setup target ownership: %w", err)
	}
	if target.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return ownership.Record{}, "", fmt.Errorf(
			"resolver setup target ownership schema is %d, want %d",
			target.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	source := target
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	fingerprint, err := source.Fingerprint()
	if err != nil {
		return ownership.Record{}, "", fmt.Errorf("fingerprint resolver setup source ownership: %w", err)
	}
	return source, fingerprint, nil
}

// networkResolverSetupPlanFromModel reconstructs the exact operation-bound target policy from one durable row.
func networkResolverSetupPlanFromModel(
	row models.NetworkResolverSetupPlan,
	operation OperationRecord,
) (ticketissuer.ResolverPlan, domain.Sequence, error) {
	key := operation.Operation.ID
	if row.Id != networkResolverSetupPlanSingletonID {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("singleton ID is %d, expected %d", row.Id, networkResolverSetupPlanSingletonID),
		)
	}
	if row.OperationId != string(operation.Operation.ID) {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, fmt.Errorf("operation ID does not match its owner"))
	}
	if row.OperationRevision <= 0 || domain.Sequence(row.OperationRevision) > domain.MaximumSequence {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("operation revision is outside the durable sequence range"),
		)
	}
	operationRevision := domain.Sequence(row.OperationRevision)
	if operationRevision != operation.Revision {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("plan revision is %d, operation revision is %d", operationRevision, operation.Revision),
		)
	}
	if row.NetworkStateId != networkStateSingletonID {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("network state ID is %d, expected %d", row.NetworkStateId, networkStateSingletonID),
		)
	}
	if row.NetworkRevision <= 0 || domain.Sequence(row.NetworkRevision) > domain.MaximumSequence {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("network revision is outside the durable sequence range"),
		)
	}
	networkRevision := domain.Sequence(row.NetworkRevision)
	targetGeneration, err := positiveNetworkGeneration(
		"resolver setup target ownership generation",
		row.TargetOwnershipGeneration,
	)
	if err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, err)
	}
	target := ownership.Record{
		SchemaVersion:            uint32(row.TargetOwnershipSchemaVersion),
		InstallationID:           row.TargetInstallationId,
		OwnerIdentity:            row.TargetOwnerIdentity,
		Generation:               targetGeneration,
		LoopbackPoolPrefix:       row.TargetLoopbackPoolPrefix,
		NetworkPolicyFingerprint: row.TargetNetworkPolicyFingerprint,
		TicketVerifierKey:        row.TargetTicketVerifierKey,
	}
	if err := target.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, err)
	}
	_, expectedSourceFingerprint, err := resolverSetupSourceOwnership(target)
	if err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, err)
	}
	if row.SourceOwnershipFingerprint != expectedSourceFingerprint {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(
			key,
			fmt.Errorf("source ownership fingerprint does not match the target-derived schema-one record"),
		)
	}

	policy, err := networkResolverSetupPolicyFromModel(row)
	if err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, err)
	}
	plan := ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposeSetup,
		Operation:                          operation.Operation,
		OperationRevision:                  operation.Revision,
		CheckpointRevision:                 0,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhaseSetupApproval,
		Mutation:                           helper.OperationEnsureResolver,
		ExpectedSourceOwnershipFingerprint: row.SourceOwnershipFingerprint,
		TargetOwnership:                    target,
		Policy:                             policy,
	}
	if err := plan.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, 0, corruptNetworkResolverSetupPlan(key, err)
	}
	return plan, networkRevision, nil
}

// networkResolverSetupPolicyFromModel reconstructs every canonical policy field without applying defaults.
func networkResolverSetupPolicyFromModel(row models.NetworkResolverSetupPlan) (networkpolicy.Policy, error) {
	dnsAdvertised, err := networkAddressPortFromModel(
		"resolver setup DNS advertised listener",
		row.PolicyDnsAdvertisedAddress,
		row.PolicyDnsAdvertisedPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	dnsBind, err := networkAddressPortFromModel(
		"resolver setup DNS bind listener",
		row.PolicyDnsBindAddress,
		row.PolicyDnsBindPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	httpAdvertised, err := networkAddressPortFromModel(
		"resolver setup HTTP advertised listener",
		row.PolicyHttpAdvertisedAddress,
		row.PolicyHttpAdvertisedPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	httpBind, err := networkAddressPortFromModel(
		"resolver setup HTTP bind listener",
		row.PolicyHttpBindAddress,
		row.PolicyHttpBindPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	httpsAdvertised, err := networkAddressPortFromModel(
		"resolver setup HTTPS advertised listener",
		row.PolicyHttpsAdvertisedAddress,
		row.PolicyHttpsAdvertisedPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	httpsBind, err := networkAddressPortFromModel(
		"resolver setup HTTPS bind listener",
		row.PolicyHttpsBindAddress,
		row.PolicyHttpsBindPort,
	)
	if err != nil {
		return networkpolicy.Policy{}, err
	}
	policy := networkpolicy.Policy{
		Suffix:               row.PolicySuffix,
		AuthorityFingerprint: row.PolicyAuthorityFingerprint,
		Mechanisms: networkpolicy.Mechanisms{
			Resolver: networkpolicy.ResolverMechanism(row.PolicyResolverMechanism),
			LowPorts: networkpolicy.LowPortMechanism(row.PolicyLowPortsMechanism),
			Trust:    networkpolicy.TrustMechanism(row.PolicyTrustMechanism),
		},
		DNS:   networkpolicy.Listener{Advertised: dnsAdvertised, Bind: dnsBind},
		HTTP:  networkpolicy.Listener{Advertised: httpAdvertised, Bind: httpBind},
		HTTPS: networkpolicy.Listener{Advertised: httpsAdvertised, Bind: httpsBind},
	}
	if err := policy.Validate(); err != nil {
		return networkpolicy.Policy{}, fmt.Errorf("resolver setup policy: %w", err)
	}
	return policy, nil
}

// networkResolverSetupPlanModel creates the exact immutable row owned by one approval revision.
func networkResolverSetupPlanModel(
	operation OperationRecord,
	request StageNetworkResolverSetupRequest,
) (models.NetworkResolverSetupPlan, error) {
	operationRevision, err := sequenceToModelInt("resolver setup plan operation revision", operation.Revision, false)
	if err != nil {
		return models.NetworkResolverSetupPlan{}, err
	}
	networkRevision, err := sequenceToModelInt("resolver setup plan network revision", request.ExpectedNetworkRevision, false)
	if err != nil {
		return models.NetworkResolverSetupPlan{}, err
	}
	targetGeneration, err := unsignedToModelInt(
		"resolver setup target ownership generation",
		request.TargetOwnership.Generation,
		false,
	)
	if err != nil {
		return models.NetworkResolverSetupPlan{}, err
	}
	return models.NetworkResolverSetupPlan{
		Id:                             networkResolverSetupPlanSingletonID,
		OperationId:                    string(operation.Operation.ID),
		OperationRevision:              operationRevision,
		NetworkStateId:                 networkStateSingletonID,
		NetworkRevision:                networkRevision,
		SourceOwnershipFingerprint:     request.ExpectedSourceOwnershipFingerprint,
		TargetOwnershipSchemaVersion:   int(request.TargetOwnership.SchemaVersion),
		TargetInstallationId:           request.TargetOwnership.InstallationID,
		TargetOwnerIdentity:            request.TargetOwnership.OwnerIdentity,
		TargetOwnershipGeneration:      targetGeneration,
		TargetLoopbackPoolPrefix:       request.TargetOwnership.LoopbackPoolPrefix,
		TargetNetworkPolicyFingerprint: request.TargetOwnership.NetworkPolicyFingerprint,
		TargetTicketVerifierKey:        request.TargetOwnership.TicketVerifierKey,
		PolicySuffix:                   request.Policy.Suffix,
		PolicyAuthorityFingerprint:     request.Policy.AuthorityFingerprint,
		PolicyResolverMechanism:        string(request.Policy.Mechanisms.Resolver),
		PolicyLowPortsMechanism:        string(request.Policy.Mechanisms.LowPorts),
		PolicyTrustMechanism:           string(request.Policy.Mechanisms.Trust),
		PolicyDnsAdvertisedAddress:     request.Policy.DNS.Advertised.Addr().String(),
		PolicyDnsAdvertisedPort:        int(request.Policy.DNS.Advertised.Port()),
		PolicyDnsBindAddress:           request.Policy.DNS.Bind.Addr().String(),
		PolicyDnsBindPort:              int(request.Policy.DNS.Bind.Port()),
		PolicyHttpAdvertisedAddress:    request.Policy.HTTP.Advertised.Addr().String(),
		PolicyHttpAdvertisedPort:       int(request.Policy.HTTP.Advertised.Port()),
		PolicyHttpBindAddress:          request.Policy.HTTP.Bind.Addr().String(),
		PolicyHttpBindPort:             int(request.Policy.HTTP.Bind.Port()),
		PolicyHttpsAdvertisedAddress:   request.Policy.HTTPS.Advertised.Addr().String(),
		PolicyHttpsAdvertisedPort:      int(request.Policy.HTTPS.Advertised.Port()),
		PolicyHttpsBindAddress:         request.Policy.HTTPS.Bind.Addr().String(),
		PolicyHttpsBindPort:            int(request.Policy.HTTPS.Bind.Port()),
	}, nil
}

// corruptNetworkResolverSetupPlan applies one typed corruption identity to every durable resolver-plan mismatch.
func corruptNetworkResolverSetupPlan(operationID domain.OperationID, cause error) error {
	return corruptStateError("network resolver setup plan", string(operationID), cause)
}
