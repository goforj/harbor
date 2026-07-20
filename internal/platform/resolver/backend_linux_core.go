package resolver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	fixedSystemdResolvedDirectory   = "/etc/systemd/resolved.conf.d"
	fixedSystemdResolvedName        = "90-goforj-harbor.conf"
	fixedSystemdResolvedPath        = fixedSystemdResolvedDirectory + "/" + fixedSystemdResolvedName
	systemdResolvedOwnerPrefix      = "# harbor-resolver-owner "
	maximumSystemdResolvedFileBytes = 64 << 10
	maximumSystemdResolvedLines     = 256
	maximumSystemdResolvedLineBytes = 1024
	maximumSystemdResolvedRuntime   = maximumRuleFacts
	systemdResolvedFileMode         = uint32(0o644)
)

// systemdResolvedArtifactMetadata is the portable security and identity subset of one Linux stat record.
type systemdResolvedArtifactMetadata struct {
	Regular              bool
	Device               uint64
	Inode                uint64
	UID                  uint32
	GID                  uint32
	Mode                 uint32
	LinkCount            uint64
	Size                 int64
	ModifiedTimeNS       int64
	ChangedTimeNS        int64
	UnsafeExtendedAccess bool
}

// systemdResolvedArtifact is the complete bounded fixed drop-in observed without following links.
type systemdResolvedArtifact struct {
	Exists   bool
	Content  []byte
	Metadata systemdResolvedArtifactMetadata
}

// systemdResolvedRuntimeServer is one server exposed by resolve1 with every routing-relevant attribute retained.
type systemdResolvedRuntimeServer struct {
	InterfaceIndex int32
	Endpoint       netip.AddrPort
	ServerName     string
}

// systemdResolvedRuntimeRule is one live route domain joined to the DNS servers on the same resolve1 scope.
type systemdResolvedRuntimeRule struct {
	InterfaceIndex int32
	Namespace      string
	RouteOnly      bool
	Servers        []systemdResolvedRuntimeServer
}

// systemdResolvedSnapshot binds the fixed ownership artifact to a complete stable resolve1 property read.
type systemdResolvedSnapshot struct {
	Artifact systemdResolvedArtifact
	Runtime  []systemdResolvedRuntimeRule
}

// systemdResolvedGuard binds mutation authority to the exact fixed artifact admitted by the adapter.
type systemdResolvedGuard struct {
	Exists                 bool
	Device                 uint64
	Inode                  uint64
	NativeAttributesSHA256 string
}

// systemdResolvedStore confines effects to Harbor's fixed drop-in and a fixed systemd-resolved reload operation.
type systemdResolvedStore interface {
	// recover repairs only Harbor-owned interrupted publications before public state is observed.
	recover(context.Context, Request) error
	// snapshot returns the fixed artifact and every live route that can claim the requested suffix.
	snapshot(context.Context, Request) (systemdResolvedSnapshot, error)
	// replace publishes canonical bytes only while the complete observation and artifact guard still match.
	replace(context.Context, Request, string, systemdResolvedGuard, []byte) error
	// remove retires only the exact fixed artifact named by an unchanged complete observation.
	remove(context.Context, Request, string, systemdResolvedGuard) error
}

// systemdResolvedBackend implements portable admission around Ubuntu's resolve1 manager and one fixed drop-in.
type systemdResolvedBackend struct {
	store systemdResolvedStore
}

// parsedSystemdResolvedArtifact retains only fields needed to correlate the fixed drop-in with live resolve1 state.
type parsedSystemdResolvedArtifact struct {
	Owner   *OwnerMarker
	Domains []systemdResolvedArtifactDomain
	Servers []systemdResolvedArtifactServer
}

// systemdResolvedArtifactDomain is one systemd route or search domain from the fixed drop-in.
type systemdResolvedArtifactDomain struct {
	Namespace string
	RouteOnly bool
}

// systemdResolvedArtifactServer is one fixed-drop-in DNS endpoint and optional TLS name.
type systemdResolvedArtifactServer struct {
	Endpoint   netip.AddrPort
	ServerName string
}

// newSystemdResolvedBackend injects fixed-path storage for portable backend tests.
func newSystemdResolvedBackend(store systemdResolvedStore) backend {
	return &systemdResolvedBackend{store: store}
}

// observe converts one stable resolve1 and fixed-artifact snapshot into bounded platform-neutral facts.
func (backend *systemdResolvedBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := backend.store.recover(ctx, request); err != nil {
		return Observation{}, err
	}
	snapshot, err := backend.store.snapshot(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	return systemdResolvedObservationFromSnapshot(request, snapshot)
}

// ensure publishes the fixed canonical drop-in only for an absent or uniquely owned drifted artifact.
func (backend *systemdResolvedBackend) ensure(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return err
	}
	if err := validateSystemdResolvedObservation(before, request); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	guard := systemdResolvedGuard{}
	switch assessment.State {
	case StateAbsent:
	case StateOwnedDrifted:
		var err error
		guard, err = uniqueSystemdResolvedOwnedGuard(before, request)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("systemd-resolved ensure rejected state %q", assessment.State)
	}
	return backend.store.replace(
		ctx,
		request,
		fingerprintValidated(before),
		guard,
		marshalSystemdResolvedValidated(request),
	)
}

