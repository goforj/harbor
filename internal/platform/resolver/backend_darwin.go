//go:build darwin

package resolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/goforj/harbor/internal/platform/darwinacl"
	"golang.org/x/sys/unix"
)

const (
	darwinResolverNativeDirectory = "/private/etc/resolver"
	darwinResolverEtcAlias        = "private/etc"
	darwinResolverDirectoryMode   = uint32(0o755)
	darwinResolverQuarantineMode  = uint32(0o700)
	darwinResolverStagingAttempts = 128
)

// darwinNativeResolverStore owns descriptor-relative access to /private/etc/resolver, the no-symlink target of /etc/resolver.
type darwinNativeResolverStore struct{}

// darwinResolverDirectoryIdentity binds one direct name to its complete native status during enumeration.
type darwinResolverDirectoryIdentity struct {
	Name   string
	Status unix.Stat_t
}

// darwinResolverMutationSyscalls isolates the two name-mutating primitives needed by deterministic race tests.
type darwinResolverMutationSyscalls struct {
	rename func(int, string, int, string, uint32) error
	unlink func(int, string, int) error
}

// darwinResolverPrivateQuarantine retains the root-only directory that makes an admitted identity safe to unlink.
type darwinResolverPrivateQuarantine struct {
	Name      string
	Directory *os.File
	Status    unix.Stat_t
}

// darwinResolverPublication records the only two stable outcomes of one native publication syscall.
type darwinResolverPublication struct {
	Published       bool
	Replaced        bool
	PublishedStatus unix.Stat_t
	DisplacedStatus unix.Stat_t
}

// darwinResolverNativeMutationSyscalls binds production effects while allowing native tests to inject name races.
var darwinResolverNativeMutationSyscalls = darwinResolverMutationSyscalls{
	rename: unix.RenameatxNp,
	unlink: unix.Unlinkat,
}

var _ darwinResolverStore = darwinNativeResolverStore{}

// New creates a resolver adapter backed by macOS's fixed /etc/resolver/test file.
func New() *Adapter {
	return newAdapter(newDarwinResolverBackend(darwinNativeResolverStore{}))
}

// snapshot reads every direct file through a retained, validated resolver directory descriptor.
func (darwinNativeResolverStore) snapshot(ctx context.Context) (entries []darwinResolverEntry, err error) {
	directory, exists, err := openDarwinResolverDirectory(false)
	if err != nil || !exists {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := lockDarwinResolverDirectory(directory, unix.LOCK_EX); err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, unlockDarwinResolverDirectory(directory))
	}()
	if err := recoverDarwinResolverOrphans(ctx, directory); err != nil {
		return nil, err
	}
	return snapshotDarwinResolverDirectory(ctx, directory)
}

