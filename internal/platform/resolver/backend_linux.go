//go:build linux

package resolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	fixedSystemdBusctlPath                 = "/usr/bin/busctl"
	fixedSystemctlPath                     = "/usr/bin/systemctl"
	fixedSystemdResolvedService            = "systemd-resolved.service"
	fixedSystemdExecutableFDPath           = "/proc/self/fd/3"
	systemdResolve1Service                 = "org.freedesktop.resolve1"
	systemdResolve1ManagerPath             = "/org/freedesktop/resolve1"
	systemdResolve1ManagerInterface        = "org.freedesktop.resolve1.Manager"
	systemdResolvedStagePrefix             = ".goforj-harbor-resolver-stage-"
	systemdResolvedQuarantinePrefix        = ".goforj-harbor-resolver-quarantine-"
	systemdResolvedTransactionHexBytes     = 16
	maximumSystemdResolvedTransactions     = 16
	maximumSystemdResolvedDirectoryEntries = 4096
	maximumSystemdCommandOutputBytes       = 1 << 20
	systemdResolvedCommandTimeout          = 10 * time.Second
	systemdResolvedStableReadAttempts      = 3
	systemdResolvedMutationNameRetries     = 128
	systemdResolvedLockPoll                = 25 * time.Millisecond
)

// systemdResolvedNativeStore owns fixed-path Linux filesystem and resolve1 effects.
type systemdResolvedNativeStore struct{}

// systemdResolvedBusctlEnvelope is busctl's lossless JSON wrapper for one D-Bus property.
type systemdResolvedBusctlEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// systemdResolvedBusctlDomain is one raw resolve1 Domains tuple.
type systemdResolvedBusctlDomain struct {
	InterfaceIndex int32
	Domain         string
	RouteOnly      bool
}

// systemdResolvedBusctlServer is one raw resolve1 DNSEx tuple.
type systemdResolvedBusctlServer struct {
	InterfaceIndex int32
	Family         int32
	Address        []byte
	Port           uint16
	ServerName     string
}

// systemdResolvedCommandResult retains bounded stdout and stderr separately for safe diagnostics.
type systemdResolvedCommandResult struct {
	Stdout []byte
	Stderr []byte
}

// boundedSystemdCommandBuffer rejects command output that crosses the fixed parser budget.
type boundedSystemdCommandBuffer struct {
	bytes []byte
}

var _ systemdResolvedStore = systemdResolvedNativeStore{}

// New creates a resolver adapter backed by Ubuntu 24.04's systemd-resolved manager and fixed Harbor drop-in.
func New() *Adapter {
	return newAdapter(newSystemdResolvedBackend(systemdResolvedNativeStore{}))
}

// recover repairs only Harbor-owned crash remnants before a public resolver observation.
func (systemdResolvedNativeStore) recover(ctx context.Context, request Request) error {
	return recoverSystemdResolvedTransactions(ctx, request)
}

// snapshot obtains one stable fixed artifact around a stable pair of resolve1 property reads.
func (systemdResolvedNativeStore) snapshot(
	ctx context.Context,
	request Request,
) (systemdResolvedSnapshot, error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return systemdResolvedSnapshot{}, err
	}
	ctx = normalizedContext(ctx)
	for attempt := 0; attempt < systemdResolvedStableReadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return systemdResolvedSnapshot{}, err
		}
		before, err := readFixedSystemdResolvedArtifact()
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		if err := requireNoSystemdResolvedTransactions(); err != nil {
			return systemdResolvedSnapshot{}, err
		}
		runtimeRules, err := observeStableSystemdResolvedRuntime(ctx, request)
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		after, err := readFixedSystemdResolvedArtifact()
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		if equalSystemdResolvedArtifacts(before, after) {
			return systemdResolvedSnapshot{Artifact: after, Runtime: runtimeRules}, nil
		}
	}
	return systemdResolvedSnapshot{}, fmt.Errorf("systemd-resolved fixed artifact did not remain stable during observation")
}

// replace publishes canonical bytes through renameat2 and rolls back the exact publication if service restart fails.
func (store systemdResolvedNativeStore) replace(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard systemdResolvedGuard,
	content []byte,
) (err error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return err
	}
	if err := validateFingerprintText("systemd-resolved expected observation fingerprint", expectedFingerprint); err != nil {
		return err
	}
	if err := validateSystemdResolvedGuard(guard); err != nil {
		return err
	}
	if !bytes.Equal(content, marshalSystemdResolvedValidated(request)) {
		return fmt.Errorf("systemd-resolved replacement bytes are not canonical for the request")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := openSystemdResolvedDirectory(true)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := lockSystemdResolvedDirectory(ctx, directory); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlockSystemdResolvedDirectory(directory))
	}()
	restartRequired, err := recoverSystemdResolvedTransactionsAt(ctx, request, directory)
	if err != nil {
		return err
	}
	if restartRequired {
		if err := restartSystemdResolved(ctx); err != nil {
			return fmt.Errorf("restart systemd-resolved after transaction recovery: %w", err)
		}
	}

	snapshot, err := store.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if err := matchSystemdResolvedMutationState(request, snapshot, expectedFingerprint, guard); err != nil {
		return err
	}
	stageName, staged, err := createSystemdResolvedStaging(directory, content)
	if err != nil {
		return err
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			err = errors.Join(err, removeSystemdResolvedTransaction(directory, stageName))
		}
	}()
	lateSnapshot, err := snapshotSystemdResolvedMutationAt(ctx, request, directory, stageName)
	if err != nil {
		return err
	}
	if err := matchSystemdResolvedMutationState(request, lateSnapshot, expectedFingerprint, guard); err != nil {
		return err
	}
	snapshot = lateSnapshot

	flags := uint(0)
	if guard.Exists {
		flags = unix.RENAME_EXCHANGE
	} else {
		flags = unix.RENAME_NOREPLACE
	}
	if err := unix.Renameat2(int(directory.Fd()), stageName, int(directory.Fd()), fixedSystemdResolvedName, flags); err != nil {
		return fmt.Errorf("publish systemd-resolved drop-in: %w", err)
	}
	// Once public state changes, automatic cleanup could destroy the displaced artifact needed for exact rollback.
	cleanupStage = false
	rollback := func(cause error) error {
		return errors.Join(cause, rollbackSystemdResolvedReplacement(directory, request, snapshot, guard, stageName, staged))
	}
	if err := directory.Sync(); err != nil {
		return rollback(fmt.Errorf("sync systemd-resolved drop-in publication: %w", err))
	}
	if guard.Exists {
		captured, err := readSystemdResolvedArtifactAt(directory, stageName)
		if err != nil {
			return rollback(fmt.Errorf("inspect displaced systemd-resolved artifact: %w", err))
		}
		if err := matchSystemdResolvedCapturedArtifact(request, snapshot, guard, captured); err != nil {
			return rollback(fmt.Errorf("verify displaced systemd-resolved artifact: %w", err))
		}
	}
	published, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
	if err != nil {
		return rollback(fmt.Errorf("inspect published systemd-resolved artifact: %w", err))
	}
	if !sameSystemdResolvedArtifactIdentity(published, staged) || !bytes.Equal(published.Content, content) {
		return rollback(fmt.Errorf("published systemd-resolved artifact differs from staged identity"))
	}

	if restartErr := restartSystemdResolved(ctx); restartErr != nil {
		return rollback(fmt.Errorf("restart systemd-resolved after publication: %w", restartErr))
	}
	verificationContext, cancelVerification := context.WithTimeout(context.Background(), systemdResolvedCommandTimeout)
	defer cancelVerification()
	allowedTransaction := ""
	if guard.Exists {
		allowedTransaction = stageName
	}
	verified, verifyErr := snapshotSystemdResolvedMutationAt(
		verificationContext,
		request,
		directory,
		allowedTransaction,
	)
	if verifyErr == nil {
		var verifiedObservation Observation
		verifiedObservation, verifyErr = systemdResolvedObservationFromSnapshot(request, verified)
		if verifyErr == nil && classifyValidated(verifiedObservation).State != StateExact {
			verifyErr = fmt.Errorf("systemd-resolved publication did not produce one exact owned route")
		}
	}
	if verifyErr != nil {
		return rollback(fmt.Errorf("verify systemd-resolved publication: %w", verifyErr))
	}
	if guard.Exists {
		retired, err := readSystemdResolvedArtifactAt(directory, stageName)
		if err != nil {
			return fmt.Errorf("inspect displaced systemd-resolved artifact before cleanup: %w", err)
		}
		if !sameSystemdResolvedCapturedArtifact(snapshot.Artifact, retired) {
			return fmt.Errorf("displaced systemd-resolved artifact changed before cleanup; preserving %q", stageName)
		}
		if err := removeSystemdResolvedTransaction(directory, stageName); err != nil {
			return fmt.Errorf("remove displaced systemd-resolved artifact: %w", err)
		}
	}
	return nil
}