// release removes only the fixed uniquely owned drop-in admitted by the observation.
func (backend *systemdResolvedBackend) release(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return err
	}
	if err := validateSystemdResolvedObservation(before, request); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State == StateIndeterminate || assessment.Owned != OwnedStateExact && assessment.Owned != OwnedStateDrifted {
		return fmt.Errorf("systemd-resolved release rejected state %q with owned state %q", assessment.State, assessment.Owned)
	}
	guard, err := uniqueSystemdResolvedOwnedGuard(before, request)
	if err != nil {
		return err
	}
	return backend.store.remove(ctx, request, fingerprintValidated(before), guard)
}

// validateSystemdResolvedRequest confines this backend to the declared Ubuntu 24.04 policy profile.
func validateSystemdResolvedRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.UbuntuSystemdResolved {
		return fmt.Errorf("systemd-resolved backend rejected mechanism %q", request.Mechanism())
	}
	return nil
}

// recoverSystemdResolvedStage distinguishes unpublished canonical staging from an exchanged prior artifact.
func recoverSystemdResolvedStage(fixed, stage systemdResolvedArtifact, request Request) (bool, error) {
	if !systemdResolvedArtifactOwnedByRequest(stage, request) {
		return false, fmt.Errorf("systemd-resolved stage is not owned by this request")
	}
	stageExact := bytes.Equal(stage.Content, marshalSystemdResolvedValidated(request))
	if !fixed.Exists {
		if !stageExact {
			return false, fmt.Errorf("systemd-resolved stage has no fixed artifact and is not unpublished canonical content")
		}
		return false, nil
	}
	if !systemdResolvedArtifactOwnedByRequest(fixed, request) {
		return false, fmt.Errorf("systemd-resolved stage has a foreign fixed artifact")
	}
	fixedExact := bytes.Equal(fixed.Content, marshalSystemdResolvedValidated(request))
	if stageExact && !fixedExact {
		return false, nil
	}
	if fixedExact {
		// An exact public artifact with a retained owned stage may have crossed exchange before a restart.
		return true, nil
	}
	return false, fmt.Errorf("systemd-resolved stage and fixed artifact are an ambiguous interrupted mutation")
}

// systemdResolvedArtifactOwnedByRequest requires the exact marker and immutable root-owned file shape.
func systemdResolvedArtifactOwnedByRequest(artifact systemdResolvedArtifact, request Request) bool {
	if !artifact.Exists || !secureSystemdResolvedArtifact(artifact.Metadata) {
		return false
	}
	parsed, err := parseSystemdResolvedArtifact(artifact.Content)
	return err == nil && parsed.Owner != nil && *parsed.Owner == request.OwnerMarker()
}

// validateSystemdResolvedObservation requires the exact immutable request admitted by the adapter.
func validateSystemdResolvedObservation(observation Observation, request Request) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	if !sameRequest(observation.Request, request) {
		return fmt.Errorf("systemd-resolved observation belongs to another request")
	}
	return nil
}

// systemdResolvedObservationFromSnapshot attributes live state only when the secure fixed artifact explains it exactly.
func systemdResolvedObservationFromSnapshot(
	request Request,
	snapshot systemdResolvedSnapshot,
) (Observation, error) {
	if err := validateSystemdResolvedSnapshot(snapshot, request); err != nil {
		return Observation{}, err
	}
	observation := Observation{Request: request, Complete: true}
	parsed, parseErr := parseSystemdResolvedArtifact(snapshot.Artifact.Content)
	artifactOwned := snapshot.Artifact.Exists &&
		parseErr == nil &&
		parsed.Owner != nil &&
		*parsed.Owner == request.OwnerMarker() &&
		secureSystemdResolvedArtifact(snapshot.Artifact.Metadata)

	if artifactOwned {
		if rule, runtimeIndex, correlated := correlatedSystemdResolvedOwnedRule(request, snapshot, parsed); correlated {
			observation.Rules = append(observation.Rules, rule)
			snapshot.Runtime = append(snapshot.Runtime[:runtimeIndex], snapshot.Runtime[runtimeIndex+1:]...)
		} else {
			observation.Rules = append(observation.Rules, systemdResolvedArtifactRule(request, snapshot.Artifact, parsed, parsed.Owner))
		}
	} else if snapshot.Artifact.Exists {
		observation.Rules = append(observation.Rules, systemdResolvedArtifactRule(request, snapshot.Artifact, parsed, nil))
	}

	for _, runtimeRule := range snapshot.Runtime {
		observation.Rules = append(observation.Rules, systemdResolvedForeignRuntimeRule(request, runtimeRule))
	}
	slices.SortFunc(observation.Rules, func(left RuleFact, right RuleFact) int {
		return strings.Compare(left.NativeID, right.NativeID)
	})
	return observation, nil
}

