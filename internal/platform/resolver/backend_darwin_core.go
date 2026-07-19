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
	"unicode/utf8"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	fixedDarwinResolverDirectory       = "/etc/resolver"
	fixedDarwinResolverName            = "test"
	fixedDarwinResolverPath            = fixedDarwinResolverDirectory + "/" + fixedDarwinResolverName
	darwinResolverOwnerPrefix          = "# harbor-resolver-owner "
	darwinResolverOrphanPrefix         = ".harbor-resolver-"
	darwinResolverQuarantinePrefix     = ".harbor-resolver-quarantine-"
	darwinResolverOrphanHexBytes       = 16
	maximumDarwinResolverOrphans       = 16
	maximumDarwinResolverEntries       = maximumRuleFacts
	maximumDarwinResolverFileBytes     = 64 << 10
	maximumDarwinResolverLines         = 256
	maximumDarwinResolverLineBytes     = 1024
	maximumDarwinResolverDirectiveArgs = 32
	darwinResolverFileMode             = uint32(0o644)
)

// darwinResolverMetadata is the portable security and identity subset of one native stat record.
type darwinResolverMetadata struct {
	Regular    bool
	Device     uint64
	Inode      uint64
	Generation uint32
	UID        uint32
	GID        uint32
	Mode       uint32
	Flags      uint32
	LinkCount  uint64
}

// darwinResolverEntry is one direct, fully read file from the fixed resolver directory.
type darwinResolverEntry struct {
	Name     string
	Content  []byte
	Metadata darwinResolverMetadata
}

// darwinResolverGuard binds a mutation to the exact destination state admitted by the adapter.
type darwinResolverGuard struct {
	Exists                 bool
	Name                   string
	Device                 uint64
	Inode                  uint64
	Generation             uint32
	NativeAttributesSHA256 string
}

// darwinResolverStore confines production effects to one fixed directory and destination.
type darwinResolverStore interface {
	// snapshot returns every direct resolver file or fails without claiming completeness.
	snapshot(context.Context) ([]darwinResolverEntry, error)
	// replace atomically publishes canonical bytes only while the complete observation and destination guard match.
	replace(context.Context, Request, string, darwinResolverGuard, []byte) error
	// remove unlinks only the exact destination named by an unchanged complete observation and admitted owned guard.
	remove(context.Context, Request, string, darwinResolverGuard) error
}

// darwinResolverBackend implements platform-neutral admission around one fixed macOS resolver file.
type darwinResolverBackend struct {
	store darwinResolverStore
}

// parsedDarwinResolver contains only resolver(5) fields needed for classification and ownership.
type parsedDarwinResolver struct {
	Namespace string
	Servers   []netip.AddrPort
	Owner     *OwnerMarker
}

// newDarwinResolverBackend injects fixed-path storage for portable backend tests.
func newDarwinResolverBackend(store darwinResolverStore) backend {
	return &darwinResolverBackend{store: store}
}

// observe converts a complete secure directory snapshot into bounded platform-neutral facts.
func (backend *darwinResolverBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateDarwinResolverRequest(request); err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	entries, err := backend.store.snapshot(ctx)
	if err != nil {
		return Observation{}, err
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	return darwinResolverObservationFromEntries(ctx, request, entries)
}

// darwinResolverObservationFromEntries converts a complete bounded directory snapshot into canonical facts.
func darwinResolverObservationFromEntries(
	ctx context.Context,
	request Request,
	entries []darwinResolverEntry,
) (Observation, error) {
	if len(entries) > maximumDarwinResolverEntries {
		return Observation{}, fmt.Errorf("Darwin resolver directory entries exceed limit %d", maximumDarwinResolverEntries)
	}
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(left darwinResolverEntry, right darwinResolverEntry) int {
		return strings.Compare(left.Name, right.Name)
	})

	observation := Observation{Request: request, Complete: true}
	for index, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		if index > 0 && entry.Name == entries[index-1].Name {
			return Observation{}, fmt.Errorf("Darwin resolver directory repeats entry %q", entry.Name)
		}
		fact, relevant, err := darwinResolverRuleFact(entry, request)
		if err != nil {
			return Observation{}, fmt.Errorf("inspect Darwin resolver entry %q: %w", entry.Name, err)
		}
		if relevant {
			observation.Rules = append(observation.Rules, fact)
		}
	}
	return observation, nil
}