// remove quarantines and verifies the exact owned artifact before restarting systemd-resolved without it.
func (store systemdResolvedNativeStore) remove(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard systemdResolvedGuard,
) (err error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return err
	}
	if err := validateFingerprintText("systemd-resolved expected observation fingerprint", expectedFingerprint); err != nil {
		return err
	}
	if err := validateSystemdResolvedGuard(guard); err != nil {
		return err
	}
	if !guard.Exists {
		return fmt.Errorf("systemd-resolved release requires an existing artifact guard")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := openSystemdResolvedDirectory(false)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := lockSystemdResolvedDirectory(ctx, directory); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlockSystemdResolvedDirectory(directory))
	}()
	restartRequired, err := recoverSystemdResolvedTransactionsAt(ctx, request, directory)
	if err != nil {
		return err
	}
	if restartRequired {
		if err := restartSystemdResolved(ctx); err != nil {
			return fmt.Errorf("restart systemd-resolved after transaction recovery: %w", err)
		}
	}
	snapshot, err := store.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if err := matchSystemdResolvedMutationState(request, snapshot, expectedFingerprint, guard); err != nil {
		return err
	}
	quarantineName, err := uniqueSystemdResolvedTransactionName(directory, systemdResolvedQuarantinePrefix)
	if err != nil {
		return err
	}
	if err := unix.Renameat2(
		int(directory.Fd()),
		fixedSystemdResolvedName,
		int(directory.Fd()),
		quarantineName,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return fmt.Errorf("quarantine systemd-resolved drop-in: %w", err)
	}
	quarantined := true
	defer func() {
		if quarantined {
			err = errors.Join(err, fmt.Errorf("systemd-resolved quarantine retained as %q", quarantineName))
		}
	}()
	if err := directory.Sync(); err != nil {
		restoreErr := restoreSystemdResolvedQuarantine(directory, quarantineName)
		if restoreErr == nil {
			quarantined = false
		}
		return errors.Join(fmt.Errorf("sync systemd-resolved quarantine: %w", err), restoreErr)
	}
	captured, err := readSystemdResolvedArtifactAt(directory, quarantineName)
	if err != nil {
		return fmt.Errorf("inspect quarantined systemd-resolved artifact: %w", err)
	}
	if err := matchSystemdResolvedCapturedArtifact(request, snapshot, guard, captured); err != nil {
		restoreErr := restoreSystemdResolvedQuarantine(directory, quarantineName)
		if restoreErr == nil {
			quarantined = false
		}
		return errors.Join(fmt.Errorf("verify quarantined systemd-resolved artifact: %w", err), restoreErr)
	}
	if restartErr := restartSystemdResolved(ctx); restartErr != nil {
		restoreErr := restoreSystemdResolvedQuarantine(directory, quarantineName)
		if restoreErr == nil {
			quarantined = false
		}
		reloadErr := restartSystemdResolved(context.Background())
		return errors.Join(
			fmt.Errorf("restart systemd-resolved after removal: %w", restartErr),
			restoreErr,
			wrapSystemdResolvedNativeError("restart systemd-resolved after rollback", reloadErr),
		)
	}
	verificationContext, cancelVerification := context.WithTimeout(context.Background(), systemdResolvedCommandTimeout)
	defer cancelVerification()
	verified, verifyErr := snapshotSystemdResolvedMutationAt(
		verificationContext,
		request,
		directory,
		quarantineName,
	)
	if verifyErr == nil {
		verifyErr = verifySystemdResolvedRelease(snapshot, verified, request)
	}
	if verifyErr != nil {
		restoreErr := restoreSystemdResolvedQuarantine(directory, quarantineName)
		if restoreErr == nil {
			quarantined = false
		}
		reloadErr := restartSystemdResolved(context.Background())
		return errors.Join(
			fmt.Errorf("verify systemd-resolved removal: %w", verifyErr),
			restoreErr,
			wrapSystemdResolvedNativeError("restart systemd-resolved after verification rollback", reloadErr),
		)
	}
	retired, err := readSystemdResolvedArtifactAt(directory, quarantineName)
	if err != nil {
		return fmt.Errorf("inspect quarantined systemd-resolved artifact before cleanup: %w", err)
	}
	if !sameSystemdResolvedCapturedArtifact(snapshot.Artifact, retired) {
		return fmt.Errorf("quarantined systemd-resolved artifact changed before cleanup; preserving %q", quarantineName)
	}
	if err := removeSystemdResolvedTransaction(directory, quarantineName); err != nil {
		return fmt.Errorf("remove quarantined systemd-resolved artifact: %w", err)
	}
	quarantined = false
	return nil
}

// snapshotSystemdResolvedMutationAt revalidates fixed and live state while admitting at most one retained private artifact.
func snapshotSystemdResolvedMutationAt(
	ctx context.Context,
	request Request,
	directory *os.File,
	allowedTransaction string,
) (systemdResolvedSnapshot, error) {
	for attempt := 0; attempt < systemdResolvedStableReadAttempts; attempt++ {
		if err := requireSystemdResolvedTransactionsAt(directory, allowedTransaction); err != nil {
			return systemdResolvedSnapshot{}, err
		}
		before, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		runtimeRules, err := observeStableSystemdResolvedRuntime(ctx, request)
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		after, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
		if err != nil {
			return systemdResolvedSnapshot{}, err
		}
		if err := requireSystemdResolvedTransactionsAt(directory, allowedTransaction); err != nil {
			return systemdResolvedSnapshot{}, err
		}
		if equalSystemdResolvedArtifacts(before, after) {
			return systemdResolvedSnapshot{Artifact: after, Runtime: runtimeRules}, nil
		}
	}
	return systemdResolvedSnapshot{}, fmt.Errorf("systemd-resolved fixed artifact did not remain stable during mutation")
}