// replace stages durable canonical bytes and publishes them only while the destination guard still matches.
func (darwinNativeResolverStore) replace(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard darwinResolverGuard,
	content []byte,
) (err error) {
	if err := validateDarwinResolverRequest(request); err != nil {
		return err
	}
	if err := validateFingerprintText("Darwin resolver expected observation fingerprint", expectedFingerprint); err != nil {
		return err
	}
	if err := validateDarwinResolverGuard(guard); err != nil {
		return err
	}
	if len(content) == 0 || len(content) > maximumDarwinResolverFileBytes {
		return fmt.Errorf("Darwin resolver replacement has invalid size %d", len(content))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, _, err := openDarwinResolverDirectory(true)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := lockDarwinResolverDirectory(directory, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlockDarwinResolverDirectory(directory))
	}()
	if err := recoverDarwinResolverOrphans(ctx, directory); err != nil {
		return err
	}

	entries, err := snapshotDarwinResolverDirectory(ctx, directory)
	if err != nil {
		return err
	}
	if err := matchDarwinResolverMutationState(ctx, request, entries, expectedFingerprint, guard); err != nil {
		return err
	}
	expectedSurroundings, err := darwinResolverSurroundingsFingerprint(ctx, request, entries)
	if err != nil {
		return err
	}

	stagingName, staging, err := createDarwinResolverStagingFile(directory)
	if err != nil {
		return err
	}
	cleanupIdentity, err := darwinResolverFileStatus(staging)
	if err != nil {
		return errors.Join(fmt.Errorf("inspect created Darwin resolver staging file: %w", err), staging.Close())
	}
	stagingOpen := true
	cleanupStaging := true
	defer func() {
		if stagingOpen {
			err = errors.Join(err, staging.Close())
		}
		if cleanupStaging {
			err = errors.Join(err, removeDarwinResolverIdentity(
				directory,
				stagingName,
				stagingName,
				cleanupIdentity,
				true,
				darwinResolverNativeMutationSyscalls,
			))
		}
	}()

	if err := unix.Fchown(int(staging.Fd()), 0, 0); err != nil {
		return fmt.Errorf("assign staged Darwin resolver ownership: %w", err)
	}
	if err := unix.Fchmod(int(staging.Fd()), 0o600); err != nil {
		return fmt.Errorf("secure staged Darwin resolver mode before writing: %w", err)
	}
	if err := secureDarwinResolverCreatedAccess(staging); err != nil {
		return fmt.Errorf("secure staged Darwin resolver extended access: %w", err)
	}
	written, err := io.Copy(staging, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("write staged Darwin resolver file: %w", err)
	}
	if written != int64(len(content)) {
		return fmt.Errorf("write staged Darwin resolver file wrote %d bytes, want %d", written, len(content))
	}
	if err := staging.Sync(); err != nil {
		return fmt.Errorf("sync staged Darwin resolver content: %w", err)
	}
	if err := unix.Fchmod(int(staging.Fd()), darwinResolverFileMode); err != nil {
		return fmt.Errorf("set staged Darwin resolver mode: %w", err)
	}
	if err := staging.Sync(); err != nil {
		return fmt.Errorf("sync staged Darwin resolver metadata: %w", err)
	}
	stagedStatus, err := darwinResolverFileStatus(staging)
	if err != nil {
		return fmt.Errorf("inspect staged Darwin resolver file: %w", err)
	}
	if err := validateDarwinResolverNativeFileStatus(stagedStatus); err != nil {
		return err
	}
	if err := validateDarwinResolverExtendedAccess(staging); err != nil {
		return err
	}
	stagedEntry := darwinResolverEntry{
		Name:     fixedDarwinResolverName,
		Content:  slices.Clone(content),
		Metadata: darwinResolverMetadataFromStatus(stagedStatus),
	}
	if !darwinResolverMetadataExact(stagedEntry.Metadata) {
		return fmt.Errorf("staged Darwin resolver owner/mode is not canonical")
	}
	stagedGuard := darwinResolverGuard{
		Exists:                 true,
		Name:                   fixedDarwinResolverName,
		Device:                 stagedEntry.Metadata.Device,
		Inode:                  stagedEntry.Metadata.Inode,
		Generation:             stagedEntry.Metadata.Generation,
		NativeAttributesSHA256: darwinResolverEntryFingerprint(stagedEntry),
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync Darwin resolver staging name: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err = snapshotDarwinResolverDirectoryIgnoring(ctx, directory, stagingName, stagedStatus)
	if err != nil {
		return err
	}
	if err := matchDarwinResolverMutationState(ctx, request, entries, expectedFingerprint, guard); err != nil {
		return err
	}
	if err := validateDarwinResolverDirectoryBinding(directory); err != nil {
		return err
	}
	publication, err := publishDarwinResolverStaging(
		directory,
		stagingName,
		stagedStatus,
		guard,
		darwinResolverNativeMutationSyscalls,
	)
	if err != nil {
		if publication.Published {
			cleanupStaging = false
		}
		return err
	}
	if publication.Replaced {
		cleanupIdentity = publication.DisplacedStatus
	} else {
		cleanupStaging = false
	}
	verificationContext := context.WithoutCancel(ctx)
	publicationNamesVerified := false
	publishedEntry, publishedExists, publishedErr := readDarwinResolverEntry(directory, fixedDarwinResolverName)
	if publishedErr == nil {
		publishedErr = matchDarwinResolverGuard(stagedGuard, publishedEntry, publishedExists)
	}
	if publishedErr == nil {
		retainedPublished, retainedErr := darwinResolverFileStatus(staging)
		if retainedErr != nil {
			publishedErr = fmt.Errorf("inspect retained staged Darwin resolver file after publication: %w", retainedErr)
		} else if !sameDarwinResolverStatusIdentity(stagedStatus, retainedPublished) {
			publishedErr = fmt.Errorf("retained staged Darwin resolver identity changed during publication")
		}
	}
	if publishedErr == nil && publication.Replaced {
		var displacedEntry darwinResolverEntry
		var displacedExists bool
		displacedEntry, displacedExists, publishedErr = readDarwinResolverEntryAs(
			directory,
			stagingName,
			fixedDarwinResolverName,
		)
		if publishedErr == nil {
			publishedErr = matchDarwinResolverGuard(guard, displacedEntry, displacedExists)
		}
	}
	if publishedErr == nil {
		publicationNamesVerified = true
	}
	var afterEntries []darwinResolverEntry
	if publishedErr == nil && publication.Replaced {
		afterEntries, publishedErr = snapshotDarwinResolverDirectoryIgnoring(
			verificationContext,
			directory,
			stagingName,
			publication.DisplacedStatus,
		)
	} else if publishedErr == nil {
		afterEntries, publishedErr = snapshotDarwinResolverDirectory(verificationContext, directory)
	}
	if publishedErr == nil {
		publishedErr = matchDarwinResolverSurroundings(
			verificationContext,
			request,
			afterEntries,
			expectedSurroundings,
		)
	}
	if publishedErr != nil {
		cleanupStaging = false
		var rollbackErr error
		if publicationNamesVerified {
			rollbackErr = rollbackDarwinResolverPublication(
				directory,
				stagingName,
				stagedStatus,
				publication,
				darwinResolverNativeMutationSyscalls,
			)
		}
		if rollbackErr == nil {
			cleanupIdentity = stagedStatus
			cleanupStaging = true
		}
		return errors.Join(publishedErr, rollbackErr)
	}
	if err := directory.Sync(); err != nil {
		cleanupStaging = false
		return fmt.Errorf("sync Darwin resolver directory: %w", err)
	}

	published, exists, err := statDarwinResolverEntry(directory, fixedDarwinResolverName)
	if err != nil {
		cleanupStaging = false
		return err
	}
	if !exists || !sameDarwinResolverStatusIdentity(stagedStatus, published) {
		cleanupStaging = false
		return fmt.Errorf("published Darwin resolver file changed before durability confirmation")
	}
	if err := validateDarwinResolverNativeFileStatus(published); err != nil {
		cleanupStaging = false
		return err
	}
	if err := validateDarwinResolverDirectoryBinding(directory); err != nil {
		cleanupStaging = false
		return err
	}
	return nil
}

// remove unlinks only a current direct file whose identity and raw attributes still match the admitted guard.
func (darwinNativeResolverStore) remove(
	ctx context.Context,
	request Request,
	expectedFingerprint string,
	guard darwinResolverGuard,
) (err error) {
	if err := validateDarwinResolverRequest(request); err != nil {
		return err
	}
	if err := validateFingerprintText("Darwin resolver expected observation fingerprint", expectedFingerprint); err != nil {
		return err
	}
	if err := validateDarwinResolverGuard(guard); err != nil {
		return err
	}
	if !guard.Exists {
		return fmt.Errorf("Darwin resolver removal requires an existing guard")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, exists, err := openDarwinResolverDirectory(false)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Darwin resolver directory disappeared before removal")
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()
	if err := lockDarwinResolverDirectory(directory, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlockDarwinResolverDirectory(directory))
	}()
	if err := recoverDarwinResolverOrphans(ctx, directory); err != nil {
		return err
	}

	entries, err := snapshotDarwinResolverDirectory(ctx, directory)
	if err != nil {
		return err
	}
	if err := matchDarwinResolverMutationState(ctx, request, entries, expectedFingerprint, guard); err != nil {
		return err
	}
	expectedSurroundings, err := darwinResolverSurroundingsFingerprint(ctx, request, entries)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateDarwinResolverDirectoryBinding(directory); err != nil {
		return err
	}
	removalStatus, removalExists, err := statDarwinResolverEntry(directory, fixedDarwinResolverName)
	if err != nil {
		return err
	}
	if !removalExists || uint64(removalStatus.Dev) != guard.Device || uint64(removalStatus.Ino) != guard.Inode ||
		uint32(removalStatus.Gen) != guard.Generation {
		return fmt.Errorf("Darwin resolver destination changed before atomic removal capture")
	}
	quarantine, captured, err := captureDarwinResolverIdentity(
		directory,
		fixedDarwinResolverName,
		fixedDarwinResolverName,
		removalStatus,
		false,
		darwinResolverNativeMutationSyscalls,
	)
	if err != nil {
		return err
	}
	if !captured {
		return fmt.Errorf("Darwin resolver destination disappeared before atomic removal capture")
	}
	quarantined, quarantinedExists, quarantineErr := readDarwinResolverEntryAs(
		quarantine.Directory,
		fixedDarwinResolverName,
		fixedDarwinResolverName,
	)
	if quarantineErr == nil {
		quarantineErr = matchDarwinResolverGuard(guard, quarantined, quarantinedExists)
	}
	if quarantineErr == nil {
		verificationContext := context.WithoutCancel(ctx)
		afterEntries, observationErr := snapshotDarwinResolverDirectoryIgnoring(
			verificationContext,
			directory,
			quarantine.Name,
			quarantine.Status,
		)
		if observationErr == nil {
			observationErr = matchDarwinResolverSurroundings(
				verificationContext,
				request,
				afterEntries,
				expectedSurroundings,
			)
		}
		quarantineErr = observationErr
	}
	if quarantineErr != nil {
		return errors.Join(quarantineErr, restoreDarwinResolverPrivateCapture(
			directory,
			quarantine,
			fixedDarwinResolverName,
			fixedDarwinResolverName,
			removalStatus,
			darwinResolverNativeMutationSyscalls,
		))
	}
	if err := deleteDarwinResolverPrivateCaptureOrRestore(
		directory,
		quarantine,
		fixedDarwinResolverName,
		removalStatus,
		fixedDarwinResolverName,
		darwinResolverNativeMutationSyscalls,
	); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync Darwin resolver directory: %w", err)
	}
	if _, exists, err := statDarwinResolverEntry(directory, fixedDarwinResolverName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("Darwin resolver file reappeared during removal")
	}
	return validateDarwinResolverDirectoryBinding(directory)
}

// snapshotDarwinResolverDirectory reads a stable, bounded view or refuses to claim completeness.
func snapshotDarwinResolverDirectory(ctx context.Context, directory *os.File) ([]darwinResolverEntry, error) {
	return snapshotDarwinResolverDirectoryWithIgnored(ctx, directory, "", unix.Stat_t{}, false)
}

// snapshotDarwinResolverDirectoryIgnoring excludes only this transaction's unchanged staged object.
func snapshotDarwinResolverDirectoryIgnoring(
	ctx context.Context,
	directory *os.File,
	ignoredName string,
	ignoredStatus unix.Stat_t,
) ([]darwinResolverEntry, error) {
	if !isDarwinResolverTransactionName(ignoredName) {
		return nil, fmt.Errorf("Darwin resolver ignored transaction name %q is invalid", ignoredName)
	}
	return snapshotDarwinResolverDirectoryWithIgnored(ctx, directory, ignoredName, ignoredStatus, true)
}