// ensure publishes only canonical fixed-path bytes for an absent or uniquely owned drifted artifact.
func (backend *darwinResolverBackend) ensure(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateDarwinResolverRequest(request); err != nil {
		return err
	}
	if err := validateDarwinResolverObservation(before, request); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	guard := darwinResolverGuard{Name: fixedDarwinResolverName}
	switch assessment.State {
	case StateAbsent:
	case StateOwnedDrifted:
		var err error
		guard, err = uniqueDarwinOwnedGuard(before, request)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("Darwin resolver ensure rejected state %q", assessment.State)
	}
	return backend.store.replace(
		ctx,
		request,
		fingerprintValidated(before),
		guard,
		marshalDarwinResolverValidated(request),
	)
}

// release removes only the fixed uniquely owned artifact admitted by the observation.
func (backend *darwinResolverBackend) release(
	ctx context.Context,
	request Request,
	before Observation,
) error {
	if err := validateDarwinResolverRequest(request); err != nil {
		return err
	}
	if err := validateDarwinResolverObservation(before, request); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State == StateIndeterminate || assessment.Owned != OwnedStateExact && assessment.Owned != OwnedStateDrifted {
		return fmt.Errorf("Darwin resolver release rejected state %q with owned state %q", assessment.State, assessment.Owned)
	}
	guard, err := uniqueDarwinOwnedGuard(before, request)
	if err != nil {
		return err
	}
	return backend.store.remove(ctx, request, fingerprintValidated(before), guard)
}

// validateDarwinResolverRequest confines this backend to the canonical macOS policy profile.
func validateDarwinResolverRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.DarwinResolverFile {
		return fmt.Errorf("Darwin resolver backend rejected mechanism %q", request.Mechanism())
	}
	return nil
}

// validateDarwinResolverObservation requires the exact immutable request admitted by the adapter.
func validateDarwinResolverObservation(observation Observation, request Request) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	if !sameRequest(observation.Request, request) {
		return fmt.Errorf("Darwin resolver observation belongs to another request")
	}
	return nil
}

// uniqueDarwinOwnedGuard returns one exact-path guard for the current request marker.
func uniqueDarwinOwnedGuard(observation Observation, request Request) (darwinResolverGuard, error) {
	var owned *RuleFact
	for index := range observation.Rules {
		rule := &observation.Rules[index]
		if !markerMatchesRequest(rule.Owner, request) {
			continue
		}
		if owned != nil {
			return darwinResolverGuard{}, fmt.Errorf("Darwin resolver observation contains multiple owned rules")
		}
		owned = rule
	}
	if owned == nil {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver observation contains no owned rule")
	}
	guard, err := darwinResolverGuardFromRule(*owned)
	if err != nil {
		return darwinResolverGuard{}, err
	}
	if owned.Namespace != request.Suffix() {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver owned rule is outside %q", fixedDarwinResolverPath)
	}
	return guard, nil
}

// darwinResolverGuardFromRule reconstructs the exact fixed-file CAS guard from bounded facts.
func darwinResolverGuardFromRule(rule RuleFact) (darwinResolverGuard, error) {
	name, device, inode, generation, err := parseDarwinResolverNativeID(rule.NativeID)
	if err != nil {
		return darwinResolverGuard{}, err
	}
	if name != fixedDarwinResolverName {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver native ID names %q, want %q", name, fixedDarwinResolverName)
	}
	if err := validateFingerprintText("Darwin resolver native attribute fingerprint", rule.NativeAttributesSHA256); err != nil {
		return darwinResolverGuard{}, err
	}
	return darwinResolverGuard{
		Exists:                 true,
		Name:                   name,
		Device:                 device,
		Inode:                  inode,
		Generation:             generation,
		NativeAttributesSHA256: rule.NativeAttributesSHA256,
	}, nil
}