// verifySystemdResolvedRelease requires only the one runtime route explained by the captured artifact to disappear.
func verifySystemdResolvedRelease(
	before systemdResolvedSnapshot,
	after systemdResolvedSnapshot,
	request Request,
) error {
	if after.Artifact.Exists {
		return fmt.Errorf("systemd-resolved fixed artifact remains after release")
	}
	parsed, err := parseSystemdResolvedArtifact(before.Artifact.Content)
	if err != nil {
		return fmt.Errorf("parse released systemd-resolved artifact: %w", err)
	}
	expected := make([]systemdResolvedRuntimeRule, 0, len(before.Runtime))
	removed := 0
	for _, rule := range before.Runtime {
		if systemdResolvedRuntimeExplainedByArtifact(rule, parsed, request.Suffix()) {
			removed++
			continue
		}
		expected = append(expected, rule)
	}
	if removed > 1 {
		return fmt.Errorf("released systemd-resolved artifact explained %d runtime routes", removed)
	}
	if !reflect.DeepEqual(expected, after.Runtime) {
		return fmt.Errorf("systemd-resolved runtime changed outside the released artifact")
	}
	return nil
}

// systemdResolvedRuntimeExplainedByArtifact matches only one global route encoded by the fixed artifact.
func systemdResolvedRuntimeExplainedByArtifact(
	runtimeRule systemdResolvedRuntimeRule,
	artifact parsedSystemdResolvedArtifact,
	suffix string,
) bool {
	if runtimeRule.InterfaceIndex != 0 {
		return false
	}
	domains := relevantSystemdResolvedArtifactDomains(artifact.Domains, suffix)
	if len(domains) != 1 || runtimeRule.Namespace != domains[0].Namespace || runtimeRule.RouteOnly != domains[0].RouteOnly {
		return false
	}
	return slices.Equal(runtimeRule.Servers, systemdResolvedArtifactRuntimeServers(artifact.Servers, 0))
}

// readFixedSystemdResolvedArtifact observes the fixed path through no-follow descriptor access.
func readFixedSystemdResolvedArtifact() (systemdResolvedArtifact, error) {
	directory, err := openSystemdResolvedDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return systemdResolvedArtifact{}, nil
	}
	if err != nil {
		return systemdResolvedArtifact{}, err
	}
	defer directory.Close()
	return readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
}

// readSystemdResolvedArtifactAt reads one direct drop-in name through a retained directory descriptor.
func readSystemdResolvedArtifactAt(directory *os.File, name string) (systemdResolvedArtifact, error) {
	if name != fixedSystemdResolvedName && !isSystemdResolvedTransactionName(name) {
		return systemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact name %q is outside the fixed namespace", name)
	}
	descriptor, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return systemdResolvedArtifact{}, nil
	}
	if err != nil {
		return systemdResolvedArtifact{}, fmt.Errorf("open systemd-resolved artifact %q: %w", name, err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	return readSystemdResolvedArtifactFile(file)
}

// readSystemdResolvedArtifactFile performs one bounded read and proves the open identity stayed unchanged.
func readSystemdResolvedArtifactFile(file *os.File) (artifact systemdResolvedArtifact, err error) {
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	before, err := statSystemdResolvedArtifact(file)
	if err != nil {
		return systemdResolvedArtifact{}, err
	}
	if !before.Regular || before.Size < 0 || before.Size > maximumSystemdResolvedFileBytes {
		return systemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact is not a bounded regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumSystemdResolvedFileBytes+1))
	if err != nil {
		return systemdResolvedArtifact{}, fmt.Errorf("read systemd-resolved artifact: %w", err)
	}
	if len(content) > maximumSystemdResolvedFileBytes {
		return systemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact exceeds %d bytes", maximumSystemdResolvedFileBytes)
	}
	after, err := statSystemdResolvedArtifact(file)
	if err != nil {
		return systemdResolvedArtifact{}, err
	}
	if before != after || after.Size != int64(len(content)) {
		return systemdResolvedArtifact{}, fmt.Errorf("systemd-resolved artifact changed while reading")
	}
	return systemdResolvedArtifact{Exists: true, Content: content, Metadata: after}, nil
}

// statSystemdResolvedArtifact extracts native identity and rejects unbounded extended write authority.
func statSystemdResolvedArtifact(file *os.File) (systemdResolvedArtifactMetadata, error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return systemdResolvedArtifactMetadata{}, fmt.Errorf("inspect systemd-resolved artifact: %w", err)
	}
	unsafeExtendedAccess, err := systemdResolvedUnsafeExtendedAccess(int(file.Fd()))
	if err != nil {
		return systemdResolvedArtifactMetadata{}, err
	}
	return systemdResolvedArtifactMetadata{
		Regular:              status.Mode&unix.S_IFMT == unix.S_IFREG,
		Device:               uint64(status.Dev),
		Inode:                status.Ino,
		UID:                  status.Uid,
		GID:                  status.Gid,
		Mode:                 status.Mode & 0o7777,
		LinkCount:            uint64(status.Nlink),
		Size:                 status.Size,
		ModifiedTimeNS:       int64(status.Mtim.Sec)*int64(time.Second) + int64(status.Mtim.Nsec),
		ChangedTimeNS:        int64(status.Ctim.Sec)*int64(time.Second) + int64(status.Ctim.Nsec),
		UnsafeExtendedAccess: unsafeExtendedAccess,
	}, nil
}

// systemdResolvedUnsafeExtendedAccess rejects ACLs, capabilities, and mutable user/trusted metadata.
func systemdResolvedUnsafeExtendedAccess(descriptor int) (bool, error) {
	size, err := unix.Flistxattr(descriptor, nil)
	if errors.Is(err, unix.ENOTSUP) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("list systemd-resolved object extended attributes: %w", err)
	}
	if size == 0 {
		return false, nil
	}
	if size > 4096 {
		return false, fmt.Errorf("systemd-resolved object extended attributes exceed limit")
	}
	buffer := make([]byte, size)
	written, err := unix.Flistxattr(descriptor, buffer)
	if err != nil {
		return false, fmt.Errorf("read systemd-resolved object extended attributes: %w", err)
	}
	for _, rawName := range bytes.Split(buffer[:written], []byte{0}) {
		name := string(rawName)
		if name == "" || name == "security.selinux" {
			continue
		}
		if name == "system.posix_acl_access" || name == "system.posix_acl_default" ||
			name == "security.capability" || strings.HasPrefix(name, "user.") || strings.HasPrefix(name, "trusted.") {
			return true, nil
		}
		return false, fmt.Errorf("systemd-resolved object has unsupported extended attribute %q", name)
	}
	return false, nil
}

// equalSystemdResolvedArtifacts compares stable identity, bytes, and security metadata.
func equalSystemdResolvedArtifacts(left systemdResolvedArtifact, right systemdResolvedArtifact) bool {
	return left.Exists == right.Exists && left.Metadata == right.Metadata && bytes.Equal(left.Content, right.Content)
}

// sameSystemdResolvedArtifactIdentity compares the native identity that survives a rename.
func sameSystemdResolvedArtifactIdentity(left systemdResolvedArtifact, right systemdResolvedArtifact) bool {
	return left.Exists && right.Exists &&
		left.Metadata.Device == right.Metadata.Device &&
		left.Metadata.Inode == right.Metadata.Inode
}

// sameSystemdResolvedCapturedArtifact permits rename ctime updates while requiring every mutable authority field to match.
func sameSystemdResolvedCapturedArtifact(expected systemdResolvedArtifact, captured systemdResolvedArtifact) bool {
	return sameSystemdResolvedArtifactIdentity(expected, captured) &&
		bytes.Equal(expected.Content, captured.Content) &&
		expected.Metadata.Regular == captured.Metadata.Regular &&
		expected.Metadata.UID == captured.Metadata.UID &&
		expected.Metadata.GID == captured.Metadata.GID &&
		expected.Metadata.Mode == captured.Metadata.Mode &&
		expected.Metadata.LinkCount == captured.Metadata.LinkCount &&
		expected.Metadata.Size == captured.Metadata.Size &&
		expected.Metadata.ModifiedTimeNS == captured.Metadata.ModifiedTimeNS &&
		expected.Metadata.UnsafeExtendedAccess == captured.Metadata.UnsafeExtendedAccess
}