// snapshotDarwinResolverDirectoryWithIgnored retains complete directory evidence around one proven transaction file.
func snapshotDarwinResolverDirectoryWithIgnored(
	ctx context.Context,
	directory *os.File,
	ignoredName string,
	ignoredStatus unix.Stat_t,
	ignore bool,
) ([]darwinResolverEntry, error) {
	beforeStatus, err := darwinResolverFileStatus(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect Darwin resolver directory before observation: %w", err)
	}
	before, err := listDarwinResolverDirectory(directory)
	if err != nil {
		return nil, err
	}
	entries := make([]darwinResolverEntry, 0, len(before))
	ignored := false
	for _, identity := range before {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ignore && identity.Name == ignoredName {
			if !sameDarwinResolverStatusSnapshot(ignoredStatus, identity.Status) {
				return nil, fmt.Errorf("Darwin resolver staged object changed before mutation")
			}
			ignored = true
			continue
		}
		entry, exists, err := readDarwinResolverEntry(directory, identity.Name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("Darwin resolver entry %q disappeared during observation", identity.Name)
		}
		entries = append(entries, entry)
	}
	if ignore && !ignored {
		return nil, fmt.Errorf("Darwin resolver staged object disappeared before mutation")
	}
	after, err := listDarwinResolverDirectory(directory)
	if err != nil {
		return nil, err
	}
	afterStatus, err := darwinResolverFileStatus(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect Darwin resolver directory after observation: %w", err)
	}
	if !sameDarwinResolverStatusSnapshot(beforeStatus, afterStatus) || !sameDarwinResolverDirectoryIdentities(before, after) {
		return nil, fmt.Errorf("Darwin resolver directory changed during observation")
	}
	if err := validateDarwinResolverDirectoryBinding(directory); err != nil {
		return nil, err
	}
	return entries, nil
}

// listDarwinResolverDirectory enumerates and stats every direct name through a fresh directory descriptor.
func listDarwinResolverDirectory(directory *os.File) (identities []darwinResolverDirectoryIdentity, err error) {
	descriptor, err := unix.Openat(
		int(directory.Fd()),
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: darwinResolverNativeDirectory, Err: err}
	}
	reader := os.NewFile(uintptr(descriptor), darwinResolverNativeDirectory)
	defer func() {
		err = errors.Join(err, reader.Close())
	}()
	directEntries, readErr := reader.ReadDir(maximumDarwinResolverEntries + 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("read Darwin resolver directory: %w", readErr)
	}
	if len(directEntries) > maximumDarwinResolverEntries {
		return nil, fmt.Errorf("Darwin resolver directory entries exceed limit %d", maximumDarwinResolverEntries)
	}
	identities = make([]darwinResolverDirectoryIdentity, 0, len(directEntries))
	for _, directEntry := range directEntries {
		status, exists, err := statDarwinResolverEntry(directory, directEntry.Name())
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("Darwin resolver entry %q disappeared during enumeration", directEntry.Name())
		}
		identities = append(identities, darwinResolverDirectoryIdentity{Name: directEntry.Name(), Status: status})
	}
	slices.SortFunc(identities, func(left darwinResolverDirectoryIdentity, right darwinResolverDirectoryIdentity) int {
		if left.Name < right.Name {
			return -1
		}
		if left.Name > right.Name {
			return 1
		}
		return 0
	})
	for index := 1; index < len(identities); index++ {
		if identities[index-1].Name == identities[index].Name {
			return nil, fmt.Errorf("Darwin resolver directory repeats entry %q", identities[index].Name)
		}
	}
	return identities, nil
}

// sameDarwinResolverDirectoryIdentities compares complete sorted enumeration records.
func sameDarwinResolverDirectoryIdentities(left []darwinResolverDirectoryIdentity, right []darwinResolverDirectoryIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Name != right[index].Name || !sameDarwinResolverStatusSnapshot(left[index].Status, right[index].Status) {
			return false
		}
	}
	return true
}

// lockDarwinResolverDirectory serializes Harbor snapshots and mutations on the retained directory.
func lockDarwinResolverDirectory(directory *os.File, operation int) error {
	if err := unix.Flock(int(directory.Fd()), operation|unix.LOCK_NB); err != nil {
		return fmt.Errorf("lock Darwin resolver directory: %w", err)
	}
	return nil
}

// unlockDarwinResolverDirectory releases a retained-directory advisory lock.
func unlockDarwinResolverDirectory(directory *os.File) error {
	if err := unix.Flock(int(directory.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("unlock Darwin resolver directory: %w", err)
	}
	return nil
}

// openDarwinResolverDirectory securely traverses /private/etc and optionally creates its resolver child.
func openDarwinResolverDirectory(create bool) (*os.File, bool, error) {
	etc, err := openDarwinResolverEtcDirectory()
	if err != nil {
		return nil, false, err
	}

	_, exists, err := statDarwinResolverEntry(etc, "resolver")
	if err != nil {
		return nil, false, errors.Join(err, etc.Close())
	}
	created := false
	var createdStatus unix.Stat_t
	if !exists && create {
		mkdirErr := unix.Mkdirat(int(etc.Fd()), "resolver", darwinResolverDirectoryMode)
		if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return nil, false, errors.Join(&os.PathError{Op: "mkdir", Path: darwinResolverNativeDirectory, Err: mkdirErr}, etc.Close())
		}
		created = mkdirErr == nil
		exists = true
		if created {
			createdStatus, exists, err = statDarwinResolverEntry(etc, "resolver")
			if err != nil || !exists {
				if err == nil {
					err = fmt.Errorf("created Darwin resolver directory disappeared before opening")
				}
				return nil, false, errors.Join(err, etc.Close())
			}
		}
	}
	if !exists {
		return nil, false, etc.Close()
	}
	directory, err := openDarwinResolverDirectDirectoryWithAccess(etc, "resolver", darwinResolverNativeDirectory, !created)
	if err == nil && created {
		openedStatus, statusErr := darwinResolverFileStatus(directory)
		if statusErr != nil {
			err = fmt.Errorf("inspect created Darwin resolver directory before securing: %w", statusErr)
		} else if !sameDarwinResolverStatusIdentity(createdStatus, openedStatus) {
			err = fmt.Errorf("created Darwin resolver directory changed before securing")
		} else if ownershipErr := unix.Fchown(int(directory.Fd()), 0, 0); ownershipErr != nil {
			err = fmt.Errorf("assign Darwin resolver directory ownership: %w", ownershipErr)
		} else if modeErr := unix.Fchmod(int(directory.Fd()), darwinResolverDirectoryMode); modeErr != nil {
			err = fmt.Errorf("set Darwin resolver directory mode: %w", modeErr)
		} else if accessErr := secureDarwinResolverCreatedAccess(directory); accessErr != nil {
			err = fmt.Errorf("secure Darwin resolver directory extended access: %w", accessErr)
		} else if status, statusErr := darwinResolverFileStatus(directory); statusErr != nil {
			err = fmt.Errorf("inspect created Darwin resolver directory: %w", statusErr)
		} else if statusErr := validateDarwinResolverNativeDirectoryStatus(status, darwinResolverNativeDirectory); statusErr != nil {
			err = statusErr
		} else if darwinResolverStatusMode(status) != darwinResolverDirectoryMode || uint32(status.Gid) != 0 {
			err = fmt.Errorf("created Darwin resolver directory owner/mode is not canonical")
		} else if syncErr := directory.Sync(); syncErr != nil {
			err = fmt.Errorf("sync Darwin resolver directory: %w", syncErr)
		} else if syncErr := etc.Sync(); syncErr != nil {
			err = fmt.Errorf("sync Darwin resolver parent directory: %w", syncErr)
		}
	}
	etcCloseErr := etc.Close()
	if err != nil || etcCloseErr != nil {
		if directory != nil {
			_ = directory.Close()
		}
		return nil, false, errors.Join(err, etcCloseErr)
	}
	return directory, true, nil
}

// openDarwinResolverEtcDirectory securely traverses and retains the physical parent of the resolver directory.
func openDarwinResolverEtcDirectory() (*os.File, error) {
	root, err := openDarwinResolverRoot()
	if err != nil {
		return nil, err
	}
	if err := validateDarwinResolverEtcAlias(root); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	private, err := openDarwinResolverDirectDirectory(root, "private", "/private")
	rootCloseErr := root.Close()
	if err != nil || rootCloseErr != nil {
		if private != nil {
			_ = private.Close()
		}
		return nil, errors.Join(err, rootCloseErr)
	}
	etc, err := openDarwinResolverDirectDirectory(private, "etc", "/private/etc")
	privateCloseErr := private.Close()
	if err != nil || privateCloseErr != nil {
		if etc != nil {
			_ = etc.Close()
		}
		return nil, errors.Join(err, privateCloseErr)
	}
	return etc, nil
}