// validateDarwinResolverGuard confines native effects to the fixed destination and canonical evidence.
func validateDarwinResolverGuard(guard darwinResolverGuard) error {
	if guard.Name != fixedDarwinResolverName {
		return fmt.Errorf("Darwin resolver guard names %q, want %q", guard.Name, fixedDarwinResolverName)
	}
	if !guard.Exists {
		if guard.Device != 0 || guard.Inode != 0 || guard.Generation != 0 || guard.NativeAttributesSHA256 != "" {
			return fmt.Errorf("absent Darwin resolver guard contains native identity")
		}
		return nil
	}
	if guard.Inode == 0 {
		return fmt.Errorf("Darwin resolver guard has no inode")
	}
	return validateFingerprintText("Darwin resolver guard native attribute fingerprint", guard.NativeAttributesSHA256)
}

// matchDarwinResolverGuard rejects destination creation, removal, replacement, or content drift since observation.
func matchDarwinResolverGuard(guard darwinResolverGuard, entry darwinResolverEntry, exists bool) error {
	if guard.Exists != exists {
		return fmt.Errorf("Darwin resolver destination changed after observation")
	}
	if !exists {
		return nil
	}
	if entry.Name != guard.Name ||
		entry.Metadata.Device != guard.Device ||
		entry.Metadata.Inode != guard.Inode ||
		entry.Metadata.Generation != guard.Generation ||
		darwinResolverEntryFingerprint(entry) != guard.NativeAttributesSHA256 {
		return fmt.Errorf("Darwin resolver destination changed after observation")
	}
	return nil
}

// findDarwinResolverEntry finds one exact direct filename in a complete snapshot.
func findDarwinResolverEntry(entries []darwinResolverEntry, name string) (darwinResolverEntry, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return darwinResolverEntry{}, false
}

// matchDarwinResolverMutationState re-proves the complete relevant observation and exact destination before an effect.
func matchDarwinResolverMutationState(
	ctx context.Context,
	request Request,
	entries []darwinResolverEntry,
	expectedFingerprint string,
	guard darwinResolverGuard,
) error {
	observation, err := darwinResolverObservationFromEntries(ctx, request, entries)
	if err != nil {
		return err
	}
	if fingerprintValidated(observation) != expectedFingerprint {
		return fmt.Errorf("Darwin resolver observation changed before mutation")
	}
	current, exists := findDarwinResolverEntry(entries, fixedDarwinResolverName)
	return matchDarwinResolverGuard(guard, current, exists)
}

// darwinResolverSurroundingsFingerprint binds every relevant rule outside Harbor's fixed destination.
func darwinResolverSurroundingsFingerprint(
	ctx context.Context,
	request Request,
	entries []darwinResolverEntry,
) (string, error) {
	surroundings := make([]darwinResolverEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name != fixedDarwinResolverName {
			surroundings = append(surroundings, entry)
		}
	}
	observation, err := darwinResolverObservationFromEntries(ctx, request, surroundings)
	if err != nil {
		return "", err
	}
	return fingerprintValidated(observation), nil
}

// matchDarwinResolverSurroundings rejects a relevant concurrent change that raced the fixed-file publication.
func matchDarwinResolverSurroundings(
	ctx context.Context,
	request Request,
	entries []darwinResolverEntry,
	expectedFingerprint string,
) error {
	fingerprint, err := darwinResolverSurroundingsFingerprint(ctx, request, entries)
	if err != nil {
		return err
	}
	if fingerprint != expectedFingerprint {
		return fmt.Errorf("Darwin resolver surroundings changed during mutation")
	}
	return nil
}

