package lowport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
)

const (
	maximumArtifacts          = 4
	canonicalFingerprintBytes = sha256.Size * 2
)

var canonicalLocalhost = netip.MustParseAddr("127.0.0.1")

// Request is immutable low-port authority derived from validated schema-2 ownership and one Darwin policy.
type Request struct {
	installationID    string
	ownerUID          uint32
	policyFingerprint string
	httpUpstream      netip.AddrPort
	httpsUpstream     netip.AddrPort
}

// NewRequest derives exact service authority without accepting a label, path, port, or executable from callers.
func NewRequest(record ownership.Record, policy networkpolicy.Policy) (Request, error) {
	if err := record.Validate(); err != nil {
		return Request{}, fmt.Errorf("low-port ownership: %w", err)
	}
	if record.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return Request{}, fmt.Errorf("low-port ownership schema must be %d", ownership.NetworkPolicySchemaVersion)
	}
	if err := policy.Validate(); err != nil {
		return Request{}, fmt.Errorf("low-port policy: %w", err)
	}
	if policy.Mechanisms.LowPorts != networkpolicy.DarwinLaunchdRelay {
		return Request{}, fmt.Errorf("low-port policy mechanism %q is unsupported", policy.Mechanisms.LowPorts)
	}
	fingerprint, err := policy.Fingerprint()
	if err != nil || fingerprint != record.NetworkPolicyFingerprint {
		return Request{}, fmt.Errorf("low-port policy fingerprint does not match schema-2 ownership")
	}
	uid, err := strconv.ParseUint(record.OwnerIdentity, 10, 32)
	if err != nil || strconv.FormatUint(uid, 10) != record.OwnerIdentity {
		return Request{}, fmt.Errorf("low-port ownership owner identity is not a Darwin UID")
	}
	if uid == 0 {
		return Request{}, fmt.Errorf("low-port ownership owner UID must be non-root")
	}
	if policy.HTTP.Bind.Port() < 1024 || policy.HTTPS.Bind.Port() < 1024 {
		return Request{}, fmt.Errorf("low-port upstreams must use unprivileged high ports")
	}
	request := Request{installationID: record.InstallationID, ownerUID: uint32(uid), policyFingerprint: fingerprint, httpUpstream: policy.HTTP.Bind, httpsUpstream: policy.HTTPS.Bind}
	return request, request.Validate()
}

// Validate proves every immutable request component remains canonical.
func (r Request) Validate() error {
	if err := helper.ValidateInstallationID(r.installationID); err != nil {
		return err
	}
	if r.ownerUID == 0 {
		return fmt.Errorf("low-port owner UID must be non-root")
	}
	if len(r.policyFingerprint) != canonicalFingerprintBytes {
		return fmt.Errorf("low-port policy fingerprint is invalid")
	}
	if decoded, err := hex.DecodeString(r.policyFingerprint); err != nil || hex.EncodeToString(decoded) != r.policyFingerprint {
		return fmt.Errorf("low-port policy fingerprint is invalid")
	}
	for _, upstream := range []netip.AddrPort{r.httpUpstream, r.httpsUpstream} {
		if !upstream.IsValid() || upstream.Addr() != canonicalLocalhost || upstream.Port() < 1024 {
			return fmt.Errorf("low-port upstream is invalid")
		}
	}
	if r.httpUpstream == r.httpsUpstream {
		return fmt.Errorf("low-port upstreams must differ")
	}
	return nil
}

// InstallationID returns the installation identity encoded into fixed service ownership.
func (r Request) InstallationID() string { return r.installationID }

// OwnerUID returns the Darwin service user derived from schema-2 ownership.
func (r Request) OwnerUID() uint32 { return r.ownerUID }

// PolicyFingerprint returns the canonical policy digest bound to the service.
func (r Request) PolicyFingerprint() string { return r.policyFingerprint }

// HTTPUpstream returns the exact high HTTP upstream.
func (r Request) HTTPUpstream() netip.AddrPort { return r.httpUpstream }

// HTTPSUpstream returns the exact high HTTPS upstream.
func (r Request) HTTPSUpstream() netip.AddrPort { return r.httpsUpstream }

// ArtifactKind identifies one fixed native object in the Darwin low-port contract.
type ArtifactKind string

const (
	// ArtifactKindPlist identifies Harbor's root-owned canonical LaunchDaemon definition.
	ArtifactKindPlist ArtifactKind = "plist"
	// ArtifactKindService identifies the matching service currently loaded in launchd's system domain.
	ArtifactKindService ArtifactKind = "service"
)

// Artifact is one bounded native fact from the fixed low-port namespace.
type Artifact struct {
	// Kind identifies the fixed native object observed.
	Kind ArtifactKind
	// Present reports whether the native object currently exists.
	Present bool
	// Owned reports whether the object is uniquely attributable to Harbor.
	Owned bool
	// Exact reports whether the object matches Harbor's complete canonical contract.
	Exact bool
	// Ambiguous reports native evidence that cannot safely identify one object.
	Ambiguous bool
	// Fingerprint is canonical bounded evidence for this artifact's observed content.
	Fingerprint string
}