// validateDarwinResolverDirectoryBinding proves a retained descriptor still names /private/etc/resolver.
func validateDarwinResolverDirectoryBinding(directory *os.File) (err error) {
	retainedStatus, err := darwinResolverFileStatus(directory)
	if err != nil {
		return fmt.Errorf("inspect retained Darwin resolver directory: %w", err)
	}
	etc, err := openDarwinResolverEtcDirectory()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, etc.Close())
	}()
	bound, err := openDarwinResolverDirectDirectory(etc, "resolver", darwinResolverNativeDirectory)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, bound.Close())
	}()
	boundStatus, err := darwinResolverFileStatus(bound)
	if err != nil {
		return fmt.Errorf("inspect bound Darwin resolver directory: %w", err)
	}
	if !sameDarwinResolverStatusIdentity(retainedStatus, boundStatus) {
		return fmt.Errorf("Darwin resolver directory was replaced after opening")
	}
	return nil
}

// validateDarwinResolverEtcAlias proves the public /etc path still names the fixed no-symlink /private/etc traversal.
func validateDarwinResolverEtcAlias(root *os.File) error {
	before, exists, err := statDarwinResolverEntry(root, "etc")
	if err != nil {
		return err
	}
	if !exists || darwinResolverStatusType(before) != unix.S_IFLNK || uint32(before.Uid) != 0 || uint64(before.Nlink) != 1 {
		return fmt.Errorf("Darwin resolver /etc alias is unsafe")
	}
	target := make([]byte, len(darwinResolverEtcAlias)+1)
	length, err := unix.Readlinkat(int(root.Fd()), "etc", target)
	if err != nil {
		return &os.PathError{Op: "readlink", Path: "/etc", Err: err}
	}
	after, exists, err := statDarwinResolverEntry(root, "etc")
	if err != nil {
		return err
	}
	if !exists || !sameDarwinResolverStatusIdentity(before, after) || length != len(darwinResolverEtcAlias) || string(target[:length]) != darwinResolverEtcAlias {
		return fmt.Errorf("Darwin resolver /etc alias changed or has an unexpected target")
	}
	return nil
}

// openDarwinResolverRoot starts fixed-path traversal from an unfollowed root descriptor.
func openDarwinResolverRoot() (*os.File, error) {
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: "/", Err: err}
	}
	root := os.NewFile(uintptr(descriptor), "/")
	status, err := darwinResolverFileStatus(root)
	if err == nil {
		err = validateDarwinResolverNativeDirectoryStatus(status, "/")
	}
	if err == nil {
		err = validateDarwinResolverExtendedAccess(root)
	}
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

// openDarwinResolverDirectDirectory retains one child only after no-follow identity comparison.
func openDarwinResolverDirectDirectory(parent *os.File, name string, path string) (*os.File, error) {
	return openDarwinResolverDirectDirectoryWithAccess(parent, name, path, true)
}

// openDarwinResolverDirectDirectoryWithAccess permits ACL removal only for a directory created by this transaction.
func openDarwinResolverDirectDirectoryWithAccess(parent *os.File, name string, path string, validateAccess bool) (*os.File, error) {
	expected, exists, err := statDarwinResolverEntry(parent, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &os.PathError{Op: "open", Path: path, Err: unix.ENOENT}
	}
	if err := validateDarwinResolverNativeDirectoryStatus(expected, path); err != nil {
		return nil, err
	}
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	directory := os.NewFile(uintptr(descriptor), path)
	actual, err := darwinResolverFileStatus(directory)
	if err == nil && !sameDarwinResolverStatusIdentity(expected, actual) {
		err = fmt.Errorf("Darwin resolver directory %q changed while opening", path)
	}
	if err == nil {
		err = validateDarwinResolverNativeDirectoryStatus(actual, path)
	}
	if err == nil && validateAccess {
		err = validateDarwinResolverExtendedAccess(directory)
	}
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

// openDarwinResolverPrivateQuarantineDirectory retains one transaction directory against its exact parent identity.
func openDarwinResolverPrivateQuarantineDirectory(
	parent *os.File,
	name string,
	expected unix.Stat_t,
) (*os.File, error) {
	if !isDarwinResolverQuarantineName(name) {
		return nil, fmt.Errorf("Darwin resolver private quarantine name %q is invalid", name)
	}
	parentStatus, err := darwinResolverFileStatus(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect Darwin resolver parent before opening private quarantine: %w", err)
	}
	if err := validateDarwinResolverPrivateQuarantineStatus(expected, parentStatus); err != nil {
		return nil, err
	}
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: parent.Name() + "/" + name, Err: err}
	}
	directory := os.NewFile(uintptr(descriptor), parent.Name()+"/"+name)
	actual, err := darwinResolverFileStatus(directory)
	if err == nil && !sameDarwinResolverStatusSnapshot(expected, actual) {
		err = fmt.Errorf("Darwin resolver private quarantine %q changed while opening", name)
	}
	if err == nil {
		err = validateDarwinResolverPrivateQuarantineStatus(actual, parentStatus)
	}
	if err == nil {
		err = validateDarwinResolverExtendedAccess(directory)
	}
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

// readDarwinResolverEntry opens and fully reads one direct regular file without following links.
func readDarwinResolverEntry(parent *os.File, name string) (entry darwinResolverEntry, exists bool, err error) {
	return readDarwinResolverEntryAs(parent, name, name)
}

// readDarwinResolverEntryAs reads one native name while attributing its facts to a fixed logical destination.
func readDarwinResolverEntryAs(
	parent *os.File,
	nativeName string,
	logicalName string,
) (entry darwinResolverEntry, exists bool, err error) {
	expected, exists, err := statDarwinResolverEntry(parent, nativeName)
	if err != nil || !exists {
		return darwinResolverEntry{}, exists, err
	}
	if err := validateDarwinResolverNativeFileStatus(expected); err != nil {
		return darwinResolverEntry{}, false, fmt.Errorf("unsafe Darwin resolver entry %q: %w", nativeName, err)
	}
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		nativeName,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return darwinResolverEntry{}, false, &os.PathError{Op: "open", Path: darwinResolverNativeDirectory + "/" + nativeName, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), darwinResolverNativeDirectory+"/"+nativeName)
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	opened, err := darwinResolverFileStatus(file)
	if err != nil {
		return darwinResolverEntry{}, false, err
	}
	if !sameDarwinResolverStatusSnapshot(expected, opened) {
		return darwinResolverEntry{}, false, fmt.Errorf("Darwin resolver entry %q changed while opening", nativeName)
	}
	if err := validateDarwinResolverNativeFileStatus(opened); err != nil {
		return darwinResolverEntry{}, false, err
	}
	if err := validateDarwinResolverExtendedAccess(file); err != nil {
		return darwinResolverEntry{}, false, err
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumDarwinResolverFileBytes+1))
	if err != nil {
		return darwinResolverEntry{}, false, fmt.Errorf("read Darwin resolver entry %q: %w", nativeName, err)
	}
	if len(content) > maximumDarwinResolverFileBytes {
		return darwinResolverEntry{}, false, fmt.Errorf("Darwin resolver entry %q exceeds %d bytes", nativeName, maximumDarwinResolverFileBytes)
	}
	completed, err := darwinResolverFileStatus(file)
	if err != nil {
		return darwinResolverEntry{}, false, err
	}
	if !sameDarwinResolverStatusSnapshot(opened, completed) || completed.Size != int64(len(content)) {
		return darwinResolverEntry{}, false, fmt.Errorf("Darwin resolver entry %q changed or was truncated while reading", nativeName)
	}
	if err := validateDarwinResolverExtendedAccess(file); err != nil {
		return darwinResolverEntry{}, false, err
	}
	current, exists, err := statDarwinResolverEntry(parent, nativeName)
	if err != nil {
		return darwinResolverEntry{}, false, err
	}
	if !exists || !sameDarwinResolverStatusSnapshot(completed, current) {
		return darwinResolverEntry{}, false, fmt.Errorf("Darwin resolver entry %q changed before observation completed", nativeName)
	}
	entry = darwinResolverEntry{
		Name:     logicalName,
		Content:  content,
		Metadata: darwinResolverMetadataFromStatus(completed),
	}
	if err := validateDarwinResolverEntry(entry); err != nil {
		return darwinResolverEntry{}, false, err
	}
	return entry, true, nil
}