// darwinResolverOrphanNames selects only Harbor's exact bounded transaction namespace for recovery.
func darwinResolverOrphanNames(names []string) ([]string, error) {
	if len(names) > maximumDarwinResolverEntries {
		return nil, fmt.Errorf("Darwin resolver directory entries exceed limit %d", maximumDarwinResolverEntries)
	}
	seen := make(map[string]struct{}, len(names))
	orphans := make([]string, 0)
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("Darwin resolver directory repeats entry %q", name)
		}
		seen[name] = struct{}{}
		if !isDarwinResolverOrphanName(name) {
			continue
		}
		orphans = append(orphans, name)
		if len(orphans) > maximumDarwinResolverOrphans {
			return nil, fmt.Errorf("Darwin resolver transaction orphans exceed limit %d", maximumDarwinResolverOrphans)
		}
	}
	slices.Sort(orphans)
	return orphans, nil
}

// isDarwinResolverOrphanName recognizes the unpredictable lowercase namespace reserved by Harbor transactions.
func isDarwinResolverOrphanName(name string) bool {
	encoded, found := strings.CutPrefix(name, darwinResolverOrphanPrefix)
	if !found || len(encoded) != darwinResolverOrphanHexBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == darwinResolverOrphanHexBytes && hex.EncodeToString(decoded) == encoded
}

// isDarwinResolverQuarantineName recognizes the root-only directory namespace used to fence destructive mutations.
func isDarwinResolverQuarantineName(name string) bool {
	encoded, found := strings.CutPrefix(name, darwinResolverQuarantinePrefix)
	if !found || len(encoded) != darwinResolverOrphanHexBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(encoded)
	return err == nil && len(decoded) == darwinResolverOrphanHexBytes && hex.EncodeToString(decoded) == encoded
}

// darwinResolverQuarantineNames selects only Harbor's bounded root-only mutation namespace for recovery.
func darwinResolverQuarantineNames(names []string) ([]string, error) {
	if len(names) > maximumDarwinResolverEntries {
		return nil, fmt.Errorf("Darwin resolver directory entries exceed limit %d", maximumDarwinResolverEntries)
	}
	seen := make(map[string]struct{}, len(names))
	quarantines := make([]string, 0)
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("Darwin resolver directory repeats entry %q", name)
		}
		seen[name] = struct{}{}
		if !isDarwinResolverQuarantineName(name) {
			continue
		}
		quarantines = append(quarantines, name)
		if len(quarantines) > maximumDarwinResolverOrphans {
			return nil, fmt.Errorf("Darwin resolver transaction quarantines exceed limit %d", maximumDarwinResolverOrphans)
		}
	}
	slices.Sort(quarantines)
	return quarantines, nil
}

// isDarwinResolverTransactionName admits only names created by staging or identity-fenced quarantine operations.
func isDarwinResolverTransactionName(name string) bool {
	return isDarwinResolverOrphanName(name) || isDarwinResolverQuarantineName(name)
}