// validateSystemdResolvedSnapshot rejects incomplete, duplicated, or unbounded live resolver facts.
func validateSystemdResolvedSnapshot(snapshot systemdResolvedSnapshot, request Request) error {
	if snapshot.Artifact.Exists {
		if len(snapshot.Artifact.Content) > maximumSystemdResolvedFileBytes {
			return fmt.Errorf("systemd-resolved artifact exceeds %d bytes", maximumSystemdResolvedFileBytes)
		}
		if !snapshot.Artifact.Metadata.Regular || snapshot.Artifact.Metadata.Device == 0 || snapshot.Artifact.Metadata.Inode == 0 {
			return fmt.Errorf("systemd-resolved artifact has an invalid native identity")
		}
		if snapshot.Artifact.Metadata.Size != int64(len(snapshot.Artifact.Content)) ||
			snapshot.Artifact.Metadata.ModifiedTimeNS < 0 || snapshot.Artifact.Metadata.ChangedTimeNS < 0 {
			return fmt.Errorf("systemd-resolved artifact has inconsistent file metadata")
		}
	} else if len(snapshot.Artifact.Content) != 0 || snapshot.Artifact.Metadata != (systemdResolvedArtifactMetadata{}) {
		return fmt.Errorf("absent systemd-resolved artifact carries native state")
	}
	if len(snapshot.Runtime) > maximumSystemdResolvedRuntime {
		return fmt.Errorf("systemd-resolved runtime routes exceed limit %d", maximumSystemdResolvedRuntime)
	}
	for index := range snapshot.Runtime {
		rule := &snapshot.Runtime[index]
		if rule.InterfaceIndex < 0 {
			return fmt.Errorf("systemd-resolved runtime route %d has a negative interface index", index)
		}
		if err := validateNamespace(rule.Namespace); err != nil {
			return fmt.Errorf("systemd-resolved runtime route %d: %w", index, err)
		}
		if !namespaceClaimsSuffix(rule.Namespace, request.Suffix()) {
			return fmt.Errorf("systemd-resolved runtime route %d does not claim %q", index, request.Suffix())
		}
		if len(rule.Servers) > maximumServersPerRule {
			return fmt.Errorf("systemd-resolved runtime route %d servers exceed limit %d", index, maximumServersPerRule)
		}
		for serverIndex, server := range rule.Servers {
			if server.InterfaceIndex != rule.InterfaceIndex {
				return fmt.Errorf("systemd-resolved runtime route %d server %d belongs to another interface", index, serverIndex)
			}
			if err := validateServer(server.Endpoint); err != nil {
				return fmt.Errorf("systemd-resolved runtime route %d server %d: %w", index, serverIndex, err)
			}
			if err := validateSystemdResolvedServerName(server.ServerName); err != nil {
				return fmt.Errorf("systemd-resolved runtime route %d server %d: %w", index, serverIndex, err)
			}
			if serverIndex > 0 && compareSystemdResolvedRuntimeServer(rule.Servers[serverIndex-1], server) >= 0 {
				return fmt.Errorf("systemd-resolved runtime route %d servers must be unique and ordered", index)
			}
		}
		if index > 0 && compareSystemdResolvedRuntimeRule(snapshot.Runtime[index-1], *rule) >= 0 {
			return fmt.Errorf("systemd-resolved runtime routes must be unique and ordered")
		}
	}
	return nil
}

// correlatedSystemdResolvedOwnedRule joins the marker to one live route only when no unresolved route ambiguity remains.
func correlatedSystemdResolvedOwnedRule(
	request Request,
	snapshot systemdResolvedSnapshot,
	parsed parsedSystemdResolvedArtifact,
) (RuleFact, int, bool) {
	domains := relevantSystemdResolvedArtifactDomains(parsed.Domains, request.Suffix())
	if len(domains) != 1 || len(snapshot.Runtime) != 1 {
		return RuleFact{}, 0, false
	}
	runtimeRule := snapshot.Runtime[0]
	if runtimeRule.InterfaceIndex != 0 ||
		runtimeRule.Namespace != domains[0].Namespace ||
		runtimeRule.RouteOnly != domains[0].RouteOnly {
		return RuleFact{}, 0, false
	}
	artifactServers := systemdResolvedArtifactRuntimeServers(parsed.Servers, runtimeRule.InterfaceIndex)
	if !slices.Equal(artifactServers, runtimeRule.Servers) {
		return RuleFact{}, 0, false
	}
	servers := systemdResolvedRuleEndpoints(runtimeRule.Servers)
	canonical := bytes.Equal(snapshot.Artifact.Content, marshalSystemdResolvedValidated(request)) &&
		secureSystemdResolvedArtifact(snapshot.Artifact.Metadata) &&
		domains[0].Namespace == request.Suffix() &&
		domains[0].RouteOnly &&
		len(runtimeRule.Servers) == 1 &&
		runtimeRule.Servers[0].Endpoint == request.Endpoint() &&
		runtimeRule.Servers[0].ServerName == ""
	return RuleFact{
		Mechanism:              networkpolicy.UbuntuSystemdResolved,
		NativeID:               systemdResolvedArtifactNativeID(snapshot.Artifact.Metadata),
		Namespace:              runtimeRule.Namespace,
		Servers:                servers,
		RouteOnly:              runtimeRule.RouteOnly,
		NativeExact:            canonical,
		NativeAttributesSHA256: systemdResolvedArtifactFingerprint(snapshot.Artifact, &runtimeRule),
		Owner:                  parsed.Owner,
	}, 0, true
}

