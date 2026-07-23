package launcher

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/network/identity"
)

// LaunchTicket is the immutable non-secret metadata needed to launch one opaque helper capability.
type LaunchTicket struct {
	operationID domain.OperationID
	leaseKey    identity.LeaseKey
	reference   helper.TicketReference
	operation   helper.Operation
	address     netip.Addr
	expiresAt   time.Time
}

// PoolLaunchTicket is the immutable non-secret metadata needed to launch one aggregate pool capability.
type PoolLaunchTicket struct {
	operationID domain.OperationID
	reference   helper.TicketReference
	operation   helper.Operation
	pool        netip.Prefix
	expiresAt   time.Time
}

// ResolverLaunchTicket is the immutable non-secret metadata needed to launch one resolver capability.
type ResolverLaunchTicket struct {
	operationID          domain.OperationID
	reference            helper.TicketReference
	operation            helper.Operation
	policyFingerprint    string
	ownershipFingerprint string
	expiresAt            time.Time
}

// TrustLaunchTicket is the immutable non-secret metadata needed to launch one public-CA trust capability.
type TrustLaunchTicket struct {
	operationID          domain.OperationID
	reference            helper.TicketReference
	operation            helper.Operation
	policyFingerprint    string
	ownershipFingerprint string
	authorityFingerprint string
	trustMechanism       string
	expiresAt            time.Time
}

// LowPortLaunchTicket is immutable non-secret metadata for one policy-bound low-port capability.
type LowPortLaunchTicket struct {
	operationID            domain.OperationID
	reference              helper.TicketReference
	operation              helper.Operation
	policyFingerprint      string
	ownershipFingerprint   string
	observationFingerprint string
	expiresAt              time.Time
}

// NewLowPortLaunchTicket validates metadata from an authenticated low-port approval response.
func NewLowPortLaunchTicket(
	operationID domain.OperationID,
	reference helper.TicketReference,
	operation helper.Operation,
	policyFingerprint string,
	ownershipFingerprint string,
	observationFingerprint string,
	expiresAt time.Time,
) (LowPortLaunchTicket, error) {
	ticket := LowPortLaunchTicket{
		operationID:            operationID,
		reference:              reference,
		operation:              operation,
		policyFingerprint:      policyFingerprint,
		ownershipFingerprint:   ownershipFingerprint,
		observationFingerprint: observationFingerprint,
		expiresAt:              expiresAt,
	}
	if err := ticket.validateStructure(); err != nil {
		return LowPortLaunchTicket{}, err
	}
	return ticket, nil
}

// validateAt rejects stale low-port launch metadata.
func (ticket LowPortLaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) || ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("low-port launch ticket expiry is invalid")
	}
	return nil
}

// validateStructure confines consent to exact low-port operations and canonical evidence.
func (ticket LowPortLaunchTicket) validateStructure() error {
	if err := ticket.operationID.Validate(); err != nil {
		return err
	}
	if err := ticket.reference.Validate(); err != nil {
		return err
	}
	if ticket.operation != helper.OperationEnsureLowPorts && ticket.operation != helper.OperationReleaseLowPorts {
		return fmt.Errorf("low-port launch ticket operation is unsupported")
	}
	for _, value := range []string{ticket.policyFingerprint, ticket.ownershipFingerprint, ticket.observationFingerprint} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
			return fmt.Errorf("low-port launch ticket fingerprint is invalid")
		}
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("low-port launch ticket expiry must be UTC")
	}
	return nil
}

// NewLaunchTicket validates and captures metadata from an already authenticated approval response.
// Launcher.Invoke independently applies the trusted-clock lifetime checks immediately before native consent.
func NewLaunchTicket(
	operationID domain.OperationID,
	leaseKey identity.LeaseKey,
	reference helper.TicketReference,
	operation helper.Operation,
	address string,
	expiresAt time.Time,
) (LaunchTicket, error) {
	parsedAddress, err := netip.ParseAddr(address)
	if err != nil {
		return LaunchTicket{}, fmt.Errorf("launch ticket address is not canonical IPv4 loopback")
	}

	ticket := LaunchTicket{
		operationID: operationID,
		leaseKey:    leaseKey,
		reference:   reference,
		operation:   operation,
		address:     parsedAddress,
		expiresAt:   expiresAt,
	}
	if err := ticket.validateStructure(address); err != nil {
		return LaunchTicket{}, err
	}
	return ticket, nil
}