// observeStableSystemdResolvedRuntime requires two equal complete resolve1 reads before claiming observation completeness.
func observeStableSystemdResolvedRuntime(ctx context.Context, request Request) ([]systemdResolvedRuntimeRule, error) {
	var previous []systemdResolvedRuntimeRule
	for attempt := 0; attempt < systemdResolvedStableReadAttempts; attempt++ {
		result, err := runFixedSystemdCommand(
			ctx,
			fixedSystemdBusctlPath,
			"--system",
			"--json=short",
			"--no-pager",
			"--timeout=5s",
			"get-property",
			systemdResolve1Service,
			systemdResolve1ManagerPath,
			systemdResolve1ManagerInterface,
			"Domains",
			"DNSEx",
		)
		if err != nil {
			return nil, fmt.Errorf("read systemd-resolved resolve1 properties: %w", err)
		}
		rules, err := parseSystemdResolvedBusctlProperties(result.Stdout, request)
		if err != nil {
			return nil, err
		}
		if previous != nil && reflect.DeepEqual(previous, rules) {
			return rules, nil
		}
		previous = rules
	}
	return nil, fmt.Errorf("systemd-resolved resolve1 properties did not remain stable")
}

// parseSystemdResolvedBusctlProperties parses exact Domains and DNSEx JSON objects in requested property order.
func parseSystemdResolvedBusctlProperties(output []byte, request Request) ([]systemdResolvedRuntimeRule, error) {
	if len(output) == 0 || len(output) > maximumSystemdCommandOutputBytes {
		return nil, fmt.Errorf("systemd-resolved busctl output has invalid size %d", len(output))
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var domainsEnvelope systemdResolvedBusctlEnvelope
	if err := decoder.Decode(&domainsEnvelope); err != nil {
		return nil, fmt.Errorf("decode systemd-resolved Domains property: %w", err)
	}
	var serversEnvelope systemdResolvedBusctlEnvelope
	if err := decoder.Decode(&serversEnvelope); err != nil {
		return nil, fmt.Errorf("decode systemd-resolved DNSEx property: %w", err)
	}
	if err := requireSystemdResolvedJSONEOF(decoder); err != nil {
		return nil, err
	}
	if domainsEnvelope.Type != "a(isb)" || serversEnvelope.Type != "a(iiayqs)" {
		return nil, fmt.Errorf(
			"systemd-resolved properties have signatures %q and %q, want a(isb) and a(iiayqs)",
			domainsEnvelope.Type,
			serversEnvelope.Type,
		)
	}
	domains, err := parseSystemdResolvedBusctlDomains(domainsEnvelope.Data)
	if err != nil {
		return nil, err
	}
	servers, err := parseSystemdResolvedBusctlServers(serversEnvelope.Data)
	if err != nil {
		return nil, err
	}
	return joinSystemdResolvedRuntimeRules(domains, servers, request)
}

// requireSystemdResolvedJSONEOF rejects appended property objects or non-whitespace data.
func requireSystemdResolvedJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("systemd-resolved busctl output contains extra JSON data")
		}
		return fmt.Errorf("decode trailing systemd-resolved busctl output: %w", err)
	}
	return nil
}

// parseSystemdResolvedBusctlDomains decodes bounded a(isb) rows without numeric coercion.
func parseSystemdResolvedBusctlDomains(data json.RawMessage) ([]systemdResolvedBusctlDomain, error) {
	rows, err := decodeSystemdResolvedRows(data, "Domains")
	if err != nil {
		return nil, err
	}
	if len(rows) > maximumSystemdResolvedRuntime {
		return nil, fmt.Errorf("systemd-resolved Domains rows exceed limit %d", maximumSystemdResolvedRuntime)
	}
	result := make([]systemdResolvedBusctlDomain, len(rows))
	for index, row := range rows {
		if len(row) != 3 {
			return nil, fmt.Errorf("systemd-resolved Domains row %d has %d fields", index, len(row))
		}
		if err := json.Unmarshal(row[0], &result[index].InterfaceIndex); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved Domains row %d interface: %w", index, err)
		}
		if err := json.Unmarshal(row[1], &result[index].Domain); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved Domains row %d domain: %w", index, err)
		}
		if err := json.Unmarshal(row[2], &result[index].RouteOnly); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved Domains row %d route flag: %w", index, err)
		}
	}
	return result, nil
}

// parseSystemdResolvedBusctlServers decodes bounded a(iiayqs) rows and exact byte-array address families.
func parseSystemdResolvedBusctlServers(data json.RawMessage) ([]systemdResolvedBusctlServer, error) {
	rows, err := decodeSystemdResolvedRows(data, "DNSEx")
	if err != nil {
		return nil, err
	}
	if len(rows) > maximumSystemdResolvedRuntime*maximumServersPerRule {
		return nil, fmt.Errorf("systemd-resolved DNSEx rows exceed limit")
	}
	result := make([]systemdResolvedBusctlServer, len(rows))
	for index, row := range rows {
		if len(row) != 5 {
			return nil, fmt.Errorf("systemd-resolved DNSEx row %d has %d fields", index, len(row))
		}
		if err := json.Unmarshal(row[0], &result[index].InterfaceIndex); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved DNSEx row %d interface: %w", index, err)
		}
		if err := json.Unmarshal(row[1], &result[index].Family); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved DNSEx row %d family: %w", index, err)
		}
		if err := json.Unmarshal(row[2], &result[index].Address); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved DNSEx row %d address: %w", index, err)
		}
		if err := json.Unmarshal(row[3], &result[index].Port); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved DNSEx row %d port: %w", index, err)
		}
		if err := json.Unmarshal(row[4], &result[index].ServerName); err != nil {
			return nil, fmt.Errorf("decode systemd-resolved DNSEx row %d server name: %w", index, err)
		}
	}
	return result, nil
}

// decodeSystemdResolvedRows admits only one JSON array of tuple arrays.
func decodeSystemdResolvedRows(data json.RawMessage, property string) ([][]json.RawMessage, error) {
	var rows [][]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode systemd-resolved %s rows: %w", property, err)
	}
	if err := requireSystemdResolvedJSONEOF(decoder); err != nil {
		return nil, err
	}
	return rows, nil
}

