package control

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const maximumNetworkObservationDetailBytes = 160

// NetworkSetupObservationStage identifies the reviewed host fact that blocked setup.
type NetworkSetupObservationStage string

const (
	// NetworkSetupObservationAssignment identifies native loopback assignment inspection.
	NetworkSetupObservationAssignment NetworkSetupObservationStage = "loopback assignment"
	// NetworkSetupObservationHostConflicts identifies native route, socket, and policy inspection.
	NetworkSetupObservationHostConflicts NetworkSetupObservationStage = "host conflicts"
)

// Caller carries both identities established before a product method reaches daemon authority.
type Caller struct {
	// Transport is the operating-system identity authenticated by the local socket or pipe.
	Transport local.PeerIdentity
	// Session is the role and feature identity established by protocol negotiation.
	Session session.Peer
}

// Authority owns the daemon-side implementation of bounded control methods.
type Authority interface {
	// Status returns the ready daemon's standalone product diagnostic.
	Status(context.Context, Caller) (DaemonStatus, error)
	// Snapshot returns a complete authoritative replacement of client-visible state.
	Snapshot(context.Context, Caller) (domain.Snapshot, error)
	// StartNetworkSetup starts or resumes one idempotent machine-global network setup intent.
	StartNetworkSetup(context.Context, Caller, StartNetworkSetupRequest) (NetworkSetupOperation, error)
	// PrepareNetworkSetupApproval returns one caller-bound helper capability for an exact setup revision.
	PrepareNetworkSetupApproval(context.Context, Caller, PrepareNetworkSetupApprovalRequest) (NetworkSetupApprovalPreparation, error)
	// ConfirmNetworkSetupApproval verifies the complete loopback pool before finishing setup.
	ConfirmNetworkSetupApproval(context.Context, Caller, ConfirmNetworkSetupApprovalRequest) (NetworkSetupApprovalConfirmation, error)
	// StartNetworkResolverSetup starts or resumes one idempotent machine-global resolver setup intent.
	StartNetworkResolverSetup(context.Context, Caller, StartNetworkResolverSetupRequest) (NetworkResolverSetupOperation, error)
	// PrepareNetworkResolverSetupApproval returns one caller-bound helper capability for an exact resolver setup revision.
	PrepareNetworkResolverSetupApproval(context.Context, Caller, PrepareNetworkResolverSetupApprovalRequest) (NetworkResolverSetupApprovalPreparation, error)
	// ConfirmNetworkResolverSetupApproval verifies the exact resolver policy before finishing setup.
	ConfirmNetworkResolverSetupApproval(context.Context, Caller, ConfirmNetworkResolverSetupApprovalRequest) (NetworkResolverSetupApprovalConfirmation, error)
	// ProjectActivity returns bounded output for a project's current durable session.
	ProjectActivity(context.Context, Caller, ProjectActivityRequest) (ProjectActivity, error)
	// RegisterProject discovers and durably registers one canonical GoForj checkout.
	RegisterProject(context.Context, Caller, RegisterProjectRequest) (ProjectRegistration, error)
	// StartProject starts or resumes one idempotent managed project lifecycle.
	StartProject(context.Context, Caller, StartProjectRequest) (ProjectLifecycleOperation, error)
	// StopProject stops or resumes one idempotent managed project lifecycle.
	StopProject(context.Context, Caller, StopProjectRequest) (ProjectLifecycleOperation, error)
	// UnregisterProject starts or resumes one idempotent project removal intent.
	UnregisterProject(context.Context, Caller, UnregisterProjectRequest) (ProjectUnregistration, error)
	// PrepareProjectUnregisterApproval returns release progress and at most one caller-bound helper capability.
	PrepareProjectUnregisterApproval(context.Context, Caller, PrepareProjectUnregisterApprovalRequest) (ProjectUnregisterApprovalPreparation, error)
	// ConfirmProjectUnregisterApproval verifies host release before completing the durable unregister operation.
	ConfirmProjectUnregisterApproval(context.Context, Caller, ConfirmProjectUnregisterApprovalRequest) (ProjectUnregisterApprovalConfirmation, error)
}

// normalizeContext lets public control calls accept a nil context without weakening dependency wiring.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// authorityError preserves cancellation while classifying every other authority failure as daemon-internal.
func authorityError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var classified *session.HandlerError
	if errors.As(err, &classified) {
		return classified
	}

	return session.NewHandlerError(rpc.ErrorCodeInternal, err)
}

// NewProjectRegistrationConflictError classifies a safe daemon-side registration conflict for control clients.
func NewProjectRegistrationConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectRegistrationInvalidError classifies a selected checkout that cannot form a valid registration.
func NewProjectRegistrationInvalidError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeInvalidRequest, cause)
}

// NewProjectLifecycleInvalidError classifies a start or stop request that cannot form a valid lifecycle intent.
func NewProjectLifecycleInvalidError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeInvalidRequest, cause)
}