// NewPoolLaunchTicket validates and captures metadata from an already authenticated pool approval response.
// Launcher.InvokePool independently applies the trusted-clock lifetime checks immediately before native consent.
func NewPoolLaunchTicket(
	operationID domain.OperationID,
	reference helper.TicketReference,
	operation helper.Operation,
	pool string,
	expiresAt time.Time,
) (PoolLaunchTicket, error) {
	parsedPool, err := netip.ParsePrefix(pool)
	if err != nil {
		return PoolLaunchTicket{}, fmt.Errorf("pool launch ticket pool is not a canonical IPv4 loopback /29")
	}

	ticket := PoolLaunchTicket{
		operationID: operationID,
		reference:   reference,
		operation:   operation,
		pool:        parsedPool,
		expiresAt:   expiresAt,
	}
	if err := ticket.validateStructure(pool); err != nil {
		return PoolLaunchTicket{}, err
	}
	return ticket, nil
}

// NewResolverLaunchTicket validates metadata from an authenticated resolver approval response.
// Launcher.InvokeResolver independently applies trusted-clock lifetime checks before native consent.
func NewResolverLaunchTicket(
	operationID domain.OperationID,
	reference helper.TicketReference,
	operation helper.Operation,
	policyFingerprint string,
	ownershipFingerprint string,
	expiresAt time.Time,
) (ResolverLaunchTicket, error) {
	ticket := ResolverLaunchTicket{
		operationID:          operationID,
		reference:            reference,
		operation:            operation,
		policyFingerprint:    policyFingerprint,
		ownershipFingerprint: ownershipFingerprint,
		expiresAt:            expiresAt,
	}
	if err := ticket.validateStructure(); err != nil {
		return ResolverLaunchTicket{}, err
	}
	return ticket, nil
}

// NewTrustLaunchTicket validates metadata from an authenticated trust approval response.
// Launcher.InvokeTrust independently applies trusted-clock lifetime checks before native consent.
func NewTrustLaunchTicket(
	operationID domain.OperationID,
	reference helper.TicketReference,
	operation helper.Operation,
	policyFingerprint string,
	ownershipFingerprint string,
	authorityFingerprint string,
	trustMechanism string,
	expiresAt time.Time,
) (TrustLaunchTicket, error) {
	ticket := TrustLaunchTicket{
		operationID:          operationID,
		reference:            reference,
		operation:            operation,
		policyFingerprint:    policyFingerprint,
		ownershipFingerprint: ownershipFingerprint,
		authorityFingerprint: authorityFingerprint,
		trustMechanism:       trustMechanism,
		expiresAt:            expiresAt,
	}
	if err := ticket.validateStructure(); err != nil {
		return TrustLaunchTicket{}, err
	}
	return ticket, nil
}

// validateAt rejects stale, excessively long-lived, or internally malformed launch metadata.
func (ticket LaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(ticket.address.String()); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) {
		return fmt.Errorf("launch ticket expiry is not in the future")
	}
	if ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("launch ticket expiry exceeds the protocol bound")
	}
	return nil
}

// validateStructure keeps the client-visible metadata canonical without interpreting the signed ticket contents.
func (ticket LaunchTicket) validateStructure(address string) error {
	if err := ticket.operationID.Validate(); err != nil {
		return fmt.Errorf("launch ticket operation ID: %w", err)
	}
	if err := ticket.leaseKey.Validate(); err != nil {
		return fmt.Errorf("launch ticket lease key: %w", err)
	}
	if err := ticket.reference.Validate(); err != nil {
		return fmt.Errorf("launch ticket reference: %w", err)
	}
	if ticket.operation != helper.OperationEnsureLoopbackIdentity && ticket.operation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("launch ticket helper operation %q is unsupported", ticket.operation)
	}
	if !ticket.address.Is4() || !ticket.address.IsLoopback() || ticket.address != ticket.address.Unmap() || address != ticket.address.String() {
		return fmt.Errorf("launch ticket address is not canonical IPv4 loopback")
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("launch ticket expiry must be a nonzero UTC time")
	}
	return nil
}

// validateAt rejects stale, excessively long-lived, or internally malformed aggregate launch metadata.
func (ticket PoolLaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(ticket.pool.String()); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) {
		return fmt.Errorf("pool launch ticket expiry is not in the future")
	}
	if ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("pool launch ticket expiry exceeds the protocol bound")
	}
	return nil
}