// statDarwinResolverEntry inspects one direct child without following a symbolic link.
func statDarwinResolverEntry(parent *os.File, name string) (unix.Stat_t, bool, error) {
	var status unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &status, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return unix.Stat_t{}, false, nil
	}
	if err != nil {
		return unix.Stat_t{}, false, &os.PathError{Op: "stat", Path: parent.Name() + "/" + name, Err: err}
	}
	return status, true, nil
}

// createDarwinResolverStagingFile reserves a random direct name beside the fixed destination.
func createDarwinResolverStagingFile(parent *os.File) (string, *os.File, error) {
	for range darwinResolverStagingAttempts {
		name, err := newDarwinResolverStagingName()
		if err != nil {
			return "", nil, err
		}
		descriptor, err := unix.Openat(
			int(parent.Fd()),
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, &os.PathError{Op: "open", Path: darwinResolverNativeDirectory + "/" + name, Err: err}
		}
		return name, os.NewFile(uintptr(descriptor), darwinResolverNativeDirectory+"/"+name), nil
	}
	return "", nil, fmt.Errorf("Darwin resolver staging names were exhausted")
}

// newDarwinResolverStagingName returns an unpredictable bounded direct filename.
func newDarwinResolverStagingName() (string, error) {
	randomBytes := make([]byte, darwinResolverOrphanHexBytes)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return "", fmt.Errorf("generate Darwin resolver staging name: %w", err)
	}
	return ".harbor-resolver-" + hex.EncodeToString(randomBytes), nil
}

// newDarwinResolverQuarantineName returns an unpredictable root-only directory name for one destructive mutation.
func newDarwinResolverQuarantineName() (string, error) {
	randomBytes := make([]byte, darwinResolverOrphanHexBytes)
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return "", fmt.Errorf("generate Darwin resolver quarantine name: %w", err)
	}
	return darwinResolverQuarantinePrefix + hex.EncodeToString(randomBytes), nil
}

// createDarwinResolverPrivateQuarantine establishes a retained 0700 boundary owned like the validated resolver directory.
func createDarwinResolverPrivateQuarantine(parent *os.File) (*darwinResolverPrivateQuarantine, error) {
	parentStatus, err := darwinResolverFileStatus(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect Darwin resolver directory before quarantine creation: %w", err)
	}
	for range darwinResolverStagingAttempts {
		name, err := newDarwinResolverQuarantineName()
		if err != nil {
			return nil, err
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, darwinResolverQuarantineMode); errors.Is(err, unix.EEXIST) {
			continue
		} else if err != nil {
			return nil, &os.PathError{Op: "mkdir", Path: parent.Name() + "/" + name, Err: err}
		}

		created, exists, err := statDarwinResolverEntry(parent, name)
		if err != nil || !exists {
			if err == nil {
				err = fmt.Errorf("created Darwin resolver quarantine disappeared before opening")
			}
			return nil, err
		}
		descriptor, err := unix.Openat(
			int(parent.Fd()),
			name,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: parent.Name() + "/" + name, Err: err}
		}
		directory := os.NewFile(uintptr(descriptor), parent.Name()+"/"+name)
		opened, err := darwinResolverFileStatus(directory)
		if err == nil && !sameDarwinResolverStatusIdentity(created, opened) {
			err = fmt.Errorf("created Darwin resolver quarantine changed while opening")
		}
		if err == nil && (uint32(opened.Uid) != uint32(parentStatus.Uid) || uint32(opened.Gid) != uint32(parentStatus.Gid)) {
			err = unix.Fchown(int(directory.Fd()), int(parentStatus.Uid), int(parentStatus.Gid))
		}
		if err == nil {
			err = unix.Fchmod(int(directory.Fd()), darwinResolverQuarantineMode)
		}
		if err == nil {
			err = secureDarwinResolverCreatedAccess(directory)
		}
		if err == nil {
			opened, err = darwinResolverFileStatus(directory)
		}
		if err == nil {
			err = validateDarwinResolverPrivateQuarantineStatus(opened, parentStatus)
		}
		if err == nil {
			err = validateDarwinResolverExtendedAccess(directory)
		}
		if err == nil {
			err = directory.Sync()
		}
		if err == nil {
			err = parent.Sync()
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("secure Darwin resolver private quarantine: %w", err), directory.Close())
		}
		return &darwinResolverPrivateQuarantine{Name: name, Directory: directory, Status: opened}, nil
	}
	return nil, fmt.Errorf("Darwin resolver quarantine names were exhausted")
}

// validateDarwinResolverPrivateQuarantineStatus pins the mutation boundary to its validated parent's identity and mode.
func validateDarwinResolverPrivateQuarantineStatus(status unix.Stat_t, parentStatus unix.Stat_t) error {
	if darwinResolverStatusType(status) != unix.S_IFDIR {
		return fmt.Errorf("Darwin resolver private quarantine is not a directory")
	}
	if uint32(status.Uid) != uint32(parentStatus.Uid) || uint32(status.Gid) != uint32(parentStatus.Gid) ||
		darwinResolverStatusMode(status) != darwinResolverQuarantineMode {
		return fmt.Errorf("Darwin resolver private quarantine owner/mode is unsafe")
	}
	return nil
}

// captureDarwinResolverIdentity atomically moves one live name behind a retained private boundary before checking identity.
func captureDarwinResolverIdentity(
	parent *os.File,
	sourceName string,
	quarantineChildName string,
	expected unix.Stat_t,
	allowAbsent bool,
	syscalls darwinResolverMutationSyscalls,
) (*darwinResolverPrivateQuarantine, bool, error) {
	if sourceName != fixedDarwinResolverName && !isDarwinResolverOrphanName(sourceName) {
		return nil, false, fmt.Errorf("Darwin resolver capture source name %q is invalid", sourceName)
	}
	if quarantineChildName != fixedDarwinResolverName && !isDarwinResolverOrphanName(quarantineChildName) {
		return nil, false, fmt.Errorf("Darwin resolver private capture name %q is invalid", quarantineChildName)
	}
	if syscalls.rename == nil || syscalls.unlink == nil {
		panic("Darwin resolver identity capture requires complete mutation syscalls")
	}
	quarantine, err := createDarwinResolverPrivateQuarantine(parent)
	if err != nil {
		return nil, false, err
	}
	err = syscalls.rename(
		int(parent.Fd()),
		sourceName,
		int(quarantine.Directory.Fd()),
		quarantineChildName,
		unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY,
	)
	if errors.Is(err, unix.ENOENT) && allowAbsent {
		return nil, false, closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, syscalls)
	}
	if err != nil {
		cleanupErr := closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, syscalls)
		return nil, false, errors.Join(
			&os.PathError{Op: "quarantine", Path: parent.Name() + "/" + sourceName, Err: err},
			cleanupErr,
		)
	}

	captured, capturedExists, capturedErr := statDarwinResolverEntry(quarantine.Directory, quarantineChildName)
	_, sourceExists, sourceErr := statDarwinResolverEntry(parent, sourceName)
	if capturedErr == nil && (!capturedExists || !sameDarwinResolverStatusIdentity(expected, captured)) {
		capturedErr = fmt.Errorf("Darwin resolver quarantine captured a different identity")
	}
	if sourceErr == nil && sourceExists {
		sourceErr = fmt.Errorf("Darwin resolver source reappeared during quarantine")
	}
	if captureErr := errors.Join(capturedErr, sourceErr); captureErr != nil {
		var restoreErr error
		if capturedExists && !sourceExists {
			restoreErr = restoreDarwinResolverPrivateCapture(
				parent,
				quarantine,
				quarantineChildName,
				sourceName,
				captured,
				syscalls,
			)
		} else {
			restoreErr = retainDarwinResolverPrivateQuarantine(quarantine)
		}
		return nil, false, errors.Join(captureErr, restoreErr)
	}
	quarantineStatus, err := darwinResolverFileStatus(quarantine.Directory)
	if err != nil {
		return nil, false, errors.Join(
			fmt.Errorf("inspect Darwin resolver private quarantine after capture: %w", err),
			restoreDarwinResolverPrivateCapture(
				parent,
				quarantine,
				quarantineChildName,
				sourceName,
				captured,
				syscalls,
			),
		)
	}
	quarantine.Status = quarantineStatus
	return quarantine, true, nil
}