// joinSystemdResolvedRuntimeRules associates each relevant route domain with DNSEx servers from its exact scope.
func joinSystemdResolvedRuntimeRules(
	domains []systemdResolvedBusctlDomain,
	servers []systemdResolvedBusctlServer,
	request Request,
) ([]systemdResolvedRuntimeRule, error) {
	serversByInterface := make(map[int32][]systemdResolvedRuntimeServer)
	for index, server := range servers {
		if server.InterfaceIndex < 0 {
			return nil, fmt.Errorf("systemd-resolved DNSEx row %d has a negative interface", index)
		}
		endpoint, err := systemdResolvedBusctlEndpoint(server)
		if err != nil {
			return nil, fmt.Errorf("systemd-resolved DNSEx row %d: %w", index, err)
		}
		if err := validateSystemdResolvedServerName(server.ServerName); err != nil {
			return nil, fmt.Errorf("systemd-resolved DNSEx row %d: %w", index, err)
		}
		serversByInterface[server.InterfaceIndex] = append(
			serversByInterface[server.InterfaceIndex],
			systemdResolvedRuntimeServer{
				InterfaceIndex: server.InterfaceIndex,
				Endpoint:       endpoint,
				ServerName:     server.ServerName,
			},
		)
	}
	for interfaceIndex := range serversByInterface {
		slices.SortFunc(serversByInterface[interfaceIndex], compareSystemdResolvedRuntimeServer)
		for index := 1; index < len(serversByInterface[interfaceIndex]); index++ {
			if compareSystemdResolvedRuntimeServer(
				serversByInterface[interfaceIndex][index-1],
				serversByInterface[interfaceIndex][index],
			) == 0 {
				return nil, fmt.Errorf("systemd-resolved DNSEx repeats a server")
			}
		}
	}

	rules := make([]systemdResolvedRuntimeRule, 0, len(domains))
	globalRelevantRoute := false
	for index, domain := range domains {
		if domain.InterfaceIndex < 0 {
			return nil, fmt.Errorf("systemd-resolved Domains row %d has a negative interface", index)
		}
		namespace, relevant, err := systemdResolvedBusctlNamespace(domain.Domain, request.Suffix())
		if err != nil {
			return nil, fmt.Errorf("systemd-resolved Domains row %d: %w", index, err)
		}
		if !relevant {
			continue
		}
		if domain.InterfaceIndex == 0 {
			globalRelevantRoute = true
		}
		interfaceServers := slices.Clone(serversByInterface[domain.InterfaceIndex])
		if len(interfaceServers) > maximumServersPerRule {
			return nil, fmt.Errorf("systemd-resolved route %q has more than %d DNS servers", namespace, maximumServersPerRule)
		}
		rules = append(rules, systemdResolvedRuntimeRule{
			InterfaceIndex: domain.InterfaceIndex,
			Namespace:      namespace,
			RouteOnly:      domain.RouteOnly,
			Servers:        interfaceServers,
		})
	}
	if !globalRelevantRoute && len(serversByInterface[0]) != 0 {
		// A new global Domains= route would make every preexisting global DNS server serve Harbor's suffix.
		rules = append(rules, systemdResolvedRuntimeRule{
			Namespace: request.Suffix(),
			Servers:   slices.Clone(serversByInterface[0]),
		})
	}
	slices.SortFunc(rules, compareSystemdResolvedRuntimeRule)
	for index := 1; index < len(rules); index++ {
		if rules[index-1].InterfaceIndex == rules[index].InterfaceIndex &&
			rules[index-1].Namespace == rules[index].Namespace {
			return nil, fmt.Errorf("systemd-resolved resolve1 properties repeat a route")
		}
	}
	return rules, nil
}

// systemdResolvedBusctlEndpoint converts one AF_INET or AF_INET6 byte vector into a canonical nonzero socket.
func systemdResolvedBusctlEndpoint(server systemdResolvedBusctlServer) (netip.AddrPort, error) {
	if server.Port == 0 {
		return netip.AddrPort{}, fmt.Errorf("DNSEx server port is zero")
	}
	var address netip.Addr
	switch server.Family {
	case unix.AF_INET:
		if len(server.Address) != 4 {
			return netip.AddrPort{}, fmt.Errorf("AF_INET DNSEx address has %d bytes", len(server.Address))
		}
		var raw [4]byte
		copy(raw[:], server.Address)
		address = netip.AddrFrom4(raw)
	case unix.AF_INET6:
		if len(server.Address) != 16 {
			return netip.AddrPort{}, fmt.Errorf("AF_INET6 DNSEx address has %d bytes", len(server.Address))
		}
		var raw [16]byte
		copy(raw[:], server.Address)
		address = netip.AddrFrom16(raw)
	default:
		return netip.AddrPort{}, fmt.Errorf("DNSEx address family %d is unsupported", server.Family)
	}
	address = address.Unmap()
	endpoint := netip.AddrPortFrom(address, server.Port)
	if err := validateServer(endpoint); err != nil {
		return netip.AddrPort{}, err
	}
	return endpoint, nil
}

// systemdResolvedBusctlNamespace normalizes one resolve1 domain while retaining only suffix-relevant routes.
func systemdResolvedBusctlNamespace(domain string, suffix string) (string, bool, error) {
	if domain == "." {
		return "", false, nil
	}
	if domain == "" || strings.HasPrefix(domain, "~") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false, fmt.Errorf("resolve1 domain %q is not canonical", domain)
	}
	namespace := "." + domain
	if err := validateNamespace(namespace); err != nil {
		return "", false, err
	}
	return namespace, namespaceClaimsSuffix(namespace, suffix), nil
}

// runFixedSystemdCommand executes one root-owned systemd tool without a shell, inherited arguments, or unbounded output.
func runFixedSystemdCommand(ctx context.Context, path string, arguments ...string) (systemdResolvedCommandResult, error) {
	if path != fixedSystemdBusctlPath && path != fixedSystemctlPath {
		return systemdResolvedCommandResult{}, fmt.Errorf("systemd command path %q is not allowlisted", path)
	}
	if err := validateFixedSystemdArguments(path, arguments); err != nil {
		return systemdResolvedCommandResult{}, err
	}
	executable, err := openFixedSystemdCommand(path)
	if err != nil {
		return systemdResolvedCommandResult{}, err
	}
	ctx = normalizedContext(ctx)
	commandContext, cancel := context.WithTimeout(ctx, systemdResolvedCommandTimeout)
	defer cancel()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = executable.Close()
		return systemdResolvedCommandResult{}, fmt.Errorf("open %s stdout pipe: %w", filepath.Base(path), err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = executable.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return systemdResolvedCommandResult{}, fmt.Errorf("open %s stderr pipe: %w", filepath.Base(path), err)
	}
	process, err := os.StartProcess(fixedSystemdExecutableFDPath, append([]string{filepath.Base(path)}, arguments...), &os.ProcAttr{
		Dir: "/",
		Env: []string{
			"LANG=C",
			"LC_ALL=C",
			"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
			"SYSTEMD_PAGER=",
		},
		Files: []*os.File{nil, stdoutWriter, stderrWriter, executable},
	})
	closeWriterErr := errors.Join(stdoutWriter.Close(), stderrWriter.Close(), executable.Close())
	if err != nil {
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
		return systemdResolvedCommandResult{}, errors.Join(fmt.Errorf("start %s: %w", filepath.Base(path), err), closeWriterErr)
	}
	stdout := new(boundedSystemdCommandBuffer)
	stderr := new(boundedSystemdCommandBuffer)
	stdoutResult := readSystemdResolvedCommandPipe(stdoutReader, stdout)
	stderrResult := readSystemdResolvedCommandPipe(stderrReader, stderr)
	waitResult := make(chan systemdResolvedProcessResult, 1)
	go func() {
		state, waitErr := process.Wait()
		waitResult <- systemdResolvedProcessResult{State: state, Err: waitErr}
	}()
	var processResult systemdResolvedProcessResult
	select {
	case processResult = <-waitResult:
	case <-commandContext.Done():
		killErr := process.Kill()
		processResult = <-waitResult
		processResult.Err = errors.Join(commandContext.Err(), killErr, processResult.Err)
	}
	stdoutErr := <-stdoutResult
	stderrErr := <-stderrResult
	result := systemdResolvedCommandResult{
		Stdout: slices.Clone(stdout.bytes),
		Stderr: slices.Clone(stderr.bytes),
	}
	err = errors.Join(closeWriterErr, stdoutErr, stderrErr, processResult.Err)
	if err == nil && (processResult.State == nil || !processResult.State.Success()) {
		if processResult.State == nil {
			err = fmt.Errorf("%s exited without process state", filepath.Base(path))
		} else {
			err = fmt.Errorf("%s exited with %s", filepath.Base(path), processResult.State.String())
		}
	}
	if err != nil {
		message := strings.TrimSpace(string(result.Stderr))
		if len(message) > 512 {
			message = message[:512]
		}
		if message == "" {
			return result, fmt.Errorf("execute %s: %w", filepath.Base(path), err)
		}
		return result, fmt.Errorf("execute %s: %w: %s", filepath.Base(path), err, message)
	}
	return result, nil
}

