package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	windowsNRPTDisplayNamePrefix       = "GoForj Harbor Resolver "
	windowsNRPTOwnerPrefix             = "goforj.harbor.resolver "
	windowsNRPTFingerprintDomain       = "goforj.harbor.windows-nrpt-rule.v1"
	maximumWindowsNRPTNamespaces       = 32
	maximumWindowsNRPTTextBytes        = 1024
	maximumWindowsNRPTDisplayNameBytes = 512
	maximumWindowsNRPTCommentBytes     = 1024
)

// windowsNRPTRule is the complete reviewed property set of one local DnsClientNrptRule instance.
type windowsNRPTRule struct {
	Version                          uint32   `json:"version"`
	Name                             string   `json:"name"`
	Namespaces                       []string `json:"namespaces"`
	IPsecCARestriction               string   `json:"ipsec_ca_restriction"`
	DirectAccessDNSServers           []string `json:"direct_access_dns_servers"`
	DirectAccessEnabled              bool     `json:"direct_access_enabled"`
	DirectAccessProxyType            string   `json:"direct_access_proxy_type"`
	DirectAccessProxyName            string   `json:"direct_access_proxy_name"`
	DirectAccessQueryIPsecEncryption string   `json:"direct_access_query_ipsec_encryption"`
	DirectAccessQueryIPsecRequired   bool     `json:"direct_access_query_ipsec_required"`
	NameServers                      []string `json:"name_servers"`
	DNSSecEnabled                    bool     `json:"dnssec_enabled"`
	DNSSecQueryIPsecEncryption       string   `json:"dnssec_query_ipsec_encryption"`
	DNSSecQueryIPsecRequired         bool     `json:"dnssec_query_ipsec_required"`
	DNSSecValidationRequired         bool     `json:"dnssec_validation_required"`
	NameEncoding                     string   `json:"name_encoding"`
	DisplayName                      string   `json:"display_name"`
	Comment                          string   `json:"comment"`
}

// windowsNRPTExpectedRule binds one mutation to every relevant native rule seen by the adapter.
type windowsNRPTExpectedRule struct {
	Name                   string `json:"name"`
	NativeAttributesSHA256 string `json:"native_attributes_sha256"`
}

// windowsNRPTGuard identifies the sole owned native rule admitted for replacement or removal.
type windowsNRPTGuard struct {
	Exists                 bool   `json:"exists"`
	Name                   string `json:"name"`
	NativeAttributesSHA256 string `json:"native_attributes_sha256"`
}

// windowsNRPTStore confines native effects to complete local NRPT snapshots and guarded mutations.
type windowsNRPTStore interface {
	// snapshot returns every local rule relevant to the Harbor suffix or deterministic owned destination.
	snapshot(context.Context, Request) ([]windowsNRPTRule, error)
	// ensure creates or repairs one deterministic Harbor rule after rechecking every expected native rule.
	ensure(context.Context, Request, []windowsNRPTExpectedRule, windowsNRPTGuard) error
	// release removes only one guarded owned rule after rechecking every expected native rule.
	release(context.Context, Request, []windowsNRPTExpectedRule, windowsNRPTGuard) error
}

// windowsNRPTBackend implements adapter admission around Windows' local Name Resolution Policy Table.
type windowsNRPTBackend struct {
	store windowsNRPTStore
}

// newWindowsNRPTBackend injects native NRPT storage for portable safety tests.
func newWindowsNRPTBackend(store windowsNRPTStore) backend {
	return &windowsNRPTBackend{store: store}
}