// restoreDarwinResolverPrivateCapture uses no-replace publication and verifies both names after restoration.
func restoreDarwinResolverPrivateCapture(
	parent *os.File,
	quarantine *darwinResolverPrivateQuarantine,
	quarantineChildName string,
	destinationName string,
	expected unix.Stat_t,
	syscalls darwinResolverMutationSyscalls,
) error {
	current, exists, err := statDarwinResolverEntry(quarantine.Directory, quarantineChildName)
	if err != nil || !exists || !sameDarwinResolverStatusIdentity(expected, current) {
		if err == nil {
			err = fmt.Errorf("Darwin resolver private capture changed before restoration")
		}
		return errors.Join(err, retainDarwinResolverPrivateQuarantine(quarantine))
	}
	err = syscalls.rename(
		int(quarantine.Directory.Fd()),
		quarantineChildName,
		int(parent.Fd()),
		destinationName,
		unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY,
	)
	if err != nil {
		return errors.Join(
			&os.PathError{Op: "restore", Path: parent.Name() + "/" + destinationName, Err: err},
			retainDarwinResolverPrivateQuarantine(quarantine),
		)
	}
	restored, restoredExists, restoredErr := statDarwinResolverEntry(parent, destinationName)
	_, capturedExists, capturedErr := statDarwinResolverEntry(quarantine.Directory, quarantineChildName)
	if restoredErr == nil && (!restoredExists || !sameDarwinResolverStatusIdentity(expected, restored)) {
		restoredErr = fmt.Errorf("restored Darwin resolver identity changed after publication")
	}
	if capturedErr == nil && capturedExists {
		capturedErr = fmt.Errorf("restored Darwin resolver private capture still exists")
	}
	if err := errors.Join(restoredErr, capturedErr); err != nil {
		return errors.Join(err, retainDarwinResolverPrivateQuarantine(quarantine))
	}
	return closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, syscalls)
}

// deleteDarwinResolverPrivateCapture unlinks only inside a retained root-only boundary after exact identity verification.
func deleteDarwinResolverPrivateCapture(
	parent *os.File,
	quarantine *darwinResolverPrivateQuarantine,
	quarantineChildName string,
	expected unix.Stat_t,
	syscalls darwinResolverMutationSyscalls,
) error {
	current, exists, err := statDarwinResolverEntry(quarantine.Directory, quarantineChildName)
	if err != nil || !exists || !sameDarwinResolverStatusIdentity(expected, current) {
		if err == nil {
			err = fmt.Errorf("Darwin resolver private capture changed before deletion")
		}
		return err
	}
	// A root-only 0700 directory is the strongest available Darwin fence because unlinkat has no identity-conditional form.
	// A malicious concurrent root writer inside this retained boundary is outside Harbor's local privilege threat model.
	if err := syscalls.unlink(int(quarantine.Directory.Fd()), quarantineChildName, 0); err != nil {
		return &os.PathError{Op: "remove", Path: quarantine.Directory.Name() + "/" + quarantineChildName, Err: err}
	}
	if err := quarantine.Directory.Sync(); err != nil {
		return fmt.Errorf("sync Darwin resolver private quarantine: %w", err)
	}
	return closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, syscalls)
}

// deleteDarwinResolverPrivateCaptureOrRestore preserves the captured identity whenever private deletion cannot finish.
func deleteDarwinResolverPrivateCaptureOrRestore(
	parent *os.File,
	quarantine *darwinResolverPrivateQuarantine,
	quarantineChildName string,
	expected unix.Stat_t,
	restoreName string,
	syscalls darwinResolverMutationSyscalls,
) error {
	deleteErr := deleteDarwinResolverPrivateCapture(parent, quarantine, quarantineChildName, expected, syscalls)
	if deleteErr == nil {
		return nil
	}
	current, exists, statusErr := statDarwinResolverEntry(quarantine.Directory, quarantineChildName)
	if statusErr != nil {
		return errors.Join(deleteErr, statusErr)
	}
	if !exists {
		return errors.Join(deleteErr, closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, syscalls))
	}
	return errors.Join(deleteErr, restoreDarwinResolverPrivateCapture(
		parent,
		quarantine,
		quarantineChildName,
		restoreName,
		current,
		syscalls,
	))
}

// removeDarwinResolverIdentity captures, verifies, and deletes one exact name without unlinking in the shared directory.
func removeDarwinResolverIdentity(
	parent *os.File,
	sourceName string,
	quarantineChildName string,
	expected unix.Stat_t,
	allowAbsent bool,
	syscalls darwinResolverMutationSyscalls,
) error {
	quarantine, captured, err := captureDarwinResolverIdentity(
		parent,
		sourceName,
		quarantineChildName,
		expected,
		allowAbsent,
		syscalls,
	)
	if err != nil || !captured {
		return err
	}
	return deleteDarwinResolverPrivateCaptureOrRestore(
		parent,
		quarantine,
		quarantineChildName,
		expected,
		sourceName,
		syscalls,
	)
}

// closeEmptyDarwinResolverPrivateQuarantine removes only the still-bound empty root-only transaction directory.
func closeEmptyDarwinResolverPrivateQuarantine(
	parent *os.File,
	quarantine *darwinResolverPrivateQuarantine,
	syscalls darwinResolverMutationSyscalls,
) error {
	bound, exists, err := statDarwinResolverEntry(parent, quarantine.Name)
	if err != nil || !exists || !sameDarwinResolverStatusIdentity(quarantine.Status, bound) {
		if err == nil {
			err = fmt.Errorf("Darwin resolver private quarantine changed before removal")
		}
		return errors.Join(err, quarantine.Directory.Close())
	}
	// The validated shared parent is root-owned and non-writable to local users; only an out-of-model root writer can race rmdir.
	if err := syscalls.unlink(int(parent.Fd()), quarantine.Name, unix.AT_REMOVEDIR); err != nil {
		return errors.Join(
			&os.PathError{Op: "remove", Path: parent.Name() + "/" + quarantine.Name, Err: err},
			quarantine.Directory.Close(),
		)
	}
	closeErr := quarantine.Directory.Close()
	syncErr := parent.Sync()
	if syncErr != nil {
		syncErr = fmt.Errorf("sync Darwin resolver directory after quarantine removal: %w", syncErr)
	}
	return errors.Join(closeErr, syncErr)
}

// retainDarwinResolverPrivateQuarantine syncs and closes an ambiguous capture without mutating its names.
func retainDarwinResolverPrivateQuarantine(quarantine *darwinResolverPrivateQuarantine) error {
	return errors.Join(
		fmt.Errorf("retained ambiguous Darwin resolver private quarantine %q", quarantine.Name),
		quarantine.Directory.Sync(),
		quarantine.Directory.Close(),
	)
}

// recoverDarwinResolverOrphans removes only stable root-owned files in Harbor's private transaction namespace.
func recoverDarwinResolverOrphans(ctx context.Context, parent *os.File) error {
	identities, err := listDarwinResolverDirectory(parent)
	if err != nil {
		return err
	}
	names := make([]string, len(identities))
	for index, identity := range identities {
		names[index] = identity.Name
	}
	quarantines, err := darwinResolverQuarantineNames(names)
	if err != nil {
		return err
	}
	for _, name := range quarantines {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := recoverDarwinResolverPrivateQuarantine(parent, name); err != nil {
			return err
		}
	}
	if len(quarantines) != 0 {
		identities, err = listDarwinResolverDirectory(parent)
		if err != nil {
			return err
		}
		names = make([]string, len(identities))
		for index, identity := range identities {
			names[index] = identity.Name
		}
	}
	orphans, err := darwinResolverOrphanNames(names)
	if err != nil {
		return err
	}
	for _, name := range orphans {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := recoverDarwinResolverOrphan(parent, name); err != nil {
			return err
		}
	}
	return nil
}