// systemdResolvedArtifactRule retains a secure owned marker only when native ownership makes repair safe.
func systemdResolvedArtifactRule(
	request Request,
	artifact systemdResolvedArtifact,
	parsed parsedSystemdResolvedArtifact,
	owner *OwnerMarker,
) RuleFact {
	domains := relevantSystemdResolvedArtifactDomains(parsed.Domains, request.Suffix())
	namespace := request.Suffix()
	routeOnly := false
	if len(domains) == 1 {
		namespace = domains[0].Namespace
		routeOnly = domains[0].RouteOnly
	}
	servers := make([]netip.AddrPort, 0, len(parsed.Servers))
	for _, server := range parsed.Servers {
		servers = append(servers, server.Endpoint)
	}
	slices.SortFunc(servers, func(left netip.AddrPort, right netip.AddrPort) int { return left.Compare(right) })
	servers = slices.Compact(servers)
	if len(servers) > maximumServersPerRule {
		servers = nil
	}
	return RuleFact{
		Mechanism:              networkpolicy.UbuntuSystemdResolved,
		NativeID:               systemdResolvedArtifactNativeID(artifact.Metadata),
		Namespace:              namespace,
		Servers:                servers,
		RouteOnly:              routeOnly,
		NativeExact:            false,
		NativeAttributesSHA256: systemdResolvedArtifactFingerprint(artifact, nil),
		Owner:                  owner,
	}
}

// systemdResolvedForeignRuntimeRule exposes one live unowned route without manufacturing file ownership.
func systemdResolvedForeignRuntimeRule(request Request, runtimeRule systemdResolvedRuntimeRule) RuleFact {
	return RuleFact{
		Mechanism:              networkpolicy.UbuntuSystemdResolved,
		NativeID:               systemdResolvedRuntimeNativeID(runtimeRule),
		Namespace:              runtimeRule.Namespace,
		Servers:                systemdResolvedRuleEndpoints(runtimeRule.Servers),
		RouteOnly:              runtimeRule.RouteOnly,
		NativeExact:            false,
		NativeAttributesSHA256: systemdResolvedRuntimeFingerprint(runtimeRule),
	}
}

// secureSystemdResolvedArtifact requires the immutable root-owned shape admitted for repair or removal.
func secureSystemdResolvedArtifact(metadata systemdResolvedArtifactMetadata) bool {
	return metadata.Regular &&
		metadata.UID == 0 &&
		metadata.GID == 0 &&
		metadata.Mode == systemdResolvedFileMode &&
		metadata.LinkCount == 1 &&
		!metadata.UnsafeExtendedAccess
}

// uniqueSystemdResolvedOwnedGuard returns the exact fixed-file identity carrying this request's marker.
func uniqueSystemdResolvedOwnedGuard(observation Observation, request Request) (systemdResolvedGuard, error) {
	var owned *RuleFact
	for index := range observation.Rules {
		rule := &observation.Rules[index]
		if !markerMatchesRequest(rule.Owner, request) {
			continue
		}
		if owned != nil {
			return systemdResolvedGuard{}, fmt.Errorf("systemd-resolved observation contains multiple owned rules")
		}
		owned = rule
	}
	if owned == nil {
		return systemdResolvedGuard{}, fmt.Errorf("systemd-resolved observation contains no owned rule")
	}
	return systemdResolvedGuardFromRule(*owned)
}

// systemdResolvedGuardFromRule reconstructs the fixed-file CAS guard from bounded facts.
func systemdResolvedGuardFromRule(rule RuleFact) (systemdResolvedGuard, error) {
	device, inode, err := parseSystemdResolvedArtifactNativeID(rule.NativeID)
	if err != nil {
		return systemdResolvedGuard{}, err
	}
	if err := validateFingerprintText("systemd-resolved native attribute fingerprint", rule.NativeAttributesSHA256); err != nil {
		return systemdResolvedGuard{}, err
	}
	return systemdResolvedGuard{
		Exists:                 true,
		Device:                 device,
		Inode:                  inode,
		NativeAttributesSHA256: rule.NativeAttributesSHA256,
	}, nil
}