// observe converts one complete bounded NRPT snapshot into platform-neutral resolver facts.
func (backend *windowsNRPTBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateWindowsNRPTRequest(request); err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	rules, err := backend.store.snapshot(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	return windowsNRPTObservationFromRules(ctx, request, rules)
}

// ensure applies only an absent rule or the uniquely owned drifted rule selected by the adapter.
func (backend *windowsNRPTBackend) ensure(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateWindowsNRPTRequest(request); err != nil {
		return err
	}
	if err := validateWindowsNRPTObservation(before, request); err != nil {
		return err
	}
	expected, err := windowsNRPTExpectedRules(before)
	if err != nil {
		return err
	}
	guard := windowsNRPTGuard{}
	switch assessment := classifyValidated(before); assessment.State {
	case StateAbsent:
	case StateOwnedDrifted:
		guard, err = uniqueWindowsNRPTOwnedGuard(before, request)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("Windows NRPT ensure rejected state %q", assessment.State)
	}
	return backend.store.ensure(ctx, request, expected, guard)
}

// release removes only the uniquely owned rule represented by an unchanged complete observation.
func (backend *windowsNRPTBackend) release(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateWindowsNRPTRequest(request); err != nil {
		return err
	}
	if err := validateWindowsNRPTObservation(before, request); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State == StateIndeterminate ||
		assessment.Owned != OwnedStateExact && assessment.Owned != OwnedStateDrifted {
		return fmt.Errorf("Windows NRPT release rejected state %q with owned state %q", assessment.State, assessment.Owned)
	}
	expected, err := windowsNRPTExpectedRules(before)
	if err != nil {
		return err
	}
	guard, err := uniqueWindowsNRPTOwnedGuard(before, request)
	if err != nil {
		return err
	}
	return backend.store.release(ctx, request, expected, guard)
}

// validateWindowsNRPTRequest confines this backend to Harbor's complete Windows 11 network profile.
func validateWindowsNRPTRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.WindowsNRPT {
		return fmt.Errorf("Windows NRPT backend rejected mechanism %q", request.Mechanism())
	}
	if request.Endpoint().Port() != 53 {
		return fmt.Errorf("Windows NRPT backend requires DNS port 53")
	}
	return nil
}

// validateWindowsNRPTObservation requires one complete immutable request snapshot before native mutation.
func validateWindowsNRPTObservation(observation Observation, request Request) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	if !sameRequest(observation.Request, request) {
		return fmt.Errorf("Windows NRPT observation belongs to another request")
	}
	if !observation.Complete || observation.Truncated {
		return fmt.Errorf("Windows NRPT mutation requires a complete observation")
	}
	return nil
}

// windowsNRPTObservationFromRules converts complete native records into sorted bounded facts.
func windowsNRPTObservationFromRules(
	ctx context.Context,
	request Request,
	rules []windowsNRPTRule,
) (Observation, error) {
	if err := validateWindowsNRPTRequest(request); err != nil {
		return Observation{}, err
	}
	if len(rules) > maximumRuleFacts {
		return Observation{}, fmt.Errorf("Windows NRPT relevant rules exceed limit %d", maximumRuleFacts)
	}
	rules = slices.Clone(rules)
	slices.SortFunc(rules, func(left windowsNRPTRule, right windowsNRPTRule) int {
		return strings.Compare(left.Name, right.Name)
	})

	observation := Observation{Request: request, Complete: true}
	for index, rule := range rules {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		if index > 0 && rule.Name == rules[index-1].Name {
			return Observation{}, fmt.Errorf("Windows NRPT snapshot repeats rule %q", rule.Name)
		}
		facts, err := windowsNRPTRuleFacts(rule, request)
		if err != nil {
			return Observation{}, fmt.Errorf("inspect Windows NRPT rule %q: %w", rule.Name, err)
		}
		if len(observation.Rules)+len(facts) > maximumRuleFacts {
			return Observation{}, fmt.Errorf("Windows NRPT facts exceed limit %d", maximumRuleFacts)
		}
		observation.Rules = append(observation.Rules, facts...)
	}
	return observation, nil
}

// windowsNRPTRuleFacts retains every namespace claim plus occupancy of Harbor's deterministic display name.
func windowsNRPTRuleFacts(rule windowsNRPTRule, request Request) ([]RuleFact, error) {
	if err := validateWindowsNRPTRule(rule); err != nil {
		return nil, err
	}
	namespaces := make([]string, 0, len(rule.Namespaces))
	seenNamespaces := make(map[string]struct{}, len(rule.Namespaces))
	for _, raw := range rule.Namespaces {
		namespace, relevant, err := windowsNRPTRelevantNamespace(raw, request.Suffix())
		if err != nil {
			return nil, err
		}
		if !relevant {
			continue
		}
		if _, duplicate := seenNamespaces[namespace]; duplicate {
			return nil, fmt.Errorf("Windows NRPT rule repeats relevant namespace %q", namespace)
		}
		seenNamespaces[namespace] = struct{}{}
		namespaces = append(namespaces, namespace)
	}
	fixedDestination := rule.DisplayName == windowsNRPTDisplayName(request)
	if len(namespaces) == 0 {
		if !fixedDestination {
			return nil, nil
		}
		namespaces = append(namespaces, request.Suffix())
	}
	slices.Sort(namespaces)

	servers, err := windowsNRPTServers(rule.NameServers)
	if err != nil {
		return nil, err
	}
	owner, ownerErr := parseWindowsNRPTOwnerComment(rule.Comment)
	if ownerErr != nil && strings.HasPrefix(rule.Comment, windowsNRPTOwnerPrefix) {
		return nil, ownerErr
	}
	ownedShape := ownerErr == nil &&
		fixedDestination &&
		slices.Equal(rule.Namespaces, []string{request.Suffix()}) &&
		len(namespaces) == 1 &&
		namespaces[0] == request.Suffix()
	nativeExact := ownedShape && windowsNRPTRuleNativeExact(rule, request)
	fingerprint := windowsNRPTRuleFingerprint(rule)

	facts := make([]RuleFact, 0, len(namespaces))
	for _, namespace := range namespaces {
		var factOwner *OwnerMarker
		if ownedShape {
			marker := owner
			factOwner = &marker
		}
		facts = append(facts, RuleFact{
			Mechanism:              networkpolicy.WindowsNRPT,
			NativeID:               rule.Name,
			Namespace:              namespace,
			Servers:                slices.Clone(servers),
			RouteOnly:              true,
			NativeExact:            nativeExact,
			NativeAttributesSHA256: fingerprint,
			Owner:                  factOwner,
		})
	}
	return facts, nil
}