// validateFixedSystemdArguments confines process authority to one observation and one deterministic reload.
func validateFixedSystemdArguments(path string, arguments []string) error {
	busctlArguments := []string{
		"--system",
		"--json=short",
		"--no-pager",
		"--timeout=5s",
		"get-property",
		systemdResolve1Service,
		systemdResolve1ManagerPath,
		systemdResolve1ManagerInterface,
		"Domains",
		"DNSEx",
	}
	systemctlArguments := []string{"--no-ask-password", "restart", fixedSystemdResolvedService}
	if path == fixedSystemdBusctlPath && slices.Equal(arguments, busctlArguments) ||
		path == fixedSystemctlPath && slices.Equal(arguments, systemctlArguments) {
		return nil
	}
	return fmt.Errorf("systemd command arguments are outside Harbor's fixed resolver operations")
}

// systemdResolvedProcessResult retains the narrow process completion evidence needed by the fixed runner.
type systemdResolvedProcessResult struct {
	State *os.ProcessState
	Err   error
}

// readSystemdResolvedCommandPipe drains one child stream concurrently so the other stream cannot deadlock it.
func readSystemdResolvedCommandPipe(
	reader *os.File,
	buffer *boundedSystemdCommandBuffer,
) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(buffer, reader)
		result <- errors.Join(copyErr, reader.Close())
	}()
	return result
}

// Write stores command output only while it remains inside the fixed parser budget.
func (buffer *boundedSystemdCommandBuffer) Write(value []byte) (int, error) {
	if len(buffer.bytes)+len(value) > maximumSystemdCommandOutputBytes {
		return 0, fmt.Errorf("systemd command output exceeds %d bytes", maximumSystemdCommandOutputBytes)
	}
	buffer.bytes = append(buffer.bytes, value...)
	return len(value), nil
}

// openFixedSystemdCommand retains the validated inode so pathname replacement cannot redirect execution.
func openFixedSystemdCommand(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open fixed systemd command %q: %w", path, err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		return nil, errors.Join(fmt.Errorf("inspect fixed systemd command %q: %w", path, err), file.Close())
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != 0 || status.Gid != 0 || status.Mode&0o022 != 0 {
		return nil, errors.Join(
			fmt.Errorf("fixed systemd command %q has unsafe ownership, type, or mode", path),
			file.Close(),
		)
	}
	unsafeExtendedAccess, err := systemdResolvedUnsafeExtendedAccess(descriptor)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect fixed systemd command %q: %w", path, err), file.Close())
	}
	if unsafeExtendedAccess {
		return nil, errors.Join(fmt.Errorf("fixed systemd command %q delegates extended access", path), file.Close())
	}
	return file, nil
}

// restartSystemdResolved asks PID 1 to apply the fixed drop-in without shell interpretation.
func restartSystemdResolved(ctx context.Context) error {
	_, err := runFixedSystemdCommand(
		ctx,
		fixedSystemctlPath,
		"--no-ask-password",
		"restart",
		fixedSystemdResolvedService,
	)
	return err
}

// openSystemdResolvedDirectory walks retained no-follow descriptors so pathname races cannot redirect authority.
func openSystemdResolvedDirectory(create bool) (*os.File, error) {
	rootDescriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open systemd-resolved root ancestor: %w", err)
	}
	root := os.NewFile(uintptr(rootDescriptor), "/")
	defer root.Close()
	if err := validateOpenedSystemdResolvedDirectory(root, "/"); err != nil {
		return nil, err
	}
	etc, err := openSystemdResolvedChildDirectory(root, "etc")
	if err != nil {
		return nil, err
	}
	defer etc.Close()
	systemd, err := openSystemdResolvedChildDirectory(etc, "systemd")
	if err != nil {
		return nil, err
	}
	defer systemd.Close()
	if create {
		if err := unix.Mkdirat(int(systemd.Fd()), "resolved.conf.d", 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create systemd-resolved drop-in directory: %w", err)
		}
	}
	directory, err := openSystemdResolvedChildDirectory(systemd, "resolved.conf.d")
	if err != nil {
		return nil, err
	}
	return directory, nil
}

// openSystemdResolvedChildDirectory opens one allowlisted child beneath a retained trusted parent.
func openSystemdResolvedChildDirectory(parent *os.File, name string) (*os.File, error) {
	switch name {
	case "etc", "systemd", "resolved.conf.d":
	default:
		return nil, fmt.Errorf("systemd-resolved directory component %q is not allowlisted", name)
	}
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open systemd-resolved directory component %q: %w", name, err)
	}
	path := filepath.Join(parent.Name(), name)
	directory := os.NewFile(uintptr(descriptor), path)
	if err := validateOpenedSystemdResolvedDirectory(directory, path); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

// validateOpenedSystemdResolvedDirectory requires one root-owned descriptor without ACL or write delegation.
func validateOpenedSystemdResolvedDirectory(directory *os.File, path string) error {
	var status unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &status); err != nil {
		return fmt.Errorf("inspect opened systemd-resolved directory %q: %w", path, err)
	}
	mode := status.Mode & 0o7777
	if status.Mode&unix.S_IFMT != unix.S_IFDIR || status.Dev == 0 || status.Ino == 0 ||
		status.Uid != 0 || status.Gid != 0 || mode&0o022 != 0 || mode&0o7000 != 0 {
		return fmt.Errorf("opened systemd-resolved directory %q has unsafe ownership, type, or mode", path)
	}
	unsafeExtendedAccess, err := systemdResolvedUnsafeExtendedAccess(int(directory.Fd()))
	if err != nil {
		return fmt.Errorf("inspect opened systemd-resolved directory %q: %w", path, err)
	}
	if unsafeExtendedAccess {
		return fmt.Errorf("opened systemd-resolved directory %q delegates extended access", path)
	}
	return nil
}