// validateSystemdResolvedGuard rejects guards that could name anything outside the fixed artifact identity.
func validateSystemdResolvedGuard(guard systemdResolvedGuard) error {
	if !guard.Exists {
		if guard.Device != 0 || guard.Inode != 0 || guard.NativeAttributesSHA256 != "" {
			return fmt.Errorf("absent systemd-resolved guard carries native identity")
		}
		return nil
	}
	if guard.Device == 0 || guard.Inode == 0 {
		return fmt.Errorf("systemd-resolved guard has an invalid file identity")
	}
	return validateFingerprintText("systemd-resolved guard native attribute fingerprint", guard.NativeAttributesSHA256)
}

// matchSystemdResolvedMutationState re-proves the complete runtime observation and exact fixed artifact before mutation.
func matchSystemdResolvedMutationState(
	request Request,
	snapshot systemdResolvedSnapshot,
	expectedFingerprint string,
	guard systemdResolvedGuard,
) error {
	if err := validateFingerprintText("systemd-resolved expected observation fingerprint", expectedFingerprint); err != nil {
		return err
	}
	if err := validateSystemdResolvedGuard(guard); err != nil {
		return err
	}
	observation, err := systemdResolvedObservationFromSnapshot(request, snapshot)
	if err != nil {
		return err
	}
	if fingerprintValidated(observation) != expectedFingerprint {
		return fmt.Errorf("systemd-resolved observation changed before mutation")
	}
	if snapshot.Artifact.Exists != guard.Exists {
		return fmt.Errorf("systemd-resolved fixed artifact existence changed before mutation")
	}
	if !guard.Exists {
		return nil
	}
	if snapshot.Artifact.Metadata.Device != guard.Device || snapshot.Artifact.Metadata.Inode != guard.Inode {
		return fmt.Errorf("systemd-resolved fixed artifact identity changed before mutation")
	}
	parsed, err := parseSystemdResolvedArtifact(snapshot.Artifact.Content)
	if err != nil || parsed.Owner == nil || *parsed.Owner != request.OwnerMarker() ||
		!secureSystemdResolvedArtifact(snapshot.Artifact.Metadata) {
		return fmt.Errorf("systemd-resolved fixed artifact ownership changed before mutation")
	}
	if systemdResolvedArtifactFingerprint(snapshot.Artifact, correlatedRuntimeForArtifact(request, snapshot, parsed)) != guard.NativeAttributesSHA256 {
		return fmt.Errorf("systemd-resolved fixed artifact attributes changed before mutation")
	}
	return nil
}

// correlatedRuntimeForArtifact returns the runtime rule incorporated into an owned artifact fingerprint, if any.
func correlatedRuntimeForArtifact(
	request Request,
	snapshot systemdResolvedSnapshot,
	parsed parsedSystemdResolvedArtifact,
) *systemdResolvedRuntimeRule {
	_, index, correlated := correlatedSystemdResolvedOwnedRule(request, snapshot, parsed)
	if !correlated {
		return nil
	}
	runtimeRule := snapshot.Runtime[index]
	return &runtimeRule
}

// marshalSystemdResolved emits one canonical route-only global resolver drop-in.
func marshalSystemdResolved(request Request) ([]byte, error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return nil, err
	}
	return marshalSystemdResolvedValidated(request), nil
}

// marshalSystemdResolvedValidated emits canonical bytes after request validation at the boundary.
func marshalSystemdResolvedValidated(request Request) []byte {
	return []byte(fmt.Sprintf(
		"# GoForj Harbor managed systemd-resolved route.\n"+
			"%sversion=1 installation=%s policy=%s\n"+
			"[Resolve]\n"+
			"DNS=%s\n"+
			"Domains=~%s\n",
		systemdResolvedOwnerPrefix,
		request.InstallationID(),
		request.PolicyFingerprint(),
		request.Endpoint(),
		strings.TrimPrefix(request.Suffix(), "."),
	))
}

// parseSystemdResolvedArtifact parses bounded fixed-drop-in fields without treating unknown directives as authority.
func parseSystemdResolvedArtifact(content []byte) (parsedSystemdResolvedArtifact, error) {
	if len(content) == 0 || len(content) > maximumSystemdResolvedFileBytes {
		return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact has invalid size %d", len(content))
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, '\r') >= 0 {
		return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact is not canonical UTF-8 text")
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > maximumSystemdResolvedLines+1 {
		return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact lines exceed limit %d", maximumSystemdResolvedLines)
	}
	parsed := parsedSystemdResolvedArtifact{}
	section := ""
	for index, rawLine := range lines {
		if len(rawLine) > maximumSystemdResolvedLineBytes {
			return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d exceeds limit", index+1)
		}
		for _, character := range rawLine {
			if character > unicode.MaxASCII || unicode.IsControl(character) && character != '\t' {
				return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d contains unsupported text", index+1)
			}
		}
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, systemdResolvedOwnerPrefix) {
				if parsed.Owner != nil {
					return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact repeats its owner marker")
				}
				owner, err := parseSystemdResolvedOwnerMarker(line)
				if err != nil {
					return parsedSystemdResolvedArtifact{}, err
				}
				parsed.Owner = &owner
			} else if strings.HasPrefix(line, "# harbor-resolver-owner") {
				return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact contains a malformed reserved owner marker")
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			if len(line) < 3 || !strings.HasSuffix(line, "]") {
				return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d has an invalid section", index+1)
			}
			section = line[1 : len(line)-1]
			continue
		}
		if section != "Resolve" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || strings.TrimSpace(key) != key {
			return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d has an invalid assignment", index+1)
		}
		switch key {
		case "DNS":
			servers, err := parseSystemdResolvedServers(value)
			if err != nil {
				return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d DNS: %w", index+1, err)
			}
			if value == "" {
				parsed.Servers = nil
			} else {
				parsed.Servers = append(parsed.Servers, servers...)
			}
		case "Domains":
			domains, err := parseSystemdResolvedDomains(value)
			if err != nil {
				return parsedSystemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact line %d Domains: %w", index+1, err)
			}
			if value == "" {
				parsed.Domains = nil
			} else {
				parsed.Domains = append(parsed.Domains, domains...)
			}
		}
	}
	return parsed, nil
}