// darwinResolverOrphanGuard authorizes cleanup only for one safe root-owned object under Harbor's exact namespace.
func darwinResolverOrphanGuard(name string, entry darwinResolverEntry) (darwinResolverGuard, error) {
	if !isDarwinResolverOrphanName(name) {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver transaction orphan name %q is invalid", name)
	}
	if entry.Name != fixedDarwinResolverName {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver transaction orphan has an invalid logical destination")
	}
	if err := validateDarwinResolverEntry(entry); err != nil {
		return darwinResolverGuard{}, err
	}
	parsed, parseErr := parseDarwinResolver(fixedDarwinResolverName, entry.Content)
	completeOwnership := parseErr == nil && parsed.Owner != nil
	if !completeOwnership && !isDarwinResolverPartialStaging(entry) {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver transaction orphan has no complete Harbor ownership evidence")
	}
	if !completeOwnership && (entry.Metadata.GID != 0 || entry.Metadata.Flags != 0) {
		return darwinResolverGuard{}, fmt.Errorf("Darwin resolver partial staging orphan has noncanonical native ownership")
	}
	guard := darwinResolverGuard{
		Exists:                 true,
		Name:                   fixedDarwinResolverName,
		Device:                 entry.Metadata.Device,
		Inode:                  entry.Metadata.Inode,
		Generation:             entry.Metadata.Generation,
		NativeAttributesSHA256: darwinResolverEntryFingerprint(entry),
	}
	if err := validateDarwinResolverGuard(guard); err != nil {
		return darwinResolverGuard{}, err
	}
	return guard, nil
}

// isDarwinResolverPartialStaging recognizes bytes a crash may leave while the created file still has private staging mode.
func isDarwinResolverPartialStaging(entry darwinResolverEntry) bool {
	prefix := []byte(darwinResolverOwnerPrefix)
	return entry.Metadata.Mode == 0o600 &&
		(len(entry.Content) == 0 || bytes.HasPrefix(prefix, entry.Content) || bytes.HasPrefix(entry.Content, prefix))
}

// darwinResolverRuleFact parses one secure entry and retains only exact or more-specific .test claims.
func darwinResolverRuleFact(entry darwinResolverEntry, request Request) (RuleFact, bool, error) {
	if err := validateDarwinResolverEntry(entry); err != nil {
		return RuleFact{}, false, err
	}
	parsed, err := parseDarwinResolver(entry.Name, entry.Content)
	if err != nil {
		return RuleFact{}, false, err
	}
	fixedDestination := entry.Name == fixedDarwinResolverName
	if !fixedDestination && !namespaceClaimsSuffix(parsed.Namespace, request.Suffix()) {
		return RuleFact{}, false, nil
	}

	owner := parsed.Owner
	namespace := parsed.Namespace
	if fixedDestination && !namespaceClaimsSuffix(namespace, request.Suffix()) {
		// Occupancy of Harbor's only destination must conflict even when a domain override routes elsewhere.
		namespace = request.Suffix()
	}
	if !fixedDestination || parsed.Namespace != request.Suffix() {
		owner = nil
	}
	desired := marshalDarwinResolverValidated(request)
	fact := RuleFact{
		Mechanism:              networkpolicy.DarwinResolverFile,
		NativeID:               darwinResolverNativeID(entry),
		Namespace:              namespace,
		Servers:                slices.Clone(parsed.Servers),
		RouteOnly:              true,
		NativeExact:            fixedDestination && parsed.Namespace == request.Suffix() && bytes.Equal(entry.Content, desired) && darwinResolverMetadataExact(entry.Metadata),
		NativeAttributesSHA256: darwinResolverEntryFingerprint(entry),
		Owner:                  owner,
	}
	return fact, true, nil
}

// validateDarwinResolverEntry rejects filesystem shapes that could redirect or alias native ownership.
func validateDarwinResolverEntry(entry darwinResolverEntry) error {
	if _, err := darwinResolverNamespace(entry.Name); err != nil {
		return err
	}
	if !entry.Metadata.Regular {
		return fmt.Errorf("Darwin resolver entry is not a regular file")
	}
	if entry.Metadata.Inode == 0 {
		return fmt.Errorf("Darwin resolver entry has no stable inode")
	}
	if entry.Metadata.LinkCount != 1 {
		return fmt.Errorf("Darwin resolver entry link count is %d, want 1", entry.Metadata.LinkCount)
	}
	if entry.Metadata.UID != 0 {
		return fmt.Errorf("Darwin resolver entry owner UID is %d, want 0", entry.Metadata.UID)
	}
	if entry.Metadata.Mode&^0o7777 != 0 || entry.Metadata.Mode&0o7022 != 0 || entry.Metadata.Mode&0o400 == 0 {
		return fmt.Errorf("Darwin resolver entry mode %04o is unsafe", entry.Metadata.Mode)
	}
	if len(entry.Content) > maximumDarwinResolverFileBytes {
		return fmt.Errorf("Darwin resolver entry exceeds %d bytes", maximumDarwinResolverFileBytes)
	}
	return nil
}