// validateWindowsNRPTRule bounds every property that participates in classification or mutation guards.
func validateWindowsNRPTRule(rule windowsNRPTRule) error {
	if rule.Version == 0 {
		return fmt.Errorf("Windows NRPT rule version must be greater than zero")
	}
	if err := validateBoundedText("Windows NRPT rule name", rule.Name, maximumNativeIDLength); err != nil {
		return err
	}
	if len(rule.Namespaces) > maximumWindowsNRPTNamespaces {
		return fmt.Errorf("Windows NRPT namespaces exceed limit %d", maximumWindowsNRPTNamespaces)
	}
	if len(rule.NameServers) > maximumServersPerRule || len(rule.DirectAccessDNSServers) > maximumServersPerRule {
		return fmt.Errorf("Windows NRPT name servers exceed limit %d", maximumServersPerRule)
	}
	for _, values := range [][]string{rule.Namespaces, rule.NameServers, rule.DirectAccessDNSServers} {
		for _, value := range values {
			if err := validateWindowsNRPTText("Windows NRPT array value", value, maximumWindowsNRPTTextBytes, false); err != nil {
				return err
			}
		}
	}
	optional := []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "IPsec CA restriction", value: rule.IPsecCARestriction, maximum: maximumWindowsNRPTTextBytes},
		{name: "DirectAccess proxy type", value: rule.DirectAccessProxyType, maximum: maximumWindowsNRPTTextBytes},
		{name: "DirectAccess proxy name", value: rule.DirectAccessProxyName, maximum: maximumWindowsNRPTTextBytes},
		{name: "DirectAccess IPsec encryption", value: rule.DirectAccessQueryIPsecEncryption, maximum: maximumWindowsNRPTTextBytes},
		{name: "DNSSEC IPsec encryption", value: rule.DNSSecQueryIPsecEncryption, maximum: maximumWindowsNRPTTextBytes},
		{name: "name encoding", value: rule.NameEncoding, maximum: maximumWindowsNRPTTextBytes},
		{name: "display name", value: rule.DisplayName, maximum: maximumWindowsNRPTDisplayNameBytes},
		{name: "comment", value: rule.Comment, maximum: maximumWindowsNRPTCommentBytes},
	}
	for _, field := range optional {
		if err := validateWindowsNRPTText("Windows NRPT "+field.name, field.value, field.maximum, true); err != nil {
			return err
		}
	}
	return nil
}

// validateWindowsNRPTText rejects native strings that cannot be compared or logged as one bounded value.
func validateWindowsNRPTText(label string, value string, maximum int, emptyAllowed bool) error {
	if value == "" && emptyAllowed {
		return nil
	}
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return fmt.Errorf("%s is not bounded UTF-8", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			return fmt.Errorf("%s contains non-display text", label)
		}
	}
	return nil
}

// windowsNRPTRelevantNamespace maps suffix, FQDN, and any-rule spellings onto canonical conflict facts.
func windowsNRPTRelevantNamespace(value string, suffix string) (string, bool, error) {
	lower := strings.ToLower(value)
	if lower == "." {
		return suffix, true, nil
	}
	candidate := lower
	if !strings.HasPrefix(candidate, ".") {
		candidate = "." + candidate
	}
	if candidate != suffix && !strings.HasSuffix(candidate, suffix) {
		return "", false, nil
	}
	if err := validateNamespace(candidate); err != nil {
		return "", false, fmt.Errorf("Windows NRPT namespace %q is invalid: %w", value, err)
	}
	return candidate, true, nil
}