// parseSystemdResolvedOwnerMarker validates the exact bounded Harbor ownership comment grammar.
func parseSystemdResolvedOwnerMarker(line string) (OwnerMarker, error) {
	fields := strings.Fields(strings.TrimPrefix(line, systemdResolvedOwnerPrefix))
	if len(fields) != 3 {
		return OwnerMarker{}, fmt.Errorf("systemd-resolved owner marker must contain exactly three fields")
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if !found || key == "" || value == "" {
			return OwnerMarker{}, fmt.Errorf("systemd-resolved owner marker field is malformed")
		}
		if _, duplicate := values[key]; duplicate {
			return OwnerMarker{}, fmt.Errorf("systemd-resolved owner marker repeats field %q", key)
		}
		values[key] = value
	}
	if len(values) != 3 || values["version"] == "" || values["installation"] == "" || values["policy"] == "" {
		return OwnerMarker{}, fmt.Errorf("systemd-resolved owner marker fields are incomplete")
	}
	version, err := strconv.ParseUint(values["version"], 10, 16)
	if err != nil || strconv.FormatUint(version, 10) != values["version"] {
		return OwnerMarker{}, fmt.Errorf("systemd-resolved owner marker version is not canonical")
	}
	marker := OwnerMarker{
		Version:           uint16(version),
		InstallationID:    values["installation"],
		PolicyFingerprint: values["policy"],
	}
	if err := marker.Validate(); err != nil {
		return OwnerMarker{}, err
	}
	return marker, nil
}

// parseSystemdResolvedServers parses the bounded DNS= subset required by the Ubuntu profile.
func parseSystemdResolvedServers(value string) ([]systemdResolvedArtifactServer, error) {
	fields := strings.Fields(value)
	if len(fields) > maximumServersPerRule {
		return nil, fmt.Errorf("servers exceed limit %d", maximumServersPerRule)
	}
	servers := make([]systemdResolvedArtifactServer, 0, len(fields))
	for _, field := range fields {
		addressText, serverName, _ := strings.Cut(field, "#")
		if strings.Contains(addressText, "%") {
			return nil, fmt.Errorf("interface-scoped DNS servers are outside Harbor's fixed global profile")
		}
		if err := validateSystemdResolvedServerName(serverName); err != nil {
			return nil, err
		}
		endpoint, err := parseSystemdResolvedEndpoint(addressText)
		if err != nil {
			return nil, err
		}
		servers = append(servers, systemdResolvedArtifactServer{Endpoint: endpoint, ServerName: serverName})
	}
	slices.SortFunc(servers, compareSystemdResolvedArtifactServer)
	for index := 1; index < len(servers); index++ {
		if compareSystemdResolvedArtifactServer(servers[index-1], servers[index]) == 0 {
			return nil, fmt.Errorf("DNS servers must be unique")
		}
	}
	return servers, nil
}