// darwinResolverMetadataExact identifies the owner and mode emitted for Harbor's fixed file.
func darwinResolverMetadataExact(metadata darwinResolverMetadata) bool {
	return metadata.Regular &&
		metadata.UID == 0 &&
		metadata.GID == 0 &&
		metadata.Mode == darwinResolverFileMode &&
		metadata.Flags == 0 &&
		metadata.LinkCount == 1
}

// darwinResolverNativeID binds a direct filename to the native object observed behind it.
func darwinResolverNativeID(entry darwinResolverEntry) string {
	return fmt.Sprintf(
		"%s@%016x:%016x:%08x",
		entry.Name,
		entry.Metadata.Device,
		entry.Metadata.Inode,
		entry.Metadata.Generation,
	)
}

// parseDarwinResolverNativeID validates the canonical fixed-width native identity encoding.
func parseDarwinResolverNativeID(value string) (string, uint64, uint64, uint32, error) {
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 {
		return "", 0, 0, 0, fmt.Errorf("Darwin resolver native ID %q is malformed", value)
	}
	name := value[:separator]
	identity := strings.Split(value[separator+1:], ":")
	if len(identity) != 3 || len(identity[0]) != 16 || len(identity[1]) != 16 || len(identity[2]) != 8 {
		return "", 0, 0, 0, fmt.Errorf("Darwin resolver native ID %q is malformed", value)
	}
	device, deviceErr := strconv.ParseUint(identity[0], 16, 64)
	inode, inodeErr := strconv.ParseUint(identity[1], 16, 64)
	generation, generationErr := strconv.ParseUint(identity[2], 16, 32)
	if deviceErr != nil ||
		inodeErr != nil ||
		generationErr != nil ||
		inode == 0 ||
		fmt.Sprintf("%s@%016x:%016x:%08x", name, device, inode, generation) != value {
		return "", 0, 0, 0, fmt.Errorf("Darwin resolver native ID %q is malformed", value)
	}
	return name, device, inode, uint32(generation), nil
}