// recoverDarwinResolverPrivateQuarantine restores an interrupted capture without guessing whether deletion was authorized.
func recoverDarwinResolverPrivateQuarantine(parent *os.File, name string) error {
	if !isDarwinResolverQuarantineName(name) {
		return fmt.Errorf("Darwin resolver private quarantine name %q is invalid", name)
	}
	status, exists, err := statDarwinResolverEntry(parent, name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Darwin resolver private quarantine %q disappeared before recovery", name)
	}
	directory, err := openDarwinResolverPrivateQuarantineDirectory(parent, name, status)
	if err != nil {
		return err
	}
	quarantine := &darwinResolverPrivateQuarantine{Name: name, Directory: directory, Status: status}
	identities, err := listDarwinResolverDirectory(directory)
	if err != nil {
		return errors.Join(err, retainDarwinResolverPrivateQuarantine(quarantine))
	}
	if len(identities) == 0 {
		return closeEmptyDarwinResolverPrivateQuarantine(parent, quarantine, darwinResolverNativeMutationSyscalls)
	}
	if len(identities) != 1 {
		return errors.Join(
			fmt.Errorf("Darwin resolver private quarantine %q contains %d entries", name, len(identities)),
			retainDarwinResolverPrivateQuarantine(quarantine),
		)
	}
	child := identities[0]
	if child.Name != fixedDarwinResolverName && !isDarwinResolverOrphanName(child.Name) {
		return errors.Join(
			fmt.Errorf("Darwin resolver private quarantine %q contains invalid name %q", name, child.Name),
			retainDarwinResolverPrivateQuarantine(quarantine),
		)
	}
	if err := validateDarwinResolverNativeFileStatus(child.Status); err != nil {
		return errors.Join(err, retainDarwinResolverPrivateQuarantine(quarantine))
	}
	return restoreDarwinResolverPrivateCapture(
		parent,
		quarantine,
		child.Name,
		child.Name,
		child.Status,
		darwinResolverNativeMutationSyscalls,
	)
}

// recoverDarwinResolverOrphan quarantines and revalidates an orphan before deleting the captured object.
func recoverDarwinResolverOrphan(parent *os.File, name string) error {
	entry, exists, err := readDarwinResolverEntryAs(parent, name, fixedDarwinResolverName)
	if err != nil {
		return fmt.Errorf("inspect Darwin resolver transaction orphan %q: %w", name, err)
	}
	if !exists {
		return fmt.Errorf("Darwin resolver transaction orphan %q disappeared before recovery", name)
	}
	guard, err := darwinResolverOrphanGuard(name, entry)
	if err != nil {
		return fmt.Errorf("admit Darwin resolver transaction orphan %q: %w", name, err)
	}
	expected, exists, err := statDarwinResolverEntry(parent, name)
	if err != nil {
		return err
	}
	if !exists || uint64(expected.Dev) != guard.Device || uint64(expected.Ino) != guard.Inode || uint32(expected.Gen) != guard.Generation {
		return fmt.Errorf("Darwin resolver transaction orphan %q changed before atomic recovery capture", name)
	}
	quarantine, captured, err := captureDarwinResolverIdentity(
		parent,
		name,
		name,
		expected,
		false,
		darwinResolverNativeMutationSyscalls,
	)
	if err != nil {
		return err
	}
	if !captured {
		return fmt.Errorf("Darwin resolver transaction orphan %q disappeared before atomic recovery capture", name)
	}
	quarantined, quarantinedExists, quarantineErr := readDarwinResolverEntryAs(
		quarantine.Directory,
		name,
		fixedDarwinResolverName,
	)
	if quarantineErr == nil {
		quarantineErr = matchDarwinResolverGuard(guard, quarantined, quarantinedExists)
	}
	if quarantineErr != nil {
		return errors.Join(quarantineErr, restoreDarwinResolverPrivateCapture(
			parent,
			quarantine,
			name,
			name,
			expected,
			darwinResolverNativeMutationSyscalls,
		))
	}
	if err := deleteDarwinResolverPrivateCaptureOrRestore(
		parent,
		quarantine,
		name,
		expected,
		name,
		darwinResolverNativeMutationSyscalls,
	); err != nil {
		return err
	}
	return nil
}

// publishDarwinResolverStaging creates an absent destination or atomically swaps an existing one without an unbound-name window.
func publishDarwinResolverStaging(
	parent *os.File,
	stagingName string,
	stagedStatus unix.Stat_t,
	guard darwinResolverGuard,
	syscalls darwinResolverMutationSyscalls,
) (darwinResolverPublication, error) {
	publication := darwinResolverPublication{Replaced: guard.Exists}
	if !isDarwinResolverOrphanName(stagingName) {
		return publication, fmt.Errorf("Darwin resolver staging name %q is invalid", stagingName)
	}
	if syscalls.rename == nil {
		panic("Darwin resolver publication requires a rename syscall")
	}
	stagingCurrent, stagingExists, err := statDarwinResolverEntry(parent, stagingName)
	if err != nil {
		return publication, err
	}
	if !stagingExists || !sameDarwinResolverStatusSnapshot(stagedStatus, stagingCurrent) {
		return publication, fmt.Errorf("staged Darwin resolver identity changed before publication")
	}
	destination, destinationExists, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil {
		return publication, err
	}
	if guard.Exists {
		if !destinationExists || uint64(destination.Dev) != guard.Device || uint64(destination.Ino) != guard.Inode ||
			uint32(destination.Gen) != guard.Generation {
			return publication, fmt.Errorf("Darwin resolver destination changed before atomic swap")
		}
	} else if destinationExists {
		return publication, fmt.Errorf("Darwin resolver destination appeared before atomic publication")
	}

	flags := uint32(unix.RENAME_EXCL | unix.RENAME_NOFOLLOW_ANY)
	if publication.Replaced {
		flags = uint32(unix.RENAME_SWAP | unix.RENAME_NOFOLLOW_ANY)
	}
	if err := syscalls.rename(
		int(parent.Fd()),
		stagingName,
		int(parent.Fd()),
		fixedDarwinResolverName,
		flags,
	); err != nil {
		return publication, &os.PathError{Op: "publish", Path: parent.Name() + "/" + fixedDarwinResolverName, Err: err}
	}
	publication.Published = true
	published, publishedExists, publishedErr := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if publishedErr == nil && (!publishedExists || !sameDarwinResolverStatusIdentity(stagedStatus, published)) {
		publishedErr = fmt.Errorf("atomic Darwin resolver publication moved an unexpected identity")
	}
	if publishedErr == nil {
		publication.PublishedStatus = published
	}
	displaced, displacedExists, displacedErr := statDarwinResolverEntry(parent, stagingName)
	if publication.Replaced {
		if displacedErr == nil && (!displacedExists || !sameDarwinResolverStatusIdentity(destination, displaced)) {
			displacedErr = fmt.Errorf("atomic Darwin resolver publication displaced an unexpected identity")
		}
		if displacedErr == nil {
			publication.DisplacedStatus = displaced
		}
	} else if displacedErr == nil && displacedExists {
		displacedErr = fmt.Errorf("atomic Darwin resolver publication retained its staging name")
	}
	if err := errors.Join(publishedErr, displacedErr); err != nil {
		return publication, err
	}
	return publication, nil
}