// windowsNRPTServers converts NRPT's portless addresses into the Windows policy's fixed DNS port.
func windowsNRPTServers(values []string) ([]netip.AddrPort, error) {
	servers := make([]netip.AddrPort, 0, len(values))
	seen := make(map[netip.Addr]struct{}, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil || address != address.Unmap() || address.String() != value || address.Zone() != "" {
			return nil, fmt.Errorf("Windows NRPT name server %q is not a canonical address", value)
		}
		if _, duplicate := seen[address]; duplicate {
			return nil, fmt.Errorf("Windows NRPT repeats name server %s", address)
		}
		seen[address] = struct{}{}
		servers = append(servers, netip.AddrPortFrom(address, 53))
	}
	slices.SortFunc(servers, func(left netip.AddrPort, right netip.AddrPort) int {
		return left.Compare(right)
	})
	return servers, nil
}

// windowsNRPTRuleNativeExact proves every route-affecting field has Harbor's canonical disabled-feature shape.
func windowsNRPTRuleNativeExact(rule windowsNRPTRule, request Request) bool {
	return slices.Equal(rule.Namespaces, []string{request.Suffix()}) &&
		slices.Equal(rule.NameServers, []string{request.Endpoint().Addr().String()}) &&
		!rule.DirectAccessEnabled &&
		!rule.DirectAccessQueryIPsecRequired &&
		!rule.DNSSecEnabled &&
		!rule.DNSSecQueryIPsecRequired &&
		!rule.DNSSecValidationRequired &&
		rule.NameEncoding == "Disable" &&
		rule.DisplayName == windowsNRPTDisplayName(request) &&
		rule.Comment == windowsNRPTOwnerComment(request)
}

// windowsNRPTDisplayName derives the stable local destination name from Harbor's installation identity.
func windowsNRPTDisplayName(request Request) string {
	return windowsNRPTDisplayNamePrefix + request.InstallationID()
}

// windowsNRPTOwnerComment emits the exact bounded marker carried by Harbor's deterministic display destination.
func windowsNRPTOwnerComment(request Request) string {
	marker := request.OwnerMarker()
	return fmt.Sprintf(
		"%sversion=%d installation=%s policy=%s",
		windowsNRPTOwnerPrefix,
		marker.Version,
		marker.InstallationID,
		marker.PolicyFingerprint,
	)
}