// darwinResolverEntryFingerprint hashes raw bytes and every security-relevant native attribute.
func darwinResolverEntryFingerprint(entry darwinResolverEntry) string {
	payload := appendString(nil, "goforj.harbor.darwin-resolver-entry.v1")
	payload = appendString(payload, entry.Name)
	if entry.Metadata.Regular {
		payload = append(payload, 1)
	} else {
		payload = append(payload, 0)
	}
	payload = binary.AppendUvarint(payload, entry.Metadata.Device)
	payload = binary.AppendUvarint(payload, entry.Metadata.Inode)
	payload = binary.AppendUvarint(payload, uint64(entry.Metadata.Generation))
	payload = binary.AppendUvarint(payload, uint64(entry.Metadata.UID))
	payload = binary.AppendUvarint(payload, uint64(entry.Metadata.GID))
	payload = binary.AppendUvarint(payload, uint64(entry.Metadata.Mode))
	payload = binary.AppendUvarint(payload, uint64(entry.Metadata.Flags))
	payload = binary.AppendUvarint(payload, entry.Metadata.LinkCount)
	payload = appendBytes(payload, entry.Content)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// marshalDarwinResolver emits the one canonical resolver(5) representation Harbor may own.
func marshalDarwinResolver(request Request) ([]byte, error) {
	if err := validateDarwinResolverRequest(request); err != nil {
		return nil, err
	}
	return marshalDarwinResolverValidated(request), nil
}

// marshalDarwinResolverValidated emits canonical bytes after the fixed request contract is proven.
func marshalDarwinResolverValidated(request Request) []byte {
	marker := request.OwnerMarker()
	domain := strings.TrimPrefix(request.Suffix(), ".")
	content := fmt.Sprintf(
		"%sversion=%d installation=%s policy=%s\ndomain %s\nnameserver %s\nport %d\n",
		darwinResolverOwnerPrefix,
		marker.Version,
		marker.InstallationID,
		marker.PolicyFingerprint,
		domain,
		request.Endpoint().Addr(),
		request.Endpoint().Port(),
	)
	return []byte(content)
}

// parseDarwinResolver decodes one bounded resolver(5) file without losing namespace claims.
func parseDarwinResolver(name string, content []byte) (parsedDarwinResolver, error) {
	fallbackNamespace, err := darwinResolverNamespace(name)
	if err != nil {
		return parsedDarwinResolver{}, err
	}
	if len(content) > maximumDarwinResolverFileBytes {
		return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file exceeds %d bytes", maximumDarwinResolverFileBytes)
	}
	if !utf8.Valid(content) {
		return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file is not valid UTF-8")
	}
	for _, character := range string(content) {
		if character == '\n' || character == '\t' {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file contains a control character")
		}
		if character > 0x7e {
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file contains non-ASCII text")
		}
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > maximumDarwinResolverLines+1 || len(lines) == maximumDarwinResolverLines+1 && lines[len(lines)-1] != "" {
		return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file exceeds %d lines", maximumDarwinResolverLines)
	}

	parsed := parsedDarwinResolver{Namespace: fallbackNamespace}
	port := uint16(53)
	addresses := make([]netip.Addr, 0, maximumServersPerRule)
	seenSingleton := make(map[string]struct{})
	for index, line := range lines {
		if len(line) > maximumDarwinResolverLineBytes {
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver line %d exceeds %d bytes", index+1, maximumDarwinResolverLineBytes)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.HasPrefix(trimmed, darwinResolverOwnerPrefix) {
				if parsed.Owner != nil {
					return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file repeats the Harbor owner marker")
				}
				marker, err := parseDarwinResolverOwnerMarker(trimmed)
				if err != nil {
					return parsedDarwinResolver{}, err
				}
				parsed.Owner = &marker
			} else if strings.HasPrefix(trimmed, "# harbor-resolver-owner") {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver file contains a malformed Harbor owner marker")
			}
			continue
		}
		line = trimDarwinResolverInlineComment(line)
		fields := strings.Fields(line)
		if len(fields)-1 > maximumDarwinResolverDirectiveArgs || fields[0] != strings.ToLower(fields[0]) {
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver line %d is not canonical", index+1)
		}
		switch fields[0] {
		case "domain":
			if err := requireDarwinResolverSingleton(fields, seenSingleton); err != nil {
				return parsedDarwinResolver{}, err
			}
			namespace, err := darwinResolverNamespace(fields[1])
			if err != nil {
				return parsedDarwinResolver{}, err
			}
			parsed.Namespace = namespace
		case "nameserver":
			if len(fields) != 2 {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver nameserver requires one address")
			}
			if len(addresses) >= maximumServersPerRule {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver nameservers exceed limit %d", maximumServersPerRule)
			}
			address, err := netip.ParseAddr(fields[1])
			if err != nil || address.String() != fields[1] || address != address.Unmap() {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver nameserver %q is not canonical", fields[1])
			}
			addresses = append(addresses, address)
		case "port":
			if err := requireDarwinResolverSingleton(fields, seenSingleton); err != nil {
				return parsedDarwinResolver{}, err
			}
			value, err := strconv.ParseUint(fields[1], 10, 16)
			if err != nil || value == 0 || strconv.FormatUint(value, 10) != fields[1] {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver port %q is not canonical", fields[1])
			}
			port = uint16(value)
		case "search":
			if len(fields) < 2 {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver search requires at least one domain")
			}
			for _, domain := range fields[1:] {
				if _, err := darwinResolverNamespace(domain); err != nil {
					return parsedDarwinResolver{}, err
				}
			}
		case "search_order", "timeout":
			if err := requireDarwinResolverUnsigned(fields); err != nil {
				return parsedDarwinResolver{}, err
			}
		case "options", "sortlist", "lookup":
			if len(fields) < 2 {
				return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver %s requires an argument", fields[0])
			}
		default:
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver directive %q is unsupported", fields[0])
		}
	}

	seenServers := make(map[netip.AddrPort]struct{}, len(addresses))
	for _, address := range addresses {
		server := netip.AddrPortFrom(address, port)
		if err := validateServer(server); err != nil {
			return parsedDarwinResolver{}, err
		}
		if _, duplicate := seenServers[server]; duplicate {
			return parsedDarwinResolver{}, fmt.Errorf("Darwin resolver repeats nameserver %s", server)
		}
		seenServers[server] = struct{}{}
		parsed.Servers = append(parsed.Servers, server)
	}
	return parsed, nil
}

// parseDarwinResolverOwnerMarker validates the exact bounded Harbor ownership comment grammar.
func parseDarwinResolverOwnerMarker(line string) (OwnerMarker, error) {
	fields := strings.Fields(strings.TrimPrefix(line, darwinResolverOwnerPrefix))
	if len(fields) != 3 {
		return OwnerMarker{}, fmt.Errorf("Darwin resolver owner marker requires version, installation, and policy")
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if !found || value == "" {
			return OwnerMarker{}, fmt.Errorf("Darwin resolver owner marker field %q is malformed", field)
		}
		if _, duplicate := values[key]; duplicate {
			return OwnerMarker{}, fmt.Errorf("Darwin resolver owner marker repeats %q", key)
		}
		values[key] = value
	}
	version, err := strconv.ParseUint(values["version"], 10, 16)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != values["version"] || values["installation"] == "" || values["policy"] == "" {
		return OwnerMarker{}, fmt.Errorf("Darwin resolver owner marker is malformed")
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

// darwinResolverNamespace maps a canonical resolver domain to the package's dotted suffix form.
func darwinResolverNamespace(domain string) (string, error) {
	if domain == "" || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", fmt.Errorf("Darwin resolver domain %q is not canonical", domain)
	}
	namespace := "." + domain
	if err := validateNamespace(namespace); err != nil {
		return "", err
	}
	return namespace, nil
}

// requireDarwinResolverSingleton validates a one-argument directive that may appear only once.
func requireDarwinResolverSingleton(fields []string, seen map[string]struct{}) error {
	if len(fields) != 2 {
		return fmt.Errorf("Darwin resolver %s requires one argument", fields[0])
	}
	if _, duplicate := seen[fields[0]]; duplicate {
		return fmt.Errorf("Darwin resolver repeats %s", fields[0])
	}
	seen[fields[0]] = struct{}{}
	return nil
}

// requireDarwinResolverUnsigned validates one canonical nonnegative numeric directive.
func requireDarwinResolverUnsigned(fields []string) error {
	if len(fields) != 2 {
		return fmt.Errorf("Darwin resolver %s requires one integer", fields[0])
	}
	value, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil || strconv.FormatUint(value, 10) != fields[1] {
		return fmt.Errorf("Darwin resolver %s value %q is not canonical", fields[0], fields[1])
	}
	return nil
}

// trimDarwinResolverInlineComment removes only comments introduced at a token boundary.
func trimDarwinResolverInlineComment(line string) string {
	for index, character := range line {
		if character != '#' && character != ';' {
			continue
		}
		if index == 0 || line[index-1] == ' ' || line[index-1] == '\t' {
			return line[:index]
		}
	}
	return line
}
