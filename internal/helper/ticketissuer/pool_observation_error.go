package ticketissuer

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/platform/loopback"
)

const maximumPoolObservationDetailBytes = 160

// PoolObservationStage identifies the native fact Harbor could not obtain for one candidate pool address.
type PoolObservationStage string

const (
	// PoolObservationAssignment identifies native loopback assignment inspection.
	PoolObservationAssignment PoolObservationStage = "loopback assignment"
	// PoolObservationHostConflicts identifies native route, socket, and policy inspection.
	PoolObservationHostConflicts PoolObservationStage = "host conflicts"
)

// PoolObservationError preserves one native observer failure without classifying unrelated setup internals as peer-safe.
type PoolObservationError struct {
	stage   PoolObservationStage
	address netip.Addr
	cause   error
}

// NewPoolObservationError records only a canonical candidate and one allowlisted native-observation stage.
func NewPoolObservationError(stage PoolObservationStage, address netip.Addr, cause error) *PoolObservationError {
	return &PoolObservationError{stage: stage, address: address, cause: cause}
}

// Error returns the complete daemon-local diagnostic.
func (e *PoolObservationError) Error() string {
	if e == nil {
		return "pool observation failed"
	}
	return fmt.Sprintf("observe %s %s: %v", e.stage, e.address, e.cause)
}

// Unwrap preserves the native observer cause for cancellation and daemon diagnostics.
func (e *PoolObservationError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// Stage returns the allowlisted native-observation boundary.
func (e *PoolObservationError) Stage() PoolObservationStage {
	if e == nil {
		return ""
	}

	return e.stage
}

// Address returns the exact pool candidate whose host facts were requested.
func (e *PoolObservationError) Address() netip.Addr {
	if e == nil {
		return netip.Addr{}
	}

	return e.address
}

// Cause returns the native diagnostic considered for the reviewed peer message.
func (e *PoolObservationError) Cause() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// ReviewedDetail returns native diagnostics only from the two production observer contracts.
func (e *PoolObservationError) ReviewedDetail() (string, bool) {
	if err := e.Validate(); err != nil {
		return "", false
	}
	detail := e.cause.Error()
	switch e.stage {
	case PoolObservationAssignment:
		var loopbackError *loopback.Error
		if !errors.As(e.cause, &loopbackError) || loopbackError.Operation != "observe" || loopbackError.Address != e.address {
			return "", false
		}
	case PoolObservationHostConflicts:
		if !strings.HasPrefix(detail, "observe Darwin host conflicts: ") &&
			!strings.HasPrefix(detail, "observe Linux host conflicts: ") &&
			!strings.HasPrefix(detail, "observe Windows host conflicts: ") {
			return "", false
		}
	default:
		return "", false
	}
	if !validPoolObservationDetail(detail) {
		return "", false
	}

	return detail, true
}

// Validate rejects fabricated observation failures that escape the production pool boundary.
func (e *PoolObservationError) Validate() error {
	if e == nil {
		return errors.New("pool observation error is required")
	}
	switch e.stage {
	case PoolObservationAssignment, PoolObservationHostConflicts:
	default:
		return fmt.Errorf("pool observation stage %q is unsupported", e.stage)
	}
	if !e.address.Is4() || !e.address.IsLoopback() || e.address != e.address.Unmap() {
		return errors.New("pool observation address must be canonical IPv4 loopback")
	}
	octets := e.address.As4()
	if octets[0] != 127 || octets[1] != 77 {
		return errors.New("pool observation address must belong to Harbor's production namespace")
	}
	if e.cause == nil {
		return errors.New("pool observation cause is required")
	}

	return nil
}

// validPoolObservationDetail keeps native diagnostics bounded to one visible, whitespace-canonical UI line.
func validPoolObservationDetail(detail string) bool {
	if detail == "" || len(detail) > maximumPoolObservationDetailBytes || strings.TrimSpace(detail) != detail || !utf8.ValidString(detail) {
		return false
	}
	for _, character := range detail {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}

	return true
}