// parseWindowsNRPTOwnerComment validates the exact Harbor marker grammar without accepting aliases.
func parseWindowsNRPTOwnerComment(comment string) (OwnerMarker, error) {
	if !strings.HasPrefix(comment, windowsNRPTOwnerPrefix) {
		return OwnerMarker{}, fmt.Errorf("Windows NRPT rule has no Harbor owner marker")
	}
	fields := strings.Fields(strings.TrimPrefix(comment, windowsNRPTOwnerPrefix))
	if len(fields) != 3 {
		return OwnerMarker{}, fmt.Errorf("Windows NRPT owner marker requires version, installation, and policy")
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if !found || value == "" {
			return OwnerMarker{}, fmt.Errorf("Windows NRPT owner marker field %q is malformed", field)
		}
		if _, duplicate := values[key]; duplicate {
			return OwnerMarker{}, fmt.Errorf("Windows NRPT owner marker repeats %q", key)
		}
		values[key] = value
	}
	version, err := strconv.ParseUint(values["version"], 10, 16)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != values["version"] ||
		values["installation"] == "" || values["policy"] == "" {
		return OwnerMarker{}, fmt.Errorf("Windows NRPT owner marker is malformed")
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

// windowsNRPTRuleFingerprint hashes all reviewed raw properties using a PowerShell-reproducible encoding.
func windowsNRPTRuleFingerprint(rule windowsNRPTRule) string {
	lines := []string{windowsNRPTFingerprintDomain, strconv.FormatUint(uint64(rule.Version), 10)}
	lines = appendWindowsNRPTFingerprintArray(lines, rule.Namespaces)
	lines = appendWindowsNRPTFingerprintText(lines, rule.Name)
	lines = appendWindowsNRPTFingerprintText(lines, rule.IPsecCARestriction)
	lines = appendWindowsNRPTFingerprintArray(lines, rule.DirectAccessDNSServers)
	lines = appendWindowsNRPTFingerprintBool(lines, rule.DirectAccessEnabled)
	lines = appendWindowsNRPTFingerprintText(lines, rule.DirectAccessProxyType)
	lines = appendWindowsNRPTFingerprintText(lines, rule.DirectAccessProxyName)
	lines = appendWindowsNRPTFingerprintText(lines, rule.DirectAccessQueryIPsecEncryption)
	lines = appendWindowsNRPTFingerprintBool(lines, rule.DirectAccessQueryIPsecRequired)
	lines = appendWindowsNRPTFingerprintArray(lines, rule.NameServers)
	lines = appendWindowsNRPTFingerprintBool(lines, rule.DNSSecEnabled)
	lines = appendWindowsNRPTFingerprintText(lines, rule.DNSSecQueryIPsecEncryption)
	lines = appendWindowsNRPTFingerprintBool(lines, rule.DNSSecQueryIPsecRequired)
	lines = appendWindowsNRPTFingerprintBool(lines, rule.DNSSecValidationRequired)
	lines = appendWindowsNRPTFingerprintText(lines, rule.NameEncoding)
	lines = appendWindowsNRPTFingerprintText(lines, rule.DisplayName)
	lines = appendWindowsNRPTFingerprintText(lines, rule.Comment)
	digest := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(digest[:])
}

// appendWindowsNRPTFingerprintArray preserves native multiplicity and order without delimiter ambiguity.
func appendWindowsNRPTFingerprintArray(lines []string, values []string) []string {
	lines = append(lines, strconv.Itoa(len(values)))
	for _, value := range values {
		lines = appendWindowsNRPTFingerprintText(lines, value)
	}
	return lines
}

// appendWindowsNRPTFingerprintText base64-encodes arbitrary UTF-8 so one property always occupies one line.
func appendWindowsNRPTFingerprintText(lines []string, value string) []string {
	return append(lines, base64.StdEncoding.EncodeToString([]byte(value)))
}

// appendWindowsNRPTFingerprintBool keeps PowerShell and Go boolean spelling independent.
func appendWindowsNRPTFingerprintBool(lines []string, value bool) []string {
	if value {
		return append(lines, "1")
	}
	return append(lines, "0")
}

// windowsNRPTExpectedRules reconstructs the complete native precondition without duplicating multi-namespace facts.
func windowsNRPTExpectedRules(observation Observation) ([]windowsNRPTExpectedRule, error) {
	if err := observation.Validate(); err != nil {
		return nil, err
	}
	expectedByName := make(map[string]string, len(observation.Rules))
	for _, rule := range observation.Rules {
		if rule.Mechanism != networkpolicy.WindowsNRPT {
			return nil, fmt.Errorf("Windows NRPT observation contains mechanism %q", rule.Mechanism)
		}
		if existing, found := expectedByName[rule.NativeID]; found && existing != rule.NativeAttributesSHA256 {
			return nil, fmt.Errorf("Windows NRPT observation gives rule %q inconsistent native identities", rule.NativeID)
		}
		expectedByName[rule.NativeID] = rule.NativeAttributesSHA256
	}
	expected := make([]windowsNRPTExpectedRule, 0, len(expectedByName))
	for name, fingerprint := range expectedByName {
		expected = append(expected, windowsNRPTExpectedRule{Name: name, NativeAttributesSHA256: fingerprint})
	}
	slices.SortFunc(expected, func(left windowsNRPTExpectedRule, right windowsNRPTExpectedRule) int {
		return strings.Compare(left.Name, right.Name)
	})
	return expected, nil
}

// uniqueWindowsNRPTOwnedGuard returns the one exact native identity carrying this request's marker.
func uniqueWindowsNRPTOwnedGuard(observation Observation, request Request) (windowsNRPTGuard, error) {
	var owned *RuleFact
	for index := range observation.Rules {
		rule := &observation.Rules[index]
		if !markerMatchesRequest(rule.Owner, request) {
			continue
		}
		if owned != nil {
			return windowsNRPTGuard{}, fmt.Errorf("Windows NRPT observation contains multiple owned rules")
		}
		owned = rule
	}
	if owned == nil {
		return windowsNRPTGuard{}, fmt.Errorf("Windows NRPT observation contains no owned rule")
	}
	if err := validateFingerprintText("Windows NRPT native attribute fingerprint", owned.NativeAttributesSHA256); err != nil {
		return windowsNRPTGuard{}, err
	}
	return windowsNRPTGuard{
		Exists:                 true,
		Name:                   owned.NativeID,
		NativeAttributesSHA256: owned.NativeAttributesSHA256,
	}, nil
}