// parseSystemdResolvedEndpoint accepts canonical IPv4/IPv6 addresses with an optional nonzero port.
func parseSystemdResolvedEndpoint(value string) (netip.AddrPort, error) {
	if endpoint, err := netip.ParseAddrPort(value); err == nil {
		if err := validateServer(endpoint); err != nil {
			return netip.AddrPort{}, err
		}
		return endpoint, nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil || address != address.Unmap() {
		return netip.AddrPort{}, fmt.Errorf("DNS server %q is not a canonical address or address:port", value)
	}
	endpoint := netip.AddrPortFrom(address, 53)
	if err := validateServer(endpoint); err != nil {
		return netip.AddrPort{}, err
	}
	return endpoint, nil
}

// parseSystemdResolvedDomains parses canonical search and route-only domain tokens.
func parseSystemdResolvedDomains(value string) ([]systemdResolvedArtifactDomain, error) {
	fields := strings.Fields(value)
	if len(fields) > maximumSystemdResolvedRuntime {
		return nil, fmt.Errorf("domains exceed limit %d", maximumSystemdResolvedRuntime)
	}
	domains := make([]systemdResolvedArtifactDomain, 0, len(fields))
	for _, field := range fields {
		routeOnly := strings.HasPrefix(field, "~")
		if routeOnly {
			field = strings.TrimPrefix(field, "~")
		}
		if field == "." {
			continue
		}
		namespace := "." + strings.TrimSuffix(field, ".")
		if err := validateNamespace(namespace); err != nil {
			return nil, err
		}
		domains = append(domains, systemdResolvedArtifactDomain{Namespace: namespace, RouteOnly: routeOnly})
	}
	slices.SortFunc(domains, compareSystemdResolvedArtifactDomain)
	for index := 1; index < len(domains); index++ {
		if compareSystemdResolvedArtifactDomain(domains[index-1], domains[index]) == 0 {
			return nil, fmt.Errorf("domains must be unique")
		}
	}
	return domains, nil
}

// validateSystemdResolvedServerName accepts only a bounded canonical DNS name when DNS-over-TLS metadata is present.
func validateSystemdResolvedServerName(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 253 || value != strings.ToLower(value) || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return fmt.Errorf("systemd-resolved DNS server name %q is not canonical", value)
	}
	return validateNamespace("." + value)
}

// relevantSystemdResolvedArtifactDomains retains only fixed-file domains that can claim the requested suffix.
func relevantSystemdResolvedArtifactDomains(
	domains []systemdResolvedArtifactDomain,
	suffix string,
) []systemdResolvedArtifactDomain {
	relevant := make([]systemdResolvedArtifactDomain, 0, len(domains))
	for _, domain := range domains {
		if namespaceClaimsSuffix(domain.Namespace, suffix) {
			relevant = append(relevant, domain)
		}
	}
	return relevant
}

// systemdResolvedArtifactRuntimeServers converts fixed-file servers into the exact live scope representation.
func systemdResolvedArtifactRuntimeServers(
	servers []systemdResolvedArtifactServer,
	interfaceIndex int32,
) []systemdResolvedRuntimeServer {
	result := make([]systemdResolvedRuntimeServer, len(servers))
	for index, server := range servers {
		result[index] = systemdResolvedRuntimeServer{
			InterfaceIndex: interfaceIndex,
			Endpoint:       server.Endpoint,
			ServerName:     server.ServerName,
		}
	}
	slices.SortFunc(result, compareSystemdResolvedRuntimeServer)
	return result
}

// systemdResolvedRuleEndpoints projects normalized runtime servers into platform-neutral facts.
func systemdResolvedRuleEndpoints(servers []systemdResolvedRuntimeServer) []netip.AddrPort {
	result := make([]netip.AddrPort, len(servers))
	for index, server := range servers {
		result[index] = server.Endpoint
	}
	slices.SortFunc(result, func(left netip.AddrPort, right netip.AddrPort) int { return left.Compare(right) })
	return slices.Compact(result)
}

// compareSystemdResolvedRuntimeServer provides one canonical ordering for live DNSEx facts.
func compareSystemdResolvedRuntimeServer(left systemdResolvedRuntimeServer, right systemdResolvedRuntimeServer) int {
	if left.InterfaceIndex < right.InterfaceIndex {
		return -1
	}
	if left.InterfaceIndex > right.InterfaceIndex {
		return 1
	}
	if compared := left.Endpoint.Compare(right.Endpoint); compared != 0 {
		return compared
	}
	return strings.Compare(left.ServerName, right.ServerName)
}

// compareSystemdResolvedRuntimeRule provides one canonical ordering for live route facts.
func compareSystemdResolvedRuntimeRule(left systemdResolvedRuntimeRule, right systemdResolvedRuntimeRule) int {
	if left.InterfaceIndex < right.InterfaceIndex {
		return -1
	}
	if left.InterfaceIndex > right.InterfaceIndex {
		return 1
	}
	if compared := strings.Compare(left.Namespace, right.Namespace); compared != 0 {
		return compared
	}
	if left.RouteOnly != right.RouteOnly {
		if !left.RouteOnly {
			return -1
		}
		return 1
	}
	for index := 0; index < min(len(left.Servers), len(right.Servers)); index++ {
		if compared := compareSystemdResolvedRuntimeServer(left.Servers[index], right.Servers[index]); compared != 0 {
			return compared
		}
	}
	return len(left.Servers) - len(right.Servers)
}

// compareSystemdResolvedArtifactServer provides one canonical ordering for parsed DNS= facts.
func compareSystemdResolvedArtifactServer(left systemdResolvedArtifactServer, right systemdResolvedArtifactServer) int {
	if compared := left.Endpoint.Compare(right.Endpoint); compared != 0 {
		return compared
	}
	return strings.Compare(left.ServerName, right.ServerName)
}