// Validate rejects artifact facts whose flags cannot describe one native object safely.
func (a Artifact) Validate() error {
	switch a.Kind {
	case ArtifactKindPlist, ArtifactKindService:
	default:
		return fmt.Errorf("low-port artifact kind %q is unsupported", a.Kind)
	}
	if len(a.Fingerprint) != canonicalFingerprintBytes {
		return fmt.Errorf("low-port artifact fingerprint is invalid")
	}
	decoded, err := hex.DecodeString(a.Fingerprint)
	if err != nil || hex.EncodeToString(decoded) != a.Fingerprint {
		return fmt.Errorf("low-port artifact fingerprint is invalid")
	}
	if !a.Present && (a.Owned || a.Exact || a.Ambiguous) {
		return fmt.Errorf("absent low-port artifact carries ownership facts")
	}
	if a.Exact && (!a.Present || !a.Owned || a.Ambiguous) {
		return fmt.Errorf("exact low-port artifact is not uniquely owned and present")
	}
	if a.Ambiguous && a.Owned {
		return fmt.Errorf("ambiguous low-port artifact cannot claim unique ownership")
	}
	return nil
}

// Observation is the bounded state around Harbor's fixed plist and loaded launchd service.
type Observation struct {
	// Request is the immutable authority against which native facts were observed.
	Request Request
	// Complete reports whether all required native facts were obtained.
	Complete bool
	// Artifacts contains typed plist and launchd service facts.
	Artifacts []Artifact
}

// Validate rejects oversized, malformed, or falsely complete observations.
func (o Observation) Validate() error {
	if err := o.Request.Validate(); err != nil {
		return err
	}
	if len(o.Artifacts) == 0 {
		return fmt.Errorf("low-port observation has no artifacts")
	}
	if len(o.Artifacts) > maximumArtifacts {
		return fmt.Errorf("low-port observation has too many artifacts")
	}
	kinds := make(map[ArtifactKind]int, 2)
	for index, artifact := range o.Artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("low-port artifact %d: %w", index, err)
		}
		kinds[artifact.Kind]++
	}
	if o.Complete && (kinds[ArtifactKindPlist] == 0 || kinds[ArtifactKindService] == 0) {
		return fmt.Errorf("complete low-port observation must include plist and service facts")
	}
	return nil
}

// State validates and classifies the observation without assuming artifact order.
func (o Observation) State() (State, error) {
	if err := o.Validate(); err != nil {
		return StateIndeterminate, err
	}
	return classifyValidated(o), nil
}

// State classifies the only low-port states that may govern mutation.
type State string

const (
	// StateAbsent means neither canonical plist nor matching service is present.
	StateAbsent State = "absent"
	// StateExact means both canonical artifacts are uniquely Harbor-owned.
	StateExact State = "exact"
	// StateOwnedDrifted means owned evidence exists but needs a narrow repair decision.
	StateOwnedDrifted State = "owned-drifted"
	// StateForeign means observed state is not Harbor-owned.
	StateForeign State = "foreign"
	// StateAmbiguous means native facts cannot identify one safe state.
	StateAmbiguous State = "ambiguous"
	// StateIndeterminate means the required native facts are incomplete.
	StateIndeterminate State = "indeterminate"
)

// Change records post-mutation evidence.
type Change struct {
	// Attempted reports whether the adapter invoked a native mutation.
	Attempted bool
	// Changed reports whether fresh post-mutation facts differ from the precondition.
	Changed bool
	// Indeterminate reports that post-mutation facts could not be observed.
	Indeterminate bool
	// Before is the fresh precondition snapshot that authorized the mutation.
	Before Observation
	// After is the fresh post-mutation snapshot when observation succeeded.
	After Observation
}

// sameRequest compares the complete private authority carried by two requests.
func sameRequest(left, right Request) bool {
	return left.installationID == right.installationID &&
		left.ownerUID == right.ownerUID &&
		left.policyFingerprint == right.policyFingerprint &&
		left.httpUpstream == right.httpUpstream &&
		left.httpsUpstream == right.httpsUpstream
}

// cloneObservation prevents backend-owned artifact slices from crossing adapter boundaries.
func cloneObservation(observation Observation) Observation {
	cloned := observation
	cloned.Artifacts = append([]Artifact(nil), observation.Artifacts...)
	return cloned
}

// sameObservation compares validated facts through their canonical fingerprints.
func sameObservation(left, right Observation) bool {
	leftFingerprint, leftErr := left.Fingerprint()
	rightFingerprint, rightErr := right.Fingerprint()
	return leftErr == nil && rightErr == nil && bytes.Equal([]byte(leftFingerprint), []byte(rightFingerprint))
}

// validateCanonicalFingerprint accepts only lowercase SHA-256 text used by ownership and CAS evidence.
func validateCanonicalFingerprint(name, fingerprint string) error {
	if len(fingerprint) != canonicalFingerprintBytes {
		return fmt.Errorf("%s is invalid", name)
	}
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || hex.EncodeToString(decoded) != fingerprint {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}