// NewProjectLifecycleNotFoundError classifies a start or stop request for an unknown durable project.
func NewProjectLifecycleNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewProjectLifecycleConflictError classifies durable state that prevents a project start or stop.
func NewProjectLifecycleConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectActivityInvalidError classifies a current-output request that cannot identify a valid cursor.
func NewProjectActivityInvalidError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeInvalidRequest, cause)
}

// NewProjectActivityNotFoundError classifies a current-output request for an unknown durable project.
func NewProjectActivityNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewNetworkSetupConflictError classifies durable state that prevents network setup initiation or approval.
func NewNetworkSetupConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewNetworkSetupNotFoundError classifies a setup approval selection whose durable operation is missing.
func NewNetworkSetupNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewNetworkResolverSetupConflictError classifies durable state that prevents resolver setup initiation or approval.
func NewNetworkResolverSetupConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewNetworkResolverSetupNotFoundError classifies a resolver setup approval selection whose durable operation is missing.
func NewNetworkResolverSetupNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewNetworkResolverSetupPrivilegedHelperRequiredError reports an absent resolver helper boundary without exposing its path.
func NewNetworkResolverSetupPrivilegedHelperRequiredError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodePrivilegedHelperRequired, cause)
}

// NewNetworkResolverSetupPrivilegedHelperUnsafeError reports a resolver helper boundary that failed its fixed filesystem policy.
func NewNetworkResolverSetupPrivilegedHelperUnsafeError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodePrivilegedHelperUnsafe, cause)
}

// NewNetworkSetupObservationError exposes one strictly validated native observation diagnostic to an authenticated setup caller.
func NewNetworkSetupObservationError(
	cause error,
	stage NetworkSetupObservationStage,
	address netip.Addr,
	detail string,
) error {
	message := networkSetupObservationMessage(stage, address, detail)
	return session.NewNetworkObservationHandlerError(cause, message)
}

// NewNetworkSetupPrivilegedHelperRequiredError reports an absent installer-owned helper boundary without exposing its path.
func NewNetworkSetupPrivilegedHelperRequiredError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodePrivilegedHelperRequired, cause)
}

// NewNetworkSetupPrivilegedHelperUnsafeError reports an installed helper boundary that failed its fixed filesystem policy.
func NewNetworkSetupPrivilegedHelperUnsafeError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodePrivilegedHelperUnsafe, cause)
}

// networkSetupObservationMessage renders dynamic detail only when every semantic and text boundary is canonical.
func networkSetupObservationMessage(stage NetworkSetupObservationStage, address netip.Addr, detail string) string {
	fallback := rpc.NewWireError(rpc.ErrorCodeNetworkObservationFailed).Message
	switch stage {
	case NetworkSetupObservationAssignment, NetworkSetupObservationHostConflicts:
	default:
		return fallback
	}
	if !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
		return fallback
	}
	octets := address.As4()
	if octets[0] != 127 || octets[1] != 77 || !validNetworkObservationDetail(detail) {
		return fallback
	}
	switch stage {
	case NetworkSetupObservationAssignment:
		if !strings.HasPrefix(detail, "loopback observe "+address.String()+": ") {
			return fallback
		}
	case NetworkSetupObservationHostConflicts:
		if !strings.HasPrefix(detail, "observe Darwin host conflicts: ") &&
			!strings.HasPrefix(detail, "observe Linux host conflicts: ") &&
			!strings.HasPrefix(detail, "observe Windows host conflicts: ") {
			return fallback
		}
	}

	message := fmt.Sprintf("Harbor could not inspect %s for %s: %s", stage, address, detail)
	if rpc.NewNetworkObservationWireError(message).Message != message {
		return fallback
	}

	return message
}

// validNetworkObservationDetail rejects invisible, multiline, padded, and oversized native diagnostics.
func validNetworkObservationDetail(detail string) bool {
	if detail == "" || len(detail) > maximumNetworkObservationDetailBytes || strings.TrimSpace(detail) != detail || !utf8.ValidString(detail) {
		return false
	}
	lowerDetail := strings.ToLower(detail)
	for _, sensitive := range []string{"app_key", "authorization", "credential", "password", "private key", "secret", "token="} {
		if strings.Contains(lowerDetail, sensitive) {
			return false
		}
	}
	for _, character := range detail {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}

	return true
}

// NewProjectUnregisterConflictError classifies current state that prevents unregister initiation or progress.
func NewProjectUnregisterConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectUnregisterNotFoundError classifies a requested project that is not durably registered.
func NewProjectUnregisterNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewProjectUnregisterApprovalConflictError classifies a reviewed unregister lifecycle conflict for control clients.
func NewProjectUnregisterApprovalConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectUnregisterApprovalNotFoundError classifies missing unregister authority for control clients.
func NewProjectUnregisterApprovalNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}