// compareSystemdResolvedArtifactDomain provides one canonical ordering for parsed Domains= facts.
func compareSystemdResolvedArtifactDomain(left systemdResolvedArtifactDomain, right systemdResolvedArtifactDomain) int {
	if compared := strings.Compare(left.Namespace, right.Namespace); compared != 0 {
		return compared
	}
	if left.RouteOnly == right.RouteOnly {
		return 0
	}
	if !left.RouteOnly {
		return -1
	}
	return 1
}

// systemdResolvedArtifactNativeID encodes only the fixed artifact name and exact filesystem identity.
func systemdResolvedArtifactNativeID(metadata systemdResolvedArtifactMetadata) string {
	return fmt.Sprintf("%s@%016x:%016x", fixedSystemdResolvedName, metadata.Device, metadata.Inode)
}

// parseSystemdResolvedArtifactNativeID parses the one native ID shape emitted for the fixed drop-in.
func parseSystemdResolvedArtifactNativeID(value string) (uint64, uint64, error) {
	prefix := fixedSystemdResolvedName + "@"
	encoded, found := strings.CutPrefix(value, prefix)
	if !found {
		return 0, 0, fmt.Errorf("systemd-resolved native ID does not name the fixed artifact")
	}
	deviceText, inodeText, found := strings.Cut(encoded, ":")
	if !found || len(deviceText) != 16 || len(inodeText) != 16 {
		return 0, 0, fmt.Errorf("systemd-resolved native ID has an invalid identity encoding")
	}
	device, err := strconv.ParseUint(deviceText, 16, 64)
	if err != nil || fmt.Sprintf("%016x", device) != deviceText || device == 0 {
		return 0, 0, fmt.Errorf("systemd-resolved native ID has an invalid device")
	}
	inode, err := strconv.ParseUint(inodeText, 16, 64)
	if err != nil || fmt.Sprintf("%016x", inode) != inodeText || inode == 0 {
		return 0, 0, fmt.Errorf("systemd-resolved native ID has an invalid inode")
	}
	return device, inode, nil
}

// systemdResolvedRuntimeNativeID identifies one live resolve1 scope and domain without accepting it as mutation authority.
func systemdResolvedRuntimeNativeID(rule systemdResolvedRuntimeRule) string {
	domainDigest := sha256.Sum256([]byte(rule.Namespace))
	return fmt.Sprintf("resolve1@%08x:%s", uint32(rule.InterfaceIndex), hex.EncodeToString(domainDigest[:8]))
}

// systemdResolvedArtifactFingerprint binds all fixed-file safety metadata and optionally its correlated live route.
func systemdResolvedArtifactFingerprint(
	artifact systemdResolvedArtifact,
	runtimeRule *systemdResolvedRuntimeRule,
) string {
	payload := append([]byte(nil), "goforj.harbor.systemd-resolved-artifact.v1\x00"...)
	payload = binary.AppendUvarint(payload, artifact.Metadata.Device)
	payload = binary.AppendUvarint(payload, artifact.Metadata.Inode)
	payload = binary.AppendUvarint(payload, uint64(artifact.Metadata.UID))
	payload = binary.AppendUvarint(payload, uint64(artifact.Metadata.GID))
	payload = binary.AppendUvarint(payload, uint64(artifact.Metadata.Mode))
	payload = binary.AppendUvarint(payload, artifact.Metadata.LinkCount)
	payload = binary.AppendVarint(payload, artifact.Metadata.Size)
	payload = binary.AppendVarint(payload, artifact.Metadata.ModifiedTimeNS)
	payload = binary.AppendVarint(payload, artifact.Metadata.ChangedTimeNS)
	payload = appendBool(payload, artifact.Metadata.Regular)
	payload = appendBool(payload, artifact.Metadata.UnsafeExtendedAccess)
	payload = appendBytes(payload, artifact.Content)
	payload = appendBool(payload, runtimeRule != nil)
	if runtimeRule != nil {
		payload = appendBytes(payload, encodeSystemdResolvedRuntimeRule(*runtimeRule))
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// systemdResolvedRuntimeFingerprint binds every live resolve1 attribute represented by one route fact.
func systemdResolvedRuntimeFingerprint(rule systemdResolvedRuntimeRule) string {
	payload := append([]byte(nil), "goforj.harbor.systemd-resolved-runtime.v1\x00"...)
	payload = append(payload, encodeSystemdResolvedRuntimeRule(rule)...)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// encodeSystemdResolvedRuntimeRule provides one unambiguous representation for runtime CAS evidence.
func encodeSystemdResolvedRuntimeRule(rule systemdResolvedRuntimeRule) []byte {
	payload := binary.AppendVarint(nil, int64(rule.InterfaceIndex))
	payload = appendString(payload, rule.Namespace)
	payload = appendBool(payload, rule.RouteOnly)
	payload = binary.AppendUvarint(payload, uint64(len(rule.Servers)))
	for _, server := range rule.Servers {
		payload = binary.AppendVarint(payload, int64(server.InterfaceIndex))
		payload = appendServer(payload, server.Endpoint)
		payload = appendString(payload, server.ServerName)
	}
	return payload
}