// rollbackDarwinResolverPublication atomically restores the verified pre-publication names without deleting either identity.
func rollbackDarwinResolverPublication(
	parent *os.File,
	stagingName string,
	stagedStatus unix.Stat_t,
	publication darwinResolverPublication,
	syscalls darwinResolverMutationSyscalls,
) error {
	if !publication.Published {
		return fmt.Errorf("Darwin resolver rollback requires a completed publication")
	}
	if !isDarwinResolverOrphanName(stagingName) {
		return fmt.Errorf("Darwin resolver staging name %q is invalid", stagingName)
	}
	if syscalls.rename == nil {
		panic("Darwin resolver rollback requires a rename syscall")
	}
	published, publishedExists, err := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if err != nil {
		return err
	}
	if !publishedExists || !sameDarwinResolverStatusSnapshot(publication.PublishedStatus, published) {
		return fmt.Errorf("published Darwin resolver identity changed before rollback")
	}
	staging, stagingExists, err := statDarwinResolverEntry(parent, stagingName)
	if err != nil {
		return err
	}
	if publication.Replaced {
		if !stagingExists || !sameDarwinResolverStatusSnapshot(publication.DisplacedStatus, staging) {
			return fmt.Errorf("displaced Darwin resolver identity changed before rollback")
		}
		if err := syscalls.rename(
			int(parent.Fd()),
			stagingName,
			int(parent.Fd()),
			fixedDarwinResolverName,
			uint32(unix.RENAME_SWAP|unix.RENAME_NOFOLLOW_ANY),
		); err != nil {
			return &os.PathError{Op: "rollback", Path: parent.Name() + "/" + fixedDarwinResolverName, Err: err}
		}
	} else {
		if stagingExists {
			return fmt.Errorf("Darwin resolver staging name appeared before rollback")
		}
		if err := syscalls.rename(
			int(parent.Fd()),
			fixedDarwinResolverName,
			int(parent.Fd()),
			stagingName,
			uint32(unix.RENAME_EXCL|unix.RENAME_NOFOLLOW_ANY),
		); err != nil {
			return &os.PathError{Op: "rollback", Path: parent.Name() + "/" + stagingName, Err: err}
		}
	}
	restored, restoredExists, restoredErr := statDarwinResolverEntry(parent, stagingName)
	if restoredErr == nil && (!restoredExists || !sameDarwinResolverStatusIdentity(stagedStatus, restored)) {
		restoredErr = fmt.Errorf("staged Darwin resolver identity changed during rollback")
	}
	destination, destinationExists, destinationErr := statDarwinResolverEntry(parent, fixedDarwinResolverName)
	if publication.Replaced {
		if destinationErr == nil && (!destinationExists ||
			!sameDarwinResolverStatusIdentity(publication.DisplacedStatus, destination)) {
			destinationErr = fmt.Errorf("displaced Darwin resolver identity was not restored during rollback")
		}
	} else if destinationErr == nil && destinationExists {
		destinationErr = fmt.Errorf("absent Darwin resolver destination reappeared during rollback")
	}
	if err := errors.Join(restoredErr, destinationErr); err != nil {
		return err
	}
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync Darwin resolver rollback: %w", err)
	}
	return nil
}

// darwinResolverFileStatus returns native metadata for one retained object descriptor.
func darwinResolverFileStatus(file *os.File) (unix.Stat_t, error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return unix.Stat_t{}, err
	}
	return status, nil
}

// darwinResolverMetadataFromStatus retains every native field used by ownership, exactness, and mutation guards.
func darwinResolverMetadataFromStatus(status unix.Stat_t) darwinResolverMetadata {
	return darwinResolverMetadata{
		Regular:    darwinResolverStatusType(status) == unix.S_IFREG,
		Device:     uint64(status.Dev),
		Inode:      uint64(status.Ino),
		Generation: uint32(status.Gen),
		UID:        uint32(status.Uid),
		GID:        uint32(status.Gid),
		Mode:       darwinResolverStatusMode(status),
		Flags:      uint32(status.Flags),
		LinkCount:  uint64(status.Nlink),
	}
}

// validateDarwinResolverNativeDirectoryStatus rejects replaceable ancestors and non-directory objects.
func validateDarwinResolverNativeDirectoryStatus(status unix.Stat_t, path string) error {
	mode := darwinResolverStatusMode(status)
	if darwinResolverStatusType(status) != unix.S_IFDIR {
		return fmt.Errorf("Darwin resolver ancestor %q is not a directory", path)
	}
	if uint32(status.Uid) != 0 || mode&0o7022 != 0 {
		return fmt.Errorf("Darwin resolver ancestor %q owner/mode is unsafe", path)
	}
	return nil
}

// validateDarwinResolverNativeFileStatus rejects links, special objects, unsafe owners, modes, and sizes.
func validateDarwinResolverNativeFileStatus(status unix.Stat_t) error {
	mode := darwinResolverStatusMode(status)
	if darwinResolverStatusType(status) != unix.S_IFREG {
		return fmt.Errorf("Darwin resolver entry is not a regular file")
	}
	if uint64(status.Nlink) != 1 {
		return fmt.Errorf("Darwin resolver entry link count is %d, want 1", status.Nlink)
	}
	if uint32(status.Uid) != 0 || mode&0o7022 != 0 || mode&0o400 == 0 {
		return fmt.Errorf("Darwin resolver entry owner/mode is unsafe")
	}
	if status.Size < 0 || status.Size > maximumDarwinResolverFileBytes {
		return fmt.Errorf("Darwin resolver entry size %d is invalid", status.Size)
	}
	return nil
}

// validateDarwinResolverExtendedAccess rejects macOS ACLs that can grant rights beyond Unix mode bits.
func validateDarwinResolverExtendedAccess(file *os.File) error {
	present, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect Darwin resolver extended access: %w", err)
	}
	if present {
		return fmt.Errorf("Darwin resolver object has an access control list")
	}
	return nil
}

// secureDarwinResolverCreatedAccess removes only an ACL inherited by an object this transaction created.
func secureDarwinResolverCreatedAccess(file *os.File) error {
	return secureDarwinResolverCreatedAccessWith(file, darwinacl.Present, darwinacl.Remove)
}

// secureDarwinResolverCreatedAccessWith preserves the inspect-remove-verify sequence under deterministic tests.
func secureDarwinResolverCreatedAccessWith(
	file *os.File,
	present func(*os.File) (bool, error),
	remove func(*os.File) error,
) error {
	hasAccessControlList, err := present(file)
	if err != nil {
		return fmt.Errorf("inspect inherited Darwin resolver access control list: %w", err)
	}
	if !hasAccessControlList {
		return nil
	}
	if err := remove(file); err != nil {
		return fmt.Errorf("remove inherited Darwin resolver access control list: %w", err)
	}
	hasAccessControlList, err = present(file)
	if err != nil {
		return fmt.Errorf("reinspect Darwin resolver access control list after removal: %w", err)
	}
	if hasAccessControlList {
		return fmt.Errorf("Darwin resolver object retains an access control list after removal")
	}
	return nil
}

// darwinResolverStatusType extracts the native file type independently from permission bits.
func darwinResolverStatusType(status unix.Stat_t) uint32 {
	return uint32(status.Mode) & uint32(unix.S_IFMT)
}

// darwinResolverStatusMode extracts permission and special bits for exact security checks.
func darwinResolverStatusMode(status unix.Stat_t) uint32 {
	return uint32(status.Mode) & 0o7777
}

// sameDarwinResolverStatusIdentity compares the stable device and inode identity of two native objects.
func sameDarwinResolverStatusIdentity(left unix.Stat_t, right unix.Stat_t) bool {
	return uint64(left.Dev) == uint64(right.Dev) &&
		uint64(left.Ino) == uint64(right.Ino) &&
		uint32(left.Gen) == uint32(right.Gen)
}

// sameDarwinResolverStatusSnapshot compares identity and every mutation-relevant native stat field.
func sameDarwinResolverStatusSnapshot(left unix.Stat_t, right unix.Stat_t) bool {
	return sameDarwinResolverStatusIdentity(left, right) &&
		uint16(left.Mode) == uint16(right.Mode) &&
		uint16(left.Nlink) == uint16(right.Nlink) &&
		uint32(left.Uid) == uint32(right.Uid) &&
		uint32(left.Gid) == uint32(right.Gid) &&
		left.Size == right.Size &&
		left.Mtim.Sec == right.Mtim.Sec &&
		left.Mtim.Nsec == right.Mtim.Nsec &&
		left.Ctim.Sec == right.Ctim.Sec &&
		left.Ctim.Nsec == right.Ctim.Nsec &&
		uint32(left.Flags) == uint32(right.Flags) &&
		uint32(left.Gen) == uint32(right.Gen)
}