// unlockSystemdResolvedDirectory releases the helper serialization lock without hiding an earlier failure.
func unlockSystemdResolvedDirectory(directory *os.File) error {
	if err := unix.Flock(int(directory.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("unlock systemd-resolved drop-in directory: %w", err)
	}
	return nil
}

// lockSystemdResolvedDirectory acquires the mutation lock without allowing a stale holder to defeat cancellation.
func lockSystemdResolvedDirectory(ctx context.Context, directory *os.File) error {
	if directory == nil {
		return fmt.Errorf("lock systemd-resolved drop-in directory: nil directory")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lock systemd-resolved drop-in directory: %w", err)
		}
		err := unix.Flock(int(directory.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock systemd-resolved drop-in directory: %w", err)
		}
		timer := time.NewTimer(systemdResolvedLockPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("lock systemd-resolved drop-in directory: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// createSystemdResolvedStaging writes and syncs canonical bytes before any public-name mutation.
func createSystemdResolvedStaging(
	directory *os.File,
	content []byte,
) (string, systemdResolvedArtifact, error) {
	name, err := uniqueSystemdResolvedTransactionName(directory, systemdResolvedStagePrefix)
	if err != nil {
		return "", systemdResolvedArtifact{}, err
	}
	descriptor, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return "", systemdResolvedArtifact{}, fmt.Errorf("create systemd-resolved staging file: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		}
	}()
	if err := unix.Fchown(descriptor, 0, 0); err != nil {
		return "", systemdResolvedArtifact{}, fmt.Errorf("assign systemd-resolved staging ownership: %w", err)
	}
	if err := writeAllSystemdResolved(file, content); err != nil {
		return "", systemdResolvedArtifact{}, err
	}
	if err := unix.Fchmod(descriptor, systemdResolvedFileMode); err != nil {
		return "", systemdResolvedArtifact{}, fmt.Errorf("set systemd-resolved staging mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", systemdResolvedArtifact{}, fmt.Errorf("sync systemd-resolved staging file: %w", err)
	}
	if err := directory.Sync(); err != nil {
		return "", systemdResolvedArtifact{}, fmt.Errorf("sync systemd-resolved staging directory: %w", err)
	}
	artifact, err := readSystemdResolvedArtifactAt(directory, name)
	if err != nil {
		return "", systemdResolvedArtifact{}, err
	}
	cleanup = false
	return name, artifact, nil
}

// writeAllSystemdResolved rejects short writes while preserving the fixed content bound.
func writeAllSystemdResolved(file *os.File, content []byte) error {
	for len(content) > 0 {
		written, err := file.Write(content)
		if err != nil {
			return fmt.Errorf("write systemd-resolved staging file: %w", err)
		}
		if written == 0 {
			return fmt.Errorf("write systemd-resolved staging file made no progress")
		}
		content = content[written:]
	}
	return nil
}

// uniqueSystemdResolvedTransactionName reserves one unpredictable direct name without caller influence.
func uniqueSystemdResolvedTransactionName(directory *os.File, prefix string) (string, error) {
	if prefix != systemdResolvedStagePrefix && prefix != systemdResolvedQuarantinePrefix {
		return "", fmt.Errorf("systemd-resolved transaction prefix is not allowlisted")
	}
	for attempt := 0; attempt < systemdResolvedMutationNameRetries; attempt++ {
		random := make([]byte, systemdResolvedTransactionHexBytes)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", fmt.Errorf("generate systemd-resolved transaction name: %w", err)
		}
		name := prefix + fmt.Sprintf("%x", random)
		var status unix.Stat_t
		err := unix.Fstatat(int(directory.Fd()), name, &status, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return name, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect systemd-resolved transaction name: %w", err)
		}
	}
	return "", fmt.Errorf("allocate systemd-resolved transaction name after %d attempts", systemdResolvedMutationNameRetries)
}

// requireNoSystemdResolvedTransactions rejects crash remnants before observing or mutating public state.
func requireNoSystemdResolvedTransactions() error {
	directory, err := openSystemdResolvedDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open systemd-resolved drop-in directory for transaction scan: %w", err)
	}
	defer directory.Close()
	return requireNoSystemdResolvedTransactionsAt(directory)
}

// recoverSystemdResolvedTransactions reopens the fixed directory and repairs only Harbor-owned crash remnants.
func recoverSystemdResolvedTransactions(ctx context.Context, request Request) (err error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return err
	}
	directory, err := openSystemdResolvedDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open systemd-resolved drop-in directory for transaction recovery: %w", err)
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := lockSystemdResolvedDirectory(ctx, directory); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlockSystemdResolvedDirectory(directory)) }()
	restartRequired, err := recoverSystemdResolvedTransactionsAt(ctx, request, directory)
	if err != nil {
		return err
	}
	if restartRequired {
		if err := restartSystemdResolved(ctx); err != nil {
			return fmt.Errorf("restart systemd-resolved after transaction recovery: %w", err)
		}
	}
	return nil
}

// recoverSystemdResolvedTransactionsAt removes unpublished stages and restores only exact owned quarantines.
func recoverSystemdResolvedTransactionsAt(ctx context.Context, request Request, directory *os.File) (bool, error) {
	if err := validateSystemdResolvedRequest(request); err != nil {
		return false, err
	}
	if directory == nil {
		return false, fmt.Errorf("systemd-resolved transaction recovery requires a directory")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("rewind systemd-resolved directory for recovery: %w", err)
	}
	names, err := directory.Readdirnames(maximumSystemdResolvedDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("scan systemd-resolved transactions for recovery: %w", err)
	}
	if len(names) > maximumSystemdResolvedDirectoryEntries {
		return false, fmt.Errorf("systemd-resolved transactions exceed limit %d", maximumSystemdResolvedDirectoryEntries)
	}
	transactions := make([]string, 0, len(names))
	for _, name := range names {
		if isSystemdResolvedTransactionName(name) {
			transactions = append(transactions, name)
		}
	}
	if len(transactions) == 0 {
		return false, nil
	}
	slices.Sort(transactions)
	fixed, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
	if err != nil {
		return false, fmt.Errorf("inspect fixed systemd-resolved artifact for recovery: %w", err)
	}
	fixedOwned := systemdResolvedArtifactOwnedByRequest(fixed, request)
	if fixed.Exists && !fixedOwned {
		return false, fmt.Errorf("systemd-resolved transaction recovery found a foreign fixed artifact")
	}
	restartRequired := false
	for _, name := range transactions {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		artifact, err := readSystemdResolvedArtifactAt(directory, name)
		if err != nil {
			return false, fmt.Errorf("inspect systemd-resolved transaction %q: %w", name, err)
		}
		if !artifact.Exists || !secureSystemdResolvedArtifact(artifact.Metadata) {
			return false, fmt.Errorf("systemd-resolved transaction %q has unsafe artifact identity", name)
		}
		switch {
		case strings.HasPrefix(name, systemdResolvedStagePrefix):
			// A stage is never the public name; deleting it cannot change resolver behavior.
			if err := removeSystemdResolvedTransaction(directory, name); err != nil {
				return false, fmt.Errorf("recover systemd-resolved stage %q: %w", name, err)
			}
		case strings.HasPrefix(name, systemdResolvedQuarantinePrefix):
			if !systemdResolvedArtifactOwnedByRequest(artifact, request) {
				return false, fmt.Errorf("systemd-resolved quarantine %q is not owned by this request", name)
			}
			if fixed.Exists {
				if err := removeSystemdResolvedTransaction(directory, name); err != nil {
					return false, fmt.Errorf("recover completed systemd-resolved quarantine %q: %w", name, err)
				}
				continue
			}
			if err := restoreSystemdResolvedQuarantine(directory, name); err != nil {
				return false, fmt.Errorf("restore systemd-resolved quarantine %q: %w", name, err)
			}
			restartRequired = true
		default:
			return false, fmt.Errorf("systemd-resolved transaction %q is outside recovery policy", name)
		}
	}
	return restartRequired, nil
}

// systemdResolvedArtifactOwnedByRequest requires the exact marker and immutable root-owned file shape.
func systemdResolvedArtifactOwnedByRequest(artifact systemdResolvedArtifact, request Request) bool {
	if !artifact.Exists || !secureSystemdResolvedArtifact(artifact.Metadata) {
		return false
	}
	parsed, err := parseSystemdResolvedArtifact(artifact.Content)
	return err == nil && parsed.Owner != nil && *parsed.Owner == request.OwnerMarker()
}

// requireNoSystemdResolvedTransactionsAt scans a bounded direct directory namespace for incomplete mutations.
func requireNoSystemdResolvedTransactionsAt(directory *os.File) error {
	return requireSystemdResolvedTransactionsAt(directory, "")
}

// requireSystemdResolvedTransactionsAt admits exactly one caller-retained private artifact or no transactions.
func requireSystemdResolvedTransactionsAt(directory *os.File, allowedTransaction string) error {
	if allowedTransaction != "" && !isSystemdResolvedTransactionName(allowedTransaction) {
		return fmt.Errorf("allowed systemd-resolved transaction name is invalid")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind systemd-resolved drop-in directory: %w", err)
	}
	names, err := directory.Readdirnames(maximumSystemdResolvedDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("scan systemd-resolved transactions: %w", err)
	}
	if len(names) > maximumSystemdResolvedDirectoryEntries {
		return fmt.Errorf("systemd-resolved drop-in directory exceeds %d entries", maximumSystemdResolvedDirectoryEntries)
	}
	transactions := 0
	allowedFound := false
	for _, name := range names {
		if !isSystemdResolvedTransactionName(name) {
			continue
		}
		transactions++
		if transactions > maximumSystemdResolvedTransactions {
			return fmt.Errorf("systemd-resolved transactions exceed limit %d", maximumSystemdResolvedTransactions)
		}
		if name == allowedTransaction {
			allowedFound = true
			continue
		}
		return fmt.Errorf("systemd-resolved has an unexpected incomplete Harbor transaction artifact %q", name)
	}
	if allowedTransaction != "" && !allowedFound {
		return fmt.Errorf("retained systemd-resolved transaction %q is absent", allowedTransaction)
	}
	return nil
}

// isSystemdResolvedTransactionName recognizes only Harbor's fixed unpredictable staging namespaces.
func isSystemdResolvedTransactionName(name string) bool {
	for _, prefix := range []string{systemdResolvedStagePrefix, systemdResolvedQuarantinePrefix} {
		encoded, found := strings.CutPrefix(name, prefix)
		if !found || len(encoded) != systemdResolvedTransactionHexBytes*2 {
			continue
		}
		for _, character := range encoded {
			if character < '0' || character > '9' && character < 'a' || character > 'f' {
				return false
			}
		}
		return true
	}
	return false
}

// removeSystemdResolvedTransaction unlinks only one validated Harbor-private direct transaction name.
func removeSystemdResolvedTransaction(directory *os.File, name string) error {
	if !isSystemdResolvedTransactionName(name) {
		return fmt.Errorf("systemd-resolved transaction name %q is invalid", name)
	}
	if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
		return fmt.Errorf("remove systemd-resolved transaction %q: %w", name, err)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync removed systemd-resolved transaction %q: %w", name, err)
	}
	return nil
}

