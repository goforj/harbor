package launcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"

	"github.com/goforj/harbor/internal/helper"
)

// OutcomeState identifies the only conclusions the interactive launcher can safely report.
type OutcomeState string

const (
	// Succeeded means the started helper returned a valid correlated success response.
	Succeeded OutcomeState = "succeeded"
	// HelperFailed means the started helper returned a valid bounded failure response.
	HelperFailed OutcomeState = "helper_failed"
	// Declined means the transport proved that user dismissal occurred before any child started.
	Declined OutcomeState = "declined"
	// Unavailable means the transport proved that launch was unavailable before any child started.
	Unavailable OutcomeState = "unavailable"
	// Indeterminate means a child or effect may have started without one valid correlated response.
	Indeterminate OutcomeState = "indeterminate"
)

// ProcessExit records one completed helper process status without interpreting it as effect evidence.
type ProcessExit struct {
	// Code is meaningful only when the transport reported TransportCompleted.
	Code int
}

const (
	// ExitCodeSucceeded is emitted only after the helper writes an OK success response.
	ExitCodeSucceeded = 0
	// ExitCodeHelperFailed is trusted as a helper failure only alongside one structured failure response.
	ExitCodeHelperFailed = 1
)

// Outcome records one classified launch while keeping the opaque ticket reference out of caller-visible state.
type Outcome struct {
	// State is the launcher's safe conclusion about the interactive attempt.
	State OutcomeState
	// Response is populated only for a valid Succeeded or HelperFailed exchange.
	Response helper.Response
	// Exit is populated whenever the transport proved that the helper process exited.
	Exit *ProcessExit
}

// TransportState identifies whether a backend proved no child started or completed one child exchange.
type TransportState uint8

const (
	// TransportCompleted means the helper child started, exited, and wrote through the supplied response stream.
	TransportCompleted TransportState = iota + 1
	// TransportDeclined means native consent was dismissed before a helper child existed.
	TransportDeclined
	// TransportUnavailable means native launch failed before a helper child existed.
	TransportUnavailable
	// TransportIndeterminate means a helper child or effect may have started without a trustworthy completion.
	TransportIndeterminate
)

// TransportResult records the platform backend's process-lifecycle conclusion.
type TransportResult struct {
	// State is the backend's native process-lifecycle conclusion.
	State TransportState
	// ExitCode is meaningful only for TransportCompleted.
	ExitCode int
}

// Transport performs one native consent and helper-process exchange.
type Transport interface {
	// Invoke forwards request only to the fixed helper channel and must neither retain nor log its opaque contents.
	// A backend must return Declined or Unavailable only when it can prove that no child started.
	Invoke(context.Context, io.Reader, io.Writer) TransportResult
}

// Launcher owns the platform-neutral request, response, and outcome state machine.
type Launcher struct {
	transport Transport
	clock     helper.Clock
}

// resultMatcher correlates one decoded helper success with the metadata prepared before native consent.
type resultMatcher func(*helper.OperationResult) bool

// New constructs a launcher with an explicit native transport and trusted clock.
func New(transport Transport, clock helper.Clock) *Launcher {
	if requiredInterfaceIsNil(transport) {
		panic("launcher.New requires a non-nil transport")
	}
	if requiredInterfaceIsNil(clock) {
		panic("launcher.New requires a non-nil clock")
	}
	return &Launcher{transport: transport, clock: clock}
}

// requiredInterfaceIsNil rejects typed-nil dependencies before an interactive launch can reach them.
func requiredInterfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Invoke performs exactly one transport attempt for valid launch metadata and classifies its bounded response.
func (launcher *Launcher) Invoke(ctx context.Context, ticket LaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchLaunchTicket(ticket))
}

// InvokePool performs exactly one transport attempt for valid aggregate pool launch metadata.
func (launcher *Launcher) InvokePool(ctx context.Context, ticket PoolLaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper pool launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchPoolLaunchTicket(ticket))
}

// InvokeResolver performs exactly one transport attempt for valid resolver launch metadata.
func (launcher *Launcher) InvokeResolver(ctx context.Context, ticket ResolverLaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper resolver launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchResolverLaunchTicket(ticket))
}

// InvokeTrust performs exactly one transport attempt for valid public-CA trust launch metadata.
func (launcher *Launcher) InvokeTrust(ctx context.Context, ticket TrustLaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper trust launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchTrustLaunchTicket(ticket))
}

// InvokeLowPorts performs one transport attempt for valid low-port launch metadata.
func (launcher *Launcher) InvokeLowPorts(ctx context.Context, ticket LowPortLaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper low-port launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchLowPortLaunchTicket(ticket))
}

// InvokeOwnership performs one transport attempt for an exact ownership release capability.
func (launcher *Launcher) InvokeOwnership(ctx context.Context, ticket OwnershipLaunchTicket) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := ticket.validateAt(launcher.clock.Now().UTC()); err != nil {
		return Outcome{}, fmt.Errorf("validate helper ownership launch ticket: %w", err)
	}
	return launcher.invoke(ctx, ticket.reference, matchOwnershipLaunchTicket(ticket))
}

// invoke owns the shared bounded request and response exchange after launch metadata has been validated.
func (launcher *Launcher) invoke(
	ctx context.Context,
	reference helper.TicketReference,
	match resultMatcher,
) (Outcome, error) {
	var request bytes.Buffer
	if err := helper.WriteRequest(&request, helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	}); err != nil {
		return Outcome{}, fmt.Errorf("encode helper request: %w", err)
	}

	response := &boundedResponseWriter{}
	transportResult := launcher.transport.Invoke(ctx, bytes.NewReader(request.Bytes()), response)
	return classify(ctx, transportResult, response.Bytes(), match), nil
}