// validateStructure confines aggregate consent to one exact canonical /29 and the pool helper operation.
func (ticket PoolLaunchTicket) validateStructure(pool string) error {
	if err := ticket.operationID.Validate(); err != nil {
		return fmt.Errorf("pool launch ticket operation ID: %w", err)
	}
	if err := ticket.reference.Validate(); err != nil {
		return fmt.Errorf("pool launch ticket reference: %w", err)
	}
	if ticket.operation != helper.OperationEnsureLoopbackPool && ticket.operation != helper.OperationReleaseLoopbackPool {
		return fmt.Errorf("pool launch ticket helper operation %q is unsupported", ticket.operation)
	}
	if !ticket.pool.IsValid() ||
		!ticket.pool.Addr().Is4() ||
		!ticket.pool.Addr().IsLoopback() ||
		ticket.pool.Addr() != ticket.pool.Addr().Unmap() ||
		ticket.pool.Bits() != 29 ||
		ticket.pool != ticket.pool.Masked() ||
		pool != ticket.pool.String() {
		return fmt.Errorf("pool launch ticket pool is not a canonical IPv4 loopback /29")
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("pool launch ticket expiry must be a nonzero UTC time")
	}
	return nil
}

// validateAt rejects stale or excessively long-lived resolver launch metadata.
func (ticket ResolverLaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) {
		return fmt.Errorf("resolver launch ticket expiry is not in the future")
	}
	if ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("resolver launch ticket expiry exceeds the protocol bound")
	}
	return nil
}

// validateStructure confines resolver consent to one operation and canonical policy digest.
func (ticket ResolverLaunchTicket) validateStructure() error {
	if err := ticket.operationID.Validate(); err != nil {
		return fmt.Errorf("resolver launch ticket operation ID: %w", err)
	}
	if err := ticket.reference.Validate(); err != nil {
		return fmt.Errorf("resolver launch ticket reference: %w", err)
	}
	if ticket.operation != helper.OperationEnsureResolver && ticket.operation != helper.OperationReleaseResolver && ticket.operation != helper.OperationRetireResolver {
		return fmt.Errorf("resolver launch ticket helper operation %q is unsupported", ticket.operation)
	}
	decoded, err := hex.DecodeString(ticket.policyFingerprint)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != ticket.policyFingerprint {
		return fmt.Errorf("resolver launch ticket policy fingerprint is invalid")
	}
	decoded, err = hex.DecodeString(ticket.ownershipFingerprint)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != ticket.ownershipFingerprint {
		return fmt.Errorf("resolver launch ticket ownership fingerprint is invalid")
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("resolver launch ticket expiry must be a nonzero UTC time")
	}
	return nil
}

// validateAt rejects stale or excessively long-lived trust launch metadata.
func (ticket TrustLaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) {
		return fmt.Errorf("trust launch ticket expiry is not in the future")
	}
	if ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("trust launch ticket expiry exceeds the protocol bound")
	}
	return nil
}

// validateStructure confines trust consent to an exact approved CA and platform trust mechanism.
func (ticket TrustLaunchTicket) validateStructure() error {
	if err := ticket.operationID.Validate(); err != nil {
		return fmt.Errorf("trust launch ticket operation ID: %w", err)
	}
	if err := ticket.reference.Validate(); err != nil {
		return fmt.Errorf("trust launch ticket reference: %w", err)
	}
	if ticket.operation != helper.OperationEnsureTrust && ticket.operation != helper.OperationReleaseTrust {
		return fmt.Errorf("trust launch ticket helper operation %q is unsupported", ticket.operation)
	}
	for _, value := range []string{ticket.policyFingerprint, ticket.ownershipFingerprint, ticket.authorityFingerprint} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
			return fmt.Errorf("trust launch ticket fingerprint is invalid")
		}
	}
	switch ticket.trustMechanism {
	case "darwin-administrator-trust-v1", "darwin-current-user-trust-v1", "ubuntu-system-trust-v1", "windows-current-user-trust-v1":
	default:
		return fmt.Errorf("trust launch ticket mechanism %q is unsupported", ticket.trustMechanism)
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("trust launch ticket expiry must be a nonzero UTC time")
	}
	return nil
}