// matchSystemdResolvedCapturedArtifact proves a renamed file is still the exact artifact admitted before mutation.
func matchSystemdResolvedCapturedArtifact(
	request Request,
	snapshot systemdResolvedSnapshot,
	guard systemdResolvedGuard,
	captured systemdResolvedArtifact,
) error {
	if !captured.Exists || captured.Metadata.Device != guard.Device || captured.Metadata.Inode != guard.Inode {
		return fmt.Errorf("captured systemd-resolved artifact has another identity")
	}
	if !sameSystemdResolvedCapturedArtifact(snapshot.Artifact, captured) {
		return fmt.Errorf("captured systemd-resolved artifact changed during publication")
	}
	parsed, err := parseSystemdResolvedArtifact(captured.Content)
	if err != nil || parsed.Owner == nil || *parsed.Owner != request.OwnerMarker() || !secureSystemdResolvedArtifact(captured.Metadata) {
		return fmt.Errorf("captured systemd-resolved artifact has another owner or security shape")
	}
	return nil
}

// rollbackSystemdResolvedReplacement restores only unchanged published and displaced identities.
func rollbackSystemdResolvedReplacement(
	directory *os.File,
	request Request,
	snapshot systemdResolvedSnapshot,
	guard systemdResolvedGuard,
	stageName string,
	staged systemdResolvedArtifact,
) error {
	published, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
	if err != nil {
		return fmt.Errorf("inspect systemd-resolved rollback destination: %w", err)
	}
	if !sameSystemdResolvedArtifactIdentity(published, staged) {
		return fmt.Errorf("systemd-resolved rollback destination changed; preserving transaction artifacts")
	}
	if guard.Exists {
		displaced, err := readSystemdResolvedArtifactAt(directory, stageName)
		if err != nil {
			return fmt.Errorf("inspect systemd-resolved rollback source: %w", err)
		}
		if err := matchSystemdResolvedCapturedArtifact(request, snapshot, guard, displaced); err != nil {
			return fmt.Errorf("verify systemd-resolved rollback source: %w", err)
		}
		if err := unix.Renameat2(
			int(directory.Fd()),
			stageName,
			int(directory.Fd()),
			fixedSystemdResolvedName,
			unix.RENAME_EXCHANGE,
		); err != nil {
			return fmt.Errorf("restore prior systemd-resolved artifact: %w", err)
		}
		restored, err := readSystemdResolvedArtifactAt(directory, fixedSystemdResolvedName)
		if err != nil {
			return fmt.Errorf("inspect restored systemd-resolved artifact: %w", err)
		}
		retired, err := readSystemdResolvedArtifactAt(directory, stageName)
		if err != nil {
			return fmt.Errorf("inspect retired systemd-resolved publication: %w", err)
		}
		if !sameSystemdResolvedCapturedArtifact(snapshot.Artifact, restored) ||
			!sameSystemdResolvedCapturedArtifact(staged, retired) {
			return fmt.Errorf("systemd-resolved rollback exchanged another artifact; preserving both names")
		}
		if err := removeSystemdResolvedTransaction(directory, stageName); err != nil {
			return err
		}
	} else {
		rollbackName, err := uniqueSystemdResolvedTransactionName(directory, systemdResolvedQuarantinePrefix)
		if err != nil {
			return err
		}
		if err := unix.Renameat2(
			int(directory.Fd()),
			fixedSystemdResolvedName,
			int(directory.Fd()),
			rollbackName,
			unix.RENAME_NOREPLACE,
		); err != nil {
			return fmt.Errorf("withdraw failed systemd-resolved publication: %w", err)
		}
		captured, err := readSystemdResolvedArtifactAt(directory, rollbackName)
		if err != nil {
			return err
		}
		if !sameSystemdResolvedCapturedArtifact(staged, captured) {
			restoreErr := restoreSystemdResolvedQuarantine(directory, rollbackName)
			reloadErr := restartSystemdResolved(context.Background())
			return errors.Join(
				fmt.Errorf("failed systemd-resolved publication changed during rollback"),
				restoreErr,
				wrapSystemdResolvedNativeError("restart systemd-resolved after foreign rollback restoration", reloadErr),
			)
		}
		if err := removeSystemdResolvedTransaction(directory, rollbackName); err != nil {
			return err
		}
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync systemd-resolved rollback: %w", err)
	}
	return wrapSystemdResolvedNativeError("restart systemd-resolved after rollback", restartSystemdResolved(context.Background()))
}

// restoreSystemdResolvedQuarantine restores one retained artifact only when the fixed destination remains absent.
func restoreSystemdResolvedQuarantine(directory *os.File, quarantineName string) error {
	if !isSystemdResolvedTransactionName(quarantineName) {
		return fmt.Errorf("systemd-resolved quarantine name is invalid")
	}
	if err := unix.Renameat2(
		int(directory.Fd()),
		quarantineName,
		int(directory.Fd()),
		fixedSystemdResolvedName,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return fmt.Errorf("restore quarantined systemd-resolved artifact: %w", err)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync restored systemd-resolved artifact: %w", err)
	}
	return nil
}

// wrapSystemdResolvedNativeError adds recovery context without manufacturing errors for successful steps.
func wrapSystemdResolvedNativeError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