// classify treats native no-child proofs separately from every state where an effect may have started.
func classify(ctx context.Context, transportResult TransportResult, body []byte, match resultMatcher) Outcome {
	switch transportResult.State {
	case TransportDeclined:
		if len(body) != 0 {
			return Outcome{State: Indeterminate}
		}
		return Outcome{State: Declined}
	case TransportUnavailable:
		if len(body) != 0 {
			return Outcome{State: Indeterminate}
		}
		return Outcome{State: Unavailable}
	case TransportCompleted:
		exit := &ProcessExit{Code: transportResult.ExitCode}
		if ctx.Err() != nil {
			return Outcome{State: Indeterminate, Exit: exit}
		}
		response, err := helper.DecodeResponse(bytes.NewReader(body))
		if err != nil {
			return Outcome{State: Indeterminate, Exit: exit}
		}
		if !response.OK {
			if transportResult.ExitCode != ExitCodeHelperFailed {
				return Outcome{State: Indeterminate, Exit: exit}
			}
			return Outcome{State: HelperFailed, Response: response, Exit: exit}
		}
		if transportResult.ExitCode != ExitCodeSucceeded {
			return Outcome{State: Indeterminate, Exit: exit}
		}
		if !match(response.Result) {
			return Outcome{State: Indeterminate, Exit: exit}
		}
		return Outcome{State: Succeeded, Response: response, Exit: exit}
	case TransportIndeterminate:
		return Outcome{State: Indeterminate}
	default:
		return Outcome{State: Indeterminate}
	}
}

// matchLaunchTicket binds a scalar success to the prepared operation and exact address.
func matchLaunchTicket(ticket LaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		return result != nil &&
			result.Operation == ticket.operation &&
			result.Evidence.Address == ticket.address.String()
	}
}

// matchPoolLaunchTicket binds aggregate success to the prepared /29 and every canonical address it contains.
func matchPoolLaunchTicket(ticket PoolLaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		if result == nil || result.Operation != ticket.operation || result.PoolEvidence == nil || result.PoolEvidence.Pool != ticket.pool.String() {
			return false
		}
		identities := result.PoolEvidence.Identities
		if len(identities) != 8 {
			return false
		}
		address := ticket.pool.Addr()
		for _, evidence := range identities {
			if evidence.Address != address.String() {
				return false
			}
			address = address.Next()
		}
		return true
	}
}

// matchResolverLaunchTicket binds success to the prepared operation, policy, and target protected ownership.
func matchResolverLaunchTicket(ticket ResolverLaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		return result != nil &&
			result.Operation == ticket.operation &&
			result.ResolverEvidence != nil &&
			result.ResolverEvidence.PolicyFingerprint == ticket.policyFingerprint &&
			result.ResolverEvidence.OwnershipFingerprint == ticket.ownershipFingerprint
	}
}

// matchTrustLaunchTicket binds success to the prepared ensure-trust CA and platform mechanism.
func matchTrustLaunchTicket(ticket TrustLaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		return result != nil &&
			result.Operation == ticket.operation &&
			result.TrustEvidence != nil &&
			result.TrustEvidence.AuthorityFingerprint == ticket.authorityFingerprint &&
			string(result.TrustEvidence.Mechanism) == ticket.trustMechanism
	}
}

// matchLowPortLaunchTicket binds the helper result to the prepared effect while allowing mutation to change native evidence.
func matchLowPortLaunchTicket(ticket LowPortLaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		if result == nil || result.Operation != ticket.operation || result.LowPortEvidence == nil ||
			result.LowPortEvidence.PolicyFingerprint != ticket.policyFingerprint ||
			result.LowPortEvidence.OwnershipFingerprint != ticket.ownershipFingerprint {
			return false
		}
		if ticket.operation == helper.OperationEnsureLowPorts {
			return result.LowPortEvidence.Postcondition == helper.LowPortPostconditionExact
		}
		return result.LowPortEvidence.Postcondition == helper.LowPortPostconditionOwnedAbsent
	}
}

// matchOwnershipLaunchTicket binds success to one durable release checkpoint and exact released ownership record.
func matchOwnershipLaunchTicket(ticket OwnershipLaunchTicket) resultMatcher {
	return func(result *helper.OperationResult) bool {
		return result != nil && result.Operation == ticket.operation && result.OwnershipEvidence != nil &&
			result.OwnershipEvidence.ReleaseOperationID == ticket.releaseOperationID &&
			result.OwnershipEvidence.ReleaseOperationRevision == ticket.releaseOperationRevision &&
			result.OwnershipEvidence.ReleaseCheckpointRevision == ticket.releaseCheckpointRevision &&
			result.OwnershipEvidence.ReleasedOwnershipFingerprint == ticket.expectedOwnershipFingerprint &&
			result.OwnershipEvidence.Postcondition == helper.OwnershipPostconditionOwnedAbsent
	}
}

// boundedResponseWriter retains one byte beyond the protocol limit so DecodeResponse can reject overflow.
type boundedResponseWriter struct {
	body bytes.Buffer
}

// Write accepts the backend's complete stream while retaining only the bounded protocol prefix.
func (writer *boundedResponseWriter) Write(body []byte) (int, error) {
	written := len(body)
	remaining := helper.MaxResponseBytes + 1 - writer.body.Len()
	if remaining <= 0 {
		return written, nil
	}
	if len(body) > remaining {
		body = body[:remaining]
	}
	_, _ = writer.body.Write(body)
	return written, nil
}

// Bytes returns the captured bounded response after the transport invocation ends.
func (writer *boundedResponseWriter) Bytes() []byte {
	return writer.body.Bytes()
}
