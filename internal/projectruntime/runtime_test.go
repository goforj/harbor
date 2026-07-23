package projectruntime

import (
	"context"

	"github.com/goforj/harbor/internal/domain"
)

// minimalRuntime proves the core contract can be implemented without importing a project adapter.
type minimalRuntime struct{}

// Prepare returns a neutral plan for the compile-time contract proof.
func (minimalRuntime) Prepare(context.Context, PreparationRequest) (Plan, error) {
	return Plan{}, nil
}

// Launch accepts a neutral launch request for the compile-time contract proof.
func (minimalRuntime) Launch(context.Context, LaunchRequest) (Handle, error) {
	return nil, nil
}

// Reset accepts a neutral reset request for the compile-time contract proof.
func (minimalRuntime) Reset(context.Context, ResetRequest) error {
	return nil
}

// Stop accepts neutral durable identities for the compile-time contract proof.
func (minimalRuntime) Stop(context.Context, domain.ProjectID, domain.SessionID) error {
	return nil
}

// ObservePriorProcess reports absent evidence for the compile-time contract proof.
func (minimalRuntime) ObservePriorProcess(context.Context, domain.ProcessEvidence) (PriorProcessObservation, error) {
	return PriorProcessObservation{State: PriorProcessAbsent}, nil
}

// SettlePriorProcess reports absent evidence for the compile-time contract proof.
func (minimalRuntime) SettlePriorProcess(context.Context, domain.ProcessEvidence) (PriorProcessSettlement, error) {
	return PriorProcessSettlement{Outcome: PriorProcessSettlementAbsent}, nil
}

// Close completes the compile-time contract proof without provider cleanup.
func (minimalRuntime) Close(context.Context) error {
	return nil
}

var _ Runtime = minimalRuntime{}
